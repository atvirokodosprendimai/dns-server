-- +goose Up
CREATE TABLE IF NOT EXISTS records_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    ip TEXT,
    text TEXT,
    target TEXT,
    priority INTEGER NOT NULL DEFAULT 0,
    ttl INTEGER NOT NULL,
    zone TEXT NOT NULL,
    updated_at DATETIME NOT NULL,
    version INTEGER NOT NULL,
    source TEXT NOT NULL
);

INSERT INTO records_new (name, type, ip, text, target, priority, ttl, zone, updated_at, version, source)
SELECT name, type, ip, text, target, 0, ttl, zone, updated_at, version, source FROM records;

DROP TABLE records;
ALTER TABLE records_new RENAME TO records;

CREATE INDEX IF NOT EXISTS idx_records_name_type ON records(name, type);
CREATE INDEX IF NOT EXISTS idx_records_version ON records(version);
CREATE UNIQUE INDEX IF NOT EXISTS idx_records_identity ON records(name, type, COALESCE(ip,''), COALESCE(text,''), COALESCE(target,''), priority);

-- +goose Down
DROP INDEX IF EXISTS idx_records_identity;
DROP INDEX IF EXISTS idx_records_name_type;

CREATE TABLE IF NOT EXISTS records_old (
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

INSERT OR REPLACE INTO records_old (name, type, ip, text, target, ttl, zone, updated_at, version, source)
SELECT name, type, ip, text, target, ttl, zone, updated_at, version, source
FROM records;

DROP TABLE records;
ALTER TABLE records_old RENAME TO records;
CREATE INDEX IF NOT EXISTS idx_records_version ON records(version);
