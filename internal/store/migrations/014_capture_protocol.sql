-- Manifest-first capture protocol state. These rows deliberately reference the
-- accepted immutable revision so an asset upload can never be confused with a
-- same-named client variant from another capture or revision.

CREATE UNIQUE INDEX IF NOT EXISTS idx_capture_attempt_capture_revision
ON capture_attempts(capture_id, fragment_revision_id);

CREATE TABLE IF NOT EXISTS capture_asset_bindings (
    capture_id TEXT NOT NULL,
    fragment_revision_id TEXT NOT NULL,
    client_variant_id TEXT NOT NULL,
    asset_variant_id TEXT NOT NULL,
    instruction_action TEXT NOT NULL
        CHECK (instruction_action IN ('request_upload', 'reuse_blob', 'server_acquire', 'reference_only', 'rejected')),
    expected_digest TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY(capture_id, client_variant_id),
    FOREIGN KEY(capture_id, fragment_revision_id)
      REFERENCES capture_attempts(capture_id, fragment_revision_id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_revision_id) REFERENCES fragment_revisions(id) ON DELETE RESTRICT,
    FOREIGN KEY(asset_variant_id) REFERENCES asset_variants(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_capture_asset_bindings_variant
ON capture_asset_bindings(asset_variant_id, capture_id);

CREATE TABLE IF NOT EXISTS capture_asset_outcomes (
    capture_id TEXT NOT NULL,
    client_variant_id TEXT NOT NULL,
    outcome TEXT NOT NULL
        CHECK (outcome IN ('uploaded', 'already_available', 'not_available', 'failed', 'deferred')),
    digest TEXT NOT NULL DEFAULT '',
    byte_size INTEGER NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    reason TEXT NOT NULL DEFAULT '',
    retryable INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1)),
    updated_at TEXT NOT NULL,
    PRIMARY KEY(capture_id, client_variant_id),
    FOREIGN KEY(capture_id, client_variant_id)
      REFERENCES capture_asset_bindings(capture_id, client_variant_id) ON DELETE CASCADE
);

-- A completion report is immutable and idempotent. One capture can be
-- completed once, and its idempotency key cannot be reused by another capture.
CREATE TABLE IF NOT EXISTS capture_completion_reports (
    capture_id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    semantic_digest TEXT NOT NULL,
    report_json TEXT NOT NULL,
    status_json TEXT NOT NULL DEFAULT '{}',
    completion TEXT NOT NULL CHECK (completion IN ('complete', 'partial')),
    created_at TEXT NOT NULL,
    FOREIGN KEY(capture_id) REFERENCES capture_attempts(capture_id) ON DELETE CASCADE
);

-- Transactional outbox: provider/enrichment execution is deliberately owned by
-- later tasks, but its durable intent is part of manifest acceptance now.
CREATE TABLE IF NOT EXISTS capture_followup_outbox (
    id TEXT PRIMARY KEY,
    capture_id TEXT NOT NULL UNIQUE,
    fragment_revision_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'processing', 'complete', 'failed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(capture_id, fragment_revision_id)
      REFERENCES capture_attempts(capture_id, fragment_revision_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_capture_followup_outbox_state
ON capture_followup_outbox(state, created_at, id);
