package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
)

func serveCollectionRequest(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var raw []byte
	switch value := body.(type) {
	case nil:
	case string:
		raw = []byte(value)
	default:
		raw, _ = json.Marshal(value)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.RemoteAddr = "127.0.0.1:1234"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeCollectionResponse(t *testing.T, response *httptest.ResponseRecorder, status int) capturecontract.ReaderCollection {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaReaderCollection, response.Body.Bytes())
	var collection capturecontract.ReaderCollection
	if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	return collection
}

// seedCollectionCaptures accepts count distinct captures and returns their
// fragment IDs in capture order.
func seedCollectionCaptures(t *testing.T, server http.Handler, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		envelope, err := capturecontract.DecodeCaptureEnvelope(readCaptureAPIFixture(t, "valid-capture-youtube.json"))
		if err != nil {
			t.Fatal(err)
		}
		providerID := fmt.Sprintf("collect%04d", index)
		envelope.CaptureID = "collection-capture-" + providerID
		envelope.Source.ProviderItemID = providerID
		envelope.Source.SourceItemKey = "youtube:" + providerID
		envelope.Source.SubmittedURL = "https://www.youtube.com/watch?v=" + providerID
		envelope.Source.CanonicalURL = envelope.Source.SubmittedURL
		for annotationIndex := range envelope.Annotations {
			envelope.Annotations[annotationIndex].AnnotationID = fmt.Sprintf("collection-annotation-%d-%d", index, annotationIndex)
		}
		envelope.Media[0].ClientMediaID = "media-" + providerID
		envelope.Media[0].ProviderMediaID = providerID
		envelope.Media[0].SourceLocator = envelope.Source.CanonicalURL
		envelope.Media[0].Variants[0].SourceURL = envelope.Source.CanonicalURL
		envelope.Media[0].Variants[1].SourceURL = "https://i.ytimg.com/vi/" + providerID + "/maxresdefault.jpg"
		created := serveCaptureRequest(t, server, http.MethodPost, "/v1/captures", "application/json", encodeCaptureAPIEnvelope(t, envelope), "")
		if created.Code != http.StatusCreated {
			t.Fatalf("seed capture %d: %d %s", index, created.Code, created.Body.String())
		}
		var accepted capturecontract.CaptureManifestResponse
		if err := json.Unmarshal(created.Body.Bytes(), &accepted); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, accepted.FragmentID)
	}
	return ids
}

func TestReaderCollectionHTTPLifecycleReturnsPublishedContracts(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	ids := seedCollectionCaptures(t, server, 3)

	created := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodPost, "/v1/reader/collections",
		map[string]any{"name": "Watch later", "kind": "playlist"}), http.StatusCreated)
	if created.Revision != 1 || created.Kind != "playlist" || created.CollectionID == "" {
		t.Fatalf("created = %+v", created)
	}
	base := "/v1/reader/collections/" + url.PathEscape(created.CollectionID)

	revision := created.Revision
	for _, fragmentID := range ids {
		updated := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodPost, base+"/items",
			map[string]any{"expected_revision": revision, "fragment_id": fragmentID}), http.StatusOK)
		revision = updated.Revision
	}

	order := []string{ids[2], ids[0], ids[1]}
	reordered := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodPut, base+"/order",
		map[string]any{"expected_revision": revision, "fragment_ids": order}), http.StatusOK)
	if reordered.Revision != revision+1 {
		t.Fatalf("reorder revision = %d, want %d", reordered.Revision, revision+1)
	}
	revision = reordered.Revision

	listed := serveCollectionRequest(server, http.MethodGet, base+"/items", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("items status=%d body=%s", listed.Code, listed.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaReaderCollectionItems, listed.Body.Bytes())
	var page capturecontract.ReaderCollectionItems
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range page.Items {
		got = append(got, item.FragmentID)
		if len(item.Collections) != 1 || item.Collections[0].CollectionID != created.CollectionID {
			t.Fatalf("item %s collections = %+v", item.FragmentID, item.Collections)
		}
	}
	if !reflect.DeepEqual(got, order) || page.Collection.Revision != revision || page.NextCursor != "" {
		t.Fatalf("items = %v (want %v), collection = %+v", got, order, page.Collection)
	}

	// The ordinary Reader routes carry the membership and still validate.
	detail := serveReaderRequest(server, http.MethodGet, "/v1/reader/items/"+url.PathEscape(ids[0]))
	assertCaptureAPIContract(t, capturecontract.SchemaReaderItem, detail.Body.Bytes())
	var item capturecontract.ReaderItem
	if err := json.Unmarshal(detail.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if len(item.Collections) != 1 || item.Collections[0].Name != "Watch later" || item.Collections[0].Kind != "playlist" {
		t.Fatalf("reader item collections = %+v", item.Collections)
	}
	assertCaptureAPIContract(t, capturecontract.SchemaReaderList, serveReaderRequest(server, http.MethodGet, "/v1/reader/items?scope=all").Body.Bytes())

	renamed := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodPatch, base,
		map[string]any{"expected_revision": revision, "name": "Queue", "description": "Talks"}), http.StatusOK)
	if renamed.Name != "Queue" || renamed.Description != "Talks" || renamed.Revision != revision+1 {
		t.Fatalf("renamed = %+v", renamed)
	}
	revision = renamed.Revision

	removed := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodDelete,
		fmt.Sprintf("%s/items/%s?expected_revision=%d", base, url.PathEscape(ids[0]), revision), nil), http.StatusOK)
	if removed.ItemCount != 2 {
		t.Fatalf("removed = %+v", removed)
	}
	revision = removed.Revision

	all := serveCollectionRequest(server, http.MethodGet, "/v1/reader/collections", nil)
	if all.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", all.Code, all.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaReaderCollectionList, all.Body.Bytes())
	var list capturecontract.ReaderCollectionList
	if err := json.Unmarshal(all.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Collections) != 1 || list.Collections[0].ItemCount != 2 || list.Collections[0].Name != "Queue" {
		t.Fatalf("list = %+v", list.Collections)
	}
	if got := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodGet, base, nil), http.StatusOK); got.Revision != revision {
		t.Fatalf("get = %+v", got)
	}

	if deleted := serveCollectionRequest(server, http.MethodDelete, fmt.Sprintf("%s?expected_revision=%d", base, revision), nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	// The collection is gone; every fragment is still a Reader item.
	if gone := serveCollectionRequest(server, http.MethodGet, base, nil); gone.Code != http.StatusNotFound {
		t.Fatalf("get deleted status=%d", gone.Code)
	}
	for _, fragmentID := range ids {
		kept := serveReaderRequest(server, http.MethodGet, "/v1/reader/items/"+url.PathEscape(fragmentID))
		if kept.Code != http.StatusOK {
			t.Fatalf("fragment %s after collection delete: %d %s", fragmentID, kept.Code, kept.Body.String())
		}
	}
}

