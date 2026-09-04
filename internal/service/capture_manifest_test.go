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
	for table, want := range map[string]int{"fragments": 1, "fragment_revisions": 1, "capture_attempts": 1, "capture_annotations": 1, "attachment_refs": 1, "capture_asset_bindings": 2, "capture_followup_outbox": 1} {
		assertProtocolTableCount(t, st.DB, table, want)
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
	for table, wantCount := range map[string]int{"capture_attempts": 1, "capture_annotations": 1, "capture_asset_bindings": 2, "capture_followup_outbox": 1} {
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
	write.FollowUpKind, write.FollowUpPayloadJSON = "capture_enrichment", `{}`
	write.BuildAcceptanceSnapshot = func(domain.CaptureAcceptance) (string, error) { return "", errors.New("late projection failed") }
	if _, err := captureRepo.Accept(context.Background(), write); err == nil {
		t.Fatal("late projection failure unexpectedly committed")
	}
	for _, table := range []string{"fragments", "fragment_revisions", "capture_attempts", "attachment_refs", "media_assets", "asset_variants", "capture_asset_bindings", "capture_followup_outbox"} {
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
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
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
