package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
)

// TestManualIntakeEnricherFetchesGenericURLContent covers the generic
// (non-GitHub, non-Pinterest) URL branch of EnrichIntake: it should now pull
// a real title/summary from the source via the local-only linkcontent
// provider instead of writing the canned "Saved URL from X." placeholder.
func TestManualIntakeEnricherFetchesGenericURLContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head>
<meta property="og:title" content="Real Article Title">
<meta property="og:description" content="A real summary pulled straight from the page.">
</head><body><article><p>Lots of real article body text goes here so extraction has plenty of prose to work with, well past the minimum character threshold used to detect a blocked or empty page.</p></article></body></html>`))
	}))
	defer server.Close()

	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enriched, err := enricher.EnrichIntake(context.Background(), server.URL, "", "", nil, "")
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if enriched.Title != "Real Article Title" {
		t.Fatalf("expected fetched title, got %q", enriched.Title)
	}
	if enriched.Summary != "A real summary pulled straight from the page." {
		t.Fatalf("expected fetched summary, got %q", enriched.Summary)
	}
	if enriched.Metadata["enrichment_status"] != "done" {
		t.Fatalf("expected enrichment_status=done, got %+v", enriched.Metadata)
	}
	if enriched.Metadata["og:description"] != "A real summary pulled straight from the page." {
		t.Fatalf("expected og:description carried into metadata: %+v", enriched.Metadata)
	}
}

// TestManualIntakeEnricherGenericURLBlockedKeepsPlaceholder covers the
// blocked-page case: the local provider reports Content.Blocked (a 403 in
// this fixture), so EnrichIntake must fall back to the placeholder summary
// and flag the fragment as pending for a later retry, without hanging or
// erroring.
func TestManualIntakeEnricherGenericURLBlockedKeepsPlaceholder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("blocked"))
	}))
	defer server.Close()

	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	start := time.Now()
	enriched, err := enricher.EnrichIntake(context.Background(), server.URL, "", "", nil, "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected a blocked (403) response to return fast, took %s", elapsed)
	}
	if !strings.HasPrefix(enriched.Summary, "Saved URL from ") {
		t.Fatalf("expected placeholder summary for blocked page, got %q", enriched.Summary)
	}
	if enriched.Metadata["enrichment_status"] != "pending" {
		t.Fatalf("expected enrichment_status=pending for blocked page, got %+v", enriched.Metadata)
	}
}

// TestManualIntakeEnricherGenericURLRespectsBoundedTimeout covers the
// bounded-timeout requirement directly: with a short provider-level timeout
// override and a handler that never responds, EnrichIntake must still return
// quickly with the placeholder summary and a pending status, rather than
// hanging for the request's own life.
func TestManualIntakeEnricherGenericURLRespectsBoundedTimeout(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer server.Close()
	defer close(block)

	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(linkcontent.NewLocalProvider(config.LinkContentLocalConfig{RequestTimeoutSeconds: 1}))

	start := time.Now()
	enriched, err := enricher.EnrichIntake(context.Background(), server.URL, "", "", nil, "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected bounded provider timeout to return quickly, took %s", elapsed)
	}
	if !strings.HasPrefix(enriched.Summary, "Saved URL from ") {
		t.Fatalf("expected placeholder summary after timeout, got %q", enriched.Summary)
	}
	if enriched.Metadata["enrichment_status"] != "pending" {
		t.Fatalf("expected enrichment_status=pending after timeout, got %+v", enriched.Metadata)
	}
}

// TestManualIntakeEnricherGenericURLKeepsUserSuppliedTitle covers the "only
// set title if not already set from user input" rule: when the caller passes
// a title that differs from the raw URL content, a fetched og:title must not
// override it.
func TestManualIntakeEnricherGenericURLKeepsUserSuppliedTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta property="og:title" content="Fetched Title"></head><body>hi</body></html>`))
	}))
	defer server.Close()

	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enriched, err := enricher.EnrichIntake(context.Background(), server.URL, "My Own Title", "", nil, "")
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if enriched.Title != "My Own Title" {
		t.Fatalf("expected user-supplied title to be preserved, got %q", enriched.Title)
	}
}
