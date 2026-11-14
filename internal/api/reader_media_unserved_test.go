package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
)

// Over HTTP: every variant a Reader item lists as available is served, and a
// stored file of a type FE does not serve is listed as failed, not available.
func TestReaderItemListsOnlyServableStoredVariantsAsAvailable(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json",
		readCaptureAPIFixture(t, "valid-capture-youtube.json"), "")
	if created.Code != http.StatusCreated {
		t.Fatalf("seed capture: %d %s", created.Code, created.Body.String())
	}
	var accepted capturecontract.CaptureManifestResponse
	if err := json.Unmarshal(created.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	upload := func(payload []byte) int {
		sum := sha256.Sum256(payload)
		return serveCaptureRequest(t, server, http.MethodPut,
			"/v1/captures/"+url.PathEscape(accepted.CaptureID)+"/assets/"+url.PathEscape("poster:maxresdefault")+"/content",
			"image/jpeg", payload, "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":").Code
	}
	poster := func() capturecontract.ReaderAssetVariant {
		t.Helper()
		detail := serveReaderRequest(server, http.MethodGet, "/v1/reader/items/"+url.PathEscape(accepted.FragmentID))
		assertCaptureAPIContract(t, capturecontract.SchemaReaderItem, detail.Body.Bytes())
		var item capturecontract.ReaderItem
		if err := json.Unmarshal(detail.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		for _, media := range item.Media {
			for _, variant := range media.Variants {
				if variant.Kind == "poster" {
					return variant
				}
			}
		}
		t.Fatal("item has no poster variant")
		return capturecontract.ReaderAssetVariant{}
	}

	// The client says JPEG but sends an SVG. The upload is accepted as bytes
	// in custody; the item does not offer them.
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`)
	if code := upload(svg); code != http.StatusNoContent {
		t.Fatalf("upload status = %d", code)
	}
	variant := poster()
	if variant.AcquisitionState != "failed" || variant.ContentHref != "" || variant.Failure == nil || variant.Failure.Code != "unsupported_media_type" {
		t.Fatalf("poster stored as SVG projects as %+v", variant)
	}
	refused := serveReaderRequest(server, http.MethodGet, "/v1/media/variants/"+url.PathEscape(variant.AssetVariantID)+
		"/content?fragment_id="+url.QueryEscape(accepted.FragmentID)+"&revision_id="+url.QueryEscape(accepted.FragmentRevisionID))
	if refused.Code != http.StatusConflict {
		t.Fatalf("media route for an unserved type = %d, want it still refused with 409", refused.Code)
	}
}

func TestReaderItemAvailableStoredVariantIsServed(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	ids := seedCollectionCaptures(t, server, 1)
	payload := testJPEGResourceBytes()
	sum := sha256.Sum256(payload)
	uploaded := serveCaptureRequest(t, server, http.MethodPut,
		"/v1/captures/collection-capture-collect0000/assets/"+url.PathEscape("poster:maxresdefault")+"/content",
		"image/jpeg", payload, "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":")
	if uploaded.Code != http.StatusNoContent {
		t.Fatalf("upload: %d %s", uploaded.Code, uploaded.Body.String())
	}
	detail := serveReaderRequest(server, http.MethodGet, "/v1/reader/items/"+url.PathEscape(ids[0]))
	var item capturecontract.ReaderItem
	if err := json.Unmarshal(detail.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	served := 0
	for _, media := range item.Media {
		for _, variant := range media.Variants {
			if variant.AcquisitionState != "available" {
				continue
			}
			response := serveReaderRequest(server, http.MethodGet, variant.ContentHref)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/jpeg" {
				t.Fatalf("available %s variant answered %d %q", variant.Kind, response.Code, response.Header().Get("Content-Type"))
			}
			served++
		}
	}
	if served != 1 {
		t.Fatalf("served %d available variants, want the uploaded poster", served)
	}
}
