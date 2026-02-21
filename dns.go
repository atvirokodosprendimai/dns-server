package main

import (
	"context"
	"fmt"
	mrand "math/rand"
	"net"
	"sort"
	"time"

	"github.com/miekg/dns"
)

func (s *server) runDNS(ctx context.Context, network string) error {
	addr := s.cfg.DNSUDPListen
	if network == "tcp" {
		addr = s.cfg.DNSTCPListen
	}

	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleDNS)

	dnsServer := &dns.Server{Addr: addr, Net: network, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = dnsServer.ShutdownContext(context.Background())
	}()

	if err := dnsServer.ListenAndServe(); err != nil {
		return fmt.Errorf("dns/%s listen: %w", network, err)
	}
	return nil
}

func (s *server) handleDNS(w dns.ResponseWriter, req *dns.Msg) {
	resp := s.resolveDNS(req)
	_ = w.WriteMsg(resp)
}

func (s *server) resolveDNS(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true

	for _, q := range req.Question {
		name := normalizeName(q.Name)

		switch q.Qtype {
		case dns.TypeA, dns.TypeANY:
			aAnswers := make([]dns.RR, 0, 4)
			hasDirectAnswer := false
			for _, rec := range s.data.getRecords(name, q.Qtype) {
				if rec.Type == "A" {
					hasDirectAnswer = true
					rr := &dns.A{
						Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: rec.TTL},
						A:   net.ParseIP(rec.IP).To4(),
					}
					if rr.A != nil {
						aAnswers = append(aAnswers, rr)
					}
				}
				if rec.Type == "AAAA" && q.Qtype == dns.TypeANY {
					ip := net.ParseIP(rec.IP)
					if ip != nil && ip.To4() == nil {
						resp.Answer = append(resp.Answer, &dns.AAAA{
							Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: rec.TTL},
							AAAA: ip,
						})
					}
				}
				if rec.Type == "TXT" && q.Qtype == dns.TypeANY {
					hasDirectAnswer = true
					resp.Answer = append(resp.Answer, &dns.TXT{
						Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: rec.TTL},
						Txt: chunkTXT(rec.Text),
					})
				}
				if rec.Type == "CNAME" && q.Qtype == dns.TypeANY {
					hasDirectAnswer = true
					resp.Answer = append(resp.Answer, &dns.CNAME{
						Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: rec.TTL},
						Target: normalizeName(rec.Target),
					})
				}
				if rec.Type == "MX" && q.Qtype == dns.TypeANY {
					hasDirectAnswer = true
					resp.Answer = append(resp.Answer, &dns.MX{
						Hdr:        dns.RR_Header{Name: name, Rrtype: dns.TypeMX, Class: dns.ClassINET, Ttl: rec.TTL},
						Mx:         normalizeName(rec.Target),
						Preference: rec.Priority,
					})
				}
			}
			shuffleRR(aAnswers)
			resp.Answer = append(resp.Answer, aAnswers...)
			if q.Qtype == dns.TypeA && !hasDirectAnswer {
				for _, rec := range s.data.getRecords(name, dns.TypeCNAME) {
					resp.Answer = append(resp.Answer, &dns.CNAME{
						Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: rec.TTL},
						Target: normalizeName(rec.Target),
					})
				}
			}
		case dns.TypeAAAA:
			aaaaAnswers := make([]dns.RR, 0, 4)
			hasDirectAnswer := false
			for _, rec := range s.data.getRecords(name, q.Qtype) {
				ip := net.ParseIP(rec.IP)
				if ip == nil || ip.To4() != nil {
					continue
				}
				hasDirectAnswer = true
				aaaaAnswers = append(aaaaAnswers, &dns.AAAA{
					Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: rec.TTL},
					AAAA: ip,
				})
			}
			shuffleRR(aaaaAnswers)
			resp.Answer = append(resp.Answer, aaaaAnswers...)
			if !hasDirectAnswer {
				for _, rec := range s.data.getRecords(name, dns.TypeCNAME) {
					resp.Answer = append(resp.Answer, &dns.CNAME{
						Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: rec.TTL},
						Target: normalizeName(rec.Target),
					})
				}
			}
		case dns.TypeTXT:
			hasDirectAnswer := false
			for _, rec := range s.data.getRecords(name, q.Qtype) {
				hasDirectAnswer = true
				resp.Answer = append(resp.Answer, &dns.TXT{
					Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: rec.TTL},
					Txt: chunkTXT(rec.Text),
				})
			}
			if !hasDirectAnswer {
				for _, rec := range s.data.getRecords(name, dns.TypeCNAME) {
					resp.Answer = append(resp.Answer, &dns.CNAME{
						Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: rec.TTL},
						Target: normalizeName(rec.Target),
					})
				}
			}
		case dns.TypeCNAME:
			for _, rec := range s.data.getRecords(name, q.Qtype) {
				resp.Answer = append(resp.Answer, &dns.CNAME{
					Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: rec.TTL},
					Target: normalizeName(rec.Target),
				})
			}
		case dns.TypeMX:
			mxAnswers := make([]*dns.MX, 0, 4)
			for _, rec := range s.data.getRecords(name, q.Qtype) {
				mxAnswers = append(mxAnswers, &dns.MX{
					Hdr:        dns.RR_Header{Name: name, Rrtype: dns.TypeMX, Class: dns.ClassINET, Ttl: rec.TTL},
					Mx:         normalizeName(rec.Target),
					Preference: rec.Priority,
				})
			}
			sort.SliceStable(mxAnswers, func(i, j int) bool {
				if mxAnswers[i].Preference == mxAnswers[j].Preference {
					return mxAnswers[i].Mx < mxAnswers[j].Mx
				}
				return mxAnswers[i].Preference < mxAnswers[j].Preference
			})
			for _, rr := range mxAnswers {
				resp.Answer = append(resp.Answer, rr)
			}
		case dns.TypeNS:
			if zone, ok := s.data.getZone(name); ok {
				for _, ns := range zone.NS {
					resp.Answer = append(resp.Answer, &dns.NS{
						Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: zone.SOATTL},
						Ns:  ns,
					})
				}
			}
		case dns.TypeSOA:
			if zone, ok := s.data.bestZone(name); ok {
				resp.Answer = append(resp.Answer, soaForZone(zone))
			}
		}
	}

	if len(resp.Answer) == 0 {
		firstQ := "."
		firstType := dns.TypeNone
		if len(req.Question) > 0 {
			firstQ = normalizeName(req.Question[0].Name)
			firstType = req.Question[0].Qtype
		}

		if zone, ok := s.data.bestZone(firstQ); ok {
			if s.data.hasName(firstQ) && (firstType == dns.TypeA || firstType == dns.TypeAAAA || firstType == dns.TypeTXT || firstType == dns.TypeCNAME || firstType == dns.TypeMX || firstType == dns.TypeANY) {
				resp.Rcode = dns.RcodeSuccess
				resp.Ns = append(resp.Ns, soaForZone(zone))
			} else {
				resp.Rcode = dns.RcodeNameError
				resp.Ns = append(resp.Ns, soaForZone(zone))
			}
		} else {
			resp.Rcode = dns.RcodeRefused
		}
	}

	return resp
}

func shuffleRR(records []dns.RR) {
	if len(records) < 2 {
		return
	}
	r := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(records), func(i, j int) { records[i], records[j] = records[j], records[i] })
}

func chunkTXT(v string) []string {
	if v == "" {
		return []string{""}
	}
	out := make([]string, 0, (len(v)/255)+1)
	for len(v) > 255 {
		out = append(out, v[:255])
		v = v[255:]
	}
	out = append(out, v)
	return out
}

func soaForZone(z zoneConfig) dns.RR {
	mname := z.Zone
	if len(z.NS) > 0 {
		mname = z.NS[0]
	}

	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: z.Zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: z.SOATTL},
		Ns:      mname,
		Mbox:    "hostmaster." + z.Zone,
		Serial:  z.Serial,
		Refresh: 30,
		Retry:   30,
		Expire:  300,
		Minttl:  z.SOATTL,
	}
}
