package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
)

// CW-20260816-0033: shouldRetryLinkEnrichment's boundary. attempts is the
// count of fetches already made (0 before the first attempt); the helper
// must keep returning true through attempts==maxLinkEnrichmentAttempts-1
// (so the Nth attempt itself still runs) and switch to false only once
// attempts reaches the cap.
func TestShouldRetryLinkEnrichmentBoundary(t *testing.T) {
	for attempts := 0; attempts < maxLinkEnrichmentAttempts; attempts++ {
		if !shouldRetryLinkEnrichment(attempts) {
			t.Fatalf("expected attempts=%d to still be retryable (cap=%d)", attempts, maxLinkEnrichmentAttempts)
		}
	}
	if shouldRetryLinkEnrichment(maxLinkEnrichmentAttempts) {
		t.Fatalf("expected attempts==cap (%d) to no longer be retryable", maxLinkEnrichmentAttempts)
	}
	if shouldRetryLinkEnrichment(maxLinkEnrichmentAttempts + 1) {
		t.Fatalf("expected attempts beyond cap to remain not retryable")
	}
}

// currentMetaInt must treat an absent key as 0 (not as already-at-cap or an
// error), and must handle both a same-process int (e.g. a hand-built test
// fixture) and a post-JSON-round-trip float64 (see decodeFragmentMetadata in
// inbox_reviewer.go), defaulting to 0 for anything else unparseable.
func TestCurrentMetaIntHandlesIntFloat64AndAbsent(t *testing.T) {
	if got := currentMetaInt(map[string]any{}, "enrichment_attempts"); got != 0 {
		t.Fatalf("expected absent key to default to 0, got %d", got)
	}
	if got := currentMetaInt(map[string]any{"enrichment_attempts": 3}, "enrichment_attempts"); got != 3 {
		t.Fatalf("expected int 3 to read back as 3, got %d", got)
	}
	if got := currentMetaInt(map[string]any{"enrichment_attempts": float64(4)}, "enrichment_attempts"); got != 4 {
		t.Fatalf("expected float64 4 (post-JSON-decode) to read back as 4, got %d", got)
	}
	if got := currentMetaInt(map[string]any{"enrichment_attempts": "not-a-number"}, "enrichment_attempts"); got != 0 {
		t.Fatalf("expected unparseable value to default to 0, got %d", got)
	}
}

// A fixture fragment where every fallback attempt fails across
// maxLinkEnrichmentAttempts real reviewer cycles must end up "failed" with
// enrichment_attempts pinned at the cap, and a subsequent reviewer poll must
// be a total no-op: reviewFragment's top-level review_version+needsReReview
// gate should skip the fragment before ever reaching ReviewURL again, so the
// fallback provider is never called past the cap.
func TestInboxReviewerMarksFailedAfterMaxLinkEnrichmentAttempts(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	svcs.reviewer.enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	results := make([]fakeFetchResult, 0, maxLinkEnrichmentAttempts)
	for i := 0; i < maxLinkEnrichmentAttempts; i++ {
		results = append(results, fakeFetchResult{err: errors.New("still blocked")})
	}
	fallback := &sequencedLinkProvider{results: results}
	svcs.reviewer.enricher.linkFallback = fallback

	intake, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://example.com/permanently-dead",
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	for cycle := 1; cycle <= maxLinkEnrichmentAttempts; cycle++ {
		review, err := svcs.reviewer.ReviewOnce(context.Background(), 10)
		if err != nil {
			t.Fatalf("review cycle %d: %v", cycle, err)
		}
		if review.ReviewedCount != 1 {
			t.Fatalf("expected cycle %d to review the fragment, got %+v", cycle, review)
		}
	}
	if fallback.calls != maxLinkEnrichmentAttempts {
		t.Fatalf("expected exactly %d fallback fetch attempts across %d cycles, got %d", maxLinkEnrichmentAttempts, maxLinkEnrichmentAttempts, fallback.calls)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	meta := decodeFragmentMetadata(detail.Fragment.MetadataJSON)
	if got := currentMetaValue(meta, "enrichment_status"); got != "failed" {
		t.Fatalf("expected enrichment_status=failed after exhausting retries, got %q (%+v)", got, meta)
	}
	if got := currentMetaInt(meta, "enrichment_attempts"); got != maxLinkEnrichmentAttempts {
		t.Fatalf("expected enrichment_attempts==%d, got %d", maxLinkEnrichmentAttempts, got)
	}

	finalReview, err := svcs.reviewer.ReviewOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("final review: %v", err)
	}
	if finalReview.ReviewedCount != 0 || finalReview.SkippedCount != 1 {
		t.Fatalf("expected fragment to be skipped (not reviewed) once failed, got %+v", finalReview)
	}
	if fallback.calls != maxLinkEnrichmentAttempts {
		t.Fatalf("expected fallback Fetch not to be called again once failed, got %d calls (was %d)", fallback.calls, maxLinkEnrichmentAttempts)
	}
}

