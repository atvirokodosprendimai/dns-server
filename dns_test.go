package main

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestResolveDNSARecord(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.data.upsertZone(zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 60, Serial: 1, UpdatedAt: now})
	s.data.setRecord(aRecord{Name: "app.example.com", Zone: "example.com", IP: "198.51.100.10", TTL: 25, Version: 1, UpdatedAt: now})

	req := new(dns.Msg)
	req.SetQuestion("app.example.com.", dns.TypeA)

	resp := s.resolveDNS(req)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected success rcode, got %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected one answer, got %d", len(resp.Answer))
	}

	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A answer, got %T", resp.Answer[0])
	}
	if a.A.String() != "198.51.100.10" {
		t.Fatalf("unexpected A IP: %s", a.A.String())
	}
}

func TestResolveDNSNXDOMAINInsideManagedZone(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.data.upsertZone(zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 60, Serial: 1, UpdatedAt: now})

	req := new(dns.Msg)
	req.SetQuestion("missing.example.com.", dns.TypeA)

	resp := s.resolveDNS(req)
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN, got %d", resp.Rcode)
	}
	if len(resp.Ns) == 0 {
		t.Fatal("expected SOA in authority section")
	}
}

func TestResolveDNSRefusedOutsideManagedZones(t *testing.T) {
	s := newTestServer(t)
	req := new(dns.Msg)
	req.SetQuestion("example.net.", dns.TypeA)

	resp := s.resolveDNS(req)
	if resp.Rcode != dns.RcodeRefused {
		t.Fatalf("expected REFUSED, got %d", resp.Rcode)
	}
}

func TestResolveDNSAAAARecord(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.data.upsertZone(zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 60, Serial: 1, UpdatedAt: now})
	s.data.setRecord(aRecord{Name: "app.example.com", Type: "AAAA", Zone: "example.com", IP: "2001:db8::10", TTL: 25, Version: 1, UpdatedAt: now})

	req := new(dns.Msg)
	req.SetQuestion("app.example.com.", dns.TypeAAAA)

	resp := s.resolveDNS(req)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected success rcode, got %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected one answer, got %d", len(resp.Answer))
	}

	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	if !ok {
		t.Fatalf("expected AAAA answer, got %T", resp.Answer[0])
	}
	if aaaa.AAAA.String() != "2001:db8::10" {
		t.Fatalf("unexpected AAAA IP: %s", aaaa.AAAA.String())
	}
}

func TestResolveDNSNoDataForExistingNameDifferentType(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.data.upsertZone(zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 60, Serial: 1, UpdatedAt: now})
	s.data.setRecord(aRecord{Name: "app.example.com", Type: "A", Zone: "example.com", IP: "198.51.100.10", TTL: 25, Version: 1, UpdatedAt: now})

	req := new(dns.Msg)
	req.SetQuestion("app.example.com.", dns.TypeAAAA)

	resp := s.resolveDNS(req)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected NOERROR for existing name different type, got %d", resp.Rcode)
	}
	if len(resp.Answer) != 0 {
		t.Fatalf("expected empty answer for NODATA, got %d", len(resp.Answer))
	}
}

func TestResolveDNSTXTRecord(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.data.upsertZone(zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 60, Serial: 1, UpdatedAt: now})
	s.data.setRecord(aRecord{Name: "meta.example.com", Type: "TXT", Zone: "example.com", Text: "hello-world", TTL: 25, Version: 1, UpdatedAt: now})

	req := new(dns.Msg)
	req.SetQuestion("meta.example.com.", dns.TypeTXT)

	resp := s.resolveDNS(req)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected success rcode, got %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected one answer, got %d", len(resp.Answer))
	}

	txt, ok := resp.Answer[0].(*dns.TXT)
	if !ok {
		t.Fatalf("expected TXT answer, got %T", resp.Answer[0])
	}
	if len(txt.Txt) == 0 || txt.Txt[0] != "hello-world" {
		t.Fatalf("unexpected TXT payload: %#v", txt.Txt)
	}
}
