package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	scheduler "github.com/hollis-labs/go-scheduler"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

// RunIngestScheduler starts the go-scheduler engine that fires cron-scheduled
// ingest runs. It blocks until ctx is cancelled. Runs in the server runtime
// alongside RunQueueDrainer and RunIngestWorker.
func RunIngestScheduler(ctx context.Context, cfgPath string) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("ingest scheduler config error: %v", err)
		return
	}
	st, err := openStoreWithRetry(config.ExpandHome(cfg.Database.Path))
	if err != nil {
		log.Printf("ingest scheduler store error: %v", err)
		return
	}
	defer st.Close()

	engine := scheduler.New(
		repository.NewIngestScheduleRepository(st.DB),
		&ingestScheduleRunner{cfgPath: cfgPath},
	)
	engine.Start()
	defer engine.Stop()
	<-ctx.Done()
}

// ingestScheduleRunner implements go-scheduler's Runner: when a schedule fires
// it enqueues an ingest-run job onto the go-queue ingest worker.
type ingestScheduleRunner struct {
	cfgPath string
}

func (r *ingestScheduleRunner) Enqueue(ctx context.Context, job scheduler.Job) error {
	var payload domain.IngestSchedulePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode ingest schedule payload: %w", err)
	}
	cfg, err := config.Load(r.cfgPath)
	if err != nil {
		return err
	}
	ingestCfg, _, err := config.FindIngest(cfg, payload.IngestName)
	if err != nil {
		return fmt.Errorf("resolve scheduled ingest %q: %w", payload.IngestName, err)
	}
	instance, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	if _, err := instance.Fragments.EnqueueIngestRun(ctx, instance.IngestQueue, ingestCfg); err != nil {
		return fmt.Errorf("enqueue scheduled ingest run: %w", err)
	}
	return nil
}
