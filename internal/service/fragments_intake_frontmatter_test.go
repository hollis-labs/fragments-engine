package service

import (
	"context"
	"encoding/json"
	"testing"
)

// TestFragmentServiceIntake_FrontmatterAndAgentSource covers the agent-drop
// intake path: a markdown draft with a YAML frontmatter block, submitted with
// Source: "agent". Frontmatter title/tags feed the same fields an explicit
// caller-supplied title/tags would, the frontmatter block is stripped from
// stored Content, and the whole block is preserved in
// metadata["frontmatter"].
func TestFragmentServiceIntake_FrontmatterAndAgentSource(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	content := "---\ntitle: Draft from an agent\ntags:\n  - draft\n  - review\n---\nBody text the agent wrote.\n"
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: content,
		Source:  "agent",
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}

	if detail.Fragment.Source != "agent" {
		t.Fatalf("expected source agent, got %q", detail.Fragment.Source)
	}
	if detail.Fragment.Title != "Draft from an agent" {
		t.Fatalf("expected frontmatter title to populate the fragment title, got %q", detail.Fragment.Title)
	}
	if got := detail.Fragment.Content; got != "Body text the agent wrote." {
		t.Fatalf("expected frontmatter block stripped from stored content, got %q", got)
	}
	assertEntityPresent(t, detail.Entities, "tag", "draft")
	assertEntityPresent(t, detail.Entities, "tag", "review")

	var meta map[string]any
	if err := json.Unmarshal([]byte(detail.Fragment.MetadataJSON), &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	frontmatter, ok := meta["frontmatter"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata[\"frontmatter\"] to be preserved, got %+v", meta)
	}
	if frontmatter["title"] != "Draft from an agent" {
		t.Fatalf("expected frontmatter title preserved verbatim, got %+v", frontmatter)
	}
}

// TestFragmentServiceIntake_PublicationPathAndNotifyNow covers the write_doc
// contract additions: an explicit PublicationPath is stored in metadata, and
// NotifyNow=true adds the reserved "notify" tag regardless of what other
// tags the submission carries.
func TestFragmentServiceIntake_PublicationPathAndNotifyNow(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content:         "The audit findings.",
		Source:          "agent",
		Tags:            []string{"report"},
		PublicationPath: "docs/reports/2026-audit.md",
		NotifyNow:       true,
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}

	assertEntityPresent(t, detail.Entities, "tag", "report")
	assertEntityPresent(t, detail.Entities, "tag", "notify")

	var meta map[string]any
	if err := json.Unmarshal([]byte(detail.Fragment.MetadataJSON), &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta["publication_path"] != "docs/reports/2026-audit.md" {
		t.Fatalf("expected publication_path stored in metadata, got %+v", meta)
	}
}

// TestFragmentServiceIntake_PublicationPathAndNotifyNow_FromFrontmatter
// covers the same fields set via frontmatter instead of explicit request
// fields, for the hook-nudged self-written-file flow.
func TestFragmentServiceIntake_PublicationPathAndNotifyNow_FromFrontmatter(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	content := "---\ntitle: Fan-out audit\ntags: [report]\npublication_path: docs/reports/2026-audit.md\nnotify_now: true\n---\nFindings body.\n"
	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: content,
		Source:  "agent",
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}

	assertEntityPresent(t, detail.Entities, "tag", "report")
	assertEntityPresent(t, detail.Entities, "tag", "notify")

	var meta map[string]any
	if err := json.Unmarshal([]byte(detail.Fragment.MetadataJSON), &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta["publication_path"] != "docs/reports/2026-audit.md" {
		t.Fatalf("expected publication_path promoted from frontmatter, got %+v", meta)
	}
}

// TestFragmentServiceIntake_NoSource_DefaultsManual covers backward
// compatibility: a caller that doesn't set Source (every pre-existing
// caller) still gets a "manual" fragment.
func TestFragmentServiceIntake_NoSource_DefaultsManual(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "plain content, no frontmatter, no explicit source",
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Fragment.Source != "manual" {
		t.Fatalf("expected default source manual, got %q", detail.Fragment.Source)
	}
}
