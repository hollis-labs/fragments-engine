package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type RoutingService struct {
	repo        *repository.RoutingRepository
	fragments   *repository.FragmentRepository
	entities    *repository.EntityRepository
	attachments *repository.AttachmentRepository
	inbox       *repository.InboxRepository
	delivery    *DeliveryQueueService
	cfg         config.DeliveryConfig
	queueCfg    config.QueueConfig
}

func NewRoutingService(repo *repository.RoutingRepository, fragments *repository.FragmentRepository, entities *repository.EntityRepository, inbox *repository.InboxRepository, cfg config.DeliveryConfig, queueCfg config.QueueConfig) *RoutingService {
	return &RoutingService{repo: repo, fragments: fragments, entities: entities, inbox: inbox, cfg: cfg, queueCfg: queueCfg}
}

func (s *RoutingService) SetDeliveryQueue(queue *DeliveryQueueService) {
	s.delivery = queue
}

func (s *RoutingService) SetAttachmentRepository(repo *repository.AttachmentRepository) {
	s.attachments = repo
}

func (s *RoutingService) AddDestination(ctx context.Context, in domain.Destination) (domain.Destination, error) {
	normalized, err := normalizeDestination(in, s.cfg)
	if err != nil {
		return domain.Destination{}, err
	}
	in = normalized
	return s.repo.AddDestination(ctx, in)
}

func (s *RoutingService) RenameDestination(ctx context.Context, destinationID, name string) (domain.Destination, error) {
	item, err := s.repo.GetDestination(ctx, destinationID)
	if err != nil {
		return domain.Destination{}, err
	}
	item.Name = strings.TrimSpace(name)
	if item.Name == "" {
		return domain.Destination{}, fmt.Errorf("destination name is required")
	}
	return s.AddDestination(ctx, item)
}

func (s *RoutingService) ValidateDestination(ctx context.Context, in domain.Destination) (domain.DestinationValidation, error) {
	normalized, err := normalizeDestination(in, s.cfg)
	if err != nil {
		return domain.DestinationValidation{
			Destination: in,
			Provider:    destinationProvider(in),
			ConfigError: err.Error(),
		}, nil
	}
	validation := domain.DestinationValidation{
		Destination: normalized,
		Provider:    destinationProvider(normalized),
		ConfigValid: true,
	}
	probe, err := ingest.ProbeDestination(ctx, normalized)
	if err != nil {
		validation.Reachability = err.Error()
		return validation, nil
	}
	validation.Reachable = probe.Reachable
	validation.Reachability = probe.Message
	return validation, nil
}

func (s *RoutingService) DeleteDestination(ctx context.Context, destinationID string, force bool) (domain.DestinationDeleteResult, error) {
	result := domain.DestinationDeleteResult{
		DestinationID: destinationID,
		Force:         force,
	}
	routes, err := s.repo.ListRoutesForDestination(ctx, destinationID)
	if err != nil {
		return result, err
	}
	for _, route := range routes {
		result.RouteIDs = append(result.RouteIDs, route.ID)
	}
	if len(result.RouteIDs) > 0 && !force {
		return result, fmt.Errorf("destination %s is referenced by %d routes; retry with force to delete", destinationID, len(result.RouteIDs))
	}
	if len(result.RouteIDs) > 0 && force {
		if err := s.repo.DeleteRoutesForDestination(ctx, destinationID); err != nil {
			return result, err
		}
	}
	if err := s.repo.DeleteDestination(ctx, destinationID); err != nil {
		return result, err
	}
	result.Deleted = true
	return result, nil
}

func (s *RoutingService) ListDestinations(ctx context.Context) ([]domain.Destination, error) {
	return s.repo.ListDestinations(ctx)
}

func (s *RoutingService) UpdateDestinationRetry(ctx context.Context, destinationID string, retry domain.DeliveryRetryConfig) (domain.Destination, error) {
	item, err := s.repo.GetDestination(ctx, destinationID)
	if err != nil {
		return domain.Destination{}, err
	}
	switch item.Kind {
	case "file":
		cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.Retry = retry
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	case "mcp":
		cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.Retry = retry
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	case "api":
		cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.Retry = retry
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	case "cli":
		cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.Retry = retry
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	default:
		return domain.Destination{}, fmt.Errorf("unsupported destination kind %q for retry update", item.Kind)
	}
}

