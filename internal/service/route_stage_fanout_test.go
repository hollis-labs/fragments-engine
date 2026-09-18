package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// TestRouteStage_FanOut_DeliversEveryMatchingRoute proves a single fragment
// matching two independent routes is delivered to BOTH destinations in one
// RouteStage.Run pass -- the case that drove fan-out: an agent-authored
// drop tagged for both corpus storage and a second destination.
func TestRouteStage_FanOut_DeliversEveryMatchingRoute(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	attachmentRepo := repository.NewAttachmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)

	rootA := filepath.Join(t.TempDir(), "corpus-a")
	rootB := filepath.Join(t.TempDir(), "corpus-b")
	destA, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "corpus-a",
		Kind:       "file",
		ConfigJSON: `{"root":"` + rootA + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination a: %v", err)
	}
	destB, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "corpus-b",
		Kind:       "file",
		ConfigJSON: `{"root":"` + rootB + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination b: %v", err)
	}

	routeA, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "route-a",
		MatchSource:   "agent",
		DestinationID: destA.ID,
		AutoRoute:     true,
	})
	if err != nil {
		t.Fatalf("add route a: %v", err)
	}
	routeB, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "route-b",
		MatchSource:   "agent",
		DestinationID: destB.ID,
		AutoRoute:     true,
	})
	if err != nil {
		t.Fatalf("add route b: %v", err)
	}

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "agent",
		SourceType:    "draft",
		SourceID:      "fanout-drop",
		Title:         "Draft: fan-out",
		Content:       "This fragment matches two routes and must reach both destinations.",
		CreatedAt:     time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/agent/draft/fanout-drop",
	}, "agent-intake", time.Date(2026, 9, 16, 10, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	outcome, err := fragmentRepo.Upsert(context.Background(), fragment)
	if err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}

	stage := ingest.NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, entityRepo, nil)
	stageCtx := &ingest.StageContext{
		Fragment: fragment,
		Outcome:  outcome,
		Now:      time.Now().UTC(),
	}
	if err := stage.Run(context.Background(), stageCtx); err != nil {
		t.Fatalf("RouteStage.Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(rootA, "fragments/agent/draft/fanout-drop", "fragment.md")); err != nil {
		t.Fatalf("expected destination a to receive the fragment bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootB, "fragments/agent/draft/fanout-drop", "fragment.md")); err != nil {
		t.Fatalf("expected destination b to receive the fragment bundle: %v", err)
	}

	logs, err := routingRepo.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 route_log entries (one per matched route), got %d: %+v", len(logs), logs)
	}
	seenRoutes := map[string]bool{}
	for _, entry := range logs {
		if entry.Decision != "auto_route" {
			t.Fatalf("expected auto_route decision, got %+v", entry)
		}
		seenRoutes[entry.RouteID] = true
	}
	if !seenRoutes[routeA.ID] || !seenRoutes[routeB.ID] {
		t.Fatalf("expected route_log entries for both routes, got %+v", logs)
	}

	stored, err := fragmentRepo.GetByID(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("get fragment: %v", err)
	}
	if stored.Status != domain.FragmentStatusRouted {
		t.Fatalf("expected fragment status routed, got %s", stored.Status)
	}

	items, err := inboxRepo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	for _, item := range items {
		if item.FragmentID == fragment.ID {
			t.Fatalf("expected fragment to be removed from inbox, still staged: %+v", item)
		}
	}
}

