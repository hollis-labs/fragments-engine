package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ffs"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type FileDestinationExecutor struct{}
type MCPDestinationExecutor struct{}
type APIDestinationExecutor struct{}
type CLIDestinationExecutor struct{}
type CallbackDestinationExecutor struct{}

func (FileDestinationExecutor) Execute(_ context.Context, destination domain.Destination, fragment domain.Fragment, attachments []domain.FragmentAttachment) (DeliveryResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.FileDestinationConfig](destination)
	if err != nil {
		return DeliveryResult{}, err
	}
	root := config.ExpandHome(cfg.Root)
	if strings.TrimSpace(root) == "" {
		return DeliveryResult{}, fmt.Errorf("file destination %q missing root", destination.Name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("resolve destination root: %w", err)
	}
	if strings.TrimSpace(cfg.Provider) == "ffs" && fragment.Source == "manual" && fragment.SourceType == "pin" {
		published, err := ffs.WritePinterestPinBundle(rootAbs, domain.FragmentDetail{
			Fragment:    fragment,
			Attachments: attachments,
		})
		if err != nil {
			return DeliveryResult{}, err
		}
		return DeliveryResult{Ref: published.FragmentPath, PublishedAttachments: published.PublishedAttachments}, nil
	}
	rel := resolveFileDestinationRelativePath(cfg, fragment)
	if rel == "" {
		rel = filepath.ToSlash(filepath.Join("fragments", fragment.Source, fragment.SourceID))
	}
	rel = strings.TrimPrefix(filepath.Clean(filepath.FromSlash(rel)), string(filepath.Separator))
	targetDir := filepath.Join(rootAbs, rel)
	targetAbs, err := filepath.Abs(targetDir)
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("resolve target path: %w", err)
	}
	prefix := rootAbs + string(filepath.Separator)
	if targetAbs != rootAbs && !strings.HasPrefix(targetAbs, prefix) {
		return DeliveryResult{}, fmt.Errorf("destination path escapes root")
	}
	if err := os.MkdirAll(targetAbs, 0o750); err != nil {
		return DeliveryResult{}, fmt.Errorf("mkdir destination dir: %w", err)
	}
	fragmentPath := filepath.Join(targetAbs, "fragment.md")
	if err := os.WriteFile(fragmentPath, []byte(renderFragmentMarkdown(fragment)), 0o600); err != nil {
		return DeliveryResult{}, fmt.Errorf("write destination file: %w", err)
	}
	storage := map[string]domain.PublishedAttachmentInfo{}
	if len(attachments) > 0 {
		attachmentDir := filepath.Join(targetAbs, "attachments")
		if err := os.MkdirAll(attachmentDir, 0o750); err != nil {
			return DeliveryResult{}, fmt.Errorf("mkdir attachment dir: %w", err)
		}
		previewDir := filepath.Join(attachmentDir, "previews")
		for _, item := range attachments {
			if strings.TrimSpace(item.SourcePath) == "" {
				continue
			}
			name := publishedAttachmentName(item)
			dst := filepath.Join(attachmentDir, name)
			if err := copyLocalAttachment(item.SourcePath, dst); err != nil {
				return DeliveryResult{}, fmt.Errorf("copy attachment %s: %w", item.SourcePath, err)
			}
			info := domain.PublishedAttachmentInfo{StoragePath: dst}
			if isImageAttachment(item) {
				if previewPath, err := generateImagePreview(item.SourcePath, previewDir, name); err == nil && strings.TrimSpace(previewPath) != "" {
					info.PreviewStoragePath = previewPath
				}
			}
			storage[item.ID] = info
		}
	}
	return DeliveryResult{Ref: fragmentPath, PublishedAttachments: storage}, nil
}

