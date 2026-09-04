-- Idempotent semantic-command receipts for explicit media custody requests.
-- AssetVariant remains the lifecycle authority. This table records who asked
-- for which transition and which concrete variant owns the eventual bytes.

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_variants_id_asset
ON asset_variants(id, media_asset_id);

CREATE TABLE IF NOT EXISTS asset_acquisition_requests (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    semantic_digest TEXT NOT NULL,
    media_asset_id TEXT NOT NULL,
    asset_variant_id TEXT NOT NULL,
    variant_kind TEXT NOT NULL
        CHECK (variant_kind IN ('original', 'preview', 'thumbnail', 'poster',
          'audio', 'subtitles', 'transcript')),
    requested_custody TEXT NOT NULL
        CHECK (requested_custody IN ('cache', 'mirror', 'adopted')),
    requested_by TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    FOREIGN KEY(asset_variant_id, media_asset_id)
      REFERENCES asset_variants(id, media_asset_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_asset_acquisition_requests_target
ON asset_acquisition_requests(media_asset_id, variant_kind, created_at, id);
