package main

import (
	"context"
	"fmt"
	"net"

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
			if rec, ok := s.data.getRecord(name); ok {
				rr := &dns.A{
					Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: rec.TTL},
					A:   net.ParseIP(rec.IP).To4(),
				}
				if rr.A != nil {
					resp.Answer = append(resp.Answer, rr)
				}
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
		if len(req.Question) > 0 {
			firstQ = normalizeName(req.Question[0].Name)
		}

		if zone, ok := s.data.bestZone(firstQ); ok {
			resp.Rcode = dns.RcodeNameError
			resp.Ns = append(resp.Ns, soaForZone(zone))
		} else {
			resp.Rcode = dns.RcodeRefused
		}
	}

	return resp
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
