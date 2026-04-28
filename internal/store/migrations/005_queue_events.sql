CREATE TABLE IF NOT EXISTS queue_job_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    failed_job_id INTEGER DEFAULT NULL,
    fragment_id TEXT NOT NULL DEFAULT '',
    route_id TEXT NOT NULL DEFAULT '',
    destination_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE,
    FOREIGN KEY(route_id) REFERENCES routes(id),
    FOREIGN KEY(destination_id) REFERENCES destinations(id)
);

CREATE INDEX IF NOT EXISTS idx_queue_job_events_destination_created_at
ON queue_job_events(destination_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_queue_job_events_failed_job_id
ON queue_job_events(failed_job_id);