func (s *RoutingService) UpdateDestinationQueuePolicy(ctx context.Context, destinationID string, policy domain.QueuePolicyConfig) (domain.Destination, error) {
	item, err := s.repo.GetDestination(ctx, destinationID)
	if err != nil {
		return domain.Destination{}, err
	}
	switch item.Kind {
	case "file":
		cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.QueuePolicy = &policy
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	case "mcp":
		cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.QueuePolicy = &policy
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	case "api":
		cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.QueuePolicy = &policy
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	case "cli":
		cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](item)
		if err != nil {
			return domain.Destination{}, err
		}
		cfg.QueuePolicy = &policy
		updated, err := encodeDestinationConfig(item, cfg)
		if err != nil {
			return domain.Destination{}, err
		}
		return s.AddDestination(ctx, updated)
	default:
		return domain.Destination{}, fmt.Errorf("unsupported destination kind %q for queue policy update", item.Kind)
	}
}

func (s *RoutingService) ListDestinationStatus(ctx context.Context) ([]domain.DestinationStatus, error) {
	items, err := s.repo.ListDestinations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.DestinationStatus, 0, len(items))
	for _, item := range items {
		status, err := s.destinationStatus(ctx, item)
		if err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, nil
}

func (s *RoutingService) GetDestinationStatus(ctx context.Context, destinationID string) (domain.DestinationStatus, error) {
	item, err := s.repo.GetDestination(ctx, destinationID)
	if err != nil {
		return domain.DestinationStatus{}, err
	}
	return s.destinationStatus(ctx, item)
}

func (s *RoutingService) AddRoute(ctx context.Context, in domain.Route) (domain.Route, error) {
	return s.repo.AddRoute(ctx, in)
}

func (s *RoutingService) RenameRoute(ctx context.Context, routeID, name string) (domain.Route, error) {
	item, err := s.repo.GetRoute(ctx, routeID)
	if err != nil {
		return domain.Route{}, err
	}
	item.Name = strings.TrimSpace(name)
	if item.Name == "" {
		return domain.Route{}, fmt.Errorf("route name is required")
	}
	return s.repo.AddRoute(ctx, item)
}

func (s *RoutingService) ListRoutes(ctx context.Context) ([]domain.Route, error) {
	return s.repo.ListRoutes(ctx)
}

func (s *RoutingService) PreviewRoute(ctx context.Context, routeID string, limit int) (domain.RoutePreviewResult, error) {
	result := domain.RoutePreviewResult{RouteID: routeID}
	route, err := s.repo.GetRoute(ctx, routeID)
	if err != nil {
		return result, err
	}
	items, err := s.inbox.List(ctx, limit)
	if err != nil {
		return result, err
	}
	for _, item := range items {
		fragment, err := s.fragments.GetByID(ctx, item.FragmentID)
		if err != nil {
			continue
		}
		entities, err := s.entities.ListByFragment(ctx, item.FragmentID)
		if err != nil {
			continue
		}
		if !routeMatchesPreview(route, fragment, entities) {
			continue
		}
		result.MatchedCount++
		result.PreviewItems = append(result.PreviewItems, domain.RoutePreviewItem{
			FragmentID: fragment.ID,
			Title:      fragment.Title,
			Source:     fragment.Source,
			SourceType: fragment.SourceType,
			Reason:     item.Reason,
		})
	}
	return result, nil
}

