package service

import (
	"context"
	"testing"
)

func TestFragmentServiceIntakeLegacyAdapterReplayContextAndRevision(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()
	ctx := context.Background()
	firstReq := IntakeRequest{
		SourceURL: "https://example.test/article", Title: "Source title",
		Description: "Source description", Content: "first source body",
		Tags: []string{"alpha"}, Selection: "legacy selection",
		Highlights: []string{"highlight one", "highlight two"}, Notes: []string{"note one"},
	}
	first, err := svcs.fragments.Intake(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != "inserted" {
		t.Fatalf("first outcome=%s", first.Outcome)
	}
	replay, err := svcs.fragments.Intake(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if replay.FragmentID != first.FragmentID || replay.Outcome != "skipped" {
		t.Fatalf("exact replay=%+v first=%+v", replay, first)
	}
	assertLegacySQLCount(t, svcs, `SELECT COUNT(*) FROM capture_attempts WHERE fragment_id = ?`, first.FragmentID, 1)
	// Selection remains one compatibility alias alongside both independent
	// highlight values; it does not replace or collapse the multi-value form.
	assertLegacySQLCount(t, svcs, `SELECT COUNT(*) FROM capture_annotations WHERE fragment_id = ?`, first.FragmentID, 4)

	additiveReq := firstReq
	additiveReq.Tags = []string{"beta"}
	additiveReq.Selection = ""
	additiveReq.Highlights = []string{"highlight three"}
	additiveReq.Notes = []string{"note two"}
	additive, err := svcs.fragments.Intake(ctx, additiveReq)
	if err != nil {
		t.Fatal(err)
	}
	if additive.FragmentID != first.FragmentID || additive.Outcome != "skipped" {
		t.Fatalf("additive context=%+v first=%+v", additive, first)
	}
	assertLegacySQLCount(t, svcs, `SELECT COUNT(*) FROM fragment_revisions WHERE fragment_id = ?`, first.FragmentID, 1)
	assertLegacySQLCount(t, svcs, `SELECT COUNT(*) FROM fragment_tag_observations WHERE fragment_id = ?`, first.FragmentID, 2)
	assertLegacySQLCount(t, svcs, `SELECT COUNT(*) FROM capture_annotations WHERE fragment_id = ?`, first.FragmentID, 6)

	changedReq := additiveReq
	changedReq.Content = "changed source body"
	changed, err := svcs.fragments.Intake(ctx, changedReq)
	if err != nil {
		t.Fatal(err)
	}
	if changed.FragmentID != first.FragmentID || changed.Outcome != "updated" {
		t.Fatalf("changed material=%+v first=%+v", changed, first)
	}
	assertLegacySQLCount(t, svcs, `SELECT COUNT(*) FROM fragment_revisions WHERE fragment_id = ?`, first.FragmentID, 2)
}

func assertLegacySQLCount(t *testing.T, svcs manualTestServices, query, fragmentID string, want int) {
	t.Helper()
	var got int
	if err := svcs.db.QueryRow(query, fragmentID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d want %d", query, got, want)
	}
}
