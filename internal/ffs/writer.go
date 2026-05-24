package ffs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type PublishResult struct {
	FragmentPath         string
	PublishedAttachments map[string]domain.PublishedAttachmentInfo
}

type publishedAttachment struct {
	Attachment  domain.FragmentAttachment
	OriginalRel string
	PreviewRel  string
}

func WritePinterestPinBundle(root string, detail domain.FragmentDetail) (PublishResult, error) {
	if strings.TrimSpace(root) == "" {
		return PublishResult{}, nil
	}
	fragment := detail.Fragment
	if fragment.Source != "manual" || fragment.SourceType != "pin" {
		return PublishResult{}, nil
	}

	rootAbs, err := filepath.Abs(config.ExpandHome(root))
	if err != nil {
		return PublishResult{}, fmt.Errorf("resolve ffs root: %w", err)
	}
	rel := PinterestPinRelativePath(fragment)
	targetDir := filepath.Join(rootAbs, rel)
	targetAbs, err := filepath.Abs(targetDir)
	if err != nil {
		return PublishResult{}, fmt.Errorf("resolve ffs path: %w", err)
	}
	prefix := rootAbs + string(filepath.Separator)
	if targetAbs != rootAbs && !strings.HasPrefix(targetAbs, prefix) {
		return PublishResult{}, fmt.Errorf("ffs path escapes root")
	}
	if err := os.MkdirAll(targetAbs, 0o750); err != nil {
		return PublishResult{}, fmt.Errorf("mkdir ffs dir: %w", err)
	}

	published, storage := publishAttachments(detail.Attachments, targetAbs)
	fragmentPath := filepath.Join(targetAbs, "fragment.md")
	if err := os.WriteFile(fragmentPath, []byte(renderPinterestPinMarkdown(detail, published)), 0o600); err != nil {
		return PublishResult{}, fmt.Errorf("write ffs fragment markdown: %w", err)
	}
	return PublishResult{
		FragmentPath:         fragmentPath,
		PublishedAttachments: storage,
	}, nil
}

func PinterestPinRelativePath(fragment domain.Fragment) string {
	meta := decodeMetadata(fragment.MetadataJSON)
	platform := strings.TrimSpace(metadataString(meta, "platform"))
	if platform == "" {
		platform = "unknown"
	}
	identifier := strings.TrimSpace(metadataString(meta, "pin_id"))
	if identifier == "" {
		identifier = strings.TrimSpace(fragment.SourceID)
	}
	if identifier == "" {
		identifier = "pin"
	}
	return filepath.Join(platform, identifier)
}

func publishAttachments(items []domain.FragmentAttachment, targetDir string) ([]publishedAttachment, map[string]domain.PublishedAttachmentInfo) {
	if len(items) == 0 {
		return nil, nil
	}
	attachmentDir := filepath.Join(targetDir, "attachments")
	previewDir := filepath.Join(attachmentDir, "previews")
	_ = os.MkdirAll(attachmentDir, 0o750)
	_ = os.MkdirAll(previewDir, 0o750)

	out := make([]publishedAttachment, 0, len(items))
	storage := make(map[string]domain.PublishedAttachmentInfo)
	for _, item := range items {
		published := publishedAttachment{Attachment: item}
		info := domain.PublishedAttachmentInfo{}
		if src := firstNonEmpty(strings.TrimSpace(item.StoragePath), strings.TrimSpace(item.SourcePath)); src != "" {
			name := attachmentName(item)
			dst := filepath.Join(attachmentDir, name)
			if err := copyFileIfNeeded(src, dst); err == nil {
				published.OriginalRel = filepath.ToSlash(filepath.Join("attachments", name))
				info.StoragePath = dst
			}
		}
		if src := strings.TrimSpace(item.PreviewStoragePath); src != "" {
			name := strings.TrimSuffix(attachmentName(item), filepath.Ext(attachmentName(item))) + ".preview.jpg"
			dst := filepath.Join(previewDir, name)
			if err := copyFileIfNeeded(src, dst); err == nil {
				published.PreviewRel = filepath.ToSlash(filepath.Join("attachments", "previews", name))
				info.PreviewStoragePath = dst
			}
		}
		out = append(out, published)
		if item.ID != "" && (info.StoragePath != "" || info.PreviewStoragePath != "") {
			storage[item.ID] = info
		}
	}
	return out, storage
}

func copyFileIfNeeded(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("source %s is a directory", src)
	}
	if info, err := os.Stat(dst); err == nil {
		if info.Size() == srcInfo.Size() && info.ModTime().Equal(srcInfo.ModTime()) {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
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
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, time.Now(), srcInfo.ModTime())
}

