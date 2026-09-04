package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

var captureTestTime = time.Date(2026, 9, 3, 18, 42, 0, 0, time.UTC)

func TestCaptureContextExactReplayReturnsStoredOutcomeWithoutDuplicateContext(t *testing.T) {
	ctx := context.Background()
	st, svc, repo := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()

	req := captureRequest(t, "capture-1", "annotation-1", "Research")
	first, err := svc.Accept(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// JSON object order and server receipt time are not semantic payload
	// changes, so the same capture remains an exact retry.
	replayReq := req
	replayReq.PageContextJSON = `{"document_title":"Captured title","selection_present":true}`
	svc.now = func() time.Time { return captureTestTime.Add(time.Hour) }
	replay, err := svc.Accept(ctx, replayReq)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotentReplay || !replay.IdempotentReplay {
		t.Fatalf("replay flags first=%v replay=%v", first.IdempotentReplay, replay.IdempotentReplay)
	}
	if replay.Attempt.ID != first.Attempt.ID || replay.Attempt.FragmentID != first.Attempt.FragmentID || replay.Attempt.FragmentRevisionID != first.Attempt.FragmentRevisionID {
		t.Fatalf("replay changed accepted identifiers: first=%+v replay=%+v", first.Attempt, replay.Attempt)
	}
	if replay.Attempt.AcceptedCaptureCount != 1 || replay.Attempt.AcceptanceResultJSON != first.Attempt.AcceptanceResultJSON {
		t.Fatalf("replay did not preserve acceptance snapshot: first=%+v replay=%+v", first.Attempt, replay.Attempt)
	}
	assertTableCount(t, st.DB, "capture_attempts", 1)
	assertTableCount(t, st.DB, "capture_annotations", 1)
	assertTableCount(t, st.DB, "fragment_tag_observations", 1)
	assertTableCount(t, st.DB, "fragment_description_observations", 1)
	count, err := repo.CaptureCount(ctx, first.Fragment.ID)
	if err != nil || count != 1 {
		t.Fatalf("capture count = %d, %v", count, err)
	}
}

func TestCaptureContextRejectsCapturePayloadAndKeyReuse(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()

	base := captureRequest(t, "capture-1", "annotation-1", "research")
	if _, err := svc.Accept(ctx, base); err != nil {
		t.Fatal(err)
	}

	changedPayload := base
	changedPayload.Annotations = append([]domain.CaptureAnnotation(nil), base.Annotations...)
	changedPayload.Annotations[0].Text = "different"
	_, err := svc.Accept(ctx, changedPayload)
	assertCaptureConflict(t, err)
	changedProtocolPayload := base
	changedProtocolPayload.ProtocolPayloadDigest = "media-manifest-digest-v2"
	_, err = svc.Accept(ctx, changedProtocolPayload)
	assertCaptureConflict(t, err)

	changedKey := base
	changedKey.IdempotencyKey = "other-key"
	_, err = svc.Accept(ctx, changedKey)
	assertCaptureConflict(t, err)

	reusedKey := base
	reusedKey.CaptureID = "capture-2"
	reusedKey.Annotations = []domain.CaptureAnnotation{{ID: "annotation-2", Kind: domain.CaptureAnnotationHighlight, Text: "same quote"}}
	_, err = svc.Accept(ctx, reusedKey)
	assertCaptureConflict(t, err)

	assertTableCount(t, st.DB, "capture_attempts", 1)
	assertTableCount(t, st.DB, "capture_annotations", 1)
}

func TestNewCapturesAppendAttemptsAnnotationsDescriptionsAndUnionTagsWithoutRevisions(t *testing.T) {
	ctx := context.Background()
	st, svc, repo := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()

	firstReq := captureRequest(t, "capture-1", "annotation-1", "Research")
	firstReq.Annotations = append(firstReq.Annotations, domain.CaptureAnnotation{
		ID: "capture-note-1", Kind: domain.CaptureAnnotationNote, Text: "same capture note",
	})
	first, err := svc.Accept(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	secondReq := captureRequest(t, "capture-2", "annotation-2", "research")
	secondReq.CapturedAt = captureTestTime.Add(time.Hour)
	secondReq.IdempotencyKey = "capture-2"
	secondReq.Tags = append(secondReq.Tags, domain.AttributedTag{Value: "Reader"})
	secondReq.Annotations = append(secondReq.Annotations, domain.CaptureAnnotation{
		ID: "capture-note-2", Kind: domain.CaptureAnnotationNote, Text: "same capture note",
	})
	second, err := svc.Accept(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if second.IdempotentReplay || second.Attempt.AcceptedCaptureCount != 2 {
		t.Fatalf("second deliberate capture = %+v", second.Attempt)
	}
	replayedFirst, err := svc.Accept(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if !replayedFirst.IdempotentReplay || replayedFirst.Attempt.AcceptedCaptureCount != 1 || replayedFirst.Attempt.AcceptanceResultJSON != first.Attempt.AcceptanceResultJSON {
		t.Fatalf("old capture replay did not return its stored acceptance result: %+v", replayedFirst.Attempt)
	}
	if first.Fragment.ID != second.Fragment.ID || first.ObservedRevision.ID != second.ObservedRevision.ID {
		t.Fatalf("user context manufactured source identity/revision: first=%+v second=%+v", first.Attempt, second.Attempt)
	}
	assertTableCount(t, st.DB, "fragments", 1)
	assertTableCount(t, st.DB, "fragment_revisions", 1)
	assertTableCount(t, st.DB, "capture_attempts", 2)

	annotations, err := repo.ListAnnotations(ctx, first.Fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 4 {
		t.Fatalf("repeat annotation occurrences were not retained: %+v", annotations)
	}
	var highlights, notes int
	for _, annotation := range annotations {
		switch annotation.Kind {
		case domain.CaptureAnnotationHighlight:
			highlights++
			if annotation.Text != "same quote" || annotation.Selector == nil || annotation.Selector.Exact != "same quote" || annotation.Position == nil || annotation.Position.BlockAnchor != "paragraph-1" {
				t.Fatalf("highlight occurrence did not round trip: %+v", annotation)
			}
		case domain.CaptureAnnotationNote:
			notes++
			if annotation.Text != "same capture note" {
				t.Fatalf("capture note occurrence did not round trip: %+v", annotation)
			}
		}
	}
	if highlights != 2 || notes != 2 {
		t.Fatalf("expected two highlights and two capture notes, got %+v", annotations)
	}
	descriptions, err := repo.ListDescriptions(ctx, first.Fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptions) != 2 || descriptions[0].Value != descriptions[1].Value || descriptions[0].CaptureID == descriptions[1].CaptureID {
		t.Fatalf("description observation occurrences were not retained: %+v", descriptions)
	}
	tags, err := repo.ListTags(ctx, first.Fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].NormalizedValue != "reader" || tags[1].NormalizedValue != "research" {
		t.Fatalf("expected user tag set union [reader research], got %+v", tags)
	}
	entities, err := repository.NewEntityRepository(st.DB).ListByFragment(ctx, first.Fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tagEntities int
	for _, entity := range entities {
		if entity.Kind == "tag" {
			tagEntities++
		}
	}
	if tagEntities != 2 {
		t.Fatalf("compatibility tag projection duplicated captures: %+v", entities)
	}
}

func TestCaptureContextRetainsDistinctTagAttributionsAndUnionsRepeatAttribution(t *testing.T) {
	ctx := context.Background()
	st, svc, repo := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()

	firstReq := captureRequest(t, "capture-1", "annotation-1", "Research")
	first, err := svc.Accept(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	secondReq := captureRequest(t, "capture-2", "annotation-2", "research")
	secondReq.IdempotencyKey = "capture-2"
	secondReq.CapturedAt = captureTestTime.Add(time.Minute)
	secondReq.ActorID = "actor-2"
	if _, err := svc.Accept(ctx, secondReq); err != nil {
		t.Fatal(err)
	}
	thirdReq := captureRequest(t, "capture-3", "annotation-3", "RESEARCH")
	thirdReq.IdempotencyKey = "capture-3"
	thirdReq.CapturedAt = captureTestTime.Add(2 * time.Minute)
	thirdReq.ActorID = "actor-2"
	if _, err := svc.Accept(ctx, thirdReq); err != nil {
		t.Fatal(err)
	}
	tags, err := repo.ListTags(ctx, first.Fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].ActorID == tags[1].ActorID || tags[0].Source != domain.AttributionUser || tags[1].Source != domain.AttributionUser {
		t.Fatalf("tag attribution was not retained: %+v", tags)
	}
	entities, err := repository.NewEntityRepository(st.DB).ListByFragment(ctx, first.Fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	var matching int
	for _, entity := range entities {
		if entity.Kind == "tag" && (entity.Value == "Research" || entity.Value == "research") {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("expected one normalized compatibility entity, got %+v", entities)
	}
}

func TestCaptureAnnotationIDConflictIsGlobalAndRollsBackRevision(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()

	firstReq := captureRequest(t, "capture-1", "shared-annotation", "research")
	if _, err := svc.Accept(ctx, firstReq); err != nil {
		t.Fatal(err)
	}
	secondReq := captureRequest(t, "capture-2", "shared-annotation", "new-tag")
	secondReq.IdempotencyKey = "capture-2"
	secondReq.CapturedAt = captureTestTime.Add(time.Hour)
	secondReq.Fragment = captureFragment(t, "changed source material")
	_, err := svc.Accept(ctx, secondReq)
	assertCaptureConflict(t, err)

	assertTableCount(t, st.DB, "capture_attempts", 1)
	assertTableCount(t, st.DB, "fragment_revisions", 1)
	assertTableCount(t, st.DB, "fragment_tag_observations", 1)
}

func TestCaptureAcceptanceRollsBackFragmentAndContextAfterPostUpsertFailure(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()

	firstReq := captureRequest(t, "capture-1", "annotation-1", "research")
	firstReq.Descriptions[0].ID = "shared-description"
	if _, err := svc.Accept(ctx, firstReq); err != nil {
		t.Fatal(err)
	}
	secondReq := captureRequest(t, "capture-2", "annotation-2", "new-tag")
	secondReq.IdempotencyKey = "capture-2"
	secondReq.CapturedAt = captureTestTime.Add(time.Hour)
	secondReq.Fragment = captureFragment(t, "changed source material")
	secondReq.Descriptions[0].ID = "shared-description"
	if _, err := svc.Accept(ctx, secondReq); err == nil {
		t.Fatal("expected globally reused description occurrence ID to fail")
	}

	// The description conflict happens after the fragment/revision, attempt,
	// annotation, and tag inserts. One encompassing transaction must remove all
	// of those provisional writes.
	assertTableCount(t, st.DB, "fragments", 1)
	assertTableCount(t, st.DB, "fragment_revisions", 1)
	assertTableCount(t, st.DB, "capture_attempts", 1)
	assertTableCount(t, st.DB, "capture_annotations", 1)
	assertTableCount(t, st.DB, "fragment_tag_observations", 1)
	assertTableCount(t, st.DB, "fragment_description_observations", 1)
}

func TestCaptureAcceptanceConcurrentAcrossStoreHandles(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	firstStore, firstSvc, _ := openCaptureTestService(t, dbPath)
	defer firstStore.Close()
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondSvc := NewCaptureContextService(repository.NewCaptureRepository(secondStore.DB))

	req := captureRequest(t, "capture-1", "annotation-1", "research")
	var wg sync.WaitGroup
	results := make([]domain.CaptureAcceptance, 2)
	errs := make([]error, 2)
	services := []*CaptureContextService{firstSvc, secondSvc}
	for i := range services {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = services[index].Accept(ctx, req)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent accept: %v", err)
		}
	}
	if results[0].IdempotentReplay == results[1].IdempotentReplay {
		t.Fatalf("expected one insert and one replay: %+v", results)
	}
	if results[0].Attempt.ID != results[1].Attempt.ID || results[0].Attempt.AcceptanceResultJSON != results[1].Attempt.AcceptanceResultJSON {
		t.Fatalf("concurrent replay outcome changed: %+v", results)
	}
	assertTableCount(t, firstStore.DB, "fragments", 1)
	assertTableCount(t, firstStore.DB, "fragment_revisions", 1)
	assertTableCount(t, firstStore.DB, "capture_attempts", 1)
}

func TestDistinctCapturesConcurrentAcrossStoreHandlesIncrementCount(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	firstStore, firstSvc, _ := openCaptureTestService(t, dbPath)
	defer firstStore.Close()
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondSvc := NewCaptureContextService(repository.NewCaptureRepository(secondStore.DB))

	requests := []CaptureContextRequest{
		captureRequest(t, "capture-1", "annotation-1", "alpha"),
		captureRequest(t, "capture-2", "annotation-2", "beta"),
	}
	requests[1].IdempotencyKey = "capture-2"
	requests[1].CapturedAt = captureTestTime.Add(time.Minute)
	services := []*CaptureContextService{firstSvc, secondSvc}
	results := make([]domain.CaptureAcceptance, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range services {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = services[index].Accept(ctx, requests[index])
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent distinct capture: %v", err)
		}
	}
	if results[0].Fragment.ID != results[1].Fragment.ID || results[0].ObservedRevision.ID != results[1].ObservedRevision.ID {
		t.Fatalf("distinct captures diverged from stable item/revision: %+v", results)
	}
	counts := map[int]bool{
		results[0].Attempt.AcceptedCaptureCount: true,
		results[1].Attempt.AcceptedCaptureCount: true,
	}
	if !counts[1] || !counts[2] {
		t.Fatalf("acceptance counts did not serialize: %+v", results)
	}
	assertTableCount(t, firstStore.DB, "fragments", 1)
	assertTableCount(t, firstStore.DB, "fragment_revisions", 1)
	assertTableCount(t, firstStore.DB, "capture_attempts", 2)
	assertTableCount(t, firstStore.DB, "capture_annotations", 2)
	assertTableCount(t, firstStore.DB, "fragment_tag_observations", 2)
}

func TestCaptureCompletionTransitionsAreMonotonicAndTerminal(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()
	if _, err := svc.Accept(ctx, captureRequest(t, "capture-1", "annotation-1", "research")); err != nil {
		t.Fatal(err)
	}
	transferring, err := svc.AdvanceCompletion(ctx, "capture-1", domain.CaptureTransferring, `[{"message":"waiting","code":"asset.pending"}]`, captureTestTime.Add(time.Minute))
	if err != nil || transferring.Completion != domain.CaptureTransferring {
		t.Fatalf("advance transferring = %+v, %v", transferring, err)
	}
	repeated, err := svc.AdvanceCompletion(ctx, "capture-1", domain.CaptureTransferring, `[{"message":"ignored","code":"changed"}]`, captureTestTime.Add(2*time.Minute))
	if err != nil || repeated.WarningsJSON != transferring.WarningsJSON || !repeated.UpdatedAt.Equal(transferring.UpdatedAt) {
		t.Fatalf("same-state retry mutated attempt: %+v, %v", repeated, err)
	}
	complete, err := svc.AdvanceCompletion(ctx, "capture-1", domain.CapturePartial, `[]`, captureTestTime.Add(3*time.Minute))
	if err != nil || complete.Completion != domain.CapturePartial {
		t.Fatalf("advance partial = %+v, %v", complete, err)
	}
	_, err = svc.AdvanceCompletion(ctx, "capture-1", domain.CaptureComplete, `[]`, captureTestTime.Add(4*time.Minute))
	var conflict *repository.CaptureCompletionConflictError
	if !errors.As(err, &conflict) || conflict.Current != domain.CapturePartial {
		t.Fatalf("expected terminal-state conflict, got %T %v", err, err)
	}
	stored, err := repository.NewCaptureRepository(st.DB).GetAttempt(ctx, "capture-1")
	if err != nil || stored.Completion != domain.CapturePartial {
		t.Fatalf("terminal state regressed: %+v, %v", stored, err)
	}
}

func TestCuratedNoteOptimisticReplacementAcrossStoreHandles(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	firstStore, firstSvc, _ := openCaptureTestService(t, dbPath)
	defer firstStore.Close()
	accepted, err := firstSvc.Accept(ctx, captureRequest(t, "capture-1", "annotation-1", "research"))
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondSvc := NewCaptureContextService(repository.NewCaptureRepository(secondStore.DB))

	created, err := firstSvc.UpdateCuratedNote(ctx, accepted.Fragment.ID, 0, "# First", "actor-1", captureTestTime.Add(time.Minute))
	if err != nil || created.Revision != 1 {
		t.Fatalf("create note = %+v, %v", created, err)
	}
	type noteResult struct {
		note domain.CuratedNote
		err  error
	}
	start := make(chan struct{})
	results := make(chan noteResult, 2)
	for i, item := range []struct {
		svc  *CaptureContextService
		body string
	}{
		{firstSvc, "# Writer one"},
		{secondSvc, "# Writer two"},
	} {
		go func(index int, item struct {
			svc  *CaptureContextService
			body string
		}) {
			<-start
			note, err := item.svc.UpdateCuratedNote(ctx, accepted.Fragment.ID, 1, item.body, "actor", captureTestTime.Add(time.Duration(index+2)*time.Minute))
			results <- noteResult{note, err}
		}(i, item)
	}
	close(start)
	one := <-results
	two := <-results
	all := []noteResult{one, two}
	var successes int
	var winning domain.CuratedNote
	var conflict *repository.CuratedNoteConflictError
	for _, result := range all {
		if result.err == nil {
			successes++
			winning = result.note
			continue
		}
		if !errors.As(result.err, &conflict) {
			t.Fatalf("unexpected note update error: %T %v", result.err, result.err)
		}
	}
	if successes != 1 || winning.Revision != 2 || conflict == nil || conflict.Current.Revision != 2 || conflict.Current.BodyMarkdown != winning.BodyMarkdown {
		t.Fatalf("optimistic note results winner=%+v conflict=%+v all=%+v", winning, conflict, all)
	}
	stored, found, err := secondSvc.GetCuratedNote(ctx, accepted.Fragment.ID)
	if err != nil || !found || stored != winning {
		t.Fatalf("stored curated note = %+v found=%v err=%v, want %+v", stored, found, err, winning)
	}
	assertTableCount(t, firstStore.DB, "fragment_revisions", 1)
}

func TestCaptureUserStateDoesNotChangeMaterialDigest(t *testing.T) {
	ctx := context.Background()
	st, svc, repo := openCaptureTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	defer st.Close()
	first, err := svc.Accept(ctx, captureRequest(t, "capture-1", "annotation-1", "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateCuratedNote(ctx, first.Fragment.ID, 0, "personal note", "actor-1", captureTestTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	secondReq := captureRequest(t, "capture-2", "annotation-2", "beta")
	secondReq.IdempotencyKey = "capture-2"
	secondReq.CapturedAt = captureTestTime.Add(time.Hour)
	secondReq.Annotations[0].Text = "different personal highlight"
	secondReq.Descriptions = []domain.DescriptionObservation{{
		Value: "my personal description", Source: domain.AttributionUser,
	}}
	second, err := svc.Accept(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if second.ObservedRevision.MaterialDigest != first.ObservedRevision.MaterialDigest || second.ObservedRevision.ID != first.ObservedRevision.ID {
		t.Fatalf("user state changed material revision: first=%+v second=%+v", first.ObservedRevision, second.ObservedRevision)
	}
	revisions, err := repository.NewFragmentRepository(st.DB).ListRevisions(ctx, first.Fragment.ID)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("source revisions = %+v, %v", revisions, err)
	}
	tags, err := repo.ListTags(ctx, first.Fragment.ID)
	if err != nil || len(tags) != 2 {
		t.Fatalf("independent tag state = %+v, %v", tags, err)
	}
}

func openCaptureTestService(t *testing.T, dbPath string) (*store.Store, *CaptureContextService, *repository.CaptureRepository) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewCaptureRepository(st.DB)
	svc := NewCaptureContextService(repo)
	svc.now = func() time.Time { return captureTestTime }
	return st, svc, repo
}

func captureRequest(t *testing.T, captureID, annotationID, tag string) CaptureContextRequest {
	t.Helper()
	startOffset, endOffset := 5, 15
	return CaptureContextRequest{
		Fragment:       captureFragment(t, "source body"),
		CaptureID:      captureID,
		IdempotencyKey: captureID,
		PrincipalID:    "principal-1",
		ActorID:        "actor-1",
		Client:         domain.CaptureClient{Kind: "fe-clipper", Version: "1.2.3"},
		CapturedAt:     captureTestTime,
		SubmittedURL:   "https://example.com/article",
		PageContextJSON: `{
          "selection_present": true,
          "document_title": "Captured title"
        }`,
		ExtractionAdapter: domain.AdapterVersion{Adapter: "generic-web", Version: "2.0.0"},
		ExtractionJSON:    `{"observed_capabilities":["description","body"]}`,
		WarningsJSON:      `[]`,
		Completion:        domain.CaptureAccepting,
		Annotations: []domain.CaptureAnnotation{{
			ID: annotationID, Kind: domain.CaptureAnnotationHighlight,
			Text: "same quote", Selector: &domain.TextQuoteSelector{Exact: "same quote"},
			Position: &domain.DocumentPosition{
				BlockAnchor: "paragraph-1", StartOffset: &startOffset, EndOffset: &endOffset,
			},
		}},
		Tags: []domain.AttributedTag{{Value: tag}},
		Descriptions: []domain.DescriptionObservation{{
			Value: "source description", Source: domain.AttributionSourceMaterial,
		}},
	}
}

func captureFragment(t *testing.T, content string) domain.Fragment {
	t.Helper()
	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source: "browser", SourceType: "article", SourceID: "https://example.com/article",
		Title: "Captured title", Description: "source description", Content: content,
		ContentFormat: "markdown", CreatedAt: captureTestTime,
		SourceIdentity: domain.SourceIdentity{
			SourceRegistrationID: "browser:default", Provider: "web",
			SourceItemKey: "https://example.com/article", SegmentKey: "root",
			SubmittedURL: "https://example.com/article", CanonicalURL: "https://example.com/article",
			SourceAdapter: domain.AdapterVersion{Adapter: "generic-web", Version: "2.0.0"},
			Canonicalizer: domain.AdapterVersion{Adapter: "generic-web", Version: "2.0.0"},
		},
	}, "browser:default", captureTestTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return fragment
}

func assertTableCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertCaptureConflict(t *testing.T, err error) {
	t.Helper()
	var conflict *repository.CaptureConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected capture conflict, got %T %v", err, err)
	}
}
