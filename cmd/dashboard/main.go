package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type endpoint struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

type endpointStore struct {
	mu        sync.RWMutex
	path      string
	endpoints []endpoint
}

type server struct {
	store      *endpointStore
	httpClient *http.Client
	tpl        *template.Template
}

type actionResult struct {
	Endpoint string
	Action   string
	Success  bool
	Status   int
	Body     string
	Error    string
}

type pageData struct {
	Endpoints []endpoint
	Results   []actionResult
	States    []endpointState
	Message   string
	Now       string
}

type endpointState struct {
	Endpoint string
	Success  bool
	Error    string
	Zones    []zoneView
	Records  []dashboardRecord
}

type zoneView struct {
	Zone   string
	NS     []string
	SOATTL uint32
}

type dashboardRecord struct {
	Name  string
	Type  string
	Value string
	TTL   uint32
	Zone  string
}

func main() {
	listen := envOrDefault("DASHBOARD_LISTEN", ":8090")
	storePath := envOrDefault("DASHBOARD_STORE", "dashboard-endpoints.json")

	st, err := newEndpointStore(storePath)
	if err != nil {
		log.Fatalf("failed to initialize endpoint store: %v", err)
	}

	tpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		log.Fatalf("failed to parse template: %v", err)
	}

	s := &server{
		store: st,
		httpClient: &http.Client{
			Timeout: 4 * time.Second,
		},
		tpl: tpl,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/endpoints", s.handleAddEndpoint)
	mux.HandleFunc("/endpoints/delete", s.handleDeleteEndpoint)
	mux.HandleFunc("/actions/zone-upsert", s.handleZoneUpsert)
	mux.HandleFunc("/actions/record-upsert", s.handleRecordUpsert)
	mux.HandleFunc("/actions/record-add", s.handleRecordAdd)
	mux.HandleFunc("/actions/record-remove", s.handleRecordRemove)
	mux.HandleFunc("/actions/record-delete", s.handleRecordDelete)
	mux.HandleFunc("/actions/query-state", s.handleQueryState)

	log.Printf("dashboard listening on %s", listen)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatalf("dashboard server failed: %v", err)
	}
}

func newEndpointStore(path string) (*endpointStore, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	st := &endpointStore{path: absPath, endpoints: make([]endpoint, 0)}
	if err := st.load(); err != nil {
		return nil, err
	}

	return st, nil
}

func (s *endpointStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var items []endpoint
	if err := json.Unmarshal(b, &items); err != nil {
		return fmt.Errorf("decode %s: %w", s.path, err)
	}

	s.endpoints = sanitizeEndpoints(items)
	return nil
}

func (s *endpointStore) saveLocked() error {
	data, err := json.MarshalIndent(s.endpoints, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return err
	}

	return nil
}

func (s *endpointStore) list() []endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]endpoint, len(s.endpoints))
	copy(out, s.endpoints)
	return out
}

func (s *endpointStore) add(e endpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, cur := range s.endpoints {
		if cur.BaseURL == e.BaseURL {
			return fmt.Errorf("endpoint already exists: %s", e.BaseURL)
		}
	}

	s.endpoints = append(s.endpoints, e)
	sort.Slice(s.endpoints, func(i, j int) bool { return s.endpoints[i].Name < s.endpoints[j].Name })

	return s.saveLocked()
}

func (s *endpointStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, e := range s.endpoints {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("endpoint not found")
	}

	s.endpoints = append(s.endpoints[:idx], s.endpoints[idx+1:]...)
	return s.saveLocked()
}

