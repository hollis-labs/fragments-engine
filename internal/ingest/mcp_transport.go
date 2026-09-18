package ingest

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpDestinationTransport builds the client transport for an MCP
// destination's configured transport kind ("" / "stdio" or "http"). The
// returned error's text is the bare complaint ("missing command", ...) --
// callers prefix it with the destination name themselves, since writer and
// probe error-wrap slightly differently.
func mcpDestinationTransport(cfg domain.MCPDestinationConfig) (mcpsdk.Transport, error) {
	switch transport := strings.TrimSpace(cfg.Transport); transport {
	case "", "stdio":
		command := strings.TrimSpace(cfg.Command)
		if command == "" {
			return nil, fmt.Errorf("missing command")
		}
		cmd := exec.Command(command, cfg.Args...)
		if len(cfg.Env) > 0 {
			cmd.Env = append(os.Environ(), cfg.Env...)
		}
		return &mcpsdk.CommandTransport{Command: cmd}, nil
	case "http":
		// Streamable HTTP: a long-running peer's own MCP server (e.g.
		// Tangent on :7842), not a subprocess FE spawns.
		baseURL := strings.TrimSpace(cfg.BaseURL)
		if baseURL == "" {
			return nil, fmt.Errorf("missing base_url for http transport")
		}
		return &mcpsdk.StreamableClientTransport{Endpoint: baseURL}, nil
	default:
		return nil, fmt.Errorf("unsupported mcp transport %q", transport)
	}
}
