package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

func newStore() *store {
	return &store{
		records: make(map[string]aRecord),
		zones:   make(map[string]zoneConfig),
	}
}

func (s *store) setRecord(rec aRecord) bool {
	rec.Name = normalizeName(rec.Name)
	rec.Type = normalizeRecordType(rec.Type)
	rec.Zone = normalizeName(rec.Zone)
	key := recordKey(rec)

	s.mu.Lock()
	defer s.mu.Unlock()

	for k, prev := range s.records {
		if prev.Name != rec.Name || prev.Type != rec.Type {
			continue
		}
		if prev.Version > rec.Version {
			return false
		}
		delete(s.records, k)
	}

	if prev, ok := s.records[key]; ok && prev.Version > rec.Version {
		return false
	}

	s.records[key] = rec
	return true
}

func (s *store) addRecord(rec aRecord) bool {
	rec.Name = normalizeName(rec.Name)
	rec.Type = normalizeRecordType(rec.Type)
	rec.Zone = normalizeName(rec.Zone)
	key := recordKey(rec)

	s.mu.Lock()
	defer s.mu.Unlock()

	if prev, ok := s.records[key]; ok && prev.Version > rec.Version {
		return false
	}
	s.records[key] = rec
	return true
}

func (s *store) deleteRecord(name string, version int64) bool {
	return s.deleteRecordByType(name, "", version)
}

func (s *store) deleteRecordByType(name, recordType string, version int64) bool {
	name = normalizeName(name)
	recordType = strings.ToUpper(strings.TrimSpace(recordType))

	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := false

	for key, prev := range s.records {
		if prev.Name != name {
			continue
		}
		if recordType != "" && prev.Type != recordType {
			continue
		}
		if prev.Version > version {
			continue
		}
		delete(s.records, key)
		deleted = true
	}

	return deleted
}

func (s *store) removeRecord(rec aRecord, version int64) bool {
	rec.Name = normalizeName(rec.Name)
	rec.Type = normalizeRecordType(rec.Type)
	rec.Zone = normalizeName(rec.Zone)
	key := recordKey(rec)

	s.mu.Lock()
	defer s.mu.Unlock()

	prev, ok := s.records[key]
	if !ok {
		return false
	}
	if prev.Version > version {
		return false
	}
	delete(s.records, key)
	return true
}

func (s *store) getRecords(name string, qtype uint16) []aRecord {
	name = normalizeName(name)

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]aRecord, 0, 2)
	for _, rec := range s.records {
		if rec.Name != name {
			continue
		}
		switch qtype {
		case dns.TypeA:
			if rec.Type == "A" {
				out = append(out, rec)
			}
		case dns.TypeAAAA:
			if rec.Type == "AAAA" {
				out = append(out, rec)
			}
		case dns.TypeANY:
			out = append(out, rec)
		case dns.TypeTXT:
			if rec.Type == "TXT" {
				out = append(out, rec)
			}
		case dns.TypeCNAME:
			if rec.Type == "CNAME" {
				out = append(out, rec)
			}
		case dns.TypeMX:
			if rec.Type == "MX" {
				out = append(out, rec)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type == out[j].Type {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func (s *store) getRecord(name string) (aRecord, bool) {
	recs := s.getRecords(name, dns.TypeANY)
	if len(recs) == 0 {
		return aRecord{}, false
	}
	for _, rec := range recs {
		if rec.Type == "A" {
			return rec, true
		}
	}
	return recs[0], true
}

func (s *store) hasName(name string) bool {
	name = normalizeName(name)

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rec := range s.records {
		if rec.Name == name {
			return true
		}
	}
	return false
}

func recordKey(rec aRecord) string {
	val := ""
	switch rec.Type {
	case "A", "AAAA":
		val = strings.ToLower(strings.TrimSpace(rec.IP))
	case "TXT":
		val = rec.Text
	case "CNAME":
		val = normalizeName(rec.Target)
	case "MX":
		val = fmt.Sprintf("%d|%s", rec.Priority, normalizeName(rec.Target))
	}
	return rec.Name + "|" + rec.Type + "|" + val
}

func (s *store) listRecords() []aRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]aRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec)
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
	key := normalizeName(zone)

	s.mu.RLock()
	defer s.mu.RUnlock()

	z, ok := s.zones[key]
	return z, ok
}

func (s *store) listZones() []zoneConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]zoneConfig, 0, len(s.zones))
	for _, z := range s.zones {
		out = append(out, z)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Zone < out[j].Zone })
	return out
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
