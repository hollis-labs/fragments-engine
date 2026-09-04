package repository

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

var readerCommandTestTime = time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)

func TestReaderReadingPositionsValidateCurrentRevisionOwnershipAndStayIndependent(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "reader-positions", nil)
	defer st.Close()
	media := []domain.MediaManifestItem{
		mediaTestItem("youtube", "video-owned", "video", "https://example.test/video", domain.MediaVideo, domain.AttachmentPrimary, 0, readerTestVariants()),
		mediaTestItem("web", "image-owned", "image", "https://example.test/image", domain.MediaImage, domain.AttachmentGalleryItem, 1, readerTestVariants()),
		mediaTestItem("web", "document-owned", "document", "https://example.test/document", domain.MediaDocument, domain.AttachmentOther, 2, readerTestVariants()),
		mediaTestItem("web", "audio-owned", "audio", "https://example.test/audio", domain.MediaAudio, domain.AttachmentOther, 3, readerTestVariants()),
	}
	media[2].Asset.PageCount = 12
	resolved, err := NewMediaRepository(st.DB).UpsertManifest(context.Background(), fragment.Revision.ID, media, readerCommandTestTime)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewReaderCommandRepository(st.DB)
	positions := []domain.ReadingPosition{
		{Kind: domain.ReadingPositionNone},
		{Kind: domain.ReadingPositionArticle, Progress: floatPtr(0.3)},
		{Kind: domain.ReadingPositionVideo, ElapsedSeconds: floatPtr(10), DurationSeconds: floatPtr(60), ProviderMediaID: "video-owned"},
		{Kind: domain.ReadingPositionGallery, AttachmentID: resolved[1].Attachment.ID, Index: intPtr(1)},
		{Kind: domain.ReadingPositionDocument, Page: intPtr(4), Progress: floatPtr(0.25)},
		{Kind: domain.ReadingPositionAudio, ElapsedSeconds: floatPtr(5), DurationSeconds: floatPtr(50)},
	}
	for index, position := range positions {
		result, err := repo.ApplyLocal(context.Background(), readerWrite(fragment.ID,
			"position-"+string(rune('a'+index)), "set_reading_progress", index, position))
		if err != nil {
			t.Fatalf("position %s: %v", position.Kind, err)
		}
		if result.ReadingState.Position.Kind != position.Kind || result.ReadingState.Revision != index+1 {
			t.Fatalf("position %s result = %+v", position.Kind, result)
		}
	}
	stored, err := repo.GetReadingState(context.Background(), "local-user", fragment.ID)
	if err != nil || stored.Revision != len(positions) || stored.Position.Kind != domain.ReadingPositionAudio {
		t.Fatalf("stored reading state = %+v, %v", stored, err)
	}
	current, err := NewFragmentRepository(st.DB).GetByID(context.Background(), fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != fragment.Status || current.CurrentRevisionID != fragment.Revision.ID {
		t.Fatalf("reading changed processing/material state: before=%+v after=%+v", fragment, current)
	}
	for _, table := range []string{"route_log", "asset_acquisition_requests"} {
		var count int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("reading touched %s: count=%d err=%v", table, count, err)
		}
	}

	otherBuilt, err := BuildFragment(domain.PipelineFragment{Source: "url", SourceType: "video",
		SourceID: "https://example.com/reader-other", Title: "Other", Content: "Other body",
		CreatedAt: readerCommandTestTime}, "reader-test", readerCommandTestTime)
	if err != nil {
		t.Fatal(err)
	}
	otherFragment, _, err := NewFragmentRepository(st.DB).UpsertResolved(context.Background(), otherBuilt)
	if err != nil {
		t.Fatal(err)
	}
	otherMedia, err := NewMediaRepository(st.DB).UpsertManifest(context.Background(), otherFragment.Revision.ID,
		[]domain.MediaManifestItem{mediaTestItem("youtube", "other-video", "other-video", "https://example.test/other-video",
			domain.MediaVideo, domain.AttachmentGalleryItem, 0, readerTestVariants())}, readerCommandTestTime)
	if err != nil {
		t.Fatal(err)
	}
	// Real identifiers owned by another fragment/current revision are rejected.
	_, err = repo.ApplyLocal(context.Background(), readerWrite(fragment.ID, "bad-gallery", "set_reading_progress", len(positions), domain.ReadingPosition{
		Kind: domain.ReadingPositionGallery, AttachmentID: otherMedia[0].Attachment.ID, Index: intPtr(0),
	}))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-revision gallery position error = %T %v", err, err)
	}
	_, err = repo.ApplyLocal(context.Background(), readerWrite(fragment.ID, "bad-video", "set_reading_progress", len(positions), domain.ReadingPosition{
		Kind: domain.ReadingPositionVideo, ElapsedSeconds: floatPtr(1), ProviderMediaID: "other-video",
	}))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-fragment provider position error = %T %v", err, err)
	}
}

