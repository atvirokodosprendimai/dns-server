package main

import (
	"net/http"
	"sync"
	"time"

	"gorm.io/gorm"
)

type config struct {
	NodeID         string
	HTTPListen     string
	DNSUDPListen   string
	DNSTCPListen   string
	DBPath         string
	MigrationsDir  string
	DebugLog       bool
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
	ID        uint64    `json:"id,omitempty"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	IP        string    `json:"ip"`
	Text      string    `json:"text,omitempty"`
	Target    string    `json:"target,omitempty"`
	Priority  uint16    `json:"priority,omitempty"`
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
	Type       string      `json:"type,omitempty"`
	Zone       string      `json:"zone,omitempty"`
	Version    int64       `json:"version"`
	EventTime  time.Time   `json:"event_time"`
	ZoneConfig *zoneConfig `json:"zone_config,omitempty"`
}

type upsertRecordRequest struct {
	IP        string `json:"ip"`
	Type      string `json:"type,omitempty"`
	Text      string `json:"text,omitempty"`
	Target    string `json:"target,omitempty"`
	Priority  uint16 `json:"priority,omitempty"`
	TTL       uint32 `json:"ttl"`
	Zone      string `json:"zone"`
	Propagate *bool  `json:"propagate,omitempty"`
}

type upsertZoneRequest struct {
	NS        []string `json:"ns"`
	SOATTL    uint32   `json:"soa_ttl"`
	Propagate *bool    `json:"propagate,omitempty"`
}

type store struct {
	mu      sync.RWMutex
	records map[string]aRecord
	zones   map[string]zoneConfig
}

type recordModel struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	Name      string    `gorm:"size:255;index:idx_records_name_type,priority:1"`
	Type      string    `gorm:"size:10;index:idx_records_name_type,priority:2"`
	IP        string    `gorm:"size:45"`
	Text      string    `gorm:"type:text"`
	Target    string    `gorm:"size:255"`
	Priority  uint16    `gorm:"not null;default:0"`
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

func (recordModel) TableName() string {
	return "records"
}

func (zoneModel) TableName() string {
	return "zones"
}

type persistence struct {
	db *gorm.DB
}

type server struct {
	cfg     config
	data    *store
	persist *persistence
	start   time.Time
}
