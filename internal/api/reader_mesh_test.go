package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
)

const meshRemotePeer = "203.0.113.9:4242"

func meshRequest(h http.Handler, peer, method, path, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://fragments.example"+path, bytes.NewReader(body))
	r.RemoteAddr = peer
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A remote peer with the right scope reaches every Reader route and the media
// route. Wrong scope or no credential is refused, and the administrative
// routes keep their independent loopback restriction.
func TestReaderAndMediaRoutesOverTheMeshAreGatedByScope(t *testing.T) {
	i := newTestIssuer(t)
	s, _ := issuerServer(t, i)
	h := s.Handler()
	tok := func(scope string) string { return i.token(t, map[string]any{"scope": scope}) }

	// Seed from loopback with a capture-scoped token: one item with a poster.
	capture := tok("fragments-engine.capture")
	envelope, err := capturecontract.DecodeCaptureEnvelope(readCaptureAPIFixture(t, "valid-capture-youtube.json"))
	if err != nil {
		t.Fatal(err)
	}
	envelope.PrincipalID = "svc:basalisk"
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	created := meshCaptureRequest(h, "POST", "/v1/captures", capture, raw)
	if created.Code != http.StatusCreated {
		t.Fatalf("seed capture: %d %s", created.Code, created.Body.String())
	}
	var accepted capturecontract.CaptureManifestResponse
	if err := json.Unmarshal(created.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	var posterID string
	for _, variant := range accepted.ReaderItem.Media[0].Variants {
		if variant.Kind == "poster" {
			posterID = variant.AssetVariantID
		}
	}
	payload := testJPEGResourceBytes()
	sum := sha256.Sum256(payload)
	upload := httptest.NewRequest("PUT", "/v1/captures/"+accepted.CaptureID+"/assets/poster:maxresdefault/content", bytes.NewReader(payload))
	upload.RemoteAddr = "127.0.0.1:1234"
	upload.Header.Set("Authorization", "Bearer "+capture)
	upload.Header.Set("Content-Type", "image/jpeg")
	upload.Header.Set("Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":")
	uploaded := httptest.NewRecorder()
	h.ServeHTTP(uploaded, upload)
	if uploaded.Code != http.StatusNoContent {
		t.Fatalf("seed upload: %d %s", uploaded.Code, uploaded.Body.String())
	}

	id := url.PathEscape(accepted.FragmentID)
	mediaQuery := "?fragment_id=" + url.QueryEscape(accepted.FragmentID) + "&revision_id=" + url.QueryEscape(accepted.FragmentRevisionID)
	gets := map[string]string{
		"list":    "/v1/reader/items?scope=all",
		"item":    "/v1/reader/items/" + id,
		"content": "/v1/reader/items/" + id + "/content?revision_id=" + url.QueryEscape(accepted.FragmentRevisionID),
		"media":   "/v1/media/variants/" + url.PathEscape(posterID) + "/content" + mediaQuery,
	}
	command := []byte(`{"schema_version":"fe.reader.command.v1","command":"mark_read","command_id":"mesh-read","idempotency_key":"mesh-read-key","expected_revision":0}`)
	commandPath := "/v1/reader/items/" + id + "/commands"

	read := tok("fragments-engine.read")
	for name, path := range gets {
		if r := meshRequest(h, meshRemotePeer, "GET", path, read, nil, nil); r.Code != http.StatusOK {
			t.Errorf("remote read-scope GET %s: %d %s", name, r.Code, truncate(r.Body.String()))
		}
	}
	for name, token := range map[string]string{
		"capture-only": capture,
		"admin-only":   tok("fragments-engine.admin"),
		"write-only":   tok("fragments-engine.write"),
		"no scope":     tok(""),
	} {
		for route, path := range gets {
			if r := meshRequest(h, meshRemotePeer, "GET", path, token, nil, nil); r.Code != http.StatusForbidden {
				t.Errorf("remote %s GET %s: got %d, want 403", name, route, r.Code)
			}
		}
	}
	for route, path := range gets {
		for _, token := range []string{"", "wrong"} {
			if r := meshRequest(h, meshRemotePeer, "GET", path, token, nil, nil); r.Code != http.StatusUnauthorized {
				t.Errorf("remote token=%q GET %s: got %d, want 401", token, route, r.Code)
			}
		}
	}

	// Commands need the write scope from a remote address.
	for name, token := range map[string]string{
		"read-only":    read,
		"capture-only": capture,
		"admin-only":   tok("fragments-engine.admin"),
	} {
		if r := meshRequest(h, meshRemotePeer, "POST", commandPath, token, command, nil); r.Code != http.StatusForbidden {
			t.Errorf("remote %s POST command: got %d, want 403", name, r.Code)
		}
	}
	if r := meshRequest(h, meshRemotePeer, "POST", commandPath, "", command, nil); r.Code != http.StatusUnauthorized {
		t.Errorf("remote unauthenticated POST command: got %d, want 401", r.Code)
	}
	written := meshRequest(h, meshRemotePeer, "POST", commandPath, tok("fragments-engine.write"), command, nil)
	if written.Code != http.StatusOK {
		t.Fatalf("remote write-scope command: %d %s", written.Code, written.Body.String())
	}
	// The refused attempts above must not have changed anything.
	item := meshRequest(h, meshRemotePeer, "GET", gets["item"], read, nil, nil)
	if !strings.Contains(item.Body.String(), `"state":"read"`) {
		t.Fatalf("command from write token did not apply: %s", truncate(item.Body.String()))
	}

	// The media route still pins fragment and revision ownership remotely.
	wrong := "/v1/media/variants/" + url.PathEscape(posterID) + "/content?fragment_id=" + url.QueryEscape(accepted.FragmentID) + "&revision_id=missing-revision"
	if r := meshRequest(h, meshRemotePeer, "GET", wrong, read, nil, nil); r.Code != http.StatusNotFound {
		t.Errorf("remote media with a foreign revision: got %d, want 404", r.Code)
	}
	other := "/v1/media/variants/" + url.PathEscape(posterID) + "/content?fragment_id=not-the-owner&revision_id=" + url.QueryEscape(accepted.FragmentRevisionID)
	if r := meshRequest(h, meshRemotePeer, "GET", other, read, nil, nil); r.Code != http.StatusNotFound {
		t.Errorf("remote media with a foreign fragment: got %d, want 404", r.Code)
	}

	// Administrative routes stay loopback-only, even for the owner key and a
	// token carrying the matching scope.
	for method, path := range map[string]string{"GET": "/v1/config", "POST": "/v1/config/update"} {
		for _, token := range []string{testAPIToken, tok("fragments-engine.admin")} {
			if r := meshRequest(h, meshRemotePeer, method, path, token, nil, nil); r.Code != http.StatusForbidden {
				t.Errorf("remote %s %s: got %d, want 403", method, path, r.Code)
			}
		}
	}
	for _, path := range []string{"/v1/workers/status", "/v1/jobs/ingest"} {
		method := "GET"
		for _, token := range []string{testAPIToken, tok("fragments-engine.admin")} {
			if r := meshRequest(h, meshRemotePeer, method, path, token, nil, nil); r.Code != http.StatusForbidden {
				t.Errorf("remote %s %s: got %d, want 403", method, path, r.Code)
			}
		}
	}
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
