package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestHTTPAuthMiddleware(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	req := httptest.NewRequest(http.MethodGet, "/v1/records", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.Code)
	}
}

func TestHTTPRecordUpsertAndList(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	body := `{"ip":"198.51.100.5","ttl":15}`
	req := httptest.NewRequest(http.MethodPut, "/v1/records/app.example.com", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for put, got %d", resp.Code)
	}

	reqList := httptest.NewRequest(http.MethodGet, "/v1/records", nil)
	reqList.Header.Set("Authorization", "Bearer token")
	respList := httptest.NewRecorder()
	r.ServeHTTP(respList, reqList)
	if respList.Code != http.StatusOK {
		t.Fatalf("expected 200 for list, got %d", respList.Code)
	}

	var out struct {
		Records []aRecord `json:"records"`
	}
	if err := json.Unmarshal(respList.Body.Bytes(), &out); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if len(out.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(out.Records))
	}
}

func TestHTTPRecordUpsertAAAA(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	body := `{"ip":"2001:db8::5","type":"AAAA","ttl":15}`
	req := httptest.NewRequest(http.MethodPut, "/v1/records/ipv6.example.com", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for AAAA put, got %d", resp.Code)
	}

	var out aRecord
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if out.Type != "AAAA" {
		t.Fatalf("expected AAAA type, got %s", out.Type)
	}
}

func TestHTTPRecordUpsertTXT(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	body := `{"type":"TXT","text":"site-verification=abc","ttl":15}`
	req := httptest.NewRequest(http.MethodPut, "/v1/records/txt.example.com", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for TXT put, got %d", resp.Code)
	}

	var out aRecord
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if out.Type != "TXT" || out.Text != "site-verification=abc" {
		t.Fatalf("unexpected TXT record: %#v", out)
	}
}

func TestHTTPRecordUpsertCNAME(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	body := `{"type":"CNAME","target":"app.example.com","ttl":20}`
	req := httptest.NewRequest(http.MethodPut, "/v1/records/www.example.com", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for CNAME put, got %d", resp.Code)
	}

	var out aRecord
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if out.Type != "CNAME" || out.Target != "app.example.com." {
		t.Fatalf("unexpected CNAME record: %#v", out)
	}
}

func TestHTTPRecordUpsertMX(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	body := `{"type":"MX","target":"mail.example.com","priority":10,"ttl":20}`
	req := httptest.NewRequest(http.MethodPut, "/v1/records/example.com", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for MX put, got %d", resp.Code)
	}

	var out aRecord
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if out.Type != "MX" || out.Target != "mail.example.com." || out.Priority != 10 {
		t.Fatalf("unexpected MX record: %#v", out)
	}
}

func TestHTTPRecordAddAndRemove(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	addReq := httptest.NewRequest(http.MethodPost, "/v1/records/pool.example.com/add", strings.NewReader(`{"type":"A","ip":"198.51.100.1"}`))
	addReq.Header.Set("Authorization", "Bearer token")
	addReq.Header.Set("Content-Type", "application/json")
	addResp := httptest.NewRecorder()
	r.ServeHTTP(addResp, addReq)
	if addResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for add, got %d", addResp.Code)
	}

	addReq2 := httptest.NewRequest(http.MethodPost, "/v1/records/pool.example.com/add", strings.NewReader(`{"type":"A","ip":"198.51.100.2"}`))
	addReq2.Header.Set("Authorization", "Bearer token")
	addReq2.Header.Set("Content-Type", "application/json")
	addResp2 := httptest.NewRecorder()
	r.ServeHTTP(addResp2, addReq2)
	if addResp2.Code != http.StatusOK {
		t.Fatalf("expected 200 for second add, got %d", addResp2.Code)
	}

	removeReq := httptest.NewRequest(http.MethodPost, "/v1/records/pool.example.com/remove", strings.NewReader(`{"type":"A","ip":"198.51.100.1"}`))
	removeReq.Header.Set("Authorization", "Bearer token")
	removeReq.Header.Set("Content-Type", "application/json")
	removeResp := httptest.NewRecorder()
	r.ServeHTTP(removeResp, removeReq)
	if removeResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for remove, got %d", removeResp.Code)
	}
}

func TestDoHGetAndPost(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.data.upsertZone(zoneConfig{Zone: "example.com", NS: []string{"love.me.cloudroof.eu"}, SOATTL: 60, Serial: 1, UpdatedAt: now})
	s.data.setRecord(aRecord{Name: "app.example.com", Zone: "example.com", IP: "198.51.100.7", TTL: 30, Version: 1, UpdatedAt: now})
	r := s.newRouter()

	msg := new(dns.Msg)
	msg.SetQuestion("app.example.com.", dns.TypeA)
	wire, err := msg.Pack()
	if err != nil {
		t.Fatalf("pack dns msg: %v", err)
	}

	q := base64.RawURLEncoding.EncodeToString(wire)
	getReq := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+q, nil)
	getResp := httptest.NewRecorder()
	r.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected 200 from DoH GET, got %d", getResp.Code)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(wire))
	postResp := httptest.NewRecorder()
	r.ServeHTTP(postResp, postReq)
	if postResp.Code != http.StatusOK {
		t.Fatalf("expected 200 from DoH POST, got %d", postResp.Code)
	}
}

func TestSyncAuthMiddleware(t *testing.T) {
	s := newTestServer(t)
	r := s.newRouter()

	req := httptest.NewRequest(http.MethodPost, "/v1/sync/event", strings.NewReader(`{"op":"delete","name":"a.example.com","version":1}`))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without sync token, got %d", resp.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/sync/event", strings.NewReader(`{"op":"delete","name":"a.example.com","version":1}`))
	req2.Header.Set("X-Sync-Token", "sync-token")
	resp2 := httptest.NewRecorder()
	r.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusOK {
		t.Fatalf("expected 200 with sync token, got %d", resp2.Code)
	}
}
