package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/recall"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// TestPipeline_DirectiveTaggedFragment_RoutesToWikiCallback covers
// CW-20260816-0018, the pilot's single production route
// (loom-architecture.md §6, §10): a fragment carrying an inline ::command
// directive flows through the real ingest pipeline stage order from
// internal/app/app.go (AttachmentStage -> DirectiveStage -> RouteStage ->
// InboxStage -> RecallStage), gets tagged Kind:"directive" by DirectiveStage
// (CW-20260816-0012), matches a broad Route{MatchEntityKind:"directive"}
// (matching ANY directive value -- Curator classifies content downstream,
// not FE) pointing at a "callback" destination with generator:"wiki_page"
// (CW-20260816-0011's schema), and is dispatched only via the async
// delivery_jobs queue (CW-20260816-0034's dispatch wiring), never
// synchronously.
//
// This test also covers the fix landed alongside it: RouteStage.Run
// previously matched only against extract.FromFragment's fixed kind set
// (workspace/repo/model/tool, computed in-memory), never against entities
// already persisted to fragment_entities earlier in the same pipeline run.
// DirectiveStage persists its Kind:"directive" rows via
// ReplaceFragmentEntitiesByKind before RouteStage runs, but RouteStage never
// read them back, so a Route{MatchEntityKind:"directive"} could never match
// via the real ingest hot path even though the entity row existed in the DB.
// RouteStage now also loads entities already persisted at that point in the
// run (see the entities field/param added to RouteStage), which is what
// this test exercises and would fail without.
func TestPipeline_DirectiveTaggedFragment_RoutesToWikiCallback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	attachmentRepo := repository.NewAttachmentRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	recallIndex := recall.NewSQLiteIndexer(fragmentRepo, entityRepo)
	defer recallIndex.Close()
	ctx := context.Background()

	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, entityRepo, inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}
	queueSvc.SetAttachmentRepository(attachmentRepo)

	// Target is never dialed -- callback destinations must never fire
	// synchronously (CW-20260816-0034) -- so a plausible-looking placeholder
	// is enough; this test proves the match+enqueue mechanism, not delivery.
	dest, err := routingRepo.AddDestination(ctx, domain.Destination{
		Name:       "nanite-wiki-callback",
		Kind:       "callback",
		ConfigJSON: `{"target":"http://127.0.0.1:0/nanite/wake","generator":"wiki_page"}`,
	})
	if err != nil {
		t.Fatalf("add callback destination: %v", err)
	}

	// Per the pilot's decided match criteria: broad match on ANY directive
	// tag (MatchEntityValue left empty), since the pilot has exactly one
	// wiki bundle and one output type -- Curator classifies content
	// downstream, not FE.
	route, err := routingRepo.AddRoute(ctx, domain.Route{
		Name:            "nanite-wiki-route",
		MatchEntityKind: "directive",
		DestinationID:   dest.ID,
		AutoRoute:       true,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	content := "::draft Write the nanite wiki page for the Loom pilot route\n\nSome supporting body text for the fragment."
	candidate := domain.PipelineFragment{
		Source:     "manual",
		SourceType: "text",
		SourceID:   "loom-pilot-route-fragment",
		Title:      "Loom pilot route fragment",
		Content:    content,
		CreatedAt:  time.Now().UTC(),
	}
	fragment, err := repository.BuildFragment(candidate, "test", time.Now().UTC())
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	outcome, err := fragmentRepo.Upsert(ctx, fragment)
	if err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	stageCtx := &ingest.StageContext{
		Candidate: candidate,
		Fragment:  fragment,
		Outcome:   outcome,
		Now:       time.Now().UTC(),
	}

	// Mirrors internal/app/app.go's real stage order exactly: DirectiveStage
	// before RouteStage, RecallStage last.
	stages := []ingest.Stage{
		ingest.NewAttachmentStage(attachmentRepo),
		ingest.NewDirectiveStage(entityRepo),
		ingest.NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, entityRepo, queueSvc),
		ingest.NewInboxStage(inboxRepo),
		ingest.NewRecallStage(recallIndex),
	}

	for _, stage := range stages {
		if err := stage.Run(ctx, stageCtx); err != nil {
			t.Fatalf("stage %s: %v", stage.Name(), err)
		}
		if stage.Name() == "directive_tag" {
			mid, err := entityRepo.ListByFragment(ctx, fragment.ID)
			if err != nil {
				t.Fatalf("list entities after directive stage: %v", err)
			}
			found := false
			for _, e := range mid {
				if e.Kind == "directive" && e.Value == "draft" {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected Kind:directive Value:draft entity to exist before RouteStage runs, got %+v", mid)
			}
		}
	}

	// The route must have matched and enqueued a delivery job, not fallen
	// through to "no_route_match".
	stats, err := queueSvc.Stats(ctx)
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.Pending != 1 {
		t.Fatalf("expected exactly 1 pending delivery_jobs row (match+enqueue), got %+v", stats)
	}

	var jobCount int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery_jobs WHERE json_extract(payload, '$.destination_id') = ? AND json_extract(payload, '$.fragment_id') = ?`,
		dest.ID, fragment.ID,
	).Scan(&jobCount); err != nil {
		t.Fatalf("count delivery_jobs rows: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("expected exactly 1 delivery_jobs row for this fragment/destination, got %d", jobCount)
	}

	// Fragment must remain unrouted: callback dispatch is queue-only until
	// the queue drainer actually delivers it (out of scope for this test,
	// already covered by CW-20260816-0034's tests).
	stored, err := fragmentRepo.GetByID(ctx, fragment.ID)
	if err != nil {
		t.Fatalf("get fragment: %v", err)
	}
	if stored.Status == domain.FragmentStatusRouted {
		t.Fatalf("expected fragment to remain unrouted until the queue drainer delivers it, got status %s", stored.Status)
	}

	// route_log must record the match and that it was queued by design, not
	// after a failed synchronous attempt.
	logs, err := routingRepo.ListRouteLog(ctx, fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one route log entry, got %d: %+v", len(logs), logs)
	}
	entry := logs[0]
	if entry.Decision != "inbox" {
		t.Fatalf("expected inbox decision (queued, not yet delivered), got %+v", entry)
	}
	if entry.RouteID != route.ID {
		t.Fatalf("expected route log entry to reference the matched route %q, got %+v", route.ID, entry)
	}
	if entry.DestinationID != dest.ID {
		t.Fatalf("expected route log entry to reference the callback destination %q, got %+v", dest.ID, entry)
	}
	if !strings.Contains(entry.Reason, "delivery_queued_by_design") {
		t.Fatalf("expected delivery_queued_by_design reason (queued by design, not after a failed attempt), got %q", entry.Reason)
	}
}
