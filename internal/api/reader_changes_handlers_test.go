package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
)

func decodeChangesResponse(t *testing.T, response *httptest.ResponseRecorder) capturecontract.ReaderChangeList {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("changes status=%d body=%s", response.Code, response.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaReaderChanges, response.Body.Bytes())
	var page capturecontract.ReaderChangeList
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func changesPath(after string, limit string) string {
	query := url.Values{}
	if after != "" {
		query.Set("after", after)
	}
	if limit != "" {
		query.Set("limit", limit)
	}
	if len(query) == 0 {
		return "/v1/reader/changes"
	}
	return "/v1/reader/changes?" + query.Encode()
}

func TestReaderChangesHTTPSyncsThenReportsCaptureMediaAndCommandChanges(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	ids := seedCollectionCaptures(t, server, 3)

	// A first sync, one item per page, returns every capture once.
	seen := map[string]int{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("first sync never caught up")
		}
		page := decodeChangesResponse(t, serveReaderRequest(server, http.MethodGet, changesPath(cursor, "1")))
		for _, entry := range page.Items {
			seen[entry.FragmentID]++
			if entry.Item.FragmentID != entry.FragmentID || !containsKind(entry.Kinds, "capture") {
				t.Fatalf("first-sync entry = %+v", entry)
			}
		}
		cursor = page.NextCursor
		if page.CaughtUp {
			break
		}
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("first sync saw %v, want each of %v once", seen, ids)
		}
	}
	if page := decodeChangesResponse(t, serveReaderRequest(server, http.MethodGet, changesPath(cursor, ""))); len(page.Items) != 0 || !page.CaughtUp || page.NextCursor == "" {
		t.Fatalf("idle poll = %+v", page)
	}

	// Uploading a requested asset makes media available on one item.
	payload := testJPEGResourceBytes()
	sum := sha256.Sum256(payload)
	uploaded := serveCaptureRequest(t, server, http.MethodPut,
		"/v1/captures/collection-capture-collect0001/assets/"+url.PathEscape("poster:maxresdefault")+"/content",
		"image/jpeg", payload, "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":")
	if uploaded.Code != http.StatusNoContent {
		t.Fatalf("upload: %d %s", uploaded.Code, uploaded.Body.String())
	}
	page := decodeChangesResponse(t, serveReaderRequest(server, http.MethodGet, changesPath(cursor, "")))
	if len(page.Items) != 1 || page.Items[0].FragmentID != ids[1] || !containsKind(page.Items[0].Kinds, "media") || !page.CaughtUp {
		t.Fatalf("after an asset upload the feed returned %+v", page.Items)
	}
	cursor = page.NextCursor

	// A Reader command and a collection change on two different items.
	command := `{"schema_version":"fe.reader.command.v1","command":"mark_read","command_id":"changes-read","idempotency_key":"changes-read-key","expected_revision":0}`
	if response := serveCollectionRequest(server, http.MethodPost, "/v1/reader/items/"+url.PathEscape(ids[0])+"/commands", command); response.Code != http.StatusOK {
		t.Fatalf("command: %d %s", response.Code, response.Body.String())
	}
	collection := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodPost, "/v1/reader/collections",
		map[string]any{"name": "Feed"}), http.StatusCreated)
	decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodPost, "/v1/reader/collections/"+collection.CollectionID+"/items",
		map[string]any{"expected_revision": 1, "fragment_id": ids[2]}), http.StatusOK)
	page = decodeChangesResponse(t, serveReaderRequest(server, http.MethodGet, changesPath(cursor, "")))
	if len(page.Items) != 2 || page.Items[0].FragmentID != ids[0] || page.Items[1].FragmentID != ids[2] ||
		!containsKind(page.Items[0].Kinds, "reading_state") || !containsKind(page.Items[1].Kinds, "collections") {
		t.Fatalf("after a command and a collection add the feed returned %+v", page.Items)
	}
	if page.Items[0].Item.ReadingState.State != "read" || len(page.Items[1].Item.Collections) != 1 {
		t.Fatalf("feed items are not at their latest state: %+v", page.Items)
	}
	if page.Items[0].ChangeSeq >= page.Items[1].ChangeSeq {
		t.Fatalf("feed is not in sequence order: %d then %d", page.Items[0].ChangeSeq, page.Items[1].ChangeSeq)
	}
}

func TestReaderChangesHTTPRejectsInvalidQueries(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	for _, path := range []string{
		"/v1/reader/changes?after=not-a-cursor",
		"/v1/reader/changes?limit=0",
		"/v1/reader/changes?limit=201",
		"/v1/reader/changes?limit=many",
		"/v1/reader/changes?limit=1&limit=2",
		"/v1/reader/changes?after=a&after=b",
	} {
		response := serveReaderRequest(server, http.MethodGet, path)
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status=%d want=400 body=%s", path, response.Code, response.Body.String())
			continue
		}
		assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, response.Body.Bytes())
	}
	if response := serveReaderRequest(server, http.MethodPost, "/v1/reader/changes"); response.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST changes status=%d, want 405", response.Code)
	}
	// An empty database is caught up with an empty page and a usable cursor.
	page := decodeChangesResponse(t, serveReaderRequest(server, http.MethodGet, "/v1/reader/changes?after="))
	if len(page.Items) != 0 || !page.CaughtUp || page.NextCursor == "" {
		t.Fatalf("empty feed = %+v", page)
	}
	decodeChangesResponse(t, serveReaderRequest(server, http.MethodGet, changesPath(page.NextCursor, "200")))
}

func TestReaderChangesOverTheMeshNeedsTheReadScope(t *testing.T) {
	i := newTestIssuer(t)
	s, _ := issuerServer(t, i)
	h := s.Handler()
	tok := func(scope string) string { return i.token(t, map[string]any{"scope": scope}) }
	if r := meshRequest(h, meshRemotePeer, "GET", "/v1/reader/changes", tok("fragments-engine.read"), nil, nil); r.Code != http.StatusOK {
		t.Errorf("remote read-scope changes: %d %s", r.Code, truncate(r.Body.String()))
	}
	for name, scope := range map[string]string{"write-only": "fragments-engine.write", "capture-only": "fragments-engine.capture", "admin-only": "fragments-engine.admin"} {
		if r := meshRequest(h, meshRemotePeer, "GET", "/v1/reader/changes", tok(scope), nil, nil); r.Code != http.StatusForbidden {
			t.Errorf("remote %s changes: got %d, want 403", name, r.Code)
		}
	}
	if r := meshRequest(h, meshRemotePeer, "GET", "/v1/reader/changes", "", nil, nil); r.Code != http.StatusUnauthorized {
		t.Errorf("remote unauthenticated changes: got %d, want 401", r.Code)
	}
}

func containsKind(kinds []string, kind string) bool {
	for _, value := range kinds {
		if value == kind {
			return true
		}
	}
	return false
}
