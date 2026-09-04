-- Logical media, independently acquired variants, immutable revision-scoped
-- placement, and content-addressed FE custody.

CREATE TABLE IF NOT EXISTS media_assets (
    id TEXT PRIMARY KEY,
    identity_key TEXT NOT NULL UNIQUE,
    source_registration_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_media_id TEXT NOT NULL DEFAULT '',
    source_media_key TEXT NOT NULL,
    source_locator TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('image', 'video', 'audio', 'document', 'timed_text', 'other')),
    width INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
    height INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0),
    duration_seconds REAL NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
    page_count INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    alt_text TEXT NOT NULL DEFAULT '',
    source_authority TEXT NOT NULL,
    default_custody TEXT NOT NULL CHECK (default_custody IN ('reference', 'cache', 'mirror', 'adopted')),
    metadata_json TEXT NOT NULL DEFAULT '{}',
    legacy_attachment_id TEXT DEFAULT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(legacy_attachment_id) REFERENCES attachments(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_media_assets_provider_identity
ON media_assets(source_registration_id, provider, provider_media_id);

-- Source locators and client/source media keys are observations. In
-- particular, provider IDs keep the logical asset stable when a browser
-- adapter rotates a client key or signed locator.
CREATE TABLE IF NOT EXISTS media_asset_source_observations (
    id TEXT PRIMARY KEY,
    media_asset_id TEXT NOT NULL,
    source_media_key TEXT NOT NULL DEFAULT '',
    source_locator TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL,
    UNIQUE(media_asset_id, source_media_key, source_locator),
    FOREIGN KEY(media_asset_id) REFERENCES media_assets(id) ON DELETE CASCADE
);

-- Provider completion can supply a stronger provider-ID identity after the
-- browser first keyed the asset by a locator. The original stable asset ID is
-- retained and the stronger identity becomes an alias.
CREATE TABLE IF NOT EXISTS media_asset_identity_aliases (
    identity_key TEXT PRIMARY KEY,
    media_asset_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(media_asset_id) REFERENCES media_assets(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS media_blobs (
    digest TEXT PRIMARY KEY CHECK (length(digest) = 64),
    algorithm TEXT NOT NULL CHECK (algorithm = 'sha256'),
    storage_handle TEXT NOT NULL UNIQUE,
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    retention TEXT NOT NULL CHECK (retention IN ('indefinite', 'cache')),
    created_at TEXT NOT NULL,
    verified_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS asset_variants (
    id TEXT PRIMARY KEY,
    media_asset_id TEXT NOT NULL,
    variant_identity TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('original', 'preview', 'thumbnail', 'poster', 'audio', 'subtitles', 'transcript')),
    source_url TEXT NOT NULL DEFAULT '',
    source_expires_at TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    mime_type TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
    height INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0),
    duration_seconds REAL NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
    byte_size INTEGER NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    expected_digest TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL DEFAULT '',
    blob_digest TEXT DEFAULT NULL,
    custody TEXT NOT NULL CHECK (custody IN ('reference', 'cache', 'mirror', 'adopted')),
    acquisition_state TEXT NOT NULL CHECK (acquisition_state IN ('pending', 'available', 'reference_only', 'failed')),
    failure_code TEXT NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    failure_retryable INTEGER NOT NULL DEFAULT 0 CHECK (failure_retryable IN (0, 1)),
    retention TEXT NOT NULL CHECK (retention IN ('indefinite', 'cache', 'external')),
    metadata_json TEXT NOT NULL DEFAULT '{}',
    legacy_storage_path TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(media_asset_id, variant_identity),
    FOREIGN KEY(media_asset_id) REFERENCES media_assets(id) ON DELETE CASCADE,
    FOREIGN KEY(blob_digest) REFERENCES media_blobs(digest) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_asset_variants_asset_kind
ON asset_variants(media_asset_id, kind, id);

CREATE INDEX IF NOT EXISTS idx_asset_variants_blob
ON asset_variants(blob_digest);

CREATE TABLE IF NOT EXISTS asset_variant_source_observations (
    id TEXT PRIMARY KEY,
    asset_variant_id TEXT NOT NULL,
    source_url TEXT NOT NULL,
    source_expires_at TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL,
    UNIQUE(asset_variant_id, source_url, source_expires_at),
    FOREIGN KEY(asset_variant_id) REFERENCES asset_variants(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS attachment_refs (
    id TEXT PRIMARY KEY,
    fragment_revision_id TEXT NOT NULL,
    media_asset_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('primary', 'gallery_item', 'hero', 'inline', 'poster', 'transcript', 'other')),
    position INTEGER NOT NULL CHECK (position >= 0),
    caption TEXT NOT NULL DEFAULT '',
    source_context TEXT NOT NULL DEFAULT '',
    legacy_fragment_id TEXT DEFAULT NULL,
    legacy_attachment_id TEXT DEFAULT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(fragment_revision_id, position),
    FOREIGN KEY(fragment_revision_id) REFERENCES fragment_revisions(id) ON DELETE CASCADE,
    FOREIGN KEY(media_asset_id) REFERENCES media_assets(id) ON DELETE RESTRICT,
    FOREIGN KEY(legacy_fragment_id) REFERENCES fragments(id) ON DELETE SET NULL,
    FOREIGN KEY(legacy_attachment_id) REFERENCES attachments(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_attachment_refs_revision_position
ON attachment_refs(fragment_revision_id, position);

CREATE TABLE IF NOT EXISTS media_backfill_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    completed_at TEXT NOT NULL
);
