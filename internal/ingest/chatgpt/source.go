package chatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const kind = "chatgpt_export"

type Source struct{}

func (Source) Kind() string {
	return kind
}

func (Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	root := config.ExpandHome(cfg.Source.Root)
	exportDirs, err := discoverExportDirs(root)
	if err != nil {
		return nil, err
	}

	maxSizeBytes := int64(50 * 1024 * 1024)
	rules, err := config.DecodeRules[config.ChatGPTExportRules](cfg)
	if err != nil {
		return nil, err
	}
	if _, err := ValidateArchivePolicy(root, rules); err != nil {
		return nil, err
	}
	if rules.MaxFileSizeMB > 0 {
		maxSizeBytes = int64(rules.MaxFileSizeMB) * 1024 * 1024
	}

	var out []domain.PipelineFragment
	for _, exportDir := range exportDirs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		parseDir, err := prepareExportDir(exportDir, rules)
		if err != nil {
			return nil, fmt.Errorf("prepare export dir %s: %w", exportDir, err)
		}
		fileIndex, err := buildAttachmentIndex(exportDir)
		if err != nil {
			return nil, fmt.Errorf("build attachment index %s: %w", exportDir, err)
		}
		userMeta := readUserMeta(parseDir)
		paths, err := conversationFiles(parseDir)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil || info.Size() > maxSizeBytes {
				continue
			}
			fragments, err := parseConversationFile(path, parseDir, userMeta, fileIndex)
			if err != nil {
				return nil, fmt.Errorf("parse chatgpt export file %s: %w", path, err)
			}
			out = append(out, fragments...)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

type exportUserMeta struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type exportConversation struct {
	ID          string                      `json:"id"`
	Title       string                      `json:"title"`
	CreateTime  float64                     `json:"create_time"`
	UpdateTime  float64                     `json:"update_time"`
	CurrentNode string                      `json:"current_node"`
	Mapping     map[string]conversationNode `json:"mapping"`
}

type conversationNode struct {
	ID      string        `json:"id"`
	Parent  string        `json:"parent"`
	Message exportMessage `json:"message"`
}

type exportMessage struct {
	ID         string         `json:"id"`
	CreateTime float64        `json:"create_time"`
	Author     exportAuthor   `json:"author"`
	Content    exportContent  `json:"content"`
	Metadata   map[string]any `json:"metadata"`
}

type exportAuthor struct {
	Role string `json:"role"`
}

type exportContent struct {
	ContentType string `json:"content_type"`
	Parts       []any  `json:"parts"`
}

func discoverExportDirs(root string) ([]string, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat source root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source root is not a directory: %s", root)
	}
	if hasConversationFiles(root) {
		return []string{root}, nil
	}
	seen := map[string]struct{}{}
	var dirs []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if matched, _ := filepath.Match("conversations-*.json", filepath.Base(path)); matched {
			dir := filepath.Dir(path)
			if _, ok := seen[dir]; !ok {
				seen[dir] = struct{}{}
				dirs = append(dirs, dir)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk chatgpt export dirs: %w", err)
	}
	sort.Strings(dirs)
	return dirs, nil
}

func hasConversationFiles(root string) bool {
	paths, err := filepath.Glob(filepath.Join(root, "conversations-*.json"))
	return err == nil && len(paths) > 0
}

func conversationFiles(root string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(root, "conversations-*.json"))
	if err != nil {
		return nil, fmt.Errorf("glob chatgpt conversations: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func prepareExportDir(sourceDir string, rules config.ChatGPTExportRules) (string, error) {
	if !rules.CopyTextExports || strings.TrimSpace(rules.ArchiveRoot) == "" {
		return sourceDir, nil
	}
	archiveRoot := config.ExpandHome(rules.ArchiveRoot)
	targetDir := filepath.Join(archiveRoot, filepath.Base(sourceDir))
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir archive root: %w", err)
	}
	files := exportTextFiles(sourceDir)
	for _, src := range files {
		dst := filepath.Join(targetDir, filepath.Base(src))
		if err := copyFile(src, dst); err != nil {
			return "", fmt.Errorf("copy export file %s: %w", src, err)
		}
	}
	if rules.DeleteCopiedSource {
		for _, src := range files {
			if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("delete copied source file %s: %w", src, err)
			}
		}
	}
	return targetDir, nil
}

func exportTextFiles(root string) []string {
	patterns := []string{
		"conversations-*.json",
		"shared_conversations.json",
		"chat.html",
		"user.json",
		"message_feedback.json",
	}
	var out []string
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(root, pattern))
		out = append(out, matches...)
	}
	sort.Strings(out)
	return out
}

