package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

func TestReaderServiceProjectsCompleteValidatedItemWithIndependentStates(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, t.TempDir()+"/reader.db")
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	seedReaderProjectionFacts(t, st.DB, accepted)
	reader := NewReaderService(repository.NewReaderRepository(st.DB))

	list, err := reader.List(context.Background(), ReaderListRequest{Scope: "inbox", PrincipalID: "reader-user"})
	if err != nil {
		t.Fatalf("list Reader items: %v", err)
	}
	assertContractJSON(t, capturecontract.SchemaReaderList, list)
	if len(list.Items) != 1 {
		t.Fatalf("Reader list items=%d", len(list.Items))
	}
	item := list.Items[0]
	assertContractJSON(t, capturecontract.SchemaReaderItem, item)
	if item.Display.Title.Value != "Contract fixture video" || item.Display.Title.ObservationID != "" {
		t.Fatalf("unsafe selected title did not fall back to immutable source: %+v", item.Display.Title)
	}
	if item.Display.Summary.Value != "Provider summary" || item.Display.Summary.Source != "provider" || item.Display.Summary.ObservationID != "reader-summary" {
		t.Fatalf("summary precedence/provenance = %+v", item.Display.Summary)
	}
	if item.Display.Description == nil || item.Display.Description.Source != "source" || item.Display.Description.ObservationID == "" {
		t.Fatalf("source description provenance = %+v", item.Display.Description)
	}
	if item.Renderer != "video" || item.Playback == nil || item.Playback.ProviderItemID != "3RmtNXqnreI" {
		t.Fatalf("renderer/playback = %q %+v", item.Renderer, item.Playback)
	}
	if len(item.Media) != 1 || len(item.Media[0].Variants) != 2 || item.Media[0].Attachment.Position != 0 {
		t.Fatalf("ordered media = %+v", item.Media)
	}
	if item.Media[0].Variants[0].Kind != "original" || item.Media[0].Variants[1].Kind != "poster" {
		t.Fatalf("canonical variant order = %+v", item.Media[0].Variants)
	}
	if item.Operations.Acquisition.ReferenceOnly != 1 || item.Operations.Acquisition.Failed != 1 || item.Operations.Acquisition.Pending != 0 {
		t.Fatalf("acquisition summary = %+v", item.Operations.Acquisition)
	}
	failedFound := false
	for _, variant := range item.Media[0].Variants {
		if variant.AcquisitionState == "failed" {
			failedFound = variant.Failure != nil && variant.Failure.Code == "poster_unavailable"
		}
	}
	if !failedFound {
		t.Fatalf("failed variant did not retain classified state: %+v", item.Media[0].Variants)
	}
	if !containsString(item.Tags.Combined, "provider-tag") || len(item.Annotations) != 1 || item.CuratedNote == nil {
		t.Fatalf("tags/annotations/note incomplete: tags=%+v annotations=%+v note=%+v", item.Tags, item.Annotations, item.CuratedNote)
	}
	if item.CaptureCount != 1 || item.ReadingState.PrincipalID != "reader-user" || item.ReadingState.State != "unread" || len(item.Actions) == 0 {
		t.Fatalf("capture/reading/actions = %d %+v %+v", item.CaptureCount, item.ReadingState, item.Actions)
	}
	if item.Operations.Triage.UnresolvedCount != 1 || item.Operations.Routing.State != "succeeded" ||
		item.Operations.Materialization.State != "failed" || len(item.Operations.Enrichment) != 12 {
		t.Fatalf("operational axes = %+v", item.Operations)
	}
	if len(item.Operations.Routing.References) != 1 || item.Operations.Routing.References[0] != "reader-route" {
		t.Fatalf("routing references = %+v", item.Operations.Routing.References)
	}
	var summaryCoverage *capturecontract.CapabilityCoverage
	for index := range item.Operations.Enrichment {
		if item.Operations.Enrichment[index].Capability == "summary" {
			summaryCoverage = &item.Operations.Enrichment[index]
		}
	}
	if summaryCoverage == nil || summaryCoverage.State != "stale" || summaryCoverage.ObservationID != "reader-summary" {
		t.Fatalf("stale coverage lost selected display observation: %+v", summaryCoverage)
	}
	if !item.Article.FullContentAvailable || !strings.Contains(item.Article.FullContentHref, accepted.FragmentRevisionID) {
		t.Fatalf("article content reference = %+v", item.Article)
	}
}

