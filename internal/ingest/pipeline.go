package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type Source interface {
	Kind() string
	Collect(context.Context, config.IngestConfig) ([]domain.PipelineFragment, error)
}

type Stage interface {
	Name() string
	Run(context.Context, *StageContext) error
}

type StageContext struct {
	IngestConfig config.IngestConfig
	Candidate    domain.PipelineFragment
	Fragment     domain.Fragment
	Outcome      repository.UpsertOutcome
	Now          time.Time
}

type Pipeline struct {
	sources map[string]Source
	repo    *repository.FragmentRepository
	stages  []Stage
	now     func() time.Time
	vision  analyze.VisionAnalyzer
}

func NewPipeline(repo *repository.FragmentRepository, vision analyze.VisionAnalyzer, stages []Stage, sources ...Source) *Pipeline {
	index := make(map[string]Source, len(sources))
	for _, source := range sources {
		index[source.Kind()] = source
	}
	return &Pipeline{
		sources: index,
		repo:    repo,
		stages:  stages,
		now:     time.Now,
		vision:  vision,
	}
}

// Stages returns the pipeline stages so callers can drive them directly.
func (p *Pipeline) Stages() []Stage {
	return p.stages
}

func (p *Pipeline) Run(ctx context.Context, ingestCfg config.IngestConfig) (domain.IngestRun, error) {
	source, ok := p.sources[ingestCfg.Kind]
	if !ok {
		return domain.IngestRun{}, fmt.Errorf("pipeline: unsupported ingest kind %q", ingestCfg.Kind)
	}
	startedAt := p.now().UTC()
	collected, err := source.Collect(ctx, ingestCfg)
	if err != nil {
		return domain.IngestRun{}, err
	}

	run := domain.IngestRun{
		Name:      ingestCfg.Name,
		Kind:      ingestCfg.Kind,
		StartedAt: startedAt,
	}
	now := p.now().UTC()
	for _, candidate := range collected {
		candidate, err = EnrichAttachmentContent(ctx, candidate, p.vision)
		if err != nil {
			return domain.IngestRun{}, err
		}
		if err := validateEnrichedCandidate(candidate); err != nil {
			return domain.IngestRun{}, err
		}
		fragment, err := repository.BuildFragment(candidate, ingestCfg.Name, now)
		if err != nil {
			return domain.IngestRun{}, err
		}
		outcome, err := p.repo.Upsert(ctx, fragment)
		if err != nil {
			return domain.IngestRun{}, err
		}
		stageCtx := &StageContext{
			IngestConfig: ingestCfg,
			Candidate:    candidate,
			Fragment:     fragment,
			Outcome:      outcome,
			Now:          now,
		}
		for _, stage := range p.stages {
			if err := stage.Run(ctx, stageCtx); err != nil {
				return domain.IngestRun{}, fmt.Errorf("stage %s: %w", stage.Name(), err)
			}
		}
		switch outcome {
		case repository.UpsertInserted:
			run.Inserted++
		case repository.UpsertUpdated:
			run.Updated++
		default:
			run.Skipped++
		}
	}
	run.FinishedAt = p.now().UTC()
	if err := p.repo.RecordIngestRun(ctx, run); err != nil {
		return domain.IngestRun{}, err
	}
	return run, nil
}

type InboxStage struct {
	inbox *repository.InboxRepository
}

func NewInboxStage(inbox *repository.InboxRepository) *InboxStage {
	return &InboxStage{inbox: inbox}
}

func (s *InboxStage) Name() string {
	return "stage_inbox"
}

func (s *InboxStage) Run(ctx context.Context, stageCtx *StageContext) error {
	if stageCtx.Outcome == repository.UpsertSkipped {
		return nil
	}
	if stageCtx.Fragment.Status != domain.FragmentStatusInbox {
		return nil
	}
	return s.inbox.Stage(ctx, stageCtx.Fragment.ID, "awaiting routing", stageCtx.Now)
}

type RouteStage struct {
	fragments   *repository.FragmentRepository
	attachments *repository.AttachmentRepository
	routes      *repository.RoutingRepository
	inbox       *repository.InboxRepository
	retrier     DeliveryRetrier
}

type DeliveryRetrier interface {
	EnqueueDestinationRetry(context.Context, string, string, string, domain.DeliveryRetryConfig) error
}

func NewRouteStage(fragments *repository.FragmentRepository, attachments *repository.AttachmentRepository, routes *repository.RoutingRepository, inbox *repository.InboxRepository, retrier DeliveryRetrier) *RouteStage {
	return &RouteStage{
		fragments:   fragments,
		attachments: attachments,
		routes:      routes,
		inbox:       inbox,
		retrier:     retrier,
	}
}

