package main

import (
	"sort"

	"github.com/miekg/dns"
)

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