func TestReaderServiceRejectsCrossScopeAndMalformedCursors(t *testing.T) {
	encoded, err := encodeReaderCursor("all", time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), "fragment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeReaderCursor("all", encoded); err != nil {
		t.Fatalf("valid cursor: %v", err)
	}
	for _, test := range []struct {
		scope, cursor string
	}{
		{"library", encoded},
		{"all", encoded + "!"},
		{"all", "eyJ2IjoyfQ"},
	} {
		if _, err := decodeReaderCursor(test.scope, test.cursor); !IsInvalidReaderRequest(err) {
			t.Fatalf("decode %q/%q error=%v, want invalid Reader request", test.scope, test.cursor, err)
		}
	}
}

func TestReaderServiceOmitsInvalidYouTubePlayback(t *testing.T) {
	base := repository.ReaderBase{FragmentID: "fragment", CaptureCount: 1,
		FragmentRevision: testReaderRevision("revision"),
		Source:           repositoryReaderSource("not<script>"),
	}
	media := testReaderVideoMedia("revision")
	snapshot := repository.ReaderSnapshot{Bases: []repository.ReaderBase{base},
		Coverage: map[string][]repository.ReaderCoverage{"revision": testReaderCoverage("fragment", "revision")},
		Media:    map[string][]domain.MediaManifestItem{"revision": media}, Tags: map[string][]repository.ReaderTagFact{},
		Annotations: map[string][]domain.CaptureAnnotation{}, Effects: map[string][]repository.ReaderEffect{}}
	item, err := projectReaderItem(base, snapshot, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	if item.Playback != nil {
		t.Fatalf("invalid provider ID produced playback: %+v", item.Playback)
	}
}

func TestReaderRendererUsesProviderNeutralMediaCapabilityPriority(t *testing.T) {
	usable := []capturecontract.ReaderAssetVariant{{AssetVariantID: "variant", Kind: "original",
		Custody: "reference", AcquisitionState: "reference_only", SourceURL: "https://example.test/media"}}
	videoAndTranscript := []capturecontract.ReaderMediaItem{
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "video-ref", FragmentRevisionID: "revision", MediaAssetID: "video-asset", Role: "primary", Position: 0},
			MediaAssetID: "video-asset", Kind: "video", Variants: usable},
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "transcript-ref", FragmentRevisionID: "revision", MediaAssetID: "transcript-asset", Role: "transcript", Position: 1},
			MediaAssetID: "transcript-asset", Kind: "timed_text", Variants: usable},
	}
	if got := readerRenderer(testReaderRevision("revision"), videoAndTranscript); got != "video" {
		t.Fatalf("video + timed text renderer=%q, want video", got)
	}
	mixedCarousel := []capturecontract.ReaderMediaItem{
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "image-ref", FragmentRevisionID: "revision", MediaAssetID: "image-asset", Role: "gallery_item", Position: 0},
			MediaAssetID: "image-asset", Kind: "image", Variants: usable},
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "video-ref", FragmentRevisionID: "revision", MediaAssetID: "video-asset", Role: "gallery_item", Position: 1},
			MediaAssetID: "video-asset", Kind: "video", Variants: usable},
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "image-2-ref", FragmentRevisionID: "revision", MediaAssetID: "image-2-asset", Role: "gallery_item", Position: 2},
			MediaAssetID: "image-2-asset", Kind: "image", Variants: usable},
	}
	if got := readerRenderer(testReaderRevision("revision"), mixedCarousel); got != "gallery" {
		t.Fatalf("mixed image/video carousel renderer=%q, want gallery", got)
	}
	articleAndAuxiliary := []capturecontract.ReaderMediaItem{
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "transcript-ref", FragmentRevisionID: "revision", MediaAssetID: "transcript-asset", Role: "transcript", Position: 0},
			MediaAssetID: "transcript-asset", Kind: "timed_text", Variants: usable},
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "poster-ref", FragmentRevisionID: "revision", MediaAssetID: "poster-asset", Role: "poster", Position: 1},
			MediaAssetID: "poster-asset", Kind: "image", Variants: usable},
	}
	if got := readerRenderer(testReaderRevision("revision"), articleAndAuxiliary); got != "article" {
		t.Fatalf("article + auxiliary renderer=%q, want article", got)
	}
	failed := []capturecontract.ReaderAssetVariant{{AssetVariantID: "failed", Kind: "original",
		Custody: "mirror", AcquisitionState: "failed",
		Failure: &capturecontract.AssetFailure{Code: "failed", Message: "failed", Retryable: true}}}
	failedImages := []capturecontract.ReaderMediaItem{
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "image-0-ref", FragmentRevisionID: "revision", MediaAssetID: "image-0", Role: "gallery_item", Position: 0},
			MediaAssetID: "image-0", Kind: "image", Variants: failed},
		{Attachment: capturecontract.AttachmentRef{AttachmentID: "image-1-ref", FragmentRevisionID: "revision", MediaAssetID: "image-1", Role: "gallery_item", Position: 1},
			MediaAssetID: "image-1", Kind: "image", Variants: failed},
	}
	if got := readerRenderer(testReaderRevision("revision"), failedImages[:1]); got != "image" {
		t.Fatalf("all-failed sole image renderer=%q, want image", got)
	}
	if got := readerRenderer(testReaderRevision("revision"), failedImages); got != "gallery" {
		t.Fatalf("all-failed image gallery renderer=%q, want gallery", got)
	}
}

