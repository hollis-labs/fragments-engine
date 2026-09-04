package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
)

func TestCaptureHTTPManifestLookupUploadCompletionAndReplay(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	manifest := readCaptureAPIFixture(t, "valid-capture-youtube.json")

	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", manifest, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("manifest status = %d body=%s", created.Code, created.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaCaptureResponse, created.Body.Bytes())
	var accepted capturecontract.CaptureManifestResponse
	if err := json.Unmarshal(created.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.IdempotentReplay {
		t.Fatal("new manifest marked replay")
	}
	replay := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", manifest, "")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s", replay.Code, replay.Body.String())
	}
	var replayed capturecontract.CaptureManifestResponse
	_ = json.Unmarshal(replay.Body.Bytes(), &replayed)
	if !replayed.IdempotentReplay || replayed.FragmentID != accepted.FragmentID || replayed.ReaderItem.FragmentRevisionID != accepted.ReaderItem.FragmentRevisionID {
		t.Fatalf("replay changed acceptance: %+v", replayed)
	}

	lookup := serveCaptureRequest(t, server, http.MethodGet, "/v1/captures/"+accepted.CaptureID, "", nil, "")
	if lookup.Code != http.StatusOK {
		t.Fatalf("lookup status = %d body=%s", lookup.Code, lookup.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaCaptureStatus, lookup.Body.Bytes())

	content := []byte("poster bytes from browser")
	sum := sha256.Sum256(content)
	digestHeader := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	upload := serveCaptureRequest(t, server, http.MethodPut,
		"/v1/captures/"+accepted.CaptureID+"/assets/poster:maxresdefault/content",
		"image/jpeg", content, digestHeader)
	if upload.Code != http.StatusNoContent {
		t.Fatalf("upload status = %d body=%s", upload.Code, upload.Body.String())
	}
	// Exact transfer retry is idempotent and reuses the same content-addressed blob.
	uploadReplay := serveCaptureRequest(t, server, http.MethodPut,
		"/v1/captures/"+accepted.CaptureID+"/assets/poster:maxresdefault/content",
		"application/octet-stream", content, digestHeader)
	if uploadReplay.Code != http.StatusNoContent {
		t.Fatalf("upload replay status = %d body=%s", uploadReplay.Code, uploadReplay.Body.String())
	}

	digestHex := hex.EncodeToString(sum[:])
	byteSize := int64(len(content))
	retryable := false
	completion := capturecontract.CaptureCompletionRequest{SchemaVersion: capturecontract.CaptureCompletionVersion,
		IdempotencyKey: "http-completion", Warnings: []capturecontract.ContractWarning{}, Assets: []capturecontract.AssetOutcome{
			{ClientVariantID: "poster:maxresdefault", Outcome: "uploaded", Digest: &capturecontract.Digest{Algorithm: "sha256", Value: digestHex}, ByteSize: &byteSize},
			{ClientVariantID: "video:reference", Outcome: "not_available", Reason: "reference-only video was not transferred", Retryable: &retryable},
		}}
	completionRaw, _ := json.Marshal(completion)
	completed := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures/"+accepted.CaptureID+"/complete", "application/json", completionRaw, "")
	if completed.Code != http.StatusOK {
		t.Fatalf("completion status = %d body=%s", completed.Code, completed.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaCaptureStatus, completed.Body.Bytes())
	var completedStatus capturecontract.CaptureStatus
	_ = json.Unmarshal(completed.Body.Bytes(), &completedStatus)
	if completedStatus.Completion != "complete" {
		t.Fatalf("reference-only sibling made completion %q", completedStatus.Completion)
	}
	completedReplay := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures/"+accepted.CaptureID+"/complete", "application/json", completionRaw, "")
	if completedReplay.Body.String() != completed.Body.String() {
		t.Fatalf("completion replay changed snapshot\nfirst=%s\nreplay=%s", completed.Body.String(), completedReplay.Body.String())
	}
}

func TestCaptureHTTPProblemStatusesAndBodyLimits(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	manifest := readCaptureAPIFixture(t, "valid-capture-youtube.json")
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", manifest, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("seed capture: %d %s", created.Code, created.Body.String())
	}

	cases := []struct {
		name, method, path, contentType, digest string
		body                                    []byte
		want                                    int
	}{
		{"invalid manifest", http.MethodPost, "/v1/captures", "application/json", "", []byte(`{"schema_version":"wrong"}`), http.StatusBadRequest},
		{"missing manifest content type", http.MethodPost, "/v1/captures", "", "", manifest, http.StatusBadRequest},
		{"missing capture", http.MethodGet, "/v1/captures/missing", "", "", nil, http.StatusNotFound},
		{"invalid digest", http.MethodPut, "/v1/captures/01KCAPTURE0000000000000000/assets/poster:maxresdefault/content", "image/jpeg", "sha-256=:bad:", []byte("x"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := serveCaptureRequest(t, server, tc.method, tc.path, tc.contentType, tc.body, tc.digest)
			if response.Code != tc.want {
				t.Fatalf("status = %d want %d body=%s", response.Code, tc.want, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
				t.Fatalf("problem content type = %q", got)
			}
			assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, response.Body.Bytes())
		})
	}

	tooLarge := httptest.NewRequest(http.MethodPost, "/v1/captures", bytes.NewReader([]byte("{}")))
	tooLarge.Header.Set("Content-Type", "application/json")
	tooLarge.ContentLength = maxCaptureManifestBytes + 1
	tooLargeResponse := httptest.NewRecorder()
	server.ServeHTTP(tooLargeResponse, tooLarge)
	if tooLargeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("manifest limit status = %d body=%s", tooLargeResponse.Code, tooLargeResponse.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, tooLargeResponse.Body.Bytes())

	completionTooLarge := httptest.NewRequest(http.MethodPost, "/v1/captures/01KCAPTURE0000000000000000/complete", bytes.NewReader([]byte("{}")))
	completionTooLarge.Header.Set("Content-Type", "application/json")
	completionTooLarge.ContentLength = maxCaptureCompletionBytes + 1
	completionTooLargeResponse := httptest.NewRecorder()
	server.ServeHTTP(completionTooLargeResponse, completionTooLarge)
	if completionTooLargeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("completion limit status = %d body=%s", completionTooLargeResponse.Code, completionTooLargeResponse.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, completionTooLargeResponse.Body.Bytes())

	assetTooLarge := httptest.NewRequest(http.MethodPut, "/v1/captures/01KCAPTURE0000000000000000/assets/poster:maxresdefault/content", bytes.NewReader([]byte("x")))
	assetTooLarge.Header.Set("Content-Type", "image/jpeg")
	assetTooLarge.Header.Set("Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(make([]byte, 32))+":")
	assetTooLarge.ContentLength = maxCaptureAssetBytes + 1
	assetTooLargeResponse := httptest.NewRecorder()
	server.ServeHTTP(assetTooLargeResponse, assetTooLarge)
	if assetTooLargeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("asset limit status = %d body=%s", assetTooLargeResponse.Code, assetTooLargeResponse.Body.String())
	}
}