func resolveFileDestinationRelativePath(cfg domain.FileDestinationConfig, fragment domain.Fragment) string {
	if strings.TrimSpace(cfg.PathTemplate) == "" {
		return strings.TrimSpace(fragment.CanonicalPath)
	}
	meta := map[string]any{}
	if raw := strings.TrimSpace(fragment.MetadataJSON); raw != "" {
		_ = json.Unmarshal([]byte(raw), &meta)
	}
	domain := referenceDomain(fragment, meta)
	rel := strings.TrimSpace(cfg.PathTemplate)
	replacements := map[string]string{
		"{id}":             sanitizePathSegment(fragment.ID),
		"{source}":         sanitizePathSegment(fragment.Source),
		"{source_type}":    sanitizePathSegment(fragment.SourceType),
		"{source_id}":      sanitizePathSegment(fragment.SourceID),
		"{title}":          sanitizePathSegment(fragment.Title),
		"{ref_name}":       sanitizePathSegment(referenceName(fragment, meta)),
		"{canonical_path}": sanitizeRelativePath(fragment.CanonicalPath),
		"{domain}":         sanitizePathSegment(domain),
		"{platform}":       sanitizePathSegment(metaString(meta, "platform")),
		"{pin_id}":         sanitizePathSegment(metaString(meta, "pin_id")),
		"{repo_owner}":     sanitizePathSegment(metaString(meta, "repo_owner")),
		"{repo_name}":      sanitizePathSegment(metaString(meta, "repo_name")),
	}
	for token, value := range replacements {
		rel = strings.ReplaceAll(rel, token, value)
	}
	return sanitizeRelativePath(rel)
}

func metaString(meta map[string]any, key string) string {
	value, ok := meta[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func sanitizePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	value = strings.Trim(b.String(), "-._")
	value = collapseRepeated(value, "--", "-")
	value = collapseRepeated(value, "__", "_")
	value = collapseRepeated(value, "..", ".")
	if len(value) > 96 {
		value = strings.Trim(value[:96], "-._")
	}
	if value == "" {
		return "item"
	}
	return value
}

func sanitizeRelativePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.TrimPrefix(filepath.Clean(filepath.FromSlash(value)), string(filepath.Separator))
}

func referenceDomain(fragment domain.Fragment, meta map[string]any) string {
	if domain := metaString(meta, "domain"); domain != "" {
		return domain
	}
	rawURL := referenceURL(fragment, meta)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Hostname())
}

func referenceName(fragment domain.Fragment, meta map[string]any) string {
	if owner := metaString(meta, "repo_owner"); owner != "" {
		if name := metaString(meta, "repo_name"); name != "" {
			return owner + "-" + name
		}
	}
	rawURL := referenceURL(fragment, meta)
	if rawURL != "" {
		if name := referenceNameFromURL(rawURL); name != "" {
			return name
		}
	}
	if title := strings.TrimSpace(fragment.Title); title != "" && !looksLikeURL(title) {
		return title
	}
	if sourceID := strings.TrimSpace(fragment.SourceID); sourceID != "" {
		return sourceID
	}
	return fragment.ID
}

func referenceURL(fragment domain.Fragment, meta map[string]any) string {
	return firstNonEmpty(
		metaString(meta, "url"),
		metaString(meta, "external_url"),
		extractLeadingURL(fragment.Title),
		fragmentContentURL(fragment.Content),
	)
}

func referenceNameFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	segments := make([]string, 0)
	for _, segment := range strings.Split(strings.Trim(parsed.Path, "/"), "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		segments = append(segments, segment)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if strings.Contains(host, "reddit.com") && len(segments) >= 5 && segments[0] == "r" && segments[2] == "comments" {
		return segments[4]
	}
	if len(segments) > 0 {
		last := segments[len(segments)-1]
		if last != "" && last != "comments" && last != "blog" && last != "docs" {
			if parsed.Fragment != "" {
				return last + "-" + parsed.Fragment
			}
			return last
		}
	}
	if parsed.Fragment != "" {
		return parsed.Fragment
	}
	if host != "" {
		return host
	}
	return ""
}

