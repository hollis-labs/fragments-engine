package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	scheduler "github.com/hollis-labs/go-scheduler"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

// IngestScheduleService is the CRUD surface for ingest cron schedules. The
// go-scheduler engine that fires them runs separately in the app runtime.
type IngestScheduleService struct {
	repo *repository.IngestScheduleRepository
}

func NewIngestScheduleService(repo *repository.IngestScheduleRepository) *IngestScheduleService {
	return &IngestScheduleService{repo: repo}
}

// IngestScheduleInput is the mutable surface accepted by Create and Update.
type IngestScheduleInput struct {
	IngestName string
	CronExpr   string
	Enabled    bool
}

// List returns all ingest schedules.
func (s *IngestScheduleService) List(ctx context.Context) ([]domain.IngestSchedule, error) {
	return s.repo.ListSchedules(ctx)
}

// Create validates the input and persists a new schedule with its initial
// next_run computed from the cron expression.
func (s *IngestScheduleService) Create(ctx context.Context, cfg config.Config, in IngestScheduleInput) (domain.IngestSchedule, error) {
	if err := validateScheduleInput(cfg, in); err != nil {
		return domain.IngestSchedule{}, err
	}
	next, err := scheduler.NextRun(in.CronExpr, time.Now().UTC())
	if err != nil {
		return domain.IngestSchedule{}, ValidationError{Msg: err.Error()}
	}
	return s.repo.CreateSchedule(ctx, domain.IngestSchedule{
		IngestName: strings.TrimSpace(in.IngestName),
		CronExpr:   strings.TrimSpace(in.CronExpr),
		Enabled:    in.Enabled,
		NextRun:    next.UTC().Format(time.RFC3339),
	})
}

// Update validates the input and rewrites an existing schedule, recomputing
// next_run from the (possibly changed) cron expression.
func (s *IngestScheduleService) Update(ctx context.Context, cfg config.Config, id string, in IngestScheduleInput) (domain.IngestSchedule, error) {
	existing, ok, err := s.repo.GetSchedule(ctx, id)
	if err != nil {
		return domain.IngestSchedule{}, err
	}
	if !ok {
		return domain.IngestSchedule{}, ValidationError{Msg: fmt.Sprintf("ingest schedule %q not found", id)}
	}
	if err := validateScheduleInput(cfg, in); err != nil {
		return domain.IngestSchedule{}, err
	}
	next, err := scheduler.NextRun(in.CronExpr, time.Now().UTC())
	if err != nil {
		return domain.IngestSchedule{}, ValidationError{Msg: err.Error()}
	}
	existing.IngestName = strings.TrimSpace(in.IngestName)
	existing.CronExpr = strings.TrimSpace(in.CronExpr)
	existing.Enabled = in.Enabled
	existing.NextRun = next.UTC().Format(time.RFC3339)
	if err := s.repo.UpdateSchedule(ctx, existing); err != nil {
		return domain.IngestSchedule{}, err
	}
	return existing, nil
}

// Delete removes a schedule by id.
func (s *IngestScheduleService) Delete(ctx context.Context, id string) error {
	_, ok, err := s.repo.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ValidationError{Msg: fmt.Sprintf("ingest schedule %q not found", id)}
	}
	return s.repo.DeleteSchedule(ctx, id)
}

// validateScheduleInput enforces ingest existence and cron validity. Failures
// are ValidationError (HTTP 400).
func validateScheduleInput(cfg config.Config, in IngestScheduleInput) error {
	name := strings.TrimSpace(in.IngestName)
	if name == "" {
		return ValidationError{Msg: "ingest_name is required"}
	}
	if _, _, err := config.FindIngest(cfg, name); err != nil {
		return ValidationError{Msg: err.Error()}
	}
	if err := scheduler.ValidateCron(in.CronExpr); err != nil {
		return ValidationError{Msg: err.Error()}
	}
	return nil
}
