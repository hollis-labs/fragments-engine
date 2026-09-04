package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestReaderArticleHTTPNegotiatesSafeImmutableRepresentations(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	envelope, err := capturecontract.DecodeCaptureEnvelope(readCaptureAPIFixture(t, "valid-capture-youtube.json"))
	if err != nil {
		t.Fatal(err)
	}
	envelope.Document.Content.Body = "# Heading\n\n<script>alert(1)</script><iframe src=\"https://evil.example\"></iframe>\n[bad](javascript:alert(2))\n\nSafe text."
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", raw, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("seed capture: %d %s", created.Code, created.Body.String())
	}
	var accepted capturecontract.CaptureManifestResponse
	if err := json.Unmarshal(created.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	path := "/v1/reader/items/" + url.PathEscape(accepted.FragmentID) + "/content?revision_id=" + url.QueryEscape(accepted.FragmentRevisionID)

	htmlResponse := serveReaderRequest(server, http.MethodGet, path, map[string]string{"Accept": "text/html"})
	if htmlResponse.Code != http.StatusOK {
		t.Fatalf("HTML status = %d body=%s", htmlResponse.Code, htmlResponse.Body.String())
	}
	if got := htmlResponse.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("HTML content type = %q", got)
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Content-Digest", "ETag", "X-FE-Revision-ID"} {
		if htmlResponse.Header().Get(header) == "" {
			t.Fatalf("HTML response missing %s", header)
		}
	}
	if htmlResponse.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(htmlResponse.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("unsafe HTML headers: %v", htmlResponse.Header())
	}
	body := strings.ToLower(htmlResponse.Body.String())
	for _, forbidden := range []string{"<script", "<iframe", "javascript:", "evil.example"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("HTML retained %q: %s", forbidden, body)
		}
	}

	markdownResponse := serveReaderRequest(server, http.MethodGet, path+"&format=markdown", nil)
	if markdownResponse.Code != http.StatusOK || markdownResponse.Body.String() != envelope.Document.Content.Body {
		t.Fatalf("Markdown response = %d %q", markdownResponse.Code, markdownResponse.Body.String())
	}
	if got := markdownResponse.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/markdown") || markdownResponse.Header().Get("X-Content-Type-Options") != "nosniff" || markdownResponse.Header().Get("Content-Disposition") != "inline" {
		t.Fatalf("unsafe Markdown headers: %v", markdownResponse.Header())
	}
	if markdownResponse.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("Markdown response lacks CSP")
	}

	head := serveReaderRequest(server, http.MethodHead, path, map[string]string{"Accept": "text/markdown"})
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(envelope.Document.Content.Body)) {
		t.Fatalf("HEAD mismatch: status=%d len=%d headers=%v", head.Code, head.Body.Len(), head.Header())
	}
	unsupported := serveReaderRequest(server, http.MethodGet, path+"&format=xml", nil)
	if unsupported.Code != http.StatusBadRequest {
		t.Fatalf("unsupported format status = %d body=%s", unsupported.Code, unsupported.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, unsupported.Body.Bytes())
	emptyFormat := serveReaderRequest(server, http.MethodGet, path+"&format=", nil)
	if emptyFormat.Code != http.StatusBadRequest {
		t.Fatalf("empty format status = %d", emptyFormat.Code)
	}
	notAcceptable := serveReaderRequest(server, http.MethodGet, path, map[string]string{"Accept": "application/json"})
	if notAcceptable.Code != http.StatusNotAcceptable {
		t.Fatalf("unsupported Accept status = %d", notAcceptable.Code)
	}

	remote := httptest.NewRequest(http.MethodGet, path, nil)
	remote.RemoteAddr = "203.0.113.9:1234"
	remoteResponse := httptest.NewRecorder()
	server.ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("remote article status = %d", remoteResponse.Code)
	}
}

