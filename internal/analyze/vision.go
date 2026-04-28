package analyze

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

type VisionAnalyzer interface {
	Backend() string
	AnalyzeImage(ctx context.Context, path string, current map[string]any) (map[string]any, error)
}

func NewVisionAnalyzer(cfg config.AttachmentAnalysisConfig) VisionAnalyzer {
	primary := buildVisionAnalyzer(strings.ToLower(strings.TrimSpace(cfg.Backend)), cfg)
	fallback := buildVisionAnalyzer(strings.ToLower(strings.TrimSpace(cfg.FallbackBackend)), cfg)
	if primary == nil {
		return nil
	}
	if fallback == nil || fallback.Backend() == primary.Backend() {
		return primary
	}
	return &fallbackVisionAnalyzer{
		primary:       primary,
		fallback:      fallback,
		minConfidence: cfg.MinConfidence,
	}
}

func buildVisionAnalyzer(backend string, cfg config.AttachmentAnalysisConfig) VisionAnalyzer {
	switch backend {
	case "", "none":
		return nil
	case "ollama":
		return NewOllamaVisionAnalyzer(cfg.Ollama)
	case "openai":
		return NewOpenAIVisionAnalyzer(cfg.OpenAI)
	default:
		return nil
	}
}

type fallbackVisionAnalyzer struct {
	primary       VisionAnalyzer
	fallback      VisionAnalyzer
	minConfidence float64
}

func (a *fallbackVisionAnalyzer) Backend() string {
	return a.primary.Backend()
}

func (a *fallbackVisionAnalyzer) AnalyzeImage(ctx context.Context, path string, current map[string]any) (map[string]any, error) {
	out, err := a.primary.AnalyzeImage(ctx, path, current)
	if err == nil && !needsFallback(out, a.minConfidence) {
		return out, nil
	}
	fallbackOut, fallbackErr := a.fallback.AnalyzeImage(ctx, path, current)
	if fallbackErr == nil {
		return fallbackOut, nil
	}
	if err != nil {
		return nil, fmt.Errorf("primary %s failed: %v; fallback %s failed: %w", a.primary.Backend(), err, a.fallback.Backend(), fallbackErr)
	}
	return nil, fmt.Errorf("primary %s fell below confidence threshold; fallback %s failed: %w", a.primary.Backend(), a.fallback.Backend(), fallbackErr)
}

func needsFallback(out map[string]any, min float64) bool {
	if min <= 0 {
		return false
	}
	raw, ok := out["confidence"]
	if !ok {
		return true
	}
	conf, ok := visionConfidence(raw)
	if !ok {
		return true
	}
	return conf < min
}

func visionConfidence(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func buildOllamaVisionPrompt(base string, current map[string]any) string {
	var b strings.Builder
	b.WriteString(base)
	if len(current) > 0 {
		b.WriteString("\n\nCurrent deterministic image metadata:\n")
		for _, key := range []string{"image_format", "image_width", "image_height", "ocr_status", "extracted_text_preview"} {
			if value, ok := current[key]; ok && value != nil && value != "" {
				b.WriteString(fmt.Sprintf("- %s: %v\n", key, value))
			}
		}
	}
	return b.String()
}

func firstJSONObject(text string) (string, error) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", fmt.Errorf("no json object found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unterminated json object")
}
