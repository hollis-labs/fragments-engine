package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/store"
	"github.com/hollis-labs/libs/util/scheduler"
)

func TestIngestFireRecoveryAndFencing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fires.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewIngestScheduleRepository(st.DB)
	now := time.Date(2026, 10, 9, 1, 0, 0, 123456789, time.UTC)
	_, err = repo.CreateSchedule(ctx, domain.IngestSchedule{ID: "schedule", IngestName: "fixture", Enabled: true, CronExpr: "* * * * *", NextRun: now.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	creation := scheduler.FireCreation{ScheduleID: "schedule", ExpectedNext: now, NextRun: now.Add(time.Minute), Fire: scheduler.Fire{ID: "fire", ScheduleID: "schedule", ScheduledAt: now, Status: scheduler.FirePending, NextAttemptAt: now, JobType: "ingest_run", Payload: []byte(`{"ingest_name":"fixture"}`)}}
	if ok, err := repo.CreateFire(ctx, creation); err != nil || !ok {
		t.Fatalf("create=%v %v", ok, err)
	}
	if ok, err := repo.CreateFire(ctx, creation); err != nil || ok {
		t.Fatalf("duplicate=%v %v", ok, err)
	}
	creation.ExpectedNext = creation.NextRun
	creation.NextRun = creation.NextRun.Add(time.Minute)
	if ok, err := repo.CreateFire(ctx, creation); err != nil || ok {
		t.Fatalf("same fire identity=%v %v", ok, err)
	}
	sch, _, err := repo.GetSchedule(ctx, "schedule")
	if err != nil || sch.NextRun != now.Add(time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("duplicate advanced schedule: %+v %v", sch, err)
	}
	fires, err := repo.ListDueFires(ctx, now, 10)
	if err != nil || len(fires) != 1 {
		t.Fatalf("due=%v %v", fires, err)
	}
	claim := scheduler.FireClaim{FireID: "fire", ExpectedStatus: scheduler.FirePending, ClaimedAt: now, ClaimExpiresAt: now.Add(time.Minute)}
	fire, ok, err := repo.ClaimFire(ctx, claim)
	if err != nil || !ok || fire.Attempt != 1 {
		t.Fatalf("claim=%+v %v %v", fire, ok, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo = NewIngestScheduleRepository(st.DB)
	claim.ExpectedStatus = scheduler.FireClaimed
	claim.ExpectedAttempt = 1
	claim.ExpectedFiredAt = now
	claim.ClaimedAt = now.Add(30 * time.Second)
	if _, ok, err := repo.ClaimFire(ctx, claim); err != nil || ok {
		t.Fatalf("unexpired=%v %v", ok, err)
	}
	claim.ClaimedAt = now.Add(time.Minute)
	claim.ClaimExpiresAt = now.Add(2 * time.Minute)
	fire, ok, err = repo.ClaimFire(ctx, claim)
	if err != nil || !ok || fire.Attempt != 1 {
		t.Fatalf("recovery=%+v %v %v", fire, ok, err)
	}
	tr := scheduler.FireTransition{FireID: "fire", Attempt: 1, From: scheduler.FireClaimed, To: scheduler.FireSucceeded, ClaimedAt: now}
	if ok, err := repo.TransitionFire(ctx, tr); err != nil || ok {
		t.Fatalf("stale transition=%v %v", ok, err)
	}
	tr.ClaimedAt = claim.ClaimedAt
	if ok, err := repo.TransitionFire(ctx, tr); err != nil || !ok {
		t.Fatalf("current transition=%v %v", ok, err)
	}
	fires, err = repo.ListDueFires(ctx, now.Add(3*time.Minute), 10)
	if err != nil || len(fires) != 0 {
		t.Fatalf("terminal due=%v %v", fires, err)
	}
}
