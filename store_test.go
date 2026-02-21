package main

import (
	"testing"
	"time"
)

func TestStoreSetRecordVersioning(t *testing.T) {
	s := newStore()
	newRec := aRecord{Name: "app.example.com", Zone: "example.com", IP: "192.0.2.1", TTL: 10, Version: 20}
	if !s.setRecord(newRec) {
		t.Fatal("expected initial setRecord to succeed")
	}

	oldRec := aRecord{Name: "app.example.com", Zone: "example.com", IP: "192.0.2.2", TTL: 10, Version: 10}
	if s.setRecord(oldRec) {
		t.Fatal("expected stale record update to be rejected")
	}
}

func TestStoreDeleteRecordVersioning(t *testing.T) {
	s := newStore()
	s.setRecord(aRecord{Name: "app.example.com", Zone: "example.com", IP: "192.0.2.1", TTL: 10, Version: 50})

	if s.deleteRecord("app.example.com", 10) {
		t.Fatal("expected stale delete to be rejected")
	}
	if _, ok := s.getRecord("app.example.com"); !ok {
		t.Fatal("record should still exist after stale delete")
	}

	if !s.deleteRecord("app.example.com", 51) {
		t.Fatal("expected newer delete to succeed")
	}
}

func TestStoreBestZoneLongestMatch(t *testing.T) {
	s := newStore()
	now := time.Now().UTC()
	s.upsertZone(zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 30, Serial: 1, UpdatedAt: now})
	s.upsertZone(zoneConfig{Zone: "svc.example.com", NS: []string{"hate.you.cloudroof.eu"}, SOATTL: 30, Serial: 1, UpdatedAt: now})

	z, ok := s.bestZone("api.svc.example.com")
	if !ok {
		t.Fatal("expected bestZone to find a match")
	}
	if z.Zone != "svc.example.com." {
		t.Fatalf("unexpected best zone: %s", z.Zone)
	}
}
