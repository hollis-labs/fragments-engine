package service

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
)

// countingLinkProvider is a fake linkcontent.Provider that records how many
// times Fetch was called. Used to PROVE (not just eyeball) that the
// source_url pre-fetched-content path never triggers a network fetch --
// unlike the generic-URL branch, which does call linkProvider.Fetch.
type countingLinkProvider struct {
	fetchCalls int
}

func (p *countingLinkProvider) Backend() string { return "counting-fake" }

func (p *countingLinkProvider) Fetch(ctx context.Context, rawURL string) (linkcontent.Content, error) {
	p.fetchCalls++
	return linkcontent.Content{}, nil
}

var _ linkcontent.Provider = (*countingLinkProvider)(nil)

const prefetchedArticleContent = `# A Real Article

This is the full, already-extracted body of an article that a client (the
web clipper browser extension) fetched and parsed on its own, well before
ever calling into fragments-engine. It intentionally runs past two hundred
characters so the derived summary's word-boundary truncation behavior is
exercised by these tests, rather than trivially returning the entire string
untouched. Padding out a bit more to be safe about length here.`

// TestEnrichIntakePrefetched_NoFetch_DerivesSummaryAndURLMetadata covers the
// core acceptance bullet: content = full article markdown, source_url set,
// no description/selection -> source_type=article, Summary is the
// first-~200-chars preview of content, metadata.url/domain come from
// source_url, no enrichment_status key at all, and -- proven via the
// call-counting fake provider, not just an eyeballed response check --
// linkProvider.Fetch is never invoked.
func TestEnrichIntakePrefetched_NoFetch_DerivesSummaryAndURLMetadata(t *testing.T) {
	fake := &countingLinkProvider{}
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(fake)

	enriched, err := enricher.EnrichIntake(context.Background(), prefetchedArticleContent, "", "", nil, "", PrefetchedContent{
		SourceURL: "https://example.com/blog/real-article",
	})
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}

	if fake.fetchCalls != 0 {
		t.Fatalf("expected linkProvider.Fetch to never be called for a source_url request, got %d call(s)", fake.fetchCalls)
	}

	if enriched.SourceType != "article" {
		t.Fatalf("expected source_type article, got %q", enriched.SourceType)
	}

	wantSummary := linkcontent.PreviewText(prefetchedArticleContent, 200)
	if enriched.Summary != wantSummary {
		t.Fatalf("expected derived first-200-chars summary %q, got %q", wantSummary, enriched.Summary)
	}
	if len(enriched.Summary) == len(prefetchedArticleContent) {
		t.Fatalf("expected summary to actually be truncated (content is longer than 200 chars): summary len=%d content len=%d", len(enriched.Summary), len(prefetchedArticleContent))
	}

	if enriched.Metadata["url"] != "https://example.com/blog/real-article" {
		t.Fatalf("expected metadata.url from source_url, got %+v", enriched.Metadata)
	}
	if enriched.Metadata["domain"] != "example.com" {
		t.Fatalf("expected metadata.domain from source_url, got %+v", enriched.Metadata)
	}

	if _, ok := enriched.Metadata["enrichment_status"]; ok {
		t.Fatalf("expected no enrichment_status key at all for pre-fetched content, got %+v", enriched.Metadata)
	}

	if enriched.Metadata["capture_method"] != "web_clipper" {
		t.Fatalf("expected capture_method=web_clipper for provenance, got %+v", enriched.Metadata)
	}

	if _, ok := enriched.Metadata["selection"]; ok {
		t.Fatalf("expected no selection key when Selection was empty, got %+v", enriched.Metadata)
	}
}

// TestEnrichIntakePrefetched_DescriptionTakesPrecedenceOverDerivedSummary
// covers: when description is set, it is used verbatim as Summary instead of
// deriving one from content.
func TestEnrichIntakePrefetched_DescriptionTakesPrecedenceOverDerivedSummary(t *testing.T) {
	fake := &countingLinkProvider{}
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(fake)

	enriched, err := enricher.EnrichIntake(context.Background(), prefetchedArticleContent, "", "", nil, "", PrefetchedContent{
		SourceURL:   "https://example.com/blog/real-article",
		Description: "The page's own meta-description, captured client-side.",
	})
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if fake.fetchCalls != 0 {
		t.Fatalf("expected no fetch, got %d call(s)", fake.fetchCalls)
	}
	if enriched.Summary != "The page's own meta-description, captured client-side." {
		t.Fatalf("expected description used verbatim as summary, got %q", enriched.Summary)
	}
}

