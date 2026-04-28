package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
	readability "github.com/mackee/go-readability"
)

type ExtractedContent struct {
	Title    string
	Text     string
	Metadata map[string]any
}

var pdftotextCommand = "pdftotext"
var ocrmypdfCommand = "ocrmypdf"

func ExtractArticle(body []byte, sourceURL string) (ExtractedContent, error) {
	options := readability.DefaultOptions()
	options.CharThreshold = 32
	article, err := readability.Extract(string(body), options)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("readability extract: %w", err)
	}
	text := strings.TrimSpace(readability.ExtractTextContent(article.Root))
	return ExtractedContent{
		Title: strings.TrimSpace(article.Title),
		Text:  text,
		Metadata: map[string]any{
			"extract_source_url": strings.TrimSpace(sourceURL),
			"byline":             strings.TrimSpace(article.Byline),
			"node_count":         article.NodeCount,
			"page_type":          string(article.PageType),
		},
	}, nil
}

func ExtractPDF(body []byte) (ExtractedContent, error) {
	embedded, embeddedErr := extractPDFEmbedded(body)
	if embeddedErr == nil && strings.TrimSpace(embedded.Text) != "" {
		embedded.Metadata["extractor"] = "ledongthuc/pdf"
		return embedded, nil
	}
	external, externalErr := extractPDFWithPDFToText(body)
	if externalErr == nil && strings.TrimSpace(external.Text) != "" {
		external.Metadata["extractor"] = "pdftotext"
		if embeddedErr != nil {
			external.Metadata["embedded_error"] = embeddedErr.Error()
		}
		return external, nil
	}
	ocr, ocrErr := extractPDFWithOCRMyPDF(body)
	if ocrErr == nil && strings.TrimSpace(ocr.Text) != "" {
		ocr.Metadata["extractor"] = "ocrmypdf"
		if embeddedErr != nil {
			ocr.Metadata["embedded_error"] = embeddedErr.Error()
		}
		if externalErr != nil {
			ocr.Metadata["pdftotext_error"] = externalErr.Error()
		}
		return ocr, nil
	}
	if embeddedErr == nil {
		if externalErr != nil {
			embedded.Metadata["pdftotext_error"] = externalErr.Error()
		}
		if ocrErr != nil {
			embedded.Metadata["ocrmypdf_error"] = ocrErr.Error()
		}
		embedded.Metadata["extractor"] = "ledongthuc/pdf"
		return embedded, nil
	}
	if externalErr == nil {
		external.Metadata["extractor"] = "pdftotext"
		if ocrErr != nil {
			external.Metadata["ocrmypdf_error"] = ocrErr.Error()
		}
		return external, nil
	}
	if ocrErr == nil {
		ocr.Metadata["extractor"] = "ocrmypdf"
		return ocr, nil
	}
	return ExtractedContent{}, fmt.Errorf("embedded pdf extract: %v; pdftotext fallback: %v; ocrmypdf fallback: %v", embeddedErr, externalErr, ocrErr)
}

func extractPDFEmbedded(body []byte) (ExtractedContent, error) {
	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("open pdf: %w", err)
	}
	pageSnippets, textPages, err := extractPDFPageSnippets(reader, 5)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("pdf page snippets: %w", err)
	}
	textReader, err := reader.GetPlainText()
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("pdf plain text: %w", err)
	}
	raw, err := io.ReadAll(textReader)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("read pdf text: %w", err)
	}
	return ExtractedContent{
		Text: strings.TrimSpace(string(raw)),
		Metadata: map[string]any{
			"page_count":      reader.NumPage(),
			"text_page_count": textPages,
			"page_snippets":   pageSnippets,
		},
	}, nil
}

func extractPDFPageSnippets(reader *pdf.Reader, limit int) ([]map[string]any, int, error) {
	totalPages := reader.NumPage()
	fonts := make(map[string]*pdf.Font)
	out := make([]map[string]any, 0, min(limit, totalPages))
	textPages := 0
	for i := 1; i <= totalPages; i++ {
		page := reader.Page(i)
		for _, name := range page.Fonts() {
			if _, ok := fonts[name]; !ok {
				font := page.Font(name)
				fonts[name] = &font
			}
		}
		text, err := page.GetPlainText(fonts)
		if err != nil {
			return nil, 0, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		textPages++
		if len(out) >= limit {
			continue
		}
		out = append(out, map[string]any{
			"page":       i,
			"text_bytes": len(text),
			"snippet":    previewExtractedText(text, 240),
		})
	}
	return out, textPages, nil
}

func extractPDFWithPDFToText(body []byte) (ExtractedContent, error) {
	path, err := exec.LookPath(pdftotextCommand)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("pdftotext unavailable: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "fe-pdftotext-*")
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("create pdftotext temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	pdfPath := filepath.Join(tempDir, "input.pdf")
	if err := os.WriteFile(pdfPath, body, 0o600); err != nil {
		return ExtractedContent{}, fmt.Errorf("write temp pdf: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-q", pdfPath, "-")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return ExtractedContent{}, fmt.Errorf("run pdftotext: %s", msg)
	}
	text := strings.TrimSpace(stdout.String())
	return ExtractedContent{
		Text:     text,
		Metadata: map[string]any{},
	}, nil
}

func extractPDFWithOCRMyPDF(body []byte) (ExtractedContent, error) {
	path, err := exec.LookPath(ocrmypdfCommand)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("ocrmypdf unavailable: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "fe-ocrmypdf-*")
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("create ocrmypdf temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	inputPath := filepath.Join(tempDir, "input.pdf")
	outputPath := filepath.Join(tempDir, "output.pdf")
	if err := os.WriteFile(inputPath, body, 0o600); err != nil {
		return ExtractedContent{}, fmt.Errorf("write temp pdf for ocrmypdf: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--skip-text", "--force-ocr", "--sidecar", "-", inputPath, outputPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return ExtractedContent{}, fmt.Errorf("run ocrmypdf: %s", msg)
	}
	text := strings.TrimSpace(stdout.String())
	if text != "" {
		return ExtractedContent{
			Text:     text,
			Metadata: map[string]any{},
		}, nil
	}
	ocrPDF, err := os.ReadFile(outputPath)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("read ocrmypdf output: %w", err)
	}
	result, err := extractPDFEmbedded(ocrPDF)
	if err == nil && strings.TrimSpace(result.Text) != "" {
		return result, nil
	}
	fallback, fallbackErr := extractPDFWithPDFToText(ocrPDF)
	if fallbackErr == nil && strings.TrimSpace(fallback.Text) != "" {
		return fallback, nil
	}
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("ocrmypdf produced unreadable pdf: %v", err)
	}
	return ExtractedContent{}, fmt.Errorf("ocrmypdf produced no extractable text")
}

func previewExtractedText(text string, limit int) string {
	text = strings.TrimSpace(spaceSquash(text))
	if len(text) <= limit {
		return text
	}
	if limit < 4 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func spaceSquash(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
