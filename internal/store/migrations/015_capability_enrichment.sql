-- Revision-aware, per-capability enrichment. Source/browser and user facts are
-- append-only observations, while coverage is a replaceable resolution/work cursor.

CREATE UNIQUE INDEX IF NOT EXISTS idx_fragment_revisions_id_fragment
ON fragment_revisions(id, fragment_id);

CREATE TABLE IF NOT EXISTS enrichment_observations (
    id TEXT PRIMARY KEY,
    fragment_id TEXT NOT NULL,
    fragment_revision_id TEXT NOT NULL,
    capture_id TEXT DEFAULT NULL,
    capability TEXT NOT NULL
        CHECK (capability IN ('title', 'description', 'body', 'gallery_manifest',
          'original_media', 'thumbnail_or_poster', 'transcript', 'OCR', 'vision',
          'summary', 'tags', 'entities')),
    attribution_source TEXT NOT NULL
        CHECK (attribution_source IN ('source', 'user', 'provider', 'deterministic', 'model')),
    producer TEXT NOT NULL,
    producer_version TEXT NOT NULL,
    input_material_digest TEXT NOT NULL,
    input_asset_digests_json TEXT NOT NULL DEFAULT '[]',
    value_json TEXT NOT NULL,
    confidence REAL DEFAULT NULL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    actor_id TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL,
    asserted_at TEXT NOT NULL,
    expires_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(id, fragment_revision_id, capability),
    FOREIGN KEY(fragment_revision_id, fragment_id)
      REFERENCES fragment_revisions(id, fragment_id) ON DELETE CASCADE,
    FOREIGN KEY(capture_id, fragment_revision_id)
      REFERENCES capture_attempts(capture_id, fragment_revision_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_enrichment_observations_resolution
ON enrichment_observations(fragment_revision_id, capability, attribution_source,
  asserted_at DESC, id);

CREATE TABLE IF NOT EXISTS fragment_capability_coverage (
    fragment_id TEXT NOT NULL,
    fragment_revision_id TEXT NOT NULL,
    capability TEXT NOT NULL
        CHECK (capability IN ('title', 'description', 'body', 'gallery_manifest',
          'original_media', 'thumbnail_or_poster', 'transcript', 'OCR', 'vision',
          'summary', 'tags', 'entities')),
    state TEXT NOT NULL
        CHECK (state IN ('provided', 'missing', 'pending', 'failed', 'stale', 'not_applicable')),
    selected_observation_id TEXT DEFAULT NULL,
    detail TEXT NOT NULL DEFAULT '',
    error_class TEXT NOT NULL DEFAULT '' CHECK (error_class IN ('', 'retryable', 'permanent')),
    error_code TEXT NOT NULL DEFAULT '',
    error_retryable INTEGER NOT NULL DEFAULT 0 CHECK (error_retryable IN (0, 1)),
    requested_generation INTEGER NOT NULL DEFAULT 0 CHECK (requested_generation >= 0),
    satisfied_generation INTEGER NOT NULL DEFAULT 0 CHECK (satisfied_generation >= 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY(fragment_revision_id, capability),
    FOREIGN KEY(fragment_revision_id, fragment_id)
      REFERENCES fragment_revisions(id, fragment_id) ON DELETE CASCADE,
    FOREIGN KEY(selected_observation_id, fragment_revision_id, capability)
      REFERENCES enrichment_observations(id, fragment_revision_id, capability) ON DELETE RESTRICT,
    CHECK (state <> 'provided' OR selected_observation_id IS NOT NULL),
    CHECK (satisfied_generation <= requested_generation)
);

CREATE INDEX IF NOT EXISTS idx_fragment_capability_coverage_planning
ON fragment_capability_coverage(state, error_retryable, updated_at, fragment_revision_id);

CREATE TABLE IF NOT EXISTS enrichment_capability_requests (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    semantic_digest TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    fragment_revision_id TEXT NOT NULL,
    capability TEXT NOT NULL,
    requested_generation INTEGER NOT NULL CHECK (requested_generation > 0),
    requested_by TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(fragment_revision_id, capability, requested_generation),
    FOREIGN KEY(fragment_revision_id, fragment_id)
      REFERENCES fragment_revisions(id, fragment_id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_revision_id, capability)
      REFERENCES fragment_capability_coverage(fragment_revision_id, capability) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS enrichment_jobs (
    id TEXT PRIMARY KEY,
    fragment_id TEXT NOT NULL,
    fragment_revision_id TEXT NOT NULL,
    capability TEXT NOT NULL,
    trigger_kind TEXT NOT NULL
        CHECK (trigger_kind IN ('capture', 'missing', 'retry', 'stale', 'explicit')),
    coverage_version INTEGER NOT NULL CHECK (coverage_version > 0),
    request_generation INTEGER NOT NULL DEFAULT 0 CHECK (request_generation >= 0),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    claim_token TEXT NOT NULL DEFAULT '',
    claimed_by TEXT NOT NULL DEFAULT '',
    lease_expires_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(fragment_revision_id, capability, coverage_version, request_generation),
    UNIQUE(id, fragment_revision_id, capability),
    FOREIGN KEY(fragment_revision_id, fragment_id)
      REFERENCES fragment_revisions(id, fragment_id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_revision_id, capability)
      REFERENCES fragment_capability_coverage(fragment_revision_id, capability) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_enrichment_jobs_claim
ON enrichment_jobs(status, created_at, id);

CREATE TABLE IF NOT EXISTS enrichment_job_attempts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    fragment_revision_id TEXT NOT NULL,
    capability TEXT NOT NULL,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    claim_token TEXT NOT NULL UNIQUE,
    adapter TEXT NOT NULL,
    adapter_version TEXT NOT NULL,
    input_schema_id TEXT NOT NULL,
    input_schema_version TEXT NOT NULL,
    output_schema_id TEXT NOT NULL,
    output_schema_version TEXT NOT NULL,
    network_class TEXT NOT NULL CHECK (network_class IN ('none', 'public_read', 'authenticated_read')),
    effects_json TEXT NOT NULL,
    credential_refs_json TEXT NOT NULL,
    descriptor_json TEXT NOT NULL,
    input_material_digest TEXT NOT NULL,
    input_asset_digests_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    error_class TEXT NOT NULL DEFAULT '' CHECK (error_class IN ('', 'retryable', 'permanent')),
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    claimed_at TEXT NOT NULL,
    completed_at TEXT NOT NULL DEFAULT '',
    UNIQUE(job_id, attempt_number),
    FOREIGN KEY(job_id, fragment_revision_id, capability)
      REFERENCES enrichment_jobs(id, fragment_revision_id, capability) ON DELETE CASCADE
);

-- The capture outbox remains the durable manifest transaction seam. Planning
-- completion is recorded here, while logical provider jobs and their attempts live
-- independently in the tables above.
ALTER TABLE capture_followup_outbox ADD COLUMN planned_at TEXT NOT NULL DEFAULT '';

-- Deterministic compatibility seed for databases that accepted revisions
-- before per-capability coverage existed. Only immutable typed revision fields
-- become provided observations. Mutable legacy summary/metadata flags do not.
INSERT OR IGNORE INTO enrichment_observations (
  id, fragment_id, fragment_revision_id, capability, attribution_source,
  producer, producer_version, input_material_digest, value_json, actor_id,
  observed_at, asserted_at, created_at
)
SELECT 'migration015:' || r.id || ':title', r.fragment_id, r.id, 'title', 'source',
       r.normalizer_adapter, r.normalizer_version, r.material_digest,
       '{"text":' || json_quote(r.title) || '}', 'migration-015',
       r.observed_at, r.committed_at, r.committed_at
FROM fragment_revisions r WHERE length(r.title) > 0;

INSERT OR IGNORE INTO enrichment_observations (
  id, fragment_id, fragment_revision_id, capability, attribution_source,
  producer, producer_version, input_material_digest, value_json, actor_id,
  observed_at, asserted_at, created_at
)
SELECT 'migration015:' || r.id || ':description', r.fragment_id, r.id, 'description', 'source',
       r.normalizer_adapter, r.normalizer_version, r.material_digest,
       '{"text":' || json_quote(r.description) || '}', 'migration-015',
       r.observed_at, r.committed_at, r.committed_at
FROM fragment_revisions r WHERE length(r.description) > 0;

INSERT OR IGNORE INTO enrichment_observations (
  id, fragment_id, fragment_revision_id, capability, attribution_source,
  producer, producer_version, input_material_digest, value_json, actor_id,
  observed_at, asserted_at, created_at
)
SELECT 'migration015:' || r.id || ':body', r.fragment_id, r.id, 'body', 'source',
       r.normalizer_adapter, r.normalizer_version, r.material_digest,
       '{"format":' || json_quote(r.content_format) || ',"text":' || json_quote(r.content) || '}',
       'migration-015', r.observed_at, r.committed_at, r.committed_at
FROM fragment_revisions r WHERE length(r.content) > 0;

WITH capabilities(capability) AS (VALUES
  ('title'), ('description'), ('body'), ('gallery_manifest'), ('original_media'),
  ('thumbnail_or_poster'), ('transcript'), ('OCR'), ('vision'), ('summary'),
  ('tags'), ('entities')
)
INSERT OR IGNORE INTO fragment_capability_coverage (
  fragment_id, fragment_revision_id, capability, state,
  selected_observation_id, detail, updated_at
)
SELECT r.fragment_id, r.id, c.capability,
       CASE
         WHEN c.capability = 'title' AND length(r.title) > 0 THEN 'provided'
         WHEN c.capability = 'description' AND length(r.description) > 0 THEN 'provided'
         WHEN c.capability = 'body' AND length(r.content) > 0 THEN 'provided'
         ELSE 'missing'
       END,
       CASE
         WHEN c.capability = 'title' AND length(r.title) > 0 THEN 'migration015:' || r.id || ':title'
         WHEN c.capability = 'description' AND length(r.description) > 0 THEN 'migration015:' || r.id || ':description'
         WHEN c.capability = 'body' AND length(r.content) > 0 THEN 'migration015:' || r.id || ':body'
         ELSE NULL
       END,
       'migration 015 compatibility seed', r.committed_at
FROM fragment_revisions r CROSS JOIN capabilities c;
