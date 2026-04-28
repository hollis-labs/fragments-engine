package urlsource

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
)

const kind = "url_source"

var (
	titlePattern  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	scriptPattern = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	stylePattern  = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	tagPattern    = regexp.MustCompile(`(?is)<[^>]+>`)
	spacePattern  = regexp.MustCompile(`\s+`)
)

type Source struct {
	client *http.Client
	now    func() time.Time
}

func (s Source) Kind() string {
	return kind
}

func (s Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	root := config.ExpandHome(cfg.Source.Root)
	entries, err := discoverEntries(root)
	if err != nil {
		return nil, err
	}
	rules, err := config.DecodeRules[config.URLSourceRules](cfg)
	if err != nil {
		return nil, err
	}
	timeout := 20 * time.Second
	if rules.RequestTimeoutSeconds > 0 {
		timeout = time.Duration(rules.RequestTimeoutSeconds) * time.Second
	}
	maxBodyBytes := int64(5 * 1024 * 1024)
	if rules.MaxBodyMB > 0 {
		maxBodyBytes = int64(rules.MaxBodyMB) * 1024 * 1024
	}
	userAgent := strings.TrimSpace(rules.UserAgent)
	if userAgent == "" {
		userAgent = "FragmentsEngine/0.1 (+url_source)"
	}
	client := s.client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}

	out := make([]domain.PipelineFragment, 0, len(entries))
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		item, err := collectURL(ctx, client, cfg, entry, maxBodyBytes, userAgent, now)
		if err != nil {
			return nil, fmt.Errorf("collect url %s: %w", entry.URL, err)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

type manifestEntry struct {
	URL        string
	Title      string
	CreatedAt  time.Time
	Labels     []string
	SourceFile string
}

func discoverEntries(root string) ([]manifestEntry, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat source root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source root is not a directory: %s", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".txt", ".json", ".jsonl":
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk url manifests: %w", err)
	}
	sort.Strings(paths)

	var out []manifestEntry
	for _, path := range paths {
		items, err := parseManifest(path)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return dedupeEntries(out), nil
}

func parseManifest(path string) ([]manifestEntry, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt":
		return parseTextManifest(path)
	case ".json":
		return parseJSONManifest(path)
	case ".jsonl":
		return parseJSONLManifest(path)
	default:
		return nil, nil
	}
}

func parseTextManifest(path string) ([]manifestEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest %s: %w", path, err)
	}
	defer f.Close()
	var out []manifestEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, manifestEntry{URL: line, SourceFile: path})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan manifest %s: %w", path, err)
	}
	return out, nil
}

type jsonManifestItem struct {
	URL       string   `json:"url"`
	Title     string   `json:"title"`
	CreatedAt string   `json:"created_at"`
	Labels    []string `json:"labels"`
}

func parseJSONManifest(path string) ([]manifestEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("decode json manifest %s: %w", path, err)
	}
	out := make([]manifestEntry, 0, len(arr))
	for _, item := range arr {
		entry, ok, err := decodeManifestItem(item, path)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, entry)
		}
	}
	return out, nil
}

func parseJSONLManifest(path string) ([]manifestEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest %s: %w", path, err)
	}
	defer f.Close()
	var out []manifestEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		entry, ok, err := decodeManifestItem(line, path)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan manifest %s: %w", path, err)
	}
	return out, nil
}

func decodeManifestItem(raw []byte, path string) (manifestEntry, bool, error) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return manifestEntry{}, false, nil
		}
		return manifestEntry{URL: asString, SourceFile: path}, true, nil
	}
	var item jsonManifestItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return manifestEntry{}, false, fmt.Errorf("decode manifest item %s: %w", path, err)
	}
	if strings.TrimSpace(item.URL) == "" {
		return manifestEntry{}, false, nil
	}
	entry := manifestEntry{
		URL:        item.URL,
		Title:      item.Title,
		Labels:     item.Labels,
		SourceFile: path,
	}
	if item.CreatedAt != "" {
		if ts, err := time.Parse(time.RFC3339, item.CreatedAt); err == nil {
			entry.CreatedAt = ts.UTC()
		}
	}
	return entry, true, nil
}

