package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type DeliveryResult struct {
	Ref                  string
	Attempts             int
	PublishedAttachments map[string]domain.PublishedAttachmentInfo
}

type deliveryError struct {
	retryable bool
	err       error
}

func (e *deliveryError) Error() string {
	return e.err.Error()
}

func (e *deliveryError) Unwrap() error {
	return e.err
}

func markRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &deliveryError{retryable: true, err: err}
}

func isRetryable(err error) bool {
	var target *deliveryError
	return errors.As(err, &target) && target.retryable
}

func ExecuteDestination(ctx context.Context, destination domain.Destination, fragment domain.Fragment) (string, error) {
	result, err := ExecuteDestinationWithRetry(ctx, destination, fragment, nil)
	if err != nil {
		return "", err
	}
	return result.Ref, nil
}

func ExecuteDestinationOnce(ctx context.Context, destination domain.Destination, fragment domain.Fragment) (string, error) {
	result, err := executeDestinationOnce(ctx, destination, fragment, nil)
	if err != nil {
		return "", err
	}
	return result.Ref, nil
}

func ExecuteDestinationWithRetry(ctx context.Context, destination domain.Destination, fragment domain.Fragment, attachments []domain.FragmentAttachment) (DeliveryResult, error) {
	retry := retryConfigForDestination(destination)
	attempts := 0
	var lastErr error
	for {
		attempts++
		result, err := executeDestinationOnce(ctx, destination, fragment, attachments)
		if err == nil {
			result.Attempts = attempts
			return result, nil
		}
		lastErr = err
		if attempts >= retry.MaxAttempts || !isRetryable(err) {
			break
		}
		if retry.BackoffMS > 0 {
			timer := time.NewTimer(time.Duration(retry.BackoffMS) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return DeliveryResult{Attempts: attempts}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return DeliveryResult{Attempts: attempts}, fmt.Errorf("attempts=%d: %w", attempts, lastErr)
}

func executeDestinationOnce(ctx context.Context, destination domain.Destination, fragment domain.Fragment, attachments []domain.FragmentAttachment) (DeliveryResult, error) {
	switch destination.Kind {
	case "file":
		return FileDestinationExecutor{}.Execute(ctx, destination, fragment, attachments)
	case "mcp":
		return MCPDestinationExecutor{}.Execute(ctx, destination, fragment, attachments)
	case "api":
		return APIDestinationExecutor{}.Execute(ctx, destination, fragment, attachments)
	case "cli":
		return CLIDestinationExecutor{}.Execute(ctx, destination, fragment, attachments)
	case "callback":
		return CallbackDestinationExecutor{}.Execute(ctx, destination, fragment, attachments)
	default:
		return DeliveryResult{}, fmt.Errorf("unsupported destination kind %q", destination.Kind)
	}
}

func retryConfigForDestination(destination domain.Destination) domain.DeliveryRetryConfig {
	switch destination.Kind {
	case "file":
		cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](destination)
		if err == nil {
			return withRetryDefaults(cfg.Retry, 1, 0)
		}
	case "mcp":
		cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](destination)
		if err == nil {
			return withRetryDefaults(cfg.Retry, 3, 500)
		}
	case "api":
		cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](destination)
		if err == nil {
			return withRetryDefaults(cfg.Retry, 3, 500)
		}
	case "cli":
		cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](destination)
		if err == nil {
			return withRetryDefaults(cfg.Retry, 3, 500)
		}
	case "callback":
		cfg, err := domain.DecodeDestinationConfig[domain.CallbackDestinationConfig](destination)
		if err == nil {
			return withRetryDefaults(cfg.Retry, 3, 500)
		}
	}
	return domain.DeliveryRetryConfig{MaxAttempts: 1, BackoffMS: 0}
}

func withRetryDefaults(cfg domain.DeliveryRetryConfig, attempts, backoff int) domain.DeliveryRetryConfig {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = attempts
	}
	if cfg.BackoffMS < 0 {
		cfg.BackoffMS = 0
	}
	if cfg.BackoffMS == 0 && backoff > 0 && cfg.MaxAttempts > 1 {
		cfg.BackoffMS = backoff
	}
	return cfg
}
