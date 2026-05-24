package service

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type PinterestCorpusWriter struct {
	root string
}

func NewPinterestCorpusWriter(root string) *PinterestCorpusWriter {
	return &PinterestCorpusWriter{root: strings.TrimSpace(root)}
}

func (w *PinterestCorpusWriter) Write(detail domain.FragmentDetail) (string, error) {
	if w == nil || strings.TrimSpace(w.root) == "" {
		return "", nil
	}
	fragment := detail.Fragment
	if fragment.Source != "manual" || fragment.SourceType != "pin" {
		return "", nil
	}

	rootAbs, err := filepath.Abs(config.ExpandHome(w.root))
	if err != nil {
		return "", fmt.Errorf("resolve pinterest corpus root: %w", err)
	}
	rel := strings.TrimPrefix(filepath.Clean(filepath.FromSlash(fragment.CanonicalPath)), string(filepath.Separator))
	if rel == "" {
		rel = filepath.Join("fragments", "manual", "pin", fragment.SourceID)
	}
	targetDir := filepath.Join(rootAbs, rel)
	targetAbs, err := filepath.Abs(targetDir)
	if err != nil {
		return "", fmt.Errorf("resolve pinterest corpus path: %w", err)
	}
	prefix := rootAbs + string(filepath.Separator)
	if targetAbs != rootAbs && !strings.HasPrefix(targetAbs, prefix) {
		return "", fmt.Errorf("pinterest corpus path escapes root")
	}
	if err := os.MkdirAll(targetAbs, 0o750); err != nil {
		return "", fmt.Errorf("mkdir pinterest corpus dir: %w", err)
	}

	published := publishCorpusAttachments(detail.Attachments, targetAbs)
	fragmentPath := filepath.Join(targetAbs, "fragment.md")
	if err := os.WriteFile(fragmentPath, []byte(renderPinterestCorpusMarkdown(detail, published)), 0o600); err != nil {
		return "", fmt.Errorf("write pinterest corpus markdown: %w", err)
	}
	return fragmentPath, nil
}

type publishedCorpusAttachment struct {
	Attachment  domain.FragmentAttachment
	OriginalRel string
	PreviewRel  string
}

func publishCorpusAttachments(items []domain.FragmentAttachment, targetDir string) []publishedCorpusAttachment {
	if len(items) == 0 {
		return nil
	}
	attachmentDir := filepath.Join(targetDir, "attachments")
	previewDir := filepath.Join(attachmentDir, "previews")
	_ = os.MkdirAll(attachmentDir, 0o750)
	_ = os.MkdirAll(previewDir, 0o750)

	out := make([]publishedCorpusAttachment, 0, len(items))
	for _, item := range items {
		published := publishedCorpusAttachment{Attachment: item}
		if src := firstNonEmpty(strings.TrimSpace(item.StoragePath), strings.TrimSpace(item.SourcePath)); src != "" {
			name := corpusAttachmentName(item)
			dst := filepath.Join(attachmentDir, name)
			if err := copyFileIfNeeded(src, dst); err == nil {
				published.OriginalRel = filepath.ToSlash(filepath.Join("attachments", name))
			}
		}
		if src := strings.TrimSpace(item.PreviewStoragePath); src != "" {
			name := strings.TrimSuffix(corpusAttachmentName(item), filepath.Ext(corpusAttachmentName(item))) + ".preview.jpg"
			dst := filepath.Join(previewDir, name)
			if err := copyFileIfNeeded(src, dst); err == nil {
				published.PreviewRel = filepath.ToSlash(filepath.Join("attachments", "previews", name))
			}
		}
		out = append(out, published)
	}
	return out
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

func corpusAttachmentName(item domain.FragmentAttachment) string {
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

func renderPinterestCorpusMarkdown(detail domain.FragmentDetail, attachments []publishedCorpusAttachment) string {
	fragment := detail.Fragment
	meta := decodeFragmentMetadata(fragment.MetadataJSON)
	url := pinterestSourceURL(detail, meta)
	description := strings.TrimSpace(firstNonEmpty(metadataString(meta, "user_description"), metadataString(meta, "pin_description"), fragment.Summary))
	notes := strings.TrimSpace(metadataString(meta, "user_notes"))
	tags := pinterestTagValues(detail.Entities)
	hero := ""
	for _, item := range attachments {
		rel := firstNonEmpty(item.PreviewRel, item.OriginalRel)
		if rel != "" && isCorpusImageAttachment(item.Attachment) {
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
	return ""
}

func pinterestTagValues(entities []domain.FragmentEntity) []string {
	values := make([]string, 0, len(entities))
	seen := map[string]struct{}{}
	for _, entity := range entities {
		if entity.Kind != "tag" || strings.TrimSpace(entity.Value) == "" {
			continue
		}
		key := strings.ToLower(entity.Value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, entity.Value)
	}
	slices.Sort(values)
	return values
}

func isCorpusImageAttachment(item domain.FragmentAttachment) bool {
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
