-- Stable fragment identity and immutable source-material revisions.
--
-- Historical fragment primary keys are deliberately retained. The Go
-- backfill that follows schema migration chooses one deterministic canonical
-- fragment for each stable identity, records other historical rows as aliases,
-- and converts every historical snapshot into a revision. Keeping that data
-- transformation in Go lets it use the exact production digest algorithm.

ALTER TABLE fragments ADD COLUMN accepted_revision_id TEXT NOT NULL DEFAULT '';
ALTER TABLE fragments ADD COLUMN current_revision_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS fragment_source_identities (
    fragment_id TEXT PRIMARY KEY,
    source_registration_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_item_id TEXT NOT NULL DEFAULT '',
    source_item_key TEXT NOT NULL,
    source_locator TEXT NOT NULL DEFAULT '',
    segment_key TEXT NOT NULL DEFAULT 'root',
    submitted_url TEXT NOT NULL DEFAULT '',
    canonical_url TEXT NOT NULL DEFAULT '',
    source_adapter TEXT NOT NULL,
    source_adapter_version TEXT NOT NULL,
    canonicalizer_adapter TEXT NOT NULL,
    canonicalizer_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(source_registration_id, source_item_key, segment_key),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fragment_source_identity_provider_item
ON fragment_source_identities(provider, provider_item_id);

CREATE TABLE IF NOT EXISTS fragment_revisions (
    id TEXT PRIMARY KEY,
    fragment_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    material_digest TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    content_format TEXT NOT NULL,
    ordered_media_digest TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    normalizer_adapter TEXT NOT NULL,
    normalizer_version TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    legacy_fragment_id TEXT DEFAULT NULL,
    UNIQUE(fragment_id, ordinal),
    UNIQUE(fragment_id, material_digest),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(legacy_fragment_id) REFERENCES fragments(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_fragment_revisions_fragment_committed
ON fragment_revisions(fragment_id, ordinal DESC);

-- A historical database could contain several content-hash-addressed fragment
-- rows for one source item. Aliases retain those IDs and all inbound foreign
-- keys while identifying the canonical stable fragment for future writes.
CREATE TABLE IF NOT EXISTS fragment_identity_aliases (
    alias_fragment_id TEXT PRIMARY KEY,
    fragment_id TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT 'legacy_content_identity',
    created_at TEXT NOT NULL,
    FOREIGN KEY(alias_fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fragment_identity_aliases_fragment
ON fragment_identity_aliases(fragment_id);

CREATE TABLE IF NOT EXISTS fragment_identity_backfill_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    completed_at TEXT NOT NULL
);
