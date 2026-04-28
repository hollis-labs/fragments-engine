package ingest

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestEnrichAttachmentContent_AppendsMarkdownAndPersistsMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.md")
	raw := `---
title: Attachment Roadmap
project: fragments
---

The attachment body should be searchable.
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write markdown fixture: %v", err)
	}
	candidate := domain.PipelineFragment{
		Source:     "chatgpt",
		SourceType: "chat",
		SourceID:   "conv-1",
		Title:      "ChatGPT chat: attachment test",
		Content:    "## user\nPlease review the note.",
		Attachments: []domain.PipelineAttachment{
			{
				Kind:       "markdown",
				Role:       "attachment",
				Name:       "note.md",
				MIMEType:   "text/markdown",
				SourcePath: path,
				Source:     "chatgpt_export",
			},
		},
	}
	out, err := EnrichAttachmentContent(context.Background(), candidate, nil)
	if err != nil {
		t.Fatalf("enrich attachment content: %v", err)
	}
	if !strings.Contains(out.Content, "## attachments") {
		t.Fatalf("expected attachments section, got %q", out.Content)
	}
	if !strings.Contains(out.Content, "The attachment body should be searchable.") {
		t.Fatalf("expected attachment body in content, got %q", out.Content)
	}
	if out.Metadata["attachment_text_enrichment_count"] != 1 {
		t.Fatalf("unexpected fragment metadata: %+v", out.Metadata)
	}
	if out.Attachments[0].Metadata["extractor"] != "markdown_frontmatter" {
		t.Fatalf("unexpected attachment metadata: %+v", out.Attachments[0].Metadata)
	}
	if out.Attachments[0].Metadata["extracted_title"] != "Attachment Roadmap" {
		t.Fatalf("expected extracted title metadata, got %+v", out.Attachments[0].Metadata)
	}
	if _, ok := out.Attachments[0].Metadata["frontmatter"]; !ok {
		t.Fatalf("expected frontmatter metadata, got %+v", out.Attachments[0].Metadata)
	}
}

func TestEnrichAttachmentContent_PersistsImageMetadataWithoutText(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "diagram.png")
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	if err := os.WriteFile(path, rawPNG, 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	candidate := domain.PipelineFragment{
		Source:     "chatgpt",
		SourceType: "chat",
		SourceID:   "conv-2",
		Title:      "ChatGPT chat: image metadata test",
		Content:    "## user\nPlease review the diagram.",
		Attachments: []domain.PipelineAttachment{
			{
				Kind:       "image",
				Role:       "attachment",
				Name:       "diagram.png",
				MIMEType:   "image/png",
				SourcePath: path,
				Source:     "chatgpt_export",
			},
		},
	}
	out, err := EnrichAttachmentContent(context.Background(), candidate, nil)
	if err != nil {
		t.Fatalf("enrich image metadata: %v", err)
	}
	if strings.Contains(out.Content, "## attachments") {
		t.Fatalf("did not expect attachment text section when no ocr text is present: %q", out.Content)
	}
	if out.Metadata["attachment_text_enrichment_count"] != 1 {
		t.Fatalf("expected attachment enrichment count metadata: %+v", out.Metadata)
	}
	if out.Attachments[0].Metadata["image_width"] != 1 || out.Attachments[0].Metadata["image_height"] != 1 {
		t.Fatalf("expected persisted image metadata: %+v", out.Attachments[0].Metadata)
	}
	if out.Attachments[0].Metadata["ocr_status"] != "unavailable" {
		t.Fatalf("expected ocr unavailable metadata: %+v", out.Attachments[0].Metadata)
	}
}