func copyFile(src, dst string) error {
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

func readUserMeta(root string) exportUserMeta {
	path := filepath.Join(root, "user.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return exportUserMeta{}
	}
	var meta exportUserMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return exportUserMeta{}
	}
	return meta
}

func parseConversationFile(path, exportDir string, userMeta exportUserMeta, fileIndex map[string]string) ([]domain.PipelineFragment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read export file: %w", err)
	}
	var conversations []exportConversation
	if err := json.Unmarshal(raw, &conversations); err != nil {
		return nil, fmt.Errorf("decode export json: %w", err)
	}
	out := make([]domain.PipelineFragment, 0, len(conversations))
	for _, conv := range conversations {
		fragment, ok := buildConversationFragment(conv, path, exportDir, userMeta, fileIndex)
		if ok {
			out = append(out, fragment)
		}
	}
	return out, nil
}

func buildConversationFragment(conv exportConversation, sourceFile, exportDir string, userMeta exportUserMeta, fileIndex map[string]string) (domain.PipelineFragment, bool) {
	if strings.TrimSpace(conv.ID) == "" {
		return domain.PipelineFragment{}, false
	}
	messages := conversationMessages(conv)
	if len(messages) == 0 {
		return domain.PipelineFragment{}, false
	}
	var (
		blocks      []string
		attachments []domain.PipelineAttachment
	)
	createdAt := timeFromUnixFloat(conv.CreateTime)
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Author.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		text := extractTextParts(msg.Content)
		if text == "" {
			continue
		}
		blocks = append(blocks, "## "+role+"\n\n"+text)
		attachments = append(attachments, extractMessageAttachments(msg, fileIndex)...)
		msgTime := timeFromUnixFloat(msg.CreateTime)
		if createdAt.IsZero() || (!msgTime.IsZero() && msgTime.Before(createdAt)) {
			createdAt = msgTime
		}
	}
	if len(blocks) == 0 {
		return domain.PipelineFragment{}, false
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	title := strings.TrimSpace(conv.Title)
	if title == "" {
		title = conv.ID
	}
	meta := map[string]any{
		"conversation_id": conv.ID,
		"export_dir":      exportDir,
		"source_file":     sourceFile,
		"current_node":    conv.CurrentNode,
	}
	if userMeta.ID != "" {
		meta["user_id"] = userMeta.ID
	}
	if userMeta.Email != "" {
		meta["user_email"] = userMeta.Email
	}
	if userMeta.Name != "" {
		meta["user_name"] = userMeta.Name
	}
	canonicalPath := filepath.ToSlash(filepath.Join("fragments", "chats", "chatgpt", createdAt.Format("2006-01-02"), conv.ID))
	return domain.PipelineFragment{
		Source:        "chatgpt",
		SourceType:    "chat",
		SourceID:      conv.ID,
		Title:         "ChatGPT chat: " + title,
		Content:       strings.Join(blocks, "\n\n"),
		CreatedAt:     createdAt.UTC(),
		Metadata:      meta,
		CanonicalPath: canonicalPath,
		Attachments:   dedupeAttachments(attachments),
	}, true
}

func conversationMessages(conv exportConversation) []exportMessage {
	if len(conv.Mapping) == 0 {
		return nil
	}
	if strings.TrimSpace(conv.CurrentNode) != "" {
		chain := make([]exportMessage, 0, len(conv.Mapping))
		seen := map[string]struct{}{}
		current := conv.CurrentNode
		for current != "" {
			if _, ok := seen[current]; ok {
				break
			}
			seen[current] = struct{}{}
			node, ok := conv.Mapping[current]
			if !ok {
				break
			}
			if node.Message.ID != "" || node.Message.Author.Role != "" {
				chain = append(chain, node.Message)
			}
			current = node.Parent
		}
		reverseMessages(chain)
		return chain
	}
	var out []exportMessage
	for _, node := range conv.Mapping {
		if node.Message.ID == "" && node.Message.Author.Role == "" {
			continue
		}
		out = append(out, node.Message)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreateTime < out[j].CreateTime
	})
	return out
}

func reverseMessages(in []exportMessage) {
	for i, j := 0, len(in)-1; i < j; i, j = i+1, j-1 {
		in[i], in[j] = in[j], in[i]
	}
}

func extractTextParts(content exportContent) string {
	if content.ContentType != "" && content.ContentType != "text" {
		return ""
	}
	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		text, ok := part.(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func timeFromUnixFloat(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	seconds := int64(v)
	nanos := int64((v - float64(seconds)) * float64(time.Second))
	return time.Unix(seconds, nanos).UTC()
}

func buildAttachmentIndex(root string) (map[string]string, error) {
	index := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := strings.ToLower(filepath.Base(path))
		index[base] = path
		if idx := strings.LastIndex(base, "-"); idx >= 0 && idx+1 < len(base) {
			index[base[idx+1:]] = path
		}
		return nil
	})
	return index, err
}

