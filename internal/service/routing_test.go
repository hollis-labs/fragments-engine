package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestRoutingService_AddDestination_NaniteMessagingDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "nanite-user",
		Kind:       "api",
		ConfigJSON: `{"base_url":"http://127.0.0.1:8090","provider":"nanite_messaging","nanite_messaging":{"to_session_id":"sess-123"}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}

	cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode normalized config: %v", err)
	}
	if cfg.Path != "/api/messaging/send" {
		t.Fatalf("unexpected path: %s", cfg.Path)
	}
	if cfg.NaniteMessaging.ToAgentID != "user" {
		t.Fatalf("unexpected to_agent_id: %s", cfg.NaniteMessaging.ToAgentID)
	}
	if cfg.NaniteMessaging.FromSessionID != "sess-123" {
		t.Fatalf("unexpected from_session_id: %s", cfg.NaniteMessaging.FromSessionID)
	}
	if cfg.NaniteMessaging.FromAgentID != "fragments-engine" {
		t.Fatalf("unexpected from_agent_id: %s", cfg.NaniteMessaging.FromAgentID)
	}
	if cfg.Retry.MaxAttempts != 3 || cfg.Retry.BackoffMS != 500 {
		t.Fatalf("unexpected retry defaults: %+v", cfg.Retry)
	}
}

func TestRoutingService_AddDestination_UsesGlobalDeliveryDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{
			MCP: config.DeliveryRetryDefaults{MaxAttempts: 5, BackoffMS: 1200},
		},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "nil-custom",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"stdio","command":"/usr/bin/env","provider":"nil_inbox"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}

	cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode normalized config: %v", err)
	}
	if cfg.Retry.MaxAttempts != 5 || cfg.Retry.BackoffMS != 1200 {
		t.Fatalf("unexpected retry defaults from global delivery config: %+v", cfg.Retry)
	}
}

func TestRoutingService_AddDestination_CLIDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{
			CLI: config.DeliveryRetryDefaults{MaxAttempts: 4, BackoffMS: 900},
		},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "cli-export",
		Kind:       "cli",
		ConfigJSON: `{"command":"/usr/bin/env","provider":"corpus_cli"}`,
	})
	if err != nil {
		t.Fatalf("add cli destination: %v", err)
	}
	cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode cli config: %v", err)
	}
	if cfg.TimeoutSeconds != 30 {
		t.Fatalf("unexpected cli timeout default: %d", cfg.TimeoutSeconds)
	}
	if cfg.Retry.MaxAttempts != 4 || cfg.Retry.BackoffMS != 900 {
		t.Fatalf("unexpected cli retry defaults: %+v", cfg.Retry)
	}
}

// TestRoutingService_AddDestination_MCPHTTPTransport is the regression
// test for a gap `route destination-add` had until now: normalizeDestination
// (shared by AddDestination and ValidateDestination) validated "mcp"
// destinations against stdio only, even after MCPDestinationExecutor itself
// gained "http" transport support -- so creating the exact tangent_hitl
// destination the README documents failed with "unsupported transport
// \"http\"" despite the transport working end to end once created.
func TestRoutingService_AddDestination_MCPHTTPTransport(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name: "tangent-hitl",
		Kind: "mcp",
		ConfigJSON: `{
			"transport": "http",
			"base_url": "http://127.0.0.1:7842/mcp",
			"provider": "tangent_hitl",
			"tangent_hitl": {"application_id": "fragments-engine", "agent_id": "fe-router"}
		}`,
	})
	if err != nil {
		t.Fatalf("add mcp http destination: %v", err)
	}
	cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode mcp config: %v", err)
	}
	if cfg.Transport != "http" {
		t.Fatalf("expected transport http preserved, got %q", cfg.Transport)
	}
	if cfg.Tool != "tangent.hitl_enqueue" {
		t.Fatalf("expected tangent_hitl provider to default tool, got %q", cfg.Tool)
	}

	if _, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "tangent-hitl-no-url",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"http","provider":"tangent_hitl"}`,
	}); err == nil || !strings.Contains(err.Error(), "missing base_url") {
		t.Fatalf("expected missing base_url error, got %v", err)
	}

	if _, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "bad-transport",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"sse","command":"/usr/bin/env"}`,
	}); err == nil || !strings.Contains(err.Error(), "unsupported transport") {
		t.Fatalf("expected unsupported transport error, got %v", err)
	}
}

func TestRoutingService_UpdateDestinationPolicies(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "policy-target",
		Kind:       "api",
		ConfigJSON: `{"base_url":"http://127.0.0.1:8090","provider":"nanite_user_mailbox","nanite_messaging":{"to_session_id":"sess-123"}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}

	dest, err = svc.UpdateDestinationRetry(context.Background(), dest.ID, domain.DeliveryRetryConfig{
		MaxAttempts: 9,
		BackoffMS:   1800,
	})
	if err != nil {
		t.Fatalf("update retry: %v", err)
	}
	cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode updated api config: %v", err)
	}
	if cfg.Retry.MaxAttempts != 9 || cfg.Retry.BackoffMS != 1800 {
		t.Fatalf("unexpected updated retry policy: %+v", cfg.Retry)
	}

	dest, err = svc.UpdateDestinationQueuePolicy(context.Background(), dest.ID, domain.QueuePolicyConfig{
		ReplayCooldownSeconds:    33,
		MaxReplaysPerHour:        4,
		AlertPendingThreshold:    2,
		AlertDeadLetterThreshold: 1,
	})
	if err != nil {
		t.Fatalf("update queue policy: %v", err)
	}
	cfg, err = domain.DecodeDestinationConfig[domain.APIDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode updated api config second pass: %v", err)
	}
	if cfg.QueuePolicy == nil {
		t.Fatalf("expected queue policy override to be stored")
	}
	if cfg.QueuePolicy.ReplayCooldownSeconds != 33 ||
		cfg.QueuePolicy.MaxReplaysPerHour != 4 ||
		cfg.QueuePolicy.AlertPendingThreshold != 2 ||
		cfg.QueuePolicy.AlertDeadLetterThreshold != 1 {
		t.Fatalf("unexpected updated queue policy: %+v", cfg.QueuePolicy)
	}
}

