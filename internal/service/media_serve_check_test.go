package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func testSVGBytes() []byte {
	return []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="90" height="20"><rect width="90" height="20"/></svg>`)
}

func testJPEGBytes() []byte {
	return []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00\xff\xd9")
}

// mirroredImageFixture stores payload as the mirrored original of an image
// attached to a fresh fragment, declaring declaredMIME (which may be empty).
func mirroredImageFixture(t *testing.T, declaredMIME string, payload []byte) (*store.Store, *blobstore.FileStore, domain.Fragment, domain.AssetVariant) {
	t.Helper()
	st, blobs, fragment, variant := readerMediaFixture(t, domain.MediaImage, domain.AssetVariant{
		VariantIdentity: "original", Kind: domain.VariantOriginal, MIMEType: declaredMIME,
		Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending,
	})
	t.Cleanup(func() { st.Close() })
	stored, err := NewMediaService(repository.NewMediaRepository(st.DB), blobs).StoreVariantContent(
		context.Background(), variant.ID, domain.ContentDigest{}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	return st, blobs, fragment, stored
}

func readerImageVariant(t *testing.T, st *store.Store, fragment domain.Fragment) (capturecontract.ReaderItem, capturecontract.ReaderAssetVariant) {
	t.Helper()
	item, err := NewReaderService(repository.NewReaderRepository(st.DB)).Get(context.Background(),
		ReaderGetRequest{FragmentID: fragment.ID, PrincipalID: "reader-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Media) != 1 || len(item.Media[0].Variants) != 1 {
		t.Fatalf("item media = %+v", item.Media)
	}
	return item, item.Media[0].Variants[0]
}

// A mirrored original stored with no declared MIME gets the type its bytes
// have, is listed as available with that type, and is served.
func TestMirroredOriginalWithNoRecordedMIMEIsListedAndServed(t *testing.T) {
	st, blobs, fragment, stored := mirroredImageFixture(t, "", testPNGBytes())
	if stored.MIMEType != "image/png" || stored.Failure != nil || stored.AcquisitionState != domain.AcquisitionAvailable {
		t.Fatalf("stored variant = %+v", stored)
	}
	item, variant := readerImageVariant(t, st, fragment)
	if variant.AcquisitionState != "available" || variant.MIMEType != "image/png" || variant.ContentHref == "" || variant.Failure != nil {
		t.Fatalf("projected variant = %+v", variant)
	}
	if item.Display.ImageCount != 1 || item.Display.FeaturedMediaID == "" || item.Operations.Acquisition.Available != 1 {
		t.Fatalf("display = %+v acquisition = %+v", item.Display, item.Operations.Acquisition)
	}
	resource, err := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, t.TempDir()).
		OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, stored.ID)
	if err != nil {
		t.Fatalf("media route refused a variant the item lists as available: %v", err)
	}
	defer resource.Close()
	if resource.ContentType != "image/png" {
		t.Fatalf("served content type = %q", resource.ContentType)
	}
}

// A mirrored original of a type FE does not serve keeps its bytes but is not
// listed as available, has no content link, and is not counted as an image.
// The media route still refuses it: the closed MIME list is unchanged.
func TestMirroredOriginalOfAnUnservedTypeIsNotListedAsAvailable(t *testing.T) {
	for name, test := range map[string]struct {
		declared string
		payload  []byte
	}{
		"SVG with no declared MIME": {"", testSVGBytes()},
		// The shape found on the live FE: a README badge with no XML prolog.
		"badge SVG with no declared MIME":     {"", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="90" height="20" role="img"><title>downloads: 12k</title><rect width="90" height="20" fill="#555"/></svg>`)},
		"SVG declared as SVG":                 {"image/svg+xml", testSVGBytes()},
		"unrecognized bytes (as AVIF sniffs)": {"", []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf")},
		"JPEG bytes declared as PNG":          {"image/png", testJPEGBytes()},
	} {
		t.Run(name, func(t *testing.T) {
			st, blobs, fragment, stored := mirroredImageFixture(t, test.declared, test.payload)
			if stored.AcquisitionState != domain.AcquisitionAvailable || stored.BlobDigest == "" || !stored.Unserved() {
				t.Fatalf("stored variant = %+v, want bytes kept and marked unsupported", stored)
			}
			item, variant := readerImageVariant(t, st, fragment)
			if variant.AcquisitionState != "failed" || variant.ContentHref != "" || variant.Failure == nil ||
				variant.Failure.Code != domain.AssetFailureUnsupportedMediaType || variant.Failure.Retryable ||
				!strings.Contains(variant.Failure.Message, "the bytes look like ") {
				t.Fatalf("projected variant = %+v", variant)
			}
			if item.Display.ImageCount != 0 || item.Display.FeaturedMediaID != "" {
				t.Fatalf("an unserved image was counted or featured: %+v", item.Display)
			}
			if item.Operations.Acquisition.Available != 0 || item.Operations.Acquisition.Failed != 1 {
				t.Fatalf("acquisition summary = %+v", item.Operations.Acquisition)
			}
			_, err := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, t.TempDir()).
				OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, stored.ID)
			var unavailable *ReaderResourceUnavailableError
			if !errors.As(err, &unavailable) || unavailable.Reason != "unsafe_or_mismatched_mime" {
				t.Fatalf("media route error = %v, want it still refused", err)
			}
		})
	}
}

