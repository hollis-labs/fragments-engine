package service

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
)

const inboxReviewerVersion = "inbox-reviewer-v1"

var (
	metaTagPattern  = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	metaAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:][a-zA-Z0-9_:\-]*)\s*=\s*["']([^"']*)["']`)
	titleTagPattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	spacePattern    = regexp.MustCompile(`\s+`)
)

type ManualIntakeEnricher struct {
	client        *http.Client
	vision        analyze.VisionAnalyzer
	now           func() time.Time
	downloadRoot  string
	githubAPIBase string
	githubToken   string
	pinterestBase string
}

type ManualEnrichment struct {
	Title         string
	SourceType    string
	CanonicalPath string
	Summary       string
	Metadata      map[string]any
	Entities      []domain.FragmentEntity
	Attachments   []domain.PipelineAttachment
}

type PinterestFetchResult struct {
	Title       string
	Description string
	PinImageURL string
}

func NewManualIntakeEnricher(vision analyze.VisionAnalyzer, downloadRoot string) *ManualIntakeEnricher {
	return &ManualIntakeEnricher{
		client:        &http.Client{Timeout: 20 * time.Second},
		vision:        vision,
		now:           func() time.Time { return time.Now().UTC() },
		downloadRoot:  strings.TrimSpace(downloadRoot),
		githubAPIBase: "https://api.github.com",
	}
}

func (e *ManualIntakeEnricher) SetGitHubToken(token string) {
	if e == nil {
		return
	}
	e.githubToken = strings.TrimSpace(token)
}

func (e *ManualIntakeEnricher) EnrichIntake(_ context.Context, content, title, sourceType string, tags []string) (ManualEnrichment, error) {
	normalizedTitle := strings.TrimSpace(title)
	normalizedType := strings.TrimSpace(sourceType)
	if normalizedType == "url" || normalizedType == "article" {
		normalizedType = ""
	}
	trimmed := strings.TrimSpace(content)
	if normalizedTitle == trimmed {
		normalizedTitle = ""
	}
	if trimmed == "" {
		return ManualEnrichment{}, nil
	}
	rawURL, ok := singleURL(trimmed)
	if !ok {
		return ManualEnrichment{}, nil
	}
	u, err := normalizeURL(rawURL)
	if err != nil {
		return ManualEnrichment{}, nil
	}
	metadata := baseURLMetadata(u)
	entities := []domain.FragmentEntity{
		entity("domain", u.Hostname(), "manual-intake"),
	}
	if len(tags) > 0 {
		metadata["input_tags"] = append([]string(nil), tags...)
	}

	if owner, repo, ok := githubRepoURL(u); ok {
		entities = appendEntity(entities,
			entity("platform", "github", "manual-intake"),
			entity("repo", owner+"/"+repo, "manual-intake"),
			entity("repo_owner", owner, "manual-intake"),
		)
		metadata["platform"] = "github"
		metadata["repo_owner"] = owner
		metadata["repo_name"] = repo
		metadata["repo_full_name"] = owner + "/" + repo
		metadata["suggested_destination"] = "stack_explorer"
		if normalizedTitle == "" {
			normalizedTitle = owner + "/" + repo
		}
		if normalizedType == "" {
			normalizedType = "repo"
		}
		return ManualEnrichment{
			Title:         normalizedTitle,
			SourceType:    normalizedType,
			CanonicalPath: path.Join("fragments", "manual", normalizedType, "github.com", owner, repo),
			Summary:       "Saved GitHub repo " + owner + "/" + repo + " for review.",
			Metadata:      metadata,
			Entities:      entities,
			Attachments: []domain.PipelineAttachment{
				urlReferenceAttachment(u.String(), normalizedType, "manual-intake"),
			},
		}, nil
	}

	if pinID, ok := pinterestPinURL(u); ok {
		entities = appendEntity(entities,
			entity("platform", "pinterest", "manual-intake"),
			entity("pin", pinID, "manual-intake"),
		)
		metadata["platform"] = "pinterest"
		metadata["pin_id"] = pinID
		metadata["suggested_action"] = "fetch_pin_image"
		if normalizedTitle == "" {
			normalizedTitle = "Pinterest pin " + pinID
		}
		if normalizedType == "" {
			normalizedType = "pin"
		}
		return ManualEnrichment{
			Title:         normalizedTitle,
			SourceType:    normalizedType,
			CanonicalPath: path.Join("fragments", "manual", normalizedType, "pinterest", pinID),
			Summary:       "Saved Pinterest pin " + pinID + " for image review.",
			Metadata:      metadata,
			Entities:      entities,
			Attachments: []domain.PipelineAttachment{
				urlReferenceAttachment(u.String(), normalizedType, "manual-intake"),
			},
		}, nil
	}

	if normalizedType == "" {
		normalizedType = "url"
	}
	if normalizedTitle == "" {
		normalizedTitle = deriveTitleFromURL(u)
	}
	return ManualEnrichment{
		Title:         normalizedTitle,
		SourceType:    normalizedType,
		CanonicalPath: path.Join("fragments", "manual", normalizedType, u.Hostname(), canonicalURLLeaf(u)),
		Summary:       "Saved URL from " + u.Hostname() + ".",
		Metadata:      metadata,
		Entities:      entities,
		Attachments: []domain.PipelineAttachment{
			urlReferenceAttachment(u.String(), normalizedType, "manual-intake"),
		},
	}, nil
}

func (e *ManualIntakeEnricher) ReviewURL(ctx context.Context, fragment domain.Fragment, currentMeta map[string]any) (ManualEnrichment, bool, error) {
	rawURL, ok := singleURL(fragment.Content)
	if !ok {
		return ManualEnrichment{}, false, nil
	}
	u, err := normalizeURL(rawURL)
	if err != nil {
		return ManualEnrichment{}, false, nil
	}
	base, err := e.EnrichIntake(ctx, fragment.Content, fragment.Title, fragment.SourceType, nil)
	if err != nil {
		return ManualEnrichment{}, false, err
	}
	base.Metadata = mergeMetadata(currentMeta, base.Metadata)
	base.Metadata["reviewed_by"] = "inbox_reviewer"
	base.Metadata["review_version"] = inboxReviewerVersion
	base.Metadata["reviewed_at"] = e.now().Format(time.RFC3339)

	switch {
	case currentMetaValue(currentMeta, "platform") == "github" || isGitHubHost(u):
		owner, repo, ok := githubRepoURL(u)
		if !ok {
			return base, true, nil
		}
		info, err := e.fetchGitHubRepo(ctx, owner, repo)
		if err != nil {
			base.Metadata["review_error"] = err.Error()
			return base, true, nil
		}
		base.Title = firstNonEmpty(info.FullName, base.Title)
		base.Summary = firstNonEmpty(info.Description, base.Summary)
		if base.Summary != "" {
			base.Summary = "GitHub repo: " + base.Summary
		}
		if info.Description != "" {
			base.Metadata["repo_description"] = info.Description
		}
		if info.Language != "" {
			base.Metadata["repo_language"] = info.Language
			base.Entities = appendEntity(base.Entities, entity("language", info.Language, "inbox-reviewer"))
		}
		if info.HTMLURL != "" {
			base.Metadata["url"] = info.HTMLURL
		}
		if info.Stars > 0 {
			base.Metadata["repo_stars"] = info.Stars
		}
		if len(info.Topics) > 0 {
			base.Metadata["repo_topics"] = info.Topics
			for _, topic := range info.Topics {
				base.Entities = appendEntity(base.Entities, entity("topic", topic, "inbox-reviewer"))
			}
		}
		return base, true, nil
	case currentMetaValue(currentMeta, "platform") == "pinterest" || isPinterestHost(u):
		pin, err := e.fetchPinterestPin(ctx, u.String())
		if err != nil {
			base.Metadata["review_error"] = err.Error()
			return base, true, nil
		}
		if pin.Title != "" {
			base.Title = pin.Title
		}
		if pin.Description != "" {
			base.Summary = pin.Description
			base.Metadata["pin_description"] = pin.Description
		}
		if strings.TrimSpace(pin.PinImageURL) != "" {
			attachment, err := e.downloadPinterestImage(ctx, u.String(), base.Metadata, pin.PinImageURL)
			if err == nil {
				base.Attachments = appendUniqueAttachment(base.Attachments, attachment)
				base.Metadata["pin_image_url"] = pin.PinImageURL
			} else {
				base.Metadata["review_error"] = err.Error()
			}
		}
		return base, true, nil
	default:
		return base, true, nil
	}
}

type GitHubRepoInfo struct {
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	HTMLURL     string   `json:"html_url"`
	Language    string   `json:"language"`
	Stars       int      `json:"stargazers_count"`
	Topics      []string `json:"topics"`
}

func (e *ManualIntakeEnricher) fetchGitHubRepo(ctx context.Context, owner, repo string) (GitHubRepoInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.githubAPIBase, "/")+"/repos/"+owner+"/"+repo, nil)
	if err != nil {
		return GitHubRepoInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "FragmentsEngine/0.1 (+manual-reviewer)")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if e.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+e.githubToken)
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return GitHubRepoInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		if strings.TrimSpace(payload.Message) != "" {
			return GitHubRepoInfo{}, fmt.Errorf("github repo fetch status %d: %s", resp.StatusCode, strings.TrimSpace(payload.Message))
		}
		return GitHubRepoInfo{}, fmt.Errorf("github repo fetch status %d", resp.StatusCode)
	}
	var out GitHubRepoInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return GitHubRepoInfo{}, err
	}
	return out, nil
}

func (e *ManualIntakeEnricher) fetchPinterestPin(ctx context.Context, rawURL string) (PinterestFetchResult, error) {
	requestURL := rawURL
	if strings.TrimSpace(e.pinterestBase) != "" {
		if u, err := normalizeURL(rawURL); err == nil && isPinterestHost(u) {
			requestURL = strings.TrimRight(e.pinterestBase, "/") + u.EscapedPath()
			if u.RawQuery != "" {
				requestURL += "?" + u.RawQuery
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return PinterestFetchResult{}, err
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return PinterestFetchResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PinterestFetchResult{}, fmt.Errorf("pinterest fetch status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return PinterestFetchResult{}, err
	}
	meta := parseMetaTags(string(body))
	title := firstNonEmpty(meta["og:title"], meta["twitter:title"], htmlTitle(string(body)))
	description := firstNonEmpty(meta["og:description"], meta["description"], meta["twitter:description"])
	imageURL := firstNonEmpty(meta["og:image"], meta["twitter:image"])
	return PinterestFetchResult{
		Title:       normalizeText(title),
		Description: normalizeText(description),
		PinImageURL: strings.TrimSpace(imageURL),
	}, nil
}

func (e *ManualIntakeEnricher) downloadPinterestImage(ctx context.Context, pinURL string, meta map[string]any, imageURL string) (domain.PipelineAttachment, error) {
	if strings.TrimSpace(e.downloadRoot) == "" {
		return domain.PipelineAttachment{}, fmt.Errorf("download root is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return domain.PipelineAttachment{}, err
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return domain.PipelineAttachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.PipelineAttachment{}, fmt.Errorf("image fetch status %d", resp.StatusCode)
	}
	pinID := currentMetaValue(meta, "pin_id")
	if pinID == "" {
		if u, err := normalizeURL(pinURL); err == nil {
			pinID, _ = pinterestPinURL(u)
		}
	}
	if pinID == "" {
		pinID = "pin"
	}
	dir := filepath.Join(e.downloadRoot, "pinterest", pinID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return domain.PipelineAttachment{}, err
	}
	imageName := imageFilename(imageURL, resp.Header.Get("Content-Type"), pinID)
	dst := filepath.Join(dir, imageName)
	out, err := os.Create(dst)
	if err != nil {
		return domain.PipelineAttachment{}, err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return domain.PipelineAttachment{}, err
	}
	if err := out.Close(); err != nil {
		return domain.PipelineAttachment{}, err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return domain.PipelineAttachment{}, err
	}
	attachment := domain.PipelineAttachment{
		Kind:         "image",
		Role:         "reference",
		Name:         imageName,
		MIMEType:     strings.TrimSpace(resp.Header.Get("Content-Type")),
		SourcePath:   dst,
		ExternalURL:  imageURL,
		StoragePath:  dst,
		SizeBytes:    info.Size(),
		Source:       "inbox_reviewer",
		SourceItemID: pinID,
		Metadata: map[string]any{
			"pin_url":         pinURL,
			"downloaded_at":   e.now().Format(time.RFC3339),
			"download_source": "pinterest",
		},
	}
	if err := e.enrichImageAttachment(ctx, &attachment); err != nil {
		attachment.Metadata["review_error"] = err.Error()
	}
	if previewPath, err := generateLocalImagePreview(dst); err == nil && strings.TrimSpace(previewPath) != "" {
		attachment.Metadata["preview_storage_path"] = previewPath
	}
	return attachment, nil
}

func (e *ManualIntakeEnricher) enrichImageAttachment(ctx context.Context, item *domain.PipelineAttachment) error {
	out, err := extract.ExtractLocalAttachment(item.SourcePath, item.MIMEType, item.Kind)
	if err != nil {
		return err
	}
	if item.Metadata == nil {
		item.Metadata = map[string]any{}
	}
	for key, value := range out.Metadata {
		item.Metadata[key] = value
	}
	if strings.TrimSpace(out.Text) != "" {
		item.Metadata["extracted_text_bytes"] = len(out.Text)
		item.Metadata["extracted_text_preview"] = previewText(out.Text, 280)
	}
	if out.Title != "" {
		item.Metadata["extracted_title"] = out.Title
	}
	*item = analyze.EnrichAttachmentMetadata(*item)
	if e.vision != nil {
		if visionOut, err := e.vision.AnalyzeImage(ctx, item.SourcePath, item.Metadata); err == nil && len(visionOut) > 0 {
			backend := e.vision.Backend()
			if raw, ok := visionOut["_provider_backend"].(string); ok && strings.TrimSpace(raw) != "" {
				backend = strings.TrimSpace(raw)
			}
			delete(visionOut, "_provider_backend")
			item.Metadata["vision_analysis"] = visionOut
			item.Metadata["vision_analysis_backend"] = backend
		}
	}
	return nil
}

func generateLocalImagePreview(srcPath string) (string, error) {
	if _, err := exec.LookPath("sips"); err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(srcPath), "previews")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	if base == "" {
		base = "preview"
	}
	dst := filepath.Join(dir, base+".preview.jpg")
	cmd := exec.Command("sips", "-s", "format", "jpeg", "-Z", "512", srcPath, "--out", dst)
	if output, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("generate preview: %s", msg)
	}
	return dst, nil
}

func baseURLMetadata(u *neturl.URL) map[string]any {
	return map[string]any{
		"url":      u.String(),
		"domain":   u.Hostname(),
		"url_path": u.EscapedPath(),
	}
}

func urlReferenceAttachment(rawURL, kind, source string) domain.PipelineAttachment {
	return domain.PipelineAttachment{
		Kind:        "url",
		Role:        "reference",
		Name:        attachmentNameFromURL(rawURL),
		ExternalURL: rawURL,
		Source:      source,
		Metadata: map[string]any{
			"url_kind": kind,
		},
	}
}

func attachmentNameFromURL(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	leaf := path.Base(strings.TrimSpace(u.Path))
	if leaf == "" || leaf == "/" || leaf == "." {
		return u.Hostname()
	}
	return leaf
}

func deriveTitleFromURL(u *neturl.URL) string {
	leaf := canonicalURLLeaf(u)
	if leaf != "" && leaf != u.Hostname() {
		return leaf
	}
	return u.Hostname()
}

func canonicalURLLeaf(u *neturl.URL) string {
	leaf := path.Base(strings.TrimRight(u.Path, "/"))
	leaf = strings.TrimSpace(leaf)
	if leaf == "" || leaf == "." || leaf == "/" {
		return "index"
	}
	return leaf
}

func singleURL(content string) (string, bool) {
	content = strings.TrimSpace(content)
	if strings.ContainsAny(content, " \t\n") {
		return "", false
	}
	if strings.HasPrefix(content, "http://") || strings.HasPrefix(content, "https://") {
		return content, true
	}
	return "", false
}

func normalizeURL(raw string) (*neturl.URL, error) {
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme")
	}
	u.Fragment = ""
	return u, nil
}

func githubRepoURL(u *neturl.URL) (owner, repo string, ok bool) {
	if !isGitHubHost(u) {
		return "", "", false
	}
	parts := urlParts(u)
	if len(parts) < 2 {
		return "", "", false
	}
	if slices.Contains([]string{"orgs", "topics", "collections", "features"}, parts[0]) {
		return "", "", false
	}
	repo = strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return "", "", false
	}
	return parts[0], repo, true
}

func pinterestPinURL(u *neturl.URL) (string, bool) {
	if !isPinterestHost(u) {
		return "", false
	}
	parts := urlParts(u)
	if len(parts) < 2 || parts[0] != "pin" {
		return "", false
	}
	return parts[1], parts[1] != ""
}

func isGitHubHost(u *neturl.URL) bool {
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "github.com"
}

func isPinterestHost(u *neturl.URL) bool {
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "pinterest.com" || host == "www.pinterest.com"
}

func urlParts(u *neturl.URL) []string {
	raw := strings.Trim(u.Path, "/")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func entity(kind, value, source string) domain.FragmentEntity {
	return domain.FragmentEntity{
		Kind:       strings.TrimSpace(kind),
		Value:      strings.TrimSpace(value),
		Source:     source,
		Confidence: 1,
	}
}

func appendEntity(items []domain.FragmentEntity, additions ...domain.FragmentEntity) []domain.FragmentEntity {
	seen := map[string]struct{}{}
	out := make([]domain.FragmentEntity, 0, len(items)+len(additions))
	for _, item := range append(items, additions...) {
		if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.Value) == "" {
			continue
		}
		key := item.Kind + "\n" + item.Value + "\n" + item.Source
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func appendUniqueAttachment(items []domain.PipelineAttachment, item domain.PipelineAttachment) []domain.PipelineAttachment {
	for _, current := range items {
		if current.SourcePath == item.SourcePath && current.ExternalURL == item.ExternalURL && current.Name == item.Name {
			return items
		}
	}
	return append(items, item)
}

func mergeMetadata(current, next map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range current {
		out[key] = value
	}
	for key, value := range next {
		out[key] = value
	}
	return out
}

func currentMetaValue(meta map[string]any, key string) string {
	raw, ok := meta[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func previewText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	if limit < 4 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func normalizeText(text string) string {
	text = html.UnescapeString(text)
	return strings.TrimSpace(spacePattern.ReplaceAllString(text, " "))
}

func parseMetaTags(raw string) map[string]string {
	tags := metaTagPattern.FindAllString(raw, -1)
	out := make(map[string]string, len(tags))
	for _, tag := range tags {
		attrs := metaAttrPattern.FindAllStringSubmatch(tag, -1)
		if len(attrs) == 0 {
			continue
		}
		attrMap := map[string]string{}
		for _, attr := range attrs {
			if len(attr) < 3 {
				continue
			}
			attrMap[strings.ToLower(strings.TrimSpace(attr[1]))] = strings.TrimSpace(attr[2])
		}
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(attrMap["property"], attrMap["name"])))
		if key == "" {
			continue
		}
		content := strings.TrimSpace(attrMap["content"])
		if content == "" {
			continue
		}
		out[key] = content
	}
	return out
}

func htmlTitle(raw string) string {
	match := titleTagPattern.FindStringSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	return normalizeText(match[1])
}

func imageFilename(rawURL, contentType, fallback string) string {
	u, err := neturl.Parse(rawURL)
	ext := ""
	if err == nil {
		ext = strings.ToLower(filepath.Ext(u.Path))
	}
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(strings.TrimSpace(contentType)); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == "" {
		ext = ".img"
	}
	return fallback + ext
}

func (e *ManualIntakeEnricher) httpClient() *http.Client {
	if e.client != nil {
		return e.client
	}
	return &http.Client{Timeout: 20 * time.Second}
}