func (s *RouteStage) Name() string {
	return "route_match"
}

func (s *RouteStage) Run(ctx context.Context, stageCtx *StageContext) error {
	if stageCtx.Outcome == repository.UpsertSkipped {
		return nil
	}

	routes, err := s.routes.ListRoutes(ctx)
	if err != nil {
		return err
	}

	var matched *domain.Route
	fragmentEntities := extract.FromFragment(stageCtx.Fragment)
	for i := range routes {
		if routeMatches(routes[i], stageCtx.Fragment, fragmentEntities) {
			matched = &routes[i]
			break
		}
	}

	if matched == nil {
		return s.routes.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID: stageCtx.Fragment.ID,
			Decision:   "inbox",
			Reason:     "no_route_match",
			CreatedAt:  stageCtx.Now,
		})
	}

	if !matched.AutoRoute {
		return s.routes.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID:    stageCtx.Fragment.ID,
			RouteID:       matched.ID,
			DestinationID: matched.DestinationID,
			Decision:      "inbox",
			Reason:        "matched_manual_review",
			CreatedAt:     stageCtx.Now,
		})
	}

	destination, err := s.routes.GetDestination(ctx, matched.DestinationID)
	if err != nil {
		return err
	}
	attachments, err := s.attachments.ListByFragment(ctx, stageCtx.Fragment.ID)
	if err != nil {
		return err
	}
	routedFragment := stageCtx.Fragment
	routedFragment.Status = domain.FragmentStatusRouted
	delivery, err := s.executeDestination(ctx, destination, routedFragment, attachments)
	if err != nil {
		reason := encodeDeliveryReason("destination_error", deliveryReasonPayload{Attempts: delivery.Attempts, Error: err.Error()})
		if s.retrier != nil {
			retry := retryConfigForDestination(destination)
			if queueErr := s.retrier.EnqueueDestinationRetry(ctx, stageCtx.Fragment.ID, matched.ID, matched.DestinationID, retry); queueErr == nil {
				reason = EncodeDeliveryQueued(delivery.Attempts, err.Error())
			}
		}
		return s.routes.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID:    stageCtx.Fragment.ID,
			RouteID:       matched.ID,
			DestinationID: matched.DestinationID,
			Decision:      "inbox",
			Reason:        reason,
			CreatedAt:     stageCtx.Now,
		})
	}

	if err := s.fragments.UpdateStatus(ctx, stageCtx.Fragment.ID, domain.FragmentStatusRouted); err != nil {
		return err
	}
	if len(delivery.PublishedAttachments) > 0 {
		if err := s.attachments.UpdateFragmentAttachmentStoragePaths(ctx, stageCtx.Fragment.ID, delivery.PublishedAttachments); err != nil {
			return err
		}
	}
	stageCtx.Fragment = routedFragment
	if err := s.inbox.Remove(ctx, stageCtx.Fragment.ID); err != nil {
		return err
	}
	return s.routes.LogDecision(ctx, domain.RouteLogEntry{
		FragmentID:    stageCtx.Fragment.ID,
		RouteID:       matched.ID,
		DestinationID: matched.DestinationID,
		Decision:      "auto_route",
		Reason:        encodeDeliveryReason("matched_auto_route", deliveryReasonPayload{Attempts: delivery.Attempts, Ref: delivery.Ref}),
		CreatedAt:     stageCtx.Now,
	})
}

func routeMatches(route domain.Route, fragment domain.Fragment, entities []domain.FragmentEntity) bool {
	if route.MatchSource != "" && route.MatchSource != fragment.Source {
		return false
	}
	if route.MatchType != "" && route.MatchType != fragment.SourceType {
		return false
	}
	if route.MatchEntityKind != "" || route.MatchEntityValue != "" {
		matched := false
		for _, entity := range entities {
			if route.MatchEntityKind != "" && route.MatchEntityKind != entity.Kind {
				continue
			}
			if route.MatchEntityValue != "" && route.MatchEntityValue != entity.Value {
				continue
			}
			matched = true
			break
		}
		if !matched {
			return false
		}
	}
	return true
}

func (s *RouteStage) executeDestination(ctx context.Context, destination domain.Destination, fragment domain.Fragment, attachments []domain.FragmentAttachment) (DeliveryResult, error) {
	return ExecuteDestinationWithRetry(ctx, destination, fragment, attachments)
}
