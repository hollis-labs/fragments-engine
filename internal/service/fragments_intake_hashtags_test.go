package service

import (
	"context"
	"testing"
)

// TestFragmentServiceIntake_HashtagOnly_NoOtherBehaviorChange covers: "#link"
// alone extracts as a tag with no other change in behavior for non-URL
// content (source type detection is untouched, no URL is surfaced).
func TestFragmentServiceIntake_HashtagOnly_NoOtherBehaviorChange(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	content := "#link"
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: content,
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if result.LinkURL != "" {
		t.Fatalf("expected no extracted url for hashtag-only content, got %q", result.LinkURL)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Fragment.Content != content {
		t.Fatalf("expected content to be byte-identical, got %q want %q", detail.Fragment.Content, content)
	}
	if detail.Fragment.SourceType != "text" {
		t.Fatalf("expected unaffected source type detection (text), got %s", detail.Fragment.SourceType)
	}
	assertEntityPresent(t, detail.Entities, "tag", "link")
}

// TestFragmentServiceIntake_LinkTagWithEmbeddedURL covers: "check this out
// #link https://example.com/foo" is detected as link-shaped and the correct
// URL is extracted, without requiring content to be exactly the URL.
func TestFragmentServiceIntake_LinkTagWithEmbeddedURL(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	content := "check this out #link https://example.com/foo"
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: content,
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if result.LinkURL != "https://example.com/foo" {
		t.Fatalf("expected extracted url https://example.com/foo, got %q", result.LinkURL)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Fragment.Content != content {
		t.Fatalf("expected content to be byte-identical, got %q want %q", detail.Fragment.Content, content)
	}
	if detail.Fragment.SourceType != "url" {
		t.Fatalf("expected content to be detected as link-shaped (source type url), got %s", detail.Fragment.SourceType)
	}
	assertEntityPresent(t, detail.Entities, "tag", "link")
}

// TestFragmentServiceIntake_BareURL_RegressionUnaffected covers: a bare
// "https://example.com" with no hashtag still works exactly as it does
// today.
func TestFragmentServiceIntake_BareURL_RegressionUnaffected(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	content := "https://example.com"
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: content,
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Fragment.Content != content {
		t.Fatalf("expected content to be byte-identical, got %q want %q", detail.Fragment.Content, content)
	}
	if detail.Fragment.SourceType != "url" {
		t.Fatalf("expected bare url to still be detected as source type url, got %s", detail.Fragment.SourceType)
	}
	for _, entity := range detail.Entities {
		if entity.Kind == "tag" {
			t.Fatalf("expected no tag entities for plain bare-url intake, found %+v", entity)
		}
	}
}

// TestFragmentServiceIntake_MultipleHashtags covers: multiple hashtags in one
// fragment ("#link #reading") all get extracted as separate tags.
func TestFragmentServiceIntake_MultipleHashtags(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	content := "#link #reading"
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: content,
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Fragment.Content != content {
		t.Fatalf("expected content to be byte-identical, got %q want %q", detail.Fragment.Content, content)
	}
	assertEntityPresent(t, detail.Entities, "tag", "link")
	assertEntityPresent(t, detail.Entities, "tag", "reading")
}

// TestFragmentServiceIntake_ContentNeverMutated is a focused check that
// req.Content and the persisted Fragment.Content are byte-identical to the
// input across a handful of hashtag/URL-bearing shapes.
func TestFragmentServiceIntake_ContentNeverMutated(t *testing.T) {
	contents := []string{
		"#link",
		"#link #reading",
		"check this out #link https://example.com/foo",
		"https://example.com",
		"plain text with #hashtags and #MoreHashtags mixed in, plus a url https://example.org/bar?query=1",
	}

	for _, content := range contents {
		content := content
		t.Run(content, func(t *testing.T) {
			svcs := setupManualTestServices(t)
			defer svcs.close()

			result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
				Content: content,
			})
			if err != nil {
				t.Fatalf("intake: %v", err)
			}
			detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
			if err != nil {
				t.Fatalf("detail: %v", err)
			}
			if detail.Fragment.Content != content {
				t.Fatalf("content mutated: got %q want %q", detail.Fragment.Content, content)
			}
		})
	}
}
