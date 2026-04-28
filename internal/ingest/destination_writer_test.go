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