// Variants stored before the serve check existed are checked by the
// reconcile pass: a servable one gains its MIME, an unservable one stops
// being listed as available, and a second pass finds nothing to do.
func TestReconcileServeChecksFixesVariantsStoredBeforeTheCheck(t *testing.T) {
	servable, servableBlobs, servableFragment, servableVariant := mirroredImageFixture(t, "", testPNGBytes())
	unservable, unservableBlobs, unservableFragment, unservableVariant := mirroredImageFixture(t, "", testSVGBytes())
	// Put both rows back to how a row stored before the check looks.
	for _, target := range []struct {
		st *store.Store
		id string
	}{{servable, servableVariant.ID}, {unservable, unservableVariant.ID}} {
		mustExec(t, target.st.DB, `UPDATE asset_variants SET serve_checked = 0, mime_type = '', failure_code = '', failure_message = '' WHERE id = ?`, target.id)
	}
	if _, variant := readerImageVariant(t, unservable, unservableFragment); variant.AcquisitionState != "available" {
		t.Fatalf("fixture: unchecked SVG projects as %q, want the old behavior (available)", variant.AcquisitionState)
	}

	checked, unserved, err := NewMediaService(repository.NewMediaRepository(servable.DB), servableBlobs).ReconcileServeChecks(context.Background())
	if err != nil || checked != 1 || unserved != 0 {
		t.Fatalf("reconcile servable = %d checked, %d unserved, %v", checked, unserved, err)
	}
	if _, variant := readerImageVariant(t, servable, servableFragment); variant.AcquisitionState != "available" || variant.MIMEType != "image/png" {
		t.Fatalf("servable variant after reconcile = %+v", variant)
	}

	media := NewMediaService(repository.NewMediaRepository(unservable.DB), unservableBlobs)
	checked, unserved, err = media.ReconcileServeChecks(context.Background())
	if err != nil || checked != 1 || unserved != 1 {
		t.Fatalf("reconcile unservable = %d checked, %d unserved, %v", checked, unserved, err)
	}
	item, variant := readerImageVariant(t, unservable, unservableFragment)
	if variant.AcquisitionState != "failed" || variant.ContentHref != "" || item.Display.ImageCount != 0 {
		t.Fatalf("unservable variant after reconcile = %+v display = %+v", variant, item.Display)
	}
	if checked, _, err := media.ReconcileServeChecks(context.Background()); err != nil || checked != 0 {
		t.Fatalf("second reconcile pass = %d checked, %v", checked, err)
	}
}

func TestAssetAcquisitionPreservesUnservedMark(t *testing.T) {
	st, blobs, fragment, variant := mirroredImageFixture(t, "", testSVGBytes())
	repo := repository.NewMediaRepository(st.DB)
	svc := NewAssetAcquisitionService(repo)
	command := AssetAcquisitionCommand{CommandID: "keep-svg", IdempotencyKey: "keep-svg", MediaAssetID: variant.MediaAssetID, VariantKind: variant.Kind, RequestedCustody: domain.CustodyMirror, RequestedBy: "reader-a"}
	for i := 0; i < 2; i++ {
		result, err := svc.Request(context.Background(), command)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Variant.Unserved() || result.Variant.AcquisitionState != domain.AcquisitionAvailable {
			t.Fatalf("request erased check: %+v", result.Variant)
		}
	}
	if checked, _, err := NewMediaService(repo, blobs).ReconcileServeChecks(context.Background()); err != nil || checked != 0 {
		t.Fatalf("reconcile = %d, %v", checked, err)
	}
	item, projected := readerImageVariant(t, st, fragment)
	if projected.AcquisitionState != "failed" || projected.ContentHref != "" || item.Display.ImageCount != 0 {
		t.Fatalf("reacquisition offered SVG: %+v", item)
	}
}