func sanitizeEndpoints(items []endpoint) []endpoint {
	out := make([]endpoint, 0, len(items))
	seen := map[string]struct{}{}
	for _, e := range items {
		e.Name = strings.TrimSpace(e.Name)
		e.ID = strings.TrimSpace(e.ID)
		e.BaseURL = sanitizeURL(e.BaseURL)
		e.Token = strings.TrimSpace(e.Token)
		if e.ID == "" || e.Name == "" || e.BaseURL == "" {
			continue
		}
		if _, ok := seen[e.BaseURL]; ok {
			continue
		}
		seen[e.BaseURL] = struct{}{}
		out = append(out, e)
	}
	return out
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	msg := strings.TrimSpace(r.URL.Query().Get("msg"))
	if err := s.tpl.Execute(w, pageData{
		Endpoints: s.store.list(),
		Message:   msg,
		Now:       time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *server) handleAddEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	baseURL := sanitizeURL(r.FormValue("base_url"))
	token := strings.TrimSpace(r.FormValue("token"))

	if name == "" || baseURL == "" {
		http.Redirect(w, r, "/?msg=Name+and+base+URL+are+required", http.StatusSeeOther)
		return
	}

	err := s.store.add(endpoint{
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:    name,
		BaseURL: baseURL,
		Token:   token,
	})
	if err != nil {
		http.Redirect(w, r, "/?msg="+urlQueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/?msg=Endpoint+added", http.StatusSeeOther)
}

func (s *server) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		http.Redirect(w, r, "/?msg=Missing+endpoint+id", http.StatusSeeOther)
		return
	}

	if err := s.store.delete(id); err != nil {
		http.Redirect(w, r, "/?msg="+urlQueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/?msg=Endpoint+deleted", http.StatusSeeOther)
}

func (s *server) handleZoneUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	zone := strings.TrimSpace(r.FormValue("zone"))
	ns1 := strings.TrimSpace(r.FormValue("ns1"))
	ns2 := strings.TrimSpace(r.FormValue("ns2"))
	soaTTL := strings.TrimSpace(r.FormValue("soa_ttl"))

	if zone == "" || ns1 == "" || ns2 == "" {
		s.renderWithResults(w, "zone-upsert", []actionResult{{Error: "zone, ns1 and ns2 are required"}})
		return
	}
	if soaTTL == "" {
		soaTTL = "60"
	}

	body := map[string]any{"ns": []string{ns1, ns2}, "soa_ttl": mustAtoi(soaTTL, 60)}
	results := s.broadcastJSON(http.MethodPut, "/v1/zones/"+zone, body)
	s.renderWithResults(w, "zone-upsert", results)
}

func (s *server) handleRecordUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	recordType := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
	zone := strings.TrimSpace(r.FormValue("zone"))
	ttl := strings.TrimSpace(r.FormValue("ttl"))
	value := strings.TrimSpace(r.FormValue("value"))

	if name == "" || value == "" {
		s.renderWithResults(w, "record-upsert", []actionResult{{Error: "name and value are required"}})
		return
	}
	if recordType == "" {
		recordType = "A"
	}
	if ttl == "" {
		ttl = "60"
	}

	body := buildRecordBody(recordType, value, zone, mustAtoi(ttl, 60))

	results := s.broadcastJSON(http.MethodPut, "/v1/records/"+name, body)
	s.renderWithResults(w, "record-upsert", results)
}

func (s *server) handleRecordAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	recordType := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
	zone := strings.TrimSpace(r.FormValue("zone"))
	ttl := strings.TrimSpace(r.FormValue("ttl"))
	value := strings.TrimSpace(r.FormValue("value"))

	if name == "" || value == "" {
		s.renderWithResults(w, "record-add", []actionResult{{Error: "name and value are required"}})
		return
	}
	if recordType == "" {
		recordType = "A"
	}
	if ttl == "" {
		ttl = "60"
	}

	body := buildRecordBody(recordType, value, zone, mustAtoi(ttl, 60))
	results := s.broadcastJSON(http.MethodPost, "/v1/records/"+name+"/add", body)
	s.renderWithResults(w, "record-add", results)
}

