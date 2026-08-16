package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func testQueueConfig() config.QueueConfig {
	return config.QueueConfig{
		MaxReplaysPerHour:        10,
		AlertPendingThreshold:    10,
		AlertDeadLetterThreshold: 3,
	}
}

func testDeliveryConfig() config.DeliveryConfig {
	return config.DeliveryConfig{
		File: config.DeliveryRetryDefaults{MaxAttempts: 1, BackoffMS: 0},
		MCP:  config.DeliveryRetryDefaults{MaxAttempts: 3, BackoffMS: 500},
		API:  config.DeliveryRetryDefaults{MaxAttempts: 3, BackoffMS: 500},
	}
}

func TestDeliveryQueueService_DrainSuccess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	destRoot := filepath.Join(t.TempDir(), "corpus")
	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"` + destRoot + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "retry-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "chatgpt",
		SourceType:    "chat",
		SourceID:      "conv-123",
		Title:         "ChatGPT chat: Roadmap planning",
		Content:       "## user\n\nSummarize the roadmap.\n\n## assistant\n\nThe roadmap starts with deterministic ingest and search.",
		CreatedAt:     time.Date(2026, 4, 26, 5, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/chatgpt/2026-04-26/conv-123",
	}, "chatgpt-test", time.Date(2026, 4, 27, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}
	if err := queueSvc.EnqueueDestinationRetry(context.Background(), fragment.ID, route.ID, dest.ID, domain.DeliveryRetryConfig{MaxAttempts: 1}); err != nil {
		t.Fatalf("enqueue delivery retry: %v", err)
	}

	stats, err := queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.Pending != 1 || stats.Failed != 0 {
		t.Fatalf("unexpected initial queue stats: %+v", stats)
	}

	processed, err := queueSvc.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain queue: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 processed job, got %d", processed)
	}
	stats, err = queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("queue stats after drain: %v", err)
	}
	if stats.Pending != 0 || stats.Failed != 0 {
		t.Fatalf("unexpected queue stats after drain: %+v", stats)
	}

	stored, err := fragmentRepo.GetByID(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("get fragment: %v", err)
	}
	if stored.Status != domain.FragmentStatusRouted {
		t.Fatalf("expected routed status, got %s", stored.Status)
	}
	inboxItems, err := inboxRepo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inboxItems) != 0 {
		t.Fatalf("expected empty inbox, got %d items", len(inboxItems))
	}
	logs, err := routingRepo.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) == 0 || logs[len(logs)-1].Decision != "queued_route" {
		t.Fatalf("expected queued_route log entry, got %+v", logs)
	}
}

