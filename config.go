package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

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

	apiToken := strings.TrimSpace(os.Getenv("API_TOKEN"))
	if apiToken == "" {
		log.Printf("warning: API_TOKEN is empty, control API is open")
	}

	syncToken := strings.TrimSpace(os.Getenv("SYNC_TOKEN"))
	if syncToken == "" {
		syncToken = apiToken
	}

	return config{
		NodeID:        nodeID,
		HTTPListen:    envOrDefault("HTTP_LISTEN", ":8080"),
		DNSUDPListen:  envOrDefault("DNS_UDP_LISTEN", ":53"),
		DNSTCPListen:  envOrDefault("DNS_TCP_LISTEN", ":53"),
		DBPath:        envOrDefault("DB_PATH", "dns.db"),
		MigrationsDir: envOrDefault("MIGRATIONS_DIR", "migrations"),
		DebugLog:      envOrDefaultBool("DEBUG_LOG", false),
		APIToken:      apiToken,
		SyncToken:     syncToken,
		Peers:         splitCSV(os.Getenv("PEERS")),
		DefaultTTL:    envOrDefaultUint32("DEFAULT_TTL", 20),
		DefaultZone:   defaultZone,
		DefaultNS:     defaultNS,
		SyncHTTPClient: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

func (c config) defaultNSForZone(_ string) []string {
	if len(c.DefaultNS) > 0 {
		return append([]string(nil), c.DefaultNS...)
	}
	return nil
}

func splitCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}

	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
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

	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 {
		return fallback
	}

	return uint32(n)
}

func envOrDefaultBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}

	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}

	return b
}