func (s *server) handleRecordRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	recordType := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
	zone := strings.TrimSpace(r.FormValue("zone"))
	value := strings.TrimSpace(r.FormValue("value"))

	if name == "" || value == "" {
		s.renderWithResults(w, "record-remove", []actionResult{{Error: "name and value are required"}})
		return
	}
	if recordType == "" {
		recordType = "A"
	}

	body := buildRecordBody(recordType, value, zone, 60)
	results := s.broadcastJSON(http.MethodPost, "/v1/records/"+name+"/remove", body)
	s.renderWithResults(w, "record-remove", results)
}

func (s *server) handleRecordDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	recordType := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))

	if name == "" {
		s.renderWithResults(w, "record-delete", []actionResult{{Error: "name is required"}})
		return
	}

	path := "/v1/records/" + name
	if recordType != "" {
		path += "?type=" + recordType
	}

	results := s.broadcastJSON(http.MethodDelete, path, nil)
	s.renderWithResults(w, "record-delete", results)
}

func (s *server) handleQueryState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	states := s.queryStateAll()
	if err := s.tpl.Execute(w, pageData{
		Endpoints: s.store.list(),
		States:    states,
		Message:   "Action: query-state",
		Now:       time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *server) queryStateAll() []endpointState {
	eps := s.store.list()
	if len(eps) == 0 {
		return []endpointState{{Error: "no endpoints configured"}}
	}

	out := make([]endpointState, len(eps))
	var wg sync.WaitGroup
	for i, ep := range eps {
		wg.Add(1)
		go func(i int, ep endpoint) {
			defer wg.Done()
			st := endpointState{Endpoint: ep.Name}

			var zr struct {
				Zones []struct {
					Zone   string   `json:"zone"`
					NS     []string `json:"ns"`
					SOATTL uint32   `json:"soa_ttl"`
				} `json:"zones"`
			}
			if err := s.fetchJSON(ep, "/v1/zones", &zr); err != nil {
				st.Error = "zones fetch failed: " + err.Error()
				out[i] = st
				return
			}
			st.Zones = make([]zoneView, 0, len(zr.Zones))
			for _, z := range zr.Zones {
				st.Zones = append(st.Zones, zoneView{Zone: z.Zone, NS: z.NS, SOATTL: z.SOATTL})
			}

			var rr struct {
				Records []struct {
					Name     string `json:"name"`
					Type     string `json:"type"`
					IP       string `json:"ip"`
					Text     string `json:"text"`
					Target   string `json:"target"`
					Priority uint16 `json:"priority"`
					TTL      uint32 `json:"ttl"`
					Zone     string `json:"zone"`
				} `json:"records"`
			}
			if err := s.fetchJSON(ep, "/v1/records", &rr); err != nil {
				st.Error = "records fetch failed: " + err.Error()
				out[i] = st
				return
			}

			st.Records = make([]dashboardRecord, 0, len(rr.Records))
			for _, rec := range rr.Records {
				value := rec.IP
				if rec.Type == "TXT" {
					value = rec.Text
				}
				if rec.Type == "CNAME" {
					value = rec.Target
				}
				if rec.Type == "MX" {
					value = fmt.Sprintf("%d %s", rec.Priority, rec.Target)
				}
				st.Records = append(st.Records, dashboardRecord{
					Name:  rec.Name,
					Type:  rec.Type,
					Value: value,
					TTL:   rec.TTL,
					Zone:  rec.Zone,
				})
			}

			st.Success = true
			out[i] = st
		}(i, ep)
	}

	wg.Wait()
	return out
}

func (s *server) fetchJSON(ep endpoint, path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.BaseURL+path, nil)
	if err != nil {
		return err
	}
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return err
	}

	return nil
}

