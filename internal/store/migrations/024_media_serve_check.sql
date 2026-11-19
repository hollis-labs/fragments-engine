-- serve_checked records that a variant's stored bytes have been checked
-- against the media types the Reader serves. The outcome lives in columns
-- that already exist: a servable variant has its verified mime_type, and an
-- unservable one keeps its bytes and state but carries failure_code
-- 'unsupported_media_type'. Rows stored before this column existed are 0 and
-- are checked once by the reconcile pass at server start.
ALTER TABLE asset_variants ADD COLUMN serve_checked INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_asset_variants_serve_unchecked
ON asset_variants(id) WHERE serve_checked = 0 AND blob_digest IS NOT NULL;
