-- Cron-scheduled ingest runs. The go-scheduler engine reads due rows and the
-- ingest-schedule runner enqueues an ingest-run job for each fired schedule.
CREATE TABLE IF NOT EXISTS ingest_schedules (
    id          TEXT PRIMARY KEY,
    ingest_name TEXT NOT NULL,
    cron_expr   TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_run    TEXT NOT NULL DEFAULT '',
    next_run    TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ingest_schedules_due
ON ingest_schedules (enabled, next_run);
