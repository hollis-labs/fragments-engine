ALTER TABLE fragments ADD COLUMN summary_text TEXT NOT NULL DEFAULT '';
ALTER TABLE fragments ADD COLUMN indexed_at TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS fragment_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    fragment_id TEXT NOT NULL,
    related_fragment_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    score REAL NOT NULL DEFAULT 0,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    UNIQUE(fragment_id, related_fragment_id, kind),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(related_fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fragment_links_fragment_id
ON fragment_links(fragment_id, score DESC);
