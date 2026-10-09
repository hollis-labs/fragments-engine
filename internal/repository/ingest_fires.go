package repository

import (
	"context"
	"encoding/json"
	"time"

	scheduler "github.com/hollis-labs/libs/util/scheduler"
	"github.com/hollis-labs/libs/util/scheduler/sqlstore"
)

// CreateFire advances the schedule and inserts the fire in one transaction.
// The schedule UPDATE is the first statement, so it is the compare-and-swap on
// ExpectedNext and takes the write lock before anything is read. If the fire
// ID already exists (including terminal fires) the transaction rolls back and
// neither record changes.
func (s *IngestScheduleRepository) CreateFire(ctx context.Context, creation scheduler.FireCreation) (bool, error) {
	retry, err := json.Marshal(creation.Fire.Retry)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE ingest_schedules SET last_run = ?, next_run = ?, updated_at = ?
WHERE id = ? AND enabled = 1 AND next_run = ?`,
		now.Format(time.RFC3339Nano), creation.NextRun.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339),
		creation.ScheduleID, creation.ExpectedNext.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	if n, rerr := res.RowsAffected(); rerr != nil || n != 1 {
		return false, rerr
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO gosched_fires
(id, schedule_id, scheduled_at, fired_at, claim_expires_at, attempt, status, next_attempt_at, last_error, retry_json, job_type, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		creation.Fire.ID, creation.ScheduleID, fireTime(creation.Fire.ScheduledAt),
		fireTime(creation.Fire.FiredAt), fireTime(creation.Fire.ClaimExpiresAt),
		creation.Fire.Attempt, string(creation.Fire.Status), fireTime(creation.Fire.NextAttemptAt),
		creation.Fire.LastError, string(retry), creation.Fire.JobType, creation.Fire.Payload)
	if err != nil {
		return false, err
	}
	if n, rerr := res.RowsAffected(); rerr != nil || n != 1 {
		return false, rerr // duplicate ID: rollback restores the schedule
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *IngestScheduleRepository) ListDueFires(ctx context.Context, now time.Time, limit int) ([]scheduler.Fire, error) {
	f, err := sqlstore.New(s.db)
	if err != nil {
		return nil, err
	}
	return f.ListDueFires(ctx, now, limit)
}
func (s *IngestScheduleRepository) ClaimFire(ctx context.Context, claim scheduler.FireClaim) (scheduler.Fire, bool, error) {
	f, err := sqlstore.New(s.db)
	if err != nil {
		return scheduler.Fire{}, false, err
	}
	return f.ClaimFire(ctx, claim)
}
func (s *IngestScheduleRepository) TransitionFire(ctx context.Context, tr scheduler.FireTransition) (bool, error) {
	f, err := sqlstore.New(s.db)
	if err != nil {
		return false, err
	}
	return f.TransitionFire(ctx, tr)
}
func fireTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

var _ scheduler.Store = (*IngestScheduleRepository)(nil)
