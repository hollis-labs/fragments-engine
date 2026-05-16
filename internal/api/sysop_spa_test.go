package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSysopSPAHandlerServesIndexAtBasePath(t *testing.T) {
	handler := newSysopSPAHandlerFS(sysopBasePath, testSysopDistFS())

	req := httptest.NewRequest(http.MethodGet, "/sysop/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, "sysop-index") {
		t.Fatalf("body = %q, want embedded index", body)
	}
}

func TestSysopSPAHandlerFallsBackToIndexForClientRoutes(t *testing.T) {
	handler := newSysopSPAHandlerFS(sysopBasePath, testSysopDistFS())

	req := httptest.NewRequest(http.MethodGet, "/sysop/some-nonexistent-route", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, "sysop-index") {
		t.Fatalf("body = %q, want embedded index fallback", body)
	}
}

func TestSysopSPAHandlerServesAssetsAnd404sMissingAssets(t *testing.T) {
	handler := newSysopSPAHandlerFS(sysopBasePath, testSysopDistFS())

	t.Run("asset hit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sysop/assets/app.js", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if body := rec.Body.String(); !strings.Contains(body, "console.log") {
			t.Fatalf("body = %q, want asset content", body)
		}
	})

	t.Run("asset miss", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sysop/assets/missing.js", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestSysopSPAHandlerServesPlaceholderWithoutIndex(t *testing.T) {
	handler := newSysopSPAHandlerFS(sysopBasePath, fstest.MapFS{
		".gitkeep": &fstest.MapFile{Data: []byte{}},
	})

	req := httptest.NewRequest(http.MethodGet, "/sysop/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, "make sysop-build") {
		t.Fatalf("body = %q, want placeholder instructions", body)
	}
}

func TestSysopMountPatternLeavesV1RoutesAlone(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(sysopBasePath+"/", newSysopSPAHandlerFS(sysopBasePath, testSysopDistFS()))
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("api"))
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/search?q=test", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if body := rec.Body.String(); body != "api" {
		t.Fatalf("body = %q, want API response", body)
	}
}

func testSysopDistFS() fs.FS {
	return fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html><body>sysop-index</body></html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('sysop');")},
	}
}
