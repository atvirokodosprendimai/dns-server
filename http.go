package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/miekg/dns"
)

var errMissingZoneNS = errors.New("zone NS is not configured; set DEFAULT_NS or create zone with explicit ns")

func (s *server) runHTTP(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.cfg.HTTPListen,
		Handler:           s.newRouter(),
		ReadHeaderTimeout: 2 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	return httpServer.ListenAndServe()
}

func (s *server) newRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealth)
	r.Get("/dns-query", s.handleDoH)
	r.Post("/dns-query", s.handleDoH)

	r.Group(func(r chi.Router) {
		r.Use(s.apiAuthMiddleware)
		r.Get("/v1/records", s.handleRecords)
		r.Put("/v1/records/{name}", s.handleRecordByName)
		r.Delete("/v1/records/{name}", s.handleRecordByName)
		r.Get("/v1/zones", s.handleZones)
		r.Put("/v1/zones/{zone}", s.handleZoneByName)
	})

	r.Group(func(r chi.Router) {
		r.Use(s.syncAuthMiddleware)
		r.Post("/v1/sync/event", s.handleSyncEvent)
	})
	return r
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"node_id":    s.cfg.NodeID,
		"uptime_sec": int(time.Since(s.start).Seconds()),
	})
}

func (s *server) handleDoH(w http.ResponseWriter, r *http.Request) {
	var payload []byte

	switch r.Method {
	case http.MethodGet:
		q := strings.TrimSpace(r.URL.Query().Get("dns"))
		if q == "" {
			http.Error(w, "missing dns query parameter", http.StatusBadRequest)
			return
		}

		decoded, err := base64.RawURLEncoding.DecodeString(q)
		if err != nil {
			http.Error(w, "invalid base64url dns parameter", http.StatusBadRequest)
			return
		}
		payload = decoded
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if len(body) == 0 {
			http.Error(w, "empty request body", http.StatusBadRequest)
			return
		}
		payload = body
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if len(payload) > dns.MaxMsgSize {
		http.Error(w, "dns message too large", http.StatusRequestEntityTooLarge)
		return
	}

	var req dns.Msg
	if err := req.Unpack(payload); err != nil {
		http.Error(w, "invalid dns message", http.StatusBadRequest)
		return
	}

	resp := s.resolveDNS(&req)
	wire, err := resp.Pack()
	if err != nil {
		http.Error(w, "failed to encode dns response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/dns-message")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(wire)
}

func (s *server) handleRecords(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"records": s.data.listRecords()})
}

func (s *server) handleRecordByName(w http.ResponseWriter, r *http.Request) {
	name := normalizeName(chi.URLParam(r, "name"))
	if name == "." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing record name"})
		return
	}

	switch r.Method {
	case http.MethodPut:
		s.handleRecordUpsert(w, r, name)
	case http.MethodDelete:
		s.handleRecordDelete(w, r, name)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleRecordUpsert(w http.ResponseWriter, r *http.Request, name string) {
	var req upsertRecordRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	text := strings.TrimSpace(req.Text)
	rawType := strings.ToUpper(strings.TrimSpace(req.Type))
	if rawType == "" {
		if text != "" {
			rawType = "TXT"
		} else {
			maybeIP := net.ParseIP(strings.TrimSpace(req.IP))
			if maybeIP != nil && maybeIP.To4() == nil {
				rawType = "AAAA"
			} else {
				rawType = "A"
			}
		}
	}
	if rawType != "A" && rawType != "AAAA" && rawType != "TXT" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be A, AAAA or TXT"})
		return
	}
	recordType := normalizeRecordType(rawType)

	parsedIP := net.ParseIP(strings.TrimSpace(req.IP))
	if recordType == "A" {
		if parsedIP == nil || parsedIP.To4() == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type A requires IPv4"})
			return
		}
	}
	if recordType == "AAAA" {
		if parsedIP == nil || parsedIP.To4() != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type AAAA requires IPv6"})
			return
		}
	}
	if recordType == "TXT" && text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type TXT requires text field"})
		return
	}

	ttl := req.TTL
	if ttl == 0 {
		ttl = s.cfg.DefaultTTL
	}

	zone := req.Zone
	if zone == "" {
		zone = s.inferZone(name)
	}

	now := time.Now().UTC()
	version := now.UnixNano()
	rec := aRecord{
		Name:      name,
		Type:      recordType,
		IP:        strings.TrimSpace(req.IP),
		Text:      text,
		TTL:       ttl,
		Zone:      zone,
		UpdatedAt: now,
		Version:   version,
		Source:    s.cfg.NodeID,
	}
	if recordType == "A" || recordType == "AAAA" {
		rec.IP = parsedIP.String()
		rec.Text = ""
	}

	zoneCfg := zoneConfig{
		Zone: zone,
	}
	if err := s.ensureZoneDefaults(&zoneCfg, now); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if s.data.upsertZone(zoneCfg) {
		if err := s.persist.upsertZone(zoneCfg); err != nil {
			log.Printf("persist zone failed: %v", err)
		}
	}

	if s.data.setRecord(rec) {
		if err := s.persist.upsertRecord(rec); err != nil {
			log.Printf("persist record failed: %v", err)
		}
	}

	writeJSON(w, http.StatusOK, rec)

	if shouldPropagate(req.Propagate) {
		go s.propagate(syncEvent{
			OriginNode: s.cfg.NodeID,
			Op:         "set",
			Record:     &rec,
			Version:    version,
			EventTime:  now,
		})
	}
}

