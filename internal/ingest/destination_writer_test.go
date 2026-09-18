package ingest

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestAPIDestinationExecutor_NaniteMessaging(t *testing.T) {
	t.Setenv("FE_TEST_NANITE_PASSWORD", "secret")

	var (
		gotAuth string
		gotPath string
		gotBody map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	destination := domain.Destination{
		Name: "nanite-api",
		Kind: "api",
		ConfigJSON: fmt.Sprintf(`{
			"base_url": %q,
			"provider": "nanite_messaging",
			"basic_auth_username": "fe-bot",
			"basic_auth_password_env": "FE_TEST_NANITE_PASSWORD",
			"nanite_messaging": {
				"from_session_id": "sess-fe",
				"from_agent_id": "fragments-engine",
				"to_session_id": "sess-nanite",
				"to_agent_id": "user",
				"channel": "inbox",
				"kind": "notification",
				"type": "status_update",
				"register_as": "external",
				"subject_prefix": "[FE] "
			}
		}`, srv.URL),
	}

	written, err := APIDestinationExecutor{}.Execute(context.Background(), destination, testFragment(), nil)
	if err != nil {
		t.Fatalf("execute api destination: %v", err)
	}
	if !strings.HasPrefix(written.Ref, "api:POST:"+srv.URL+"/api/messaging/send") {
		t.Fatalf("unexpected written ref: %s", written.Ref)
	}
	if gotPath != "/api/messaging/send" {
		t.Fatalf("unexpected api path: %s", gotPath)
	}
	if gotAuth == "" {
		t.Fatal("expected basic auth header")
	}
	if gotBody["FromSessionID"] != "sess-fe" {
		t.Fatalf("unexpected FromSessionID: %#v", gotBody["FromSessionID"])
	}
	if gotBody["ToAgentID"] != "user" {
		t.Fatalf("unexpected ToAgentID: %#v", gotBody["ToAgentID"])
	}
	if gotBody["RegisterAs"] != "external" {
		t.Fatalf("unexpected RegisterAs: %#v", gotBody["RegisterAs"])
	}
	bodyText, _ := gotBody["Body"].(string)
	if !strings.Contains(bodyText, "# Claude session: roadmap-review") {
		t.Fatalf("expected fragment markdown in body: %s", bodyText)
	}
}

func TestMCPDestinationExecutor_NilInbox(t *testing.T) {
	tempDir := t.TempDir()
	capturePath := filepath.Join(tempDir, "tool-call.json")

	destination := domain.Destination{
		Name: "nil-inbox",
		Kind: "mcp",
		ConfigJSON: fmt.Sprintf(`{
			"transport": "stdio",
			"command": %q,
			"args": ["-test.run=TestHelperProcessMCPServer", "--", %q],
			"env": ["GO_WANT_HELPER_PROCESS=1"],
			"provider": "nil_inbox",
			"nil_inbox": {
				"title_prefix": "[FE] ",
				"item_type": "note",
				"tags": ["fragment", "fe"],
				"contexts": ["@inbox"],
				"projects": ["fragments-engine"]
			}
		}`, os.Args[0], capturePath),
	}

	written, err := MCPDestinationExecutor{}.Execute(context.Background(), destination, testFragment(), nil)
	if err != nil {
		t.Fatalf("execute mcp destination: %v", err)
	}
	if !strings.HasPrefix(written.Ref, "mcp:nil_create_inbox:") {
		t.Fatalf("unexpected written ref: %s", written.Ref)
	}

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	var captured struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatalf("decode capture file: %v", err)
	}
	if captured.Name != "nil_create_inbox" {
		t.Fatalf("unexpected tool name: %s", captured.Name)
	}
	if captured.Arguments["type"] != "note" {
		t.Fatalf("unexpected nil inbox type: %#v", captured.Arguments["type"])
	}
	if captured.Arguments["title"] != "[FE] Claude session: roadmap-review" {
		t.Fatalf("unexpected nil inbox title: %#v", captured.Arguments["title"])
	}
	notes, _ := captured.Arguments["notes_md"].(string)
	if !strings.Contains(notes, "deterministic ingest and search") {
		t.Fatalf("expected fragment markdown in notes: %s", notes)
	}
}