func TestReaderMediaProjectsAvailableLegacyContentAsAResourceReference(t *testing.T) {
	media, acquisition := projectReaderMedia("fragment", "revision", []domain.MediaManifestItem{{
		Asset: domain.MediaAsset{ID: "asset", Kind: domain.MediaDocument},
		Attachment: domain.AttachmentRef{ID: "attachment", FragmentRevisionID: "revision",
			MediaAssetID: "asset", Role: domain.AttachmentPrimary},
		Variants: []domain.AssetVariant{{ID: "legacy-variant", Kind: domain.VariantOriginal,
			Custody: domain.CustodyAdopted, AcquisitionState: domain.AcquisitionAvailable,
			SourcePath: "/private/source.pdf", LegacyStoragePath: "/private/stored.pdf", MIMEType: "application/pdf"}},
	}})
	if acquisition.Available != 1 || len(media) != 1 || len(media[0].Variants) != 1 || media[0].Variants[0].ContentHref == "" {
		t.Fatalf("legacy available media = %+v acquisition=%+v", media, acquisition)
	}
	if strings.Contains(media[0].Variants[0].ContentHref, "/private/") || media[0].Variants[0].SourceURL != "" {
		t.Fatalf("legacy filesystem path leaked through Reader: %+v", media[0].Variants[0])
	}
	if got, want := media[0].Variants[0].ContentHref,
		"/v1/media/variants/legacy-variant/content?fragment_id=fragment&revision_id=revision"; got != want {
		t.Fatalf("authorized media href = %q, want %q", got, want)
	}
}

