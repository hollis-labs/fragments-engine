package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// IngestJobQueueRepository reads the go-queue SQLite tables that back the
// async ingest worker. The go-queue Queue interface offers no listing API
// (only Pop/Size), so observability reads the underlying tables directly.
//
// Table names match app.ingestJobsTable / app.ingestFailedJobsTable; they are
// duplicated here to keep this repository free of an app-package import.
type IngestJobQueueRepository struct {
	db          *sql.DB
	jobsTable   string
	failedTable string
}

// NewIngestJobQueueRepository builds the repository over the standard ingest
// queue table names.
func NewIngestJobQueueRepository(db *sql.DB) *IngestJobQueueRepository {
	return &IngestJobQueueRepository{
		db:          db,
		jobsTable:   "ingest_jobs",
		failedTable: "ingest_failed_jobs",
	}
}

// ingestJobPayload mirrors service.IngestRunPayload. It is decoded locally to
// avoid a repository→service import.
type ingestJobPayload struct {
	RunID      int64  `json:"run_id"`
	IngestName string `json:"ingest_name"`
}

// decodeIngestJobPayload decodes a go-queue job payload. A decode error is
// returned (not swallowed) so callers can surface it rather than silently
// emitting a record with an empty ingest name / run id.
func decodeIngestJobPayload(raw []byte) (ingestJobPayload, error) {
	var p ingestJobPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return ingestJobPayload{}, fmt.Errorf("decode ingest job payload: %w", err)
	}
	return p, nil
}

func unixToRFC3339(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// ListPending returns the live (not-yet-dead-lettered) ingest work queue,
// oldest first. A row with a non-null reserved_at is in flight.
func (r *IngestJobQueueRepository) ListPending(ctx context.Context, limit int) ([]domain.IngestJobRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, type, payload, attempts, max_tries, reserved_at, available_at, created_at
FROM `+r.jobsTable+`
ORDER BY id ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending ingest jobs: %w", err)
	}
	defer rows.Close()
	out := make([]domain.IngestJobRecord, 0)
	for rows.Next() {
		var (
			id          int64
			jobType     string
			payload     []byte
			attempts    int
			maxTries    int
			reservedAt  sql.NullInt64
			availableAt int64
			createdAt   int64
		)
		if err := rows.Scan(&id, &jobType, &payload, &attempts, &maxTries, &reservedAt, &availableAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan pending ingest job: %w", err)
		}
		p, decErr := decodeIngestJobPayload(payload)
		status := "queued"
		reserved := ""
		if reservedAt.Valid && reservedAt.Int64 > 0 {
			status = "running"
			reserved = unixToRFC3339(reservedAt.Int64)
		}
		ingestName := p.IngestName
		lastErr := ""
		if decErr != nil {
			ingestName = "(unknown — payload decode failed)"
			lastErr = decErr.Error()
		}
		out = append(out, domain.IngestJobRecord{
			ID:          id,
			IngestName:  ingestName,
			RunID:       p.RunID,
			Type:        jobType,
			Status:      status,
			Attempts:    attempts,
			MaxAttempts: maxTries,
			EnqueuedAt:  unixToRFC3339(createdAt),
			AvailableAt: unixToRFC3339(availableAt),
			ReservedAt:  reserved,
			LastError:   lastErr,
		})
	}
	return out, rows.Err()
}

// ListFailed returns the dead-letter queue, newest failure first.
func (r *IngestJobQueueRepository) ListFailed(ctx context.Context, limit int) ([]domain.IngestJobRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, type, payload, error, attempts, failed_at
FROM `+r.failedTable+`
ORDER BY id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list failed ingest jobs: %w", err)
	}
	defer rows.Close()
	out := make([]domain.IngestJobRecord, 0)
	for rows.Next() {
		var (
			id       int64
			jobType  string
			payload  []byte
			errMsg   string
			attempts int
			failedAt int64
		)
		if err := rows.Scan(&id, &jobType, &payload, &errMsg, &attempts, &failedAt); err != nil {
			return nil, fmt.Errorf("scan failed ingest job: %w", err)
		}
		p, decErr := decodeIngestJobPayload(payload)
		ingestName := p.IngestName
		if decErr != nil {
			ingestName = "(unknown — payload decode failed)"
		}
		out = append(out, domain.IngestJobRecord{
			ID:         id,
			IngestName: ingestName,
			RunID:      p.RunID,
			Type:       jobType,
			Status:     "failed",
			Attempts:   attempts,
			FailedAt:   unixToRFC3339(failedAt),
			LastError:  errMsg,
		})
	}
	return out, rows.Err()
}

// PendingCount returns the number of rows in the live ingest work queue.
func (r *IngestJobQueueRepository) PendingCount(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+r.jobsTable).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending ingest jobs: %w", err)
	}
	return n, nil
}

// FailedCount returns the number of rows in the dead-letter queue.
func (r *IngestJobQueueRepository) FailedCount(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+r.failedTable).Scan(&n); err != nil {
		return 0, fmt.Errorf("count failed ingest jobs: %w", err)
	}
	return n, nil
}