// TestRouteStage_FanOut_RecordsAttachmentStoragePathCollision covers the
// gap fan-out opened: two matched "file" routes both publish the same
// attachment, so only one storage_path survives. The later match (by route
// name order, "route-a" then "route-b") must record on its own route_log
// entry which earlier route it overwrote, instead of the collision
// happening silently.
func TestRouteStage_FanOut_RecordsAttachmentStoragePathCollision(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	attachmentRepo := repository.NewAttachmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)

	rootA := filepath.Join(t.TempDir(), "corpus-a")
	rootB := filepath.Join(t.TempDir(), "corpus-b")
	destA, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "corpus-a",
		Kind:       "file",
		ConfigJSON: `{"root":"` + rootA + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination a: %v", err)
	}
	destB, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "corpus-b",
		Kind:       "file",
		ConfigJSON: `{"root":"` + rootB + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination b: %v", err)
	}
	routeA, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "route-a",
		MatchSource:   "agent",
		DestinationID: destA.ID,
		AutoRoute:     true,
	})
	if err != nil {
		t.Fatalf("add route a: %v", err)
	}
	routeB, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "route-b",
		MatchSource:   "agent",
		DestinationID: destB.ID,
		AutoRoute:     true,
	})
	if err != nil {
		t.Fatalf("add route b: %v", err)
	}

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "agent",
		SourceType:    "draft",
		SourceID:      "fanout-collision-drop",
		Title:         "Draft: fan-out collision",
		Content:       "This fragment carries an attachment that both matched routes will publish.",
		CreatedAt:     time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/agent/draft/fanout-collision-drop",
	}, "agent-intake", time.Date(2026, 9, 16, 10, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	outcome, err := fragmentRepo.Upsert(context.Background(), fragment)
	if err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}

	sourceFile := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(sourceFile, []byte("attachment body"), 0o600); err != nil {
		t.Fatalf("write source attachment: %v", err)
	}
	if err := attachmentRepo.ReplaceFragmentAttachments(context.Background(), fragment.ID, []domain.PipelineAttachment{
		{Kind: "file", Role: "content", Name: "note.txt", SourcePath: sourceFile, Source: "agent"},
	}, time.Now().UTC()); err != nil {
		t.Fatalf("replace fragment attachments: %v", err)
	}
	attachmentsBefore, err := attachmentRepo.ListByFragment(context.Background(), fragment.ID)
	if err != nil || len(attachmentsBefore) != 1 {
		t.Fatalf("expected exactly one attachment, got %d: %v", len(attachmentsBefore), err)
	}
	attachmentID := attachmentsBefore[0].ID

	stage := ingest.NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, entityRepo, nil)
	stageCtx := &ingest.StageContext{
		Fragment: fragment,
		Outcome:  outcome,
		Now:      time.Now().UTC(),
	}
	if err := stage.Run(context.Background(), stageCtx); err != nil {
		t.Fatalf("RouteStage.Run: %v", err)
	}

	logs, err := routingRepo.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 route_log entries, got %d: %+v", len(logs), logs)
	}

	var routeBEntry *domain.RouteLogEntry
	for i := range logs {
		if logs[i].RouteID == routeB.ID {
			routeBEntry = &logs[i]
		}
	}
	if routeBEntry == nil {
		t.Fatalf("expected a route_log entry for route-b, got %+v", logs)
	}

	_, rawJSON, found := strings.Cut(routeBEntry.Reason, ":")
	if !found {
		t.Fatalf("expected prefix:json reason, got %q", routeBEntry.Reason)
	}
	var payload struct {
		AttachmentCollisions []struct {
			AttachmentID    string `json:"attachment_id"`
			PreviousRouteID string `json:"previous_route_id"`
		} `json:"attachment_collisions"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("decode route-b reason payload: %v", err)
	}
	if len(payload.AttachmentCollisions) != 1 {
		t.Fatalf("expected exactly one recorded collision on route-b, got %+v", payload.AttachmentCollisions)
	}
	if payload.AttachmentCollisions[0].AttachmentID != attachmentID {
		t.Fatalf("expected collision on attachment %q, got %+v", attachmentID, payload.AttachmentCollisions[0])
	}
	if payload.AttachmentCollisions[0].PreviousRouteID != routeA.ID {
		t.Fatalf("expected route-a recorded as the earlier claimant, got %+v", payload.AttachmentCollisions[0])
	}

	// route-a (processed first) claimed the attachment cleanly -- no
	// collision recorded on its own entry.
	for i := range logs {
		if logs[i].RouteID != routeA.ID {
			continue
		}
		_, rawJSON, found := strings.Cut(logs[i].Reason, ":")
		if !found {
			t.Fatalf("expected prefix:json reason, got %q", logs[i].Reason)
		}
		var routeAPayload struct {
			AttachmentCollisions []any `json:"attachment_collisions"`
		}
		if err := json.Unmarshal([]byte(rawJSON), &routeAPayload); err != nil {
			t.Fatalf("decode route-a reason payload: %v", err)
		}
		if len(routeAPayload.AttachmentCollisions) != 0 {
			t.Fatalf("expected no collision recorded on route-a (it claimed the attachment first), got %+v", routeAPayload.AttachmentCollisions)
		}
	}
}