func (s *RoutingService) MaterializeRoute(ctx context.Context, routeID string, limit int) (domain.RouteMaterializeResult, error) {
	result := domain.RouteMaterializeResult{RouteID: routeID}
	if strings.TrimSpace(routeID) == "" {
		return result, fmt.Errorf("route id is required")
	}
	route, err := s.repo.GetRoute(ctx, routeID)
	if err != nil {
		return result, err
	}
	destination, err := s.repo.GetDestination(ctx, route.DestinationID)
	if err != nil {
		return result, err
	}
	items, err := s.inbox.List(ctx, limit)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	for _, item := range items {
		fragment, err := s.fragments.GetByID(ctx, item.FragmentID)
		if err != nil {
			continue
		}
		entities, err := s.entities.ListByFragment(ctx, item.FragmentID)
		if err != nil {
			continue
		}
		if !routeMatchesPreview(route, fragment, entities) {
			continue
		}
		result.MatchedCount++
		entry := domain.RouteMaterializeItem{FragmentID: item.FragmentID}
		var attachments []domain.FragmentAttachment
		if s.attachments != nil {
			attachments, err = s.attachments.ListByFragment(ctx, item.FragmentID)
			if err != nil {
				entry.Status = "failed"
				entry.Error = err.Error()
				result.Items = append(result.Items, entry)
				result.FailedCount++
				continue
			}
		}

		delivery, err := ingest.ExecuteDestinationWithRetry(ctx, destination, fragment, attachments)
		if err != nil {
			entry.Status = "failed"
			entry.Error = err.Error()
			result.Items = append(result.Items, entry)
			result.FailedCount++
			_ = s.repo.LogDecision(ctx, domain.RouteLogEntry{
				FragmentID:    item.FragmentID,
				RouteID:       route.ID,
				DestinationID: destination.ID,
				Decision:      "materialize",
				Reason:        encodeRouteDeliveryReason("materialize_error", deliveryReasonPayload{Attempts: delivery.Attempts, Error: err.Error()}),
				CreatedAt:     now,
			})
			continue
		}
		if s.attachments != nil && len(delivery.PublishedAttachments) > 0 {
			if err := s.attachments.UpdateFragmentAttachmentStoragePaths(ctx, item.FragmentID, delivery.PublishedAttachments); err != nil {
				return result, err
			}
		}
		if err := s.inbox.UpdateReason(ctx, item.FragmentID, materializedInboxReason(item.Reason, destination.Name)); err != nil {
			return result, err
		}
		if err := s.repo.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID:    item.FragmentID,
			RouteID:       route.ID,
			DestinationID: destination.ID,
			Decision:      "materialize",
			Reason:        encodeRouteDeliveryReason("materialize_route", deliveryReasonPayload{Attempts: delivery.Attempts, Ref: delivery.Ref}),
			CreatedAt:     now,
		}); err != nil {
			return result, err
		}
		entry.Status = "materialized"
		entry.WrittenPath = delivery.Ref
		result.Items = append(result.Items, entry)
		result.MaterializedCount++
	}
	return result, nil
}

func (s *RoutingService) DeleteRoute(ctx context.Context, routeID string, force bool) (domain.RouteDeleteResult, error) {
	result := domain.RouteDeleteResult{RouteID: routeID, Force: force}
	var err error
	result.StagedRefs, err = s.repo.CountInboxRefs(ctx, routeID)
	if err != nil {
		return result, err
	}
	result.RouteLogRefs, err = s.repo.CountRouteLogRefs(ctx, routeID)
	if err != nil {
		return result, err
	}
	if (result.StagedRefs > 0 || result.RouteLogRefs > 0) && !force {
		return result, fmt.Errorf("route %s still has staged or route-log references; retry with force to delete", routeID)
	}
	if force && (result.StagedRefs > 0 || result.RouteLogRefs > 0) {
		if err := s.repo.ClearRouteRefs(ctx, routeID); err != nil {
			return result, err
		}
	}
	if err := s.repo.DeleteRoute(ctx, routeID); err != nil {
		return result, err
	}
	result.Deleted = true
	return result, nil
}

func (s *RoutingService) ListRouteLog(ctx context.Context, fragmentID string) ([]domain.RouteLogEntry, error) {
	return s.repo.ListRouteLog(ctx, fragmentID)
}

