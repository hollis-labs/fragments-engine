-- Durable scheduler fire snapshots and accepted dispatch identities.
CREATE TABLE IF NOT EXISTS gosched_fires (
    id               TEXT PRIMARY KEY,
    schedule_id      TEXT    NOT NULL,
    scheduled_at     TEXT    NOT NULL,
    fired_at         TEXT    NOT NULL,
    claim_expires_at TEXT    NOT NULL,
    attempt          INTEGER NOT NULL DEFAULT 0,
    status           TEXT    NOT NULL,
    next_attempt_at  TEXT    NOT NULL,
    last_error       TEXT    NOT NULL DEFAULT '',
    retry_json       TEXT    NOT NULL DEFAULT '{}',
    job_type         TEXT    NOT NULL DEFAULT '',
    payload          BLOB
);

CREATE INDEX IF NOT EXISTS gosched_fires_due
    ON gosched_fires (status, next_attempt_at);

CREATE TABLE ingest_fire_dispatches (fire_id TEXT PRIMARY KEY, run_id INTEGER REFERENCES ingest_runs(id));