// TestEnrichIntakePrefetched_SelectionStoredDistinctFromContent covers: when
// selection is set, it is stored in metadata.selection, distinct from
// Content -- never merged into the returned content/summary.
func TestEnrichIntakePrefetched_SelectionStoredDistinctFromContent(t *testing.T) {
	fake := &countingLinkProvider{}
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(fake)

	const selection = "This exact sentence is what the user highlighted."
	enriched, err := enricher.EnrichIntake(context.Background(), prefetchedArticleContent, "", "", nil, "", PrefetchedContent{
		SourceURL: "https://example.com/blog/real-article",
		Selection: selection,
	})
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if fake.fetchCalls != 0 {
		t.Fatalf("expected no fetch, got %d call(s)", fake.fetchCalls)
	}
	if enriched.Metadata["selection"] != selection {
		t.Fatalf("expected metadata.selection to hold the selection text, got %+v", enriched.Metadata)
	}
	if strings.Contains(enriched.Summary, selection) {
		t.Fatalf("selection must not leak into the derived Summary: %q", enriched.Summary)
	}
}

// TestEnrichIntakePrefetched_ExplicitSourceTypeHonored covers: an explicit,
// non-generic caller-supplied source_type hint (e.g. "reference") is
// preserved verbatim, matching the same convention the generic-URL branch
// already follows for non-"url"/"article" hints.
func TestEnrichIntakePrefetched_ExplicitSourceTypeHonored(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(&countingLinkProvider{})

	enriched, err := enricher.EnrichIntake(context.Background(), prefetchedArticleContent, "", "reference", nil, "", PrefetchedContent{
		SourceURL: "https://example.com/blog/real-article",
	})
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if enriched.SourceType != "reference" {
		t.Fatalf("expected explicit non-generic source_type hint to be preserved, got %q", enriched.SourceType)
	}
}

// TestEnrichIntakePrefetched_TitleFallsBackToDerivedFromURL covers: with no
// caller-supplied title, the title falls back to one derived from the
// source_url (same helper the generic-URL branch uses), not from content.
func TestEnrichIntakePrefetched_TitleFallsBackToDerivedFromURL(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(&countingLinkProvider{})

	enriched, err := enricher.EnrichIntake(context.Background(), prefetchedArticleContent, "", "", nil, "", PrefetchedContent{
		SourceURL: "https://example.com/blog/real-article",
	})
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if enriched.Title != "real-article" {
		t.Fatalf("expected title derived from source_url path leaf, got %q", enriched.Title)
	}
}

// TestEnrichIntakePrefetched_UserSuppliedTitlePreserved covers: a
// caller-supplied title is preserved rather than overridden by the
// derived-from-URL fallback.
func TestEnrichIntakePrefetched_UserSuppliedTitlePreserved(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(&countingLinkProvider{})

	enriched, err := enricher.EnrichIntake(context.Background(), prefetchedArticleContent, "My Clipped Title", "", nil, "", PrefetchedContent{
		SourceURL: "https://example.com/blog/real-article",
	})
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if enriched.Title != "My Clipped Title" {
		t.Fatalf("expected user-supplied title to be preserved, got %q", enriched.Title)
	}
}

// TestEnrichIntakePrefetched_EmptySourceURLUnaffected is a direct regression
// check: PrefetchedContent{} (zero value, as passed by ReviewURL and every
// other existing caller) must not change EnrichIntake's existing bare-URL
// behavior at all.
func TestEnrichIntakePrefetched_EmptySourceURLUnaffected(t *testing.T) {
	fake := &countingLinkProvider{}
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.SetLinkProvider(fake)

	enriched, err := enricher.EnrichIntake(context.Background(), "https://github.com/openai/openai-go", "", "", nil, "", PrefetchedContent{})
	if err != nil {
		t.Fatalf("enrich intake: %v", err)
	}
	if enriched.SourceType != "repo" {
		t.Fatalf("expected untouched GitHub-repo detection to still apply for bare-URL content, got %q", enriched.SourceType)
	}
	if fake.fetchCalls != 0 {
		t.Fatalf("GitHub-repo branch should not call linkProvider.Fetch either, got %d call(s)", fake.fetchCalls)
	}
}
