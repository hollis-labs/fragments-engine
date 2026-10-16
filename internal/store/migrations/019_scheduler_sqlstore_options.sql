-- Additive DDL for libs/core/util v0.4.0 scheduler/sqlstore (schema/002_retention
-- and schema/003_options). 018 is not edited and every statement is idempotent.
-- Fire reads, claims and transitions delegate to the shared sqlstore, which
-- reads gosched_fire_options and the pruned fence.
-- Rollback (migrations here are forward-only): DROP TABLE gosched_fire_options,
-- gosched_schedule_options, gosched_pruned once empty.

CREATE TABLE IF NOT EXISTS gosched_pruned (
    schedule_id TEXT PRIMARY KEY,
    through_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS gosched_schedule_options (
    schedule_id TEXT PRIMARY KEY,
    options_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS gosched_fire_options (
    fire_id TEXT PRIMARY KEY,
    options_json TEXT NOT NULL DEFAULT '{}'
);