func (s *server) handleRecordDelete(w http.ResponseWriter, r *http.Request, name string) {
	now := time.Now().UTC()
	version := now.UnixNano()
	recordType := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("type")))
	if recordType != "" && recordType != "A" && recordType != "AAAA" && recordType != "TXT" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type filter must be A, AAAA or TXT"})
		return
	}

	if s.data.deleteRecordByType(name, recordType, version) {
		if err := s.persist.deleteRecord(name, recordType, version); err != nil {
			log.Printf("persist record delete failed: %v", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"deleted": name, "type": recordType, "version": version})

	propagate := true
	if q := r.URL.Query().Get("propagate"); strings.EqualFold(q, "false") {
		propagate = false
	}
	if propagate {
		go s.propagate(syncEvent{
			OriginNode: s.cfg.NodeID,
			Op:         "delete",
			Name:       name,
			Type:       recordType,
			Version:    version,
			EventTime:  now,
		})
	}
}

func (s *server) handleZones(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"zones": s.data.listZones()})
}

func (s *server) handleZoneByName(w http.ResponseWriter, r *http.Request) {
	zone := normalizeName(chi.URLParam(r, "zone"))
	if zone == "." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing zone name"})
		return
	}

	var req upsertZoneRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	now := time.Now().UTC()
	ttl := req.SOATTL
	if ttl == 0 {
		ttl = s.cfg.DefaultTTL
	}
	ns := normalizeNames(req.NS)
	if len(ns) == 0 {
		if existing, ok := s.data.getZone(zone); ok && len(existing.NS) > 0 {
			ns = existing.NS
		} else {
			ns = s.cfg.defaultNSForZone(zone)
		}
	}
	if len(ns) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ns is required when DEFAULT_NS is not configured"})
		return
	}

	z := zoneConfig{
		Zone:      zone,
		NS:        ns,
		SOATTL:    ttl,
		Serial:    uint32(now.Unix()),
		UpdatedAt: now,
	}

	if s.data.upsertZone(z) {
		if err := s.persist.upsertZone(z); err != nil {
			log.Printf("persist zone failed: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, z)

	if shouldPropagate(req.Propagate) {
		go s.propagate(syncEvent{
			OriginNode: s.cfg.NodeID,
			Op:         "zone",
			Zone:       zone,
			Version:    int64(z.Serial),
			EventTime:  now,
			ZoneConfig: &z,
		})
	}
}

func (s *server) handleSyncEvent(w http.ResponseWriter, r *http.Request) {
	var ev syncEvent
	if err := decodeJSON(r.Body, &ev); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if ev.Version == 0 {
		ev.Version = time.Now().UTC().UnixNano()
	}

	switch ev.Op {
	case "set":
		if ev.Record == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "record required for set"})
			return
		}
		rec := *ev.Record
		rec.Version = ev.Version
		if rec.TTL == 0 {
			rec.TTL = s.cfg.DefaultTTL
		}
		if rec.Zone == "" {
			rec.Zone = s.inferZone(rec.Name)
		}
		if strings.TrimSpace(rec.Type) == "" {
			if strings.TrimSpace(rec.Text) != "" {
				rec.Type = "TXT"
			} else if ip := net.ParseIP(rec.IP); ip != nil && ip.To4() == nil {
				rec.Type = "AAAA"
			}
		}
		rec.Type = normalizeRecordType(rec.Type)
		if rec.Type == "A" && net.ParseIP(rec.IP).To4() == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sync set type A requires IPv4"})
			return
		}
		if rec.Type == "AAAA" {
			ip := net.ParseIP(rec.IP)
			if ip == nil || ip.To4() != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sync set type AAAA requires IPv6"})
				return
			}
		}
		if rec.Type == "TXT" && strings.TrimSpace(rec.Text) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sync set type TXT requires text"})
			return
		}
		rec.Source = ev.OriginNode
		rec.UpdatedAt = ev.EventTime

		if s.data.setRecord(rec) {
			if err := s.persist.upsertRecord(rec); err != nil {
				log.Printf("persist record failed: %v", err)
			}
		}

		zoneCfg := zoneConfig{
			Zone: rec.Zone,
		}
		now := time.Now().UTC()
		if err := s.ensureZoneDefaults(&zoneCfg, now); err == nil {
			if s.data.upsertZone(zoneCfg) {
				if err := s.persist.upsertZone(zoneCfg); err != nil {
					log.Printf("persist zone failed: %v", err)
				}
			}
		} else {
			log.Printf("sync set skipped zone defaults for %s: %v", rec.Zone, err)
		}
	case "delete":
		evType := strings.ToUpper(strings.TrimSpace(ev.Type))
		if evType != "" && evType != "A" && evType != "AAAA" && evType != "TXT" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sync delete type must be A, AAAA or TXT"})
			return
		}
		if s.data.deleteRecordByType(ev.Name, evType, ev.Version) {
			if err := s.persist.deleteRecord(normalizeName(ev.Name), evType, ev.Version); err != nil {
				log.Printf("persist record delete failed: %v", err)
			}
		}
	case "zone":
		if ev.ZoneConfig == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "zone_config required for zone op"})
			return
		}
		if s.data.upsertZone(*ev.ZoneConfig) {
			if err := s.persist.upsertZone(*ev.ZoneConfig); err != nil {
				log.Printf("persist zone failed: %v", err)
			}
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported op"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) apiAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken != "" && !validToken(r, s.cfg.APIToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) syncAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.SyncToken == "" {
			next.ServeHTTP(w, r)
			return
		}

		tok := strings.TrimSpace(r.Header.Get("X-Sync-Token"))
		if tok == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing sync token"})
			return
		}
		if tok != s.cfg.SyncToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid sync token"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *server) propagate(ev syncEvent) {
	if len(s.cfg.Peers) == 0 {
		return
	}

	body, err := json.Marshal(ev)
	if err != nil {
		log.Printf("sync marshal failed: %v", err)
		return
	}

	for _, peer := range s.cfg.Peers {
		peer = strings.TrimRight(strings.TrimSpace(peer), "/")
		if peer == "" {
			continue
		}

		go func(peerURL string) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, peerURL+"/v1/sync/event", bytes.NewReader(body))
			if err != nil {
				log.Printf("sync request build failed for %s: %v", peerURL, err)
				return
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Sync-Token", s.cfg.SyncToken)

			resp, err := s.cfg.SyncHTTPClient.Do(req)
			if err != nil {
				log.Printf("sync request failed for %s: %v", peerURL, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				log.Printf("sync request rejected by %s status=%d body=%s", peerURL, resp.StatusCode, strings.TrimSpace(string(b)))
			}
		}(peer)
	}
}

