-- Principal-scoped Reader state, tag overlays, semantic-command receipts, and
-- durable effect intents. Source revisions and processing state remain owned by
-- their existing tables.

CREATE TABLE IF NOT EXISTS reading_states (
    principal_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('unread', 'in_progress', 'read')),
    position_kind TEXT NOT NULL
        CHECK (position_kind IN ('none', 'article', 'video', 'gallery', 'document', 'audio')),
    position_json TEXT NOT NULL CHECK (
        json_valid(position_json) AND
        json_extract(position_json, '$.kind') = position_kind
    ),
    last_opened_at TEXT DEFAULT NULL,
    completed_at TEXT DEFAULT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY(principal_id, fragment_id),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_reading_states_fragment
ON reading_states(fragment_id, principal_id);

CREATE TABLE IF NOT EXISTS reader_command_aggregates (
    principal_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY(principal_id, fragment_id),
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

-- Tag overlay events are append-only. A suppress event hides the combined tag
-- only for this principal and never removes attributed source/provider/user
-- observations. A later add event deliberately makes the tag visible again.
CREATE TABLE IF NOT EXISTS reader_tag_overlay_events (
    command_id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    normalized_value TEXT NOT NULL CHECK (length(normalized_value) > 0),
    display_value TEXT NOT NULL CHECK (length(display_value) > 0),
    action TEXT NOT NULL CHECK (action IN ('add', 'suppress')),
    aggregate_revision INTEGER NOT NULL CHECK (aggregate_revision > 0),
    created_at TEXT NOT NULL,
    UNIQUE(principal_id, fragment_id, aggregate_revision),
    FOREIGN KEY(command_id) REFERENCES reader_command_receipts(command_id)
      ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_reader_tag_overlay_effective
ON reader_tag_overlay_events(principal_id, fragment_id, normalized_value,
                             aggregate_revision DESC);

CREATE TABLE IF NOT EXISTS reader_command_receipts (
    command_id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    semantic_digest TEXT NOT NULL CHECK (length(semantic_digest) = 64),
    principal_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    command TEXT NOT NULL CHECK (command IN (
        'add_tag', 'remove_tag', 'append_capture_note', 'update_curated_note',
        'set_reading_progress', 'mark_read', 'mark_unread',
        'request_asset_acquisition', 'route', 'materialize'
    )),
    state TEXT NOT NULL
        CHECK (state IN ('pending', 'executing', 'succeeded', 'failed', 'uncertain')),
    aggregate_revision INTEGER NOT NULL CHECK (aggregate_revision >= 0),
    result_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(result_json)),
    error_code TEXT NOT NULL DEFAULT '',
    error_detail TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_reader_command_receipts_fragment
ON reader_command_receipts(principal_id, fragment_id, created_at, command_id);

-- Only effects whose existing service boundary cannot join the command
-- transaction use this table. A claim is never automatically reclaimed from
-- executing/uncertain, avoiding duplicate external delivery after a crash.
CREATE TABLE IF NOT EXISTS reader_command_effects (
    command_id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('route', 'materialize')),
    target_id TEXT NOT NULL CHECK (length(target_id) > 0),
    state TEXT NOT NULL
        CHECK (state IN ('pending', 'executing', 'succeeded', 'failed', 'uncertain')),
    result_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(result_json)),
    error_detail TEXT NOT NULL DEFAULT '',
    claimed_at TEXT DEFAULT NULL,
    completed_at TEXT DEFAULT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(command_id) REFERENCES reader_command_receipts(command_id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_reader_command_effects_state
ON reader_command_effects(state, created_at, command_id);
