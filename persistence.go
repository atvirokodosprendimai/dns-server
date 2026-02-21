package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
)

func newPersistence(dbPath, migrationsDir string) (*persistence, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("open sql db: %w", err)
	}

	if err := runMigrations(sqlDB, migrationsDir); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &persistence{db: db}, nil
}

func runMigrations(db *sql.DB, migrationsDir string) error {
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	if err := goose.Up(db, migrationsDir); err != nil {
		return err
	}
	return nil
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
		s.addRecord(aRecord{
			ID:        r.ID,
			Name:      r.Name,
			Type:      r.Type,
			IP:        r.IP,
			Text:      r.Text,
			Target:    r.Target,
			Priority:  r.Priority,
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
	rec = normalizeRecord(rec)

	var existing []recordModel
	if err := p.db.Where("name = ? AND type = ?", rec.Name, rec.Type).Find(&existing).Error; err != nil {
		return fmt.Errorf("lookup record set: %w", err)
	}
	for _, row := range existing {
		if row.Version > rec.Version {
			return nil
		}
	}
	if err := p.db.Where("name = ? AND type = ?", rec.Name, rec.Type).Delete(&recordModel{}).Error; err != nil {
		return fmt.Errorf("delete existing record set: %w", err)
	}

	model := recordModelFrom(rec)
	if err := p.db.Create(&model).Error; err != nil {
		return fmt.Errorf("create record: %w", err)
	}

	return nil
}

func (p *persistence) addRecord(rec aRecord) error {
	rec.Type = normalizeRecordType(rec.Type)
	if rec.Type == "" {
		rec.Type = "A"
	}
	rec = normalizeRecord(rec)

	var existing recordModel
	err := recordIdentityQuery(p.db, rec).First(&existing).Error
	if err == nil && existing.Version > rec.Version {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("lookup record: %w", err)
	}

	model := recordModelFrom(rec)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := p.db.Create(&model).Error; err != nil {
			return fmt.Errorf("create record: %w", err)
		}
		return nil
	}

	if err := p.db.Model(&existing).Updates(model).Error; err != nil {
		return fmt.Errorf("update record: %w", err)
	}

	return nil
}

func (p *persistence) deleteRecord(name, recordType string, version int64) error {
	name = normalizeName(name)
	recordType = strings.ToUpper(strings.TrimSpace(recordType))

	query := p.db.Model(&recordModel{}).Where("name = ?", name)
	if recordType != "" {
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
		if err := p.db.Delete(&recordModel{}, "id = ?", rec.ID).Error; err != nil {
			return fmt.Errorf("delete record: %w", err)
		}
	}

	return nil
}

func (p *persistence) removeRecord(rec aRecord, version int64) error {
	rec = normalizeRecord(rec)

	var existing []recordModel
	if err := recordIdentityQuery(p.db, rec).Find(&existing).Error; err != nil {
		return fmt.Errorf("lookup records before remove: %w", err)
	}

	for _, row := range existing {
		if row.Version > version {
			continue
		}
		if err := p.db.Delete(&recordModel{}, "id = ?", row.ID).Error; err != nil {
			return fmt.Errorf("delete record by identity: %w", err)
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

func recordModelFrom(rec aRecord) recordModel {
	return recordModel{
		Name:      rec.Name,
		Type:      rec.Type,
		IP:        rec.IP,
		Text:      rec.Text,
		Target:    rec.Target,
		Priority:  rec.Priority,
		TTL:       rec.TTL,
		Zone:      rec.Zone,
		UpdatedAt: rec.UpdatedAt,
		Version:   rec.Version,
		Source:    rec.Source,
	}
}

func normalizeRecord(rec aRecord) aRecord {
	rec.Name = normalizeName(rec.Name)
	rec.Type = normalizeRecordType(rec.Type)
	rec.Zone = normalizeName(rec.Zone)
	rec.Target = normalizeName(rec.Target)
	rec.Text = strings.TrimSpace(rec.Text)
	if rec.Type == "A" || rec.Type == "AAAA" {
		if ip := net.ParseIP(strings.TrimSpace(rec.IP)); ip != nil {
			rec.IP = ip.String()
		}
	}
	return rec
}

func recordIdentityQuery(db *gorm.DB, rec aRecord) *gorm.DB {
	q := db.Model(&recordModel{}).Where("name = ? AND type = ?", rec.Name, rec.Type)
	switch rec.Type {
	case "A", "AAAA":
		q = q.Where("ip = ?", rec.IP)
	case "TXT":
		q = q.Where("text = ?", rec.Text)
	case "CNAME":
		q = q.Where("target = ?", rec.Target)
	case "MX":
		q = q.Where("target = ? AND priority = ?", rec.Target, rec.Priority)
	}
	return q
}
