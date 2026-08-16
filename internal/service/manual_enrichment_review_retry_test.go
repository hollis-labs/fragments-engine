package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
)

// fakeLinkProvider is a minimal call-counting linkcontent.Provider stub used
// to test ReviewURL's default-branch retry logic without touching the
// network. It always returns the same canned Content/error.
type fakeLinkProvider struct {
	backend string
	calls   int
	content linkcontent.Content
	err     error
}

func (p *fakeLinkProvider) Backend() string {
	if p.backend != "" {
		return p.backend
	}
	return "fake"
}

func (p *fakeLinkProvider) Fetch(ctx context.Context, rawURL string) (linkcontent.Content, error) {
	p.calls++
	return p.content, p.err
}

// sequencedLinkProvider returns a different canned result on each successive
// call (holding on the last entry once exhausted), used to simulate a
// fallback provider that fails on an earlier poll cycle and succeeds later.
type sequencedLinkProvider struct {
	backend string
	calls   int
	results []fakeFetchResult
}

type fakeFetchResult struct {
	content linkcontent.Content
	err     error
}

func (p *sequencedLinkProvider) Backend() string {
	if p.backend != "" {
		return p.backend
	}
	return "fake"
}

func (p *sequencedLinkProvider) Fetch(ctx context.Context, rawURL string) (linkcontent.Content, error) {
	idx := p.calls
	if idx >= len(p.results) {
		idx = len(p.results) - 1
	}
	p.calls++
	if idx < 0 {
		return linkcontent.Content{}, errors.New("sequencedLinkProvider: no results configured")
	}
	r := p.results[idx]
	return r.content, r.err
}

// CW-20260816-0032: ReviewURL's default branch (generic, non-GitHub,
// non-Pinterest URLs) must retry a pending fragment through the
// fallback-capable linkFallback provider and, on success, populate real
// title/summary/metadata and clear enrichment_status.
func TestReviewURLRetriesPendingGenericURLViaFallbackSuccess(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	// Local-only provider used inside EnrichIntake's own generic branch --
	// fails so intake-time enrichment would have been "pending" too.
	enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	fallback := &fakeLinkProvider{
		content: linkcontent.Content{
			Title:   "Real Article Title",
			Summary: "Real fetched summary.",
			Text:    "Full article body text.",
			Metadata: map[string]any{
				"author": "Jane Doe",
			},
		},
	}
	enricher.linkFallback = fallback

	fragment := domain.Fragment{
		ID:         "frag-1",
		Content:    "https://example.com/article",
		SourceType: "url",
	}
	currentMeta := map[string]any{"enrichment_status": "pending"}

	enriched, shouldReview, err := enricher.ReviewURL(context.Background(), fragment, currentMeta)
	if err != nil {
		t.Fatalf("review url: %v", err)
	}
	if !shouldReview {
		t.Fatalf("expected shouldReview=true")
	}
	if fallback.calls != 1 {
		t.Fatalf("expected fallback Fetch to be called once, got %d", fallback.calls)
	}
	if enriched.Title != "Real Article Title" {
		t.Fatalf("expected fetched title to populate, got %q", enriched.Title)
	}
	if enriched.Summary != "Real fetched summary." {
		t.Fatalf("expected fetched summary to populate, got %q", enriched.Summary)
	}
	if got := currentMetaValue(enriched.Metadata, "link_text"); got != "Full article body text." {
		t.Fatalf("expected link_text metadata to populate, got %q", got)
	}
	if got, ok := enriched.Metadata["author"]; !ok || got != "Jane Doe" {
		t.Fatalf("expected fetched metadata to be merged in, got %+v", enriched.Metadata)
	}
	if got := currentMetaValue(enriched.Metadata, "enrichment_status"); got != "done" {
		t.Fatalf("expected enrichment_status to be cleared to done, got %q", got)
	}
}

