package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
	"github.com/hollis-labs/fragments-engine/internal/legacycapture"
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
	// LegacyCaptureApplied means canonical media and the compatibility
	// attachment projection were already committed atomically by the shared
	// legacy capture adapter. AttachmentStage must not dual-write enriched
	// display attachments into immutable source revision evidence.
	LegacyCaptureApplied bool
}

type Pipeline struct {
	sources map[string]Source
	repo    *repository.FragmentRepository
	stages  []Stage
	now     func() time.Time
	vision  analyze.VisionAnalyzer
	legacy  *legacycapture.Service
}

// SetLegacyCaptureService installs the shared application adapter used by all
// production non-browser ingests. NewPipeline retains its historical shape so
// focused stage tests can continue to construct a pipeline without app wiring.
func (p *Pipeline) SetLegacyCaptureService(service *legacycapture.Service) {
	if p != nil {
		p.legacy = service
	}
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

// Run executes an ingest and records the result as a new ingest_runs row.
// Used by synchronous callers (CLI). The async worker uses RunOnce against a
// pre-created run row instead.
func (p *Pipeline) Run(ctx context.Context, ingestCfg config.IngestConfig) (domain.IngestRun, error) {
	run, err := p.RunOnce(ctx, ingestCfg)
	if err != nil {
		return domain.IngestRun{}, err
	}
	if err := p.repo.RecordIngestRun(ctx, run); err != nil {
		return domain.IngestRun{}, err
	}
	return run, nil
}

// RunOnce executes an ingest and returns the run result WITHOUT persisting an
// ingest_runs row. Callers that track the run themselves (async worker) own
// the row lifecycle.
func (p *Pipeline) RunOnce(ctx context.Context, ingestCfg config.IngestConfig) (domain.IngestRun, error) {
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
	for _, materialCandidate := range collected {
		projectionCandidate, err := EnrichAttachmentContent(ctx, materialCandidate, p.vision)
		if err != nil {
			return domain.IngestRun{}, err
		}
		if err := validateEnrichedCandidate(projectionCandidate); err != nil {
			return domain.IngestRun{}, err
		}
		var fragment domain.Fragment
		var outcome repository.UpsertOutcome
		if p.legacy != nil {
			accepted, acceptErr := p.legacy.Accept(ctx, legacycapture.Request{
				IngestName: ingestCfg.Name, Material: materialCandidate,
				Projection: projectionCandidate,
				SourceTags: legacycapture.SourceTags(materialCandidate),
			})
			if acceptErr != nil {
				return domain.IngestRun{}, acceptErr
			}
			fragment, outcome = accepted.Fragment, accepted.Outcome
		} else {
			fragment, err = repository.BuildFragment(projectionCandidate, ingestCfg.Name, now)
			if err != nil {
				return domain.IngestRun{}, err
			}
			fragment, outcome, err = p.repo.UpsertResolved(ctx, fragment)
			if err != nil {
				return domain.IngestRun{}, err
			}
		}
		stageCtx := &StageContext{
			IngestConfig:         ingestCfg,
			Candidate:            projectionCandidate,
			Fragment:             fragment,
			Outcome:              outcome,
			Now:                  now,
			LegacyCaptureApplied: p.legacy != nil,
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
	entities    *repository.EntityRepository
	retrier     DeliveryRetrier
}

type DeliveryRetrier interface {
	EnqueueDestinationRetry(context.Context, string, string, string, domain.DeliveryRetryConfig) error
}

// NewRouteStage wires the entity repository in addition to extract.FromFragment
// (see Run) so that entity kinds written directly to fragment_entities by
// earlier stages in the same pipeline run -- e.g. DirectiveStage's
// Kind:"directive" rows (CW-20260816-0012) -- are visible to route matching.
// extract.FromFragment only ever produces its own fixed kind set (see
// extract.Kinds()), and those are computed in-memory rather than read back
// from the DB, since RecallStage (which persists them) runs after RouteStage;
// entities is what lets RouteStage also see entities other stages have
// already persisted by the time it runs.
func NewRouteStage(fragments *repository.FragmentRepository, attachments *repository.AttachmentRepository, routes *repository.RoutingRepository, inbox *repository.InboxRepository, entities *repository.EntityRepository, retrier DeliveryRetrier) *RouteStage {
	return &RouteStage{
		fragments:   fragments,
		attachments: attachments,
		routes:      routes,
		inbox:       inbox,
		entities:    entities,
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
	// fragmentEntities starts from the in-memory extract.FromFragment kinds
	// (workspace/repo/model/tool) -- those can't be read from the DB yet
	// because RecallStage, which persists them, runs after RouteStage -- and
	// is extended with whatever entities earlier stages in this same run
	// have already persisted (e.g. DirectiveStage's Kind:"directive" rows),
	// so directive-aware routes can match on the very first ingest pass.
	fragmentEntities := extract.FromFragment(stageCtx.Fragment)
	if s.entities != nil {
		persisted, err := s.entities.ListByFragment(ctx, stageCtx.Fragment.ID)
		if err != nil {
			return err
		}
		fragmentEntities = append(fragmentEntities, persisted...)
	}
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

	// Callback destinations must never fire synchronously against Curator's
	// endpoint (see domain.CallbackDestinationConfig): skip the inline
	// attempt entirely and go straight to the async delivery queue. This is
	// the routing hot path, so it must never block on live network I/O.
	if destination.Kind == "callback" {
		return s.enqueueCallbackDelivery(ctx, stageCtx, matched, destination)
	}

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

// enqueueCallbackDelivery handles the callback-destination branch of Run: it
// never attempts inline delivery, only enqueues. The fragment is left as-is
// (still awaiting routing) until the queue drainer (DeliveryQueueService.
// processJob) actually delivers it and moves it to Routed/removes it from
// the inbox, exactly like the queued-retry path for the other four
// destination kinds.
func (s *RouteStage) enqueueCallbackDelivery(ctx context.Context, stageCtx *StageContext, matched *domain.Route, destination domain.Destination) error {
	if s.retrier == nil {
		return s.routes.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID:    stageCtx.Fragment.ID,
			RouteID:       matched.ID,
			DestinationID: matched.DestinationID,
			Decision:      "inbox",
			Reason:        encodeDeliveryReason("callback_queue_unavailable", deliveryReasonPayload{Error: "delivery queue not configured"}),
			CreatedAt:     stageCtx.Now,
		})
	}
	retry := retryConfigForDestination(destination)
	if queueErr := s.retrier.EnqueueDestinationRetry(ctx, stageCtx.Fragment.ID, matched.ID, matched.DestinationID, retry); queueErr != nil {
		return s.routes.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID:    stageCtx.Fragment.ID,
			RouteID:       matched.ID,
			DestinationID: matched.DestinationID,
			Decision:      "inbox",
			Reason:        encodeDeliveryReason("callback_enqueue_error", deliveryReasonPayload{Error: queueErr.Error()}),
			CreatedAt:     stageCtx.Now,
		})
	}
	return s.routes.LogDecision(ctx, domain.RouteLogEntry{
		FragmentID:    stageCtx.Fragment.ID,
		RouteID:       matched.ID,
		DestinationID: matched.DestinationID,
		Decision:      "inbox",
		Reason:        EncodeDeliveryQueuedByDesign(destination.Kind),
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