func looksLikeURL(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func collapseRepeated(value, needle, replacement string) string {
	for strings.Contains(value, needle) {
		value = strings.ReplaceAll(value, needle, replacement)
	}
	return value
}

func fragmentContentURL(content string) string {
	content = strings.TrimSpace(content)
	if direct := extractLeadingURL(content); direct != "" {
		return direct
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func extractLeadingURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	candidate := strings.TrimSpace(fields[0])
	if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
		return candidate
	}
	return ""
}

func renderFragmentMarkdown(fragment domain.Fragment) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + fragment.ID + "\n")
	b.WriteString("source: " + fragment.Source + "\n")
	b.WriteString("source_type: " + fragment.SourceType + "\n")
	b.WriteString("source_id: " + fragment.SourceID + "\n")
	b.WriteString("status: " + string(fragment.Status) + "\n")
	b.WriteString("created_at: " + fragment.CreatedAt.Format("2006-01-02T15:04:05Z07:00") + "\n")
	b.WriteString("ingested_at: " + fragment.IngestedAt.Format("2006-01-02T15:04:05Z07:00") + "\n")
	b.WriteString("ingest_name: " + fragment.IngestName + "\n")
	b.WriteString("canonical_path: " + fragment.CanonicalPath + "\n")
	b.WriteString("---\n\n")
	b.WriteString("# " + fragment.Title + "\n\n")
	b.WriteString(fragment.Content)
	if !strings.HasSuffix(fragment.Content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func (MCPDestinationExecutor) Execute(ctx context.Context, destination domain.Destination, fragment domain.Fragment, _ []domain.FragmentAttachment) (DeliveryResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.MCPDestinationConfig](destination)
	if err != nil {
		return DeliveryResult{}, err
	}
	if transport := strings.TrimSpace(cfg.Transport); transport != "" && transport != "stdio" {
		return DeliveryResult{}, fmt.Errorf("unsupported mcp transport %q", transport)
	}
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return DeliveryResult{}, fmt.Errorf("mcp destination %q missing command", destination.Name)
	}
	timeout := time.Duration(max(cfg.TimeoutSeconds, 30)) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c, err := mcpclient.NewStdioMCPClient(command, cfg.Env, cfg.Args...)
	if err != nil {
		return DeliveryResult{}, markRetryable(fmt.Errorf("start mcp client: %w", err))
	}
	defer c.Close()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "fragments-engine",
		Version: "0.1.0",
	}
	if _, err := c.Initialize(callCtx, initReq); err != nil {
		return DeliveryResult{}, markRetryable(fmt.Errorf("initialize mcp client: %w", err))
	}

	toolName := strings.TrimSpace(cfg.Tool)
	args := map[string]any{}
	switch strings.TrimSpace(cfg.Provider) {
	case "", "nil_inbox":
		if toolName == "" {
			toolName = "nil_create_inbox"
		}
		args = buildNilInboxArguments(fragment, cfg.NilInbox)
	default:
		if toolName == "" {
			return DeliveryResult{}, fmt.Errorf("mcp destination %q missing tool for provider %q", destination.Name, cfg.Provider)
		}
		args = cloneArguments(cfg.Arguments)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args
	result, err := c.CallTool(callCtx, req)
	if err != nil {
		return DeliveryResult{}, markRetryable(fmt.Errorf("call mcp tool %q: %w", toolName, err))
	}
	return DeliveryResult{Ref: "mcp:" + toolName + ":" + firstToolResultText(result)}, nil
}

func (APIDestinationExecutor) Execute(ctx context.Context, destination domain.Destination, fragment domain.Fragment, _ []domain.FragmentAttachment) (DeliveryResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.APIDestinationConfig](destination)
	if err != nil {
		return DeliveryResult{}, err
	}
	baseURL := strings.TrimRight(expandConfigValue(cfg.BaseURL), "/")
	if baseURL == "" {
		return DeliveryResult{}, fmt.Errorf("api destination %q missing base_url", destination.Name)
	}
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	path := expandConfigValue(cfg.Path)
	if path == "" && (strings.TrimSpace(cfg.Provider) == "nanite_messaging" || strings.TrimSpace(cfg.Provider) == "nanite_user_mailbox") {
		path = "/api/messaging/send"
	}
	body, err := buildAPIBody(cfg, fragment)
	if err != nil {
		return DeliveryResult{}, err
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("encode api body: %w", err)
	}

	timeout := time.Duration(max(cfg.TimeoutSeconds, 30)) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, method, baseURL+path, bytes.NewReader(bodyJSON))
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("build api request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range cfg.Headers {
		req.Header.Set(key, expandConfigValue(value))
	}
	if user := expandConfigValue(cfg.BasicAuthUsername); user != "" {
		password := ""
		if envName := strings.TrimSpace(cfg.BasicAuthPasswordEnv); envName != "" {
			password = os.Getenv(envName)
		}
		req.SetBasicAuth(user, password)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return DeliveryResult{}, markRetryable(fmt.Errorf("api request failed: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("api request failed: status %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return DeliveryResult{}, markRetryable(err)
		}
		return DeliveryResult{}, err
	}
	return DeliveryResult{Ref: "api:" + method + ":" + req.URL.String()}, nil
}

