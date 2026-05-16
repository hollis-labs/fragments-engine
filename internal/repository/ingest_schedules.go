package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	scheduler "github.com/hollis-labs/go-scheduler"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// ingestScheduleJobType labels go-scheduler jobs produced by ingest schedules.
const ingestScheduleJobType = "ingest_run"

// IngestScheduleRepository persists ingest cron schedules. It also implements
// go-scheduler's Store interface (ListDueSchedules / ClaimAndUpdateScheduleRun
// / SetScheduleNextRun / DisableSchedule) over those rows.
type IngestScheduleRepository struct {
	db *sql.DB
}

func NewIngestScheduleRepository(db *sql.DB) *IngestScheduleRepository {
	return &IngestScheduleRepository{db: db}
}

// CreateSchedule inserts a new schedule, assigning an id when absent.
func (r *IngestScheduleRepository) CreateSchedule(ctx context.Context, in domain.IngestSchedule) (domain.IngestSchedule, error) {
	now := time.Now().UTC()
	if in.ID == "" {
		in.ID = stableID("ingest_schedule", in.IngestName, in.CronExpr, now.Format(time.RFC3339Nano))
	}
	in.CreatedAt = now.Format(time.RFC3339)
	in.UpdatedAt = in.CreatedAt
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO ingest_schedules (id, ingest_name, cron_expr, enabled, last_run, next_run, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.IngestName, in.CronExpr, boolToInt(in.Enabled), in.LastRun, in.NextRun, in.CreatedAt, in.UpdatedAt,
	); err != nil {
		return domain.IngestSchedule{}, fmt.Errorf("create ingest schedule: %w", err)
	}
	return in, nil
}

// GetSchedule returns a schedule by id; ok is false when it does not exist.
func (r *IngestScheduleRepository) GetSchedule(ctx context.Context, id string) (domain.IngestSchedule, bool, error) {
	var (
		s       domain.IngestSchedule
		enabled int
	)
	err := r.db.QueryRowContext(ctx, `
SELECT id, ingest_name, cron_expr, enabled, last_run, next_run, created_at, updated_at
FROM ingest_schedules WHERE id = ?`, id).
		Scan(&s.ID, &s.IngestName, &s.CronExpr, &enabled, &s.LastRun, &s.NextRun, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.IngestSchedule{}, false, nil
	}
	if err != nil {
		return domain.IngestSchedule{}, false, fmt.Errorf("get ingest schedule: %w", err)
	}
	s.Enabled = enabled != 0
	return s, true, nil
}

// ListSchedules returns all schedules ordered by id.
func (r *IngestScheduleRepository) ListSchedules(ctx context.Context) ([]domain.IngestSchedule, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, ingest_name, cron_expr, enabled, last_run, next_run, created_at, updated_at
FROM ingest_schedules ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list ingest schedules: %w", err)
	}
	defer rows.Close()
	out := make([]domain.IngestSchedule, 0)
	for rows.Next() {
		var (
			s       domain.IngestSchedule
			enabled int
		)
		if err := rows.Scan(&s.ID, &s.IngestName, &s.CronExpr, &enabled, &s.LastRun, &s.NextRun, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan ingest schedule: %w", err)
		}
		s.Enabled = enabled != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateSchedule rewrites a schedule's mutable fields.
func (r *IngestScheduleRepository) UpdateSchedule(ctx context.Context, in domain.IngestSchedule) error {
	in.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE ingest_schedules
SET ingest_name = ?, cron_expr = ?, enabled = ?, last_run = ?, next_run = ?, updated_at = ?
WHERE id = ?`,
		in.IngestName, in.CronExpr, boolToInt(in.Enabled), in.LastRun, in.NextRun, in.UpdatedAt, in.ID)
	if err != nil {
		return fmt.Errorf("update ingest schedule: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update ingest schedule rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("ingest schedule %q not found", in.ID)
	}
	return nil
}

// DeleteSchedule removes a schedule by id.
func (r *IngestScheduleRepository) DeleteSchedule(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM ingest_schedules WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete ingest schedule: %w", err)
	}
	return nil
}

// --- go-scheduler Store implementation ---

// ListDueSchedules returns up to limit enabled schedules due at or before now.
func (r *IngestScheduleRepository) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]scheduler.Schedule, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, ingest_name, cron_expr, last_run, next_run
FROM ingest_schedules
WHERE enabled = 1 AND next_run != '' AND next_run <= ?
ORDER BY next_run ASC
LIMIT ?`, now.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("list due ingest schedules: %w", err)
	}
	defer rows.Close()
	var out []scheduler.Schedule
	for rows.Next() {
		var id, ingestName, cronExpr, lastRun, nextRun string
		if err := rows.Scan(&id, &ingestName, &cronExpr, &lastRun, &nextRun); err != nil {
			return nil, fmt.Errorf("scan due ingest schedule: %w", err)
		}
		payload, err := json.Marshal(domain.IngestSchedulePayload{IngestName: ingestName})
		if err != nil {
			return nil, fmt.Errorf("encode ingest schedule payload: %w", err)
		}
		out = append(out, scheduler.Schedule{
			ID:       id,
			CronExpr: cronExpr,
			LastRun:  parseRFC3339(lastRun),
			NextRun:  parseRFC3339(nextRun),
			Enabled:  true,
			JobType:  ingestScheduleJobType,
			Payload:  payload,
		})
	}
	return out, rows.Err()
}

// ClaimAndUpdateScheduleRun atomically advances a schedule, but only if its
// stored next_run still equals expectedNext — the compare-and-set that
// prevents double-dispatch.
func (r *IngestScheduleRepository) ClaimAndUpdateScheduleRun(ctx context.Context, id string, expectedNext, lastRun, nextRun time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE ingest_schedules
SET last_run = ?, next_run = ?, updated_at = ?
WHERE id = ? AND next_run = ?`,
		lastRun.UTC().Format(time.RFC3339), nextRun.UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339), id, expectedNext.UTC().Format(time.RFC3339))
	if err != nil {
		return false, fmt.Errorf("claim ingest schedule: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim ingest schedule rows: %w", err)
	}
	return n == 1, nil
}

// SetScheduleNextRun resets a schedule's next_run (engine requeue after a
// failed dispatch).
func (r *IngestScheduleRepository) SetScheduleNextRun(ctx context.Context, id string, nextRun time.Time) error {
	if _, err := r.db.ExecContext(ctx, `
UPDATE ingest_schedules SET next_run = ?, updated_at = ? WHERE id = ?`,
		nextRun.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), id); err != nil {
		return fmt.Errorf("set ingest schedule next run: %w", err)
	}
	return nil
}

// DisableSchedule marks a schedule disabled (engine call after a one-time fire).
func (r *IngestScheduleRepository) DisableSchedule(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `
UPDATE ingest_schedules SET enabled = 0, updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id); err != nil {
		return fmt.Errorf("disable ingest schedule: %w", err)
	}
	return nil
}

func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