func TestReaderServiceUsesAggregateRevisionZeroAndOpaquePagination(t *testing.T) {
	stub := &readerProjectionStub{snapshot: repository.ReaderSnapshot{
		Coverage: map[string][]repository.ReaderCoverage{}, Media: map[string][]domain.MediaManifestItem{},
		Tags: map[string][]repository.ReaderTagFact{}, Annotations: map[string][]domain.CaptureAnnotation{},
		Effects: map[string][]repository.ReaderEffect{},
	}}
	start := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	for index := 0; index < readerPageSize+1; index++ {
		fragmentID, revisionID := fmt.Sprintf("fragment-%02d", index), fmt.Sprintf("revision-%02d", index)
		base := repository.ReaderBase{FragmentID: fragmentID, CaptureCount: 1,
			SortAt: start.Add(-time.Duration(index) * time.Minute),
			FragmentRevision: domain.FragmentRevision{ID: revisionID, FragmentID: fragmentID,
				Ordinal: index + 10, Title: fragmentID, Content: "body", ContentFormat: "markdown", MetadataJSON: `{}`},
			Source: domain.SourceIdentity{SourceRegistrationID: "reader-test", Provider: "web",
				SourceItemKey: fragmentID, SegmentKey: "root"},
		}
		stub.snapshot.Bases = append(stub.snapshot.Bases, base)
		stub.snapshot.Coverage[revisionID] = testReaderCoverage(fragmentID, revisionID)
	}
	reader := NewReaderService(stub)
	page, err := reader.List(context.Background(), ReaderListRequest{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != readerPageSize || page.NextCursor == "" || len(stub.requests) != 1 || stub.requests[0].Limit != readerPageSize+1 {
		t.Fatalf("pagination result items=%d cursor=%q requests=%+v", len(page.Items), page.NextCursor, stub.requests)
	}
	if page.Items[0].Revision != 0 || page.Items[0].FragmentRevisionID != "revision-00" {
		t.Fatalf("aggregate/source revisions were conflated: %+v", page.Items[0])
	}
	cursor, err := decodeReaderCursor("all", page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.FragmentID != "fragment-49" || !cursor.SortAt.Equal(start.Add(-49*time.Minute)) {
		t.Fatalf("next cursor = %+v", cursor)
	}
}

func TestCaptureOptimisticReaderUsesAggregateRevisionAndValidatedPlayback(t *testing.T) {
	_, captureService, _ := openManifestTestService(t, t.TempDir()+"/reader-capture.db")
	envelope := decodeEnvelope(t, captureFixture(t))
	envelope.CaptureID = "reader-invalid-playback"
	envelope.Source.ProviderItemID = "bad-id"
	envelope.Source.SourceItemKey = "youtube:bad-id"
	envelope.Source.SubmittedURL = "https://www.youtube.com/watch?v=bad-id"
	envelope.Source.CanonicalURL = envelope.Source.SubmittedURL
	accepted, err := captureService.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ReaderItem.Revision != 0 || accepted.ReaderItem.Playback != nil {
		t.Fatalf("optimistic Reader revision/playback = %d %+v", accepted.ReaderItem.Revision, accepted.ReaderItem.Playback)
	}
	assertContractJSON(t, capturecontract.SchemaCaptureResponse, accepted)
}

func TestCaptureOptimisticReaderRequiresVideoRendererForProviderPlayback(t *testing.T) {
	_, captureService, _ := openManifestTestService(t, t.TempDir()+"/reader-no-video-playback.db")
	envelope := decodeEnvelope(t, captureFixture(t))
	envelope.CaptureID = "reader-valid-provider-without-video"
	envelope.Source.ProviderItemID = "3RmtNXqnreI"
	envelope.Source.SourceItemKey = "youtube:3RmtNXqnreI"
	envelope.Source.SubmittedURL = "https://www.youtube.com/watch?v=3RmtNXqnreI"
	envelope.Source.CanonicalURL = envelope.Source.SubmittedURL
	envelope.Media = []capturecontract.CaptureMediaItem{}
	accepted, err := captureService.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ReaderItem.Renderer == "video" || accepted.ReaderItem.Playback != nil {
		t.Fatalf("non-video Reader advertised provider playback: renderer=%q playback=%+v", accepted.ReaderItem.Renderer, accepted.ReaderItem.Playback)
	}
	assertContractJSON(t, capturecontract.SchemaCaptureResponse, accepted)
}

func TestReaderServiceHistoricalRevisionAndCanonicalAlias(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, t.TempDir()+"/reader-history.db")
	first, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, captureFixture(t))
	envelope.CaptureID = "reader-second-capture"
	envelope.CapturedAt = envelope.CapturedAt.Add(time.Minute)
	envelope.Document.Title = "Second source revision"
	envelope.Document.Content.Body = "Second body."
	envelope.Annotations[0].AnnotationID = "reader-second-annotation"
	second, err := captureService.AcceptManifest(context.Background(), encodeEnvelope(t, envelope))
	if err != nil {
		t.Fatal(err)
	}
	if first.FragmentID != second.FragmentID || first.FragmentRevisionID == second.FragmentRevisionID {
		t.Fatalf("revision setup first=%+v second=%+v", first, second)
	}
	const aliasID = "reader-history-alias"
	if _, err := st.DB.Exec(`INSERT INTO fragments (
id, source, source_type, source_id, title, content, content_hash, created_at,
ingested_at, status, metadata_json, ingest_name, canonical_path,
accepted_revision_id, current_revision_id
) SELECT ?, source, source_type, source_id || ':alias', title, content,
content_hash || ':alias', created_at, ingested_at, status, metadata_json,
ingest_name, canonical_path, '', '' FROM fragments WHERE id = ?`, aliasID, first.FragmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO fragment_identity_aliases (alias_fragment_id, fragment_id, reason, created_at)
VALUES (?, ?, 'reader test', '2026-09-03T12:00:00Z')`, aliasID, first.FragmentID); err != nil {
		t.Fatal(err)
	}
	reader := NewReaderService(repository.NewReaderRepository(st.DB))
	historical, err := reader.Get(context.Background(), ReaderGetRequest{FragmentID: aliasID, RevisionID: first.FragmentRevisionID})
	if err != nil {
		t.Fatal(err)
	}
	if historical.FragmentID != first.FragmentID || historical.FragmentRevisionID != first.FragmentRevisionID ||
		historical.Display.Title.Value != "Contract fixture video" || historical.Revision != 0 || historical.CaptureCount != 2 {
		t.Fatalf("historical alias projection = %+v", historical)
	}
	assertContractJSON(t, capturecontract.SchemaReaderItem, historical)
	current, err := reader.Get(context.Background(), ReaderGetRequest{FragmentID: first.FragmentID})
	if err != nil {
		t.Fatal(err)
	}
	if current.FragmentRevisionID != second.FragmentRevisionID || current.Display.Title.Value != "Second source revision" {
		t.Fatalf("current projection = %+v", current)
	}
}

func TestReaderCommandResultsHydrateProjectionAndRemainPrincipalScoped(t *testing.T) {
	st, fragment, _ := readerCommandServiceFixture(t)
	defer st.Close()
	reader := NewReaderService(repository.NewReaderRepository(st.DB))
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)

	added, err := commands.Execute(context.Background(), "reader-a", fragment.ID,
		readerContractCommand("projection-add", "add_tag", 0, func(command *capturecontract.ReaderCommand) {
			command.Tag = "Research"
		}))
	if err != nil {
		t.Fatal(err)
	}
	if added.Item.Revision != 1 || !containsString(added.Item.Tags.Combined, "Research") ||
		!containsReaderAction(added.Item.Actions, "append_capture_note") {
		t.Fatalf("add-tag projection = %+v", added.Item)
	}
	marked, err := commands.Execute(context.Background(), "reader-a", fragment.ID,
		readerContractCommand("projection-read", "mark_read", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if marked.Item.Revision != 2 || marked.Item.ReadingState.State != "read" ||
		marked.Item.ReadingState.Revision != 1 || containsReaderAction(marked.Item.Actions, "mark_read") ||
		!containsReaderAction(marked.Item.Actions, "mark_unread") {
		t.Fatalf("read-state projection = %+v", marked.Item)
	}

	other, err := reader.Get(context.Background(), ReaderGetRequest{FragmentID: fragment.ID, PrincipalID: "reader-b"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Revision != 0 || other.ReadingState.State != "unread" || containsString(other.Tags.Combined, "Research") ||
		len(other.Tags.Attributed) != 0 {
		t.Fatalf("other principal observed private command state: %+v", other)
	}

	removed, err := commands.Execute(context.Background(), "reader-a", fragment.ID,
		readerContractCommand("projection-remove", "remove_tag", 2, func(command *capturecontract.ReaderCommand) {
			command.Tag = "Research"
		}))
	if err != nil {
		t.Fatal(err)
	}
	if removed.Item.Revision != 3 || containsString(removed.Item.Tags.Combined, "Research") ||
		!containsAttributedReaderTag(removed.Item.Tags.Attributed, "Research", "projection-add") {
		t.Fatalf("tag suppression lost provenance or remained combined: %+v", removed.Item.Tags)
	}

	if _, err := st.DB.Exec(`DELETE FROM capture_attempts WHERE fragment_id = ?`, fragment.ID); err != nil {
		t.Fatal(err)
	}
	withoutCapture, err := reader.Get(context.Background(), ReaderGetRequest{FragmentID: fragment.ID, PrincipalID: "reader-b"})
	if err != nil {
		t.Fatal(err)
	}
	if containsReaderAction(withoutCapture.Actions, "append_capture_note") {
		t.Fatalf("append_capture_note advertised without a real capture attempt: %+v", withoutCapture.Actions)
	}
}

func TestReaderProjectionMergesPrincipalEffectsByCreationOrder(t *testing.T) {
	st, fragment, _ := readerCommandServiceFixture(t)
	defer st.Close()
	created := time.Date(2026, 9, 3, 20, 31, 0, 0, time.UTC)
	insertEffect := func(commandID, command, target, state string, at time.Time) {
		t.Helper()
		stamp := at.Format(time.RFC3339Nano)
		if _, err := st.DB.Exec(`INSERT INTO reader_command_receipts (
command_id, idempotency_key, semantic_digest, principal_id, fragment_id,
command, state, aggregate_revision, result_json, created_at, updated_at
) VALUES (?, ?, ?, 'reader-a', ?, ?, ?, 0, '{}', ?, ?)`, commandID, "key-"+commandID,
			strings.Repeat("a", 64), fragment.ID, command, state, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.Exec(`INSERT INTO reader_command_effects (
command_id, principal_id, fragment_id, kind, target_id, state, created_at, updated_at
) VALUES (?, 'reader-a', ?, ?, ?, ?, ?, ?)`, commandID, fragment.ID, command, target, state, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	insertEffect("reader-route-effect", "route", "route-reader", "succeeded", created)
	insertEffect("reader-materialize-effect", "materialize", "destination-reader", "uncertain", created)
	later := created.Add(time.Minute).Format(time.RFC3339Nano)
	if _, err := st.DB.Exec(`INSERT INTO route_log (
fragment_id, route_id, destination_id, decision, reason, created_at
) VALUES (?, 'route-reader', 'destination-reader', 'inbox', 'delivery_queued_by_design:{}', ?)`, fragment.ID, later); err != nil {
		t.Fatal(err)
	}
	reader := NewReaderService(repository.NewReaderRepository(st.DB))
	item, err := reader.Get(context.Background(), ReaderGetRequest{FragmentID: fragment.ID, PrincipalID: "reader-a"})
	if err != nil {
		t.Fatal(err)
	}
	if item.Operations.Routing.State != "pending" || item.Operations.Materialization.State != "partial" {
		t.Fatalf("merged command/route effects = %+v", item.Operations)
	}
	other, err := reader.Get(context.Background(), ReaderGetRequest{FragmentID: fragment.ID, PrincipalID: "reader-b"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Operations.Materialization.State != "none" {
		t.Fatalf("other principal observed command effect: %+v", other.Operations.Materialization)
	}
}

func containsReaderAction(actions []capturecontract.CommandCapability, command string) bool {
	for _, action := range actions {
		if action.Command == command && action.ExpectedRevisionRequired && strings.Contains(action.InputSchema, "reader-command.schema.json#/$defs/") {
			return true
		}
	}
	return false
}

func containsAttributedReaderTag(tags []capturecontract.AttributedTag, value, observationID string) bool {
	for _, tag := range tags {
		if tag.Value == value && tag.Source == "user" && tag.ObservationID == observationID {
			return true
		}
	}
	return false
}

func seedReaderProjectionFacts(t *testing.T, db *sql.DB, accepted capturecontract.CaptureManifestResponse) {
	t.Helper()
	const now = "2026-09-03T12:00:00Z"
	var materialDigest string
	if err := db.QueryRow(`SELECT material_digest FROM fragment_revisions WHERE id = ?`, accepted.FragmentRevisionID).Scan(&materialDigest); err != nil {
		t.Fatal(err)
	}
	insertObservation := func(id, capability, attribution, value string) {
		if _, err := db.Exec(`INSERT INTO enrichment_observations (
id, fragment_id, fragment_revision_id, capability, attribution_source,
producer, producer_version, input_material_digest, value_json,
observed_at, asserted_at, created_at
) VALUES (?, ?, ?, ?, ?, 'reader-test', '1.0.0', ?, ?, ?, ?, ?)`, id,
			accepted.FragmentID, accepted.FragmentRevisionID, capability, attribution,
			materialDigest, value, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	insertObservation("reader-summary", "summary", "provider", `{"text":"Provider summary"}`)
	insertObservation("reader-bad-title", "title", "source", `{"text":"<script>alert(1)</script>"}`)
	insertObservation("reader-later-provider-title", "title", "provider", `{"text":"Later provider title"}`)
	insertObservation("reader-provider-tags", "tags", "provider", `{"values":["provider-tag","video"]}`)
	if _, err := db.Exec(`UPDATE fragment_capability_coverage
SET state = 'stale', selected_observation_id = 'reader-summary', detail = 'refresh pending'
WHERE fragment_revision_id = ? AND capability = 'summary'`, accepted.FragmentRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE fragment_capability_coverage
SET state = 'pending', selected_observation_id = 'reader-bad-title', detail = 'unsafe selected test'
WHERE fragment_revision_id = ? AND capability = 'title'`, accepted.FragmentRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE asset_variants SET acquisition_state = 'failed',
failure_code = 'poster_unavailable', failure_message = 'poster could not be acquired', failure_retryable = 1
WHERE media_asset_id IN (SELECT media_asset_id FROM attachment_refs WHERE fragment_revision_id = ?)
AND kind = 'poster'`, accepted.FragmentRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inbox (fragment_id, reason, staged_at) VALUES (?, 'reader test', ?)`, accepted.FragmentID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO curated_notes (fragment_id, body_markdown, revision, actor_id, updated_at)
VALUES (?, 'Remember this.', 1, 'reader-user', ?)`, accepted.FragmentID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO destinations (id, name, kind, config_json, created_at, updated_at)
VALUES ('reader-destination', 'Reader Destination', 'file', '{}', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO routes (id, name, destination_id, created_at, updated_at)
VALUES ('reader-route', 'Reader Route', 'reader-destination', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ decision, reason string }{
		{"inbox", `delivery_queued_by_design:{"destination_kind":"file"}`},
		{"queued_route", `queued_delivery_success:{"attempts":1,"ref":"ignored-path"}`},
		{"materialize", `materialize_error:{"attempts":1,"error":"write failed"}`},
	} {
		if _, err := db.Exec(`INSERT INTO route_log (fragment_id, route_id, destination_id, decision, reason, created_at)
VALUES (?, 'reader-route', 'reader-destination', ?, ?, ?)`, accepted.FragmentID, entry.decision, entry.reason, now); err != nil {
			t.Fatal(err)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func repositoryReaderSource(providerItemID string) domain.SourceIdentity {
	return domain.SourceIdentity{SourceRegistrationID: "browser", Provider: "youtube",
		ProviderItemID: providerItemID, SourceItemKey: "youtube:" + providerItemID,
		SegmentKey: "root", Canonicalizer: domain.AdapterVersion{Adapter: "youtube", Version: "1.0.0"}}
}

func testReaderRevision(id string) domain.FragmentRevision {
	return domain.FragmentRevision{ID: id, FragmentID: "fragment", Ordinal: 1,
		Title: "video", Content: "body", ContentFormat: "markdown", MetadataJSON: `{}`}
}

func testReaderVideoMedia(revisionID string) []domain.MediaManifestItem {
	return []domain.MediaManifestItem{{
		Asset: domain.MediaAsset{ID: "asset", Kind: domain.MediaVideo},
		Attachment: domain.AttachmentRef{ID: "attachment", FragmentRevisionID: revisionID,
			MediaAssetID: "asset", Role: domain.AttachmentPrimary, Position: 0},
		Variants: []domain.AssetVariant{{ID: "variant", MediaAssetID: "asset", Kind: domain.VariantOriginal,
			SourceURL: "https://example.test/video.mp4", Custody: domain.CustodyReference,
			AcquisitionState: domain.AcquisitionReferenceOnly}},
	}}
}

func testReaderCoverage(fragmentID, revisionID string) []repository.ReaderCoverage {
	out := make([]repository.ReaderCoverage, 0, len(domain.AllEnrichmentCapabilities()))
	for _, capability := range domain.AllEnrichmentCapabilities() {
		out = append(out, repository.ReaderCoverage{Coverage: domain.CapabilityCoverage{
			FragmentID: fragmentID, FragmentRevisionID: revisionID, Capability: capability,
			State: domain.CoverageMissing,
		}})
	}
	return out
}

type readerProjectionStub struct {
	snapshot repository.ReaderSnapshot
	requests []repository.ReaderPageRequest
}

func (s *readerProjectionStub) List(_ context.Context, request repository.ReaderPageRequest) (repository.ReaderSnapshot, error) {
	s.requests = append(s.requests, request)
	return s.snapshot, nil
}

func (s *readerProjectionStub) Get(context.Context, string, string, string) (repository.ReaderSnapshot, error) {
	return repository.ReaderSnapshot{}, sql.ErrNoRows
}
