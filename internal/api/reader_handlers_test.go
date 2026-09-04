package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
)

func TestReaderHTTPListAndDetailReturnFrozenV1Contracts(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json",
		readCaptureAPIFixture(t, "valid-capture-youtube.json"), "")
	if created.Code != http.StatusCreated {
		t.Fatalf("seed capture: %d %s", created.Code, created.Body.String())
	}
	var acceptance capturecontract.CaptureManifestResponse
	if err := json.Unmarshal(created.Body.Bytes(), &acceptance); err != nil {
		t.Fatal(err)
	}

	listed := serveReaderRequest(server, http.MethodGet, "/v1/reader/items?scope=all")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaReaderList, listed.Body.Bytes())
	var page capturecontract.ReaderItemList
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].FragmentID != acceptance.FragmentID ||
		page.Items[0].FragmentRevisionID != acceptance.FragmentRevisionID {
		t.Fatalf("persisted Reader list changed accepted identity: %+v", page.Items)
	}
	if page.Items[0].Revision != 0 {
		t.Fatalf("Reader aggregate revision=%d, want explicit no-command-state fallback 0", page.Items[0].Revision)
	}
	if page.Items[0].Playback == nil || page.Items[0].Playback.ProviderItemID != acceptance.ReaderItem.Playback.ProviderItemID {
		t.Fatalf("persisted projection lost safe optimistic playback: %+v", page.Items[0].Playback)
	}
	if len(page.Items[0].Actions) != 0 || page.Items[0].ReadingState.State != "unread" {
		t.Fatalf("unsupported commands/reading default = %+v %+v", page.Items[0].Actions, page.Items[0].ReadingState)
	}

	detailPath := "/v1/reader/items/" + acceptance.FragmentID + "?revision_id=" + acceptance.FragmentRevisionID
	detail := serveReaderRequest(server, http.MethodGet, detailPath)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaReaderItem, detail.Body.Bytes())
	if got := detail.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("detail Cache-Control=%q", got)
	}
}

func TestReaderHTTPRejectsInvalidQueriesAndUnownedRevision(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json",
		readCaptureAPIFixture(t, "valid-capture-youtube.json"), "")
	if created.Code != http.StatusCreated {
		t.Fatalf("seed capture: %d %s", created.Code, created.Body.String())
	}
	var acceptance capturecontract.CaptureManifestResponse
	_ = json.Unmarshal(created.Body.Bytes(), &acceptance)

	cases := []struct {
		path string
		want int
	}{
		{"/v1/reader/items", http.StatusBadRequest},
		{"/v1/reader/items?scope=unknown", http.StatusBadRequest},
		{"/v1/reader/items?scope=all&cursor=not-a-cursor", http.StatusBadRequest},
		{"/v1/reader/items/" + acceptance.FragmentID + "?revision_id=", http.StatusBadRequest},
		{"/v1/reader/items/" + acceptance.FragmentID + "?revision_id=another-revision", http.StatusNotFound},
		{"/v1/reader/items/" + acceptance.FragmentID + "/commands", http.StatusNotFound},
	}
	for _, test := range cases {
		response := serveReaderRequest(server, http.MethodGet, test.path)
		if response.Code != test.want {
			t.Errorf("GET %s status=%d want=%d body=%s", test.path, response.Code, test.want, response.Body.String())
			continue
		}
		assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, response.Body.Bytes())
	}
}

func TestReaderHTTPIsRestrictedToLoopback(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/reader/items?scope=all", nil)
	request.RemoteAddr = "203.0.113.10:1234"
	response := httptest.NewRecorder()
	NewServer("unused.yaml").Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote Reader status=%d body=%s", response.Code, response.Body.String())
	}
}

func serveReaderRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(nil))
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