func TestReaderCollectionHTTPProblems(t *testing.T) {
	server := NewServer(writeCaptureAPIConfig(t)).Handler()
	ids := seedCollectionCaptures(t, server, 1)
	created := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodPost, "/v1/reader/collections",
		map[string]any{"name": "Problems"}), http.StatusCreated)
	base := "/v1/reader/collections/" + url.PathEscape(created.CollectionID)
	missing := "/v1/reader/collections/rc-missing"

	cases := []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"create without a name", http.MethodPost, "/v1/reader/collections", map[string]any{"kind": "playlist"}, http.StatusBadRequest},
		{"create with a blank name", http.MethodPost, "/v1/reader/collections", map[string]any{"name": "   "}, http.StatusBadRequest},
		{"create with an unknown field", http.MethodPost, "/v1/reader/collections", map[string]any{"name": "x", "owner": "someone"}, http.StatusBadRequest},
		{"create with a bad kind", http.MethodPost, "/v1/reader/collections", map[string]any{"name": "x", "kind": "Play List"}, http.StatusBadRequest},
		{"create with malformed JSON", http.MethodPost, "/v1/reader/collections", `{"name":`, http.StatusBadRequest},
		{"create a duplicate name", http.MethodPost, "/v1/reader/collections", map[string]any{"name": "problems"}, http.StatusConflict},
		{"get unknown", http.MethodGet, missing, nil, http.StatusNotFound},
		{"items of unknown", http.MethodGet, missing + "/items", nil, http.StatusNotFound},
		{"items with a bad cursor", http.MethodGet, base + "/items?cursor=not-a-cursor", nil, http.StatusBadRequest},
		{"update unknown", http.MethodPatch, missing, map[string]any{"expected_revision": 1, "name": "x"}, http.StatusNotFound},
		{"update without a revision", http.MethodPatch, base, map[string]any{"name": "x"}, http.StatusBadRequest},
		{"update with nothing to change", http.MethodPatch, base, map[string]any{"expected_revision": 1}, http.StatusBadRequest},
		{"update with a stale revision", http.MethodPatch, base, map[string]any{"expected_revision": 0, "name": "x"}, http.StatusConflict},
		{"add without a revision", http.MethodPost, base + "/items", map[string]any{"fragment_id": ids[0]}, http.StatusBadRequest},
		{"add an unknown fragment", http.MethodPost, base + "/items", map[string]any{"expected_revision": 1, "fragment_id": "no-such-fragment"}, http.StatusNotFound},
		{"add from a future revision", http.MethodPost, base + "/items", map[string]any{"expected_revision": 7, "fragment_id": ids[0]}, http.StatusConflict},
		{"remove without a revision", http.MethodDelete, base + "/items/" + url.PathEscape(ids[0]), nil, http.StatusBadRequest},
		{"remove with a stale revision", http.MethodDelete, base + "/items/" + url.PathEscape(ids[0]) + "?expected_revision=0", nil, http.StatusConflict},
		{"reorder with a non-member", http.MethodPut, base + "/order", map[string]any{"expected_revision": 1, "fragment_ids": []string{ids[0]}}, http.StatusBadRequest},
		{"reorder with a duplicate", http.MethodPut, base + "/order", map[string]any{"expected_revision": 1, "fragment_ids": []string{"a", "a"}}, http.StatusBadRequest},
		{"delete without a revision", http.MethodDelete, base, nil, http.StatusBadRequest},
		{"delete with a stale revision", http.MethodDelete, base + "?expected_revision=4", nil, http.StatusConflict},
		{"delete unknown", http.MethodDelete, missing + "?expected_revision=1", nil, http.StatusNotFound},
	}
	for _, test := range cases {
		response := serveCollectionRequest(server, test.method, test.path, test.body)
		if response.Code != test.want {
			t.Errorf("%s: status=%d want=%d body=%s", test.name, response.Code, test.want, response.Body.String())
			continue
		}
		assertCaptureAPIContract(t, capturecontract.SchemaAPIProblem, response.Body.Bytes())
	}
	if response := serveCollectionRequest(server, http.MethodPost, base, nil); response.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST on a collection: status=%d, want 405", response.Code)
	}
	// None of the refused requests changed the collection.
	if got := decodeCollectionResponse(t, serveCollectionRequest(server, http.MethodGet, base, nil), http.StatusOK); got.Revision != 1 || got.ItemCount != 0 || got.Name != "Problems" {
		t.Fatalf("collection after refused requests = %+v", got)
	}
}