// Execute performs the callback destination's HTTP POST against
// CallbackDestinationConfig.Target -- Curator's Nanite durable-agent wake
// endpoint. This is the only place in the callback dispatch path that does
// live network I/O; every call site that can reach it (the routing hot
// path, the queue drainer) must go through the async delivery queue first
// (see internal/service/delivery_queue.go's processJob), never call this
// inline. Generator is forwarded to the request body completely unmodified
// -- FE treats it as an opaque tag and must never interpret or branch on
// its value (loom-architecture.md §4).
func (CallbackDestinationExecutor) Execute(ctx context.Context, destination domain.Destination, fragment domain.Fragment, _ []domain.FragmentAttachment) (DeliveryResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.CallbackDestinationConfig](destination)
	if err != nil {
		return DeliveryResult{}, err
	}
	target := strings.TrimSpace(cfg.Target)
	if target == "" {
		return DeliveryResult{}, fmt.Errorf("callback destination %q missing target", destination.Name)
	}
	if strings.TrimSpace(cfg.Generator) == "" {
		return DeliveryResult{}, fmt.Errorf("callback destination %q missing generator", destination.Name)
	}

	bodyJSON, err := json.Marshal(buildCallbackPayload(fragment, cfg))
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("encode callback payload: %w", err)
	}

	timeout := 30 * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, target, bytes.NewReader(bodyJSON))
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("build callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return DeliveryResult{}, markRetryable(fmt.Errorf("callback request failed: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("callback request failed: status %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return DeliveryResult{}, markRetryable(err)
		}
		return DeliveryResult{}, err
	}
	return DeliveryResult{Ref: "callback:" + target}, nil
}

// buildCallbackPayload forwards Generator unmodified alongside enough
// fragment identity for Curator to look up what triggered the wake. FE never
// interprets Generator's value -- it is only ever copied through.
func buildCallbackPayload(fragment domain.Fragment, cfg domain.CallbackDestinationConfig) map[string]any {
	return map[string]any{
		"generator": cfg.Generator,
		"fragment": map[string]any{
			"id":             fragment.ID,
			"source":         fragment.Source,
			"source_type":    fragment.SourceType,
			"source_id":      fragment.SourceID,
			"title":          fragment.Title,
			"canonical_path": fragment.CanonicalPath,
		},
	}
}

func (CLIDestinationExecutor) Execute(ctx context.Context, destination domain.Destination, fragment domain.Fragment, _ []domain.FragmentAttachment) (DeliveryResult, error) {
	cfg, err := domain.DecodeDestinationConfig[domain.CLIDestinationConfig](destination)
	if err != nil {
		return DeliveryResult{}, err
	}
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return DeliveryResult{}, fmt.Errorf("cli destination %q missing command", destination.Name)
	}
	timeout := time.Duration(max(cfg.TimeoutSeconds, 30)) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string{}, cfg.Args...)
	cmd := exec.CommandContext(callCtx, command, args...)
	if wd := strings.TrimSpace(expandConfigValue(cfg.WorkingDir)); wd != "" {
		cmd.Dir = wd
	}
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	payload := buildCLIPayload(destination, fragment)
	input, err := json.Marshal(payload)
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("encode cli payload: %w", err)
	}
	cmd.Stdin = bytes.NewReader(input)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(err.Error())
		}
		return DeliveryResult{}, markRetryable(fmt.Errorf("cli command failed: %s", msg))
	}
	ref := strings.TrimSpace(stdout.String())
	if ref == "" {
		ref = "cli:" + command
	}
	return DeliveryResult{Ref: ref}, nil
}

func buildNilInboxArguments(fragment domain.Fragment, cfg domain.MCPNilInboxConfig) map[string]any {
	itemType := strings.TrimSpace(cfg.ItemType)
	if itemType == "" {
		itemType = "note"
	}
	title := strings.TrimSpace(fragment.Title)
	if strings.TrimSpace(cfg.TitlePrefix) != "" {
		prefix := cfg.TitlePrefix
		title = prefix + title
	}
	args := map[string]any{
		"title":    title,
		"notes_md": renderFragmentMarkdown(fragment),
		"type":     itemType,
	}
	if len(cfg.Tags) > 0 {
		args["tags"] = cfg.Tags
	}
	if len(cfg.Contexts) > 0 {
		args["contexts"] = cfg.Contexts
	}
	if len(cfg.Projects) > 0 {
		args["projects"] = cfg.Projects
	}
	return args
}