func attachmentName(item domain.FragmentAttachment) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = filepath.Base(firstNonEmpty(item.StoragePath, item.SourcePath))
	}
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	if name == "" {
		name = item.ID
	}
	if len(item.ID) >= 12 {
		return item.ID[:12] + "-" + name
	}
	return item.ID + "-" + name
}

func renderPinterestPinMarkdown(detail domain.FragmentDetail, attachments []publishedAttachment) string {
	fragment := detail.Fragment
	meta := decodeMetadata(fragment.MetadataJSON)
	url := pinterestSourceURL(detail, meta)
	description := strings.TrimSpace(firstNonEmpty(metadataString(meta, "user_description"), metadataString(meta, "pin_description"), fragment.Summary))
	notes := strings.TrimSpace(metadataString(meta, "user_notes"))
	tags := pinterestTagValues(detail.Entities, meta)
	hero := ""
	for _, item := range attachments {
		rel := firstNonEmpty(item.PreviewRel, item.OriginalRel)
		if rel != "" && isImageAttachment(item.Attachment) {
			hero = rel
			break
		}
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + fragment.ID + "\n")
	b.WriteString("source: " + fragment.Source + "\n")
	b.WriteString("source_type: " + fragment.SourceType + "\n")
	b.WriteString("source_id: " + fragment.SourceID + "\n")
	b.WriteString("canonical_path: " + fragment.CanonicalPath + "\n")
	if pinID := metadataString(meta, "pin_id"); pinID != "" {
		b.WriteString("pin_id: " + pinID + "\n")
	}
	if url != "" {
		b.WriteString("url: " + url + "\n")
	}
	if len(tags) > 0 {
		b.WriteString("tags:\n")
		for _, tag := range tags {
			b.WriteString("  - " + tag + "\n")
		}
	}
	b.WriteString("---\n\n")
	b.WriteString("# " + firstNonEmpty(fragment.Title, "Pinterest pin") + "\n\n")
	if hero != "" {
		b.WriteString("![](" + hero + ")\n\n")
	}
	if description != "" {
		b.WriteString("## Description\n\n")
		b.WriteString(description + "\n\n")
	}
	if notes != "" {
		b.WriteString("## Notes\n\n")
		b.WriteString(notes + "\n\n")
	}
	if len(tags) > 0 {
		b.WriteString("## Tags\n\n")
		for _, tag := range tags {
			b.WriteString("- " + tag + "\n")
		}
		b.WriteString("\n")
	}
	if url != "" {
		b.WriteString("## Source\n\n")
		b.WriteString("- [Open pin](" + url + ")\n\n")
	}
	if len(attachments) > 0 {
		b.WriteString("## Attachments\n\n")
		for _, item := range attachments {
			label := firstNonEmpty(strings.TrimSpace(item.Attachment.Name), item.Attachment.ID)
			rel := firstNonEmpty(item.OriginalRel, item.PreviewRel)
			if rel == "" {
				continue
			}
			b.WriteString("- [" + label + "](" + rel + ")\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Captured Content\n\n")
	if strings.TrimSpace(fragment.Content) == "" {
		b.WriteString("_No content captured._\n")
	} else {
		b.WriteString("```\n")
		b.WriteString(strings.TrimSpace(fragment.Content) + "\n")
		b.WriteString("```\n")
	}
	return b.String()
}

func pinterestSourceURL(detail domain.FragmentDetail, meta map[string]any) string {
	if url := strings.TrimSpace(metadataString(meta, "url")); url != "" {
		return url
	}
	for _, item := range detail.Attachments {
		if strings.TrimSpace(item.ExternalURL) == "" {
			continue
		}
		if item.Role == "source_url" || item.Role == "reference" {
			return item.ExternalURL
		}
	}
	return strings.TrimSpace(fragmentContentURL(detail.Fragment.Content))
}

func pinterestTagValues(entities []domain.FragmentEntity, meta map[string]any) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, entity := range entities {
		if entity.Kind != "tag" {
			continue
		}
		tag := strings.TrimSpace(entity.Value)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	if raw, ok := meta["user_tags"].([]any); ok {
		for _, item := range raw {
			tag, ok := item.(string)
			if !ok {
				continue
			}
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			key := strings.ToLower(tag)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, tag)
		}
	}
	slices.Sort(out)
	return out
}

func decodeMetadata(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil || meta == nil {
		return map[string]any{}
	}
	return meta
}

func metadataString(meta map[string]any, key string) string {
	value, ok := meta[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func fragmentContentURL(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "http://") || strings.HasPrefix(content, "https://") {
		return content
	}
	return ""
}

func isImageAttachment(item domain.FragmentAttachment) bool {
	if strings.EqualFold(item.Kind, "image") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.MIMEType)), "image/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