func (s *RoutingService) ApplyRouteByEntity(ctx context.Context, routeID, kind, value string, limit int) (domain.RouteApplyResult, error) {
	result := domain.RouteApplyResult{
		RouteID:     routeID,
		EntityKind:  kind,
		EntityValue: value,
	}
	if strings.TrimSpace(routeID) == "" {
		return result, fmt.Errorf("route id is required")
	}
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(value) == "" {
		return result, fmt.Errorf("entity kind and value are required")
	}

	route, err := s.repo.GetRoute(ctx, routeID)
	if err != nil {
		return result, err
	}
	destination, err := s.repo.GetDestination(ctx, route.DestinationID)
	if err != nil {
		return result, err
	}
	items, err := s.inbox.ListByEntity(ctx, kind, value, limit)
	if err != nil {
		return result, err
	}
	result.MatchedCount = len(items)

	now := time.Now().UTC()
	for _, item := range items {
		entry := domain.RouteApplyItem{FragmentID: item.FragmentID}
		fragment, err := s.fragments.GetByID(ctx, item.FragmentID)
		if err != nil {
			entry.Status = "failed"
			entry.Error = err.Error()
			result.Items = append(result.Items, entry)
			result.FailedCount++
			continue
		}
		var attachments []domain.FragmentAttachment
		if s.attachments != nil {
			attachments, err = s.attachments.ListByFragment(ctx, item.FragmentID)
			if err != nil {
				entry.Status = "failed"
				entry.Error = err.Error()
				result.Items = append(result.Items, entry)
				result.FailedCount++
				continue
			}
		}

		fragment.Status = domain.FragmentStatusRouted
		delivery, err := ingest.ExecuteDestinationWithRetry(ctx, destination, fragment, attachments)
		if err != nil {
			reason := ingest.EncodeManualRouteError(kind, value, delivery.Attempts, err.Error())
			status := "failed"
			if s.delivery != nil {
				retry := destinationRetryConfig(destination, s.cfg)
				if queueErr := s.delivery.EnqueueDestinationRetry(ctx, item.FragmentID, route.ID, destination.ID, retry); queueErr == nil {
					reason = ingest.EncodeDeliveryQueued(delivery.Attempts, err.Error())
					status = "queued"
				}
			}
			_ = s.repo.LogDecision(ctx, domain.RouteLogEntry{
				FragmentID:    item.FragmentID,
				RouteID:       route.ID,
				DestinationID: destination.ID,
				Decision:      "inbox",
				Reason:        reason,
				CreatedAt:     now,
			})
			entry.Status = status
			entry.Error = err.Error()
			result.Items = append(result.Items, entry)
			if status == "failed" {
				result.FailedCount++
			}
			continue
		}

		if err := s.fragments.UpdateStatus(ctx, item.FragmentID, domain.FragmentStatusRouted); err != nil {
			return result, err
		}
		if s.attachments != nil && len(delivery.PublishedAttachments) > 0 {
			if err := s.attachments.UpdateFragmentAttachmentStoragePaths(ctx, item.FragmentID, delivery.PublishedAttachments); err != nil {
				return result, err
			}
		}
		if err := s.inbox.Remove(ctx, item.FragmentID); err != nil {
			return result, err
		}
		if err := s.repo.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID:    item.FragmentID,
			RouteID:       route.ID,
			DestinationID: destination.ID,
			Decision:      "manual_route",
			Reason:        ingest.EncodeManualRouteSuccess(kind, value, delivery.Attempts, delivery.Ref),
			CreatedAt:     now,
		}); err != nil {
			return result, err
		}

		entry.Status = "routed"
		entry.WrittenPath = delivery.Ref
		result.Items = append(result.Items, entry)
		result.RoutedCount++
	}

	return result, nil
}

func routeMatchesPreview(route domain.Route, fragment domain.Fragment, entities []domain.FragmentEntity) bool {
	if route.MatchSource != "" && route.MatchSource != fragment.Source {
		return false
	}
	if route.MatchType != "" && route.MatchType != fragment.SourceType {
		return false
	}
	if route.MatchEntityKind != "" || route.MatchEntityValue != "" {
		for _, entity := range entities {
			if route.MatchEntityKind != "" && route.MatchEntityKind != entity.Kind {
				continue
			}
			if route.MatchEntityValue != "" && route.MatchEntityValue != entity.Value {
				continue
			}
			return true
		}
		return false
	}
	return true
}

