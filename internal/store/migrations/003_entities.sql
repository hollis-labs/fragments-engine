CREATE TABLE IF NOT EXISTS entities (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    value TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(kind, value)
);

CREATE TABLE IF NOT EXISTS fragment_entities (
    fragment_id TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    confidence REAL NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    PRIMARY KEY(fragment_id, entity_id, source),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_entities_kind_value
ON entities(kind, value);

CREATE INDEX IF NOT EXISTS idx_fragment_entities_fragment_id
ON fragment_entities(fragment_id, confidence DESC);

CREATE INDEX IF NOT EXISTS idx_fragment_entities_entity_id
ON fragment_entities(entity_id, confidence DESC);
