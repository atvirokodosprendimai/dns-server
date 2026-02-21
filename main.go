package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/miekg/dns"
	"gorm.io/gorm"
)

type config struct {
	NodeID         string
	HTTPListen     string
	DNSUDPListen   string
	DNSTCPListen   string
	DBPath         string
	APIToken       string
	SyncToken      string
	Peers          []string
	DefaultTTL     uint32
	DefaultZone    string
	DefaultNS      []string
	SyncHTTPClient *http.Client
}

type zoneConfig struct {
	Zone      string    `json:"zone"`
	NS        []string  `json:"ns"`
	SOATTL    uint32    `json:"soa_ttl"`
	Serial    uint32    `json:"serial"`
	UpdatedAt time.Time `json:"updated_at"`
}

type aRecord struct {
	Name      string    `json:"name"`
	IP        string    `json:"ip"`
	TTL       uint32    `json:"ttl"`
	Zone      string    `json:"zone"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int64     `json:"version"`
	Source    string    `json:"source"`
}

type syncEvent struct {
	OriginNode string      `json:"origin_node"`
	Op         string      `json:"op"`
	Record     *aRecord    `json:"record,omitempty"`
	Name       string      `json:"name,omitempty"`
	Zone       string      `json:"zone,omitempty"`
	Version    int64       `json:"version"`
	EventTime  time.Time   `json:"event_time"`
	ZoneConfig *zoneConfig `json:"zone_config,omitempty"`
}

type store struct {
	mu      sync.RWMutex
	records map[string]aRecord
	zones   map[string]zoneConfig
}

func newStore() *store {
	return &store{
		records: make(map[string]aRecord),
		zones:   make(map[string]zoneConfig),
	}
}

func (s *store) setRecord(rec aRecord) bool {
	key := normalizeName(rec.Name)
	rec.Name = key
	rec.Zone = normalizeName(rec.Zone)

	s.mu.Lock()
	defer s.mu.Unlock()

	prev, ok := s.records[key]
	if ok && prev.Version > rec.Version {
		return false
	}

	s.records[key] = rec
	return true
}

func (s *store) deleteRecord(name string, version int64) bool {
	key := normalizeName(name)

	s.mu.Lock()
	defer s.mu.Unlock()

	prev, ok := s.records[key]
	if ok && prev.Version > version {
		return false
	}

	delete(s.records, key)
	return true
}

func (s *store) getRecord(name string) (aRecord, bool) {
	key := normalizeName(name)

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.records[key]
	return rec, ok
}

func (s *store) listRecords() []aRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]aRecord, 0, len(s.records))
	for _, v := range s.records {
		out = append(out, v)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *store) upsertZone(z zoneConfig) bool {
	z.Zone = normalizeName(z.Zone)
	z.NS = normalizeNames(z.NS)

	s.mu.Lock()
	defer s.mu.Unlock()

	prev, ok := s.zones[z.Zone]
	if ok && prev.Serial > z.Serial {
		return false
	}

	s.zones[z.Zone] = z
	return true
}

func (s *store) getZone(zone string) (zoneConfig, bool) {
	z := normalizeName(zone)

	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, ok := s.zones[z]
	return cfg, ok
}

func (s *store) listZones() []zoneConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]zoneConfig, 0, len(s.zones))
	for _, v := range s.zones {
		out = append(out, v)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Zone < out[j].Zone })
	return out
}

func (s *store) isManagedName(name string) bool {
	q := normalizeName(name)

	s.mu.RLock()
	defer s.mu.RUnlock()

	for zone := range s.zones {
		if dns.IsSubDomain(zone, q) {
			return true
		}
	}

	return false
}

func (s *store) bestZone(name string) (zoneConfig, bool) {
	q := normalizeName(name)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var (
		best       zoneConfig
		found      bool
		bestLabels int
	)

	for zone, cfg := range s.zones {
		if !dns.IsSubDomain(zone, q) {
			continue
		}
		labels := dns.CountLabel(zone)
		if !found || labels > bestLabels {
			best = cfg
			bestLabels = labels
			found = true
		}
	}

	return best, found
}

type server struct {
	cfg     config
	data    *store
	persist *persistence
	start   time.Time
}

type recordModel struct {
	Name      string    `gorm:"primaryKey;size:255"`
	IP        string    `gorm:"size:45;not null"`
	TTL       uint32    `gorm:"not null"`
	Zone      string    `gorm:"size:255;not null"`
	UpdatedAt time.Time `gorm:"not null"`
	Version   int64     `gorm:"not null;index"`
	Source    string    `gorm:"size:128;not null"`
}

type zoneModel struct {
	Zone      string    `gorm:"primaryKey;size:255"`
	NSJSON    string    `gorm:"type:text;not null"`
	SOATTL    uint32    `gorm:"not null"`
	Serial    uint32    `gorm:"not null;index"`
	UpdatedAt time.Time `gorm:"not null"`
}

type persistence struct {
	db *gorm.DB
}

func newPersistence(dbPath string) (*persistence, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.AutoMigrate(&recordModel{}, &zoneModel{}); err != nil {
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}

	return &persistence{db: db}, nil
}

func (p *persistence) loadIntoStore(s *store) error {
	var zones []zoneModel
	if err := p.db.Find(&zones).Error; err != nil {
		return fmt.Errorf("load zones: %w", err)
	}

	for _, z := range zones {
		ns, err := unmarshalNS(z.NSJSON)
		if err != nil {
			return fmt.Errorf("decode zone %s: %w", z.Zone, err)
		}
		s.upsertZone(zoneConfig{
			Zone:      z.Zone,
			NS:        ns,
			SOATTL:    z.SOATTL,
			Serial:    z.Serial,
			UpdatedAt: z.UpdatedAt,
		})
	}

	var records []recordModel
	if err := p.db.Find(&records).Error; err != nil {
		return fmt.Errorf("load records: %w", err)
	}

	for _, r := range records {
		s.setRecord(aRecord{
			Name:      r.Name,
			IP:        r.IP,
			TTL:       r.TTL,
			Zone:      r.Zone,
			UpdatedAt: r.UpdatedAt,
			Version:   r.Version,
			Source:    r.Source,
		})
	}

	return nil
}

func (p *persistence) upsertRecord(rec aRecord) error {
	var existing recordModel
	err := p.db.First(&existing, "name = ?", rec.Name).Error
	if err == nil && existing.Version > rec.Version {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("lookup record: %w", err)
	}

	model := recordModel{
		Name:      rec.Name,
		IP:        rec.IP,
		TTL:       rec.TTL,
		Zone:      rec.Zone,
		UpdatedAt: rec.UpdatedAt,
		Version:   rec.Version,
		Source:    rec.Source,
	}

	if err := p.db.Save(&model).Error; err != nil {
		return fmt.Errorf("save record: %w", err)
	}

	return nil
}

func (p *persistence) deleteRecord(name string, version int64) error {
	var existing recordModel
	err := p.db.First(&existing, "name = ?", name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup record before delete: %w", err)
	}
	if existing.Version > version {
		return nil
	}

	if err := p.db.Delete(&recordModel{}, "name = ?", name).Error; err != nil {
		return fmt.Errorf("delete record: %w", err)
	}

	return nil
}

func (p *persistence) upsertZone(z zoneConfig) error {
	nsJSON, err := marshalNS(z.NS)
	if err != nil {
		return err
	}

	var existing zoneModel
	err = p.db.First(&existing, "zone = ?", z.Zone).Error
	if err == nil && existing.Serial > z.Serial {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("lookup zone: %w", err)
	}

	model := zoneModel{
		Zone:      z.Zone,
		NSJSON:    nsJSON,
		SOATTL:    z.SOATTL,
		Serial:    z.Serial,
		UpdatedAt: z.UpdatedAt,
	}
	if err := p.db.Save(&model).Error; err != nil {
		return fmt.Errorf("save zone: %w", err)
	}

	return nil
}

func marshalNS(ns []string) (string, error) {
	b, err := json.Marshal(ns)
	if err != nil {
		return "", fmt.Errorf("encode ns list: %w", err)
	}
	return string(b), nil
}

func unmarshalNS(v string) ([]string, error) {
	out := []string{}
	if strings.TrimSpace(v) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, err
	}
	return normalizeNames(out), nil
}

func main() {
	cfg := loadConfig()
	st := newStore()
	persist, err := newPersistence(cfg.DBPath)
	if err != nil {
		log.Fatalf("persistence init failed: %v", err)
	}
	if err := persist.loadIntoStore(st); err != nil {
		log.Fatalf("persistence load failed: %v", err)
	}

	if cfg.DefaultZone != "" {
		z := zoneConfig{
			Zone:      cfg.DefaultZone,
			NS:        cfg.DefaultNS,
			SOATTL:    cfg.DefaultTTL,
			Serial:    uint32(time.Now().Unix()),
			UpdatedAt: time.Now().UTC(),
		}
		if st.upsertZone(z) {
			if err := persist.upsertZone(z); err != nil {
				log.Printf("persist default zone failed: %v", err)
			}
		}
	}

	srv := &server{cfg: cfg, data: st, persist: persist, start: time.Now().UTC()}

	errCh := make(chan error, 3)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

func (s *server) runDNS(ctx context.Context, network string) error {
	addr := s.cfg.DNSUDPListen
	if network == "tcp" {
		addr = s.cfg.DNSTCPListen
	}

	h := dns.NewServeMux()
	h.HandleFunc(".", s.handleDNS)

	dnsServer := &dns.Server{Addr: addr, Net: network, Handler: h}

	go func() {
		<-ctx.Done()
		_ = dnsServer.ShutdownContext(context.Background())
	}()

	log.Printf("dns/%s listening on %s", network, addr)
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
	mname := "ns1." + z.Zone
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

func (s *server) runHTTP(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/dns-query", s.handleDoH)
	mux.HandleFunc("/v1/records", s.withAuth(s.handleRecords))
	mux.HandleFunc("/v1/records/", s.withAuth(s.handleRecordByName))
	mux.HandleFunc("/v1/zones", s.withAuth(s.handleZones))
	mux.HandleFunc("/v1/zones/", s.withAuth(s.handleZoneByName))
	mux.HandleFunc("/v1/sync/event", s.withSyncAuth(s.handleSyncEvent))

	httpServer := &http.Server{
		Addr:              s.cfg.HTTPListen,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("http listening on %s", s.cfg.HTTPListen)
	return httpServer.ListenAndServe()
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

func (s *server) handleRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"records": s.data.listRecords()})
}

type upsertRecordRequest struct {
	IP        string `json:"ip"`
	TTL       uint32 `json:"ttl"`
	Zone      string `json:"zone"`
	Propagate *bool  `json:"propagate,omitempty"`
}

func (s *server) handleRecordByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/v1/records/")
	name = normalizeName(name)
	if name == "." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing record name"})
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req upsertRecordRequest
		if err := decodeJSON(r.Body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		ip := net.ParseIP(strings.TrimSpace(req.IP)).To4()
		if ip == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ip must be valid IPv4"})
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
			IP:        ip.String(),
			TTL:       ttl,
			Zone:      zone,
			UpdatedAt: now,
			Version:   version,
			Source:    s.cfg.NodeID,
		}

		zoneCfg := zoneConfig{
			Zone:      zone,
			NS:        s.cfg.DefaultNSForZone(zone),
			SOATTL:    s.cfg.DefaultTTL,
			Serial:    uint32(now.Unix()),
			UpdatedAt: now,
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
	case http.MethodDelete:
		now := time.Now().UTC()
		version := now.UnixNano()
		if s.data.deleteRecord(name, version) {
			if err := s.persist.deleteRecord(name, version); err != nil {
				log.Printf("persist record delete failed: %v", err)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": name, "version": version})

		propagate := true
		if q := r.URL.Query().Get("propagate"); strings.EqualFold(q, "false") {
			propagate = false
		}
		if propagate {
			go s.propagate(syncEvent{
				OriginNode: s.cfg.NodeID,
				Op:         "delete",
				Name:       name,
				Version:    version,
				EventTime:  now,
			})
		}
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

type upsertZoneRequest struct {
	NS        []string `json:"ns"`
	SOATTL    uint32   `json:"soa_ttl"`
	Propagate *bool    `json:"propagate,omitempty"`
}

func (s *server) handleZones(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"zones": s.data.listZones()})
}

func (s *server) handleZoneByName(w http.ResponseWriter, r *http.Request) {
	zone := strings.TrimPrefix(r.URL.Path, "/v1/zones/")
	zone = normalizeName(zone)
	if zone == "." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing zone name"})
		return
	}

	switch r.Method {
	case http.MethodPut:
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
			ns = s.cfg.DefaultNSForZone(zone)
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
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleSyncEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

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
		rec.Source = ev.OriginNode
		rec.UpdatedAt = ev.EventTime
		if s.data.setRecord(rec) {
			if err := s.persist.upsertRecord(rec); err != nil {
				log.Printf("persist record failed: %v", err)
			}
		}
		zoneCfg := zoneConfig{
			Zone:      rec.Zone,
			NS:        s.cfg.DefaultNSForZone(rec.Zone),
			SOATTL:    s.cfg.DefaultTTL,
			Serial:    uint32(time.Now().Unix()),
			UpdatedAt: time.Now().UTC(),
		}
		if s.data.upsertZone(zoneCfg) {
			if err := s.persist.upsertZone(zoneCfg); err != nil {
				log.Printf("persist zone failed: %v", err)
			}
		}
	case "delete":
		if s.data.deleteRecord(ev.Name, ev.Version) {
			if err := s.persist.deleteRecord(normalizeName(ev.Name), ev.Version); err != nil {
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
		peer := strings.TrimRight(strings.TrimSpace(peer), "/")
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

func (s *server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" {
			next(w, r)
			return
		}

		if !validToken(r, s.cfg.APIToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		next(w, r)
	}
}

func (s *server) withSyncAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.SyncToken == "" {
			next(w, r)
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

		next(w, r)
	}
}

func validToken(r *http.Request, expected string) bool {
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if bearer != "" && bearer == expected {
		return true
	}

	header := strings.TrimSpace(r.Header.Get("X-API-Token"))
	return header != "" && header == expected
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r io.Reader, out any) error {
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

func normalizeName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "."
	}
	return dns.Fqdn(name)
}

func normalizeNames(in []string) []string {
	out := make([]string, 0, len(in))
	for _, name := range in {
		n := normalizeName(name)
		if n == "." {
			continue
		}
		out = append(out, n)
	}
	return out
}

func shouldPropagate(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

func loadConfig() config {
	nodeID := strings.TrimSpace(os.Getenv("NODE_ID"))
	if nodeID == "" {
		host, _ := os.Hostname()
		nodeID = host
	}

	defaultZone := normalizeName(strings.TrimSpace(os.Getenv("DEFAULT_ZONE")))
	if defaultZone == "." {
		defaultZone = ""
	}

	defaultNS := normalizeNames(splitCSV(os.Getenv("DEFAULT_NS")))
	if len(defaultNS) == 0 && defaultZone != "" {
		defaultNS = []string{"ns1." + defaultZone}
	}

	apiToken := strings.TrimSpace(os.Getenv("API_TOKEN"))
	if apiToken == "" {
		log.Printf("warning: API_TOKEN is empty, control API is open")
	}

	syncToken := strings.TrimSpace(os.Getenv("SYNC_TOKEN"))
	if syncToken == "" {
		syncToken = apiToken
	}

	return config{
		NodeID:       nodeID,
		HTTPListen:   envOrDefault("HTTP_LISTEN", ":8080"),
		DNSUDPListen: envOrDefault("DNS_UDP_LISTEN", ":53"),
		DNSTCPListen: envOrDefault("DNS_TCP_LISTEN", ":53"),
		DBPath:       envOrDefault("DB_PATH", "dns.db"),
		APIToken:     apiToken,
		SyncToken:    syncToken,
		Peers:        splitCSV(os.Getenv("PEERS")),
		DefaultTTL:   envOrDefaultUint32("DEFAULT_TTL", 20),
		DefaultZone:  defaultZone,
		DefaultNS:    defaultNS,
		SyncHTTPClient: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

func (c config) DefaultNSForZone(zone string) []string {
	if len(c.DefaultNS) > 0 {
		return append([]string(nil), c.DefaultNS...)
	}
	return []string{"ns1." + normalizeName(zone)}
}

func splitCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envOrDefaultUint32(key string, fallback uint32) uint32 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}

	var parsed uint32
	_, err := fmt.Sscanf(v, "%d", &parsed)
	if err != nil || parsed == 0 {
		return fallback
	}

	return parsed
}
