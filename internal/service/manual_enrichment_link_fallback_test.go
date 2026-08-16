package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
)

// TestManualIntakeEnricherLinkFallbackProviderIsFallbackCapable covers
// CW-20260816-0031: ManualIntakeEnricher.SetLinkContentProvider must wire in
// a real fallback-capable linkcontent.Provider (primary "local", fallback
// "firecrawl", built via linkcontent.NewProvider(cfg.LinkContent)) -- not
// just a bare local provider -- as the linkFallback field. This is a pure
// wiring test: it does not exercise EnrichIntake/ReviewURL, which don't read
// linkFallback yet.
//
// To prove the wired-in provider is actually fallback-capable (rather than
// just checking Backend(), which reports "local" for both a bare local
// provider and a fallback-wrapped one per fallbackProvider.Backend() in
// internal/linkcontent/linkcontent.go), this points the primary (local)
// backend at an address nothing listens on so its Fetch fails, then asserts
// the wired-in provider still succeeds by retrying through a stubbed
// Firecrawl backend. A bare local provider would just surface the
// connection error instead.
func TestManualIntakeEnricherLinkFallbackProviderIsFallbackCapable(t *testing.T) {
	const marker = "FIRECRAWL-FALLBACK-MARKER"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"Fallback body text long enough to clear the blocked-content minimum length check used by isBlocked.","metadata":{"title":"` + marker + `"}}}`))
	}))
	defer server.Close()

	t.Setenv("CW20260816_TEST_FIRECRAWL_KEY", "test-key")

	cfg := config.LinkContentConfig{
		Backend:         "local",
		FallbackBackend: "firecrawl",
		Firecrawl: config.LinkContentFirecrawlConfig{
			BaseURL:   server.URL,
			APIKeyEnv: "CW20260816_TEST_FIRECRAWL_KEY",
		},
	}

	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkContentProvider(linkcontent.NewProvider(cfg))

	if enricher.linkFallback == nil {
		t.Fatalf("expected linkFallback to be set")
	}
	if got := enricher.linkFallback.Backend(); got != "local" {
		t.Fatalf("expected fallback-wrapped provider to report primary backend %q, got %q", "local", got)
	}

	// Nothing should be listening on this loopback port, so the primary
	// (local) backend's Fetch call fails and the wrapper must retry via
	// the stubbed Firecrawl backend above.
	content, err := enricher.linkFallback.Fetch(context.Background(), "http://127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatalf("expected fetch to succeed via the fallback firecrawl backend, got error: %v", err)
	}
	if content.Title != marker {
		t.Fatalf("expected content fetched via the fallback firecrawl backend (title %q), got %q", marker, content.Title)
	}
}

// TestManualIntakeEnricherLinkFallbackProviderWithoutFallbackBackendIsLocalOnly
// covers the FallbackBackend empty/"none" case. Per this task's decision
// (documented alongside SetLinkContentProvider and in app.go): the field
// holds the bare local-only provider returned by linkcontent.NewProvider in
// that case, not nil -- this matches NewProvider's own existing behavior
// (buildProvider returns nil fallback, so NewProvider falls back to
// returning just the primary) and needs no special-casing in the wiring
// code.
func TestManualIntakeEnricherLinkFallbackProviderWithoutFallbackBackendIsLocalOnly(t *testing.T) {
	cfg := config.LinkContentConfig{
		Backend: "local",
	}

	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkContentProvider(linkcontent.NewProvider(cfg))

	if enricher.linkFallback == nil {
		t.Fatalf("expected linkFallback to hold the bare local provider when no fallback backend is configured, not nil")
	}
	if got := enricher.linkFallback.Backend(); got != "local" {
		t.Fatalf("expected local-only provider backend %q, got %q", "local", got)
	}
}
