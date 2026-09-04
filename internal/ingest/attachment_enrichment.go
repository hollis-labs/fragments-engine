package ingest

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
)

func EnrichAttachmentContent(ctx context.Context, candidate domain.PipelineFragment, vision analyze.VisionAnalyzer) (domain.PipelineFragment, error) {
	if len(candidate.Attachments) == 0 {
		return candidate, nil
	}
	enriched := candidate
	var sections []string
	count := 0
	for i := range enriched.Attachments {
		item := enriched.Attachments[i]
		if strings.TrimSpace(item.SourcePath) == "" {
			continue
		}
		out, err := extract.ExtractLocalAttachment(item.SourcePath, item.MIMEType, item.Kind)
		if err != nil {
			continue
		}
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
		for key, value := range out.Metadata {
			item.Metadata[key] = value
		}
		if strings.TrimSpace(out.Text) != "" {
			item.Metadata["extracted_text_bytes"] = len(out.Text)
			item.Metadata["extracted_text_preview"] = previewText(out.Text, 280)
		}
		if out.Title != "" {
			item.Metadata["extracted_title"] = out.Title
		}
		if item.Kind == "file" {
			item.Kind = inferredAttachmentTextKind(item.SourcePath)
		}
		item = analyze.EnrichAttachmentMetadata(item)
		if vision != nil && item.Kind == "image" {
			if visionOut, err := vision.AnalyzeImage(ctx, item.SourcePath, item.Metadata); err == nil && len(visionOut) > 0 {
				backend := vision.Backend()
				if raw, ok := visionOut["_provider_backend"].(string); ok && strings.TrimSpace(raw) != "" {
					backend = strings.TrimSpace(raw)
				}
				delete(visionOut, "_provider_backend")
				item.Metadata["vision_analysis"] = visionOut
				item.Metadata["vision_analysis_backend"] = backend
			} else if err != nil {
				item.Metadata["vision_analysis_backend"] = vision.Backend()
				item.Metadata["vision_analysis_error"] = err.Error()
			}
		}
		enriched.Attachments[i] = item
		count++
		if strings.TrimSpace(out.Text) != "" {
			sections = append(sections, renderAttachmentSection(item, out))
		}
	}
	if len(sections) > 0 {
		enriched.Content = strings.TrimSpace(enriched.Content) + "\n\n## attachments\n\n" + strings.Join(sections, "\n\n")
	}
	if enriched.Metadata == nil {
		enriched.Metadata = map[string]any{}
	}
	if count > 0 {
		enriched.Metadata["attachment_text_enrichment_count"] = count
	}
	return enriched, nil
}

func renderAttachmentSection(item domain.PipelineAttachment, out extract.ExtractedContent) string {
	title := strings.TrimSpace(item.Name)
	if title == "" {
		title = filepath.Base(item.SourcePath)
	}
	if title == "" {
		title = "attachment"
	}
	var b strings.Builder
	b.WriteString("### attachment: " + title + "\n")
	b.WriteString("kind: " + inferredAttachmentTextKindFromCurrent(item) + "\n")
	if out.Title != "" && !strings.EqualFold(out.Title, title) {
		b.WriteString("title: " + out.Title + "\n")
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(out.Text))
	return b.String()
}

func inferredAttachmentTextKindFromCurrent(item domain.PipelineAttachment) string {
	if strings.TrimSpace(item.Kind) != "" && item.Kind != "file" {
		return item.Kind
	}
	return inferredAttachmentTextKind(item.SourcePath)
}

func inferredAttachmentTextKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdx":
		return "markdown"
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rb", ".rs", ".java", ".c", ".cc", ".cpp", ".h", ".hpp", ".css", ".scss", ".sh", ".zsh", ".bash", ".sql":
		return "code"
	default:
		return "text"
	}
}

func previewText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	if limit < 4 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func validateEnrichedCandidate(candidate domain.PipelineFragment) error {
	if strings.TrimSpace(candidate.Content) == "" && strings.TrimSpace(candidate.Title) == "" &&
		strings.TrimSpace(candidate.Description) == "" && len(candidate.Attachments) == 0 &&
		!validEnrichedSourceURL(candidate.SourceIdentity.SubmittedURL) &&
		!validEnrichedSourceURL(candidate.SourceIdentity.CanonicalURL) {
		return fmt.Errorf("enriched candidate has no source material")
	}
	return nil
}

func validEnrichedSourceURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
