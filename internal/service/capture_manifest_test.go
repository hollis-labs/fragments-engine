package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestCaptureManifestAcceptReplayConflictAndAtomicFollowUp(t *testing.T) {
	st, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	raw := captureFixture(t)
	first, err := svc.AcceptManifest(context.Background(), raw)
	if err != nil {
		t.Fatalf("accept manifest: %v", err)
	}
	if first.IdempotentReplay || first.FragmentID == "" || first.FragmentRevisionID == "" || first.CaptureAttemptID == "" {
		t.Fatalf("unexpected first response: %+v", first)
	}
	assertContractJSON(t, capturecontract.SchemaCaptureResponse, first)
	for table, want := range map[string]int{"fragments": 1, "fragment_revisions": 1, "capture_attempts": 1, "capture_annotations": 1, "attachment_refs": 1, "capture_asset_bindings": 2, "capture_followup_outbox": 1, "enrichment_observations": 6, "fragment_capability_coverage": 12, "inbox": 1} {
		assertProtocolTableCount(t, st.DB, table, want)
	}
	if first.ReaderItem.Operations.Triage.UnresolvedCount != 1 {
		t.Fatalf("optimistic Reader triage count = %d, want 1", first.ReaderItem.Operations.Triage.UnresolvedCount)
	}
	var inboxReason, inboxStagedAt string
	if err := st.DB.QueryRow(`SELECT reason, staged_at FROM inbox WHERE fragment_id = ?`, first.FragmentID).Scan(&inboxReason, &inboxStagedAt); err != nil {
		t.Fatalf("read accepted capture inbox row: %v", err)
	}
	if inboxReason != "awaiting routing" || inboxStagedAt != captureTestTime.Format(time.RFC3339Nano) {
		t.Fatalf("accepted capture inbox row = %q at %q", inboxReason, inboxStagedAt)
	}

	replayed, err := svc.AcceptManifest(context.Background(), raw)
	if err != nil {
		t.Fatalf("replay manifest: %v", err)
	}
	if !replayed.IdempotentReplay {
		t.Fatalf("replay flag = false")
	}
	want := first
	want.IdempotentReplay = true
	if !reflect.DeepEqual(replayed, want) {
		t.Fatalf("replay changed snapshot\n got=%+v\nwant=%+v", replayed, want)
	}
	for table, wantCount := range map[string]int{"capture_attempts": 1, "capture_annotations": 1, "capture_asset_bindings": 2, "capture_followup_outbox": 1, "enrichment_observations": 6, "fragment_capability_coverage": 12, "inbox": 1} {
		assertProtocolTableCount(t, st.DB, table, wantCount)
	}

	envelope := decodeEnvelope(t, raw)
	envelope.Document.Title = "changed with reused capture id"
	_, err = svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	var conflict *repository.CaptureConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("changed replay error = %T %v, want capture conflict", err, err)
	}
}

func TestCaptureManifestAnnotationConflictDoesNotAdvanceCaptureHistory(t *testing.T) {
	ctx := context.Background()
	st, svc, captureRepo := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	firstEnvelope := decodeEnvelope(t, captureFixture(t))
	first, err := svc.AcceptManifest(ctx, encodeEnvelope(t, firstEnvelope))
	if err != nil {
		t.Fatalf("accept initial manifest: %v", err)
	}
	if first.ReaderItem.CaptureCount != 1 {
		t.Fatalf("initial capture_count = %d, want 1", first.ReaderItem.CaptureCount)
	}

	tables := []string{
		"fragments", "fragment_source_identities", "fragment_revisions",
		"fragment_identity_aliases",
		"capture_attempts", "capture_annotations", "fragment_tag_observations",
		"fragment_description_observations", "media_assets", "asset_variants",
		"media_asset_identity_aliases", "media_blobs", "attachment_refs",
		"capture_asset_bindings", "capture_asset_outcomes", "capture_followup_outbox",
		"enrichment_observations", "fragment_capability_coverage",
	}
	wantRows := protocolTableRows(t, st.DB, tables)

	conflicting := decodeEnvelope(t, captureFixture(t))
	conflicting.CaptureID = "01KCAPTURE0000000000000001"
	conflicting.CapturedAt = firstEnvelope.CapturedAt.Add(time.Minute)
	if conflicting.PageContext == nil {
		conflicting.PageContext = &capturecontract.PageContext{}
	}
	conflicting.PageContext.DocumentTitle = "Conflicting recapture context"
	conflicting.Tags = append(conflicting.Tags, "must-roll-back")
	conflicting.Annotations[0].Text = "A conflicting reuse must not be accepted."
	conflicting.Annotations[0].Selector.Exact = conflicting.Annotations[0].Text
	_, err = svc.AcceptManifest(ctx, encodeEnvelope(t, conflicting))
	var conflict *repository.CaptureConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Reason, "annotation ID") {
		t.Fatalf("conflicting annotation error = %T %v, want annotation capture conflict", err, err)
	}

	gotRows := protocolTableRows(t, st.DB, tables)
	for _, table := range tables {
		if gotRows[table] != wantRows[table] {
			t.Fatalf("%s changed after rejected manifest\n got=%s\nwant=%s", table, gotRows[table], wantRows[table])
		}
	}
	count, err := captureRepo.CaptureCount(ctx, first.FragmentID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("capture history count after rejected manifest = %d, want 1", count)
	}
	attempts, err := captureRepo.ListAttempts(ctx, first.FragmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].CaptureID != first.CaptureID {
		t.Fatalf("capture history changed after rejection: %+v", attempts)
	}
	if _, err := captureRepo.GetAttempt(ctx, conflicting.CaptureID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected capture attempt lookup error = %v, want sql.ErrNoRows", err)
	}

	valid := decodeEnvelope(t, captureFixture(t))
	valid.CaptureID = conflicting.CaptureID
	valid.CapturedAt = conflicting.CapturedAt
	valid.PageContext = &capturecontract.PageContext{DocumentTitle: "Accepted recapture context"}
	valid.Tags = append(valid.Tags, "accepted-recapture")
	valid.Annotations[0].AnnotationID = "01KANNOTATION00000000000001"
	valid.Annotations[0].Text = "A fresh observation is additive."
	valid.Annotations[0].Selector.Exact = valid.Annotations[0].Text
	second, err := svc.AcceptManifest(ctx, encodeEnvelope(t, valid))
	if err != nil {
		t.Fatalf("accept deliberate recapture: %v", err)
	}
	if second.IdempotentReplay || second.ReaderItem.CaptureCount != 2 {
		t.Fatalf("deliberate recapture = %+v, want non-replay capture_count=2", second)
	}
	if second.FragmentID != first.FragmentID || second.FragmentRevisionID != first.FragmentRevisionID {
		t.Fatalf("recapture changed stable material identity: first=%+v second=%+v", first, second)
	}
	attempts, err = captureRepo.ListAttempts(ctx, first.FragmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("successful capture history = %+v, want two attempts", attempts)
	}
	annotations, err := captureRepo.ListAnnotations(ctx, first.FragmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 2 || annotations[0].ID == annotations[1].ID {
		t.Fatalf("successful recapture did not retain additive annotation context: %+v", annotations)
	}
	if !containsString(second.ReaderItem.Tags.Combined, "accepted-recapture") || containsString(second.ReaderItem.Tags.Combined, "must-roll-back") {
		t.Fatalf("successful recapture tags include rejected context or omit accepted context: %+v", second.ReaderItem.Tags.Combined)
	}
}

