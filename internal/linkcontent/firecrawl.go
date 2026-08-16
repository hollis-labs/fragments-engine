package linkcontent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

const (
	firecrawlBackendName      = "firecrawl"
	defaultFirecrawlBaseURL   = "https://api.firecrawl.dev"
	defaultFirecrawlAPIKeyEnv = "FIRECRAWL_API_KEY"
	defaultFirecrawlTimeout   = 20 * time.Second
	// firecrawlScrapePath targets Firecrawl's current (v2) scrape endpoint. Verified
	// against https://docs.firecrawl.dev/api-reference/endpoint/scrape on 2026-08-16:
	// POST {base_url}/v2/scrape, auth via "Authorization: Bearer <api_key>", request body
	// {"url": "...", "formats": ["markdown"]}, response body
	// {"success": bool, "data": {"markdown": "...", "metadata": {"title", "description",
	// "sourceURL"/"url", "statusCode", "contentType", ...}}, "error": "..."}. Firecrawl's
	// v1 endpoint has been superseded by v2; the request/response shape above (not v1's)
	// is what this provider implements.
	firecrawlScrapePath = "/v2/scrape"
)

// MissingAPIKeyError is returned by a Provider's Fetch when the configured API key
// environment variable is unset or empty, so callers get a clear, typed failure instead
// of a generic HTTP error or a silent no-op.
type MissingAPIKeyError struct {
	Backend string
	EnvVar  string
}

func (e *MissingAPIKeyError) Error() string {
	return fmt.Sprintf("linkcontent: %s backend requires API key in environment variable %q, but it is unset or empty", e.Backend, e.EnvVar)
}

// firecrawlProvider implements Provider by calling Firecrawl's scrape API
// (https://docs.firecrawl.dev). It maps the response's markdown body and metadata
// (title/description/sourceURL/statusCode) into the shared Content struct, reusing
// PreviewText for the summary when Firecrawl does not return a meta description.
type firecrawlProvider struct {
	client    *http.Client
	baseURL   string
	apiKeyEnv string
	apiKey    string
}

// NewFirecrawlProvider builds the "firecrawl" Provider from config.LinkContentFirecrawlConfig.
// The API key is read once, from the configured environment variable (default
// FIRECRAWL_API_KEY), at construction time; Fetch fails closed with a *MissingAPIKeyError
// if it was empty rather than silently skipping the request.
func NewFirecrawlProvider(cfg config.LinkContentFirecrawlConfig) Provider {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultFirecrawlBaseURL
	}
	apiKeyEnv := strings.TrimSpace(cfg.APIKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = defaultFirecrawlAPIKeyEnv
	}
	timeout := defaultFirecrawlTimeout
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return &firecrawlProvider{
		client:    &http.Client{Timeout: timeout},
		baseURL:   baseURL,
		apiKeyEnv: apiKeyEnv,
		apiKey:    strings.TrimSpace(os.Getenv(apiKeyEnv)),
	}
}

func (p *firecrawlProvider) Backend() string {
	return firecrawlBackendName
}

type firecrawlScrapeRequest struct {
	URL     string   `json:"url"`
	Formats []string `json:"formats,omitempty"`
}

type firecrawlScrapeResponse struct {
	Success bool                `json:"success"`
	Error   string              `json:"error"`
	Data    firecrawlScrapeData `json:"data"`
}

type firecrawlScrapeData struct {
	Markdown string         `json:"markdown"`
	Metadata map[string]any `json:"metadata"`
}

func (p *firecrawlProvider) Fetch(ctx context.Context, rawURL string) (Content, error) {
	if p.apiKey == "" {
		return Content{}, &MissingAPIKeyError{Backend: firecrawlBackendName, EnvVar: p.apiKeyEnv}
	}

	payload, err := json.Marshal(firecrawlScrapeRequest{URL: rawURL, Formats: []string{"markdown"}})
	if err != nil {
		return Content{}, fmt.Errorf("firecrawl: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+firecrawlScrapePath, bytes.NewReader(payload))
	if err != nil {
		return Content{}, fmt.Errorf("firecrawl: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return Content{}, fmt.Errorf("firecrawl: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Content{}, fmt.Errorf("firecrawl: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Content{}, fmt.Errorf("firecrawl: scrape request returned status %d: %s", resp.StatusCode, firecrawlErrorMessage(body))
	}

	var parsed firecrawlScrapeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Content{}, fmt.Errorf("firecrawl: decode response: %w", err)
	}
	if !parsed.Success {
		return Content{}, fmt.Errorf("firecrawl: scrape unsuccessful: %s", firstNonEmpty(parsed.Error, "unknown error"))
	}

	metaMap := parsed.Data.Metadata
	title := NormalizeText(firecrawlMetaString(metaMap, "title"))
	description := NormalizeText(firecrawlMetaString(metaMap, "description"))
	statusCode := firecrawlMetaInt(metaMap, "statusCode")
	contentType := strings.ToLower(strings.TrimSpace(firecrawlMetaString(metaMap, "contentType")))
	finalURL := firstNonEmpty(firecrawlMetaString(metaMap, "sourceURL"), firecrawlMetaString(metaMap, "url"), rawURL)

	text := strings.TrimSpace(parsed.Data.Markdown)
	summary := description
	if len(summary) <= 20 {
		summary = PreviewText(text, 200)
	}

	metadata := map[string]any{
		"domain":       urlHost(finalURL),
		"content_type": contentType,
		"status_code":  statusCode,
	}
	for key := range metaMap {
		lower := strings.ToLower(key)
		if lower == "description" || strings.HasPrefix(lower, "og:") || strings.HasPrefix(lower, "twitter:") {
			if value := firecrawlMetaString(metaMap, key); value != "" {
				metadata[key] = value
			}
		}
	}

	content := Content{
		FinalURL: finalURL,
		Title:    title,
		Summary:  summary,
		Text:     text,
		Metadata: metadata,
		Blocked:  isBlocked(statusCode, text, text),
	}
	return content, nil
}

// firecrawlErrorMessage best-effort extracts a human-readable message from a non-200
// Firecrawl API response body, which is documented to take shapes like
// {"error": "..."} or {"success": false, "code": "...", "error": "..."}.
func firecrawlErrorMessage(body []byte) string {
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && strings.TrimSpace(errResp.Error) != "" {
		return strings.TrimSpace(errResp.Error)
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 200 {
		trimmed = trimmed[:200]
	}
	if trimmed == "" {
		return "no response body"
	}
	return trimmed
}

// firecrawlMetaString reads a metadata field that Firecrawl documents as either a plain
// string or an array of strings, returning the string value (or the first non-empty
// array element).
func firecrawlMetaString(m map[string]any, key string) string {
	value, ok := m[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		return ""
	default:
		return ""
	}
}

func firecrawlMetaInt(m map[string]any, key string) int {
	value, ok := m[key]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
