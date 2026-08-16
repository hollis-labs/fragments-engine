package linkcontent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func newTestFirecrawlProvider(t *testing.T, baseURL string) Provider {
	t.Helper()
	t.Setenv("FIRECRAWL_API_KEY", "test-key")
	return NewFirecrawlProvider(config.LinkContentFirecrawlConfig{BaseURL: baseURL})
}

func TestFirecrawlProvider_Backend(t *testing.T) {
	provider := newTestFirecrawlProvider(t, "https://example.invalid")
	if provider.Backend() != "firecrawl" {
		t.Fatalf("unexpected backend: %q", provider.Backend())
	}
}

func TestFirecrawlProvider_FetchUsesScrapeEndpointAndAuthHeader(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "# Roadmap Planning Notes\n\nThis is the article body describing the roadmap in detail across several sentences.",
				"metadata": map[string]any{
					"title":       "Roadmap Planning Notes",
					"description": "A short, deliberate summary of the roadmap planning session and its outcomes.",
					"sourceURL":   "https://example.com/roadmap",
					"statusCode":  200,
					"contentType": "text/html",
				},
			},
		})
	}))
	defer srv.Close()

	provider := newTestFirecrawlProvider(t, srv.URL)
	out, err := provider.Fetch(context.Background(), "https://example.com/roadmap")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %q", gotMethod)
	}
	if gotPath != "/v2/scrape" {
		t.Fatalf("expected /v2/scrape path, got %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("expected bearer auth header, got %q", gotAuth)
	}
	if gotBody["url"] != "https://example.com/roadmap" {
		t.Fatalf("expected url in request body, got %+v", gotBody)
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
		t.Fatalf("unexpected text: %q", out.Text)
	}
	if out.FinalURL != "https://example.com/roadmap" {
		t.Fatalf("unexpected final url: %q", out.FinalURL)
	}
	if out.Metadata["status_code"] != 200 {
		t.Fatalf("unexpected status_code metadata: %+v", out.Metadata)
	}
	if out.Metadata["content_type"] != "text/html" {
		t.Fatalf("unexpected content_type metadata: %+v", out.Metadata)
	}
}

func TestFirecrawlProvider_FallsBackToFirst200CharsWithoutDescription(t *testing.T) {
	longText := strings.Repeat("This article intentionally has no meta description at all. ", 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": longText,
				"metadata": map[string]any{
					"title":       "No Description Article",
					"sourceURL":   "https://example.com/no-description",
					"statusCode":  200,
					"contentType": "text/html",
				},
			},
		})
	}))
	defer srv.Close()

	provider := newTestFirecrawlProvider(t, srv.URL)
	out, err := provider.Fetch(context.Background(), "https://example.com/no-description")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out.Blocked {
		t.Fatalf("expected not blocked, got blocked: %+v", out)
	}
	if out.Summary == "" {
		t.Fatalf("expected non-empty fallback summary")
	}
	if len(out.Summary) > 200 {
		t.Fatalf("expected summary trimmed to ~200 chars, got %d: %q", len(out.Summary), out.Summary)
	}
	summaryBody := strings.TrimSuffix(out.Summary, "...")
	if !strings.HasPrefix(out.Text, summaryBody) {
		t.Fatalf("expected summary derived from start of text, got summary=%q text=%q", out.Summary, out.Text)
	}
}

func TestFirecrawlProvider_TitleAndDescriptionAsArrays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "Body text here that is reasonably long for extraction purposes and such.",
				"metadata": map[string]any{
					"title":       []string{"Array Title"},
					"description": []string{"Array description value."},
					"sourceURL":   "https://example.com/array",
					"statusCode":  200,
					"contentType": "text/html",
				},
			},
		})
	}))
	defer srv.Close()

	provider := newTestFirecrawlProvider(t, srv.URL)
	out, err := provider.Fetch(context.Background(), "https://example.com/array")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out.Title != "Array Title" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
	if out.Summary != "Array description value." {
		t.Fatalf("unexpected summary: %q", out.Summary)
	}
}

