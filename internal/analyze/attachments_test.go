package analyze

import (
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestEnrichAttachmentMetadata_ImageAnalysisWithOCR(t *testing.T) {
	item := domain.PipelineAttachment{
		Kind: "image",
		Metadata: map[string]any{
			"image_format":           "png",
			"image_width":            1280,
			"image_height":           720,
			"ocr_status":             "ok",
			"extracted_text_preview": "roadmap diagram",
		},
	}
	out := EnrichAttachmentMetadata(item)
	if out.Metadata["analysis_version"] != "deterministic-v1" {
		t.Fatalf("unexpected analysis version: %+v", out.Metadata)
	}
	analysis, ok := out.Metadata["analysis"].(map[string]any)
	if !ok {
		t.Fatalf("expected image analysis map: %+v", out.Metadata)
	}
	if analysis["kind"] != "image" {
		t.Fatalf("unexpected analysis kind: %+v", analysis)
	}
	if analysis["summary"] == "" {
		t.Fatalf("expected analysis summary: %+v", analysis)
	}
	tags, ok := analysis["tags"].([]string)
	if !ok {
		t.Fatalf("expected typed tags slice: %+v", analysis)
	}
	if len(tags) == 0 {
		t.Fatalf("expected analysis tags: %+v", analysis)
	}
}

func TestEnrichAttachmentMetadata_ImageAnalysisWithoutOCR(t *testing.T) {
	item := domain.PipelineAttachment{
		Kind: "image",
		Metadata: map[string]any{
			"image_format": "png",
			"image_width":  1,
			"image_height": 1,
			"ocr_status":   "unavailable",
		},
	}
	out := EnrichAttachmentMetadata(item)
	analysis, ok := out.Metadata["analysis"].(map[string]any)
	if !ok {
		t.Fatalf("expected image analysis map: %+v", out.Metadata)
	}
	if analysis["summary"] == "" {
		t.Fatalf("expected summary even without OCR text: %+v", analysis)
	}
	signals, ok := analysis["signals"].(map[string]any)
	if !ok || signals["ocr_status"] != "unavailable" {
		t.Fatalf("expected analysis signals: %+v", analysis)
	}
}
