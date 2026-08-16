package linkcontent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func newTestLocalProvider() Provider {
	return NewLocalProvider(config.LinkContentLocalConfig{})
}

func TestLocalProvider_ExtractsTitleSummaryAndTextWithOGDescription(t *testing.T) {
	const html = `<!doctype html>
<html>
<head>
<title>Fallback Title</title>
<meta property="og:title" content="Roadmap Planning Notes" />
<meta property="og:description" content="A short, deliberate summary of the roadmap planning session and its outcomes." />
</head>
<body>
<article>
<h1>Roadmap Planning Notes</h1>
<p>This is the first paragraph of a much longer article body that goes on for a while, describing the roadmap in detail across several sentences so that readability extraction has enough content to treat this as the main article body rather than boilerplate chrome around the page.</p>
<p>A second paragraph continues the discussion with more detail about phases, milestones, and the overall plan for the next quarter of work.</p>
</article>
</body>
</html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	provider := newTestLocalProvider()
	if provider.Backend() != "local" {
		t.Fatalf("unexpected backend: %q", provider.Backend())
	}

	out, err := provider.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out.Blocked {
		t.Fatalf("expected not blocked, got blocked: %+v", out)
	}
	if out.Title != "Roadmap Planning Notes" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
	if out.Summary != "A short, deliberate summary of the roadmap planning session and its outcomes." {
		t.Fatalf("unexpected summary: %q", out.Summary)
	}
	if !strings.Contains(out.Text, "roadmap in detail") {
		t.Fatalf("expected extracted text to contain article body, got: %q", out.Text)
	}
	if out.Metadata["og:title"] != "Roadmap Planning Notes" {
		t.Fatalf("expected og:title in metadata: %+v", out.Metadata)
	}
	if out.Metadata["og:description"] == "" {
		t.Fatalf("expected og:description in metadata: %+v", out.Metadata)
	}
	if out.Metadata["status_code"] != http.StatusOK {
		t.Fatalf("unexpected status_code metadata: %+v", out.Metadata)
	}
	if out.Metadata["content_type"] != "text/html" {
		t.Fatalf("unexpected content_type metadata: %+v", out.Metadata)
	}
	if out.FinalURL != srv.URL+"/" && out.FinalURL != srv.URL {
		t.Fatalf("unexpected final url: %q", out.FinalURL)
	}
}

func TestLocalProvider_FallsBackToFirst200CharsWithoutOGDescription(t *testing.T) {
	const html = `<!doctype html>
<html>
<head>
<title>No Description Article</title>
</head>
<body>
<article>
<h1>No Description Article</h1>
<p>This article intentionally has no og:description or meta description tag at all, so the summary should fall back to a trimmed preview of the extracted body text, cut at roughly two hundred characters and broken cleanly rather than mid word wherever that boundary happens to land within this paragraph of filler content written specifically to exceed the two hundred character threshold used by the summary preview logic under test here.</p>
</article>
</body>
</html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	provider := newTestLocalProvider()
	out, err := provider.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out.Blocked {
		t.Fatalf("expected not blocked, got blocked: %+v", out)
	}
	if out.Title != "No Description Article" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
	if _, ok := out.Metadata["og:description"]; ok {
		t.Fatalf("did not expect og:description metadata: %+v", out.Metadata)
	}
	if out.Summary == "" {
		t.Fatalf("expected non-empty fallback summary")
	}
	if len(out.Summary) > 200 {
		t.Fatalf("expected summary trimmed to ~200 chars, got %d: %q", len(out.Summary), out.Summary)
	}
	if !strings.Contains(out.Text, "This article intentionally has no og:description") {
		t.Fatalf("unexpected extracted text: %q", out.Text)
	}
	summaryBody := strings.TrimSuffix(out.Summary, "...")
	if !strings.HasPrefix(out.Text, summaryBody) {
		t.Fatalf("expected summary to be derived from the start of the extracted text, got summary=%q text=%q", out.Summary, out.Text)
	}
}

func TestLocalProvider_BlockedOn403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><body><p>Forbidden</p></body></html>`))
	}))
	defer srv.Close()

	provider := newTestLocalProvider()
	out, err := provider.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !out.Blocked {
		t.Fatalf("expected blocked=true for 403 response, got: %+v", out)
	}
	if out.Metadata["status_code"] != http.StatusForbidden {
		t.Fatalf("unexpected status_code metadata: %+v", out.Metadata)
	}
}

func TestLocalProvider_BlockedOnCloudflareChallengePage(t *testing.T) {
	const challengeHTML = `<!doctype html>
<html>
<head><title>Just a moment...</title></head>
<body>
<div id="challenge">Enable JavaScript and cookies to continue</div>
</body>
</html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(challengeHTML))
	}))
	defer srv.Close()

	provider := newTestLocalProvider()
	out, err := provider.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !out.Blocked {
		t.Fatalf("expected blocked=true for cloudflare challenge page, got: %+v", out)
	}
}

func TestNewProvider_ReturnsLocalBackend(t *testing.T) {
	provider := NewProvider(config.LinkContentConfig{Backend: "local"})
	if provider == nil {
		t.Fatalf("expected non-nil provider")
	}
	if provider.Backend() != "local" {
		t.Fatalf("unexpected backend: %q", provider.Backend())
	}
}

func TestNewProvider_NoneOrEmptyBackendReturnsNil(t *testing.T) {
	if p := NewProvider(config.LinkContentConfig{Backend: ""}); p != nil {
		t.Fatalf("expected nil provider for empty backend, got %+v", p)
	}
	if p := NewProvider(config.LinkContentConfig{Backend: "none"}); p != nil {
		t.Fatalf("expected nil provider for 'none' backend, got %+v", p)
	}
}

func TestNewProvider_LocalWithFirecrawlFallbackWrapsProvider(t *testing.T) {
	provider := NewProvider(config.LinkContentConfig{Backend: "local", FallbackBackend: "firecrawl"})
	if provider == nil {
		t.Fatalf("expected non-nil provider")
	}
	wrapped, ok := provider.(*fallbackProvider)
	if !ok {
		t.Fatalf("expected fallback-wrapped provider, got %T", provider)
	}
	if wrapped.primary.Backend() != "local" {
		t.Fatalf("unexpected primary backend: %q", wrapped.primary.Backend())
	}
	if wrapped.fallback.Backend() != "firecrawl" {
		t.Fatalf("unexpected fallback backend: %q", wrapped.fallback.Backend())
	}
	if provider.Backend() != "local" {
		t.Fatalf("unexpected backend: %q", provider.Backend())
	}
}
