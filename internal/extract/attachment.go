package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

import (
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

const maxAttachmentTextBytes = 256 * 1024

var tesseractCommand = "tesseract"
var swiftCommand = "swift"

func ExtractLocalAttachment(path, mimeType, kind string) (ExtractedContent, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case kind == "docx", ext == ".docx":
		return extractDOCXAttachment(path)
	case kind == "image", isImageExt(ext):
		return extractImageAttachment(path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("read attachment: %w", err)
	}
	if len(raw) > maxAttachmentTextBytes {
		raw = raw[:maxAttachmentTextBytes]
	}
	if !utf8.Valid(raw) {
		return ExtractedContent{}, fmt.Errorf("attachment is not valid utf-8 text")
	}
	switch {
	case kind == "markdown", ext == ".md", ext == ".markdown", ext == ".mdx":
		return extractMarkdownAttachment(raw, path)
	case kind == "text", kind == "code", kind == "json", kind == "xml", kind == "file", isTextLikeExt(ext):
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return ExtractedContent{}, fmt.Errorf("attachment text is empty")
		}
		return ExtractedContent{
			Text: text,
			Metadata: map[string]any{
				"extractor": "plain_text",
				"file_ext":  ext,
			},
		}, nil
	default:
		return ExtractedContent{}, fmt.Errorf("unsupported local attachment kind for text extraction")
	}
}

func SetTesseractCommandForTest(command string) func() {
	old := tesseractCommand
	tesseractCommand = command
	return func() {
		tesseractCommand = old
	}
}

func extractMarkdownAttachment(raw []byte, path string) (ExtractedContent, error) {
	body := string(raw)
	frontmatter, markdownBody := parseYAMLFrontmatter(body)
	text := strings.TrimSpace(markdownBody)
	if text == "" {
		return ExtractedContent{}, fmt.Errorf("markdown attachment body is empty")
	}
	meta := map[string]any{
		"extractor": "markdown_frontmatter",
		"file_ext":  strings.ToLower(filepath.Ext(path)),
	}
	if len(frontmatter) > 0 {
		meta["frontmatter"] = frontmatter
		meta["frontmatter_keys"] = sortedMapKeys(frontmatter)
	}
	title := ""
	if rawTitle, ok := frontmatter["title"].(string); ok {
		title = strings.TrimSpace(rawTitle)
	}
	return ExtractedContent{
		Title:    title,
		Text:     text,
		Metadata: meta,
	}, nil
}

func extractDOCXAttachment(path string) (ExtractedContent, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("open docx: %w", err)
	}
	defer reader.Close()

	documentXML, err := readZipFile(&reader.Reader, "word/document.xml")
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("read docx document.xml: %w", err)
	}
	text := strings.TrimSpace(parseDOCXText(documentXML))
	if text == "" {
		return ExtractedContent{}, fmt.Errorf("docx text is empty")
	}
	title := ""
	if coreXML, err := readZipFile(&reader.Reader, "docProps/core.xml"); err == nil {
		title = parseDOCXCoreTitle(coreXML)
	}
	return ExtractedContent{
		Title: title,
		Text:  text,
		Metadata: map[string]any{
			"extractor": "docx_zip_xml",
			"file_ext":  ".docx",
		},
	}, nil
}

func extractImageAttachment(path string) (ExtractedContent, error) {
	meta := map[string]any{
		"file_ext": strings.ToLower(filepath.Ext(path)),
	}
	if cfg, format, err := imageConfig(path); err == nil {
		meta["image_width"] = cfg.Width
		meta["image_height"] = cfg.Height
		meta["image_format"] = format
		if cfg.Height > 0 {
			meta["image_aspect_ratio"] = fmt.Sprintf("%.3f", float64(cfg.Width)/float64(cfg.Height))
		}
	}
	ocr, extractor, err := extractImageOCR(path)
	if err != nil {
		meta["ocr_status"] = "unavailable"
		meta["ocr_error"] = err.Error()
		return ExtractedContent{
			Metadata: meta,
		}, nil
	}
	meta["ocr_status"] = "ok"
	meta["ocr_extractor"] = extractor
	return ExtractedContent{
		Text:     ocr,
		Metadata: meta,
	}, nil
}

func extractImageOCR(path string) (string, string, error) {
	if runtime.GOOS == "darwin" {
		if text, err := extractImageWithAppleVision(path); err == nil {
			return text, "apple_vision", nil
		}
	}
	text, err := extractImageWithTesseract(path)
	if err != nil {
		return "", "", err
	}
	return text, "tesseract", nil
}

