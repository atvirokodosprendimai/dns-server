package main

import "testing"

func TestLoadConfigDefaultsAndFallbacks(t *testing.T) {
	t.Setenv("API_TOKEN", "api")
	t.Setenv("SYNC_TOKEN", "")
	t.Setenv("DEFAULT_ZONE", "example.com")
	t.Setenv("DEFAULT_NS", "")
	t.Setenv("DEFAULT_TTL", "not-a-number")
	t.Setenv("PEERS", "http://10.0.0.1:8080, http://10.0.0.2:8080")
	t.Setenv("HTTP_LISTEN", "")

	cfg := loadConfig()

	if cfg.SyncToken != "api" {
		t.Fatalf("expected SYNC_TOKEN fallback to API_TOKEN, got %q", cfg.SyncToken)
	}
	if cfg.HTTPListen != ":8080" {
		t.Fatalf("expected default HTTP listen, got %q", cfg.HTTPListen)
	}
	if cfg.DefaultTTL != 20 {
		t.Fatalf("expected default TTL fallback, got %d", cfg.DefaultTTL)
	}
	if cfg.DefaultZone != "example.com." {
		t.Fatalf("unexpected default zone: %q", cfg.DefaultZone)
	}
	if len(cfg.DefaultNS) != 0 {
		t.Fatalf("expected empty default NS, got %#v", cfg.DefaultNS)
	}
	if len(cfg.Peers) != 2 {
		t.Fatalf("unexpected peers: %#v", cfg.Peers)
	}
}

func TestDefaultNSForZone(t *testing.T) {
	cfg := config{}
	ns := cfg.defaultNSForZone("zone.example")
	if ns != nil {
		t.Fatalf("expected nil ns without DEFAULT_NS, got %#v", ns)
	}
}

func TestDefaultNSForZoneUsesConfiguredHostnames(t *testing.T) {
	cfg := config{DefaultNS: []string{"love.me.cloudroof.eu.", "hate.you.cloudroof.eu."}}
	ns := cfg.defaultNSForZone("zone.example")
	if len(ns) != 2 {
		t.Fatalf("expected two configured NS values, got %#v", ns)
	}
	if ns[0] != "love.me.cloudroof.eu." || ns[1] != "hate.you.cloudroof.eu." {
		t.Fatalf("unexpected configured NS values: %#v", ns)
	}
}
