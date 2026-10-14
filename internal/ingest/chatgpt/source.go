package chatgpt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
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
	if rules.CopyTextExports || rules.DeleteCopiedSource {
		return nil, fmt.Errorf("transcript: raw archive copying/deletion is forbidden")
	}
	if _, err := ValidateArchivePolicy(root, rules); err != nil {
		return nil, err
	}
	if rules.MaxFileSizeMB > 0 {
		maxSizeBytes = int64(rules.MaxFileSizeMB) * 1024 * 1024
	}

	var (
		out   []domain.PipelineFragment
		stats collectStats
	)
	for _, exportDir := range exportDirs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		parseDir := exportDir
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
			fragments, err := parseConversationFile(path, parseDir, userMeta, &stats)
			if err != nil {
				return nil, fmt.Errorf("parse chatgpt export file %s: %w", path, err)
			}
			out = append(out, fragments...)
		}
	}

	log.Printf("chatgpt export %q: fragments=%d conversations_seen=%d attachments_not_carried=%d url_refs_not_carried=%d non_text_parts_skipped=%d",
		cfg.Name, len(out), stats.conversations, stats.attachments, stats.urlRefsSkipped, stats.nonTextSkipped)
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

func parseConversationFile(path, exportDir string, userMeta exportUserMeta, stats *collectStats) ([]domain.PipelineFragment, error) {
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
		fragment, ok := buildConversationFragment(conv, path, exportDir, userMeta, stats)
		if ok {
			out = append(out, fragment)
		}
	}
	return out, nil
}

func buildConversationFragment(conv exportConversation, sourceFile, exportDir string, userMeta exportUserMeta, stats *collectStats) (domain.PipelineFragment, bool) {
	if strings.TrimSpace(conv.ID) == "" {
		return domain.PipelineFragment{}, false
	}
	messages := conversationMessages(conv)
	if len(messages) == 0 {
		return domain.PipelineFragment{}, false
	}
	var (
		blocks       []string
		attachRefs   []any
		urlRefs      int
		nonTextParts int
	)
	createdAt := timeFromUnixFloat(conv.CreateTime)
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Author.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		text, nonText := extractTextParts(msg.Content)
		nonTextParts += nonText
		if text == "" {
			continue
		}
		blocks = append(blocks, "## "+role+"\n\n"+text)
		refs, urls := summarizeAttachments(msg)
		attachRefs = append(attachRefs, refs...)
		urlRefs += urls
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
	if nonTextParts > 0 {
		meta["non_text_parts_skipped"] = nonTextParts
	}
	// Attachments are never emitted: the transcript store fails closed on them.
	// Only counts and minimal references (no paths, no bytes) are recorded.
	if len(attachRefs) > 0 {
		meta["attachment_count"] = len(attachRefs)
		meta["attachment_refs"] = attachRefs
	}
	if urlRefs > 0 {
		meta["url_refs_skipped"] = urlRefs
	}
	if stats != nil {
		stats.conversations++
		stats.attachments += len(attachRefs)
		stats.urlRefsSkipped += urlRefs
		stats.nonTextSkipped += nonTextParts
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

// extractTextParts keeps the string parts of text and multimodal_text content
// and returns how many non-string parts (images, asset pointers) were skipped.
// Other content types (thoughts, reasoning_recap, ...) yield nothing.
func extractTextParts(content exportContent) (string, int) {
	switch content.ContentType {
	case "", "text", "multimodal_text":
	default:
		return "", 0
	}
	parts := make([]string, 0, len(content.Parts))
	nonText := 0
	for _, part := range content.Parts {
		text, ok := part.(string)
		if !ok {
			if part != nil {
				nonText++
			}
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), nonText
}

func timeFromUnixFloat(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	seconds := int64(v)
	nanos := int64((v - float64(seconds)) * float64(time.Second))
	return time.Unix(seconds, nanos).UTC()
}

// collectStats totals what the private-transcript path deliberately does not
// carry, so a run can report counts without any content.
type collectStats struct {
	conversations  int
	attachments    int
	urlRefsSkipped int
	nonTextSkipped int
}

// summarizeAttachments reduces a message's attachment metadata to counts and
// minimal references (kind, mime, size, a hash of the file name). It never opens
// or stats a file, and URL references are counted only.
func summarizeAttachments(msg exportMessage) ([]any, int) {
	var refs []any
	if raw, ok := msg.Metadata["attachments"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := firstString(m, "name", "filename", "title")
			mime := firstString(m, "mimeType", "mime_type")
			sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
			refs = append(refs, map[string]any{
				"kind":        inferAttachmentKind(name, mime, ""),
				"mime":        mime,
				"size_bytes":  firstNumber(m, "size", "size_bytes", "file_size"),
				"name_sha256": hex.EncodeToString(sum[:]),
			})
		}
	}
	return refs, countMessageURLs(msg.Metadata)
}

func countMessageURLs(meta map[string]any) int {
	seen := map[string]struct{}{}
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			if rawURL, ok := typed["url"].(string); ok && strings.TrimSpace(rawURL) != "" {
				seen[strings.TrimSpace(rawURL)] = struct{}{}
			}
			if rawURLs, ok := typed["safe_urls"].([]any); ok {
				for _, item := range rawURLs {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						seen[strings.TrimSpace(s)] = struct{}{}
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
	return len(seen)
}

func firstNumber(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if n, ok := m[key].(float64); ok && n >= 0 {
			return int64(n)
		}
	}
	return 0
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
