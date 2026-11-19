-- +goose Up
-- Transcript corpus, schema v1 (Postgres + pgvector). Fragments Engine is the
-- only writer; readers get SELECT only. All times are timestamptz UTC; an
-- unknown time is NULL, never fabricated.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE sources (
  source_id          text PRIMARY KEY,
  harness            text NOT NULL,
  host_id            text NOT NULL,
  source_kind        text NOT NULL,
  canonical_root_ref text NOT NULL,
  parser_version     text NOT NULL,
  UNIQUE (host_id, harness, canonical_root_ref)
);

CREATE TABLE sessions (
  session_id           text PRIMARY KEY,
  harness              text NOT NULL,
  native_session_id    text NOT NULL,
  segment_key          text NOT NULL,
  host_id              text NOT NULL DEFAULT 'unknown',
  agent_urn            text,
  kind                 text NOT NULL DEFAULT '',
  agent_type           text NOT NULL DEFAULT '',
  spawn_depth          integer NOT NULL DEFAULT 0 CHECK (spawn_depth >= 0),
  spawn_tool_use_id    text NOT NULL DEFAULT '',
  entrypoint           text NOT NULL DEFAULT '',
  parent_session_id    text REFERENCES sessions (session_id) DEFERRABLE INITIALLY DEFERRED,
  parent_native_id     text,
  is_fork              boolean NOT NULL DEFAULT false,
  model                text,
  cwd_redacted         text,
  git_branch_redacted  text,
  description_redacted text,
  started_at           timestamptz,
  ended_at             timestamptz,
  legacy_turn_count    bigint NOT NULL DEFAULT 0 CHECK (legacy_turn_count >= 0),
  ingest_state         text NOT NULL CHECK (ingest_state IN ('partial','current','rejected')),
  generation           bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
  content_revision     bigint NOT NULL DEFAULT 1 CHECK (content_revision > 0),
  content_digest       text NOT NULL,
  metadata             jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX sessions_native ON sessions (native_session_id);
CREATE INDEX sessions_parent ON sessions (parent_session_id);
CREATE INDEX sessions_started ON sessions (started_at DESC NULLS LAST, session_id);
CREATE INDEX sessions_harness_host ON sessions (harness, host_id, started_at DESC NULLS LAST);

CREATE TABLE session_aliases (
  source_id          text NOT NULL REFERENCES sources (source_id),
  source_session_key text NOT NULL,
  segment_key        text NOT NULL,
  session_id         text NOT NULL REFERENCES sessions (session_id),
  PRIMARY KEY (source_id, source_session_key, segment_key)
);

CREATE TABLE events (
  session_id       text NOT NULL REFERENCES sessions (session_id),
  event_id         text NOT NULL,
  ordinal          bigint NOT NULL,
  parent_event_id  text,
  kind             text NOT NULL,
  role             text,
  event_at         timestamptz,
  ingested_at      timestamptz NOT NULL,
  event_revision   bigint NOT NULL CHECK (event_revision > 0),
  request_id       text NOT NULL DEFAULT '',
  is_sidechain     boolean NOT NULL DEFAULT false,
  stop_reason      text NOT NULL DEFAULT '',
  has_thinking     boolean NOT NULL DEFAULT false,
  thinking_bytes   bigint NOT NULL DEFAULT 0 CHECK (thinking_bytes >= 0),
  text_redacted    text NOT NULL DEFAULT '',
  payload_redacted jsonb NOT NULL DEFAULT '{}',
  content_digest   text NOT NULL,
  PRIMARY KEY (session_id, event_id)
);
CREATE INDEX events_order ON events (session_id, ordinal, event_id);
CREATE INDEX events_at ON events (event_at);

CREATE TABLE tool_calls (
  session_id            text NOT NULL,
  call_id               text NOT NULL,
  event_id              text NOT NULL,
  tool_name             text NOT NULL,
  target_redacted       text,
  input_text_redacted   text NOT NULL,
  input_json_redacted   jsonb,
  input_complete        boolean NOT NULL,
  input_state           text NOT NULL CHECK (input_state IN ('complete','partial','unknown')),
  reported_result_bytes bigint NOT NULL DEFAULT 0 CHECK (reported_result_bytes >= 0),
  started_at            timestamptz,
  ended_at              timestamptz,
  status                text NOT NULL,
  PRIMARY KEY (session_id, call_id),
  FOREIGN KEY (session_id, event_id) REFERENCES events (session_id, event_id)
);
CREATE INDEX tool_calls_name ON tool_calls (tool_name, started_at);
CREATE INDEX tool_calls_time ON tool_calls (started_at);
CREATE INDEX tool_calls_status ON tool_calls (status, started_at);

CREATE TABLE tool_results (
  session_id      text NOT NULL,
  call_id         text NOT NULL,
  part_id         text NOT NULL,
  ordinal         bigint NOT NULL CHECK (ordinal >= 0),
  media_type      text NOT NULL,
  text_redacted   text,
  json_redacted   jsonb,
  bytes_redacted  bytea,
  result_complete boolean NOT NULL,
  content_digest  text NOT NULL,
  PRIMARY KEY (session_id, call_id, part_id),
  FOREIGN KEY (session_id, call_id) REFERENCES tool_calls (session_id, call_id),
  CONSTRAINT result_payload_one CHECK (num_nonnulls(text_redacted, json_redacted, bytes_redacted) = 1)
);

CREATE TABLE usage (
  session_id            text NOT NULL REFERENCES sessions (session_id),
  request_id            text NOT NULL,
  model                 text,
  event_at              timestamptz,
  input_tokens          bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens         bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  cache_read_tokens     bigint NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
  cache_creation_tokens bigint NOT NULL DEFAULT 0 CHECK (cache_creation_tokens >= 0),
  effort                text NOT NULL DEFAULT '',
  PRIMARY KEY (session_id, request_id)
);
CREATE INDEX usage_at ON usage (event_at);

CREATE TABLE enrichments (
  enrichment_id  text PRIMARY KEY,
  session_id     text NOT NULL REFERENCES sessions (session_id),
  content_revision bigint NOT NULL CHECK (content_revision > 0),
  tier           integer NOT NULL,
  prompt_version text NOT NULL,
  provider       text NOT NULL,
  model          text NOT NULL,
  content_digest text NOT NULL,
  status         text NOT NULL,
  result_redacted jsonb,
  quality        jsonb,
  error_class    text,
  tokens_in      bigint NOT NULL DEFAULT 0 CHECK (tokens_in >= 0),
  tokens_out     bigint NOT NULL DEFAULT 0 CHECK (tokens_out >= 0),
  calls          bigint NOT NULL DEFAULT 0 CHECK (calls >= 0),
  chunks         bigint NOT NULL DEFAULT 0 CHECK (chunks >= 0),
  run_id         text,
  request_prefix text NOT NULL DEFAULT '',
  freshness      text NOT NULL DEFAULT 'unverified' CHECK (freshness IN ('unverified','current','stale')),
  created_at     timestamptz NOT NULL,
  UNIQUE (session_id, tier, prompt_version, provider, model, content_digest)
);
CREATE INDEX enrichments_session ON enrichments (session_id, tier, created_at);
CREATE INDEX enrichments_status ON enrichments (status, tier);

CREATE TABLE enrichment_spend (
  spend_id           text PRIMARY KEY,
  enrichment_id      text REFERENCES enrichments (enrichment_id),
  session_id         text NOT NULL REFERENCES sessions (session_id),
  run_id             text NOT NULL,
  request_id         text NOT NULL,
  provider           text NOT NULL,
  model              text NOT NULL,
  tokens_in          bigint NOT NULL CHECK (tokens_in >= 0),
  tokens_out         bigint NOT NULL CHECK (tokens_out >= 0),
  tokens_cache_read  bigint NOT NULL DEFAULT 0 CHECK (tokens_cache_read >= 0),
  tokens_cache_write bigint NOT NULL DEFAULT 0 CHECK (tokens_cache_write >= 0),
  cost_usd           numeric CHECK (cost_usd IS NULL OR cost_usd >= 0),
  occurred_at        timestamptz NOT NULL
);
CREATE INDEX enrichment_spend_run ON enrichment_spend (run_id);

CREATE TABLE embedding_groups (
  group_id  text PRIMARY KEY,
  model     text NOT NULL,
  format    text NOT NULL,
  dimension integer NOT NULL CHECK (dimension > 0),
  metric    text NOT NULL CHECK (metric = 'cosine'),
  UNIQUE (model, format, dimension, metric),
  CONSTRAINT group_dimension_unique UNIQUE (group_id, dimension)
);

CREATE TABLE embeddings (
  session_id       text NOT NULL REFERENCES sessions (session_id),
  kind             text NOT NULL,
  chunk_idx        integer NOT NULL CHECK (chunk_idx >= 0),
  group_id         text NOT NULL,
  dimension        integer NOT NULL,
  enrichment_id    text REFERENCES enrichments (enrichment_id),
  content_revision bigint NOT NULL CHECK (content_revision > 0),
  text_digest      text NOT NULL,
  embedding        vector NOT NULL,
  freshness        text NOT NULL DEFAULT 'unverified' CHECK (freshness IN ('unverified','current','stale')),
  created_at       timestamptz NOT NULL,
  PRIMARY KEY (session_id, kind, chunk_idx, group_id),
  CONSTRAINT embedding_group_dimension FOREIGN KEY (group_id, dimension) REFERENCES embedding_groups (group_id, dimension),
  CONSTRAINT embedding_dimension CHECK (vector_dims(embedding) = dimension),
  -- pgvector already rejects NaN/Infinity on input; this rejects the zero vector, which has no cosine.
  CONSTRAINT embedding_nonzero CHECK (vector_norm(embedding) > 0)
);

CREATE TABLE ingest_checkpoints (
  source_id       text NOT NULL REFERENCES sources (source_id),
  source_file_key text NOT NULL,
  file_generation text NOT NULL,
  byte_offset     bigint NOT NULL CHECK (byte_offset >= 0),
  line_no         bigint NOT NULL CHECK (line_no >= 0),
  tail_digest     text NOT NULL,
  parser_version  text NOT NULL,
  checkpoint_at   timestamptz NOT NULL,
  metadata        jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY (source_id, source_file_key)
);

CREATE TABLE import_runs (
  run_id                 text PRIMARY KEY,
  source_db_identity     text NOT NULL,
  source_manifest_digest text NOT NULL,
  mapping_version        text NOT NULL,
  state                  text NOT NULL,
  evidence               jsonb NOT NULL,
  started_at             timestamptz NOT NULL,
  completed_at           timestamptz
);

CREATE TABLE pending_records (
  pending_id     text PRIMARY KEY,
  source_id      text REFERENCES sources (source_id),
  session_hint   text,
  record_kind    text NOT NULL,
  native_key     text NOT NULL,
  required_refs  jsonb NOT NULL,
  payload_redacted bytea NOT NULL,
  encoding       text NOT NULL,
  payload_digest text NOT NULL,
  parser_version text NOT NULL,
  reason         text NOT NULL,
  first_seen_at  timestamptz NOT NULL,
  last_seen_at   timestamptz NOT NULL,
  attempts       integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  state          text NOT NULL CHECK (state IN ('pending','resolved','rejected')),
  resolution_key text,
  UNIQUE (source_id, record_kind, native_key, payload_digest)
);

CREATE TABLE legacy_rows (
  import_run_id     text NOT NULL REFERENCES import_runs (run_id),
  source_table      text NOT NULL,
  source_pk         text NOT NULL,
  source_row_digest text NOT NULL,
  payload_redacted  bytea NOT NULL,
  encoding          text NOT NULL,
  mapped_targets    jsonb NOT NULL,
  disposition       text NOT NULL CHECK (disposition IN ('mapped','pending','derived_rebuilt','rejected')),
  reason            text,
  PRIMARY KEY (import_run_id, source_table, source_pk)
);

CREATE TABLE legacy_material (
  legacy_id          text PRIMARY KEY,
  session_id         text REFERENCES sessions (session_id),
  content_digest     text NOT NULL,
  redaction_version  integer NOT NULL,
  material_json_text text NOT NULL,
  title              text NOT NULL,
  content            text NOT NULL
);

-- Derived search projection. Canonical payloads stay whole in events/tool_*;
-- chunks (<= 4096 bytes, contiguous, non-overlapping) exist only for search.
-- The tsvector column and GIN index are generated by libs core/store/search in 0002.
CREATE TABLE search_documents (
  document_id       text PRIMARY KEY,
  session_id        text NOT NULL REFERENCES sessions (session_id),
  generation        bigint NOT NULL CHECK (generation > 0),
  source_revision   bigint NOT NULL CHECK (source_revision > 0),
  source_kind       text NOT NULL CHECK (source_kind IN ('event','tool_input','tool_result','enrichment','legacy_material')),
  entity_key        text NOT NULL,
  field             text NOT NULL,
  content_digest    text NOT NULL,
  projection_version text NOT NULL,
  text_config       text NOT NULL CHECK (text_config = 'simple'),
  source_bytes      bigint NOT NULL CHECK (source_bytes >= 0),
  chunk_count       integer NOT NULL CHECK (chunk_count >= 0),
  UNIQUE (session_id, source_kind, entity_key, field)
);
CREATE INDEX search_documents_session ON search_documents (session_id, source_kind);

CREATE TABLE search_chunks (
  chunk_id      text PRIMARY KEY,
  document_id   text NOT NULL REFERENCES search_documents (document_id) ON DELETE CASCADE,
  chunk_idx     integer NOT NULL CHECK (chunk_idx >= 0),
  byte_start    bigint NOT NULL CHECK (byte_start >= 0),
  byte_end      bigint NOT NULL CHECK (byte_end > byte_start),
  text_redacted text NOT NULL CHECK (octet_length(text_redacted) <= 4096),
  UNIQUE (document_id, chunk_idx),
  CHECK (byte_end - byte_start = octet_length(text_redacted))
);

-- Change journal. seq is allocated by locking the singleton feed_state row
-- inside each short FE write transaction; a bigserial would not give a
-- commit-visible order.
CREATE TABLE feed_state (
  singleton     boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  epoch         text NOT NULL,
  head          bigint NOT NULL DEFAULT 0,
  retained_from bigint NOT NULL DEFAULT 1
);

CREATE TABLE changes (
  seq              bigint PRIMARY KEY,
  session_id       text NOT NULL REFERENCES sessions (session_id),
  generation       bigint NOT NULL,
  content_revision bigint NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('event_upsert','tool_call_upsert','tool_result_upsert','session_update','enrichment_update','session_reset')),
  entity_key       text,
  recorded_at      timestamptz NOT NULL DEFAULT transaction_timestamp()
);
CREATE INDEX changes_session ON changes (session_id, seq);

-- +goose Down
DROP TABLE search_chunks, search_documents, changes, feed_state, legacy_material, legacy_rows, pending_records, import_runs,
  ingest_checkpoints, embeddings, embedding_groups, enrichment_spend, enrichments, usage,
  tool_results, tool_calls, events, session_aliases, sessions, sources;
