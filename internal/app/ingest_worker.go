package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	queue "github.com/hollis-labs/go-queue"
	queuesqlite "github.com/hollis-labs/go-queue/driver/sqlite"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

// RunIngestWorker starts a go-queue worker that executes async ingest-run jobs.
// It blocks until ctx is cancelled. Runs alongside RunQueueDrainer in the
// server runtime.
func RunIngestWorker(ctx context.Context, cfgPath string) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("ingest worker config error: %v", err)
		return
	}
	st, err := openStoreWithRetry(config.ExpandHome(cfg.Database.Path))
	if err != nil {
		log.Printf("ingest worker store error: %v", err)
		return
	}
	defer st.Close()

	q, err := queuesqlite.New(st.DB, queuesqlite.Opts{
		Table:       ingestJobsTable,
		FailedTable: ingestFailedJobsTable,
	})
	if err != nil {
		log.Printf("ingest worker queue error: %v", err)
		return
	}

	w := queue.NewWorker(q, queue.WorkerOpts{
		Queues:       []string{service.IngestQueueName},
		PollInterval: 2 * time.Second,
		MaxTries:     3,
		RetryAfter:   30 * time.Second,
		OnFailed: func(job *queue.QueuedJob, err error) {
			log.Printf("ingest job %s permanently failed: %v", job.ID, err)
			// Mark the run failed so it does not linger as `queued` forever
			// when a job is exhausted by an early error (payload decode,
			// config load, app.Open) that never reached ExecuteIngestRun.
			failIngestRunForJob(context.Background(), cfgPath, job, err)
		},
		OnError: func(err error) {
			log.Printf("ingest worker error: %v", err)
		},
	})
	w.Register(service.IngestRunJobType, func(jobCtx context.Context, job *queue.QueuedJob) error {
		return runIngestJob(jobCtx, cfgPath, job)
	})
	if err := w.Start(ctx); err != nil && ctx.Err() == nil {
		log.Printf("ingest worker stopped: %v", err)
	}
}

// runIngestJob executes one ingest-run job: it opens an app instance, resolves
// the ingest config by name, and drives the pre-created run row to completion.
func runIngestJob(ctx context.Context, cfgPath string, job *queue.QueuedJob) error {
	var payload service.IngestRunPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode ingest job payload: %w", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	instance, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	ingestCfg, _, err := config.FindIngest(cfg, payload.IngestName)
	if err != nil {
		_ = instance.Fragments.FailIngestRun(ctx, payload.RunID, err.Error())
		return err
	}
	return instance.Fragments.ExecuteIngestRun(ctx, payload.RunID, ingestCfg)
}

// failIngestRunForJob marks the ingest_runs row referenced by a permanently
// failed job as failed. It is the last-resort cleanup for early failure paths
// (payload decode / config load / app.Open) that never reached
// ExecuteIngestRun and so never transitioned the run out of `queued`.
func failIngestRunForJob(ctx context.Context, cfgPath string, job *queue.QueuedJob, jobErr error) {
	var payload service.IngestRunPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.RunID == 0 {
		return
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return
	}
	instance, err := Open(ctx, cfg)
	if err != nil {
		return
	}
	defer instance.Close()
	_ = instance.Fragments.FailIngestRun(ctx, payload.RunID, "ingest job permanently failed: "+jobErr.Error())
}