// Collection reads need the read scope and every collection write, whatever
// its method, needs the write scope.
func TestReaderCollectionRoutesOverTheMeshAreGatedByScope(t *testing.T) {
	i := newTestIssuer(t)
	s, _ := issuerServer(t, i)
	h := s.Handler()
	tok := func(scope string) string { return i.token(t, map[string]any{"scope": scope}) }
	read, write := tok("fragments-engine.read"), tok("fragments-engine.write")

	created := meshRequest(h, meshRemotePeer, "POST", "/v1/reader/collections", write, []byte(`{"name":"Mesh"}`), nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("remote write-scope create: %d %s", created.Code, created.Body.String())
	}
	var collection capturecontract.ReaderCollection
	if err := json.Unmarshal(created.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	base := "/v1/reader/collections/" + url.PathEscape(collection.CollectionID)

	gets := []string{"/v1/reader/collections", base, base + "/items"}
	writes := []struct {
		method, path string
		body         []byte
	}{
		{"POST", "/v1/reader/collections", []byte(`{"name":"Refused"}`)},
		{"PATCH", base, []byte(`{"expected_revision":1,"name":"Refused"}`)},
		{"POST", base + "/items", []byte(`{"expected_revision":1,"fragment_id":"fragment"}`)},
		{"DELETE", base + "/items/fragment?expected_revision=1", nil},
		{"PUT", base + "/order", []byte(`{"expected_revision":1,"fragment_ids":[]}`)},
		{"DELETE", base + "?expected_revision=1", nil},
	}
	for _, path := range gets {
		if r := meshRequest(h, meshRemotePeer, "GET", path, read, nil, nil); r.Code != http.StatusOK {
			t.Errorf("remote read-scope GET %s: %d %s", path, r.Code, truncate(r.Body.String()))
		}
		for name, token := range map[string]string{"write-only": write, "capture-only": tok("fragments-engine.capture"), "admin-only": tok("fragments-engine.admin")} {
			if r := meshRequest(h, meshRemotePeer, "GET", path, token, nil, nil); r.Code != http.StatusForbidden {
				t.Errorf("remote %s GET %s: got %d, want 403", name, path, r.Code)
			}
		}
		if r := meshRequest(h, meshRemotePeer, "GET", path, "", nil, nil); r.Code != http.StatusUnauthorized {
			t.Errorf("remote unauthenticated GET %s: got %d, want 401", path, r.Code)
		}
	}
	for _, test := range writes {
		for name, token := range map[string]string{"read-only": read, "capture-only": tok("fragments-engine.capture"), "admin-only": tok("fragments-engine.admin")} {
			if r := meshRequest(h, meshRemotePeer, test.method, test.path, token, test.body, nil); r.Code != http.StatusForbidden {
				t.Errorf("remote %s %s %s: got %d, want 403", name, test.method, test.path, r.Code)
			}
		}
		if r := meshRequest(h, meshRemotePeer, test.method, test.path, "", test.body, nil); r.Code != http.StatusUnauthorized {
			t.Errorf("remote unauthenticated %s %s: got %d, want 401", test.method, test.path, r.Code)
		}
	}
	// The refused writes changed nothing, and the write scope reaches each one.
	after := meshRequest(h, meshRemotePeer, "GET", base, read, nil, nil)
	if err := json.Unmarshal(after.Body.Bytes(), &collection); err != nil || collection.Name != "Mesh" || collection.Revision != 1 {
		t.Fatalf("collection after refused writes: %s", truncate(after.Body.String()))
	}
	for _, test := range writes[1:] {
		r := meshRequest(h, meshRemotePeer, test.method, test.path, write, test.body, nil)
		if r.Code == http.StatusForbidden || r.Code == http.StatusUnauthorized {
			t.Errorf("remote write-scope %s %s was refused: %d", test.method, test.path, r.Code)
		}
	}
}