// TestAPIDestinationExecutor_GenericProviderTemplatedBody covers the "api"
// half of CW-20260917-0003's declarative-provider capability: a custom
// provider name with a templated "body" map, no Go code for that provider
// at all -- proving a new HTTP integration is addable as config.
func TestAPIDestinationExecutor_GenericProviderTemplatedBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	fragment := testFragment()
	destination := domain.Destination{
		Name: "generic-webhook",
		Kind: "api",
		ConfigJSON: fmt.Sprintf(`{
			"base_url": %q,
			"path": "/webhook",
			"provider": "generic_webhook",
			"body": {
				"fragment_id": "{{.Fragment.ID}}",
				"title": "{{.Fragment.Title | truncate 10}}",
				"payload": {"content": "{{.Fragment.Content}}"}
			}
		}`, srv.URL),
	}

	if _, err := (APIDestinationExecutor{}).Execute(context.Background(), destination, fragment, nil); err != nil {
		t.Fatalf("execute api destination: %v", err)
	}
	if gotBody["fragment_id"] != fragment.ID {
		t.Fatalf("unexpected fragment_id: %#v", gotBody["fragment_id"])
	}
	if gotBody["title"] != "Claude ses" {
		t.Fatalf("expected truncated title, got %#v", gotBody["title"])
	}
	payload, _ := gotBody["payload"].(map[string]any)
	if payload["content"] != fragment.Content {
		t.Fatalf("expected nested map field rendered from fragment content, got %#v", payload)
	}
}

func TestCallbackDestinationExecutor_ForwardsGeneratorUnmodified(t *testing.T) {
	var (
		gotMethod  string
		gotPath    string
		gotBody    map[string]any
		gotHeaders http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode callback body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	destination := domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: fmt.Sprintf(`{"target":%q,"generator":"wiki_page::do-not-interpret-me"}`, srv.URL+"/nanite/wake"),
	}

	written, err := CallbackDestinationExecutor{}.Execute(context.Background(), destination, testFragment(), nil)
	if err != nil {
		t.Fatalf("execute callback destination: %v", err)
	}
	if written.Ref != "callback:"+srv.URL+"/nanite/wake" {
		t.Fatalf("unexpected written ref: %s", written.Ref)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/nanite/wake" {
		t.Fatalf("unexpected callback path: %s", gotPath)
	}
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Fatalf("expected json content-type, got %q", gotHeaders.Get("Content-Type"))
	}
	// The generator value must be forwarded byte-for-byte, including the
	// "::" characters that would be meaningful as a directive prefix
	// elsewhere in FE -- proving FE never parses or branches on it here,
	// only passes it through.
	if gotBody["generator"] != "wiki_page::do-not-interpret-me" {
		t.Fatalf("expected generator forwarded unmodified, got %#v", gotBody["generator"])
	}
	fragmentBody, _ := gotBody["fragment"].(map[string]any)
	if fragmentBody["id"] != "fragment-123" {
		t.Fatalf("expected fragment id in callback body, got %#v", gotBody["fragment"])
	}
}

func TestCallbackDestinationExecutor_ServerErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	destination := domain.Destination{
		Name:       "curator-wake",
		Kind:       "callback",
		ConfigJSON: fmt.Sprintf(`{"target":%q,"generator":"wiki_page"}`, srv.URL),
	}

	_, err := CallbackDestinationExecutor{}.Execute(context.Background(), destination, testFragment(), nil)
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
	if !isRetryable(err) {
		t.Fatalf("expected 5xx callback failure to be marked retryable, got %v", err)
	}
}

func TestCallbackDestinationExecutor_MissingTargetOrGenerator(t *testing.T) {
	if _, err := (CallbackDestinationExecutor{}).Execute(context.Background(), domain.Destination{
		Name:       "no-target",
		Kind:       "callback",
		ConfigJSON: `{"generator":"wiki_page"}`,
	}, testFragment(), nil); err == nil || !strings.Contains(err.Error(), "missing target") {
		t.Fatalf("expected missing target error, got %v", err)
	}

	if _, err := (CallbackDestinationExecutor{}).Execute(context.Background(), domain.Destination{
		Name:       "no-generator",
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake"}`,
	}, testFragment(), nil); err == nil || !strings.Contains(err.Error(), "missing generator") {
		t.Fatalf("expected missing generator error, got %v", err)
	}
}