func TestReaderTagsReplayConflictStaleMergeSuppressionAndPrincipalScope(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reader-tags.db")
	firstStore, fragment := mediaTestStoreAndFragmentAt(t, dbPath, "reader-tags", nil)
	defer firstStore.Close()
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	repos := []*ReaderCommandRepository{NewReaderCommandRepository(firstStore.DB), NewReaderCommandRepository(secondStore.DB)}
	command := readerWrite(fragment.ID, "same-command", "add_tag", 0, domain.ReadingPosition{})
	command.Tag, command.NormalizedTag = "Research", "research"
	start := make(chan struct{})
	results := make(chan ReaderCommandResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := repos[index].ApplyLocal(context.Background(), command)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatal(err)
	}
	var replayCount int
	for result := range results {
		if result.IdempotentReplay {
			replayCount++
		}
	}
	if replayCount != 1 {
		t.Fatalf("same command replay count = %d", replayCount)
	}
	if revision, _ := repos[0].GetAggregateRevision(context.Background(), "local-user", fragment.ID); revision != 1 {
		t.Fatalf("aggregate after replay = %d", revision)
	}

	conflict := command
	conflict.Tag, conflict.NormalizedTag = "Different", "different"
	conflict.SemanticDigest = domain.DigestText("different semantics")
	if _, err := repos[0].ApplyLocal(context.Background(), conflict); err == nil {
		t.Fatal("changed same-key payload was accepted")
	} else {
		var typed *ReaderCommandConflictError
		if !errors.As(err, &typed) {
			t.Fatalf("changed key error = %T %v", err, err)
		}
	}

	stale := readerWrite(fragment.ID, "stale-add", "add_tag", 0, domain.ReadingPosition{})
	stale.Tag, stale.NormalizedTag = "Reader", "reader"
	if _, err := repos[1].ApplyLocal(context.Background(), stale); err != nil {
		t.Fatalf("stale commutative add: %v", err)
	}
	noop := readerWrite(fragment.ID, "noop-add", "add_tag", 0, domain.ReadingPosition{})
	noop.Tag, noop.NormalizedTag = "research", "research"
	noopResult, err := repos[0].ApplyLocal(context.Background(), noop)
	if err != nil || noopResult.Changed {
		t.Fatalf("semantic tag no-op = %+v, %v", noopResult, err)
	}
	if revision, _ := repos[0].GetAggregateRevision(context.Background(), "local-user", fragment.ID); revision != 2 {
		t.Fatalf("no-op changed aggregate to %d", revision)
	}
	remove := readerWrite(fragment.ID, "remove", "remove_tag", 2, domain.ReadingPosition{})
	remove.Tag, remove.NormalizedTag = "Research", "research"
	if _, err := repos[0].ApplyLocal(context.Background(), remove); err != nil {
		t.Fatal(err)
	}
	overlays, err := repos[0].ListTagOverlays(context.Background(), "local-user", fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlays) != 2 || !overlaySuppressed(overlays, "research") {
		t.Fatalf("effective overlays = %+v", overlays)
	}
	other, err := repos[0].ListTagOverlays(context.Background(), "other-user", fragment.ID)
	if err != nil || len(other) != 0 {
		t.Fatalf("suppression leaked principal: %+v, %v", other, err)
	}
	var observations int
	if err := firstStore.DB.QueryRow(`SELECT COUNT(*) FROM fragment_tag_observations WHERE fragment_id = ? AND normalized_value = 'research'`, fragment.ID).Scan(&observations); err != nil || observations != 1 {
		t.Fatalf("suppression deleted observations: count=%d err=%v", observations, err)
	}
}

