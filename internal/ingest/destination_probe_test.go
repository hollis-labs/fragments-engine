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

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	gomcpserver "github.com/hollis-labs/go-mcp/server"
	httptransport "github.com/hollis-labs/go-mcp/transport/http"
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

// TestProbeDestination_MCPHTTPTransport is the regression test for the
// third place (after MCPDestinationExecutor and normalizeDestination) that
// independently branched on MCP transport and had never been taught about
// "http": route destination-status reported a freshly created, genuinely
// reachable tangent_hitl destination as unreachable with "missing command",
// because probeMCPDestination assumed stdio unconditionally.
func TestProbeDestination_MCPHTTPTransport(t *testing.T) {
	mcpSrv := gomcpserver.NewServer("fake-tangent", "0.0.1")
	mcpSrv.RegisterTool(gomcpserver.Tool{
		Name:        "tangent.hitl_enqueue",
		Description: "fake",
		InputSchema: gomcpserver.EmptyObjectSchema(),
		Handler: func(_ context.Context, _ map[string]any) (any, error) {
			return "{}", nil
		},
	})
	httpSrv := httptest.NewServer(httptransport.NewHandler(mcpSrv, httptransport.HandlerOptions{}))
	defer httpSrv.Close()

	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "tangent-hitl",
		Kind:       "mcp",
		ConfigJSON: fmt.Sprintf(`{"transport":"http","base_url":%q,"provider":"tangent_hitl"}`, httpSrv.URL),
	})
	if err != nil {
		t.Fatalf("probe mcp http destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("expected mcp http destination to be reachable: %+v", result)
	}
	if !strings.Contains(result.Message, httpSrv.URL) {
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

func TestProbeDestination_Callback(t *testing.T) {
	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake","generator":"wiki_page"}`,
	})
	if err != nil {
		t.Fatalf("probe callback destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("expected callback destination config to be valid: %+v", result)
	}
	if !strings.Contains(result.Message, "config_valid:") {
		t.Fatalf("unexpected probe message: %s", result.Message)
	}
}

func TestProbeDestination_Callback_MissingTarget(t *testing.T) {
	_, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"generator":"wiki_page"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "missing target") {
		t.Fatalf("expected missing target error, got %v", err)
	}
}

func TestProbeDestination_Callback_MalformedTarget(t *testing.T) {
	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"target":"not-a-url","generator":"wiki_page"}`,
	})
	if err != nil {
		t.Fatalf("probe callback destination: %v", err)
	}
	if result.Reachable {
		t.Fatalf("expected malformed target to be reported as invalid: %+v", result)
	}
	if !strings.Contains(result.Message, "invalid_target:") {
		t.Fatalf("unexpected probe message: %s", result.Message)
	}
}

func TestProbeDestination_Callback_MissingGenerator(t *testing.T) {
	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake"}`,
	})
	if err != nil {
		t.Fatalf("probe callback destination: %v", err)
	}
	if result.Reachable {
		t.Fatalf("expected missing generator to be reported as invalid: %+v", result)
	}
	if !strings.Contains(result.Message, "missing_generator") {
		t.Fatalf("unexpected probe message: %s", result.Message)
	}
}

// TestProbeDestination_Callback_NeverDialsOut ensures probing a callback
// destination whose target is unreachable still reports config-only
// validity, proving the probe never performs live network I/O against
// Curator's endpoint (unlike probeAPIDestination/probeMCPDestination).
func TestProbeDestination_Callback_NeverDialsOut(t *testing.T) {
	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "curator-wake-unreachable",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://127.0.0.1:1/nanite/wake","generator":"wiki_page"}`,
	})
	if err != nil {
		t.Fatalf("probe callback destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("expected config-only validation to succeed regardless of live reachability: %+v", result)
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

func TestProbeDestination_File_AnchorsRelativeRootToInstallDir(t *testing.T) {
	installDir := t.TempDir()
	prev := config.InstallDir()
	config.SetInstallDir(installDir)
	t.Cleanup(func() { config.SetInstallDir(prev) })

	// Run from a CWD that is deliberately not installDir; a relative root
	// must still resolve under installDir, not this working directory.
	cwd := t.TempDir()
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWd) })

	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"./corpus"}`,
	})
	if err != nil {
		t.Fatalf("probe file destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("expected file destination to be reachable: %+v", result)
	}
	want := "filesystem_ready:" + filepath.Join(installDir, "corpus")
	if result.Message != want {
		t.Fatalf("relative root not anchored to install dir: got %s, want %s", result.Message, want)
	}
}

func TestProbeDestination_CLI_AnchorsRelativeWorkingDirToInstallDir(t *testing.T) {
	installDir := t.TempDir()
	prev := config.InstallDir()
	config.SetInstallDir(installDir)
	t.Cleanup(func() { config.SetInstallDir(prev) })

	if err := os.MkdirAll(filepath.Join(installDir, "work"), 0o750); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	// Run from a CWD that is deliberately not installDir; a relative
	// working_dir must still resolve under installDir, not this CWD, so the
	// probe's os.Stat(working_dir) finds the real directory.
	cwd := t.TempDir()
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWd) })

	result, err := ProbeDestination(context.Background(), domain.Destination{
		Name: "cli-export",
		Kind: "cli",
		ConfigJSON: fmt.Sprintf(`{"command":%q,"args":["-test.run=TestHelperProcessCLIDestination","--",%q],"env":["GO_WANT_HELPER_PROCESS=1"],"working_dir":"./work"}`,
			os.Args[0],
			filepath.Join(t.TempDir(), "capture.json"),
		),
	})
	if err != nil {
		t.Fatalf("probe cli destination: %v", err)
	}
	if !result.Reachable {
		t.Fatalf("relative working_dir not anchored to install dir: %+v", result)
	}
	if !strings.Contains(result.Message, "cli_ready:") {
		t.Fatalf("unexpected probe message: %s", result.Message)
	}
}