func normalizeDestination(in domain.Destination, defaults config.DeliveryConfig) (domain.Destination, error) {
	switch strings.TrimSpace(in.Kind) {
	case "file":
		cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](in)
		if err != nil {
			return domain.Destination{}, err
		}
		if strings.TrimSpace(cfg.Root) == "" {
			return domain.Destination{}, fmt.Errorf("file destination %q missing root", in.Name)
		}
		if strings.TrimSpace(cfg.Provider) == "" {
			cfg.Provider = "file"
		}
		cfg.Retry = destinationRetryConfig(in, defaults)
		return encodeDestinationConfig(in, cfg)
	case "mcp":
		cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](in)
		if err != nil {
			return domain.Destination{}, err
		}
		if transport := strings.TrimSpace(cfg.Transport); transport == "" {
			cfg.Transport = "stdio"
		} else if transport != "stdio" {
			return domain.Destination{}, fmt.Errorf("mcp destination %q unsupported transport %q", in.Name, transport)
		}
		if strings.TrimSpace(cfg.Command) == "" {
			return domain.Destination{}, fmt.Errorf("mcp destination %q missing command", in.Name)
		}
		switch strings.TrimSpace(cfg.Provider) {
		case "", "nil_inbox":
			cfg.Provider = "nil_inbox"
			if strings.TrimSpace(cfg.Tool) == "" {
				cfg.Tool = "nil_create_inbox"
			}
		default:
			if strings.TrimSpace(cfg.Tool) == "" {
				return domain.Destination{}, fmt.Errorf("mcp destination %q missing tool for provider %q", in.Name, cfg.Provider)
			}
		}
		if cfg.TimeoutSeconds <= 0 {
			cfg.TimeoutSeconds = 30
		}
		cfg.Retry = destinationRetryConfig(in, defaults)
		return encodeDestinationConfig(in, cfg)
	case "api":
		cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](in)
		if err != nil {
			return domain.Destination{}, err
		}
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return domain.Destination{}, fmt.Errorf("api destination %q missing base_url", in.Name)
		}
		if strings.TrimSpace(cfg.Method) == "" {
			cfg.Method = "POST"
		}
		switch strings.TrimSpace(cfg.Provider) {
		case "", "nanite_messaging", "nanite_user_mailbox":
			if strings.TrimSpace(cfg.Provider) == "" {
				cfg.Provider = "nanite_messaging"
			}
			if strings.TrimSpace(cfg.Path) == "" {
				cfg.Path = "/api/messaging/send"
			}
			if strings.TrimSpace(cfg.NaniteMessaging.FromAgentID) == "" {
				cfg.NaniteMessaging.FromAgentID = "fragments-engine"
			}
			if strings.TrimSpace(cfg.NaniteMessaging.ToAgentID) == "" {
				cfg.NaniteMessaging.ToAgentID = "user"
			}
			if cfg.Provider == "nanite_user_mailbox" {
				cfg.NaniteMessaging.ToAgentID = "user"
			}
			if strings.TrimSpace(cfg.NaniteMessaging.ToSessionID) == "" {
				return domain.Destination{}, fmt.Errorf("api destination %q missing nanite_messaging.to_session_id", in.Name)
			}
			if strings.TrimSpace(cfg.NaniteMessaging.FromSessionID) == "" {
				cfg.NaniteMessaging.FromSessionID = cfg.NaniteMessaging.ToSessionID
			}
			if strings.TrimSpace(cfg.NaniteMessaging.Channel) == "" {
				cfg.NaniteMessaging.Channel = "inbox"
			}
			if strings.TrimSpace(cfg.NaniteMessaging.Kind) == "" {
				cfg.NaniteMessaging.Kind = "notification"
			}
			if strings.TrimSpace(cfg.NaniteMessaging.Type) == "" {
				cfg.NaniteMessaging.Type = "status_update"
			}
			if strings.TrimSpace(cfg.NaniteMessaging.RegisterAs) == "" {
				cfg.NaniteMessaging.RegisterAs = "external"
			}
			if strings.TrimSpace(cfg.BasicAuthUsername) != "" && strings.TrimSpace(cfg.BasicAuthPasswordEnv) != "" {
				if os.Getenv(strings.TrimSpace(cfg.BasicAuthPasswordEnv)) == "" {
					return domain.Destination{}, fmt.Errorf("api destination %q missing env %q for basic auth password", in.Name, cfg.BasicAuthPasswordEnv)
				}
			}
		default:
			if strings.TrimSpace(cfg.Path) == "" {
				return domain.Destination{}, fmt.Errorf("api destination %q missing path for provider %q", in.Name, cfg.Provider)
			}
			if cfg.Body == nil {
				return domain.Destination{}, fmt.Errorf("api destination %q missing body for provider %q", in.Name, cfg.Provider)
			}
		}
		if cfg.TimeoutSeconds <= 0 {
			cfg.TimeoutSeconds = 30
		}
		cfg.Retry = destinationRetryConfig(in, defaults)
		return encodeDestinationConfig(in, cfg)
	case "cli":
		cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](in)
		if err != nil {
			return domain.Destination{}, err
		}
		if strings.TrimSpace(cfg.Command) == "" {
			return domain.Destination{}, fmt.Errorf("cli destination %q missing command", in.Name)
		}
		if cfg.TimeoutSeconds <= 0 {
			cfg.TimeoutSeconds = 30
		}
		cfg.Retry = destinationRetryConfig(in, defaults)
		return encodeDestinationConfig(in, cfg)
	default:
		return domain.Destination{}, fmt.Errorf("unsupported destination kind %q", in.Kind)
	}
}