// Regression: when the fallback-wrapped provider still fails (or still
// reports Blocked), title/summary must stay untouched and enrichment_status
// must remain "pending" -- no retry cap in this task.
func TestReviewURLRetryStillPendingOnFallbackFailure(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	fallback := &fakeLinkProvider{err: errors.New("firecrawl fetch failed")}
	enricher.linkFallback = fallback

	fragment := domain.Fragment{
		ID:         "frag-2",
		Content:    "https://example.com/still-blocked",
		SourceType: "url",
	}
	currentMeta := map[string]any{"enrichment_status": "pending"}

	enriched, shouldReview, err := enricher.ReviewURL(context.Background(), fragment, currentMeta)
	if err != nil {
		t.Fatalf("review url: %v", err)
	}
	if !shouldReview {
		t.Fatalf("expected shouldReview=true")
	}
	if fallback.calls != 1 {
		t.Fatalf("expected fallback Fetch to be called once, got %d", fallback.calls)
	}
	if enriched.Title != "still-blocked" {
		t.Fatalf("expected title to remain the URL-derived placeholder, got %q", enriched.Title)
	}
	if enriched.Summary != "Saved URL from example.com." {
		t.Fatalf("expected summary to remain the placeholder, got %q", enriched.Summary)
	}
	if got := currentMetaValue(enriched.Metadata, "enrichment_status"); got != "pending" {
		t.Fatalf("expected enrichment_status to remain pending, got %q", got)
	}
}

// Also cover the still-Blocked=true case explicitly (as distinct from a hard
// Fetch error).
func TestReviewURLRetryStillPendingWhenFallbackReportsBlocked(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	fallback := &fakeLinkProvider{content: linkcontent.Content{Title: "Should not be used", Blocked: true}}
	enricher.linkFallback = fallback

	fragment := domain.Fragment{
		ID:         "frag-3",
		Content:    "https://example.com/blocked-again",
		SourceType: "url",
	}
	currentMeta := map[string]any{"enrichment_status": "pending"}

	enriched, _, err := enricher.ReviewURL(context.Background(), fragment, currentMeta)
	if err != nil {
		t.Fatalf("review url: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("expected fallback Fetch to be called once, got %d", fallback.calls)
	}
	if enriched.Title != "blocked-again" {
		t.Fatalf("expected Blocked fetch result to be ignored and title to remain the URL-derived placeholder, got %q", enriched.Title)
	}
	if got := currentMetaValue(enriched.Metadata, "enrichment_status"); got != "pending" {
		t.Fatalf("expected enrichment_status to remain pending, got %q", got)
	}
}

// The no-op skip guard: when enrichment_status (from currentMeta, i.e. the
// pre-review state) is anything other than "pending" -- absent or "done" --
// the fallback provider must never be called. This is what keeps the
// reviewer from re-fetching (and re-billing Firecrawl for) every
// already-enriched or already-given-up link on every poll cycle.
func TestReviewURLSkipsFallbackWhenEnrichmentStatusNotPending(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta map[string]any
	}{
		{name: "absent", meta: map[string]any{}},
		{name: "done", meta: map[string]any{"enrichment_status": "done"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enricher := NewManualIntakeEnricher(nil, t.TempDir())
			enricher.linkProvider = &fakeLinkProvider{content: linkcontent.Content{Title: "Local Title"}}
			fallback := &fakeLinkProvider{content: linkcontent.Content{Title: "Should Not Be Used"}}
			enricher.linkFallback = fallback

			fragment := domain.Fragment{
				Content:    "https://example.com/already-handled",
				SourceType: "url",
			}

			if _, _, err := enricher.ReviewURL(context.Background(), fragment, tc.meta); err != nil {
				t.Fatalf("review url: %v", err)
			}
			if fallback.calls != 0 {
				t.Fatalf("expected fallback Fetch not to be called, got %d calls", fallback.calls)
			}
		})
	}
}

// e.linkFallback == nil must be handled gracefully (no panic, no-op) in case
// some caller never wired it in.
func TestReviewURLNoopsWhenLinkFallbackNotWired(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	// enricher.linkFallback intentionally left nil.

	fragment := domain.Fragment{
		Content:    "https://example.com/no-fallback-wired",
		SourceType: "url",
	}
	currentMeta := map[string]any{"enrichment_status": "pending"}

	enriched, shouldReview, err := enricher.ReviewURL(context.Background(), fragment, currentMeta)
	if err != nil {
		t.Fatalf("review url: %v", err)
	}
	if !shouldReview {
		t.Fatalf("expected shouldReview=true")
	}
	if got := currentMetaValue(enriched.Metadata, "enrichment_status"); got != "pending" {
		t.Fatalf("expected enrichment_status to remain pending, got %q", got)
	}
}