func parseYAMLFrontmatter(body string) (map[string]any, string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, "---\n") {
		return nil, body
	}
	rest := strings.TrimPrefix(body, "---\n")
	endIdx := strings.Index(rest, "\n---\n")
	endTokenLen := len("\n---\n")
	if endIdx == -1 {
		endIdx = strings.Index(rest, "\n...\n")
		endTokenLen = len("\n...\n")
	}
	if endIdx == -1 {
		return nil, body
	}

	rawFM := rest[:endIdx]
	content := rest[endIdx+endTokenLen:]
	var parsed map[string]any
	dec := yaml.NewDecoder(bytes.NewBufferString(rawFM))
	if err := dec.Decode(&parsed); err != nil || len(parsed) == 0 {
		return nil, body
	}
	return parsed, content
}

func readZipFile(reader *zip.Reader, name string) ([]byte, error) {
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("missing %s", name)
}

type docxDocument struct {
	Paragraphs []struct {
		Runs []struct {
			Text string `xml:"t"`
		} `xml:"r"`
	} `xml:"body>p"`
}

func parseDOCXText(raw []byte) string {
	var doc docxDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	var paragraphs []string
	for _, p := range doc.Paragraphs {
		var parts []string
		for _, run := range p.Runs {
			text := strings.TrimSpace(run.Text)
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			paragraphs = append(paragraphs, strings.Join(parts, " "))
		}
	}
	return strings.Join(paragraphs, "\n\n")
}

type docxCoreProperties struct {
	Title string `xml:"title"`
}

func parseDOCXCoreTitle(raw []byte) string {
	var props docxCoreProperties
	if err := xml.Unmarshal(raw, &props); err != nil {
		return ""
	}
	return strings.TrimSpace(props.Title)
}

func sortedMapKeys(in map[string]any) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func extractImageWithTesseract(path string) (string, error) {
	bin, err := exec.LookPath(tesseractCommand)
	if err != nil {
		return "", fmt.Errorf("tesseract unavailable: %w", err)
	}
	cmd := exec.Command(bin, path, "stdout")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run tesseract: %s", summarizeTesseractFailure(path, strings.TrimSpace(stderr.String()), err))
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return "", fmt.Errorf("tesseract produced no text")
	}
	return text, nil
}

func extractImageWithAppleVision(path string) (string, error) {
	bin, err := exec.LookPath(swiftCommand)
	if err != nil {
		return "", fmt.Errorf("apple vision swift unavailable: %w", err)
	}
	script, err := appleVisionOCRScript()
	if err != nil {
		return "", err
	}
	scriptPath := filepath.Join(os.TempDir(), "fragments-engine-apple-vision-ocr.swift")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return "", fmt.Errorf("write apple vision script: %w", err)
	}
	cmd := exec.Command(bin, scriptPath, path)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("run apple vision ocr: %s", msg)
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return "", fmt.Errorf("apple vision produced no text")
	}
	return text, nil
}

func appleVisionOCRScript() (string, error) {
	return `import Foundation
import Vision
import AppKit
let path = CommandLine.arguments[1]
guard let image = NSImage(contentsOfFile: path) else { fputs("load failed\n", stderr); exit(1) }
guard let tiff = image.tiffRepresentation, let bitmap = NSBitmapImageRep(data: tiff), let cg = bitmap.cgImage else { fputs("cg failed\n", stderr); exit(1) }
let request = VNRecognizeTextRequest()
request.recognitionLevel = .accurate
let handler = VNImageRequestHandler(cgImage: cg, options: [:])
do { try handler.perform([request]) } catch { fputs("vision failed: \(error)\n", stderr); exit(1) }
let texts = (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }
print(texts.joined(separator: "\n"))`, nil
}

func summarizeTesseractFailure(path, stderr string, fallback error) string {
	msg := strings.TrimSpace(stderr)
	if msg == "" && fallback != nil {
		msg = fallback.Error()
	}
	if strings.Contains(msg, "image file not found") && fileExists(path) {
		return "local tesseract cannot read image files on this machine"
	}
	return msg
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func imageConfig(path string) (image.Config, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, "", err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return image.Config{}, "", err
	}
	return cfg, format, nil
}

func isTextLikeExt(ext string) bool {
	switch ext {
	case ".txt", ".text", ".md", ".markdown", ".mdx", ".rst",
		".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rb", ".rs", ".java", ".c", ".cc", ".cpp", ".h", ".hpp",
		".css", ".scss", ".html", ".htm", ".json", ".yaml", ".yml", ".toml", ".xml", ".csv", ".sql", ".sh", ".zsh", ".bash":
		return true
	default:
		return false
	}
}

func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	default:
		return false
	}
}