// TestDeliveryQueueService_Drain_CallbackSuccess covers CW-20260816-0034's
// acceptance criterion for the background drainer: it must pop and execute
// a queued "callback" job exactly like the other four destination kinds --
// forwarding CallbackDestinationConfig.Generator unmodified in the request
// body, and updating fragment status/route_log on success the same way.
func TestDeliveryQueueService_Drain_CallbackSuccess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	var (
		gotMethod    string
		gotGenerator any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		var body map[string]any
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode callback body: %v", err)
		}
		gotGenerator = body["generator"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"target":"` + srv.URL + `","generator":"wiki_page::do-not-interpret-me"}`,
	})
	if err != nil {
		t.Fatalf("add callback destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "callback-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-callback-drain",
		Title:         "Claude session: callback drain",
		Content:       "Drained via the background queue.",
		CreatedAt:     time.Date(2026, 4, 26, 13, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-callback-drain",
	}, "claude-test", time.Date(2026, 4, 27, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}
	if err := queueSvc.EnqueueDestinationRetry(context.Background(), fragment.ID, route.ID, dest.ID, domain.DeliveryRetryConfig{MaxAttempts: 3, BackoffMS: 1}); err != nil {
		t.Fatalf("enqueue delivery retry: %v", err)
	}

	stats, err := queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.Pending != 1 || stats.Failed != 0 {
		t.Fatalf("unexpected initial queue stats: %+v", stats)
	}

	processed, err := queueSvc.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain queue: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 processed job, got %d", processed)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("expected callback POST, got %q", gotMethod)
	}
	if gotGenerator != "wiki_page::do-not-interpret-me" {
		t.Fatalf("expected generator forwarded unmodified by the drainer, got %#v", gotGenerator)
	}

	stats, err = queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("queue stats after drain: %v", err)
	}
	if stats.Pending != 0 || stats.Failed != 0 {
		t.Fatalf("unexpected queue stats after drain: %+v", stats)
	}

	stored, err := fragmentRepo.GetByID(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("get fragment: %v", err)
	}
	if stored.Status != domain.FragmentStatusRouted {
		t.Fatalf("expected routed status after callback drain, got %s", stored.Status)
	}
	inboxItems, err := inboxRepo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inboxItems) != 0 {
		t.Fatalf("expected empty inbox after callback drain, got %d items", len(inboxItems))
	}
	logs, err := routingRepo.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) == 0 || logs[len(logs)-1].Decision != "queued_route" {
		t.Fatalf("expected queued_route log entry after callback drain, got %+v", logs)
	}
}

// TestDeliveryQueueService_Drain_CallbackDeadLetter covers the failure side
// of the same acceptance criterion: callback jobs that exhaust their
// attempts land in delivery_failed_jobs exactly like the other four kinds --
// this is the same shared processJob/retry/dead-letter mechanism, so this
// test only needs to confirm "callback" actually participates in it, not
// re-verify the retry/backoff/dead-letter logic itself.
func TestDeliveryQueueService_Drain_CallbackDeadLetter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	// A closed listener refuses the connection immediately, which the
	// callback executor treats as a non-retryable transport error at the
	// HTTP-client layer -- combined with max_attempts:1, this reliably lands
	// the job in delivery_failed_jobs on the very first drain.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	refusingTarget := "http://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "bad-callback",
		Kind:       "callback",
		ConfigJSON: `{"target":"` + refusingTarget + `","generator":"wiki_page","retry":{"max_attempts":1,"backoff_ms":0}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "bad-callback-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-callback-dead-letter",
		Title:         "Claude session: callback dead letter",
		Content:       "This callback destination refuses connections.",
		CreatedAt:     time.Date(2026, 4, 26, 14, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-callback-dead-letter",
	}, "claude-test", time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}
	if err := queueSvc.EnqueueDestinationRetry(context.Background(), fragment.ID, route.ID, dest.ID, domain.DeliveryRetryConfig{MaxAttempts: 1}); err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}

	if _, err := queueSvc.Drain(context.Background(), 10); err != nil {
		t.Fatalf("drain queue: %v", err)
	}
	stats, err := queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.Pending != 0 || stats.Failed != 1 {
		t.Fatalf("unexpected callback dead-letter stats: %+v", stats)
	}
	logs, err := routingRepo.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) == 0 || !strings.Contains(logs[len(logs)-1].Reason, "queued_delivery_dead_letter") {
		t.Fatalf("expected callback dead-letter route log, got %+v", logs)
	}
}

func TestDeliveryQueueService_DeadLetter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "bad-nil",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"stdio","command":"/no/such/binary","provider":"nil_inbox","retry":{"max_attempts":1,"backoff_ms":0}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "bad-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-123",
		Title:         "Claude session: roadmap-review",
		Content:       "## user\n\nSummarize the roadmap.",
		CreatedAt:     time.Date(2026, 4, 26, 5, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-123",
	}, "claude-test", time.Date(2026, 4, 27, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}
	if err := queueSvc.EnqueueDestinationRetry(context.Background(), fragment.ID, route.ID, dest.ID, domain.DeliveryRetryConfig{MaxAttempts: 1}); err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}

	if _, err := queueSvc.Drain(context.Background(), 10); err != nil {
		t.Fatalf("drain queue: %v", err)
	}
	stats, err := queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.Pending != 0 || stats.Failed != 1 {
		t.Fatalf("unexpected dead-letter stats: %+v", stats)
	}
	logs, err := routingRepo.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) == 0 || !strings.Contains(logs[len(logs)-1].Reason, "queued_delivery_dead_letter") {
		t.Fatalf("expected dead-letter route log, got %+v", logs)
	}
}

