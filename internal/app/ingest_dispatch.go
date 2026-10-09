package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/libs/util/scheduler"
)

// enqueueScheduledIngest commits the accepted fire identity, run and SQLite
// queue envelope together. A recovered fire cannot create a second run, even
// after the worker has removed the original envelope. The ingest_jobs envelope
// uses the app's existing published SQLite queue schema and default push policy.
func (a *App) enqueueScheduledIngest(ctx context.Context, fireID string, cfg config.IngestConfig) error {
	if fireID == "" {
		return fmt.Errorf("scheduled ingest requires fire identity")
	}
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Take the write lock before checking the durable accepted identity.
	res, err := tx.ExecContext(ctx, `INSERT INTO ingest_fire_dispatches (fire_id,run_id) VALUES (?,NULL) ON CONFLICT(fire_id) DO NOTHING`, fireID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return scheduler.ErrDuplicateJob
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO ingest_runs (ingest_name,ingest_kind,started_at,finished_at,status,error) VALUES (?,?,'','','queued','')`, cfg.Name, cfg.Kind)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ingest_fire_dispatches SET run_id=? WHERE fire_id=?`, id, fireID); err != nil {
		return err
	}
	payload, err := json.Marshal(service.IngestRunPayload{RunID: id, IngestName: cfg.Name})
	if err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	if _, err = tx.ExecContext(ctx, `INSERT INTO ingest_jobs (queue,type,payload,max_tries,available_at,created_at) VALUES (?,?,?,0,?,?)`, service.IngestQueueName, service.IngestRunJobType, payload, now, now); err != nil {
		return err
	}
	return tx.Commit()
}
