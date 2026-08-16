package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
)

const inboxReviewerVersion = "inbox-reviewer-v1"

// genericLinkFetchTimeout bounds how long EnrichIntake's generic-URL branch
// waits on the local link-content provider. Kept well under the surrounding
// HTTP request's own reasonable budget so a slow/unresponsive site can never
// hang POST /v1/intake indefinitely.
const genericLinkFetchTimeout = 9 * time.Second

// maxLinkEnrichmentAttempts caps how many times ReviewURL's default (generic
// URL) branch will retry a "pending" fragment through the fallback-capable
// linkFallback provider before giving up and marking it "failed". Without
// this cap a permanently-blocked or permanently-dead URL would stay
// "pending" forever, and the reviewer would call the fallback provider (and
// bill Firecrawl) on it every poll cycle indefinitely.
const maxLinkEnrichmentAttempts = 5

type ManualIntakeEnricher struct {
	client        *http.Client
	vision        analyze.VisionAnalyzer
	now           func() time.Time
	downloadRoot  string
	githubAPIBase string
	githubToken   string
	pinterestBase string
	linkProvider  linkcontent.Provider
	// linkFallback is a SEPARATE, fallback-wrapped linkcontent.Provider (built
	// via linkcontent.NewProvider(cfg.LinkContent), primary="local",
	// fallback="firecrawl") reserved for the async inbox-reviewer retry path.
	// Unlike linkProvider above -- which is local-only and used synchronously
	// during intake -- this instance is allowed to retry through Firecrawl.
	// It is wired in by internal/app/app.go via SetLinkContentProvider and,
	// as of this field's introduction, is not yet read by EnrichIntake,
	// ReviewURL, or anything else; a later task consumes it from the
	// reviewer's async retry logic.
	linkFallback linkcontent.Provider
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
		// Local-only by default. Per product decision, Firecrawl (or any
		// fallback-wrapped provider) is never invoked synchronously during
		// intake -- callers that want a configured local provider (timeouts,
		// user agent, etc.) should call SetLinkProvider explicitly, e.g. with
		// linkcontent.NewLocalProvider(cfg.LinkContent.Local) as done in
		// internal/app/app.go.
		linkProvider: linkcontent.NewLocalProvider(config.LinkContentLocalConfig{}),
	}
}

func (e *ManualIntakeEnricher) SetGitHubToken(token string) {
	if e == nil {
		return
	}
	e.githubToken = strings.TrimSpace(token)
}

// SetLinkProvider overrides the local-only linkcontent.Provider used by the
// generic (non-GitHub, non-Pinterest) URL branch of EnrichIntake. Must remain
// local-only -- Firecrawl (or any fallback-wrapped provider) is never invoked
// synchronously during intake.
func (e *ManualIntakeEnricher) SetLinkProvider(provider linkcontent.Provider) {
	if e == nil {
		return
	}
	e.linkProvider = provider
}

// SetLinkContentProvider wires the fallback-capable linkcontent.Provider
// (linkFallback) used by the async inbox-reviewer retry path -- NOT the
// synchronous EnrichIntake/ReviewURL flow, which continues to use the
// local-only linkProvider set via SetLinkProvider above. Callers (currently
// internal/app/app.go) build this with linkcontent.NewProvider(cfg.LinkContent)
// so it may retry a configured Firecrawl backend when the primary (local)
// backend errors or reports Content.Blocked.
func (e *ManualIntakeEnricher) SetLinkContentProvider(provider linkcontent.Provider) {
	if e == nil {
		return
	}
	e.linkFallback = provider
}