func TestRoutingService_RenameValidateAndDeleteDestination(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	validation, err := svc.ValidateDestination(context.Background(), domain.Destination{
		Name:       "dry-run",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"stdio","provider":"nil_inbox"}`,
	})
	if err != nil {
		t.Fatalf("validate destination: %v", err)
	}
	if validation.ConfigValid {
		t.Fatalf("expected invalid validation result: %+v", validation)
	}
	if !strings.Contains(validation.ConfigError, "missing command") {
		t.Fatalf("expected provider-specific validation error, got %+v", validation)
	}

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "delete-me",
		Kind:       "file",
		ConfigJSON: `{"root":"` + filepath.Join(t.TempDir(), "corpus") + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}

	dest, err = svc.RenameDestination(context.Background(), dest.ID, "renamed-destination")
	if err != nil {
		t.Fatalf("rename destination: %v", err)
	}
	if dest.Name != "renamed-destination" {
		t.Fatalf("unexpected renamed destination: %+v", dest)
	}

	route, err := svc.AddRoute(context.Background(), domain.Route{
		Name:          "delete-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	if route.DestinationID != dest.ID {
		t.Fatalf("unexpected route: %+v", route)
	}

	result, err := svc.DeleteDestination(context.Background(), dest.ID, false)
	if err == nil || !strings.Contains(err.Error(), "referenced by 1 routes") {
		t.Fatalf("expected guarded delete error, got result=%+v err=%v", result, err)
	}
	if len(result.RouteIDs) != 1 || result.RouteIDs[0] != route.ID {
		t.Fatalf("expected route ids in guarded delete result: %+v", result)
	}

	result, err = svc.DeleteDestination(context.Background(), dest.ID, true)
	if err != nil {
		t.Fatalf("force delete destination: %v", err)
	}
	if !result.Deleted || len(result.RouteIDs) != 1 {
		t.Fatalf("unexpected delete result: %+v", result)
	}
	if _, err := svc.repo.GetDestination(context.Background(), dest.ID); err == nil {
		t.Fatalf("expected destination to be deleted")
	}
	if _, err := svc.repo.GetRoute(context.Background(), route.ID); err == nil {
		t.Fatalf("expected referencing route to be deleted on force")
	}
}

func TestRoutingService_RouteRenamePreviewAndDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	svc := NewRoutingService(
		routingRepo,
		fragmentRepo,
		entityRepo,
		inboxRepo,
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "preview-dest",
		Kind:       "file",
		ConfigJSON: `{"root":"` + filepath.Join(t.TempDir(), "corpus") + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := svc.AddRoute(context.Background(), domain.Route{
		Name:             "preview-route",
		MatchSource:      "claude",
		MatchType:        "chat",
		MatchEntityKind:  "repo",
		MatchEntityValue: "sample-project",
		DestinationID:    dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	route, err = svc.RenameRoute(context.Background(), route.ID, "preview-route-renamed")
	if err != nil {
		t.Fatalf("rename route: %v", err)
	}
	if route.Name != "preview-route-renamed" {
		t.Fatalf("unexpected renamed route: %+v", route)
	}

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-preview",
		Title:         "Claude session: preview",
		Content:       "Preview this route.",
		CreatedAt:     time.Date(2026, 4, 26, 13, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-26/session-preview",
	}, "claude-test", time.Date(2026, 4, 27, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := entityRepo.ReplaceFragmentEntities(context.Background(), fragment.ID, []domain.FragmentEntity{
		{Kind: "repo", Value: "sample-project", Source: "metadata", Confidence: 0.95},
	}); err != nil {
		t.Fatalf("replace fragment entities: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}

	preview, err := svc.PreviewRoute(context.Background(), route.ID, 10)
	if err != nil {
		t.Fatalf("preview route: %v", err)
	}
	if preview.MatchedCount != 1 || len(preview.PreviewItems) != 1 || preview.PreviewItems[0].FragmentID != fragment.ID {
		t.Fatalf("unexpected route preview result: %+v", preview)
	}

	if err := routingRepo.LogDecision(context.Background(), domain.RouteLogEntry{
		FragmentID:    fragment.ID,
		RouteID:       route.ID,
		DestinationID: dest.ID,
		Decision:      "inbox",
		Reason:        "preview-reference",
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("log route reference: %v", err)
	}

	result, err := svc.DeleteRoute(context.Background(), route.ID, false)
	if err == nil || !strings.Contains(err.Error(), "staged or route-log references") {
		t.Fatalf("expected guarded route delete error, got result=%+v err=%v", result, err)
	}
	if result.RouteLogRefs != 1 {
		t.Fatalf("expected route log refs in delete result: %+v", result)
	}

	result, err = svc.DeleteRoute(context.Background(), route.ID, true)
	if err != nil {
		t.Fatalf("force delete route: %v", err)
	}
	if !result.Deleted || result.RouteLogRefs != 1 {
		t.Fatalf("unexpected force delete result: %+v", result)
	}
	if _, err := routingRepo.GetRoute(context.Background(), route.ID); err == nil {
		t.Fatalf("expected route to be deleted")
	}
}

func TestRoutingService_AddDestination_NaniteMessagingRequiresSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	_, err = svc.AddDestination(context.Background(), domain.Destination{
		Name:       "nanite-user",
		Kind:       "api",
		ConfigJSON: `{"base_url":"http://127.0.0.1:8090","provider":"nanite_messaging","nanite_messaging":{}}`,
	})
	if err == nil || !strings.Contains(err.Error(), "to_session_id") {
		t.Fatalf("expected to_session_id validation error, got %v", err)
	}
}

func TestRoutingService_AddDestination_NaniteUserMailboxDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "nanite-mailbox",
		Kind:       "api",
		ConfigJSON: `{"base_url":"http://127.0.0.1:8090","provider":"nanite_user_mailbox","nanite_messaging":{"to_session_id":"sess-mailbox"}}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}

	cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode normalized config: %v", err)
	}
	if cfg.Provider != "nanite_user_mailbox" {
		t.Fatalf("unexpected provider: %s", cfg.Provider)
	}
	if cfg.NaniteMessaging.ToAgentID != "user" {
		t.Fatalf("unexpected to_agent_id: %s", cfg.NaniteMessaging.ToAgentID)
	}
	if cfg.NaniteMessaging.Channel != "inbox" {
		t.Fatalf("unexpected channel: %s", cfg.NaniteMessaging.Channel)
	}
	if cfg.Retry.MaxAttempts != 3 || cfg.Retry.BackoffMS != 500 {
		t.Fatalf("unexpected retry defaults: %+v", cfg.Retry)
	}
}

func TestRoutingService_DestinationStatus(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	svc := NewRoutingService(routingRepo, fragmentRepo, entityRepo, inboxRepo,
		config.DeliveryConfig{
			File: config.DeliveryRetryDefaults{MaxAttempts: 4, BackoffMS: 250},
		},
		config.QueueConfig{
			ReplayCooldownSeconds:    90,
			MaxReplaysPerHour:        7,
			AlertPendingThreshold:    11,
			AlertDeadLetterThreshold: 5,
		},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"` + filepath.Join(t.TempDir(), "corpus") + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := svc.AddRoute(context.Background(), domain.Route{
		Name:          "local-corpus-route",
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
		Content:       "The roadmap starts with deterministic ingest and search.",
		CreatedAt:     time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-04-25/session-123",
	}, "claude-default", time.Date(2026, 4, 27, 4, 35, 47, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := routingRepo.LogDecision(context.Background(), domain.RouteLogEntry{
		FragmentID:    fragment.ID,
		RouteID:       route.ID,
		DestinationID: dest.ID,
		Decision:      "inbox",
		Reason:        `destination_error:{"attempts":2,"error":"transport closed"}`,
		CreatedAt:     time.Date(2026, 4, 27, 4, 35, 48, 0, time.UTC),
	}); err != nil {
		t.Fatalf("log route failure: %v", err)
	}
	if err := routingRepo.LogDecision(context.Background(), domain.RouteLogEntry{
		FragmentID:    fragment.ID,
		RouteID:       route.ID,
		DestinationID: dest.ID,
		Decision:      "auto_route",
		Reason:        `matched_auto_route:{"attempts":3,"ref":"/tmp/corpus/fragments/chats/claude/2026-04-25/session-123/fragment.md"}`,
		CreatedAt:     time.Date(2026, 4, 27, 4, 35, 49, 0, time.UTC),
	}); err != nil {
		t.Fatalf("log route success: %v", err)
	}

	status, err := svc.GetDestinationStatus(context.Background(), dest.ID)
	if err != nil {
		t.Fatalf("get destination status: %v", err)
	}
	if !status.ConfigValid {
		t.Fatalf("expected destination config to be valid: %+v", status)
	}
	if !status.Reachable {
		t.Fatalf("expected destination to be reachable: %+v", status)
	}
	if status.LastAttempt == nil || !status.LastAttempt.Success {
		t.Fatalf("expected last attempt to be a success: %+v", status.LastAttempt)
	}
	if status.LastAttempt.Attempts != 3 {
		t.Fatalf("expected last attempt attempts=3: %+v", status.LastAttempt)
	}
	if status.LastSuccess == nil || !strings.Contains(status.LastSuccess.Ref, "session-123/fragment.md") {
		t.Fatalf("expected last success ref to include output path: %+v", status.LastSuccess)
	}
	if status.LastFailure == nil || status.LastFailure.Error != "transport closed" {
		t.Fatalf("expected last failure to capture error: %+v", status.LastFailure)
	}
	if status.LastFailure.Attempts != 2 {
		t.Fatalf("expected last failure attempts=2: %+v", status.LastFailure)
	}
	if status.Metrics.TotalAttempts != 5 || status.Metrics.SuccessCount != 1 || status.Metrics.FailureCount != 1 {
		t.Fatalf("unexpected metrics: %+v", status.Metrics)
	}
	if status.EffectiveRetry.MaxAttempts != 4 || status.EffectiveRetry.BackoffMS != 250 {
		t.Fatalf("unexpected effective retry policy: %+v", status.EffectiveRetry)
	}
	if status.EffectiveQueuePolicy.ReplayCooldownSeconds != 90 ||
		status.EffectiveQueuePolicy.MaxReplaysPerHour != 7 ||
		status.EffectiveQueuePolicy.AlertPendingThreshold != 11 ||
		status.EffectiveQueuePolicy.AlertDeadLetterThreshold != 5 {
		t.Fatalf("unexpected effective queue policy: %+v", status.EffectiveQueuePolicy)
	}
}

func TestRoutingService_AddDestination_CallbackDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{
			Callback: config.DeliveryRetryDefaults{MaxAttempts: 6, BackoffMS: 750},
		},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake","generator":"wiki_page"}`,
	})
	if err != nil {
		t.Fatalf("add callback destination: %v", err)
	}
	cfg, err := domain.DecodeDestinationConfig[domain.CallbackDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode callback config: %v", err)
	}
	if cfg.Target != "https://curator.example.com/nanite/wake" {
		t.Fatalf("unexpected target: %s", cfg.Target)
	}
	if cfg.Generator != "wiki_page" {
		t.Fatalf("unexpected generator: %s", cfg.Generator)
	}
	if cfg.Retry.MaxAttempts != 6 || cfg.Retry.BackoffMS != 750 {
		t.Fatalf("unexpected callback retry defaults: %+v", cfg.Retry)
	}
}

func TestRoutingService_AddDestination_CallbackRequiresTargetAndGenerator(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	if _, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "missing-target",
		Kind:       "callback",
		ConfigJSON: `{"generator":"wiki_page"}`,
	}); err == nil || !strings.Contains(err.Error(), "missing target") {
		t.Fatalf("expected missing target error, got %v", err)
	}

	if _, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "missing-generator",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake"}`,
	}); err == nil || !strings.Contains(err.Error(), "missing generator") {
		t.Fatalf("expected missing generator error, got %v", err)
	}
}

func TestRoutingService_Callback_CRUDParity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := NewRoutingService(
		repository.NewRoutingRepository(st.DB),
		repository.NewFragmentRepository(st.DB),
		repository.NewEntityRepository(st.DB),
		repository.NewInboxRepository(st.DB),
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	// Create.
	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake","generator":"wiki_page"}`,
	})
	if err != nil {
		t.Fatalf("add callback destination: %v", err)
	}

	// List.
	items, err := svc.ListDestinations(context.Background())
	if err != nil {
		t.Fatalf("list destinations: %v", err)
	}
	found := false
	for _, item := range items {
		if item.ID == dest.ID {
			found = true
			if item.Kind != "callback" {
				t.Fatalf("unexpected kind in list: %s", item.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("expected callback destination in list: %+v", items)
	}

	// Config-validate (config-only, since the callback probe never dials out).
	validation, err := svc.ValidateDestination(context.Background(), dest)
	if err != nil {
		t.Fatalf("validate callback destination: %v", err)
	}
	if !validation.ConfigValid {
		t.Fatalf("expected callback config to be valid: %+v", validation)
	}
	if validation.Provider != "callback" {
		t.Fatalf("expected callback provider to fall back to kind, got %q", validation.Provider)
	}
	if !validation.Reachable || !strings.Contains(validation.Reachability, "config_valid:") {
		t.Fatalf("expected config-only reachability result, got %+v", validation)
	}

	// Update retry policy.
	dest, err = svc.UpdateDestinationRetry(context.Background(), dest.ID, domain.DeliveryRetryConfig{
		MaxAttempts: 5,
		BackoffMS:   1500,
	})
	if err != nil {
		t.Fatalf("update callback retry: %v", err)
	}
	cfg, err := domain.DecodeDestinationConfig[domain.CallbackDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode updated callback config: %v", err)
	}
	if cfg.Retry.MaxAttempts != 5 || cfg.Retry.BackoffMS != 1500 {
		t.Fatalf("unexpected updated callback retry: %+v", cfg.Retry)
	}

	// Update queue policy.
	dest, err = svc.UpdateDestinationQueuePolicy(context.Background(), dest.ID, domain.QueuePolicyConfig{
		ReplayCooldownSeconds:    20,
		MaxReplaysPerHour:        3,
		AlertPendingThreshold:    5,
		AlertDeadLetterThreshold: 2,
	})
	if err != nil {
		t.Fatalf("update callback queue policy: %v", err)
	}
	cfg, err = domain.DecodeDestinationConfig[domain.CallbackDestinationConfig](dest)
	if err != nil {
		t.Fatalf("decode updated callback config after queue policy: %v", err)
	}
	if cfg.QueuePolicy == nil || cfg.QueuePolicy.ReplayCooldownSeconds != 20 || cfg.QueuePolicy.MaxReplaysPerHour != 3 {
		t.Fatalf("unexpected updated callback queue policy: %+v", cfg.QueuePolicy)
	}

	// Delete.
	result, err := svc.DeleteDestination(context.Background(), dest.ID, false)
	if err != nil {
		t.Fatalf("delete callback destination: %v", err)
	}
	if !result.Deleted {
		t.Fatalf("expected callback destination to be deleted: %+v", result)
	}
	if _, err := svc.repo.GetDestination(context.Background(), dest.ID); err == nil {
		t.Fatalf("expected callback destination to be gone after delete")
	}
}

// TestRoutingService_CallbackRoute_DoesNotFireEndToEnd verifies that
// materializing a fragment through a callback destination never performs
// live network I/O against Curator's endpoint. CW-20260816-0034 wired up the
// callback executor (internal/ingest/delivery.go's executeDestinationOnce
// now has a "callback" case) and dispatch happens exclusively through the
// async delivery queue during routing (RouteStage.Run / ApplyRouteByEntity)
// -- MaterializeFragmentToDestination is a synchronous, result-returning
// admin action with no queued/pending result shape, so it deliberately
// rejects callback destinations up front rather than either attempting a
// synchronous callback (which the architecture forbids) or silently
// redefining "materialize" into a queue operation.
func TestRoutingService_CallbackRoute_DoesNotFireEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	svc := NewRoutingService(routingRepo, fragmentRepo, entityRepo, inboxRepo,
		config.DeliveryConfig{},
		config.QueueConfig{},
	)

	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake","generator":"wiki_page"}`,
	})
	if err != nil {
		t.Fatalf("add callback destination: %v", err)
	}
	route, err := svc.AddRoute(context.Background(), domain.Route{
		Name:          "callback-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}
	if route.DestinationID != dest.ID {
		t.Fatalf("unexpected route destination: %+v", route)
	}

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-callback-1",
		Title:         "Claude session: callback-test",
		Content:       "This fragment should never actually reach Curator from this task's plumbing alone.",
		CreatedAt:     time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-08-16/session-callback-1",
	}, "claude-default", time.Date(2026, 8, 16, 10, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	result, err := svc.MaterializeFragmentToDestination(context.Background(), fragment.ID, dest.Name)
	if err == nil {
		t.Fatalf("expected materialize through callback destination to be rejected (materialize is unsupported for callback), got result=%+v", result)
	}
	if !strings.Contains(err.Error(), "never fire synchronously") {
		t.Fatalf("expected callback-materialize-unsupported error, got: %v", err)
	}
	if result.WrittenPath != "" {
		t.Fatalf("expected no written path for callback destination, got %+v", result)
	}

	logs, err := svc.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one route log entry recording the rejected attempt, got %d: %+v", len(logs), logs)
	}
	if !strings.Contains(logs[0].Reason, "materialize_unsupported_callback") {
		t.Fatalf("expected materialize_unsupported_callback log entry, got %+v", logs[0])
	}
}

// TestRoutingService_ApplyRouteByEntity_CallbackDestination_NeverFiresSynchronously
// covers CW-20260816-0034's acceptance criterion for the second mandatory
// call site: the manual apply-route-by-entity path must also enqueue a
// delivery_jobs row and return promptly for callback destinations, with no
// synchronous HTTP call in that call stack. Target is pointed at a listener
// that accepts but never responds, so a regression back to
// try-then-fall-back-to-queue would make this test hang/time out.
func TestRoutingService_ApplyRouteByEntity_CallbackDestination_NeverFiresSynchronously(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	attachmentRepo := repository.NewAttachmentRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)

	queueSvc, err := NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, entityRepo, inboxRepo, testDeliveryConfig(), testQueueConfig())
	if err != nil {
		t.Fatalf("new delivery queue: %v", err)
	}
	queueSvc.SetAttachmentRepository(attachmentRepo)

	svc := NewRoutingService(routingRepo, fragmentRepo, entityRepo, inboxRepo, config.DeliveryConfig{}, config.QueueConfig{})
	svc.SetDeliveryQueue(queueSvc)
	svc.SetAttachmentRepository(attachmentRepo)

	target := newHangingCallbackTarget(t)
	dest, err := svc.AddDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: fmt.Sprintf(`{"target":%q,"generator":"wiki_page"}`, target),
	})
	if err != nil {
		t.Fatalf("add callback destination: %v", err)
	}
	route, err := svc.AddRoute(context.Background(), domain.Route{
		Name:          "callback-apply-route",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-callback-apply",
		Title:         "Claude session: callback apply-route",
		Content:       "This fragment must route through the async queue via manual apply, never a live call.",
		CreatedAt:     time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC),
		CanonicalPath: "fragments/chats/claude/2026-08-16/session-callback-apply",
	}, "claude-default", time.Date(2026, 8, 16, 11, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(context.Background(), fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}
	if err := entityRepo.ReplaceFragmentEntities(context.Background(), fragment.ID, []domain.FragmentEntity{
		{Kind: "topic", Value: "roadmap", Source: "test", Confidence: 1.0},
	}); err != nil {
		t.Fatalf("replace fragment entities: %v", err)
	}
	if err := inboxRepo.Stage(context.Background(), fragment.ID, "awaiting routing", time.Now().UTC()); err != nil {
		t.Fatalf("stage inbox: %v", err)
	}

	done := make(chan struct {
		result domain.RouteApplyResult
		err    error
	}, 1)
	start := time.Now()
	go func() {
		result, err := svc.ApplyRouteByEntity(context.Background(), route.ID, "topic", "roadmap", 10)
		done <- struct {
			result domain.RouteApplyResult
			err    error
		}{result, err}
	}()

	var result domain.RouteApplyResult
	select {
	case out := <-done:
		elapsed := time.Since(start)
		if out.err != nil {
			t.Fatalf("ApplyRouteByEntity returned error: %v", out.err)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("ApplyRouteByEntity took %s to return -- looks like it attempted a synchronous callback call", elapsed)
		}
		result = out.result
	case <-time.After(2 * time.Second):
		t.Fatal("ApplyRouteByEntity did not return within 2s -- looks like it attempted a synchronous HTTP call against the hanging callback target")
	}

	if result.MatchedCount != 1 {
		t.Fatalf("expected 1 matched item, got %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].Status != "queued" {
		t.Fatalf("expected item queued (not routed/failed), got %+v", result.Items)
	}
	if result.RoutedCount != 0 {
		t.Fatalf("expected 0 routed count (delivery is only queued, not completed), got %+v", result)
	}
	if result.FailedCount != 0 {
		t.Fatalf("expected 0 failed count, got %+v", result)
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

	// The fragment must still be sitting in the inbox awaiting the queue
	// drainer -- ApplyRouteByEntity must not have removed it, since delivery
	// hasn't actually happened yet.
	inboxItems, err := inboxRepo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	found := false
	for _, item := range inboxItems {
		if item.FragmentID == fragment.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected fragment to remain staged in inbox pending async delivery, got %+v", inboxItems)
	}

	logs, err := svc.ListRouteLog(context.Background(), fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one route log entry, got %d: %+v", len(logs), logs)
	}
	if logs[0].Decision != "inbox" {
		t.Fatalf("expected inbox decision pending delivery, got %+v", logs[0])
	}
	if !strings.Contains(logs[0].Reason, "delivery_queued_by_design") {
		t.Fatalf("expected delivery_queued_by_design reason (queued by design, not after a failed attempt), got %q", logs[0].Reason)
	}
}
