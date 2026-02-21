-- +goose Up
CREATE TABLE IF NOT EXISTS records (
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    ip TEXT,
    text TEXT,
    target TEXT,
    ttl INTEGER NOT NULL,
    zone TEXT NOT NULL,
    updated_at DATETIME NOT NULL,
    version INTEGER NOT NULL,
    source TEXT NOT NULL,
    PRIMARY KEY (name, type)
);

CREATE INDEX IF NOT EXISTS idx_records_version ON records(version);

CREATE TABLE IF NOT EXISTS zones (
    zone TEXT PRIMARY KEY,
    ns_json TEXT NOT NULL,
    soa_ttl INTEGER NOT NULL,
    serial INTEGER NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_zones_serial ON zones(serial);

-- +goose Down
DROP INDEX IF EXISTS idx_zones_serial;
DROP TABLE IF EXISTS zones;
DROP INDEX IF EXISTS idx_records_version;
DROP TABLE IF EXISTS records;
