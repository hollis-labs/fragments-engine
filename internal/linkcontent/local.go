package linkcontent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/extract"
)

const localBackendName = "local"

// blockedSubstrings holds case-insensitive substrings that, when found in a fetched page
// body, indicate the request was likely intercepted by a bot-blocking / challenge page
// rather than the real content. Kept in one place so the list is easy to extend.
var blockedSubstrings = []string{
	"Just a moment",
	"Enable JavaScript and cookies to continue",
	"Attention Required! | Cloudflare",
	"Access Denied",
	"captcha",
}

const (
	defaultRequestTimeout = 20 * time.Second
	defaultMaxBodyBytes   = 5 * 1024 * 1024
	defaultUserAgent      = "FragmentsEngine/0.1 (+link-content)"
	blockedMinTextChars   = 40
)

type localProvider struct {
	client    *http.Client
	maxBody   int64
	userAgent string
}

// NewLocalProvider builds the "local" Provider: it fetches the URL directly with a bounded
// timeout and max body size, then extracts title/text via internal/extract.ExtractArticle
// and og:*/meta description tags via the shared meta helpers in this package.
func NewLocalProvider(cfg config.LinkContentLocalConfig) Provider {
	timeout := defaultRequestTimeout
	if cfg.RequestTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	}
	maxBody := int64(defaultMaxBodyBytes)
	if cfg.MaxBodyMB > 0 {
		maxBody = int64(cfg.MaxBodyMB) * 1024 * 1024
	}
	userAgent := strings.TrimSpace(cfg.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &localProvider{
		client:    &http.Client{Timeout: timeout},
		maxBody:   maxBody,
		userAgent: userAgent,
	}
}

func (p *localProvider) Backend() string {
	return localBackendName
}

func (p *localProvider) Fetch(ctx context.Context, rawURL string) (Content, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Content{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return Content{}, fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, p.maxBody)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Content{}, fmt.Errorf("read response body: %w", err)
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	rawBody := string(body)

	metaTags := ParseMetaTags(rawBody)
	title := NormalizeText(firstNonEmpty(metaTags["og:title"], metaTags["twitter:title"], HTMLTitle(rawBody)))

	text := ""
	if extracted, extractErr := extract.ExtractArticle(body, finalURL); extractErr == nil {
		text = strings.TrimSpace(extracted.Text)
		if title == "" {
			title = strings.TrimSpace(extracted.Title)
		}
	}

	metadata := map[string]any{
		"domain":       urlHost(finalURL),
		"content_type": contentType,
		"status_code":  resp.StatusCode,
	}
	for key, value := range metaTags {
		if strings.HasPrefix(key, "og:") || strings.HasPrefix(key, "twitter:") || key == "description" {
			metadata[key] = value
		}
	}

	description := NormalizeText(firstNonEmpty(metaTags["og:description"], metaTags["description"], metaTags["twitter:description"]))
	summary := description
	if len(summary) <= 20 {
		summary = PreviewText(text, 200)
	}

	content := Content{
		FinalURL: finalURL,
		Title:    title,
		Summary:  summary,
		Text:     text,
		Metadata: metadata,
		Blocked:  isBlocked(resp.StatusCode, text, rawBody),
	}
	return content, nil
}

func isBlocked(statusCode int, text, rawBody string) bool {
	switch statusCode {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	}
	if statusCode == http.StatusOK && len(strings.TrimSpace(text)) < blockedMinTextChars {
		return true
	}
	lowerBody := strings.ToLower(rawBody)
	for _, substr := range blockedSubstrings {
		if strings.Contains(lowerBody, strings.ToLower(substr)) {
			return true
		}
	}
	return false
}

func urlHost(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