func normalizeRetry(cfg domain.DeliveryRetryConfig, attempts, backoff int) domain.DeliveryRetryConfig {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = attempts
	}
	if cfg.BackoffMS < 0 {
		cfg.BackoffMS = 0
	}
	if cfg.BackoffMS == 0 && cfg.MaxAttempts > 1 {
		cfg.BackoffMS = backoff
	}
	return cfg
}

func destinationRetryConfig(destination domain.Destination, defaults config.DeliveryConfig) domain.DeliveryRetryConfig {
	switch destination.Kind {
	case "file":
		cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](destination)
		if err == nil {
			return normalizeRetry(cfg.Retry, deliveryRetryDefaults("file", defaults).MaxAttempts, deliveryRetryDefaults("file", defaults).BackoffMS)
		}
	case "mcp":
		cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](destination)
		if err == nil {
			return normalizeRetry(cfg.Retry, deliveryRetryDefaults("mcp", defaults).MaxAttempts, deliveryRetryDefaults("mcp", defaults).BackoffMS)
		}
	case "api":
		cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](destination)
		if err == nil {
			return normalizeRetry(cfg.Retry, deliveryRetryDefaults("api", defaults).MaxAttempts, deliveryRetryDefaults("api", defaults).BackoffMS)
		}
	case "cli":
		cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](destination)
		if err == nil {
			return normalizeRetry(cfg.Retry, deliveryRetryDefaults("cli", defaults).MaxAttempts, deliveryRetryDefaults("cli", defaults).BackoffMS)
		}
	}
	return domain.DeliveryRetryConfig{MaxAttempts: 1, BackoffMS: 0}
}

func deliveryRetryDefaults(kind string, defaults config.DeliveryConfig) domain.DeliveryRetryConfig {
	switch kind {
	case "file":
		return domain.DeliveryRetryConfig{MaxAttempts: maxInt(defaults.File.MaxAttempts, 1), BackoffMS: maxInt(defaults.File.BackoffMS, 0)}
	case "mcp":
		backoff := defaults.MCP.BackoffMS
		if backoff <= 0 {
			backoff = 500
		}
		attempts := defaults.MCP.MaxAttempts
		if attempts <= 0 {
			attempts = 3
		}
		return domain.DeliveryRetryConfig{MaxAttempts: attempts, BackoffMS: backoff}
	case "api":
		backoff := defaults.API.BackoffMS
		if backoff <= 0 {
			backoff = 500
		}
		attempts := defaults.API.MaxAttempts
		if attempts <= 0 {
			attempts = 3
		}
		return domain.DeliveryRetryConfig{MaxAttempts: attempts, BackoffMS: backoff}
	case "cli":
		backoff := defaults.CLI.BackoffMS
		if backoff <= 0 {
			backoff = 500
		}
		attempts := defaults.CLI.MaxAttempts
		if attempts <= 0 {
			attempts = 3
		}
		return domain.DeliveryRetryConfig{MaxAttempts: attempts, BackoffMS: backoff}
	default:
		return domain.DeliveryRetryConfig{MaxAttempts: 1, BackoffMS: 0}
	}
}