func buildAPIBody(cfg domain.APIDestinationConfig, fragment domain.Fragment) (map[string]any, error) {
	switch strings.TrimSpace(cfg.Provider) {
	case "", "nanite_messaging", "nanite_user_mailbox":
		if strings.TrimSpace(cfg.Path) == "" {
			cfg.Path = "/api/messaging/send"
		}
		return buildNaniteMessagingBody(fragment, cfg.NaniteMessaging), nil
	default:
		if cfg.Body == nil {
			return nil, fmt.Errorf("api destination missing body for provider %q", cfg.Provider)
		}
		return cloneArguments(cfg.Body), nil
	}
}

func buildNaniteMessagingBody(fragment domain.Fragment, cfg domain.APINaniteMessagingConfig) map[string]any {
	channel := strings.TrimSpace(cfg.Channel)
	if channel == "" {
		channel = "inbox"
	}
	kind := strings.TrimSpace(cfg.Kind)
	if kind == "" {
		kind = "notification"
	}
	msgType := strings.TrimSpace(cfg.Type)
	if msgType == "" {
		msgType = "status_update"
	}
	subject := strings.TrimSpace(fragment.Title)
	if strings.TrimSpace(cfg.SubjectPrefix) != "" {
		prefix := cfg.SubjectPrefix
		subject = prefix + subject
	}
	body := map[string]any{
		"FromSessionID": cfg.FromSessionID,
		"FromAgentID":   cfg.FromAgentID,
		"ToSessionID":   cfg.ToSessionID,
		"ToAgentID":     cfg.ToAgentID,
		"Channel":       channel,
		"Kind":          kind,
		"Type":          msgType,
		"Subject":       subject,
		"Body":          renderFragmentMarkdown(fragment),
		"Metadata":      buildNaniteMetadata(fragment),
		"RegisterAs":    defaultString(cfg.RegisterAs, "external"),
	}
	return body
}

func buildNaniteMetadata(fragment domain.Fragment) string {
	meta := map[string]any{
		"fragment_id":    fragment.ID,
		"source":         fragment.Source,
		"source_type":    fragment.SourceType,
		"source_id":      fragment.SourceID,
		"canonical_path": fragment.CanonicalPath,
		"ingest_name":    fragment.IngestName,
		"summary":        fragment.Summary,
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func buildCLIPayload(destination domain.Destination, fragment domain.Fragment) map[string]any {
	return map[string]any{
		"destination": map[string]any{
			"id":   destination.ID,
			"name": destination.Name,
			"kind": destination.Kind,
		},
		"fragment": map[string]any{
			"id":             fragment.ID,
			"source":         fragment.Source,
			"source_type":    fragment.SourceType,
			"source_id":      fragment.SourceID,
			"title":          fragment.Title,
			"content":        fragment.Content,
			"summary":        fragment.Summary,
			"status":         fragment.Status,
			"created_at":     fragment.CreatedAt.Format(time.RFC3339),
			"ingested_at":    fragment.IngestedAt.Format(time.RFC3339),
			"ingest_name":    fragment.IngestName,
			"canonical_path": fragment.CanonicalPath,
			"markdown":       renderFragmentMarkdown(fragment),
		},
	}
}

func copyLocalAttachment(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func publishedAttachmentName(item domain.FragmentAttachment) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = filepath.Base(item.SourcePath)
	}
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	if name == "" {
		name = item.ID
	}
	return item.ID[:12] + "-" + name
}

func isImageAttachment(item domain.FragmentAttachment) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.MIMEType)), "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(item.Name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	default:
		return item.Kind == "image"
	}
}

func generateImagePreview(srcPath, previewDir, name string) (string, error) {
	if _, err := exec.LookPath("sips"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(previewDir, 0o750); err != nil {
		return "", err
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if strings.TrimSpace(base) == "" {
		base = "preview"
	}
	dst := filepath.Join(previewDir, base+".preview.jpg")
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

func expandConfigValue(v string) string {
	return os.ExpandEnv(strings.TrimSpace(v))
}

func cloneArguments(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstToolResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return "ok"
	}
	for _, item := range result.Content {
		if text, ok := item.(mcp.TextContent); ok {
			return strings.TrimSpace(text.Text)
		}
	}
	return "ok"
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
