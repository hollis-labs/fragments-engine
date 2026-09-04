package legacycapture

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

var legacyTestNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func TestAcceptExactReplayAdditiveContextAndChangedMaterial(t *testing.T) {
	st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer st.Close()
	ctx := context.Background()
	base := legacyRequest("same source material")
	base.UserTags = []string{"alpha"}
	base.Highlights = []string{"first highlight"}
	base.Notes = []string{"first note"}

	first, err := svc.Accept(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Accept(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || replay.Outcome != repository.UpsertSkipped {
		t.Fatalf("exact replay = %+v", replay)
	}
	assertCount(t, st.DB, `SELECT COUNT(*) FROM capture_attempts`, 1)
	assertCount(t, st.DB, `SELECT COUNT(*) FROM capture_annotations`, 2)

	additive := legacyRequest("same source material")
	additive.UserTags = []string{"beta"}
	additive.Highlights = []string{"second highlight"}
	additive.Notes = []string{"second note"}
	second, err := svc.Accept(ctx, additive)
	if err != nil {
		t.Fatal(err)
	}
	if second.IdempotentReplay || second.Fragment.ID != first.Fragment.ID || second.Fragment.Revision.ID != first.Fragment.Revision.ID {
		t.Fatalf("additive context changed canonical material: first=%+v second=%+v", first, second)
	}
	assertCount(t, st.DB, `SELECT COUNT(*) FROM capture_attempts`, 2)
	assertCount(t, st.DB, `SELECT COUNT(*) FROM capture_annotations`, 4)
	assertCount(t, st.DB, `SELECT COUNT(*) FROM fragment_tag_observations`, 2)

	changed := legacyRequest("changed source material")
	changed.UserTags = []string{"alpha"}
	third, err := svc.Accept(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if third.Fragment.ID != first.Fragment.ID || third.Fragment.Revision.ID == first.Fragment.Revision.ID || third.Fragment.Revision.Ordinal != 2 {
		t.Fatalf("changed material did not create the next revision: first=%+v third=%+v", first.Fragment, third.Fragment)
	}
	assertCount(t, st.DB, `SELECT COUNT(*) FROM fragments`, 1)
	assertCount(t, st.DB, `SELECT COUNT(*) FROM fragment_revisions`, 2)
}

func TestAcceptProjectionDoesNotPerturbMaterialAndReplayIsReadOnly(t *testing.T) {
	st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer st.Close()
	ctx := context.Background()
	firstReq := legacyRequest("immutable source")
	firstReq.UserTags = []string{"first"}
	firstReq.Projection.Title = "provider title one"
	firstReq.Projection.Content = "provider display one"
	firstReq.Projection.Metadata = map[string]any{"provider_summary": "one", "user_note": "not material"}
	firstReq.Projection.Attachments = []domain.PipelineAttachment{{
		Kind: "image", Role: "content", Name: "derived-one.jpg",
		ExternalURL: "https://cdn.example/expiring-one.jpg", Source: "provider", SourceItemID: "image",
	}}
	first, err := svc.Accept(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}

	secondReq := legacyRequest("immutable source")
	secondReq.UserTags = []string{"second"}
	secondReq.Projection.Title = "provider title two"
	secondReq.Projection.Content = ""
	secondReq.Projection.Metadata = map[string]any{"provider_summary": "two"}
	secondReq.Projection.Attachments = []domain.PipelineAttachment{{
		Kind: "image", Role: "content", Name: "derived-two.jpg",
		ExternalURL: "https://cdn.example/expiring-two.jpg", Source: "provider", SourceItemID: "image",
	}}
	second, err := svc.Accept(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fragment.Revision.ID != first.Fragment.Revision.ID || second.Fragment.Revision.MaterialDigest != first.Fragment.Revision.MaterialDigest {
		t.Fatalf("display/provider projection entered material identity: first=%+v second=%+v", first.Fragment.Revision, second.Fragment.Revision)
	}
	assertCount(t, st.DB, `SELECT COUNT(*) FROM fragment_revisions`, 1)
	var title, content string
	if err := st.DB.QueryRow(`SELECT title, content FROM fragments WHERE id = ?`, first.Fragment.ID).Scan(&title, &content); err != nil {
		t.Fatal(err)
	}
	if title != "provider title two" || content != "" {
		t.Fatalf("empty-body compatibility projection lost: title=%q content=%q", title, content)
	}

	if _, err := st.DB.Exec(`UPDATE fragments SET title = 'post-accept display edit' WHERE id = ?`, first.Fragment.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Accept(ctx, secondReq)
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if err := st.DB.QueryRow(`SELECT title FROM fragments WHERE id = ?`, first.Fragment.ID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "post-accept display edit" {
		t.Fatalf("exact replay reapplied mutable projection: %q", title)
	}
}

func TestAcceptRollsBackCanonicalRowsWhenLegacyProjectionFails(t *testing.T) {
	st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer st.Close()
	req := legacyRequest("atomic source")
	req.UserTags = []string{"tag"}
	req.Projection.Metadata = map[string]any{"invalid": func() {}}
	if _, err := svc.Accept(context.Background(), req); err == nil || !strings.Contains(err.Error(), "encode metadata") {
		t.Fatalf("expected projection encoding failure, got %v", err)
	}
	for _, table := range []string{"fragments", "fragment_revisions", "capture_attempts", "fragment_tag_observations", "fragment_capability_coverage"} {
		assertCount(t, st.DB, `SELECT COUNT(*) FROM `+table, 0)
	}
	req.Projection.Metadata = map[string]any{"retry": "valid"}
	result, err := svc.Accept(context.Background(), req)
	if err != nil || result.Outcome != repository.UpsertInserted || result.IdempotentReplay {
		t.Fatalf("retry after rolled-back projection = %+v, %v", result, err)
	}
	assertCount(t, st.DB, `SELECT COUNT(*) FROM fragments`, 1)
	assertCount(t, st.DB, `SELECT COUNT(*) FROM capture_attempts`, 1)
}

func TestAcceptTwoDatabaseHandlesConvergeWithoutDroppingContext(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	firstStore, firstSvc := openLegacyTestService(t, dbPath)
	defer firstStore.Close()
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondSvc := NewService(repository.NewCaptureRepository(secondStore.DB))
	secondSvc.now = func() time.Time { return legacyTestNow }

	requests := []Request{legacyRequest("concurrent source"), legacyRequest("concurrent source")}
	requests[0].UserTags, requests[0].Highlights = []string{"alpha"}, []string{"highlight alpha"}
	requests[1].UserTags, requests[1].Notes = []string{"beta"}, []string{"note beta"}
	services := []*Service{firstSvc, secondSvc}
	results := make(chan Result, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := range requests {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := services[index].Accept(context.Background(), requests[index])
			results <- result
			errs <- err
		}(index)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var fragmentIDs, revisionIDs []string
	for result := range results {
		fragmentIDs = append(fragmentIDs, result.Fragment.ID)
		revisionIDs = append(revisionIDs, result.Fragment.Revision.ID)
	}
	if fragmentIDs[0] != fragmentIDs[1] || revisionIDs[0] != revisionIDs[1] {
		t.Fatalf("concurrent handles diverged: fragments=%v revisions=%v", fragmentIDs, revisionIDs)
	}
	assertCount(t, firstStore.DB, `SELECT COUNT(*) FROM fragments`, 1)
	assertCount(t, firstStore.DB, `SELECT COUNT(*) FROM fragment_revisions`, 1)
	assertCount(t, firstStore.DB, `SELECT COUNT(*) FROM capture_attempts`, 2)
	assertCount(t, firstStore.DB, `SELECT COUNT(*) FROM fragment_tag_observations`, 2)
	assertCount(t, firstStore.DB, `SELECT COUNT(*) FROM capture_annotations`, 2)
}

func TestAcceptInitializesAllCoverageFromActualTypedEvidence(t *testing.T) {
	st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer st.Close()
	req := legacyRequest("prefetched body")
	req.Material.Title = "supplied title"
	req.Material.Description = "supplied description"
	req.Material.Metadata = map[string]any{"enrichment_status": "complete"}
	req.Projection.Metadata = map[string]any{
		"enrichment_status": "complete", "summary": "derived summary",
		"vision_analysis": "derived vision", "transcript": "not a typed transcript field",
	}
	result, err := svc.Accept(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.DB.Query(`SELECT capability, state FROM fragment_capability_coverage WHERE fragment_revision_id = ?`, result.Fragment.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states := make(map[string]string)
	for rows.Next() {
		var capability, state string
		if err := rows.Scan(&capability, &state); err != nil {
			t.Fatal(err)
		}
		states[capability] = state
	}
	if len(states) != len(domain.AllEnrichmentCapabilities()) {
		t.Fatalf("coverage count=%d states=%v", len(states), states)
	}
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilityTitle, domain.CapabilityDescription, domain.CapabilityBody} {
		if states[string(capability)] != string(domain.CoverageProvided) {
			t.Fatalf("%s coverage=%s", capability, states[string(capability)])
		}
	}
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilitySummary, domain.CapabilityTranscript, domain.CapabilityVision, domain.CapabilityOriginalMedia} {
		if states[string(capability)] != string(domain.CoverageMissing) {
			t.Fatalf("fabricated %s coverage=%s", capability, states[string(capability)])
		}
	}
}

func TestAcceptCanonicalMediaAndCoverageUseOnlySourceAttachments(t *testing.T) {
	st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer st.Close()
	req := legacyRequest("body with source media")
	req.Material.Attachments = []domain.PipelineAttachment{
		{Kind: "image", Role: "gallery", Name: "one.jpg", ExternalURL: "https://example.test/one.jpg", Source: "source", SourceItemID: "one", Metadata: map[string]any{"caption": "source caption"}},
		{Kind: "image", Role: "gallery", Name: "two.jpg", ExternalURL: "https://example.test/two.jpg", Source: "source", SourceItemID: "two"},
	}
	req.Projection = req.Material
	req.Projection.Attachments = append([]domain.PipelineAttachment(nil), req.Material.Attachments...)
	req.Projection.Attachments[0].Metadata = map[string]any{"caption": "source caption", "vision_analysis": map[string]any{"summary": "derived"}}
	req.Projection.Attachments = append(req.Projection.Attachments, domain.PipelineAttachment{
		Kind: "image", Role: "poster", Name: "provider-only.jpg", ExternalURL: "https://provider.test/ephemeral.jpg", Source: "provider", SourceItemID: "provider-only",
	})
	result, err := svc.Accept(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(t, st.DB, `SELECT COUNT(*) FROM attachment_refs`, 2)
	assertCount(t, st.DB, `SELECT COUNT(*) FROM fragment_attachments`, 3)
	var canonicalDerivedMetadata int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_assets WHERE metadata_json LIKE '%vision_analysis%'`).Scan(&canonicalDerivedMetadata); err != nil {
		t.Fatal(err)
	}
	if canonicalDerivedMetadata != 0 {
		t.Fatalf("provider attachment analysis entered canonical source media: %d", canonicalDerivedMetadata)
	}
	var provided int
	if err := st.DB.QueryRow(`
SELECT COUNT(*) FROM fragment_capability_coverage
WHERE fragment_revision_id = ? AND capability IN ('gallery_manifest', 'original_media') AND state = 'provided'`, result.Fragment.Revision.ID).Scan(&provided); err != nil {
		t.Fatal(err)
	}
	if provided != 2 {
		t.Fatalf("source media coverage provided=%d", provided)
	}
}

func TestAcceptAllowsURLOnlySourceWithEmptyDisplayBody(t *testing.T) {
	st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer st.Close()
	req := legacyRequest("")
	req.Material.Title = ""
	req.Material.Description = ""
	req.Projection.Title = "provider display title"
	req.Projection.Content = ""
	result, err := svc.Accept(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var title, content string
	if err := st.DB.QueryRow(`SELECT title, content FROM fragments WHERE id = ?`, result.Fragment.ID).Scan(&title, &content); err != nil {
		t.Fatal(err)
	}
	if title != "provider display title" || content != "" {
		t.Fatalf("url-only projection = title %q content %q", title, content)
	}
	var missing int
	if err := st.DB.QueryRow(`
SELECT COUNT(*) FROM fragment_capability_coverage
WHERE fragment_revision_id = ? AND capability IN ('title', 'body') AND state = 'missing'`, result.Fragment.Revision.ID).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 2 {
		t.Fatalf("provider display fields fabricated source coverage: missing title/body=%d", missing)
	}
}

func TestAcceptRejectsExecutableUserTagWithoutResidue(t *testing.T) {
	st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer st.Close()
	req := legacyRequest("source")
	req.UserTags = []string{"<script>alert(1)</script>"}
	if _, err := svc.Accept(context.Background(), req); err == nil || !strings.Contains(err.Error(), "invalid value") {
		t.Fatalf("expected typed tag validation failure, got %v", err)
	}
	assertCount(t, st.DB, `SELECT COUNT(*) FROM fragments`, 0)
}

func TestTranscriptEvidenceUsesOnlyTypedTranscriptMetadata(t *testing.T) {
	for _, tc := range []struct {
		name      string
		metadata  map[string]any
		wantText  string
		wantState domain.CapabilityState
	}{
		{name: "exact transcript", metadata: map[string]any{"transcript_text": "spoken words"}, wantText: "spoken words", wantState: domain.CoverageProvided},
		{name: "combined youtube body is not transcript", metadata: map[string]any{"description": "description", "enrichment_status": "complete"}, wantState: domain.CoverageMissing},
		{name: "unavailable placeholder", metadata: map[string]any{"transcript_text": "[Transcript unavailable: captions disabled]"}, wantState: domain.CoverageMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, svc := openLegacyTestService(t, filepath.Join(t.TempDir(), "legacy.db"))
			defer st.Close()
			req := legacyRequest("description\n\nspoken words")
			req.Material.Metadata = tc.metadata
			req.Projection.Metadata = tc.metadata
			result, err := svc.Accept(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var state string
			if err := st.DB.QueryRow(`SELECT state FROM fragment_capability_coverage WHERE fragment_revision_id = ? AND capability = 'transcript'`, result.Fragment.Revision.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != string(tc.wantState) {
				t.Fatalf("transcript coverage=%s", state)
			}
			var values []string
			rows, err := st.DB.Query(`SELECT value_json FROM enrichment_observations WHERE fragment_revision_id = ? AND capability = 'transcript'`, result.Fragment.Revision.ID)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var value string
				if err := rows.Scan(&value); err != nil {
					t.Fatal(err)
				}
				values = append(values, value)
			}
			rows.Close()
			if tc.wantText == "" && len(values) != 0 {
				t.Fatalf("unexpected transcript observations: %v", values)
			}
			if tc.wantText != "" && (len(values) != 1 || !strings.Contains(values[0], tc.wantText)) {
				t.Fatalf("transcript observations=%v", values)
			}
		})
	}
}

func openLegacyTestService(t *testing.T, dbPath string) (*store.Store, *Service) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(repository.NewCaptureRepository(st.DB))
	svc.now = func() time.Time { return legacyTestNow }
	return st, svc
}

func legacyRequest(content string) Request {
	material := domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "item-1",
		Title: "source title", Description: "source description", Content: content,
		ContentFormat: "plain_text", CreatedAt: legacyTestNow,
		CanonicalPath: "fragments/url/item-1",
		SourceIdentity: domain.SourceIdentity{
			SourceRegistrationID: "legacy-source", Provider: "example",
			ProviderItemID: "item-1", SourceItemKey: "item-1", SourceLocator: "https://example.test/item-1",
			SegmentKey: "root", SubmittedURL: "https://example.test/item-1", CanonicalURL: "https://example.test/item-1",
			SourceAdapter: domain.AdapterVersion{Adapter: "test-source", Version: "1"},
			Canonicalizer: domain.AdapterVersion{Adapter: "test-canonicalizer", Version: "1"},
		},
	}
	return Request{IngestName: "legacy-test", Material: material, Projection: material, ActorID: "tester"}
}

func assertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d want %d", query, got, want)
	}
}
