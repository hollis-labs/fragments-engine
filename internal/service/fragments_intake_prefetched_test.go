package service

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
)

// TestFragmentServiceIntake_PrefetchedSourceURL_FullFlow drives the full
// FragmentService.Intake pipeline (not just EnrichIntake in isolation) with a
// source_url + already-extracted content request, covering the acceptance
// criteria end to end: source_type=article, Summary derived from the first
// ~200 chars of content, metadata.url/metadata.domain from source_url, no
// enrichment_status key at all, and hashtag extraction / tag merging
// unaffected.
func TestFragmentServiceIntake_PrefetchedSourceURL_FullFlow(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	fake := &countingLinkProvider{}
	svcs.fragments.enricher.SetLinkProvider(fake)

	content := "# Clipped Article\n\n" + strings.Repeat("Real extracted page content that a client already fetched. ", 6) + "#reading"
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content:   content,
		SourceURL: "https://blog.example.com/posts/clipped-article",
		Tags:      []string{"clipper"},
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if fake.fetchCalls != 0 {
		t.Fatalf("expected no network fetch for source_url intake, got %d call(s)", fake.fetchCalls)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}

	if detail.Fragment.Content != content {
		t.Fatalf("expected content to be stored byte-identical, got %q want %q", detail.Fragment.Content, content)
	}
	if detail.Fragment.SourceType != "article" {
		t.Fatalf("expected source_type article, got %s", detail.Fragment.SourceType)
	}
	wantSummary := linkcontent.PreviewText(content, 200)
	if detail.Fragment.Summary != wantSummary {
		t.Fatalf("expected derived summary %q, got %q", wantSummary, detail.Fragment.Summary)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"url":"https://blog.example.com/posts/clipped-article"`) {
		t.Fatalf("expected metadata.url from source_url: %s", detail.Fragment.MetadataJSON)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"domain":"blog.example.com"`) {
		t.Fatalf("expected metadata.domain from source_url: %s", detail.Fragment.MetadataJSON)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"capture_method":"web_clipper"`) {
		t.Fatalf("expected capture_method provenance metadata: %s", detail.Fragment.MetadataJSON)
	}
	if strings.Contains(detail.Fragment.MetadataJSON, "enrichment_status") {
		t.Fatalf("expected no enrichment_status key at all for pre-fetched content: %s", detail.Fragment.MetadataJSON)
	}

	// Hashtag extraction and caller-supplied tag merging (fragments.go's
	// existing Intake logic) work unmodified for this path.
	assertEntityPresent(t, detail.Entities, "tag", "reading")
	assertEntityPresent(t, detail.Entities, "tag", "clipper")
}

// TestFragmentServiceIntake_PrefetchedSourceURL_DescriptionAndSelection
// covers the description-precedence and selection-storage acceptance bullets
// through the full Intake pipeline.
func TestFragmentServiceIntake_PrefetchedSourceURL_DescriptionAndSelection(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()
	svcs.fragments.enricher.SetLinkProvider(&countingLinkProvider{})

	content := strings.Repeat("Full extracted article body text. ", 10)
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content:     content,
		SourceURL:   "https://blog.example.com/posts/another-article",
		Description: "Meta description captured by the clipper.",
		Selection:   "The bit the user actually highlighted.",
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Fragment.Summary != "Meta description captured by the clipper." {
		t.Fatalf("expected description used verbatim as summary, got %q", detail.Fragment.Summary)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"selection":"The bit the user actually highlighted."`) {
		t.Fatalf("expected selection stored in metadata, distinct from content: %s", detail.Fragment.MetadataJSON)
	}
	if strings.Contains(detail.Fragment.Content, "The bit the user actually highlighted.") {
		t.Fatalf("selection must never be merged into stored Content")
	}
}

// TestInboxReviewer_NeverTouchesPrefetchedSourceURLFragments confirms the
// async pending/retry machinery (CW-20260816-0029/0031/0032/0033) never
// engages for source_url-driven fragments: ReviewURL gates its retry path on
// singleURL(fragment.Content), and a pre-fetched fragment's Content is the
// full extracted article body, never a bare URL, so it is naturally skipped
// as unsupported -- with zero changes to the reviewer needed. This also
// covers CW-20260816-0063's targeted pending-lookup by construction: a
// fragment that never gets enrichment_status="pending" can never be a
// candidate for that lookup either (that code does not exist in this
// checkout yet, so it cannot be exercised directly).
func TestInboxReviewer_NeverTouchesPrefetchedSourceURLFragments(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()
	svcs.fragments.enricher.SetLinkProvider(&countingLinkProvider{})

	content := strings.Repeat("Pre-extracted article content from the web clipper. ", 8)
	intake, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content:   content,
		SourceURL: "https://blog.example.com/posts/never-reviewed",
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	before, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail before review: %v", err)
	}

	review, err := svcs.reviewer.ReviewOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("review once: %v", err)
	}
	if review.ReviewedCount != 0 || review.UpdatedCount != 0 {
		t.Fatalf("expected the reviewer to leave the pre-fetched fragment untouched, got %+v", review)
	}
	if len(review.Items) != 1 || review.Items[0].Action != "skip_unsupported" {
		t.Fatalf("expected a single skip_unsupported review item, got %+v", review.Items)
	}

	after, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail after review: %v", err)
	}
	if after.Fragment.MetadataJSON != before.Fragment.MetadataJSON {
		t.Fatalf("expected metadata to be completely untouched by the reviewer:\nbefore=%s\nafter=%s", before.Fragment.MetadataJSON, after.Fragment.MetadataJSON)
	}
	if strings.Contains(after.Fragment.MetadataJSON, "enrichment_status") {
		t.Fatalf("expected enrichment_status to never appear, even after a review pass: %s", after.Fragment.MetadataJSON)
	}
}

// TestFragmentServiceIntake_PrefetchedSourceURL_InvalidURLRejected covers the
// same bug fix as TestEnrichIntakePrefetched_InvalidSourceURLFailsLoudly, one
// layer up: a malformed source_url must fail the whole Intake() call (no
// fragment written at all), not silently produce a misclassified one.
func TestFragmentServiceIntake_PrefetchedSourceURL_InvalidURLRejected(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()
	svcs.fragments.enricher.SetLinkProvider(&countingLinkProvider{})

	_, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content:   strings.Repeat("Content that would otherwise intake fine. ", 5),
		SourceURL: "ftp://example.com/unsupported-scheme",
	})
	if err == nil {
		t.Fatal("expected Intake to reject an invalid source_url, got nil error")
	}
}
