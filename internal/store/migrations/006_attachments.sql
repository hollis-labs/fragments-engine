CREATE TABLE IF NOT EXISTS attachments (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    mime_type TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    external_url TEXT NOT NULL DEFAULT '',
    storage_path TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    UNIQUE(kind, source_path, external_url, name)
);

CREATE TABLE IF NOT EXISTS fragment_attachments (
    fragment_id TEXT NOT NULL,
    attachment_id TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    source_item_id TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    PRIMARY KEY(fragment_id, attachment_id, role, source, source_item_id),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(attachment_id) REFERENCES attachments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fragment_attachments_fragment_id
ON fragment_attachments(fragment_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_attachments_kind_created_at
ON attachments(kind, created_at DESC);
