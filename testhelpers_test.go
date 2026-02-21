package main

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func newTestServer(t *testing.T) *server {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "dns-test.db")
	p, err := newPersistence(dbPath)
	if err != nil {
		t.Fatalf("newPersistence: %v", err)
	}

	s := &server{
		cfg: config{
			NodeID:         "test-node",
			APIToken:       "token",
			SyncToken:      "sync-token",
			DefaultTTL:     20,
			DefaultZone:    "example.com.",
			DefaultNS:      []string{"love.me.cloudroof.eu.", "hate.you.cloudroof.eu."},
			SyncHTTPClient: &http.Client{Timeout: time.Second},
		},
		data:    newStore(),
		persist: p,
		start:   time.Now().Add(-time.Second),
	}

	return s
}
