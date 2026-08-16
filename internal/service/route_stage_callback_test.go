package service

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// newHangingCallbackTarget starts a TCP listener that accepts connections
// but never reads from, writes to, or closes them -- simulating a remote
// endpoint that would hang forever if dialed synchronously. It returns an
// http://... URL. Any code that actually attempts a synchronous HTTP POST
// against this target blocks until its own timeout expires (the callback
// executor's internal 30s timeout); a test asserting a prompt return proves
// no synchronous attempt was made at all.
func newHangingCallbackTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for hanging callback target: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Deliberately never read/write/close: this is the point.
			_ = conn
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return "http://" + ln.Addr().String() + "/nanite/wake"
}

// TestRouteStage_CallbackDestination_NeverFiresSynchronously covers
// CW-20260816-0034's core acceptance criterion for the routing-pipeline
// call site: a Route firing a "callback" destination must enqueue a
// delivery_jobs row and return from RouteStage.Run promptly, with NO
// synchronous HTTP call happening in that call stack. Target is pointed at
// a listener that accepts but never responds, so if RouteStage.Run's
// callback branch ever regressed to attempting an inline delivery first,
// this test would hang/time out instead of passing quickly.
func TestRouteStage_CallbackDestination_NeverFiresSynchronously(t *testing.T) {
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

	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, entityRepo, inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}
	queueSvc.SetAttachmentRepository(attachmentRepo)

	target := newHangingCallbackTarget(t)
	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: fmt.Sprintf(`{"target":%q,"generator":"wiki_page"}`, target),
	})
	if err != nil {
		t.Fatalf("add callback destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "callback-route",
		DestinationID: dest.ID,
		AutoRoute:     true,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-callback-hot-path",
		Title:         "Claude session: callback hot path",
		Content:       "This fragment must route through the async queue, never a live call.",
		CreatedAt:     time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-08-16/session-callback-hot-path",
	}, "claude-default", time.Date(2026, 8, 16, 10, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	outcome, err := fragmentRepo.Upsert(context.Background(), fragment)
	if err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	stage := ingest.NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, queueSvc)
	stageCtx := &ingest.StageContext{
		Fragment: fragment,
		Outcome:  outcome,
		Now:      time.Now().UTC(),
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- stage.Run(context.Background(), stageCtx)
	}()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("RouteStage.Run returned error: %v", err)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("RouteStage.Run took %s to return -- looks like it attempted a synchronous callback call", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RouteStage.Run did not return within 2s -- looks like it attempted a synchronous HTTP call against the hanging callback target")
	}

	stats, err := queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.Pending != 1 {
		t.Fatalf("expected exactly 1 pending delivery_jobs row, got %+v", stats)
	}

	var jobCount int
	if err := st.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM delivery_jobs WHERE json_extract(payload, '$.destination_id') = ? AND json_extract(payload, '$.fragment_id') = ?`,
		dest.ID, fragment.ID,
	).Scan(&jobCount); err != nil {
		t.Fatalf("count delivery_jobs rows: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("expected exactly 1 delivery_jobs row for this fragment/destination, got %d", jobCount)
	}

	stored, err := fragmentRepo.GetByID(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("get fragment: %v", err)
	}
	if stored.Status == domain.FragmentStatusRouted {
		t.Fatalf("expected fragment to remain unrouted until the queue drainer actually delivers it, got status %s", stored.Status)
	}

	logs, err := routingRepo.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one route log entry, got %d: %+v", len(logs), logs)
	}
	if logs[0].Decision != "inbox" {
		t.Fatalf("expected inbox decision pending delivery, got %+v", logs[0])
	}
	if logs[0].RouteID != route.ID {
		t.Fatalf("expected route log entry to reference the matched route %q, got %+v", route.ID, logs[0])
	}
	if !strings.Contains(logs[0].Reason, "delivery_queued_by_design") {
		t.Fatalf("expected delivery_queued_by_design reason (queued by design, not after a failed attempt), got %q", logs[0].Reason)
	}
}