func (s *server) broadcastJSON(method, path string, payload any) []actionResult {
	eps := s.store.list()
	if len(eps) == 0 {
		return []actionResult{{Action: method + " " + path, Error: "no endpoints configured"}}
	}

	results := make([]actionResult, len(eps))
	var wg sync.WaitGroup
	for i, ep := range eps {
		wg.Add(1)
		go func(i int, ep endpoint) {
			defer wg.Done()

			res := actionResult{Endpoint: ep.Name, Action: method + " " + path}
			b := []byte(nil)
			if payload != nil {
				var err error
				b, err = json.Marshal(payload)
				if err != nil {
					res.Error = err.Error()
					results[i] = res
					return
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, method, ep.BaseURL+path, bytes.NewReader(b))
			if err != nil {
				res.Error = err.Error()
				results[i] = res
				return
			}
			if payload != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			if ep.Token != "" {
				req.Header.Set("Authorization", "Bearer "+ep.Token)
			}

			resp, err := s.httpClient.Do(req)
			if err != nil {
				res.Error = err.Error()
				results[i] = res
				return
			}
			defer resp.Body.Close()

			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			res.Status = resp.StatusCode
			res.Body = strings.TrimSpace(string(bodyBytes))
			res.Success = resp.StatusCode >= 200 && resp.StatusCode < 300
			if !res.Success && res.Body == "" {
				res.Error = "non-success status"
			}

			results[i] = res
		}(i, ep)
	}

	wg.Wait()
	return results
}

func (s *server) renderWithResults(w http.ResponseWriter, action string, results []actionResult) {
	if err := s.tpl.Execute(w, pageData{
		Endpoints: s.store.list(),
		Results:   results,
		Message:   "Action: " + action,
		Now:       time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func sanitizeURL(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimRight(v, "/")
	return v
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func mustAtoi(v string, fallback int) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(v, "%d", &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseMXValue(v string) (int, string) {
	v = strings.TrimSpace(v)
	parts := strings.Fields(v)
	if len(parts) >= 2 {
		prio := mustAtoi(parts[0], 10)
		target := strings.Join(parts[1:], " ")
		return prio, target
	}
	return 10, v
}

func buildRecordBody(recordType, value, zone string, ttl int) map[string]any {
	body := map[string]any{"type": recordType, "ttl": ttl, "zone": zone}
	if recordType == "TXT" {
		body["text"] = value
	} else if recordType == "CNAME" {
		body["target"] = value
	} else if recordType == "MX" {
		prio, target := parseMXValue(value)
		body["priority"] = prio
		body["target"] = target
	} else {
		body["ip"] = value
	}
	return body
}

func urlQueryEscape(v string) string {
	return url.QueryEscape(v)
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>DNS Dashboard</title>
  <style>
    :root { --bg:#f5f7fa; --card:#fff; --txt:#1f2937; --muted:#6b7280; --accent:#0f766e; --ok:#166534; --bad:#b91c1c; }
    * { box-sizing:border-box; }
    body { margin:0; font-family: ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,Arial; color:var(--txt); background:var(--bg); }
    .wrap { max-width:1100px; margin:0 auto; padding:20px; }
    .grid { display:grid; gap:16px; grid-template-columns: repeat(auto-fit,minmax(320px,1fr)); }
    .card { background:var(--card); border-radius:12px; padding:16px; box-shadow:0 1px 6px rgba(0,0,0,.07); }
    h1,h2 { margin:0 0 10px; }
    h1 { font-size:24px; }
    h2 { font-size:18px; }
    label { display:block; font-size:13px; margin:8px 0 4px; color:var(--muted); }
    input,select,button { width:100%; padding:10px; border-radius:8px; border:1px solid #d1d5db; }
    button { background:var(--accent); border:none; color:white; font-weight:600; margin-top:10px; cursor:pointer; }
    table { width:100%; border-collapse:collapse; font-size:13px; }
    th,td { padding:8px; border-bottom:1px solid #e5e7eb; text-align:left; vertical-align:top; }
    .status-ok { color:var(--ok); font-weight:600; }
    .status-bad { color:var(--bad); font-weight:600; }
    .mono { font-family: ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; }
    .small { color:var(--muted); font-size:12px; }
    .inline { display:grid; gap:8px; grid-template-columns:1fr auto; align-items:end; }
  </style>
</head>
<body>
  <div class="wrap">
    <h1>DNS Sync Dashboard</h1>
    <p class="small">Fan-out API actions to all configured DNS nodes. Time: {{.Now}}</p>
    {{if .Message}}<p><strong>{{.Message}}</strong></p>{{end}}

    <div class="grid">
      <section class="card">
        <h2>Add DNS API Endpoint</h2>
        <form method="post" action="/endpoints">
          <label>Name</label><input name="name" placeholder="node-vilnius" required>
          <label>Base URL</label><input name="base_url" placeholder="http://10.1.0.2:8080" required>
          <label>API Token</label><input name="token" placeholder="token for this node">
          <button type="submit">Add Endpoint</button>
        </form>
      </section>

      <section class="card">
        <h2>Configured Endpoints</h2>
        {{if .Endpoints}}
        <table>
          <thead><tr><th>Name</th><th>URL</th><th></th></tr></thead>
          <tbody>
            {{range .Endpoints}}
            <tr>
              <td>{{.Name}}</td>
              <td class="mono">{{.BaseURL}}</td>
              <td>
                <form method="post" action="/endpoints/delete">
                  <input type="hidden" name="id" value="{{.ID}}">
                  <button type="submit">Delete</button>
                </form>
              </td>
            </tr>
            {{end}}
          </tbody>
        </table>
        {{else}}
        <p>No endpoints yet.</p>
        {{end}}
      </section>
    </div>

    <div class="grid" style="margin-top:16px;">
      <section class="card">
        <h2>Query Current State</h2>
        <form method="post" action="/actions/query-state">
          <p class="small">Fetch current zones and records from all configured DNS endpoints.</p>
          <button type="submit">Query All Endpoints</button>
        </form>
      </section>

      <section class="card">
        <h2>Zone Upsert (NS Sync)</h2>
        <form method="post" action="/actions/zone-upsert">
          <label>Zone</label><input name="zone" placeholder="cloudroof.eu" required>
          <label>NS1</label><input name="ns1" placeholder="snail.cloudroof.eu" required>
          <label>NS2</label><input name="ns2" placeholder="rabbit.cloudroof.eu" required>
          <label>SOA TTL</label><input name="soa_ttl" value="60">
          <button type="submit">Sync Zone To All Endpoints</button>
        </form>
      </section>

      <section class="card">
        <h2>Record Upsert</h2>
        <form method="post" action="/actions/record-upsert">
          <label>Name</label><input name="name" placeholder="app.cloudroof.eu" required>
          <label>Type</label>
          <select name="type">
            <option>A</option>
            <option>AAAA</option>
            <option>TXT</option>
            <option>CNAME</option>
            <option>MX</option>
          </select>
          <label>Value (IP for A/AAAA, text for TXT, target for CNAME, "priority target" for MX)</label><input name="value" required>
          <label>Zone (optional)</label><input name="zone" placeholder="cloudroof.eu">
          <label>TTL</label><input name="ttl" value="60">
          <button type="submit">Sync Record To All Endpoints</button>
        </form>
      </section>

      <section class="card">
        <h2>Record Add (RRset)</h2>
        <form method="post" action="/actions/record-add">
          <label>Name</label><input name="name" placeholder="pool.cloudroof.eu" required>
          <label>Type</label>
          <select name="type">
            <option>A</option>
            <option>AAAA</option>
            <option>TXT</option>
            <option>CNAME</option>
            <option>MX</option>
          </select>
          <label>Value (IP for A/AAAA, text for TXT, target for CNAME, "priority target" for MX)</label><input name="value" required>
          <label>Zone (optional)</label><input name="zone" placeholder="cloudroof.eu">
          <label>TTL</label><input name="ttl" value="60">
          <button type="submit">Add Record Member On All Endpoints</button>
        </form>
        <p class="small">Use this for RR load distribution by adding multiple A/AAAA values for the same name.</p>
      </section>

      <section class="card">
        <h2>Record Remove (RRset)</h2>
        <form method="post" action="/actions/record-remove">
          <label>Name</label><input name="name" placeholder="pool.cloudroof.eu" required>
          <label>Type</label>
          <select name="type">
            <option>A</option>
            <option>AAAA</option>
            <option>TXT</option>
            <option>CNAME</option>
            <option>MX</option>
          </select>
          <label>Value to remove (same format as add)</label><input name="value" required>
          <label>Zone (optional)</label><input name="zone" placeholder="cloudroof.eu">
          <button type="submit">Remove Record Member On All Endpoints</button>
        </form>
      </section>

      <section class="card">
        <h2>Record Delete</h2>
        <form method="post" action="/actions/record-delete">
          <label>Name</label><input name="name" placeholder="app.cloudroof.eu" required>
          <label>Type (optional)</label>
          <select name="type">
            <option value="">ALL</option>
            <option>A</option>
            <option>AAAA</option>
            <option>TXT</option>
            <option>CNAME</option>
            <option>MX</option>
          </select>
          <button type="submit">Delete On All Endpoints</button>
        </form>
      </section>
    </div>

    {{if .Results}}
    <section class="card" style="margin-top:16px;">
      <h2>Action Results</h2>
      <table>
        <thead><tr><th>Endpoint</th><th>Action</th><th>Status</th><th>Body / Error</th></tr></thead>
        <tbody>
          {{range .Results}}
          <tr>
            <td>{{.Endpoint}}</td>
            <td class="mono">{{.Action}}</td>
            <td>{{if .Success}}<span class="status-ok">OK {{.Status}}</span>{{else}}<span class="status-bad">FAIL {{.Status}}</span>{{end}}</td>
            <td class="mono">{{if .Error}}{{.Error}}{{else}}{{.Body}}{{end}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
    </section>
    {{end}}

    {{if .States}}
    <section class="card" style="margin-top:16px;">
      <h2>Current State</h2>
      {{range .States}}
        <div style="margin:12px 0; padding:12px; border:1px solid #e5e7eb; border-radius:10px;">
          <div><strong>{{if .Endpoint}}{{.Endpoint}}{{else}}unknown-endpoint{{end}}</strong></div>
          {{if .Success}}
            <div class="small">Zones: {{len .Zones}} | Records: {{len .Records}}</div>
            <div style="margin-top:8px;">
              <div><strong>Zones</strong></div>
              <table>
                <thead><tr><th>Zone</th><th>NS</th><th>SOA TTL</th></tr></thead>
                <tbody>
                  {{range .Zones}}
                  <tr>
                    <td class="mono">{{.Zone}}</td>
                    <td class="mono">{{range $i, $ns := .NS}}{{if $i}}, {{end}}{{$ns}}{{end}}</td>
                    <td>{{.SOATTL}}</td>
                  </tr>
                  {{end}}
                </tbody>
              </table>
            </div>
            <div style="margin-top:8px;">
              <div><strong>Records</strong></div>
              <table>
                <thead><tr><th>Name</th><th>Type</th><th>Value</th><th>TTL</th><th>Zone</th></tr></thead>
                <tbody>
                  {{range .Records}}
                  <tr>
                    <td class="mono">{{.Name}}</td>
                    <td>{{.Type}}</td>
                    <td class="mono">{{.Value}}</td>
                    <td>{{.TTL}}</td>
                    <td class="mono">{{.Zone}}</td>
                  </tr>
                  {{end}}
                </tbody>
              </table>
            </div>
          {{else}}
            <div class="status-bad">FAILED</div>
            <div class="mono">{{.Error}}</div>
          {{end}}
        </div>
      {{end}}
    </section>
    {{end}}
  </div>
</body>
</html>`
