package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// TestMCPDestinationExecutor_HTTPTransport_TangentHITL proves the "http"
// transport (Streamable HTTP, for a long-running peer like Tangent) reaches
// a real MCP server over the network -- not a subprocess FE spawns -- and
// that the "tangent_hitl" provider builds a well-formed
// tangent.hitl_enqueue call from a fragment.
func TestMCPDestinationExecutor_HTTPTransport_TangentHITL(t *testing.T) {
	var captured struct {
		Name      string
		Arguments map[string]any
	}

	mcpSrv := mcpserver.NewMCPServer("fake-tangent", "0.0.1")
	mcpSrv.AddTool(
		mcp.NewTool("tangent.hitl_enqueue"),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			captured.Name = req.Params.Name
			raw, _ := json.Marshal(req.Params.Arguments)
			_ = json.Unmarshal(raw, &captured.Arguments)
			return mcp.NewToolResultText(`{"item_id":"interaction_fake","item_url":"/hitl/items/interaction_fake"}`), nil
		},
	)
	httpSrv := mcpserver.NewTestStreamableHTTPServer(mcpSrv)
	defer httpSrv.Close()

	destination := domain.Destination{
		Name: "tangent-hitl",
		Kind: "mcp",
		ConfigJSON: fmt.Sprintf(`{
			"transport": "http",
			"base_url": %q,
			"provider": "tangent_hitl",
			"tangent_hitl": {
				"application_id": "fragments-engine",
				"agent_id": "fe-router"
			}
		}`, httpSrv.URL),
	}

	written, err := MCPDestinationExecutor{}.Execute(context.Background(), destination, testFragment(), nil)
	if err != nil {
		t.Fatalf("execute mcp http destination: %v", err)
	}
	if !strings.HasPrefix(written.Ref, "mcp:tangent.hitl_enqueue:") {
		t.Fatalf("unexpected written ref: %s", written.Ref)
	}

	if captured.Name != "tangent.hitl_enqueue" {
		t.Fatalf("unexpected tool name: %s", captured.Name)
	}
	if captured.Arguments["kind"] != "attention" {
		t.Fatalf("expected kind=attention, got %#v", captured.Arguments["kind"])
	}
	if captured.Arguments["idempotency_key"] != "fragments-engine:fragment-123" {
		t.Fatalf("expected idempotency key derived from fragment id, got %#v", captured.Arguments["idempotency_key"])
	}
	if captured.Arguments["title"] != "Claude session: roadmap-review" {
		t.Fatalf("unexpected title: %#v", captured.Arguments["title"])
	}
	source, _ := captured.Arguments["source"].(map[string]any)
	if source["application_id"] != "fragments-engine" || source["agent_id"] != "fe-router" {
		t.Fatalf("unexpected source assertion: %#v", source)
	}
	detailsMarkdown, _ := captured.Arguments["details_markdown"].(string)
	if !strings.Contains(detailsMarkdown, "deterministic ingest and search") {
		t.Fatalf("expected fragment content in details_markdown: %s", detailsMarkdown)
	}
}

// TestMCPDestinationExecutor_HTTPTransport_GenericProviderTemplatedArguments
// is the direct proof for CW-20260917-0003: a brand-new MCP integration --
// here targeting a hypothetical "tesseract.capture" tool no Go code knows
// about -- reaches a real MCP server with fragment-derived arguments
// (a rendered title, the markdown func, a nested map, a templated slice
// element) using nothing but destination config. No new provider function,
// no new switch case.
func TestMCPDestinationExecutor_HTTPTransport_GenericProviderTemplatedArguments(t *testing.T) {
	var captured struct {
		Name      string
		Arguments map[string]any
	}

	mcpSrv := mcpserver.NewMCPServer("fake-tesseract", "0.0.1")
	mcpSrv.AddTool(
		mcp.NewTool("tesseract.capture"),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			captured.Name = req.Params.Name
			raw, _ := json.Marshal(req.Params.Arguments)
			_ = json.Unmarshal(raw, &captured.Arguments)
			return mcp.NewToolResultText(`{"ok":true}`), nil
		},
	)
	httpSrv := mcpserver.NewTestStreamableHTTPServer(mcpSrv)
	defer httpSrv.Close()

	destination := domain.Destination{
		Name: "tesseract-capture",
		Kind: "mcp",
		ConfigJSON: fmt.Sprintf(`{
			"transport": "http",
			"base_url": %q,
			"provider": "tesseract_capture",
			"tool": "tesseract.capture",
			"arguments": {
				"title": "{{.Fragment.Title | truncate 10}}",
				"body": "{{markdown .Fragment}}",
				"source": {"application_id": "fragments-engine", "fragment_id": "{{.Fragment.ID}}"},
				"tags": ["fe-routed", "{{.Fragment.SourceType}}"]
			}
		}`, httpSrv.URL),
	}

	fragment := testFragment()
	written, err := MCPDestinationExecutor{}.Execute(context.Background(), destination, fragment, nil)
	if err != nil {
		t.Fatalf("execute mcp http destination: %v", err)
	}
	if !strings.HasPrefix(written.Ref, "mcp:tesseract.capture:") {
		t.Fatalf("unexpected written ref: %s", written.Ref)
	}

	if captured.Name != "tesseract.capture" {
		t.Fatalf("unexpected tool name: %s", captured.Name)
	}
	if captured.Arguments["title"] != "Claude ses" {
		t.Fatalf("expected truncated title, got %#v", captured.Arguments["title"])
	}
	body, _ := captured.Arguments["body"].(string)
	if body != renderFragmentMarkdown(fragment) {
		t.Fatalf("expected body to match renderFragmentMarkdown output, got %q", body)
	}
	source, _ := captured.Arguments["source"].(map[string]any)
	if source["fragment_id"] != fragment.ID {
		t.Fatalf("expected nested map field rendered, got %#v", source)
	}
	tags, _ := captured.Arguments["tags"].([]any)
	if len(tags) != 2 || tags[1] != fragment.SourceType {
		t.Fatalf("expected slice element rendered, got %#v", tags)
	}
}

func TestMCPDestinationExecutor_HTTPTransport_MissingBaseURL(t *testing.T) {
	destination := domain.Destination{
		Name:       "tangent-hitl",
		Kind:       "mcp",
		ConfigJSON: `{"transport":"http","provider":"tangent_hitl"}`,
	}
	_, err := MCPDestinationExecutor{}.Execute(context.Background(), destination, testFragment(), nil)
	if err == nil || !strings.Contains(err.Error(), "missing base_url") {
		t.Fatalf("expected missing base_url error, got %v", err)
	}
}