func dedupeEntries(items []manifestEntry) []manifestEntry {
	seen := map[string]struct{}{}
	out := make([]manifestEntry, 0, len(items))
	for _, item := range items {
		key := normalizeSourceURL(item.URL)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item.URL = key
		out = append(out, item)
	}
	return out
}

func collectURL(ctx context.Context, client *http.Client, ingestCfg config.IngestConfig, entry manifestEntry, maxBodyBytes int64, userAgent string, now time.Time) (domain.PipelineFragment, error) {
	if extract.IsYouTubeURL(entry.URL) {
		return collectYouTubeURL(ctx, client, ingestCfg, entry, userAgent, now)
	}
	if extract.IsVideoTranscriptCandidateURL(entry.URL) {
		item, ok, err := tryCollectVideoURL(ctx, ingestCfg, entry, now)
		if err != nil {
			return domain.PipelineFragment{}, err
		}
		if ok {
			return item, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("read response body: %w", err)
	}
	finalURL := entry.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	classification := classifyFetchedURL(finalURL, contentType)
	meta := map[string]any{
		"source_url":    entry.URL,
		"final_url":     finalURL,
		"status_code":   resp.StatusCode,
		"content_type":  contentType,
		"source_file":   entry.SourceFile,
		"url_kind":      classification,
		"fetched_at":    now.Format(time.RFC3339),
		"content_bytes": len(body),
	}

	title := strings.TrimSpace(entry.Title)
	content := ""
	extractorName := ""
	switch classification {
	case "article", "html":
		extracted, err := extract.ExtractArticle(body, finalURL)
		if err == nil {
			extractorName = "go-readability"
			if title == "" {
				title = extracted.Title
			}
			content = extracted.Text
			for key, value := range extracted.Metadata {
				if value != nil && value != "" {
					meta[key] = value
				}
			}
		}
		if title == "" {
			title = extractHTMLTitle(body)
		}
		if content == "" {
			content = extractHTMLText(body)
		}
	case "markdown", "text", "json", "xml":
		text := strings.TrimSpace(string(body))
		if title == "" {
			title = deriveTitleFromURL(finalURL)
		}
		content = text
	case "pdf":
		extracted, err := extract.ExtractPDF(body)
		if err == nil && extracted.Text != "" {
			extractorName = "ledongthuc/pdf"
			content = extracted.Text
			for key, value := range extracted.Metadata {
				if value != nil && value != "" {
					meta[key] = value
				}
			}
		}
		if title == "" {
			title = deriveTitleFromURL(finalURL)
		}
	default:
		if title == "" {
			title = deriveTitleFromURL(finalURL)
		}
		content = placeholderContent(finalURL, classification, contentType)
	}
	if title == "" {
		title = deriveTitleFromURL(finalURL)
	}
	if content == "" {
		content = placeholderContent(finalURL, classification, contentType)
	}

	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	host := urlHost(finalURL)
	day := createdAt.Format("2006-01-02")
	sourceID := normalizeSourceURL(finalURL)
	if extractorName != "" {
		meta["extractor"] = extractorName
	}
	if len(entry.Labels) > 0 {
		meta["labels"] = entry.Labels
	}
	namespace := strings.TrimSpace(ingestCfg.Routing.Namespace)
	if namespace == "" {
		namespace = "fragments/web"
	}
	return domain.PipelineFragment{
		Source:        "url",
		SourceType:    classification,
		SourceID:      sourceID,
		Title:         title,
		Content:       content,
		CreatedAt:     createdAt,
		Metadata:      meta,
		CanonicalPath: filepath.ToSlash(filepath.Join(filepath.FromSlash(namespace), host, day, canonicalURLLeaf(finalURL))),
		Attachments: []domain.PipelineAttachment{
			{
				Kind:        attachmentKindForClassification(classification),
				Role:        "reference",
				Name:        attachmentNameFromURL(finalURL),
				MIMEType:    contentType,
				ExternalURL: finalURL,
				Source:      "url_source",
				Metadata: map[string]any{
					"status_code":  resp.StatusCode,
					"content_type": contentType,
					"url_kind":     classification,
				},
			},
		},
	}, nil
}

func tryCollectVideoURL(ctx context.Context, ingestCfg config.IngestConfig, entry manifestEntry, now time.Time) (domain.PipelineFragment, bool, error) {
	extracted, err := extract.ExtractVideoTranscript(ctx, entry.URL)
	if err != nil {
		return domain.PipelineFragment{}, false, nil
	}
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = strings.TrimSpace(extracted.Title)
	}
	if title == "" {
		title = deriveTitleFromURL(entry.URL)
	}
	meta := map[string]any{
		"source_url":  entry.URL,
		"source_file": entry.SourceFile,
		"url_kind":    "video",
		"fetched_at":  now.Format(time.RFC3339),
	}
	for key, value := range extracted.Metadata {
		if value != nil && value != "" {
			meta[key] = value
		}
	}
	if len(entry.Labels) > 0 {
		meta["labels"] = entry.Labels
	}
	namespace := strings.TrimSpace(ingestCfg.Routing.Namespace)
	if namespace == "" {
		namespace = "fragments/web"
	}
	return domain.PipelineFragment{
		Source:        "url",
		SourceType:    "video",
		SourceID:      normalizeSourceURL(entry.URL),
		Title:         title,
		Content:       extracted.Text,
		CreatedAt:     createdAt,
		Metadata:      meta,
		CanonicalPath: filepath.ToSlash(filepath.Join(filepath.FromSlash(namespace), urlHost(entry.URL), createdAt.Format("2006-01-02"), canonicalURLLeaf(entry.URL))),
		Attachments: []domain.PipelineAttachment{
			{
				Kind:        "video",
				Role:        "reference",
				Name:        attachmentNameFromURL(entry.URL),
				ExternalURL: entry.URL,
				Source:      "url_source",
				Metadata: map[string]any{
					"url_kind": "video",
				},
			},
		},
	}, true, nil
}

