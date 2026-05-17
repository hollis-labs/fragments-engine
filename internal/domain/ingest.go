package domain

import "time"

type IngestRun struct {
	Name       string
	Kind       string
	StartedAt  time.Time
	FinishedAt time.Time
	Inserted   int
	Updated    int
	Skipped    int
}

// IngestRunRecord is a persisted ingest run row, including async lifecycle
// status. Statuses: queued, running, done, failed.
type IngestRunRecord struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Inserted   int    `json:"inserted"`
	Updated    int    `json:"updated"`
	Skipped    int    `json:"skipped"`
	Error      string `json:"error,omitempty"`
}

type PipelineFragment struct {
	Source        string
	SourceType    string
	SourceID      string
	Title         string
	Content       string
	CreatedAt     time.Time
	Metadata      map[string]any
	CanonicalPath string
	Attachments   []PipelineAttachment
}

type PipelineAttachment struct {
	Kind         string         `json:"kind"`
	Role         string         `json:"role"`
	Name         string         `json:"name"`
	MIMEType     string         `json:"mime_type"`
	SourcePath   string         `json:"source_path"`
	ExternalURL  string         `json:"external_url"`
	StoragePath  string         `json:"storage_path"`
	SizeBytes    int64          `json:"size_bytes"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Source       string         `json:"source"`
	SourceItemID string         `json:"source_item_id"`
}

// IngestSchedule is a cron schedule for an ingest source. Times are RFC3339
// strings; empty last_run / next_run mean "never".
type IngestSchedule struct {
	ID         string `json:"id"`
	IngestName string `json:"ingest_name"`
	CronExpr   string `json:"cron_expr"`
	Enabled    bool   `json:"enabled"`
	LastRun    string `json:"last_run,omitempty"`
	NextRun    string `json:"next_run,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// IngestSchedulePayload is the opaque go-scheduler job payload carried by an
// ingest schedule — just enough for the runner to enqueue the ingest run.
type IngestSchedulePayload struct {
	IngestName string `json:"ingest_name"`
}

// IngestJobRecord is one row of the async ingest work queue (go-queue
// ingest_jobs / ingest_failed_jobs tables). The pending and failed queues
// share this shape; fields that do not apply to a given queue are zero/empty.
//   - pending rows carry MaxAttempts, EnqueuedAt and an empty LastError.
//   - failed (dead-letter) rows carry LastError and FailedAt but no
//     MaxAttempts (the failed-jobs table does not retain it).
type IngestJobRecord struct {
	ID          int64  `json:"id"`
	IngestName  string `json:"ingest_name"`
	RunID       int64  `json:"run_id,omitempty"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts"`
	EnqueuedAt  string `json:"enqueued_at,omitempty"`
	AvailableAt string `json:"available_at,omitempty"`
	ReservedAt  string `json:"reserved_at,omitempty"`
	FailedAt    string `json:"failed_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// IngestJobsView is the response of GET /v1/jobs/ingest: the live work queue
// and the dead-letter queue.
type IngestJobsView struct {
	Pending []IngestJobRecord `json:"pending"`
	Failed  []IngestJobRecord `json:"failed"`
}

// WorkerInfo describes one background runtime worker. Liveness is not tracked
// per-goroutine; Running reflects whether the worker is configured to run, and
// Detail carries the observable state (queue depth, last activity).
type WorkerInfo struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Running bool   `json:"running"`
	Detail  string `json:"detail"`
}

// SchedulerScheduleInfo is one cron schedule as surfaced by /v1/workers/status.
type SchedulerScheduleInfo struct {
	IngestName string `json:"ingest_name"`
	CronExpr   string `json:"cron_expr"`
	NextRun    string `json:"next_run,omitempty"`
	LastRun    string `json:"last_run,omitempty"`
	Enabled    bool   `json:"enabled"`
}

// SchedulerInfo summarizes the ingest cron scheduler.
type SchedulerInfo struct {
	Running   bool                    `json:"running"`
	Schedules []SchedulerScheduleInfo `json:"schedules"`
}

// WorkersStatusView is the response of GET /v1/workers/status.
type WorkersStatusView struct {
	Workers   []WorkerInfo  `json:"workers"`
	Scheduler SchedulerInfo `json:"scheduler"`
}

type IngestSummary struct {
	Name               string            `json:"name"`
	Kind               string            `json:"kind"`
	Enabled            bool              `json:"enabled"`
	SourceRoot         string            `json:"source_root"`
	Namespace          string            `json:"namespace"`
	Labels             map[string]string `json:"labels,omitempty"`
	ArchiveRoot        string            `json:"archive_root,omitempty"`
	CopyTextExports    bool              `json:"copy_text_exports,omitempty"`
	DeleteCopiedSource bool              `json:"delete_copied_source,omitempty"`
}

// IngestRecord is the complete config view of a single ingest source,
// including the raw rules map. Unlike IngestSummary — which projects onto a
// summary shape and decodes only chatgpt_export archive fields — this carries
// rules verbatim so the Sysop edit UI can round-trip them without data loss.
type IngestRecord struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Enabled    bool              `json:"enabled"`
	SourceRoot string            `json:"source_root"`
	Namespace  string            `json:"namespace"`
	Rules      map[string]any    `json:"rules,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type IngestValidationResult struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Enabled     bool     `json:"enabled"`
	Valid       bool     `json:"valid"`
	Errors      []string `json:"errors,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	SourceRoot  string   `json:"source_root"`
	ArchiveRoot string   `json:"archive_root,omitempty"`
}

type IngestPreviewItem struct {
	SourceID      string    `json:"source_id"`
	Title         string    `json:"title"`
	Source        string    `json:"source"`
	SourceType    string    `json:"source_type"`
	CreatedAt     time.Time `json:"created_at"`
	CanonicalPath string    `json:"canonical_path"`
	ContentBytes  int       `json:"content_bytes"`
}

type IngestPreviewResult struct {
	Name         string              `json:"name"`
	Kind         string              `json:"kind"`
	Enabled      bool                `json:"enabled"`
	SourceRoot   string              `json:"source_root"`
	PreviewCount int                 `json:"preview_count"`
	EarliestAt   *time.Time          `json:"earliest_at,omitempty"`
	LatestAt     *time.Time          `json:"latest_at,omitempty"`
	Items        []IngestPreviewItem `json:"items"`
}

type IngestArchivePolicyResult struct {
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	ArchiveRoot        string `json:"archive_root,omitempty"`
	CopyTextExports    bool   `json:"copy_text_exports"`
	DeleteCopiedSource bool   `json:"delete_copied_source"`
}
