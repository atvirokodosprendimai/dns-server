package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg := loadConfig()
	mem := newStore()

	persist, err := newPersistence(cfg.DBPath, cfg.MigrationsDir)
	if err != nil {
		log.Fatalf("persistence init failed: %v", err)
	}
	if err := persist.loadIntoStore(mem); err != nil {
		log.Fatalf("persistence load failed: %v", err)
	}

	if cfg.DefaultZone != "" && len(cfg.DefaultNS) > 0 {
		now := time.Now().UTC()
		z := zoneConfig{
			Zone:      cfg.DefaultZone,
			NS:        cfg.DefaultNS,
			SOATTL:    cfg.DefaultTTL,
			Serial:    uint32(now.Unix()),
			UpdatedAt: now,
		}
		if mem.upsertZone(z) {
			if err := persist.upsertZone(z); err != nil {
				log.Printf("persist default zone failed: %v", err)
			}
		}
	}
	if cfg.DefaultZone != "" && len(cfg.DefaultNS) == 0 {
		log.Printf("warning: DEFAULT_ZONE is set but DEFAULT_NS is empty; create zone NS explicitly via API")
	}

	srv := &server{
		cfg:     cfg,
		data:    mem,
		persist: persist,
		start:   time.Now().UTC(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 3)
	go func() { errCh <- srv.runHTTP(ctx) }()
	go func() { errCh <- srv.runDNS(ctx, "udp") }()
	go func() { errCh <- srv.runDNS(ctx, "tcp") }()

	select {
	case <-ctx.Done():
		log.Printf("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("fatal server error: %v", err)
		}
	}
}
