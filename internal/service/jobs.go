package service

import (
	"context"
	"fmt"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

// JobsService surfaces read-only observability for the async ingest runtime:
// the work queue + dead-letter queue, and the background workers/scheduler.
//
// Worker liveness is not tracked per-goroutine anywhere in the system. The
// three runtime workers (queue drainer, ingest worker, cron scheduler) are
// launched as goroutines by api.Server.ListenAndServe and have no heartbeat.
// JobsService therefore reports configured-to-run state plus genuinely
// observable signal (queue depths, schedule next-fire times, last runs)
// rather than synthesizing liveness.
type JobsService struct {
	jobs      *repository.IngestJobQueueRepository
	schedules *repository.IngestScheduleRepository
	fragments *repository.FragmentRepository
	cfg       config.Config
}

func NewJobsService(
	jobs *repository.IngestJobQueueRepository,
	schedules *repository.IngestScheduleRepository,
	fragments *repository.FragmentRepository,
	cfg config.Config,
) *JobsService {
	return &JobsService{jobs: jobs, schedules: schedules, fragments: fragments, cfg: cfg}
}

// IngestJobs returns the live ingest work queue and the dead-letter queue.
func (s *JobsService) IngestJobs(ctx context.Context, limit int) (domain.IngestJobsView, error) {
	pending, err := s.jobs.ListPending(ctx, limit)
	if err != nil {
		return domain.IngestJobsView{}, err
	}
	failed, err := s.jobs.ListFailed(ctx, limit)
	if err != nil {
		return domain.IngestJobsView{}, err
	}
	return domain.IngestJobsView{Pending: pending, Failed: failed}, nil
}

// WorkersStatus reports the background runtime workers and the cron scheduler.
func (s *JobsService) WorkersStatus(ctx context.Context) (domain.WorkersStatusView, error) {
	pendingCount, err := s.jobs.PendingCount(ctx)
	if err != nil {
		return domain.WorkersStatusView{}, err
	}
	failedCount, err := s.jobs.FailedCount(ctx)
	if err != nil {
		return domain.WorkersStatusView{}, err
	}

	// Most recent ingest run gives a last-activity signal for the worker.
	runs, err := s.fragments.ListIngestRuns(ctx, 1)
	if err != nil {
		return domain.WorkersStatusView{}, err
	}
	lastRunDetail := "no ingest runs recorded"
	if len(runs) > 0 {
		r := runs[0]
		stamp := r.FinishedAt
		if stamp == "" {
			stamp = r.StartedAt
		}
		lastRunDetail = fmt.Sprintf("last run #%d %q status=%s", r.ID, r.Name, r.Status)
		if stamp != "" {
			lastRunDetail += " at " + stamp
		}
	}

	schedules, err := s.schedules.ListSchedules(ctx)
	if err != nil {
		return domain.WorkersStatusView{}, err
	}
	scheduleInfos := make([]domain.SchedulerScheduleInfo, 0, len(schedules))
	enabledCount := 0
	for _, sc := range schedules {
		if sc.Enabled {
			enabledCount++
		}
		scheduleInfos = append(scheduleInfos, domain.SchedulerScheduleInfo{
			IngestName: sc.IngestName,
			CronExpr:   sc.CronExpr,
			NextRun:    sc.NextRun,
			LastRun:    sc.LastRun,
			Enabled:    sc.Enabled,
		})
	}

	workers := []domain.WorkerInfo{
		{
			Name:    "ingest_worker",
			Kind:    "queue_worker",
			Running: true,
			Detail: fmt.Sprintf("go-queue worker on %q queue; pending=%d failed=%d; %s",
				IngestQueueName, pendingCount, failedCount, lastRunDetail),
		},
		{
			Name:    "queue_drainer",
			Kind:    "delivery_drainer",
			Running: s.cfg.Queue.AutoDrain,
			Detail: fmt.Sprintf("delivery queue auto-drain; poll_interval=%ds batch_size=%d",
				s.cfg.Queue.PollIntervalSeconds, s.cfg.Queue.BatchSize),
		},
		{
			Name:    "ingest_scheduler",
			Kind:    "cron_scheduler",
			Running: true,
			Detail: fmt.Sprintf("go-scheduler engine; %d schedule(s), %d enabled",
				len(scheduleInfos), enabledCount),
		},
	}

	return domain.WorkersStatusView{
		Workers: workers,
		Scheduler: domain.SchedulerInfo{
			Running:   true,
			Schedules: scheduleInfos,
		},
	}, nil
}