func encodeDestinationConfig[T any](in domain.Destination, cfg T) (domain.Destination, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return domain.Destination{}, fmt.Errorf("encode destination config: %w", err)
	}
	in.ConfigJSON = string(raw)
	return in, nil
}

func (s *RoutingService) destinationStatus(ctx context.Context, item domain.Destination) (domain.DestinationStatus, error) {
	effectiveQueue := effectiveQueueConfig(item, s.queueCfg)
	status := domain.DestinationStatus{
		Destination:    item,
		Provider:       destinationProvider(item),
		EffectiveRetry: effectiveRetryConfig(item, s.cfg),
		EffectiveQueuePolicy: domain.QueuePolicyConfig{
			ReplayCooldownSeconds:    effectiveQueue.ReplayCooldownSeconds,
			MaxReplaysPerHour:        effectiveQueue.MaxReplaysPerHour,
			AlertPendingThreshold:    effectiveQueue.AlertPendingThreshold,
			AlertDeadLetterThreshold: effectiveQueue.AlertDeadLetterThreshold,
		},
	}

	if _, err := normalizeDestination(item, s.cfg); err != nil {
		status.ConfigError = err.Error()
	} else {
		status.ConfigValid = true
	}

	if status.ConfigValid {
		probe, err := ingest.ProbeDestination(ctx, item)
		if err != nil {
			status.Reachability = err.Error()
		} else {
			status.Reachable = probe.Reachable
			status.Reachability = probe.Message
		}
	}

	logs, err := s.repo.ListRouteLogForDestination(ctx, item.ID, 20)
	if err != nil {
		return domain.DestinationStatus{}, err
	}
	metricLogs, err := s.repo.ListRouteLogForDestination(ctx, item.ID, 0)
	if err != nil {
		return domain.DestinationStatus{}, err
	}
	for _, entry := range logs {
		delivery, ok := classifyDelivery(entry)
		if !ok {
			continue
		}
		if status.LastAttempt == nil {
			status.LastAttempt = &delivery
		}
		if delivery.Success && status.LastSuccess == nil {
			status.LastSuccess = &delivery
		}
		if !delivery.Success && status.LastFailure == nil {
			status.LastFailure = &delivery
		}
		if status.LastAttempt != nil && status.LastSuccess != nil && status.LastFailure != nil {
			break
		}
	}
	for _, entry := range metricLogs {
		delivery, ok := classifyDelivery(entry)
		if !ok {
			continue
		}
		status.Metrics.TotalAttempts += maxInt(delivery.Attempts, 1)
		if status.Metrics.LastAttemptAt == nil || delivery.CreatedAt.After(*status.Metrics.LastAttemptAt) {
			ts := delivery.CreatedAt
			status.Metrics.LastAttemptAt = &ts
		}
		if delivery.Success {
			status.Metrics.SuccessCount++
			if status.Metrics.LastSuccessAt == nil || delivery.CreatedAt.After(*status.Metrics.LastSuccessAt) {
				ts := delivery.CreatedAt
				status.Metrics.LastSuccessAt = &ts
			}
		} else {
			status.Metrics.FailureCount++
			if status.Metrics.LastFailureAt == nil || delivery.CreatedAt.After(*status.Metrics.LastFailureAt) {
				ts := delivery.CreatedAt
				status.Metrics.LastFailureAt = &ts
			}
		}
	}

	return status, nil
}

