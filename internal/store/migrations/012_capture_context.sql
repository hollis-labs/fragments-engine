-- Durable capture attempts and additive user/source observations.
--
-- Capture attempts are idempotent by both the client capture identifier and
-- its explicit idempotency key. The stored semantic digest lets the service
-- distinguish an exact retry from accidental key reuse with changed input.

CREATE TABLE IF NOT EXISTS capture_attempts (
    id TEXT PRIMARY KEY,
    capture_id TEXT NOT NULL UNIQUE,
    idempotency_key TEXT NOT NULL UNIQUE,
    semantic_digest TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    fragment_revision_id TEXT NOT NULL,
    fragment_outcome TEXT NOT NULL
        CHECK (fragment_outcome IN ('inserted', 'updated', 'skipped')),
    accepted_capture_count INTEGER NOT NULL CHECK (accepted_capture_count > 0),
    acceptance_result_json TEXT NOT NULL DEFAULT '{}',
    principal_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    client_kind TEXT NOT NULL,
    client_version TEXT NOT NULL,
    captured_at TEXT NOT NULL,
    submitted_url TEXT NOT NULL DEFAULT '',
    page_context_json TEXT NOT NULL DEFAULT '{}',
    extraction_adapter TEXT NOT NULL DEFAULT '',
    extraction_adapter_version TEXT NOT NULL DEFAULT '',
    extraction_json TEXT NOT NULL DEFAULT '{}',
    completion TEXT NOT NULL DEFAULT 'accepting'
        CHECK (completion IN ('accepting', 'transferring', 'complete', 'partial', 'failed')),
    warnings_json TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_revision_id) REFERENCES fragment_revisions(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_capture_attempts_fragment_captured
ON capture_attempts(fragment_id, captured_at DESC, capture_id);

CREATE TABLE IF NOT EXISTS capture_annotations (
    id TEXT PRIMARY KEY,
    capture_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('highlight', 'capture_note')),
    text TEXT NOT NULL CHECK (length(text) > 0),
    selector_json TEXT NOT NULL DEFAULT '{}',
    position_json TEXT NOT NULL DEFAULT '{}',
    actor_id TEXT NOT NULL,
    captured_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(capture_id) REFERENCES capture_attempts(capture_id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_capture_annotations_fragment_time
ON capture_annotations(fragment_id, captured_at, id);

-- Capture tags are set-valued per attribution source. The first observation
-- remains durable provenance. Another source may independently assert the same
-- display tag. Removal is intentionally absent from intake and belongs to an
-- explicit semantic command.
CREATE TABLE IF NOT EXISTS fragment_tag_observations (
    id TEXT PRIMARY KEY,
    fragment_id TEXT NOT NULL,
    value TEXT NOT NULL,
    normalized_value TEXT NOT NULL,
    attribution_source TEXT NOT NULL
        CHECK (attribution_source IN ('user', 'provider', 'deterministic', 'model')),
    observation_id TEXT NOT NULL DEFAULT '',
    capture_id TEXT DEFAULT NULL,
    producer TEXT NOT NULL DEFAULT '',
    producer_version TEXT NOT NULL DEFAULT '',
    actor_id TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(
        fragment_id, normalized_value, attribution_source,
        producer, producer_version, actor_id
    ),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(capture_id) REFERENCES capture_attempts(capture_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fragment_tag_observations_fragment
ON fragment_tag_observations(fragment_id, normalized_value, attribution_source);

-- Descriptive observations are append-only occurrences. Source descriptions
-- remain revision material, while this table retains who observed or asserted
-- each value and in which deliberate capture.
CREATE TABLE IF NOT EXISTS fragment_description_observations (
    id TEXT PRIMARY KEY,
    fragment_id TEXT NOT NULL,
    fragment_revision_id TEXT DEFAULT NULL,
    capture_id TEXT DEFAULT NULL,
    value TEXT NOT NULL CHECK (length(value) > 0),
    attribution_source TEXT NOT NULL
        CHECK (attribution_source IN ('source', 'user', 'provider', 'deterministic', 'model')),
    producer TEXT NOT NULL DEFAULT '',
    producer_version TEXT NOT NULL DEFAULT '',
    actor_id TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_revision_id) REFERENCES fragment_revisions(id) ON DELETE RESTRICT,
    FOREIGN KEY(capture_id) REFERENCES capture_attempts(capture_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fragment_description_observations_fragment
ON fragment_description_observations(fragment_id, observed_at, id);

-- Absence represents optimistic revision zero. Creation with expected revision
-- zero writes revision one. Each explicit replacement increments it.
CREATE TABLE IF NOT EXISTS curated_notes (
    fragment_id TEXT PRIMARY KEY,
    body_markdown TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    actor_id TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);
