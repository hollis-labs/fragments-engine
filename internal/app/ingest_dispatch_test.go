package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/fragments-engine/internal/store"
	"github.com/hollis-labs/libs/util/queue/driver/sqlite"
	"github.com/hollis-labs/libs/util/scheduler"
)

func TestScheduledDispatchIsAtomicAndSurvivesQueueRemoval(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dispatch.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	cfg := config.IngestConfig{Name: "fixture", Kind: "filesystem_docs"}
	// Without a queue table, acceptance must roll back the run and identity.
	if err := a.enqueueScheduledIngest(ctx, "fire", cfg); err == nil {
		t.Fatal("expected missing queue failure")
	}
	for _, table := range []string{"ingest_runs", "ingest_fire_dispatches"} {
		var n int
		if err := st.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial %s=%d %v", table, n, err)
		}
	}
	q, err := sqlite.New(st.DB, sqlite.Opts{Table: ingestJobsTable, FailedTable: ingestFailedJobsTable})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.enqueueScheduledIngest(ctx, "fire", cfg); err != nil {
		t.Fatal(err)
	}
	job, err := q.Pop(ctx, service.IngestQueueName)
	if err != nil || job == nil {
		t.Fatalf("queued=%v %v", job, err)
	}
	if err := q.Delete(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := (&App{store: other}).enqueueScheduledIngest(ctx, "fire", cfg); !errors.Is(err, scheduler.ErrDuplicateJob) {
		t.Fatalf("replay=%v", err)
	}
	var n int
	if err := st.DB.QueryRow("SELECT count(*) FROM ingest_runs").Scan(&n); err != nil || n != 1 {
		t.Fatalf("runs=%d %v", n, err)
	}
	if n, err := q.Size(ctx, service.IngestQueueName); err != nil || n != 0 {
		t.Fatalf("duplicate queue=%d %v", n, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- a.enqueueScheduledIngest(ctx, "concurrent-fire", cfg) }()
	}
	wg.Wait()
	close(results)
	accepted, duplicates := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, scheduler.ErrDuplicateJob) {
			duplicates++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || duplicates != 1 {
		t.Fatalf("concurrent accepted=%d duplicates=%d", accepted, duplicates)
	}

}