func (s *server) inferZone(name string) string {
	if z, ok := s.data.bestZone(name); ok {
		return z.Zone
	}

	if s.cfg.DefaultZone != "" {
		return s.cfg.DefaultZone
	}

	labels := dns.SplitDomainName(normalizeName(name))
	if len(labels) <= 1 {
		return normalizeName(name)
	}

	return normalizeName(strings.Join(labels[1:], "."))
}

func (s *server) ensureZoneDefaults(z *zoneConfig, now time.Time) error {
	if existing, ok := s.data.getZone(z.Zone); ok {
		if len(z.NS) == 0 {
			z.NS = existing.NS
		}
		if z.SOATTL == 0 {
			z.SOATTL = existing.SOATTL
		}
		if z.Serial == 0 {
			z.Serial = uint32(now.Unix())
		}
		if z.UpdatedAt.IsZero() {
			z.UpdatedAt = now
		}
		if len(z.NS) == 0 {
			return errMissingZoneNS
		}
		return nil
	}

	if len(z.NS) == 0 {
		z.NS = s.cfg.defaultNSForZone(z.Zone)
	}
	if len(z.NS) == 0 {
		return errMissingZoneNS
	}
	if z.SOATTL == 0 {
		z.SOATTL = s.cfg.DefaultTTL
	}
	if z.Serial == 0 {
		z.Serial = uint32(now.Unix())
	}
	if z.UpdatedAt.IsZero() {
		z.UpdatedAt = now
	}

	return nil
}