func TestCaptureManifestLateProjectionFailureRollsBackEverything(t *testing.T) {
	st, svc, captureRepo := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := decodeEnvelope(t, captureFixture(t))
	fragment, media, bindings, err := buildCaptureManifest(envelope, captureTestTime)
	if err != nil {
		t.Fatal(err)
	}
	contextSvc := NewCaptureContextService(captureRepo)
	contextSvc.now = func() time.Time { return captureTestTime }
	write, err := contextSvc.normalize(CaptureContextRequest{
		Fragment: fragment, CaptureID: envelope.CaptureID, IdempotencyKey: envelope.CaptureID,
		ProtocolPayloadDigest: domain.DigestText("fixture"), PrincipalID: envelope.PrincipalID,
		Client:     domain.CaptureClient{Kind: envelope.Client.Kind, Version: envelope.Client.Version},
		CapturedAt: envelope.CapturedAt, SubmittedURL: envelope.Source.SubmittedURL,
		ExtractionAdapter: domain.AdapterVersion{Adapter: envelope.Extraction.Adapter, Version: envelope.Extraction.AdapterVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	write.Media, write.AssetBindings = media, bindings
	write.Inbox = &repository.CaptureInboxWrite{Reason: "awaiting routing", StagedAt: captureTestTime}
	write.EnrichmentObservations, write.CapabilityCoverage = buildInitialCaptureEnrichment(envelope, fragment, media, captureTestTime)
	write.FollowUpKind, write.FollowUpPayloadJSON = "capture_enrichment", `{}`
	write.BuildAcceptanceSnapshot = func(domain.CaptureAcceptance) (string, error) { return "", errors.New("late projection failed") }
	if _, err := captureRepo.Accept(context.Background(), write); err == nil {
		t.Fatal("late projection failure unexpectedly committed")
	}
	for _, table := range []string{"fragments", "fragment_revisions", "capture_attempts", "attachment_refs", "media_assets", "asset_variants", "capture_asset_bindings", "capture_followup_outbox", "enrichment_observations", "fragment_capability_coverage", "inbox"} {
		assertProtocolTableCount(t, st.DB, table, 0)
	}
	_ = svc
}

func TestCaptureManifestConcurrentHandlesConverge(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	firstStore, firstSvc, _ := openManifestTestService(t, dbPath)
	secondStore, secondSvc, _ := openManifestTestService(t, dbPath)
	defer secondStore.Close()
	raw := captureFixture(t)
	services := []*CaptureService{firstSvc, secondSvc}
	results := make([]capturecontract.CaptureManifestResponse, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range services {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = services[i].AcceptManifest(context.Background(), raw)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent accept: %v", err)
		}
	}
	if results[0].FragmentID != results[1].FragmentID || results[0].FragmentRevisionID != results[1].FragmentRevisionID {
		t.Fatalf("concurrent handles diverged: %+v %+v", results[0], results[1])
	}
	assertProtocolTableCount(t, firstStore.DB, "capture_attempts", 1)
	assertProtocolTableCount(t, firstStore.DB, "capture_followup_outbox", 1)
	assertProtocolTableCount(t, firstStore.DB, "enrichment_observations", 6)
	assertProtocolTableCount(t, firstStore.DB, "fragment_capability_coverage", 12)
}

func TestCaptureManifestInitializesAllCapabilityCoverageFromTypedEvidence(t *testing.T) {
	st, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := decodeEnvelope(t, captureFixture(t))
	// Bare declarations cannot manufacture observations for missing values.
	envelope.Extraction.ObservedCapabilities = append(envelope.Extraction.ObservedCapabilities, "transcript", "entities")
	response, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	assertContractJSON(t, capturecontract.SchemaCaptureResponse, response)
	want := map[string]string{
		"title": "provided", "description": "provided", "body": "provided",
		"gallery_manifest": "not_applicable", "original_media": "provided",
		"thumbnail_or_poster": "provided", "transcript": "missing", "OCR": "missing",
		"vision": "missing", "summary": "missing", "tags": "provided", "entities": "missing",
	}
	if len(response.ReaderItem.Operations.Enrichment) != len(want) {
		t.Fatalf("coverage entries = %d, want %d", len(response.ReaderItem.Operations.Enrichment), len(want))
	}
	for _, item := range response.ReaderItem.Operations.Enrichment {
		if item.State != want[item.Capability] {
			t.Fatalf("%s state = %q, want %q", item.Capability, item.State, want[item.Capability])
		}
		if item.State == "provided" && item.ObservationID == "" {
			t.Fatalf("provided %s lacks supplying observation", item.Capability)
		}
	}
	// Media coverage means an observed logical reference exists. It remains
	// independent from reference-only/pending acquisition of its bytes.
	var originalState, posterState string
	if err := st.DB.QueryRow(`SELECT state FROM fragment_capability_coverage WHERE fragment_revision_id = ? AND capability = 'original_media'`, response.FragmentRevisionID).Scan(&originalState); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT state FROM fragment_capability_coverage WHERE fragment_revision_id = ? AND capability = 'thumbnail_or_poster'`, response.FragmentRevisionID).Scan(&posterState); err != nil {
		t.Fatal(err)
	}
	if originalState != "provided" || posterState != "provided" {
		t.Fatalf("logical media evidence coupled to acquisition: original=%s poster=%s", originalState, posterState)
	}
}

func TestCaptureCapabilityInitializationIsProviderAwareAndTranscriptTyped(t *testing.T) {
	t.Run("instagram single item may be incomplete carousel", func(t *testing.T) {
		_, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
		envelope := decodeEnvelope(t, captureFixture(t))
		envelope.CaptureID = "capture-instagram-one"
		envelope.Source.Provider = "instagram"
		envelope.Source.ProviderItemID = "post-1"
		envelope.Source.SourceItemKey = "instagram:post-1"
		envelope.Source.CanonicalURL = "https://www.instagram.com/p/post-1/"
		envelope.Source.SubmittedURL = envelope.Source.CanonicalURL
		envelope.Media[0].Kind = "image"
		response, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
		if err != nil {
			t.Fatal(err)
		}
		if state := readerCoverageState(response, "gallery_manifest"); state != "missing" {
			t.Fatalf("single Instagram gallery coverage = %q, want missing", state)
		}
	})

	t.Run("source transcript variant supplies typed reference observation", func(t *testing.T) {
		st, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
		envelope := decodeEnvelope(t, captureFixture(t))
		envelope.CaptureID = "capture-youtube-transcript"
		envelope.Media[0].Variants = append(envelope.Media[0].Variants, capturecontract.CaptureAssetVariant{
			ClientVariantID: "captions-en", Kind: "transcript", SourceURL: "https://captions.example/video.vtt",
			MIMEType: "text/vtt", TransferPreference: "reference_only", RequestedCustody: "reference",
		})
		response, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
		if err != nil {
			t.Fatal(err)
		}
		if state := readerCoverageState(response, "transcript"); state != "provided" {
			t.Fatalf("transcript state = %q", state)
		}
		var valueJSON string
		if err := st.DB.QueryRow(`SELECT value_json FROM enrichment_observations WHERE fragment_revision_id = ? AND capability = 'transcript'`, response.FragmentRevisionID).Scan(&valueJSON); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(valueJSON, `"kind":"variant_refs"`) || strings.Contains(strings.ToLower(valueJSON), "html") {
			t.Fatalf("unexpected transcript observation: %s", valueJSON)
		}
	})

	t.Run("plain article marks only impossible media analysis not applicable", func(t *testing.T) {
		_, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
		envelope := decodeEnvelope(t, captureFixture(t))
		envelope.CaptureID = "capture-plain-article"
		envelope.Source.Provider = "web"
		envelope.Source.ProviderItemID = ""
		envelope.Source.SourceItemKey = "https://example.test/article"
		envelope.Source.CanonicalURL = envelope.Source.SourceItemKey
		envelope.Source.SubmittedURL = envelope.Source.SourceItemKey
		envelope.Media = []capturecontract.CaptureMediaItem{}
		response, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
		if err != nil {
			t.Fatal(err)
		}
		for _, capability := range []string{"gallery_manifest", "original_media", "thumbnail_or_poster"} {
			if state := readerCoverageState(response, capability); state != "missing" {
				t.Fatalf("article %s = %q, want conservatively missing", capability, state)
			}
		}
		for _, capability := range []string{"transcript", "OCR", "vision"} {
			if state := readerCoverageState(response, capability); state != "not_applicable" {
				t.Fatalf("article %s = %q, want not_applicable", capability, state)
			}
		}
	})
}

func TestCaptureGalleryObservationUsesStableMediaIDsAfterLegacyIdentityResolution(t *testing.T) {
	st, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := decodeEnvelope(t, captureFixture(t))
	envelope.CaptureID = "capture-legacy-gallery"
	second := envelope.Media[0]
	second.Position = 1
	second.ClientMediaID = "youtube:second"
	second.ProviderMediaID = "second-video"
	second.SourceLocator = "https://www.youtube.com/watch?v=secondvideo"
	second.Variants = append([]capturecontract.CaptureAssetVariant(nil), envelope.Media[0].Variants...)
	for index := range second.Variants {
		second.Variants[index].ClientVariantID += ":second"
		second.Variants[index].SourceURL += "?item=second"
	}
	envelope.Media = append(envelope.Media, second)

	candidate, _, _, err := buildCaptureManifest(envelope, captureTestTime)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := candidate.ID
	const preservedLegacyID = "legacy-browser-fragment"
	if candidateID == preservedLegacyID {
		t.Fatal("fixture must exercise a speculative ID different from the preserved legacy ID")
	}
	candidate.ID = preservedLegacyID
	candidate.Revision.FragmentID = preservedLegacyID
	seeded, outcome, err := repository.NewFragmentRepository(st.DB).UpsertResolved(context.Background(), candidate)
	if err != nil || outcome != repository.UpsertInserted || seeded.ID != preservedLegacyID {
		t.Fatalf("seed preserved identity: fragment=%+v outcome=%s err=%v", seeded, outcome, err)
	}

	response, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	if response.FragmentID != preservedLegacyID {
		t.Fatalf("capture did not resolve preserved fragment ID: got %q", response.FragmentID)
	}
	var valueJSON string
	if err := st.DB.QueryRow(`SELECT value_json FROM enrichment_observations WHERE fragment_revision_id = ? AND capability = 'gallery_manifest'`, response.FragmentRevisionID).Scan(&valueJSON); err != nil {
		t.Fatal(err)
	}
	var gallery provider.ReferenceListValue
	if err := json.Unmarshal([]byte(valueJSON), &gallery); err != nil {
		t.Fatal(err)
	}
	if len(gallery.AttachmentIDs) != 0 || len(gallery.MediaAssetIDs) != 2 {
		t.Fatalf("gallery carried pre-resolution attachment identity: %s", valueJSON)
	}
	rows, err := st.DB.Query(`
SELECT m.id FROM attachment_refs a
JOIN media_assets m ON m.id = a.media_asset_id
WHERE a.fragment_revision_id = ? ORDER BY a.position`, response.FragmentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var persistedMediaIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		persistedMediaIDs = append(persistedMediaIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gallery.MediaAssetIDs, persistedMediaIDs) {
		t.Fatalf("gallery media IDs = %v, want persisted ordered IDs %v", gallery.MediaAssetIDs, persistedMediaIDs)
	}
}

func TestCaptureRecaptureAppendsSourceObservationsWithoutDuplicatingReplay(t *testing.T) {
	st, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := decodeEnvelope(t, captureFixture(t))
	first, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope)); err != nil {
		t.Fatal(err)
	}
	assertProtocolTableCount(t, st.DB, "enrichment_observations", 6)

	envelope.CaptureID = "01KCAPTURE0000000000000001"
	envelope.CapturedAt = envelope.CapturedAt.Add(time.Minute)
	envelope.Annotations = []capturecontract.CaptureAnnotation{}
	second, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	if second.FragmentRevisionID != first.FragmentRevisionID {
		t.Fatalf("exact source recapture created revision: %s != %s", second.FragmentRevisionID, first.FragmentRevisionID)
	}
	assertProtocolTableCount(t, st.DB, "enrichment_observations", 12)
	assertProtocolTableCount(t, st.DB, "fragment_capability_coverage", 12)
	var captures int
	if err := st.DB.QueryRow(`SELECT COUNT(DISTINCT capture_id) FROM enrichment_observations WHERE fragment_revision_id = ?`, first.FragmentRevisionID).Scan(&captures); err != nil {
		t.Fatal(err)
	}
	if captures != 2 {
		t.Fatalf("source observations lost recapture provenance: captures=%d", captures)
	}
}

func readerCoverageState(response capturecontract.CaptureManifestResponse, capability string) string {
	for _, item := range response.ReaderItem.Operations.Enrichment {
		if item.Capability == capability {
			return item.State
		}
	}
	return ""
}

func TestCaptureRevisionUsesLogicalMediaSemanticsNotRotatingURL(t *testing.T) {
	_, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	base := decodeEnvelope(t, captureFixture(t))
	base.Source.Provider = "web"
	base.Source.ProviderItemID = ""
	base.Source.SourceItemKey = "https://example.test/item"
	base.Source.CanonicalURL = "https://example.test/item"
	base.Source.SubmittedURL = "https://example.test/item"
	base.Media[0].ProviderMediaID = ""
	base.Media[0].SourceLocator = "https://cdn.example.test/image.jpg"
	base.Media[0].Kind = "image"
	base.Media[0].Variants = base.Media[0].Variants[1:]
	base.Media[0].Variants[0].Kind = "original"
	base.Media[0].Variants = append(base.Media[0].Variants, capturecontract.CaptureAssetVariant{
		ClientVariantID: "poster:preview", Kind: "preview",
		SourceURL: "https://cdn.example.test/image-preview.jpg", MIMEType: "image/jpeg",
		TransferPreference: "server_preferred", RequestedCustody: "cache",
	})
	base.Media[0].Role = "primary"
	originalCaption := base.Media[0].Caption
	base.CaptureID = "capture-media-1"
	first, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, base))
	if err != nil {
		t.Fatal(err)
	}
	rotated := base
	rotated.CaptureID = "capture-media-2"
	rotated.CapturedAt = rotated.CapturedAt.Add(time.Minute)
	rotated.Annotations = []capturecontract.CaptureAnnotation{}
	rotated.Media[0].Variants[0].SourceURL = "https://cdn.example.test/image.jpg?token=rotated"
	second, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, rotated))
	if err != nil {
		t.Fatal(err)
	}
	if second.FragmentRevisionID != first.FragmentRevisionID {
		t.Fatalf("rotating URL manufactured revision: %s != %s", second.FragmentRevisionID, first.FragmentRevisionID)
	}
	changed := rotated
	changed.CaptureID = "capture-media-3"
	changed.CapturedAt = changed.CapturedAt.Add(time.Minute)
	changed.Media[0].Caption = "source caption changed"
	third, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	if third.FragmentRevisionID == first.FragmentRevisionID {
		t.Fatal("source caption change did not create a revision")
	}

	reordered := changed
	reordered.CaptureID = "capture-media-4"
	reordered.CapturedAt = reordered.CapturedAt.Add(time.Minute)
	reordered.Media[0].Caption = originalCaption
	reordered.Media[0].Variants = append([]capturecontract.CaptureAssetVariant(nil), base.Media[0].Variants...)
	for left, right := 0, len(reordered.Media[0].Variants)-1; left < right; left, right = left+1, right-1 {
		reordered.Media[0].Variants[left], reordered.Media[0].Variants[right] = reordered.Media[0].Variants[right], reordered.Media[0].Variants[left]
	}
	fourth, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, reordered))
	if err != nil {
		t.Fatal(err)
	}
	if fourth.FragmentRevisionID != first.FragmentRevisionID {
		t.Fatalf("variant transport order manufactured revision: %s != %s", fourth.FragmentRevisionID, first.FragmentRevisionID)
	}

	renamed := reordered
	renamed.CaptureID = "capture-media-5"
	renamed.CapturedAt = renamed.CapturedAt.Add(time.Minute)
	renamed.Media = append([]capturecontract.CaptureMediaItem(nil), reordered.Media...)
	renamed.Media[0].ClientMediaID = "recapture-media-correlation"
	renamed.Media[0].Variants = append([]capturecontract.CaptureAssetVariant(nil), reordered.Media[0].Variants...)
	for index := range renamed.Media[0].Variants {
		renamed.Media[0].Variants[index].ClientVariantID += ":recapture"
	}
	fifth, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, renamed))
	if err != nil {
		t.Fatal(err)
	}
	if fifth.FragmentRevisionID != first.FragmentRevisionID {
		t.Fatalf("client correlation IDs manufactured revision: %s != %s", fifth.FragmentRevisionID, first.FragmentRevisionID)
	}
	firstVariants := make(map[string]struct{}, len(first.AssetInstructions))
	for _, instruction := range first.AssetInstructions {
		firstVariants[instruction.AssetVariantID] = struct{}{}
	}
	for _, instruction := range fifth.AssetInstructions {
		if _, reused := firstVariants[instruction.AssetVariantID]; !reused {
			t.Fatalf("client correlation ID manufactured asset variant %q", instruction.AssetVariantID)
		}
	}
}

func TestCaptureManifestRejectsPositionOnlyMediaIdentity(t *testing.T) {
	_, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := decodeEnvelope(t, captureFixture(t))
	envelope.Source.Provider = "web"
	envelope.Source.ProviderItemID = ""
	envelope.Media[0].ClientMediaID = "generic:image:0"
	envelope.Media[0].ProviderMediaID = ""
	envelope.Media[0].SourceLocator = ""
	for index := range envelope.Media[0].Variants {
		envelope.Media[0].Variants[index].SourceURL = ""
	}
	_, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if !IsInvalidCaptureRequest(err) || !strings.Contains(err.Error(), "stable observed source URL") {
		t.Fatalf("position-only media identity error = %T %v", err, err)
	}
}

func TestCaptureVariantIdentityUsesStableLocatorNotClientCorrelation(t *testing.T) {
	_, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := actionEnvelope(t)
	envelope.CaptureID = "capture-variant-logical-1"
	envelope.Annotations = []capturecontract.CaptureAnnotation{}
	envelope.Media[0].Variants = []capturecontract.CaptureAssetVariant{
		{ClientVariantID: "browser-a", Kind: "original", SourceURL: "https://cdn.example.test/a.jpg?token=first", MIMEType: "image/jpeg", TransferPreference: "browser_preferred", RequestedCustody: "mirror"},
		{ClientVariantID: "browser-b", Kind: "original", SourceURL: "https://cdn.example.test/b.jpg?token=first", MIMEType: "image/jpeg", TransferPreference: "browser_preferred", RequestedCustody: "mirror"},
	}
	first, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.AssetInstructions) != 2 || first.AssetInstructions[0].AssetVariantID == first.AssetInstructions[1].AssetVariantID {
		t.Fatalf("stable locators did not disambiguate same-shaped variants: %+v", first.AssetInstructions)
	}

	secondEnvelope := envelope
	secondEnvelope.CaptureID = "capture-variant-logical-2"
	secondEnvelope.CapturedAt = secondEnvelope.CapturedAt.Add(time.Minute)
	secondEnvelope.Media = append([]capturecontract.CaptureMediaItem(nil), envelope.Media...)
	secondEnvelope.Media[0].ClientMediaID = "recapture-media"
	secondEnvelope.Media[0].Variants = []capturecontract.CaptureAssetVariant{
		envelope.Media[0].Variants[1], envelope.Media[0].Variants[0],
	}
	secondEnvelope.Media[0].Variants[0].ClientVariantID = "recapture-b"
	secondEnvelope.Media[0].Variants[0].SourceURL = "https://cdn.example.test/b.jpg?token=rotated&expires=999"
	secondEnvelope.Media[0].Variants[1].ClientVariantID = "recapture-a"
	secondEnvelope.Media[0].Variants[1].SourceURL = "https://cdn.example.test/a.jpg?token=rotated&expires=999"
	second, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, secondEnvelope))
	if err != nil {
		t.Fatal(err)
	}
	if second.FragmentRevisionID != first.FragmentRevisionID {
		t.Fatalf("variant order/correlation/expiring URL manufactured revision: %s != %s", second.FragmentRevisionID, first.FragmentRevisionID)
	}
	variantIDs := make(map[string]struct{}, len(first.AssetInstructions))
	for _, instruction := range first.AssetInstructions {
		variantIDs[instruction.AssetVariantID] = struct{}{}
	}
	for _, instruction := range second.AssetInstructions {
		if _, reused := variantIDs[instruction.AssetVariantID]; !reused {
			t.Fatalf("recapture manufactured variant %q", instruction.AssetVariantID)
		}
	}

	ambiguous := secondEnvelope
	ambiguous.CaptureID = "capture-variant-logical-3"
	ambiguous.Media = append([]capturecontract.CaptureMediaItem(nil), secondEnvelope.Media...)
	ambiguous.Media[0].Variants = append([]capturecontract.CaptureAssetVariant(nil), secondEnvelope.Media[0].Variants...)
	ambiguous.Media[0].Variants[0].SourceURL = "https://cdn.example.test/same.jpg?token=one"
	ambiguous.Media[0].Variants[1].SourceURL = "https://cdn.example.test/same.jpg?token=two"
	if _, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, ambiguous)); !IsInvalidCaptureRequest(err) {
		t.Fatalf("ambiguous same-shaped variants used client IDs as identity: %T %v", err, err)
	}
}

func TestCaptureAssetActionsUploadReusePartialAndTerminalImmutability(t *testing.T) {
	root := t.TempDir()
	_, svc, _ := openManifestTestService(t, filepath.Join(root, "capture.db"))
	envelope := actionEnvelope(t)
	bytesA := []byte("asset-a")
	digestA := digestBytes(bytesA)
	envelope.Media[0].Variants[0].Digest = &capturecontract.Digest{Algorithm: "sha256", Value: digestA.Value}
	accepted, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, item := range accepted.AssetInstructions {
		actions[item.ClientVariantID] = item.Action
	}
	for id, want := range map[string]string{"upload": "request_upload", "server": "server_acquire", "reference": "reference_only", "rejected": "rejected"} {
		if actions[id] != want {
			t.Fatalf("action %s = %q, want %q; all=%v", id, actions[id], want, actions)
		}
	}
	before := countBlobFiles(t, root)
	if _, err := svc.StoreAssetContent(context.Background(), envelope.CaptureID, "upload", digestA, bytes.NewReader([]byte("wrong"))); !IsDigestMismatch(err) {
		t.Fatalf("digest mismatch = %T %v", err, err)
	}
	if got := countBlobFiles(t, root); got != before {
		t.Fatalf("digest mismatch left blob: %d -> %d", before, got)
	}
	if _, err := svc.StoreAssetContent(context.Background(), envelope.CaptureID, "upload", digestA, bytes.NewReader(bytesA)); err != nil {
		t.Fatalf("upload good sibling: %v", err)
	}
	if _, err := svc.StoreAssetContent(context.Background(), envelope.CaptureID, "server", digestA, bytes.NewReader(bytesA)); err == nil {
		t.Fatal("server-acquire variant accepted browser bytes")
	}

	completion := capturecontract.CaptureCompletionRequest{SchemaVersion: capturecontract.CaptureCompletionVersion,
		IdempotencyKey: "complete-actions", Warnings: []capturecontract.ContractWarning{}, Assets: []capturecontract.AssetOutcome{
			{ClientVariantID: "upload", Outcome: "uploaded", Digest: &capturecontract.Digest{Algorithm: "sha256", Value: digestA.Value}, ByteSize: int64Pointer(int64(len(bytesA)))},
			{ClientVariantID: "server", Outcome: "deferred"},
			{ClientVariantID: "reference", Outcome: "not_available", Reason: "reference only", Retryable: boolPointer(false)},
			{ClientVariantID: "rejected", Outcome: "not_available", Reason: "no URL", Retryable: boolPointer(false)},
		}}
	completionRaw, _ := json.Marshal(completion)
	status, err := svc.Complete(context.Background(), envelope.CaptureID, completionRaw)
	if err != nil {
		t.Fatal(err)
	}
	if status.Completion != "partial" {
		t.Fatalf("completion = %q, want partial", status.Completion)
	}
	replayed, err := svc.Complete(context.Background(), envelope.CaptureID, completionRaw)
	if err != nil || !reflect.DeepEqual(replayed, status) {
		t.Fatalf("completion replay changed: err=%v got=%+v want=%+v", err, replayed, status)
	}
	changed := completion
	changed.Assets[1].Outcome = "failed"
	changed.Assets[1].Reason = "changed"
	changedRaw, _ := json.Marshal(changed)
	if _, err := svc.Complete(context.Background(), envelope.CaptureID, changedRaw); err == nil {
		t.Fatal("completion key reuse with changed payload succeeded")
	}
	if _, err := svc.StoreAssetContent(context.Background(), envelope.CaptureID, "upload", digestA, bytes.NewReader(bytesA)); err == nil {
		t.Fatal("late upload changed terminal capture")
	}

	second := envelope
	second.CaptureID = "capture-actions-2"
	second.Annotations = []capturecontract.CaptureAnnotation{}
	second.CapturedAt = second.CapturedAt.Add(time.Minute)
	second.Source.SourceItemKey = "https://example.test/actions-2"
	second.Source.CanonicalURL = "https://example.test/actions-2"
	second.Source.SubmittedURL = "https://example.test/actions-2"
	second.Media[0].SourceLocator = "https://cdn.example.test/a-2.jpg"
	second.Media[0].Variants[0].ClientVariantID = "upload-2"
	accepted2, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, second))
	if err != nil {
		t.Fatal(err)
	}
	var reuse bool
	for _, item := range accepted2.AssetInstructions {
		if item.ClientVariantID == "upload-2" && item.Action == "reuse_blob" {
			reuse = true
		}
	}
	if !reuse {
		t.Fatalf("known digest was not reused: %+v", accepted2.AssetInstructions)
	}
}

func TestCaptureCompletionCannotManufactureAvailabilityAndReferenceDeferredIsComplete(t *testing.T) {
	_, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := actionEnvelope(t)
	envelope.Media[0].Variants = envelope.Media[0].Variants[2:3]
	envelope.Media[0].Variants[0].ClientVariantID = "reference-only"
	accepted, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil || len(accepted.AssetInstructions) != 1 {
		t.Fatalf("accept reference: %v %+v", err, accepted)
	}
	completion := capturecontract.CaptureCompletionRequest{SchemaVersion: capturecontract.CaptureCompletionVersion,
		IdempotencyKey: "reference-complete", Assets: []capturecontract.AssetOutcome{}, Warnings: []capturecontract.ContractWarning{}}
	raw, _ := json.Marshal(completion)
	status, err := svc.Complete(context.Background(), envelope.CaptureID, raw)
	if err != nil || status.Completion != "complete" {
		t.Fatalf("reference deferred completion = %+v err=%v", status, err)
	}

	other := actionEnvelope(t)
	other.CaptureID = "capture-unverified"
	other.Annotations = []capturecontract.CaptureAnnotation{}
	other.Source.SourceItemKey = "https://example.test/unverified"
	other.Source.SubmittedURL = "https://example.test/unverified"
	other.Source.CanonicalURL = "https://example.test/unverified"
	other.Media[0].Variants = other.Media[0].Variants[:1]
	other.Media[0].Variants[0].Digest = nil
	if _, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, other)); err != nil {
		t.Fatal(err)
	}
	claim := capturecontract.CaptureCompletionRequest{SchemaVersion: capturecontract.CaptureCompletionVersion,
		IdempotencyKey: "claim-unverified", Warnings: []capturecontract.ContractWarning{}, Assets: []capturecontract.AssetOutcome{
			{ClientVariantID: "upload", Outcome: "already_available"},
		}}
	claimRaw, _ := json.Marshal(claim)
	if _, err := svc.Complete(context.Background(), other.CaptureID, claimRaw); err == nil {
		t.Fatal("unverified client availability claim succeeded")
	}
}

func TestCaptureCompletionPreservesServerRecordedUploadWhenOmitted(t *testing.T) {
	_, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	envelope := actionEnvelope(t)
	envelope.CaptureID = "capture-upload-authority"
	envelope.Annotations = []capturecontract.CaptureAnnotation{}
	envelope.Source.SourceItemKey = "https://example.test/upload-authority"
	envelope.Source.SubmittedURL = envelope.Source.SourceItemKey
	envelope.Source.CanonicalURL = envelope.Source.SourceItemKey
	envelope.Media[0].SourceLocator = "https://cdn.example.test/upload-authority.jpg"
	envelope.Media[0].Variants = envelope.Media[0].Variants[:1]
	envelope.Media[0].Variants[0].SourceURL = envelope.Media[0].SourceLocator
	content := []byte("authoritative uploaded bytes")
	digest := digestBytes(content)
	envelope.Media[0].Variants[0].Digest = &capturecontract.Digest{Algorithm: "sha256", Value: digest.Value}
	if _, err := svc.AcceptManifest(context.Background(), encodeEnvelope(t, envelope)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StoreAssetContent(context.Background(), envelope.CaptureID, "upload", digest, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	completion := capturecontract.CaptureCompletionRequest{
		SchemaVersion:  capturecontract.CaptureCompletionVersion,
		IdempotencyKey: "complete-upload-authority",
		Assets:         []capturecontract.AssetOutcome{},
		Warnings:       []capturecontract.ContractWarning{},
	}
	raw, _ := json.Marshal(completion)
	status, err := svc.Complete(context.Background(), envelope.CaptureID, raw)
	if err != nil {
		t.Fatal(err)
	}
	if status.Completion != "complete" || len(status.Assets) != 1 || status.Assets[0].Outcome != "uploaded" {
		t.Fatalf("completion lost authoritative upload outcome: %+v", status)
	}
	if status.Assets[0].Digest == nil || status.Assets[0].Digest.Value != digest.Value || status.Assets[0].ByteSize == nil || *status.Assets[0].ByteSize != int64(len(content)) {
		t.Fatalf("completion changed authoritative upload proof: %+v", status.Assets[0])
	}
}

func openManifestTestService(t *testing.T, dbPath string) (*store.Store, *CaptureService, *repository.CaptureRepository) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	captureRepo := repository.NewCaptureRepository(st.DB)
	mediaRepo := repository.NewMediaRepository(st.DB)
	blobs, err := blobstore.NewFileStore(filepath.Join(filepath.Dir(dbPath), ".fragments-engine-blobs"))
	if err != nil {
		t.Fatal(err)
	}
	mediaSvc := NewMediaService(mediaRepo, blobs)
	mediaSvc.now = func() time.Time { return captureTestTime }
	svc := NewCaptureService(captureRepo, mediaSvc)
	svc.now = func() time.Time { return captureTestTime }
	return st, svc, captureRepo
}

func captureFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "browser-capture-reader", "v1", "fixtures", "valid-capture-youtube.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func actionEnvelope(t *testing.T) capturecontract.CaptureEnvelope {
	t.Helper()
	envelope := decodeEnvelope(t, captureFixture(t))
	envelope.CaptureID = "capture-actions"
	envelope.Source.Provider = "web"
	envelope.Source.ProviderItemID = ""
	envelope.Source.SourceItemKey = "https://example.test/actions"
	envelope.Source.SubmittedURL = "https://example.test/actions"
	envelope.Source.CanonicalURL = "https://example.test/actions"
	envelope.Media[0].ProviderMediaID = ""
	envelope.Media[0].SourceLocator = "https://cdn.example.test/a.jpg"
	envelope.Media[0].Kind = "image"
	envelope.Media[0].DefaultCustody = "mirror"
	envelope.Media[0].Variants = []capturecontract.CaptureAssetVariant{
		{ClientVariantID: "upload", Kind: "original", SourceURL: "https://cdn.example.test/a.jpg", MIMEType: "image/jpeg", TransferPreference: "browser_preferred", RequestedCustody: "mirror"},
		{ClientVariantID: "server", Kind: "preview", SourceURL: "https://cdn.example.test/a-preview.jpg", MIMEType: "image/jpeg", TransferPreference: "server_preferred", RequestedCustody: "cache"},
		{ClientVariantID: "reference", Kind: "thumbnail", SourceURL: "https://cdn.example.test/a-thumb.jpg", MIMEType: "image/jpeg", TransferPreference: "reference_only", RequestedCustody: "reference"},
		{ClientVariantID: "rejected", Kind: "poster", MIMEType: "image/jpeg", TransferPreference: "server_preferred", RequestedCustody: "mirror"},
	}
	return envelope
}

func decodeEnvelope(t *testing.T, raw []byte) capturecontract.CaptureEnvelope {
	t.Helper()
	value, err := capturecontract.DecodeCaptureEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func encodeEnvelope(t *testing.T, value capturecontract.CaptureEnvelope) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := capturecontract.ValidateJSON(capturecontract.SchemaCaptureEnvelope, raw); err != nil {
		t.Fatalf("invalid test envelope: %v\n%s", err, raw)
	}
	return raw
}

func assertContractJSON(t *testing.T, schema capturecontract.SchemaName, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := capturecontract.ValidateJSON(schema, raw); err != nil {
		t.Fatalf("response violates %s: %v\n%s", schema, err, raw)
	}
}

func assertProtocolTableCount(t *testing.T, db interface{ QueryRow(string, ...any) *sql.Row }, table string, want int) {
	t.Helper()
	got := protocolTableCount(t, db, table)
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func protocolTableCount(t *testing.T, db interface{ QueryRow(string, ...any) *sql.Row }, table string) int {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func protocolTableRows(t *testing.T, db *sql.DB, tables []string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string, len(tables))
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("snapshot %s columns: %v", table, err)
		}
		var records [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatalf("snapshot %s row: %v", table, err)
			}
			for i, value := range values {
				if raw, ok := value.([]byte); ok {
					values[i] = string(raw)
				}
			}
			records = append(records, values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("snapshot %s rows: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("snapshot %s close: %v", table, err)
		}
		raw, err := json.Marshal(records)
		if err != nil {
			t.Fatalf("snapshot %s encode: %v", table, err)
		}
		snapshot[table] = string(raw)
	}
	return snapshot
}

func digestBytes(value []byte) domain.ContentDigest {
	sum := sha256.Sum256(value)
	return domain.ContentDigest{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])}
}

func countBlobFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	_ = filepath.Walk(filepath.Join(root, ".fragments-engine-blobs"), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && !strings.Contains(path, string(filepath.Separator)+".tmp"+string(filepath.Separator)) {
			count++
		}
		return nil
	})
	return count
}

func int64Pointer(value int64) *int64 { return &value }
func boolPointer(value bool) *bool    { return &value }
