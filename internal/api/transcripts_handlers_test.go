package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscriptAPIConfig(t *testing.T, enabled bool) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	flag := "false"
	if enabled {
		flag = "true"
	}
	raw := "database:\n  path: " + filepath.Join(root, "fragments.db") + "\nrecall:\n  backend: sqlite\ntranscripts:\n  private_root: " +
		filepath.Join(root, "private") + "\nui:\n  transcripts:\n    enabled: " + flag +
		"\ningests:\n  - name: api-test\n    kind: filesystem_docs\n    enabled: false\n    source:\n      root: ./docs\n    routing:\n      namespace: tests\n    rules: {}\n    labels: {}\n"
	path := filepath.Join(root, "fragments.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func transcriptGet(handler http.Handler, path, remote, host string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = remote
	if host != "" {
		request.Host = host
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

var transcriptPaths = []string{
	"/v1/transcripts/status", "/v1/transcripts/sessions", "/v1/transcripts/facets",
	"/v1/transcripts/sessions/" + strings.Repeat("a", 64),
}

func TestTranscriptRoutes404WhenDisabled(t *testing.T) {
	handler := NewServer(writeTranscriptAPIConfig(t, false)).Handler()
	for _, p := range transcriptPaths {
		if r := transcriptGet(handler, p, "127.0.0.1:1234", ""); r.Code != http.StatusNotFound {
			t.Fatalf("%s disabled status=%d", p, r.Code)
		}
	}
	// An unreadable config fails closed too.
	handler = NewServer(filepath.Join(t.TempDir(), "missing.yaml")).Handler()
	if r := transcriptGet(handler, transcriptPaths[0], "127.0.0.1:1234", ""); r.Code != http.StatusNotFound {
		t.Fatalf("missing config status=%d", r.Code)
	}
}

func TestTranscriptRoutesEnabledServeLoopbackOnly(t *testing.T) {
	handler := NewServer(writeTranscriptAPIConfig(t, true)).Handler()
	for _, p := range transcriptPaths[:3] {
		r := transcriptGet(handler, p, "127.0.0.1:1234", "127.0.0.1:8091")
		if r.Code != http.StatusOK {
			t.Fatalf("%s enabled status=%d body=%s", p, r.Code, r.Body.String())
		}
		if r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s missing no-store", p)
		}
	}
	if r := transcriptGet(handler, transcriptPaths[3], "127.0.0.1:1234", "localhost:8091"); r.Code != http.StatusNotFound {
		t.Fatalf("unknown session status=%d", r.Code)
	}
	if r := transcriptGet(handler, "/v1/transcripts/sessions/not-a-hash", "127.0.0.1:1234", "127.0.0.1:8091"); r.Code != http.StatusNotFound {
		t.Fatalf("malformed id status=%d", r.Code)
	}
	if r := transcriptGet(handler, transcriptPaths[1], "203.0.113.9:1234", "127.0.0.1:8091"); r.Code != http.StatusForbidden {
		t.Fatalf("remote peer status=%d", r.Code)
	}
	if r := transcriptGet(handler, transcriptPaths[1], "127.0.0.1:1234", "rebind.example.com"); r.Code != http.StatusForbidden {
		t.Fatalf("rebound host status=%d", r.Code)
	}
	post := httptest.NewRequest(http.MethodPost, transcriptPaths[1], nil)
	post.RemoteAddr = "127.0.0.1:1234"
	post.Host = "127.0.0.1:8091"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, post)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", rec.Code)
	}
}

func TestListenRefusesNonLoopbackWhenTranscriptsEnabled(t *testing.T) {
	enabled := writeTranscriptAPIConfig(t, true)
	for _, addr := range []string{":8091", "0.0.0.0:8091", "192.0.2.1:8091", "8091"} {
		if err := checkTranscriptListen(enabled, addr); err == nil {
			t.Fatalf("%q accepted", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8091", "[::1]:8091", "localhost:8091"} {
		if err := checkTranscriptListen(enabled, addr); err != nil {
			t.Fatalf("%q refused: %v", addr, err)
		}
	}
	if err := checkTranscriptListen(writeTranscriptAPIConfig(t, false), "0.0.0.0:8091"); err != nil {
		t.Fatalf("disabled config must not constrain the listener: %v", err)
	}
}