// A fixture that fails twice then succeeds on the 3rd attempt (still under
// the cap) must end with real Title/Summary populated, enrichment_status
// left at "done" (the existing CW-20260816-0032 success convention -- see
// TestReviewURLRetriesPendingGenericURLViaFallbackSuccess), and
// enrichment_attempts deleted entirely (absent from the metadata map, not
// left behind as a stale 0/2/3 counter).
func TestReviewURLClearsAttemptCounterOnSuccessWithinCap(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	fallback := &sequencedLinkProvider{
		results: []fakeFetchResult{
			{err: errors.New("attempt 1 failed")},
			{err: errors.New("attempt 2 failed")},
			{content: linkcontent.Content{Title: "Third Time Lucky", Summary: "Finally fetched."}},
		},
	}
	enricher.linkFallback = fallback

	fragment := domain.Fragment{
		ID:         "frag-cap-success",
		Content:    "https://example.com/eventually-works",
		SourceType: "url",
	}
	currentMeta := map[string]any{"enrichment_status": "pending"}

	var enriched ManualEnrichment
	for i := 0; i < 3; i++ {
		var err error
		enriched, _, err = enricher.ReviewURL(context.Background(), fragment, currentMeta)
		if err != nil {
			t.Fatalf("review url attempt %d: %v", i+1, err)
		}
		currentMeta = enriched.Metadata
	}

	if fallback.calls != 3 {
		t.Fatalf("expected 3 fallback fetch attempts, got %d", fallback.calls)
	}
	if enriched.Title != "Third Time Lucky" {
		t.Fatalf("expected real fetched title to populate, got %q", enriched.Title)
	}
	if enriched.Summary != "Finally fetched." {
		t.Fatalf("expected real fetched summary to populate, got %q", enriched.Summary)
	}
	if got := currentMetaValue(enriched.Metadata, "enrichment_status"); got != "done" {
		t.Fatalf("expected enrichment_status=done after success within cap, got %q", got)
	}
	if _, ok := enriched.Metadata["enrichment_attempts"]; ok {
		t.Fatalf("expected enrichment_attempts to be absent after success, got %+v", enriched.Metadata["enrichment_attempts"])
	}
}

// Regression guard: currentMeta hand-built without an enrichment_attempts
// key at all (as several CW-20260816-0032 tests do) must be treated as
// attempts==0, i.e. still fully retryable, not as already-at-cap.
func TestReviewURLTreatsAbsentAttemptsAsZero(t *testing.T) {
	enricher := NewManualIntakeEnricher(nil, t.TempDir())
	enricher.linkProvider = &fakeLinkProvider{err: errors.New("local fetch failed")}
	fallback := &fakeLinkProvider{content: linkcontent.Content{Title: "Fetched Title"}}
	enricher.linkFallback = fallback

	fragment := domain.Fragment{
		Content:    "https://example.com/no-attempts-key-yet",
		SourceType: "url",
	}
	currentMeta := map[string]any{"enrichment_status": "pending"}

	enriched, _, err := enricher.ReviewURL(context.Background(), fragment, currentMeta)
	if err != nil {
		t.Fatalf("review url: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("expected absent enrichment_attempts to be treated as 0 (fetch attempted), got %d calls", fallback.calls)
	}
	if enriched.Title != "Fetched Title" {
		t.Fatalf("expected fetch to succeed and populate title, got %q", enriched.Title)
	}
}