func TestCLIDestinationExecutor(t *testing.T) {
	tempDir := t.TempDir()
	capturePath := filepath.Join(tempDir, "cli-payload.json")

	destination := domain.Destination{
		Name: "cli-export",
		Kind: "cli",
		ConfigJSON: fmt.Sprintf(`{
			"command": %q,
			"args": ["-test.run=TestHelperProcessCLIDestination", "--", %q],
			"env": ["GO_WANT_HELPER_PROCESS=1"],
			"provider": "corpus_cli"
		}`, os.Args[0], capturePath),
	}

	ref, err := CLIDestinationExecutor{}.Execute(context.Background(), destination, testFragment(), nil)
	if err != nil {
		t.Fatalf("execute cli destination: %v", err)
	}
	if ref.Ref != "cli-ref:session-123" {
		t.Fatalf("unexpected cli ref: %s", ref.Ref)
	}

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read cli capture: %v", err)
	}
	var payload struct {
		Destination map[string]any `json:"destination"`
		Fragment    map[string]any `json:"fragment"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode cli payload: %v", err)
	}
	if payload.Destination["name"] != "cli-export" {
		t.Fatalf("unexpected destination payload: %#v", payload.Destination)
	}
	if payload.Fragment["source_id"] != "session-123" {
		t.Fatalf("unexpected fragment payload: %#v", payload.Fragment)
	}
	if markdown, _ := payload.Fragment["markdown"].(string); !strings.Contains(markdown, "# Claude session: roadmap-review") {
		t.Fatalf("expected markdown payload, got %q", markdown)
	}
}

func TestCLIDestinationExecutor_AnchorsRelativeWorkingDirToInstallDir(t *testing.T) {
	installDir := t.TempDir()
	prev := config.InstallDir()
	config.SetInstallDir(installDir)
	t.Cleanup(func() { config.SetInstallDir(prev) })

	workDir := filepath.Join(installDir, "work")
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	wantWD, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatalf("resolve work dir: %v", err)
	}

	// Run from a CWD that is deliberately not installDir; a relative
	// working_dir must still resolve under installDir, not this CWD.
	cwd := t.TempDir()
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWd) })

	capturePath := filepath.Join(installDir, "cli-payload.json")
	cwdCapturePath := filepath.Join(installDir, "cli-cwd.txt")

	destination := domain.Destination{
		Name: "cli-export",
		Kind: "cli",
		ConfigJSON: fmt.Sprintf(`{
			"command": %q,
			"args": ["-test.run=TestHelperProcessCLIDestination", "--", %q],
			"env": ["GO_WANT_HELPER_PROCESS=1", "CWD_CAPTURE_PATH=%s"],
			"working_dir": "./work",
			"provider": "corpus_cli"
		}`, os.Args[0], capturePath, cwdCapturePath),
	}

	if _, err := (CLIDestinationExecutor{}).Execute(context.Background(), destination, testFragment(), nil); err != nil {
		t.Fatalf("execute cli destination: %v", err)
	}

	gotWDRaw, err := os.ReadFile(cwdCapturePath)
	if err != nil {
		t.Fatalf("read cwd capture: %v", err)
	}
	gotWD, err := filepath.EvalSymlinks(strings.TrimSpace(string(gotWDRaw)))
	if err != nil {
		t.Fatalf("resolve captured cwd: %v", err)
	}
	if gotWD != wantWD {
		t.Fatalf("relative working_dir not anchored to install dir: got %s, want %s", gotWD, wantWD)
	}
}

func TestFileDestinationExecutor_AnchorsRelativeRootToInstallDir(t *testing.T) {
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

	destination := domain.Destination{
		Name:       "corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"./corpus"}`,
	}
	fragment := testFragment()
	fragment.CanonicalPath = "fragments/manual/note/abc"

	written, err := FileDestinationExecutor{}.Execute(context.Background(), destination, fragment, nil)
	if err != nil {
		t.Fatalf("execute file destination: %v", err)
	}
	want := filepath.Join(installDir, "corpus", "fragments", "manual", "note", "abc", "fragment.md")
	if written.Ref != want {
		t.Fatalf("relative root not anchored to install dir: got %s, want %s", written.Ref, want)
	}
	if _, err := os.Stat(filepath.Join(cwd, "corpus")); !os.IsNotExist(err) {
		t.Fatalf("expected no corpus dir under CWD, got err=%v", err)
	}
}

