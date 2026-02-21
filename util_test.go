package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	if got := normalizeName("  App.Example.COM "); got != "app.example.com." {
		t.Fatalf("normalizeName mismatch: %q", got)
	}
	if got := normalizeName(""); got != "." {
		t.Fatalf("normalizeName empty mismatch: %q", got)
	}
}

func TestNormalizeNames(t *testing.T) {
	got := normalizeNames([]string{"EXAMPLE.com", "", " love.me.cloudroof.eu "})
	if len(got) != 2 {
		t.Fatalf("expected 2 normalized names, got %d", len(got))
	}
	if got[0] != "example.com." || got[1] != "love.me.cloudroof.eu." {
		t.Fatalf("unexpected names: %#v", got)
	}
}

func TestDecodeJSONUnknownFields(t *testing.T) {
	var out struct {
		A int `json:"a"`
	}
	err := decodeJSON(strings.NewReader(`{"a":1,"b":2}`), &out)
	if err == nil {
		t.Fatal("expected decodeJSON to reject unknown field")
	}
}

func TestShouldPropagate(t *testing.T) {
	if !shouldPropagate(nil) {
		t.Fatal("nil pointer should default to true")
	}
	f := false
	if shouldPropagate(&f) {
		t.Fatal("false pointer should return false")
	}
}

func TestValidToken(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer abc")
	if !validToken(r, "abc") {
		t.Fatal("expected bearer token to pass")
	}

	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("X-API-Token", "xyz")
	if !validToken(r2, "xyz") {
		t.Fatal("expected X-API-Token to pass")
	}
}
