package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/miekg/dns"
)

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

func normalizeRecordType(recordType string) string {
	recordType = strings.ToUpper(strings.TrimSpace(recordType))
	switch recordType {
	case "AAAA", "TXT", "CNAME", "MX":
		return recordType
	default:
		return "A"
	}
}

func decodeJSON(r io.Reader, out any) error {
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func shouldPropagate(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

func validToken(r *http.Request, expected string) bool {
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if bearer != "" && bearer == expected {
		return true
	}

	header := strings.TrimSpace(r.Header.Get("X-API-Token"))
	return header != "" && header == expected
}