func TestFileDestinationExecutor_PublishesBundleAndLocalAttachments(t *testing.T) {
	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "diagram.png")
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	if err := os.WriteFile(sourcePath, rawPNG, 0o600); err != nil {
		t.Fatalf("write source attachment: %v", err)
	}

	destination := domain.Destination{
		Name:       "corpus",
		Kind:       "file",
		ConfigJSON: fmt.Sprintf(`{"root":%q}`, filepath.Join(tempDir, "corpus")),
	}
	fragment := testFragment()
	fragment.CanonicalPath = "fragments/chats/chatgpt/2024-04-26/conv-123"
	attachments := []domain.FragmentAttachment{
		{
			ID:         "local-attachment",
			Kind:       "image",
			Role:       "attachment",
			Name:       "diagram.png",
			MIMEType:   "image/png",
			SourcePath: sourcePath,
		},
		{
			ID:          "remote-reference",
			Kind:        "pdf",
			Role:        "reference",
			Name:        "roadmap.pdf",
			ExternalURL: "https://example.com/roadmap.pdf",
		},
	}

	written, err := FileDestinationExecutor{}.Execute(context.Background(), destination, fragment, attachments)
	if err != nil {
		t.Fatalf("execute file destination: %v", err)
	}
	expectedFragmentPath := filepath.Join(tempDir, "corpus", "fragments", "chats", "chatgpt", "2024-04-26", "conv-123", "fragment.md")
	if written.Ref != expectedFragmentPath {
		t.Fatalf("unexpected fragment ref: %s", written.Ref)
	}
	if len(written.PublishedAttachments) != 1 {
		t.Fatalf("expected 1 published attachment, got %d", len(written.PublishedAttachments))
	}
	published := written.PublishedAttachments["local-attachment"]
	if !strings.Contains(published.StoragePath, string(filepath.Separator)+"attachments"+string(filepath.Separator)) {
		t.Fatalf("expected attachment to be published into attachments dir: %s", published.StoragePath)
	}
	raw, err := os.ReadFile(expectedFragmentPath)
	if err != nil {
		t.Fatalf("read fragment bundle file: %v", err)
	}
	if !strings.Contains(string(raw), "# Claude session: roadmap-review") {
		t.Fatalf("unexpected fragment markdown: %s", string(raw))
	}
	copied, err := os.ReadFile(published.StoragePath)
	if err != nil {
		t.Fatalf("read copied attachment: %v", err)
	}
	if string(copied) != string(rawPNG) {
		t.Fatalf("unexpected copied attachment content")
	}
	if published.PreviewStoragePath == "" {
		t.Fatal("expected image preview path to be generated")
	}
	if _, err := os.Stat(published.PreviewStoragePath); err != nil {
		t.Fatalf("expected image preview file: %v", err)
	}
}

func TestFileDestinationExecutor_FFSPinterestPinBundle(t *testing.T) {
	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "pin.jpg")
	previewPath := filepath.Join(tempDir, "pin.preview.jpg")
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	if err := os.WriteFile(sourcePath, rawPNG, 0o600); err != nil {
		t.Fatalf("write source image: %v", err)
	}
	if err := os.WriteFile(previewPath, rawPNG, 0o600); err != nil {
		t.Fatalf("write preview image: %v", err)
	}

	destination := domain.Destination{
		Name:       "ffs-pins",
		Kind:       "file",
		ConfigJSON: fmt.Sprintf(`{"root":%q,"provider":"ffs"}`, filepath.Join(tempDir, "ffs", "media", "pins")),
	}
	fragment := domain.Fragment{
		ID:            "pin-fragment",
		Source:        "manual",
		SourceType:    "pin",
		SourceID:      "source-pin",
		Title:         "Warm minimal office desk",
		Content:       "https://www.pinterest.com/pin/123456/",
		Status:        domain.FragmentStatusRouted,
		CreatedAt:     time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC),
		IngestedAt:    time.Date(2026, 5, 24, 10, 1, 0, 0, time.UTC),
		IngestName:    "manual-intake",
		CanonicalPath: "fragments/manual/pin/pinterest/123456",
		Summary:       "Workspace inspiration pin.",
		MetadataJSON:  `{"platform":"pinterest","pin_id":"123456","url":"https://www.pinterest.com/pin/123456/","pin_description":"Workspace inspiration pin.","user_tags":["workspace","wood"]}`,
	}
	attachments := []domain.FragmentAttachment{{
		ID:                 "image-attachment",
		Kind:               "image",
		Role:               "reference",
		Name:               "123456.jpg",
		MIMEType:           "image/jpeg",
		SourcePath:         sourcePath,
		StoragePath:        sourcePath,
		PreviewStoragePath: previewPath,
	}}

	written, err := FileDestinationExecutor{}.Execute(context.Background(), destination, fragment, attachments)
	if err != nil {
		t.Fatalf("execute ffs destination: %v", err)
	}
	expectedFragmentPath := filepath.Join(tempDir, "ffs", "media", "pins", "pinterest", "123456", "fragment.md")
	if written.Ref != expectedFragmentPath {
		t.Fatalf("unexpected ffs fragment path: %s", written.Ref)
	}
	if len(written.PublishedAttachments) != 1 {
		t.Fatalf("expected 1 published attachment, got %d", len(written.PublishedAttachments))
	}
	raw, err := os.ReadFile(expectedFragmentPath)
	if err != nil {
		t.Fatalf("read ffs fragment: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "# Warm minimal office desk") {
		t.Fatalf("expected title in ffs markdown: %s", body)
	}
	if !strings.Contains(body, "attachments/previews/") {
		t.Fatalf("expected preview link in ffs markdown: %s", body)
	}
	if !strings.Contains(body, "- workspace") || !strings.Contains(body, "- wood") {
		t.Fatalf("expected tags in ffs markdown: %s", body)
	}
}