func TestFirecrawlProvider_BlockedOnBlockedStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "Forbidden",
				"metadata": map[string]any{
					"title":       "Forbidden",
					"sourceURL":   "https://example.com/blocked",
					"statusCode":  403,
					"contentType": "text/html",
				},
			},
		})
	}))
	defer srv.Close()

	provider := newTestFirecrawlProvider(t, srv.URL)
	out, err := provider.Fetch(context.Background(), "https://example.com/blocked")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !out.Blocked {
		t.Fatalf("expected blocked=true for target statusCode 403, got: %+v", out)
	}
}

func TestFirecrawlProvider_BlockedOnChallengePageText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "Just a moment... Enable JavaScript and cookies to continue",
				"metadata": map[string]any{
					"title":       "Just a moment...",
					"sourceURL":   "https://example.com/challenge",
					"statusCode":  200,
					"contentType": "text/html",
				},
			},
		})
	}))
	defer srv.Close()

	provider := newTestFirecrawlProvider(t, srv.URL)
	out, err := provider.Fetch(context.Background(), "https://example.com/challenge")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !out.Blocked {
		t.Fatalf("expected blocked=true for cloudflare challenge markdown, got: %+v", out)
	}
}

func TestFirecrawlProvider_APIErrorStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "Request rate limit exceeded. Please wait and try again later.",
		})
	}))
	defer srv.Close()

	provider := newTestFirecrawlProvider(t, srv.URL)
	_, err := provider.Fetch(context.Background(), "https://example.com/rate-limited")
	if err == nil {
		t.Fatalf("expected error for 429 response")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected error to surface firecrawl message, got: %v", err)
	}
}

func TestFirecrawlProvider_UnsuccessfulResponseReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "could not scrape url",
		})
	}))
	defer srv.Close()

	provider := newTestFirecrawlProvider(t, srv.URL)
	_, err := provider.Fetch(context.Background(), "https://example.com/unsuccessful")
	if err == nil {
		t.Fatalf("expected error for success=false response")
	}
	if !strings.Contains(err.Error(), "could not scrape url") {
		t.Fatalf("expected error to surface firecrawl message, got: %v", err)
	}
}

func TestFirecrawlProvider_MissingAPIKeyFailsClosedWithTypedError(t *testing.T) {
	t.Setenv("FIRECRAWL_API_KEY", "")
	provider := NewFirecrawlProvider(config.LinkContentFirecrawlConfig{BaseURL: "https://example.invalid"})

	_, err := provider.Fetch(context.Background(), "https://example.com/whatever")
	if err == nil {
		t.Fatalf("expected error when api key is missing")
	}
	var missingKeyErr *MissingAPIKeyError
	if !errors.As(err, &missingKeyErr) {
		t.Fatalf("expected *MissingAPIKeyError, got %T: %v", err, err)
	}
	if missingKeyErr.Backend != "firecrawl" {
		t.Fatalf("unexpected backend on error: %q", missingKeyErr.Backend)
	}
	if missingKeyErr.EnvVar != "FIRECRAWL_API_KEY" {
		t.Fatalf("unexpected env var on error: %q", missingKeyErr.EnvVar)
	}
}

func TestFirecrawlProvider_MissingAPIKeyRespectsConfiguredEnvVar(t *testing.T) {
	t.Setenv("CUSTOM_FIRECRAWL_KEY", "")
	provider := NewFirecrawlProvider(config.LinkContentFirecrawlConfig{
		BaseURL:   "https://example.invalid",
		APIKeyEnv: "CUSTOM_FIRECRAWL_KEY",
	})

	_, err := provider.Fetch(context.Background(), "https://example.com/whatever")
	var missingKeyErr *MissingAPIKeyError
	if !errors.As(err, &missingKeyErr) {
		t.Fatalf("expected *MissingAPIKeyError, got %T: %v", err, err)
	}
	if missingKeyErr.EnvVar != "CUSTOM_FIRECRAWL_KEY" {
		t.Fatalf("unexpected env var on error: %q", missingKeyErr.EnvVar)
	}
}

