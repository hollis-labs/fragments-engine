package ingest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type ProbeResult struct {
	Reachable bool
	Message   string
}

func ProbeDestination(ctx context.Context, destination domain.Destination) (ProbeResult, error) {
	switch destination.Kind {
	case "file":
		return probeFileDestination(destination)
	case "mcp":
		return probeMCPDestination(ctx, destination)
	case "api":
		return probeAPIDestination(ctx, destination)
	case "cli":
		return probeCLIDestination(destination)
	case "callback":
		return probeCallbackDestination(destination)
	default:
		return ProbeResult{}, fmt.Errorf("unsupported destination kind %q", destination.Kind)
	}
}

func probeFileDestination(destination domain.Destination) (ProbeResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](destination)
	if err != nil {
		return ProbeResult{}, err
	}
	root := config.AnchorPath(config.InstallDir(), config.ExpandHome(cfg.Root))
	if strings.TrimSpace(root) == "" {
		return ProbeResult{}, fmt.Errorf("file destination %q missing root", destination.Name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("resolve destination root: %w", err)
	}
	parent := filepath.Dir(rootAbs)
	info, err := os.Stat(parent)
	if err != nil {
		return ProbeResult{Reachable: false, Message: "parent_missing:" + parent}, nil
	}
	if !info.IsDir() {
		return ProbeResult{Reachable: false, Message: "parent_not_dir:" + parent}, nil
	}
	return ProbeResult{Reachable: true, Message: "filesystem_ready:" + rootAbs}, nil
}

func probeMCPDestination(ctx context.Context, destination domain.Destination) (ProbeResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](destination)
	if err != nil {
		return ProbeResult{}, err
	}
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return ProbeResult{}, fmt.Errorf("mcp destination %q missing command", destination.Name)
	}
	if strings.ContainsRune(command, filepath.Separator) {
		if _, err := os.Stat(command); err != nil {
			return ProbeResult{Reachable: false, Message: "command_missing:" + command}, nil
		}
	} else {
		path, err := exec.LookPath(command)
		if err != nil {
			return ProbeResult{Reachable: false, Message: "command_not_found:" + command}, nil
		}
		command = path
	}

	timeout := time.Duration(max(cfg.TimeoutSeconds, 30)) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, minDuration(timeout, 5*time.Second))
	defer cancel()

	client, err := mcpclient.NewStdioMCPClient(command, cfg.Env, cfg.Args...)
	if err != nil {
		return ProbeResult{Reachable: false, Message: "start_failed:" + err.Error()}, nil
	}
	defer client.Close()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "fragments-engine", Version: "0.1.0"}
	if _, err := client.Initialize(callCtx, initReq); err != nil {
		return ProbeResult{Reachable: false, Message: "initialize_failed:" + err.Error()}, nil
	}
	return ProbeResult{Reachable: true, Message: "mcp_ready:" + command}, nil
}

func probeAPIDestination(ctx context.Context, destination domain.Destination) (ProbeResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](destination)
	if err != nil {
		return ProbeResult{}, err
	}
	baseURL := strings.TrimRight(expandConfigValue(cfg.BaseURL), "/")
	if baseURL == "" {
		return ProbeResult{}, fmt.Errorf("api destination %q missing base_url", destination.Name)
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("parse base_url: %w", err)
	}
	timeout := time.Duration(max(cfg.TimeoutSeconds, 30)) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, minDuration(timeout, 5*time.Second))
	defer cancel()

	hostPort := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "https":
			hostPort = net.JoinHostPort(u.Hostname(), "443")
		default:
			hostPort = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	conn, err := (&net.Dialer{}).DialContext(callCtx, "tcp", hostPort)
	if err != nil {
		return ProbeResult{Reachable: false, Message: "tcp_unreachable:" + err.Error()}, nil
	}
	_ = conn.Close()

	if cfg.Provider == "nanite_messaging" || cfg.Provider == "nanite_user_mailbox" {
		healthReq, err := http.NewRequestWithContext(callCtx, http.MethodGet, baseURL+"/api/health", nil)
		if err != nil {
			return ProbeResult{Reachable: true, Message: "tcp_ready:" + hostPort}, nil
		}
		resp, err := http.DefaultClient.Do(healthReq)
		if err != nil {
			return ProbeResult{Reachable: false, Message: "health_check_failed:" + err.Error()}, nil
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return ProbeResult{Reachable: false, Message: fmt.Sprintf("health_status:%d", resp.StatusCode)}, nil
		}
		return ProbeResult{Reachable: true, Message: fmt.Sprintf("api_ready:%s/api/health", baseURL)}, nil
	}

	return ProbeResult{Reachable: true, Message: "tcp_ready:" + hostPort}, nil
}

func probeCLIDestination(destination domain.Destination) (ProbeResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](destination)
	if err != nil {
		return ProbeResult{}, err
	}
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return ProbeResult{}, fmt.Errorf("cli destination %q missing command", destination.Name)
	}
	if strings.ContainsRune(command, filepath.Separator) {
		if _, err := os.Stat(command); err != nil {
			return ProbeResult{Reachable: false, Message: "command_missing:" + command}, nil
		}
	} else {
		path, err := exec.LookPath(command)
		if err != nil {
			return ProbeResult{Reachable: false, Message: "command_not_found:" + command}, nil
		}
		command = path
	}
	if wd := strings.TrimSpace(expandConfigValue(cfg.WorkingDir)); wd != "" {
		wd = config.AnchorPath(config.InstallDir(), config.ExpandHome(wd))
		info, err := os.Stat(wd)
		if err != nil {
			return ProbeResult{Reachable: false, Message: "working_dir_missing:" + wd}, nil
		}
		if !info.IsDir() {
			return ProbeResult{Reachable: false, Message: "working_dir_not_dir:" + wd}, nil
		}
	}
	return ProbeResult{Reachable: true, Message: "cli_ready:" + command}, nil
}

// probeCallbackDestination deliberately performs config-only validation
// instead of a live reachability check. Callback delivery is always async
// (it is only ever fired from the queue drainer, never inline from the
// routing hot path), so probing it with a live network call against
// Curator's Nanite wake endpoint would defeat the point of never doing
// synchronous I/O against that endpoint from FE. Unlike
// probeAPIDestination/probeMCPDestination, this never dials out.
func probeCallbackDestination(destination domain.Destination) (ProbeResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.CallbackDestinationConfig](destination)
	if err != nil {
		return ProbeResult{}, err
	}
	target := strings.TrimSpace(cfg.Target)
	if target == "" {
		return ProbeResult{}, fmt.Errorf("callback destination %q missing target", destination.Name)
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ProbeResult{Reachable: false, Message: "invalid_target:" + target}, nil
	}
	if strings.TrimSpace(cfg.Generator) == "" {
		return ProbeResult{Reachable: false, Message: "missing_generator"}, nil
	}
	return ProbeResult{Reachable: true, Message: "config_valid:" + target}, nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