func TestDeliveryQueueService_ReplayAndPurgeFailed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	destRoot := filepath.Join(t.TempDir(), "corpus")
	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"` + destRoot + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{
		Name:          "retry-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "chatgpt",
		SourceType:    "chat",
		SourceID:      "conv-999",
		Title:         "ChatGPT chat: Replay test",
		Content:       "## user\n\nRetry this.",
		CreatedAt:     time.Date(2026, 4, 26, 6, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/chatgpt/2026-04-26/conv-999",
	}, "chatgpt-test", time.Date(2026, 4, 27, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}

	payload := deliveryJobPayload{
		FragmentID:    fragment.ID,
		RouteID:       route.ID,
		DestinationID: dest.ID,
		BackoffMS:     1,
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO delivery_failed_jobs (queue, type, payload, error, attempts, failed_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		deliveryQueueName, deliveryQueueJobKey, payloadRaw, "temporary failure", 2, time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("seed failed job: %v", err)
	}

	items, err := queueSvc.ListFailed(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("list failed jobs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 failed job, got %d", len(items))
	}
	if items[0].FragmentID != fragment.ID {
		t.Fatalf("unexpected failed fragment id: %+v", items[0])
	}

	if err := queueSvc.ReplayFailed(context.Background(), items[0].ID, false); err != nil {
		t.Fatalf("replay failed job: %v", err)
	}
	stats, err := queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats after replay: %v", err)
	}
	if stats.Pending != 1 || stats.Failed != 0 {
		t.Fatalf("unexpected stats after replay: %+v", stats)
	}

	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO delivery_failed_jobs (queue, type, payload, error, attempts, failed_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		deliveryQueueName, deliveryQueueJobKey, payloadRaw, "permanent failure", 3, time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("seed second failed job: %v", err)
	}
	items, err = queueSvc.ListFailed(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("list failed jobs second pass: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 failed job after replay, got %d", len(items))
	}
	if err := queueSvc.PurgeFailed(context.Background(), items[0].ID); err != nil {
		t.Fatalf("purge failed job: %v", err)
	}
	stats, err = queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats after purge: %v", err)
	}
	if stats.Failed != 0 {
		t.Fatalf("expected no failed jobs after purge: %+v", stats)
	}

	events, err := queueSvc.ListEvents(context.Background(), dest.ID, 10)
	if err != nil {
		t.Fatalf("list queue events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected replay and purge events, got %+v", events)
	}
	if events[0].EventType != "purge" || events[1].EventType != "replay" {
		t.Fatalf("unexpected queue event order: %+v", events)
	}
}

func TestDeliveryQueueService_ListJobsByDestination(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	destA, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "local-a",
		Kind:       "file",
		ConfigJSON: `{"root":"` + filepath.Join(t.TempDir(), "corpus-a") + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination a: %v", err)
	}
	destB, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "local-b",
		Kind:       "file",
		ConfigJSON: `{"root":"` + filepath.Join(t.TempDir(), "corpus-b") + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination b: %v", err)
	}
	routeA, err := routingRepo.AddRoute(context.Background(), domain.Route{Name: "route-a", DestinationID: destA.ID})
	if err != nil {
		t.Fatalf("add route a: %v", err)
	}
	routeB, err := routingRepo.AddRoute(context.Background(), domain.Route{Name: "route-b", DestinationID: destB.ID})
	if err != nil {
		t.Fatalf("add route b: %v", err)
	}
	fragmentA, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-a",
		Title:         "Claude session: A",
		Content:       "A",
		CreatedAt:     time.Date(2026, 4, 26, 7, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-a",
	}, "claude-test", time.Date(2026, 4, 27, 7, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment a: %v", err)
	}
	fragmentB, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "chatgpt",
		SourceType:    "chat",
		SourceID:      "conv-b",
		Title:         "ChatGPT chat: B",
		Content:       "B",
		CreatedAt:     time.Date(2026, 4, 26, 8, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/chatgpt/2026-04-26/conv-b",
	}, "chatgpt-test", time.Date(2026, 4, 27, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment b: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragmentA); err != nil {
		t.Fatalf("upsert fragment a: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragmentB); err != nil {
		t.Fatalf("upsert fragment b: %v", err)
	}
	if err := queueSvc.EnqueueDestinationRetry(context.Background(), fragmentA.ID, routeA.ID, destA.ID, domain.DeliveryRetryConfig{MaxAttempts: 1}); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}

	payloadRaw, err := json.Marshal(deliveryJobPayload{
		FragmentID:    fragmentB.ID,
		RouteID:       routeB.ID,
		DestinationID: destB.ID,
		BackoffMS:     1,
	})
	if err != nil {
		t.Fatalf("marshal payload b: %v", err)
	}
	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO delivery_failed_jobs (queue, type, payload, error, attempts, failed_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		deliveryQueueName, deliveryQueueJobKey, payloadRaw, "broken", 1, time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("seed failed job b: %v", err)
	}

	pending, err := queueSvc.ListPending(context.Background(), destA.ID, 10)
	if err != nil {
		t.Fatalf("list pending filtered: %v", err)
	}
	if len(pending) != 1 || pending[0].DestinationID != destA.ID {
		t.Fatalf("unexpected filtered pending jobs: %+v", pending)
	}

	failed, err := queueSvc.ListFailed(context.Background(), destB.ID, 10)
	if err != nil {
		t.Fatalf("list failed filtered: %v", err)
	}
	if len(failed) != 1 || failed[0].DestinationID != destB.ID {
		t.Fatalf("unexpected filtered failed jobs: %+v", failed)
	}
}

