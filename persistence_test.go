package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPersistenceRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "roundtrip.db")
	p, err := newPersistence(dbPath)
	if err != nil {
		t.Fatalf("newPersistence: %v", err)
	}

	now := time.Now().UTC()
	z := zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 60, Serial: 7, UpdatedAt: now}
	r := aRecord{Name: "app.example.com", Zone: "example.com", IP: "203.0.113.8", TTL: 30, Version: 99, Source: "n1", UpdatedAt: now}

	if err := p.upsertZone(z); err != nil {
		t.Fatalf("upsertZone: %v", err)
	}
	if err := p.upsertRecord(r); err != nil {
		t.Fatalf("upsertRecord: %v", err)
	}

	loaded := newStore()
	if err := p.loadIntoStore(loaded); err != nil {
		t.Fatalf("loadIntoStore: %v", err)
	}

	if _, ok := loaded.getZone("example.com"); !ok {
		t.Fatal("expected zone after load")
	}
	got, ok := loaded.getRecord("app.example.com")
	if !ok {
		t.Fatal("expected record after load")
	}
	if got.IP != "203.0.113.8" {
		t.Fatalf("unexpected loaded IP: %s", got.IP)
	}
}

func TestPersistenceVersionGuard(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "version.db")
	p, err := newPersistence(dbPath)
	if err != nil {
		t.Fatalf("newPersistence: %v", err)
	}

	now := time.Now().UTC()
	newer := aRecord{Name: "app.example.com", Zone: "example.com", IP: "198.51.100.1", TTL: 20, Version: 20, Source: "n1", UpdatedAt: now}
	older := aRecord{Name: "app.example.com", Zone: "example.com", IP: "198.51.100.2", TTL: 20, Version: 10, Source: "n2", UpdatedAt: now}

	if err := p.upsertRecord(newer); err != nil {
		t.Fatalf("upsert newer: %v", err)
	}
	if err := p.upsertRecord(older); err != nil {
		t.Fatalf("upsert older: %v", err)
	}

	loaded := newStore()
	if err := p.loadIntoStore(loaded); err != nil {
		t.Fatalf("loadIntoStore: %v", err)
	}
	got, _ := loaded.getRecord("app.example.com")
	if got.IP != "198.51.100.1" {
		t.Fatalf("older write should not win, got ip=%s", got.IP)
	}
}