func extractMessageAttachments(msg exportMessage, fileIndex map[string]string) []domain.PipelineAttachment {
	var out []domain.PipelineAttachment
	if raw, ok := msg.Metadata["attachments"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := firstString(m, "name", "filename", "title")
			sourcePath := resolveAttachmentPath(name, fileIndex)
			out = append(out, domain.PipelineAttachment{
				Kind:         inferAttachmentKind(name, firstString(m, "mimeType", "mime_type"), ""),
				Role:         "attachment",
				Name:         name,
				MIMEType:     firstString(m, "mimeType", "mime_type"),
				SourcePath:   sourcePath,
				SizeBytes:    fileSize(sourcePath),
				Metadata:     m,
				Source:       "chatgpt_export",
				SourceItemID: firstString(m, "id"),
			})
		}
	}
	out = append(out, extractMessageURLs(msg.Metadata, msg.ID)...)
	return out
}

func extractMessageURLs(meta map[string]any, msgID string) []domain.PipelineAttachment {
	var out []domain.PipelineAttachment
	seen := map[string]struct{}{}
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			if rawURL, ok := typed["url"].(string); ok {
				addURLAttachment(rawURL, msgID, seen, &out)
			}
			if rawURLs, ok := typed["safe_urls"].([]any); ok {
				for _, item := range rawURLs {
					if s, ok := item.(string); ok {
						addURLAttachment(s, msgID, seen, &out)
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(meta)
	return out
}

func addURLAttachment(rawURL, msgID string, seen map[string]struct{}, out *[]domain.PipelineAttachment) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return
	}
	if _, ok := seen[rawURL]; ok {
		return
	}
	seen[rawURL] = struct{}{}
	*out = append(*out, domain.PipelineAttachment{
		Kind:         inferAttachmentKind("", "", rawURL),
		Role:         "reference",
		Name:         attachmentNameFromURL(rawURL),
		ExternalURL:  rawURL,
		Metadata:     map[string]any{"url": rawURL},
		Source:       "chatgpt_export",
		SourceItemID: msgID,
	})
}

func attachmentNameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	base := filepath.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		return rawURL
	}
	return base
}

func resolveAttachmentPath(name string, fileIndex map[string]string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	return fileIndex[name]
}

func fileSize(path string) int64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func inferAttachmentKind(name, mimeType, rawURL string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	name = strings.ToLower(strings.TrimSpace(name))
	rawURL = strings.ToLower(strings.TrimSpace(rawURL))
	switch {
	case strings.Contains(mimeType, "image"), hasExt(name, rawURL, ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"):
		return "image"
	case strings.Contains(mimeType, "video"), hasExt(name, rawURL, ".mp4", ".mov", ".mkv", ".webm"):
		return "video"
	case strings.Contains(mimeType, "audio"), hasExt(name, rawURL, ".mp3", ".wav", ".m4a", ".aac"):
		return "audio"
	case strings.Contains(mimeType, "pdf"), hasExt(name, rawURL, ".pdf"):
		return "pdf"
	case strings.Contains(mimeType, "wordprocessingml.document"), hasExt(name, rawURL, ".docx"):
		return "docx"
	case strings.Contains(mimeType, "markdown"), hasExt(name, rawURL, ".md", ".markdown", ".mdx"):
		return "markdown"
	case strings.Contains(mimeType, "json"), hasExt(name, rawURL, ".json"):
		return "json"
	case strings.Contains(mimeType, "xml"), hasExt(name, rawURL, ".xml"):
		return "xml"
	case strings.Contains(mimeType, "text/plain"), hasExt(name, rawURL, ".txt", ".text"):
		return "text"
	case hasExt(name, rawURL, ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rb", ".rs", ".java", ".c", ".cc", ".cpp", ".h", ".hpp", ".css", ".scss", ".sql", ".sh", ".zsh", ".bash"):
		return "code"
	case hasExt(name, rawURL, ".zip", ".tar", ".gz"):
		return "archive"
	case rawURL != "":
		return "url"
	default:
		return "file"
	}
}

func hasExt(name, rawURL string, exts ...string) bool {
	for _, ext := range exts {
		if strings.HasSuffix(name, ext) || strings.Contains(rawURL, ext) {
			return true
		}
	}
	return false
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := m[key].(string); ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

func dedupeAttachments(in []domain.PipelineAttachment) []domain.PipelineAttachment {
	out := make([]domain.PipelineAttachment, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		key := strings.Join([]string{
			item.Kind,
			item.Role,
			item.Name,
			item.SourcePath,
			item.ExternalURL,
			item.SourceItemID,
		}, "\n")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}
