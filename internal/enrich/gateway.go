package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Request is one chat call. Provider and Model are hints to the gateway.
type Request struct {
	Provider, Model, System, User  string
	MaxOutputTokens                int
	CallerID, RequestID, SessionID string
}

// Response is the model's text plus what the gateway reports about the call.
type Response struct {
	Text                      string
	Provider, Model           string
	InputTokens, OutputTokens int
	CostUSD                   float64
}

// Gateway is the only way enrichment reaches a model.
type Gateway interface {
	Chat(ctx context.Context, req Request) (Response, error)
}

// GatewayError carries a class and whether a retry may help; never a body.
type GatewayError struct {
	Class     string
	Retryable bool
}

func (e *GatewayError) Error() string { return "gateway: " + e.Class }

// HTTPGateway speaks to tetherd's POST /ai/chat, over a Unix socket
// ("unix:/path") or a loopback URL.
type HTTPGateway struct {
	client *http.Client
	base   string
	token  string
}

func NewHTTPGateway(address, token string) (*HTTPGateway, error) {
	address = strings.TrimSpace(address)
	g := &HTTPGateway{token: token}
	switch {
	case strings.HasPrefix(address, "unix:"):
		path := strings.TrimPrefix(address, "unix:")
		if path == "" {
			return nil, errors.New("enrichment gateway: empty unix socket path")
		}
		g.base = "http://unix"
		g.client = &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		}}
	case strings.HasPrefix(address, "http://127.0.0.1:") || strings.HasPrefix(address, "http://localhost:") || strings.HasPrefix(address, "http://[::1]:"):
		g.base = strings.TrimRight(address, "/")
		g.client = &http.Client{Timeout: 180 * time.Second}
	default:
		return nil, errors.New("enrichment gateway: address must be unix:/path or a loopback http URL")
	}
	return g, nil
}

type part struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type message struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

func (g *HTTPGateway) Chat(ctx context.Context, r Request) (Response, error) {
	body := map[string]any{"request": map[string]any{
		"operation": "chat", "provider_hint": r.Provider, "model_hint": r.Model,
		"mode": "summarize", "intent": "transcript-enrichment",
		"request_id": r.RequestID, "session_id": r.SessionID, "caller_id": r.CallerID,
		"max_output_tokens": r.MaxOutputTokens,
		"input": []message{
			{Role: "system", Parts: []part{{Type: "text", Text: r.System}}},
			{Role: "user", Parts: []part{{Type: "text", Text: r.User}}},
		},
	}}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, &GatewayError{Class: "encode"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+"/ai/chat", bytes.NewReader(raw))
	if err != nil {
		return Response{}, &GatewayError{Class: "request"}
	}
	req.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, &GatewayError{Class: "unreachable", Retryable: true}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, &GatewayError{Class: "read", Retryable: true}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Response{}, &GatewayError{Class: "unauthorized"}
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return Response{}, &GatewayError{Class: fmt.Sprintf("http_%d", resp.StatusCode), Retryable: true}
	case resp.StatusCode != http.StatusOK:
		return Response{}, &GatewayError{Class: fmt.Sprintf("http_%d", resp.StatusCode)}
	}
	var out struct {
		Response struct {
			Provider string    `json:"provider"`
			Model    string    `json:"model"`
			Output   []message `json:"output"`
			Usage    struct {
				In   int     `json:"input_tokens"`
				Out  int     `json:"output_tokens"`
				Cost float64 `json:"estimated_cost_usd"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &out) != nil {
		return Response{}, &GatewayError{Class: "bad_response"}
	}
	var text strings.Builder
	for _, m := range out.Response.Output {
		if m.Role != "assistant" {
			continue
		}
		for _, p := range m.Parts {
			if p.Type == "text" {
				text.WriteString(p.Text)
			}
		}
	}
	return Response{Text: text.String(), Provider: out.Response.Provider, Model: out.Response.Model,
		InputTokens: out.Response.Usage.In, OutputTokens: out.Response.Usage.Out, CostUSD: out.Response.Usage.Cost}, nil
}