// Direct unit test for the needsReReview gating fix: a generic "url"
// fragment with enrichment_status="pending" must be re-reviewed even though
// review_version already matches inboxReviewerVersion.
func TestNeedsReReviewForPendingGenericURLWithReviewVersionSet(t *testing.T) {
	svc := &InboxReviewerService{}
	meta := map[string]any{
		"review_version":    inboxReviewerVersion,
		"enrichment_status": "pending",
	}
	if !svc.needsReReview(domain.Fragment{SourceType: "url"}, meta) {
		t.Fatal("expected pending generic-url fragment to require re-review even though review_version already matches")
	}
	doneMeta := map[string]any{
		"review_version":    inboxReviewerVersion,
		"enrichment_status": "done",
	}
	if svc.needsReReview(domain.Fragment{SourceType: "url"}, doneMeta) {
		t.Fatal("expected done generic-url fragment not to require re-review")
	}
}

// End-to-end regression at the InboxReviewerService/reviewFragment level:
// proves that without the needsReReview fix, a generic-URL fragment left
// "pending" after its first review pass would never be retried on a later
// poll cycle (since ReviewURL unconditionally sets review_version on every
// pass). The fallback provider fails on the first poll and succeeds on the
// second, so a passing test here demonstrates the retry actually reaches
// ReviewURL again on poll #2.
func TestInboxReviewerRetriesPendingGenericURLOnNextPollCycle(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	svcs.reviewer.enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	fallback := &sequencedLinkProvider{
		results: []fakeFetchResult{
			{err: errors.New("still blocked on first poll")},
			{content: linkcontent.Content{Title: "Second Poll Title", Summary: "Second poll summary."}},
		},
	}
	svcs.reviewer.enricher.linkFallback = fallback

	intake, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://example.com/generic-article",
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	firstReview, err := svcs.reviewer.ReviewOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("first review once: %v", err)
	}
	if firstReview.ReviewedCount != 1 {
		t.Fatalf("expected first poll to review the fragment, got %+v", firstReview)
	}
	if fallback.calls != 1 {
		t.Fatalf("expected fallback Fetch to be called once after first poll, got %d", fallback.calls)
	}
	afterFirst, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail after first review: %v", err)
	}
	firstMeta := decodeFragmentMetadata(afterFirst.Fragment.MetadataJSON)
	if currentMetaValue(firstMeta, "review_version") != inboxReviewerVersion {
		t.Fatalf("expected review_version to be set after first poll: %+v", firstMeta)
	}
	if currentMetaValue(firstMeta, "enrichment_status") != "pending" {
		t.Fatalf("expected enrichment_status still pending after first poll: %+v", firstMeta)
	}

	secondReview, err := svcs.reviewer.ReviewOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("second review once: %v", err)
	}
	if secondReview.SkippedCount != 0 || secondReview.ReviewedCount != 1 {
		t.Fatalf("expected second poll to re-review (not skip) the still-pending fragment, got %+v", secondReview)
	}
	if fallback.calls != 2 {
		t.Fatalf("expected fallback Fetch to be called again on second poll, got %d", fallback.calls)
	}
	afterSecond, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail after second review: %v", err)
	}
	if afterSecond.Fragment.Title != "Second Poll Title" {
		t.Fatalf("expected title to update on second poll retry, got %q", afterSecond.Fragment.Title)
	}
	if afterSecond.Fragment.Summary != "Second poll summary." {
		t.Fatalf("expected summary to update on second poll retry, got %q", afterSecond.Fragment.Summary)
	}
	secondMeta := decodeFragmentMetadata(afterSecond.Fragment.MetadataJSON)
	if currentMetaValue(secondMeta, "enrichment_status") != "done" {
		t.Fatalf("expected enrichment_status done after successful second-poll retry: %+v", secondMeta)
	}
}