// EnrichIntake derives a ManualEnrichment for the given fragment content. The
// linkURL parameter is an explicitly-detected target URL to fall back to when
// content isn't itself a bare URL (e.g. content has a "link" tag with the URL
// embedded mid-text) -- pass "" when no such URL was detected.
func (e *ManualIntakeEnricher) EnrichIntake(ctx context.Context, content, title, sourceType string, tags []string, linkURL string) (ManualEnrichment, error) {
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
		rawURL = strings.TrimSpace(linkURL)
		ok = rawURL != ""
	}
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
	summary := "Saved URL from " + u.Hostname() + "."
	fetchedTitle := ""
	if e.linkProvider != nil {
		fetchCtx, cancel := context.WithTimeout(ctx, genericLinkFetchTimeout)
		fetched, fetchErr := e.linkProvider.Fetch(fetchCtx, u.String())
		cancel()
		if fetchErr == nil && !fetched.Blocked {
			fetchedTitle = strings.TrimSpace(fetched.Title)
			if strings.TrimSpace(fetched.Summary) != "" {
				summary = strings.TrimSpace(fetched.Summary)
			}
			if strings.TrimSpace(fetched.Text) != "" {
				metadata["link_text"] = fetched.Text
			}
			for key, value := range fetched.Metadata {
				metadata[key] = value
			}
			// Marks this fragment as already enriched so a later reviewer
			// doesn't re-fetch it every cycle.
			metadata["enrichment_status"] = "done"
		} else {
			// Fetch failed or the site blocked us -- keep the placeholder
			// summary so intake never fails or hangs, but flag this fragment
			// for a later retry mechanism.
			metadata["enrichment_status"] = "pending"
		}
	}
	if normalizedTitle == "" {
		normalizedTitle = fetchedTitle
	}
	if normalizedTitle == "" {
		normalizedTitle = deriveTitleFromURL(u)
	}
	return ManualEnrichment{
		Title:         normalizedTitle,
		SourceType:    normalizedType,
		CanonicalPath: path.Join("fragments", "manual", normalizedType, u.Hostname(), canonicalURLLeaf(u)),
		Summary:       summary,
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
	// ReviewURL operates on already-stored fragments; retrying enrichment from
	// an embedded (non-bare-URL) link is out of scope here -- pass "" and rely
	// on singleURL(fragment.Content) alone, matching today's behavior.
	base, err := e.EnrichIntake(ctx, fragment.Content, fragment.Title, fragment.SourceType, nil, "")
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
		// Retry pending generic-URL fragments through the fallback-capable
		// provider (local -> firecrawl). Gate on currentMeta (the fragment's
		// state as of BEFORE this review pass), not base.Metadata -- the
		// latter may already read "done" or "pending" purely as a side
		// effect of EnrichIntake's own internal local-only re-fetch above,
		// and gating on that would either skip retries that should run or
		// re-invoke the fallback provider (and re-bill Firecrawl) on every
		// poll cycle for links that are already done/given-up.
		//
		// Retries are bounded by maxLinkEnrichmentAttempts (see
		// shouldRetryLinkEnrichment): once a pending fragment has been
		// attempted that many times without success, enrichment_status is
		// switched to "failed" instead of "pending" so neither this gate nor
		// needsReReview will invoke the fallback provider on it again.
		if e.linkFallback != nil && currentMetaValue(currentMeta, "enrichment_status") == "pending" {
			attempts := currentMetaInt(currentMeta, "enrichment_attempts")
			if shouldRetryLinkEnrichment(attempts) {
				fetched, fetchErr := e.linkFallback.Fetch(ctx, u.String())
				attempts++
				if fetchErr == nil && !fetched.Blocked {
					// Only replace the title if the fragment doesn't already have
					// a real, user-meaningful one. base.Title is always
					// non-empty by this point (EnrichIntake falls back to a
					// derived-from-URL placeholder), so checking base.Title
					// itself would never let a fetched title through. And
					// fragment.Title alone isn't enough either: once a "pending"
					// fragment survives one review pass, applyEnrichment
					// persists that same derived placeholder back as
					// fragment.Title, so on the NEXT poll cycle fragment.Title
					// is non-empty too even though nothing real was ever set.
					// So treat the title as "already set" only when it's
					// non-empty AND differs from what EnrichIntake would derive
					// fresh from the URL right now -- i.e. it's either a real
					// user-provided title or a title a prior successful fetch
					// already populated.
					if strings.TrimSpace(fragment.Title) == "" || fragment.Title == deriveTitleFromURL(u) {
						if title := strings.TrimSpace(fetched.Title); title != "" {
							base.Title = title
						}
					}
					if summary := strings.TrimSpace(fetched.Summary); summary != "" {
						base.Summary = summary
					}
					if text := strings.TrimSpace(fetched.Text); text != "" {
						base.Metadata["link_text"] = text
					}
					for key, value := range fetched.Metadata {
						base.Metadata[key] = value
					}
					// Matches EnrichIntake's own generic-URL branch convention.
					base.Metadata["enrichment_status"] = "done"
					// Don't leave a stale attempt counter on a
					// successfully-enriched fragment -- delete rather than
					// zero it out so it doesn't show up as noise in stored
					// metadata.
					delete(base.Metadata, "enrichment_attempts")
				} else {
					// Fallback fetch failed or the site is still blocking us.
					// Record the attempt and only keep retrying on future
					// poll cycles while under the cap; once the cap is
					// reached, give up for good by marking the fragment
					// "failed" instead of "pending". Also record why, matching
					// the review_error convention the GitHub/Pinterest
					// branches above already use -- otherwise a
					// still-pending/failed link gives no signal for why.
					if fetchErr != nil {
						base.Metadata["review_error"] = fetchErr.Error()
					} else {
						base.Metadata["review_error"] = fmt.Sprintf("%s: content still looked blocked after fallback", e.linkFallback.Backend())
					}
					base.Metadata["enrichment_attempts"] = attempts
					if shouldRetryLinkEnrichment(attempts) {
						base.Metadata["enrichment_status"] = "pending"
					} else {
						base.Metadata["enrichment_status"] = "failed"
					}
				}
			}
		}
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
	meta := linkcontent.ParseMetaTags(string(body))
	title := firstNonEmpty(meta["og:title"], meta["twitter:title"], linkcontent.HTMLTitle(string(body)))
	description := firstNonEmpty(meta["og:description"], meta["description"], meta["twitter:description"])
	imageURL := firstNonEmpty(meta["og:image"], meta["twitter:image"])
	return PinterestFetchResult{
		Title:       linkcontent.NormalizeText(title),
		Description: linkcontent.NormalizeText(description),
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
		item.Metadata["extracted_text_preview"] = linkcontent.PreviewText(out.Text, 280)
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

// currentMetaInt reads an int-valued metadata key, defaulting to 0 when the
// key is absent or unparseable. Metadata that round-trips through JSON
// storage (see decodeFragmentMetadata in inbox_reviewer.go) decodes numbers
// into map[string]any as float64, not int -- but metadata built directly in
// the same process (e.g. by a unit test, or freshly by EnrichIntake earlier
// in this same call) may still hold a plain int. Handle both.
func currentMetaInt(meta map[string]any, key string) int {
	raw, ok := meta[key]
	if !ok {
		return 0
	}
	switch value := raw.(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

// shouldRetryLinkEnrichment reports whether a pending generic-URL fragment
// that has already been attempted `attempts` times should be retried again.
// Boundary semantics: attempts is the count of fetches already made (0
// before the first attempt), and this returns true while attempts is still
// below maxLinkEnrichmentAttempts -- so attempt #5 (attempts==4 going in)
// still runs, and only once it too fails (bringing attempts to 5) does this
// start returning false, at which point the caller marks the fragment
// "failed" instead of "pending". This means exactly maxLinkEnrichmentAttempts
// real fetch attempts happen before a permanently-failing fragment is
// retired.
func shouldRetryLinkEnrichment(attempts int) bool {
	return attempts < maxLinkEnrichmentAttempts
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