func TestDeliveryQueueService_ReplayFailedRequiresHealthyDestinationUnlessForced(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "bad-mcp",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"stdio","command":"/no/such/binary","provider":"nil_inbox","retry":{"max_attempts":2,"backoff_ms":0}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{Name: "bad-route", DestinationID: dest.ID})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-replay",
		Title:         "Claude session: replay guard",
		Content:       "Replay this later.",
		CreatedAt:     time.Date(2026, 4, 26, 9, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-replay",
	}, "claude-test", time.Date(2026, 4, 27, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	payloadRaw, err := json.Marshal(deliveryJobPayload{
		FragmentID:    fragment.ID,
		RouteID:       route.ID,
		DestinationID: dest.ID,
		BackoffMS:     1,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO delivery_failed_jobs (queue, type, payload, error, attempts, failed_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		deliveryQueueName, deliveryQueueJobKey, payloadRaw, "temporary failure", 1, time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("seed failed job: %v", err)
	}

	items, err := queueSvc.ListFailed(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("list failed jobs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 failed job, got %d", len(items))
	}

	if err := queueSvc.ReplayFailed(context.Background(), items[0].ID, false); err == nil || !strings.Contains(err.Error(), "retry with force") {
		t.Fatalf("expected replay guard error, got %v", err)
	}
	stats, err := queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats after guarded replay: %v", err)
	}
	if stats.Pending != 0 || stats.Failed != 1 {
		t.Fatalf("unexpected stats after guarded replay: %+v", stats)
	}

	if err := queueSvc.ReplayFailed(context.Background(), items[0].ID, true); err != nil {
		t.Fatalf("force replay failed job: %v", err)
	}
	stats, err = queueSvc.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats after force replay: %v", err)
	}
	if stats.Pending != 1 || stats.Failed != 0 {
		t.Fatalf("unexpected stats after force replay: %+v", stats)
	}
}