func TestCaptureHTTPDigestMismatchIsConflictAndLeavesCaptureUsable(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	manifest := readCaptureAPIFixture(t, "valid-capture-youtube.json")
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", manifest, "")
	var accepted capturecontract.CaptureManifestResponse
	_ = json.Unmarshal(created.Body.Bytes(), &accepted)
	expected := sha256.Sum256([]byte("expected"))
	response := serveCaptureRequest(t, server, http.MethodPut,
		"/v1/captures/"+accepted.CaptureID+"/assets/poster:maxresdefault/content",
		"image/jpeg", []byte("different"), "sha-256=:"+base64.StdEncoding.EncodeToString(expected[:])+":")
	if response.Code != http.StatusConflict {
		t.Fatalf("digest mismatch status = %d body=%s", response.Code, response.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, response.Body.Bytes())
	lookup := serveCaptureRequest(t, server, http.MethodGet, "/v1/captures/"+accepted.CaptureID, "", nil, "")
	if lookup.Code != http.StatusOK {
		t.Fatalf("capture invalid after media failure: %d %s", lookup.Code, lookup.Body.String())
	}
}

func serveCaptureRequest(t *testing.T, handler http.Handler, method, path, contentType string, body []byte, digest string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if digest != "" {
		request.Header.Set("Digest", digest)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func writeCaptureAPIConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "fragments.yaml")
	raw := []byte("database:\n  path: ./data/fragments.db\nrecall:\n  backend: sqlite\ningests:\n  - name: api-test\n    kind: filesystem_docs\n    enabled: false\n    source:\n      root: ./docs\n    routing:\n      namespace: tests\n    rules: {}\n    labels: {}\n")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func readCaptureAPIFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "browser-capture-reader", "v1", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertCaptureAPIContract(t *testing.T, schema capturecontract.SchemaName, raw []byte) {
	t.Helper()
	if err := capturecontract.ValidateJSON(schema, raw); err != nil {
		t.Fatalf("response violates %s: %v\n%s", schema, err, raw)
	}
}