func classifyDelivery(entry domain.RouteLogEntry) (domain.DestinationDeliveryStatus, bool) {
	out := domain.DestinationDeliveryStatus{
		FragmentID: entry.FragmentID,
		RouteID:    entry.RouteID,
		Decision:   entry.Decision,
		Attempts:   1,
		CreatedAt:  entry.CreatedAt,
	}
	prefix, payload, ok := parseDeliveryReason(entry.Reason)
	if !ok {
		switch {
		case entry.Decision == "auto_route" || entry.Decision == "manual_route":
			out.Success = true
			out.Ref = trimReasonPrefix(entry.Reason, "matched_auto_route:", "manual_route_entity:")
			return out, true
		case strings.HasPrefix(entry.Reason, "destination_error:"):
			out.Error = strings.TrimPrefix(entry.Reason, "destination_error:")
			return out, true
		case strings.HasPrefix(entry.Reason, "manual_route_error:"):
			out.Error = strings.TrimPrefix(entry.Reason, "manual_route_error:")
			return out, true
		default:
			return domain.DestinationDeliveryStatus{}, false
		}
	}
	if payload.Attempts > 0 {
		out.Attempts = payload.Attempts
	}
	switch prefix {
	case "matched_auto_route", "manual_route_entity":
		out.Success = true
		out.Ref = payload.Ref
		return out, true
	case "queued_delivery_success":
		out.Success = true
		out.Ref = payload.Ref
		return out, true
	case "destination_error", "manual_route_error", "delivery_queued", "queued_delivery_dead_letter":
		out.Error = payload.Error
		return out, true
	default:
		return domain.DestinationDeliveryStatus{}, false
	}
}

func destinationProvider(item domain.Destination) string {
	switch item.Kind {
	case "file":
		cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](item)
		if err == nil && strings.TrimSpace(cfg.Provider) != "" {
			return cfg.Provider
		}
	case "mcp":
		cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](item)
		if err == nil && strings.TrimSpace(cfg.Provider) != "" {
			return cfg.Provider
		}
	case "api":
		cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](item)
		if err == nil && strings.TrimSpace(cfg.Provider) != "" {
			return cfg.Provider
		}
	case "cli":
		cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](item)
		if err == nil && strings.TrimSpace(cfg.Provider) != "" {
			return cfg.Provider
		}
	}
	return item.Kind
}

func trimReasonPrefix(reason string, prefixes ...string) string {
	for _, prefix := range prefixes {
		if strings.HasPrefix(reason, prefix) {
			value := strings.TrimPrefix(reason, prefix)
			if prefix == "manual_route_entity:" {
				if idx := strings.LastIndex(value, ":"); idx >= 0 && idx+1 < len(value) {
					return value[idx+1:]
				}
			}
			return value
		}
	}
	return reason
}

func materializedInboxReason(reason, destinationName string) string {
	reason = strings.TrimSpace(reason)
	suffix := "materialized"
	if strings.TrimSpace(destinationName) != "" {
		suffix = "materialized to " + strings.TrimSpace(destinationName)
	}
	if strings.Contains(strings.ToLower(reason), "materialized") {
		return reason
	}
	if reason == "" {
		return suffix
	}
	return reason + "; " + suffix
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type deliveryReasonPayload struct {
	Attempts int    `json:"attempts"`
	Ref      string `json:"ref"`
	Error    string `json:"error"`
}

func encodeRouteDeliveryReason(prefix string, payload deliveryReasonPayload) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return prefix
	}
	return prefix + ":" + string(raw)
}

func parseDeliveryReason(reason string) (string, deliveryReasonPayload, bool) {
	idx := strings.Index(reason, ":")
	if idx <= 0 || idx+1 >= len(reason) {
		return "", deliveryReasonPayload{}, false
	}
	prefix := reason[:idx]
	raw := reason[idx+1:]
	if !strings.HasPrefix(raw, "{") {
		return "", deliveryReasonPayload{}, false
	}
	var payload deliveryReasonPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", deliveryReasonPayload{}, false
	}
	return prefix, payload, true
}
