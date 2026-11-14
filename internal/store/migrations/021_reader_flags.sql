-- Per-principal later / archived / starred flags on the reading state. A flag
-- is set when its timestamp is non-null, and the timestamp is when it was set.
ALTER TABLE reading_states ADD COLUMN later_at TEXT DEFAULT NULL;
ALTER TABLE reading_states ADD COLUMN archived_at TEXT DEFAULT NULL;
ALTER TABLE reading_states ADD COLUMN starred_at TEXT DEFAULT NULL;

-- reader_command_receipts carries a CHECK over command names that SQLite
-- cannot alter in place, so it is rebuilt. Dropping it would cascade-delete
-- the rows of the tables that reference it, so those are parked first and
-- restored afterwards.
CREATE TEMP TABLE reader_flags_park_overlay AS SELECT * FROM reader_tag_overlay_events;
CREATE TEMP TABLE reader_flags_park_effects AS SELECT * FROM reader_command_effects;
CREATE TEMP TABLE reader_flags_park_receipts AS SELECT * FROM reader_command_receipts;

DROP TABLE reader_command_effects;
DROP TABLE reader_tag_overlay_events;
DROP TABLE reader_command_receipts;

CREATE TABLE reader_command_receipts (
    command_id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    semantic_digest TEXT NOT NULL CHECK (length(semantic_digest) = 64),
    principal_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    command TEXT NOT NULL CHECK (command IN (
        'add_tag', 'remove_tag', 'append_capture_note', 'update_curated_note',
        'set_reading_progress', 'mark_read', 'mark_unread',
        'request_asset_acquisition', 'route', 'materialize',
        'set_later', 'set_archived', 'set_starred'
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

INSERT INTO reader_command_receipts SELECT * FROM reader_flags_park_receipts;

CREATE TABLE reader_tag_overlay_events (
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

INSERT INTO reader_tag_overlay_events SELECT * FROM reader_flags_park_overlay;

CREATE TABLE reader_command_effects (
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

INSERT INTO reader_command_effects SELECT * FROM reader_flags_park_effects;

DROP TABLE reader_flags_park_overlay;
DROP TABLE reader_flags_park_effects;
DROP TABLE reader_flags_park_receipts;
