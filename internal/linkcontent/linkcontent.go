// Package linkcontent provides a swappable content-fetch/enrichment abstraction for URLs.
//
// It mirrors the shape of internal/analyze's VisionAnalyzer: a small interface backed by
// one or more concrete backends, wired together by NewProvider from config.LinkContentConfig.
package linkcontent

import (
	"context"
	"fmt"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

// Content is the normalized result of fetching and enriching a URL.
type Content struct {
	FinalURL string
	Title    string
	Summary  string
	Text     string
	Metadata map[string]any
	Blocked  bool
}

// Provider fetches and enriches content for a URL.
type Provider interface {
	Backend() string
	Fetch(ctx context.Context, rawURL string) (Content, error)
}

// NewProvider builds a Provider from config, wiring the primary backend and (when a
// distinct fallback backend is configured and implemented) wrapping it so the fallback is
// retried on error or when the primary reports Content.Blocked.
func NewProvider(cfg config.LinkContentConfig) Provider {
	primary := buildProvider(strings.ToLower(strings.TrimSpace(cfg.Backend)), cfg)
	fallback := buildProvider(strings.ToLower(strings.TrimSpace(cfg.FallbackBackend)), cfg)
	if primary == nil {
		return nil
	}
	if fallback == nil || fallback.Backend() == primary.Backend() {
		return primary
	}
	return &fallbackProvider{
		primary:  primary,
		fallback: fallback,
	}
}

func buildProvider(backend string, cfg config.LinkContentConfig) Provider {
	switch backend {
	case "", "none":
		return nil
	case "local":
		return NewLocalProvider(cfg.Local)
	case "firecrawl":
		return NewFirecrawlProvider(cfg.Firecrawl)
	default:
		return nil
	}
}

// fallbackProvider retries the fallback backend when the primary backend errors or
// reports the fetch as blocked. Mirrors fallbackVisionAnalyzer in internal/analyze/vision.go.
type fallbackProvider struct {
	primary  Provider
	fallback Provider
}

func (p *fallbackProvider) Backend() string {
	return p.primary.Backend()
}

func (p *fallbackProvider) Fetch(ctx context.Context, rawURL string) (Content, error) {
	out, err := p.primary.Fetch(ctx, rawURL)
	if err == nil && !out.Blocked {
		return out, nil
	}
	fallbackOut, fallbackErr := p.fallback.Fetch(ctx, rawURL)
	if fallbackErr == nil {
		return fallbackOut, nil
	}
	if err != nil {
		return Content{}, fmt.Errorf("primary %s failed: %v; fallback %s failed: %w", p.primary.Backend(), err, p.fallback.Backend(), fallbackErr)
	}
	return Content{}, fmt.Errorf("primary %s reported blocked content; fallback %s failed: %w", p.primary.Backend(), p.fallback.Backend(), fallbackErr)
}
