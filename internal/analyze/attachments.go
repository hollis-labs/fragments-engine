package analyze

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func EnrichAttachmentMetadata(item domain.PipelineAttachment) domain.PipelineAttachment {
	if item.Metadata == nil {
		item.Metadata = map[string]any{}
	}
	switch item.Kind {
	case "image":
		item.Metadata["analysis"] = analyzeImage(item.Metadata)
		item.Metadata["analysis_version"] = "deterministic-v1"
	}
	return item
}

func analyzeImage(meta map[string]any) map[string]any {
	format := strings.TrimSpace(stringValue(meta["image_format"]))
	width := intValue(meta["image_width"])
	height := intValue(meta["image_height"])
	ocrStatus := strings.TrimSpace(stringValue(meta["ocr_status"]))
	ocrText := strings.TrimSpace(stringValue(meta["extracted_text_preview"]))

	tags := []string{"image"}
	if format != "" {
		tags = append(tags, format)
	}
	if width > 0 && height > 0 {
		switch {
		case width == height:
			tags = append(tags, "square")
		case width > height:
			tags = append(tags, "landscape")
		default:
			tags = append(tags, "portrait")
		}
	}
	switch {
	case ocrText != "":
		tags = append(tags, "text_present")
	case ocrStatus == "unavailable":
		tags = append(tags, "ocr_unavailable")
	default:
		tags = append(tags, "text_absent")
	}
	sort.Strings(tags)

	summaryParts := []string{"Image"}
	if format != "" {
		summaryParts = append(summaryParts, "("+format)
		if width > 0 && height > 0 {
			summaryParts[len(summaryParts)-1] += fmt.Sprintf(", %dx%d", width, height)
		}
		summaryParts[len(summaryParts)-1] += ")"
	} else if width > 0 && height > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("(%dx%d)", width, height))
	}
	switch {
	case ocrText != "":
		summaryParts = append(summaryParts, "with extracted text.")
	case ocrStatus == "unavailable":
		summaryParts = append(summaryParts, "without OCR text; OCR unavailable.")
	default:
		summaryParts = append(summaryParts, "without extracted text.")
	}

	return map[string]any{
		"kind":    "image",
		"summary": strings.Join(summaryParts, " "),
		"tags":    tags,
		"signals": map[string]any{
			"format":     format,
			"width":      width,
			"height":     height,
			"ocr_status": ocrStatus,
			"has_text":   ocrText != "",
		},
	}
}

func stringValue(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func intValue(v any) int {
	switch typed := v.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
