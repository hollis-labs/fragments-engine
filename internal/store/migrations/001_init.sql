CREATE TABLE IF NOT EXISTS fragments (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    source_type TEXT NOT NULL,
    source_id TEXT NOT NULL,
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    ingested_at TEXT NOT NULL,
    status TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    ingest_name TEXT NOT NULL,
    canonical_path TEXT NOT NULL DEFAULT '',
    UNIQUE(source, source_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_fragments_source_created_at
ON fragments(source, created_at DESC);

CREATE TABLE IF NOT EXISTS ingest_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ingest_name TEXT NOT NULL,
    ingest_kind TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    inserted_count INTEGER NOT NULL DEFAULT 0,
    updated_count INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS destinations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS routes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    match_source TEXT NOT NULL DEFAULT '',
    match_type TEXT NOT NULL DEFAULT '',
    destination_id TEXT NOT NULL,
    auto_route INTEGER NOT NULL DEFAULT 0,
    confidence_min REAL NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(destination_id) REFERENCES destinations(id)
);

CREATE TABLE IF NOT EXISTS inbox (
    fragment_id TEXT PRIMARY KEY,
    reason TEXT NOT NULL DEFAULT '',
    staged_at TEXT NOT NULL,
    route_id TEXT DEFAULT NULL,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(route_id) REFERENCES routes(id)
);

CREATE TABLE IF NOT EXISTS route_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    fragment_id TEXT NOT NULL,
    route_id TEXT DEFAULT NULL,
    destination_id TEXT DEFAULT NULL,
    decision TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(route_id) REFERENCES routes(id),
    FOREIGN KEY(destination_id) REFERENCES destinations(id)
);

CREATE VIRTUAL TABLE IF NOT EXISTS fragments_fts USING fts5(
    fragment_id UNINDEXED,
    title,
    content,
    tokenize = 'porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS fragments_ai
AFTER INSERT ON fragments BEGIN
  INSERT INTO fragments_fts(fragment_id, title, content)
  VALUES (new.id, new.title, new.content);
END;

CREATE TRIGGER IF NOT EXISTS fragments_ad
AFTER DELETE ON fragments BEGIN
  DELETE FROM fragments_fts WHERE fragment_id = old.id;
END;

CREATE TRIGGER IF NOT EXISTS fragments_au
AFTER UPDATE ON fragments BEGIN
  DELETE FROM fragments_fts WHERE fragment_id = old.id;
  INSERT INTO fragments_fts(fragment_id, title, content)
  VALUES (new.id, new.title, new.content);
END;
