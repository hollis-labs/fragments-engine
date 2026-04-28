package extract

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractLocalAttachment_MarkdownFrontmatter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.md")
	raw := `---
title: Roadmap Note
tags:
  - fe
  - ingest
status: draft
---

# Heading

Deterministic ingest first.
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write markdown fixture: %v", err)
	}
	out, err := ExtractLocalAttachment(path, "text/markdown", "markdown")
	if err != nil {
		t.Fatalf("extract markdown attachment: %v", err)
	}
	if out.Title != "Roadmap Note" {
		t.Fatalf("unexpected extracted title: %s", out.Title)
	}
	if !strings.Contains(out.Text, "Deterministic ingest first.") {
		t.Fatalf("unexpected extracted markdown text: %q", out.Text)
	}
	if out.Metadata["extractor"] != "markdown_frontmatter" {
		t.Fatalf("unexpected metadata: %+v", out.Metadata)
	}
	fm, ok := out.Metadata["frontmatter"].(map[string]any)
	if !ok || fm["status"] != "draft" {
		t.Fatalf("expected parsed frontmatter metadata, got %+v", out.Metadata)
	}
}

func TestExtractLocalAttachment_TextCode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write code fixture: %v", err)
	}
	out, err := ExtractLocalAttachment(path, "text/plain", "code")
	if err != nil {
		t.Fatalf("extract code attachment: %v", err)
	}
	if !strings.Contains(out.Text, "func main()") {
		t.Fatalf("unexpected extracted code text: %q", out.Text)
	}
	if out.Metadata["extractor"] != "plain_text" {
		t.Fatalf("unexpected code metadata: %+v", out.Metadata)
	}
}

func TestExtractLocalAttachment_DOCX(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.docx")
	if err := writeDOCXFixture(path, "Roadmap Doc", []string{"Deterministic ingest first.", "Then recall and routing."}); err != nil {
		t.Fatalf("write docx fixture: %v", err)
	}
	out, err := ExtractLocalAttachment(path, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx")
	if err != nil {
		t.Fatalf("extract docx attachment: %v", err)
	}
	if out.Title != "Roadmap Doc" {
		t.Fatalf("unexpected docx title: %s", out.Title)
	}
	if !strings.Contains(out.Text, "Deterministic ingest first.") || !strings.Contains(out.Text, "Then recall and routing.") {
		t.Fatalf("unexpected docx text: %q", out.Text)
	}
	if out.Metadata["extractor"] != "docx_zip_xml" {
		t.Fatalf("unexpected docx metadata: %+v", out.Metadata)
	}
}

func TestExtractLocalAttachment_ImageOCR(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "diagram.png")
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	if err := os.WriteFile(imagePath, rawPNG, 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	scriptPath := filepath.Join(root, "tesseract")
	script := "#!/bin/sh\necho 'diagram roadmap text'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write tesseract script: %v", err)
	}
	restore := SetTesseractCommandForTest(scriptPath)
	defer restore()

	out, err := ExtractLocalAttachment(imagePath, "image/png", "image")
	if err != nil {
		t.Fatalf("extract image attachment: %v", err)
	}
	if !strings.Contains(out.Text, "diagram roadmap text") {
		t.Fatalf("unexpected ocr text: %q", out.Text)
	}
	if out.Metadata["image_width"] != 1 || out.Metadata["image_height"] != 1 || out.Metadata["image_format"] != "png" {
		t.Fatalf("unexpected image dimension metadata: %+v", out.Metadata)
	}
	if out.Metadata["ocr_status"] != "ok" {
		t.Fatalf("unexpected ocr status metadata: %+v", out.Metadata)
	}
	if out.Metadata["file_ext"] != ".png" {
		t.Fatalf("unexpected image metadata: %+v", out.Metadata)
	}
}

func TestExtractLocalAttachment_ImageMetadataWithoutOCR(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "diagram.png")
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	if err := os.WriteFile(imagePath, rawPNG, 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	restore := SetTesseractCommandForTest(filepath.Join(root, "missing-tesseract"))
	defer restore()

	out, err := ExtractLocalAttachment(imagePath, "image/png", "image")
	if err != nil {
		t.Fatalf("extract image metadata without ocr: %v", err)
	}
	if out.Text != "" {
		t.Fatalf("expected empty text when ocr unavailable, got %q", out.Text)
	}
	if out.Metadata["image_width"] != 1 || out.Metadata["image_height"] != 1 {
		t.Fatalf("expected image metadata even without ocr: %+v", out.Metadata)
	}
	if out.Metadata["ocr_status"] != "unavailable" {
		t.Fatalf("expected unavailable ocr status: %+v", out.Metadata)
	}
}

func writeDOCXFixture(path, title string, paragraphs []string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	doc, err := zw.Create("word/document.xml")
	if err != nil {
		return err
	}
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><document><body>`)
	for _, p := range paragraphs {
		body.WriteString(`<p><r><t>`)
		body.WriteString(p)
		body.WriteString(`</t></r></p>`)
	}
	body.WriteString(`</body></document>`)
	if _, err := doc.Write(body.Bytes()); err != nil {
		return err
	}
	core, err := zw.Create("docProps/core.xml")
	if err != nil {
		return err
	}
	if _, err := core.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><coreProperties><title>` + title + `</title></coreProperties>`)); err != nil {
		return err
	}
	return zw.Close()
}