func TestDeliveryQueueService_ReplayPolicyCooldownAndLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), config.QueueConfig{
		MaxReplaysPerHour:        1,
		ReplayCooldownSeconds:    3600,
		AlertPendingThreshold:    10,
		AlertDeadLetterThreshold: 3,
	})
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"` + filepath.Join(t.TempDir(), "corpus") + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{Name: "route", DestinationID: dest.ID})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-policy",
		Title:         "Claude session: policy",
		Content:       "Replay policy.",
		CreatedAt:     time.Date(2026, 4, 26, 11, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-policy",
	}, "claude-test", time.Date(2026, 4, 27, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	payloadRaw, err := json.Marshal(deliveryJobPayload{
		FragmentID:    fragment.ID,
		RouteID:       route.ID,
		DestinationID: dest.ID,
		BackoffMS:     1,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO delivery_failed_jobs (queue, type, payload, error, attempts, failed_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		deliveryQueueName, deliveryQueueJobKey, payloadRaw, "temporary failure", 1, time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("seed failed job: %v", err)
	}

	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO queue_job_events (fragment_id, route_id, destination_id, event_type, detail_json, created_at)
VALUES (?, ?, ?, 'replay', '{}', ?)`,
		fragment.ID, route.ID, dest.ID, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed replay event: %v", err)
	}

	items, err := queueSvc.ListFailed(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one failed job, got %d", len(items))
	}
	if err := queueSvc.ReplayFailed(context.Background(), items[0].ID, false); err == nil || (!strings.Contains(err.Error(), "cooldown") && !strings.Contains(err.Error(), "replay limit")) {
		t.Fatalf("expected replay policy error, got %v", err)
	}
}

func TestDeliveryQueueService_ReplayPolicyDestinationOverride(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), config.QueueConfig{
		MaxReplaysPerHour:        1,
		ReplayCooldownSeconds:    3600,
		AlertPendingThreshold:    10,
		AlertDeadLetterThreshold: 3,
	})
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "override-dest",
		Kind:       "file",
		ConfigJSON: `{"root":"` + filepath.Join(t.TempDir(), "corpus") + `","queue_policy":{"replay_cooldown_seconds":0,"max_replays_per_hour":5,"alert_pending_threshold":1,"alert_dead_letter_threshold":1}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{Name: "override-route", DestinationID: dest.ID})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-override",
		Title:         "Claude session: override",
		Content:       "Destination override.",
		CreatedAt:     time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-override",
	}, "claude-test", time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	payloadRaw, err := json.Marshal(deliveryJobPayload{
		FragmentID:    fragment.ID,
		RouteID:       route.ID,
		DestinationID: dest.ID,
		BackoffMS:     1,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO delivery_failed_jobs (queue, type, payload, error, attempts, failed_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		deliveryQueueName, deliveryQueueJobKey, payloadRaw, "temporary failure", 1, time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("seed failed job: %v", err)
	}
	if _, err := st.DB.ExecContext(context.Background(), `
INSERT INTO queue_job_events (fragment_id, route_id, destination_id, event_type, detail_json, created_at)
VALUES (?, ?, ?, 'replay', '{}', ?)`,
		fragment.ID, route.ID, dest.ID, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed replay event: %v", err)
	}

	items, err := queueSvc.ListFailed(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one failed job, got %d", len(items))
	}
	if err := queueSvc.ReplayFailed(context.Background(), items[0].ID, false); err != nil {
		t.Fatalf("expected destination override to allow replay, got %v", err)
	}

	summaries, err := queueSvc.ListDestinationSummaries(context.Background(), 10)
	if err != nil {
		t.Fatalf("list destination summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	if !summaries[0].Alert || summaries[0].AlertReason != "pending_threshold" {
		t.Fatalf("expected override alert threshold to apply, got %+v", summaries[0])
	}
}

func TestDeliveryQueueService_DestinationSummaries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, repository.NewEntityRepository(st.DB), inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}

	dest, err := routingRepo.AddDestination(context.Background(), domain.Destination{
		Name:       "bad-nil",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"stdio","command":"/no/such/binary","provider":"nil_inbox","retry":{"max_attempts":1,"backoff_ms":0}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := routingRepo.AddRoute(context.Background(), domain.Route{Name: "bad-route", DestinationID: dest.ID})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-summary",
		Title:         "Claude session: summary",
		Content:       "Summarize queue destination alerts.",
		CreatedAt:     time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-summary",
	}, "claude-test", time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := queueSvc.EnqueueDestinationRetry(context.Background(), fragment.ID, route.ID, dest.ID, domain.DeliveryRetryConfig{MaxAttempts: 1}); err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}
	if _, err := queueSvc.Drain(context.Background(), 10); err != nil {
		t.Fatalf("drain queue: %v", err)
	}

	items, err := queueSvc.ListDestinationSummaries(context.Background(), 10)
	if err != nil {
		t.Fatalf("list destination summaries: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 destination summary, got %d", len(items))
	}
	item := items[0]
	if item.DestinationID != dest.ID {
		t.Fatalf("unexpected destination summary: %+v", item)
	}
	if !item.Alert || item.AlertReason == "" {
		t.Fatalf("expected alert summary: %+v", item)
	}
	if item.FailedCount != 1 || item.DeadLetterCount != 1 {
		t.Fatalf("unexpected queue summary counts: %+v", item)
	}
	if item.LastFailureAt == nil || item.LastFailureError == "" {
		t.Fatalf("expected last failure details in summary: %+v", item)
	}
	if item.AlertReason != "unreachable" && item.AlertReason != "failed_jobs" && item.AlertReason != "dead_letter_threshold" {
		t.Fatalf("unexpected alert reason: %+v", item)
	}
}
