package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// AnthropicDirectProvider is the allow-list id for calling Anthropic's Messages
// API directly. It must be listed in transcripts.enrichment.allowed_providers.
const AnthropicDirectProvider = "anthropic-direct"

// Pricing is USD per million tokens, taken from configuration. Nothing here is
// built in: the numbers must be confirmed against Anthropic's published pricing
// before the first live call, and a run refuses to start without them because
// the spend caps are computed from them.
type Pricing struct {
	InputPerMTok, OutputPerMTok, CacheReadPerMTok, CacheWritePerMTok float64
	// LongThreshold and Long model prompt-length tiers (Claude Haiku 5.5 charges
	// higher prices for a request whose prompt is over 100,000 tokens). A
	// request's prompt length is its input tokens plus cache reads and writes,
	// and each request is priced on its own. Zero means a single tier.
	LongThreshold int
	Long          *Pricing
}

// Configured reports whether the two prices every call needs are present, and
// that a configured long-prompt tier is complete too.
func (p Pricing) Configured() bool {
	if !(p.InputPerMTok > 0 && p.OutputPerMTok > 0) {
		return false
	}
	if p.LongThreshold > 0 {
		return p.Long != nil && p.Long.InputPerMTok > 0 && p.Long.OutputPerMTok > 0
	}
	return true
}

// Cost prices one call's tokens, choosing the tier by the call's prompt length.
func (p Pricing) Cost(input, output, cacheRead, cacheWrite int) float64 {
	tier := p
	if p.LongThreshold > 0 && p.Long != nil && input+cacheRead+cacheWrite > p.LongThreshold {
		tier = *p.Long
	}
	return (float64(input)*tier.InputPerMTok + float64(output)*tier.OutputPerMTok +
		float64(cacheRead)*tier.CacheReadPerMTok + float64(cacheWrite)*tier.CacheWritePerMTok) / 1e6
}

// AnthropicGateway calls POST /v1/messages. The API key is read from the
// environment variable named in configuration, once at construction; it is
// never logged, stored, or included in an error.
type AnthropicGateway struct {
	client  *http.Client
	base    string
	key     string
	version string
	pricing Pricing
	// thinking is "disabled" (the default: summaries are a bounded JSON task and
	// thinking tokens would count toward max_tokens) or "adaptive". effort, when
	// set, is sent as output_config.effort.
	thinking string
	effort   string
}

// AnthropicOptions are the request-shaping settings.
type AnthropicOptions struct {
	Thinking string // "", "disabled" or "adaptive"
	Effort   string // "", low, medium, high, xhigh or max
}

const defaultAnthropicVersion = "2023-06-01"

// NewAnthropicGateway builds the client. getenv is os.Getenv in production.
// The base URL must be https (plain http is accepted only on loopback, for
// tests), so transcript text is never sent in the clear.
func NewAnthropicGateway(baseURL, apiKeyEnv, version string, pricing Pricing, opt AnthropicOptions, getenv func(string) string) (*AnthropicGateway, error) {
	if strings.TrimSpace(apiKeyEnv) == "" {
		return nil, errors.New("anthropic: transcripts.enrichment.anthropic.api_key_env is not set")
	}
	key := strings.TrimSpace(getenv(apiKeyEnv))
	if key == "" {
		return nil, fmt.Errorf("anthropic: environment variable %s is empty or unset", apiKeyEnv)
	}
	if !pricing.Configured() {
		return nil, errors.New("anthropic: transcripts.enrichment.anthropic.pricing (input_per_mtok and output_per_mtok) is required: spend caps are computed from it, and the values must match Anthropic's published pricing")
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("anthropic: invalid base_url")
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("anthropic: base_url must be https")
	}
	if version == "" {
		version = defaultAnthropicVersion
	}
	thinking := opt.Thinking
	if thinking == "" {
		thinking = "disabled"
	}
	if thinking != "disabled" && thinking != "adaptive" {
		return nil, errors.New("anthropic: thinking must be disabled or adaptive")
	}
	switch opt.Effort {
	case "", "low", "medium", "high":
	case "xhigh", "max":
		if thinking == "disabled" {
			return nil, errors.New("anthropic: effort xhigh/max requires thinking: adaptive (the API rejects disabled thinking there)")
		}
	default:
		return nil, errors.New("anthropic: unknown effort level")
	}
	return &AnthropicGateway{client: &http.Client{Timeout: 180 * time.Second}, base: strings.TrimRight(baseURL, "/"), key: key, version: version,
		pricing: pricing, thinking: thinking, effort: opt.Effort}, nil
}

var errType = regexp.MustCompile(`^[a-z_]{1,40}$`)

func (g *AnthropicGateway) Chat(ctx context.Context, r Request) (Response, error) {
	payload := map[string]any{
		"model": r.Model, "max_tokens": r.MaxOutputTokens, "system": r.System,
		"messages": []map[string]any{{"role": "user", "content": r.User}},
	}
	if g.thinking == "disabled" {
		payload["thinking"] = map[string]string{"type": "disabled"}
	}
	if g.effort != "" {
		payload["output_config"] = map[string]string{"effort": g.effort}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, &GatewayError{Class: "encode"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Response{}, &GatewayError{Class: "request"}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", g.key)
	req.Header.Set("anthropic-version", g.version)
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
	if resp.StatusCode != http.StatusOK {
		return Response{}, classifyAnthropic(resp.StatusCode, data)
	}
	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			In         int `json:"input_tokens"`
			Out        int `json:"output_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &out) != nil {
		return Response{}, &GatewayError{Class: "bad_response"}
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return Response{
		Text: text.String(), Provider: AnthropicDirectProvider, Model: out.Model,
		InputTokens: out.Usage.In, OutputTokens: out.Usage.Out, CacheReadTokens: out.Usage.CacheRead, CacheWriteTokens: out.Usage.CacheWrite,
		CostUSD: g.pricing.Cost(out.Usage.In, out.Usage.Out, out.Usage.CacheRead, out.Usage.CacheWrite),
	}, nil
}

// classifyAnthropic turns an error response into a class. The error type is a
// fixed vocabulary (for example rate_limit_error); the message is dropped
// because it can echo request content.
func classifyAnthropic(status int, body []byte) error {
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	suffix := ""
	if errType.MatchString(e.Error.Type) {
		suffix = ":" + e.Error.Type
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &GatewayError{Class: "unauthorized"}
	case status == http.StatusTooManyRequests || status == 529 || status >= 500:
		return &GatewayError{Class: fmt.Sprintf("http_%d%s", status, suffix), Retryable: true}
	default:
		return &GatewayError{Class: fmt.Sprintf("http_%d%s", status, suffix)}
	}
}