func TestReaderMediaHTTPEnforcesOwnershipRangeMIMEAndConditionalRequests(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", readCaptureAPIFixture(t, "valid-capture-youtube.json"), "")
	if created.Code != http.StatusCreated {
		t.Fatalf("seed capture: %d %s", created.Code, created.Body.String())
	}
	var accepted capturecontract.CaptureManifestResponse
	if err := json.Unmarshal(created.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	var posterID, originalID string
	for _, variant := range accepted.ReaderItem.Media[0].Variants {
		switch variant.Kind {
		case "poster":
			posterID = variant.AssetVariantID
		case "original":
			originalID = variant.AssetVariantID
		}
	}
	if posterID == "" || originalID == "" {
		t.Fatalf("fixture variants missing: %+v", accepted.ReaderItem.Media)
	}
	payload := testJPEGResourceBytes()
	sum := sha256.Sum256(payload)
	digestHeader := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	upload := serveCaptureRequest(t, server, http.MethodPut, "/v1/captures/"+accepted.CaptureID+"/assets/poster:maxresdefault/content", "image/jpeg", payload, digestHeader)
	if upload.Code != http.StatusNoContent {
		t.Fatalf("upload status = %d body=%s", upload.Code, upload.Body.String())
	}
	query := "?fragment_id=" + url.QueryEscape(accepted.FragmentID) + "&revision_id=" + url.QueryEscape(accepted.FragmentRevisionID)
	path := "/v1/media/variants/" + url.PathEscape(posterID) + "/content" + query

	full := serveReaderRequest(server, http.MethodGet, path, nil)
	if full.Code != http.StatusOK || !bytes.Equal(full.Body.Bytes(), payload) {
		t.Fatalf("media response = %d %x", full.Code, full.Body.Bytes())
	}
	if full.Header().Get("Content-Type") != "image/jpeg" || full.Header().Get("Content-Digest") != digestHeader || !strings.Contains(full.Header().Get("Cache-Control"), "immutable") || full.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("media headers = %v", full.Header())
	}
	etag := full.Header().Get("ETag")
	conditional := serveReaderRequest(server, http.MethodGet, path, map[string]string{"If-None-Match": etag})
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional response = %d %q", conditional.Code, conditional.Body.String())
	}
	ranged := serveReaderRequest(server, http.MethodGet, path, map[string]string{"Range": "bytes=0-3"})
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), payload[:4]) || ranged.Header().Get("Content-Range") == "" || ranged.Header().Get("Repr-Digest") == "" || ranged.Header().Get("Content-Digest") != "" {
		t.Fatalf("range response = %d %x headers=%v", ranged.Code, ranged.Body.Bytes(), ranged.Header())
	}
	for _, value := range []string{"bytes=0-1,3-4", "bytes=999-1000", "units=0-1", "bytes=-0", "bytes=abc-def", "bytes=" + strings.Repeat("1", 200) + "-"} {
		response := serveReaderRequest(server, http.MethodGet, path, map[string]string{"Range": value})
		if response.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("range %q status = %d body=%s", value, response.Code, response.Body.String())
		}
		assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, response.Body.Bytes())
	}
	head := serveReaderRequest(server, http.MethodHead, path, map[string]string{"Range": "bytes=1-2"})
	if head.Code != http.StatusPartialContent || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "2" {
		t.Fatalf("media HEAD = %d len=%d headers=%v", head.Code, head.Body.Len(), head.Header())
	}

	wrongRevision := serveReaderRequest(server, http.MethodGet, "/v1/media/variants/"+url.PathEscape(posterID)+"/content?fragment_id="+url.QueryEscape(accepted.FragmentID)+"&revision_id=missing-revision", nil)
	missingVariant := serveReaderRequest(server, http.MethodGet, "/v1/media/variants/missing/content"+query, nil)
	for name, response := range map[string]*httptest.ResponseRecorder{"wrong revision": wrongRevision, "missing variant": missingVariant} {
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body=%s", name, response.Code, response.Body.String())
		}
	}
	reference := serveReaderRequest(server, http.MethodGet, "/v1/media/variants/"+url.PathEscape(originalID)+"/content"+query, nil)
	if reference.Code != http.StatusConflict || strings.Contains(reference.Body.String(), "youtube.com") {
		t.Fatalf("reference-only response = %d %s", reference.Code, reference.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, reference.Body.Bytes())
}

func TestReaderResourcePathsRejectEncodedTraversalAndRequireContext(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	for _, path := range []string{
		"/v1/reader/items/%2e%2e%2fsecret/content?revision_id=revision",
		"/v1/media/variants/%2e%2e%2fsecret/content?fragment_id=fragment&revision_id=revision",
		"/v1/media/variants/variant/content?revision_id=revision",
		"/v1/media/variants/variant/content?fragment_id=fragment&fragment_id=other&revision_id=revision",
	} {
		response := serveReaderRequest(server, http.MethodGet, path, nil)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("unsafe path %q status = %d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestReaderLegacyMediaHTTPUsesNoStoreOnlyInsideConfiguredRoot(t *testing.T) {
	configPath := writeCaptureAPIConfig(t)
	server := NewServer(configPath).Handler()
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", readCaptureAPIFixture(t, "valid-capture-youtube.json"), "")
	var accepted capturecontract.CaptureManifestResponse
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &accepted) != nil {
		t.Fatalf("seed capture = %d %s", created.Code, created.Body.String())
	}
	var posterID string
	for _, variant := range accepted.ReaderItem.Media[0].Variants {
		if variant.Kind == "poster" {
			posterID = variant.AssetVariantID
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Reviewer.DownloadRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(cfg.Reviewer.DownloadRoot, "legacy-poster.jpg")
	payload := testJPEGResourceBytes()
	if err := os.WriteFile(legacyPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE asset_variants
SET legacy_storage_path = ?, source_path = '', custody = 'adopted', acquisition_state = 'available', retention = 'indefinite', byte_size = ?
WHERE id = ?`, legacyPath, len(payload), posterID); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	path := "/v1/media/variants/" + url.PathEscape(posterID) + "/content?fragment_id=" + url.QueryEscape(accepted.FragmentID) + "&revision_id=" + url.QueryEscape(accepted.FragmentRevisionID)
	response := serveReaderRequest(server, http.MethodGet, path, nil)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), payload) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("legacy response = %d %x headers=%v", response.Code, response.Body.Bytes(), response.Header())
	}

	outside := filepath.Join(t.TempDir(), "outside.jpg")
	if err := os.WriteFile(outside, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(legacyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, legacyPath); err != nil {
		t.Fatal(err)
	}
	blocked := serveReaderRequest(server, http.MethodGet, path, nil)
	if blocked.Code != http.StatusConflict || strings.Contains(blocked.Body.String(), outside) {
		t.Fatalf("legacy symlink response = %d %s", blocked.Code, blocked.Body.String())
	}
}

func serveReaderRequest(handler http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = "127.0.0.1:43210"
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func testJPEGResourceBytes() []byte {
	return []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00reader-jpeg\xff\xd9")
}