func collectYouTubeURL(ctx context.Context, client *http.Client, ingestCfg config.IngestConfig, entry manifestEntry, userAgent string, now time.Time) (domain.PipelineFragment, error) {
	extracted, err := extract.ExtractYouTubeTranscript(ctx, client, entry.URL, userAgent)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("extract youtube transcript: %w", err)
	}
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	videoID := extract.ExtractYouTubeID(entry.URL)
	meta := map[string]any{
		"source_url":  entry.URL,
		"source_file": entry.SourceFile,
		"url_kind":    "youtube",
		"fetched_at":  now.Format(time.RFC3339),
	}
	for key, value := range extracted.Metadata {
		if value != nil && value != "" {
			meta[key] = value
		}
	}
	namespace := strings.TrimSpace(ingestCfg.Routing.Namespace)
	if namespace == "" {
		namespace = "fragments/web"
	}
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = extracted.Title
	}
	if title == "" {
		title = "YouTube video " + videoID
	}
	return domain.PipelineFragment{
		Source:        "url",
		SourceType:    "youtube",
		SourceID:      normalizeSourceURL(entry.URL),
		Title:         title,
		Content:       extracted.Text,
		CreatedAt:     createdAt,
		Metadata:      meta,
		CanonicalPath: filepath.ToSlash(filepath.Join(filepath.FromSlash(namespace), "youtube", createdAt.Format("2006-01-02"), canonicalURLLeaf(entry.URL))),
		Attachments: []domain.PipelineAttachment{
			{
				Kind:        "video",
				Role:        "reference",
				Name:        attachmentNameFromURL(entry.URL),
				MIMEType:    "text/vtt",
				ExternalURL: entry.URL,
				Source:      "url_source",
				Metadata: map[string]any{
					"video_platform": "youtube",
					"video_id":       videoID,
				},
			},
		},
	}, nil
}