func TestServeCheckDoesNotOverwriteNewerVariantState(t *testing.T) {
	for _, change := range []string{"checked", "mime", "state", "blob"} {
		t.Run(change, func(t *testing.T) {
			st, _, _, variant := mirroredImageFixture(t, "", testPNGBytes())
			repo := repository.NewMediaRepository(st.DB)
			mustExec(t, st.DB, `UPDATE asset_variants SET serve_checked=0 WHERE id=?`, variant.ID)
			switch change {
			case "checked":
				mustExec(t, st.DB, `UPDATE asset_variants SET serve_checked=1 WHERE id=?`, variant.ID)
			case "mime":
				mustExec(t, st.DB, `UPDATE asset_variants SET mime_type='image/jpeg' WHERE id=?`, variant.ID)
			case "state":
				mustExec(t, st.DB, `UPDATE asset_variants SET acquisition_state='failed', failure_code='client.failed' WHERE id=?`, variant.ID)
			case "blob":
				mustExec(t, st.DB, `UPDATE asset_variants SET blob_digest=NULL WHERE id=?`, variant.ID)
			}
			before, err := repo.GetVariant(context.Background(), variant.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.RecordServeCheck(context.Background(), variant, "", "stale rejection", variant.UpdatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("stale check overwrote %s: got %+v want %+v", change, got, before)
			}
		})
	}
}

func TestCaptureDedupChecksMediaBeforeReceiptAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name, declared string
		payload        []byte
		unserved       bool
	}{
		{"SVG", "", testSVGBytes(), true},
		{"PNG", "", testPNGBytes(), false},
		{"wrong declaration", "image/jpeg", testPNGBytes(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, svc, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
			envelope := actionEnvelope(t)
			envelope.Media[0].Variants = envelope.Media[0].Variants[:1]
			envelope.Media[0].Variants[0].MIMEType = ""
			digest := digestBytes(tc.payload)
			envelope.Media[0].Variants[0].Digest = &capturecontract.Digest{Algorithm: "sha256", Value: digest.Value}
			if _, err := svc.AcceptManifest(ctx, encodeEnvelope(t, envelope)); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.StoreAssetContent(ctx, envelope.CaptureID, "upload", digest, bytes.NewReader(tc.payload)); err != nil {
				t.Fatal(err)
			}
			envelope.CaptureID = "capture-dedup"
			envelope.Annotations = []capturecontract.CaptureAnnotation{}
			envelope.Source.SourceItemKey = "https://example.test/dedup"
			envelope.Source.CanonicalURL = envelope.Source.SourceItemKey
			envelope.Source.SubmittedURL = envelope.Source.SourceItemKey
			envelope.Media[0].SourceLocator = "https://cdn.example.test/dedup"
			envelope.Media[0].Variants[0].MIMEType = tc.declared
			raw := encodeEnvelope(t, envelope)
			receipt, err := svc.AcceptManifest(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.AssetInstructions[0].Action != "reuse_blob" {
				t.Fatalf("instructions = %+v", receipt.AssetInstructions)
			}
			variant := receipt.ReaderItem.Media[0].Variants[0]
			stored, err := repository.NewMediaRepository(st.DB).GetVariant(ctx, variant.AssetVariantID)
			if err != nil {
				t.Fatal(err)
			}
			var checked int
			if err := st.DB.QueryRow(`SELECT serve_checked FROM asset_variants WHERE id=?`, stored.ID).Scan(&checked); err != nil {
				t.Fatal(err)
			}
			if checked != 1 || stored.Unserved() != tc.unserved {
				t.Fatalf("dedup not checked before commit: %+v checked=%d", stored, checked)
			}
			if tc.unserved {
				if variant.AcquisitionState != "failed" || variant.ContentHref != "" || variant.Failure == nil || variant.Failure.Code != domain.AssetFailureUnsupportedMediaType || receipt.ReaderItem.Operations.Acquisition.Failed != 1 || receipt.ReaderItem.Display.ImageCount != 0 {
					t.Fatalf("optimistic receipt offered unsafe media: %+v", receipt.ReaderItem)
				}
			} else if variant.AcquisitionState != "available" || variant.ContentHref == "" || variant.MIMEType != "image/png" {
				t.Fatalf("receipt lost safe media: %+v", variant)
			}
			replay, err := svc.AcceptManifest(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			receipt.IdempotentReplay = true
			if !reflect.DeepEqual(receipt, replay) {
				t.Fatal("replay changed checked receipt")
			}
			resource, err := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), svc.media.blobs.(*blobstore.FileStore), t.TempDir()).OpenMedia(ctx, receipt.FragmentID, receipt.FragmentRevisionID, stored.ID)
			if err == nil {
				resource.Close()
			}
			if (!tc.unserved && err != nil) || tc.unserved != IsReaderResourceUnavailable(err) {
				t.Fatalf("media route disagrees: %v", err)
			}
		})
	}
}
