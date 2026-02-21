package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

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
			Type:      r.Type,
			IP:        r.IP,
			Text:      r.Text,
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
	rec.Type = normalizeRecordType(rec.Type)
	if rec.Type == "" {
		rec.Type = "A"
	}

	var existing recordModel
	err := p.db.First(&existing, "name = ? AND type = ?", rec.Name, rec.Type).Error
	if err == nil && existing.Version > rec.Version {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("lookup record: %w", err)
	}

	model := recordModel{
		Name:      rec.Name,
		Type:      rec.Type,
		IP:        rec.IP,
		Text:      rec.Text,
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

func (p *persistence) deleteRecord(name, recordType string, version int64) error {
	name = normalizeName(name)
	recordType = strings.ToUpper(strings.TrimSpace(recordType))

	query := p.db.Model(&recordModel{}).Where("name = ?", name)
	if recordType == "A" || recordType == "AAAA" {
		query = query.Where("type = ?", recordType)
	}

	var records []recordModel
	if err := query.Find(&records).Error; err != nil {
		return fmt.Errorf("lookup records before delete: %w", err)
	}

	for _, rec := range records {
		if rec.Version > version {
			continue
		}
		if err := p.db.Delete(&recordModel{}, "name = ? AND type = ?", rec.Name, rec.Type).Error; err != nil {
			return fmt.Errorf("delete record: %w", err)
		}
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
