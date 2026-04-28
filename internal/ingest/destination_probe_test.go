package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestProbeDestination_File(t *testing.T) {
	root := filepath.Join(t.TempDir(), "corpus")
	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: fmt.Sprintf(`{"root":%q}`, root),
	})
	if err != nil {
		t.Fatalf("probe file destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("expected file destination to be reachable: %+v", result)
	}
	if !strings.Contains(result.Message, "filesystem_ready:") {
		t.Fatalf("unexpected probe message: %s", result.Message)
	}
}

func TestProbeDestination_API(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "nanite-user-mailbox",
		Kind:       "api",
		ConfigJSON: fmt.Sprintf(`{"base_url":%q,"provider":"nanite_user_mailbox","nanite_messaging":{"to_session_id":"sess-123"}}`, srv.URL),
	})
	if err != nil {
		t.Fatalf("probe api destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("expected api destination to be reachable: %+v", result)
	}
	if !strings.Contains(result.Message, "/api/health") {
		t.Fatalf("unexpected probe message: %s", result.Message)
	}
}

func TestProbeDestination_CLI(t *testing.T) {
	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name: "cli-export",
		Kind: "cli",
		ConfigJSON: fmt.Sprintf(`{"command":%q,"args":["-test.run=TestHelperProcessCLIDestination","--",%q],"env":["GO_WANT_HELPER_PROCESS=1"]}`,
			os.Args[0],
			filepath.Join(t.TempDir(), "capture.json"),
		),
	})
	if err != nil {
		t.Fatalf("probe cli destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("expected cli destination to be reachable: %+v", result)
	}
	if !strings.Contains(result.Message, "cli_ready:") {
		t.Fatalf("unexpected probe message: %s", result.Message)
	}
}