// --- fallback-wrapped provider integration ---

func TestFallbackProvider_CallsFirecrawlWhenLocalBlocked(t *testing.T) {
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><body><p>Forbidden</p></body></html>`))
	}))
	defer localSrv.Close()

	firecrawlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "Recovered content via firecrawl fallback path for this article body.",
				"metadata": map[string]any{
					"title":       "Recovered via Firecrawl",
					"description": "Recovered description text from the firecrawl fallback backend.",
					"sourceURL":   localSrv.URL,
					"statusCode":  200,
					"contentType": "text/html",
				},
			},
		})
	}))
	defer firecrawlSrv.Close()

	t.Setenv("FIRECRAWL_API_KEY", "test-key")

	provider := NewProvider(config.LinkContentConfig{
		Backend:         "local",
		FallbackBackend: "firecrawl",
		Firecrawl:       config.LinkContentFirecrawlConfig{BaseURL: firecrawlSrv.URL},
	})
	if provider == nil {
		t.Fatalf("expected non-nil provider")
	}

	out, err := provider.Fetch(context.Background(), localSrv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out.Blocked {
		t.Fatalf("expected fallback result to not be blocked: %+v", out)
	}
	if out.Title != "Recovered via Firecrawl" {
		t.Fatalf("expected content from firecrawl fallback, got: %+v", out)
	}
	if out.Summary != "Recovered description text from the firecrawl fallback backend." {
		t.Fatalf("unexpected summary from fallback: %q", out.Summary)
	}
}

func TestFallbackProvider_CallsFirecrawlWhenLocalErrors(t *testing.T) {
	firecrawlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "Recovered content after a primary transport error.",
				"metadata": map[string]any{
					"title":       "Recovered After Error",
					"description": "Recovered description after the primary backend errored outright.",
					"sourceURL":   "https://example.com/errored",
					"statusCode":  200,
					"contentType": "text/html",
				},
			},
		})
	}))
	defer firecrawlSrv.Close()

	t.Setenv("FIRECRAWL_API_KEY", "test-key")

	provider := NewProvider(config.LinkContentConfig{
		Backend:         "local",
		FallbackBackend: "firecrawl",
		Firecrawl:       config.LinkContentFirecrawlConfig{BaseURL: firecrawlSrv.URL},
	})
	if provider == nil {
		t.Fatalf("expected non-nil provider")
	}

	// Use a URL local's http client cannot reach at all, forcing a transport error
	// from the primary backend (rather than a blocked-content result).
	out, err := provider.Fetch(context.Background(), "http://127.0.0.1:0/unreachable")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out.Title != "Recovered After Error" {
		t.Fatalf("expected content from firecrawl fallback after primary error, got: %+v", out)
	}
}

func TestFallbackProvider_BothBackendsFailReturnsCombinedError(t *testing.T) {
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><body><p>Forbidden</p></body></html>`))
	}))
	defer localSrv.Close()

	// Missing API key so firecrawl fails closed too.
	t.Setenv("FIRECRAWL_API_KEY", "")

	provider := NewProvider(config.LinkContentConfig{
		Backend:         "local",
		FallbackBackend: "firecrawl",
	})
	if provider == nil {
		t.Fatalf("expected non-nil provider")
	}

	_, err := provider.Fetch(context.Background(), localSrv.URL)
	if err == nil {
		t.Fatalf("expected error when both primary and fallback fail")
	}
	if !strings.Contains(err.Error(), "firecrawl") {
		t.Fatalf("expected combined error to mention firecrawl backend, got: %v", err)
	}
}
