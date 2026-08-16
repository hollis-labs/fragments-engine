package service

import (
	"encoding/json"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type effectiveQueuePolicy struct {
	ReplayCooldownSeconds    int
	MaxReplaysPerHour        int
	AlertPendingThreshold    int
	AlertDeadLetterThreshold int
}

func effectiveRetryConfig(destination domain.Destination, defaults config.DeliveryConfig) domain.DeliveryRetryConfig {
	switch destination.Kind {
	case "file":
		cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](destination)
		if err == nil {
			base := deliveryRetryDefaults("file", defaults)
			return normalizeRetry(cfg.Retry, base.MaxAttempts, base.BackoffMS)
		}
	case "mcp":
		cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](destination)
		if err == nil {
			base := deliveryRetryDefaults("mcp", defaults)
			return normalizeRetry(cfg.Retry, base.MaxAttempts, base.BackoffMS)
		}
	case "api":
		cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](destination)
		if err == nil {
			base := deliveryRetryDefaults("api", defaults)
			return normalizeRetry(cfg.Retry, base.MaxAttempts, base.BackoffMS)
		}
	case "cli":
		cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](destination)
		if err == nil {
			base := deliveryRetryDefaults("cli", defaults)
			return normalizeRetry(cfg.Retry, base.MaxAttempts, base.BackoffMS)
		}
	case "callback":
		cfg, err := domain.DecodeDestinationConfig[domain.CallbackDestinationConfig](destination)
		if err == nil {
			base := deliveryRetryDefaults("callback", defaults)
			return normalizeRetry(cfg.Retry, base.MaxAttempts, base.BackoffMS)
		}
	}
	return domain.DeliveryRetryConfig{MaxAttempts: 1, BackoffMS: 0}
}

func effectiveQueueConfig(destination domain.Destination, defaults config.QueueConfig) effectiveQueuePolicy {
	policy := effectiveQueuePolicy{
		ReplayCooldownSeconds:    defaults.ReplayCooldownSeconds,
		MaxReplaysPerHour:        defaults.MaxReplaysPerHour,
		AlertPendingThreshold:    defaults.AlertPendingThreshold,
		AlertDeadLetterThreshold: defaults.AlertDeadLetterThreshold,
	}
	if destination.ConfigJSON == "" {
		return policy
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(destination.ConfigJSON), &raw); err != nil {
		return policy
	}
	queuePolicyRaw, ok := raw["queue_policy"]
	if !ok {
		return policy
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(queuePolicyRaw, &fields); err != nil {
		return policy
	}
	if rawValue, ok := fields["replay_cooldown_seconds"]; ok {
		var v int
		if err := json.Unmarshal(rawValue, &v); err == nil {
			policy.ReplayCooldownSeconds = v
		}
	}
	if rawValue, ok := fields["max_replays_per_hour"]; ok {
		var v int
		if err := json.Unmarshal(rawValue, &v); err == nil {
			policy.MaxReplaysPerHour = v
		}
	}
	if rawValue, ok := fields["alert_pending_threshold"]; ok {
		var v int
		if err := json.Unmarshal(rawValue, &v); err == nil {
			policy.AlertPendingThreshold = v
		}
	}
	if rawValue, ok := fields["alert_dead_letter_threshold"]; ok {
		var v int
		if err := json.Unmarshal(rawValue, &v); err == nil {
			policy.AlertDeadLetterThreshold = v
		}
	}
	return policy
}
