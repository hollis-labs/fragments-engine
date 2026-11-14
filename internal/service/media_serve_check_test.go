package service

import (
	"bytes"
	"context"
	"errors"
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
		"SVG with no declared MIME":           {"", testSVGBytes()},
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