func normalizeSourceURL(raw string) string {
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func classifyFetchedURL(rawURL, contentType string) string {
	lowerURL := strings.ToLower(rawURL)
	switch {
	case extract.IsYouTubeURL(rawURL):
		return "youtube"
	case contentType == "text/html":
		return "article"
	case strings.HasPrefix(contentType, "text/markdown"):
		return "markdown"
	case strings.HasPrefix(contentType, "text/plain"):
		return "text"
	case strings.HasPrefix(contentType, "application/json"):
		return "json"
	case strings.HasPrefix(contentType, "application/xml"), strings.HasPrefix(contentType, "text/xml"):
		return "xml"
	case contentType == "application/pdf" || strings.HasSuffix(lowerURL, ".pdf"):
		return "pdf"
	case strings.HasPrefix(contentType, "image/") || hasAnySuffix(lowerURL, ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"):
		return "image"
	case strings.HasPrefix(contentType, "video/") || hasAnySuffix(lowerURL, ".mp4", ".mov", ".webm", ".mkv", ".m4v"):
		return "video"
	default:
		return "url"
	}
}

func placeholderContent(rawURL, classification, contentType string) string {
	var b strings.Builder
	b.WriteString("URL: " + rawURL + "\n")
	b.WriteString("Type: " + classification + "\n")
	if contentType != "" {
		b.WriteString("Content-Type: " + contentType + "\n")
	}
	b.WriteString("Text extraction is not enabled for this content type yet.\n")
	return b.String()
}

func extractHTMLTitle(body []byte) string {
	matches := titlePattern.FindSubmatch(body)
	if len(matches) < 2 {
		return ""
	}
	return normalizeText(string(matches[1]))
}

func extractHTMLText(body []byte) string {
	text := scriptPattern.ReplaceAll(body, nil)
	text = stylePattern.ReplaceAll(text, nil)
	text = tagPattern.ReplaceAll(text, []byte(" "))
	return normalizeText(string(text))
}

func normalizeText(text string) string {
	text = html.UnescapeString(text)
	text = strings.TrimSpace(spacePattern.ReplaceAllString(text, " "))
	return text
}

func deriveTitleFromURL(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	leaf := pathLeaf(u.Path)
	if leaf != "" {
		leaf = strings.TrimSuffix(leaf, filepath.Ext(leaf))
		leaf = strings.ReplaceAll(leaf, "-", " ")
		leaf = strings.ReplaceAll(leaf, "_", " ")
		leaf = strings.TrimSpace(leaf)
		if leaf != "" {
			return leaf
		}
	}
	return u.Host
}

func pathLeaf(path string) string {
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func canonicalURLLeaf(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return "item"
	}
	leaf := pathLeaf(u.Path)
	if leaf == "" {
		leaf = "index"
	}
	leaf = strings.TrimSuffix(leaf, filepath.Ext(leaf))
	leaf = strings.ToLower(leaf)
	leaf = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(leaf, "-")
	leaf = strings.Trim(leaf, "-")
	if leaf == "" {
		leaf = "item"
	}
	return leaf
}

func attachmentNameFromURL(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return "reference"
	}
	leaf := pathLeaf(u.Path)
	if leaf != "" {
		return leaf
	}
	return u.Host
}

func attachmentKindForClassification(classification string) string {
	switch classification {
	case "article", "html":
		return "html"
	case "markdown", "text":
		return "text"
	case "json":
		return "json"
	case "xml":
		return "xml"
	case "pdf":
		return "pdf"
	case "image":
		return "image"
	case "video":
		return "video"
	case "youtube":
		return "video"
	default:
		return "url"
	}
}

func urlHost(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "unknown-host"
	}
	host := strings.ToLower(u.Host)
	host = strings.ReplaceAll(host, ":", "-")
	return host
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}