func TestFileDestinationExecutor_PathTemplate(t *testing.T) {
	tempDir := t.TempDir()
	destination := domain.Destination{
		Name:       "templated-file",
		Kind:       "file",
		ConfigJSON: fmt.Sprintf(`{"root":%q,"path_template":"docs/references/{platform}/{ref_name}"}`, filepath.Join(tempDir, "ffs")),
	}
	fragment := testFragment()
	fragment.MetadataJSON = `{"platform":"github"}`

	written, err := FileDestinationExecutor{}.Execute(context.Background(), destination, fragment, nil)
	if err != nil {
		t.Fatalf("execute templated file destination: %v", err)
	}
	expected := filepath.Join(tempDir, "ffs", "docs", "references", "github", "claude-session-roadmap-review", "fragment.md")
	if written.Ref != expected {
		t.Fatalf("unexpected templated fragment path: %s", written.Ref)
	}
}

func TestHelperProcessMCPServer(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	capturePath := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			capturePath = os.Args[i+1]
			break
		}
	}
	if capturePath == "" {
		os.Exit(2)
	}

	type request struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      *mcp.RequestId  `json:"id,omitempty"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	type response struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      *mcp.RequestId `json:"id,omitempty"`
		Result  map[string]any `json:"result,omitempty"`
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		rsp := response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}
		switch req.Method {
		case "initialize":
			rsp.Result = map[string]any{
				"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
				"serverInfo": map[string]any{
					"name":    "fe-test-mcp",
					"version": "1.0.0",
				},
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
			}
		case "notifications/initialized":
			continue
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err == nil {
				raw, _ := json.Marshal(params)
				_ = os.WriteFile(capturePath, raw, 0o600)
			}
			rsp.Result = map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
			}
		default:
			rsp.Result = map[string]any{}
		}

		out, _ := json.Marshal(rsp)
		fmt.Fprintf(os.Stdout, "%s\n", out)
	}
}

func TestHelperProcessCLIDestination(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	capturePath := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			capturePath = os.Args[i+1]
			break
		}
	}
	if capturePath == "" {
		os.Exit(2)
	}
	if cwdCapturePath := os.Getenv("CWD_CAPTURE_PATH"); cwdCapturePath != "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		if err := os.WriteFile(cwdCapturePath, []byte(wd), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if err := os.WriteFile(capturePath, raw, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "cli-ref:session-123")
	os.Exit(0)
}

func testFragment() domain.Fragment {
	return domain.Fragment{
		ID:            "fragment-123",
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      "session-123",
		Title:         "Claude session: roadmap-review",
		Content:       "The roadmap starts with deterministic ingest and search.",
		CreatedAt:     time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC),
		IngestedAt:    time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC),
		Status:        domain.FragmentStatusRouted,
		Summary:       "Roadmap fragment about deterministic ingest and recall.",
		IngestName:    "claude-default",
		CanonicalPath: "fragments/chats/claude/2026-04-25/session-123",
	}
}