func TestReaderCaptureNoteAndCuratedNoteOptimism(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "reader-notes", nil)
	defer st.Close()
	insertReaderCaptureAttempt(t, st.DB, fragment)
	repo := NewReaderCommandRepository(st.DB)
	appendWrite := readerWrite(fragment.ID, "append-note", "append_capture_note", 0, domain.ReadingPosition{})
	appendWrite.AnnotationID, appendWrite.AnnotationText = "annotation-reader", "Remember this"
	if _, err := repo.ApplyLocal(context.Background(), appendWrite); err != nil {
		t.Fatal(err)
	}
	var captureID string
	if err := st.DB.QueryRow(`SELECT capture_id FROM capture_annotations WHERE id = ?`, appendWrite.AnnotationID).Scan(&captureID); err != nil || captureID != "capture-reader" {
		t.Fatalf("annotation provenance capture=%q err=%v", captureID, err)
	}
	curated := readerWrite(fragment.ID, "curated-one", "update_curated_note", 0, domain.ReadingPosition{})
	curated.ExpectedNote, curated.BodyMarkdown = 0, "first"
	if _, err := repo.ApplyLocal(context.Background(), curated); err != nil {
		t.Fatal(err)
	}
	stale := readerWrite(fragment.ID, "curated-stale", "update_curated_note", 0, domain.ReadingPosition{})
	stale.ExpectedNote, stale.BodyMarkdown = 0, "lost"
	if _, err := repo.ApplyLocal(context.Background(), stale); err == nil {
		t.Fatal("stale curated replacement was accepted")
	} else {
		var conflict *CuratedNoteConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("curated error = %T %v", err, err)
		}
	}
	var noteCount int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM curated_notes WHERE fragment_id = ? AND body_markdown = 'first'`, fragment.ID).Scan(&noteCount); err != nil || noteCount != 1 {
		t.Fatalf("authoritative curated note lost: %d, %v", noteCount, err)
	}
}

func TestReaderReadingReplacementConflictsAcrossStoreHandles(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reading-conflict.db")
	firstStore, fragment := mediaTestStoreAndFragmentAt(t, dbPath, "reading-conflict", nil)
	defer firstStore.Close()
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	repos := []*ReaderCommandRepository{NewReaderCommandRepository(firstStore.DB), NewReaderCommandRepository(secondStore.DB)}
	initial := readerWrite(fragment.ID, "reading-initial", "set_reading_progress", 0, domain.ReadingPosition{Kind: domain.ReadingPositionArticle, Progress: floatPtr(.2)})
	if _, err := repos[0].ApplyLocal(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			command := readerWrite(fragment.ID, "reading-replace-"+string(rune('a'+index)), "mark_read", 1, domain.ReadingPosition{})
			_, err := repos[index].ApplyLocal(context.Background(), command)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		var conflict *ReaderRevisionConflictError
		if errors.As(err, &conflict) {
			conflicts++
			continue
		}
		t.Fatalf("replacement error = %T %v", err, err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("replacement results success=%d conflicts=%d", successes, conflicts)
	}
	state, err := repos[0].GetReadingState(context.Background(), "local-user", fragment.ID)
	if err != nil || state.State != domain.ReadingRead || state.Revision != 2 {
		t.Fatalf("authoritative reading state = %+v, %v", state, err)
	}
}

func TestReaderCaptureNoteRequiresExistingCaptureProvenance(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "reader-note-without-capture", nil)
	defer st.Close()
	write := readerWrite(fragment.ID, "orphan-note", "append_capture_note", 0, domain.ReadingPosition{})
	write.AnnotationID, write.AnnotationText = "orphan-annotation", "note"
	if _, err := NewReaderCommandRepository(st.DB).ApplyLocal(context.Background(), write); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("orphan capture note error = %T %v", err, err)
	}
	var receipts int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM reader_command_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("orphan capture note left receipt: count=%d err=%v", receipts, err)
	}
}

func readerWrite(fragmentID, id, command string, expected int, position domain.ReadingPosition) ReaderCommandWrite {
	return ReaderCommandWrite{CommandID: id, IdempotencyKey: "key-" + id,
		SemanticDigest: domain.DigestText("reader-test\n" + id), PrincipalID: "local-user",
		FragmentID: fragmentID, Command: command, ExpectedRevision: expected,
		Position: position, CreatedAt: readerCommandTestTime.Add(time.Duration(expected) * time.Minute)}
}

func insertReaderCaptureAttempt(t *testing.T, db *sql.DB, fragment domain.Fragment) {
	t.Helper()
	stamp := readerCommandTestTime.Format(time.RFC3339Nano)
	_, err := db.Exec(`
INSERT INTO capture_attempts (
  id, capture_id, idempotency_key, semantic_digest, fragment_id,
  fragment_revision_id, fragment_outcome, accepted_capture_count,
  principal_id, actor_id, client_kind, client_version, captured_at,
  created_at, updated_at
) VALUES ('attempt-reader', 'capture-reader', 'capture-key-reader', 'capture-digest',
          ?, ?, 'inserted', 1, 'local-user', 'local-user', 'test', '1', ?, ?, ?)`,
		fragment.ID, fragment.Revision.ID, stamp, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
}

func overlaySuppressed(items []domain.ReaderTagOverlay, value string) bool {
	for _, item := range items {
		if item.NormalizedValue == value {
			return item.Suppressed
		}
	}
	return false
}

func floatPtr(value float64) *float64 { return &value }
func intPtr(value int) *int           { return &value }

func readerTestVariants() []domain.AssetVariant {
	return []domain.AssetVariant{{VariantIdentity: "original", Kind: domain.VariantOriginal,
		Custody: domain.CustodyReference, AcquisitionState: domain.AcquisitionReferenceOnly}}
}
