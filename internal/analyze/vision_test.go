package analyze

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestOllamaVisionAnalyzer(t *testing.T) {
	var got map[string]any
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		requests++
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{
				"content": `{"summary":"Diagram of roadmap phases","tags":["diagram","roadmap"],"text_present":true,"confidence":0.82}`,
			},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	imagePath := filepath.Join(root, "diagram.png")
	if err := os.WriteFile(imagePath, rawPNG, 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	analyzer := NewOllamaVisionAnalyzer(config.AttachmentAnalysisOllamaConfig{
		Host:  srv.URL,
		Model: "gemma3",
	})
	out, err := analyzer.AnalyzeImage(context.Background(), imagePath, map[string]any{
		"image_format": "png",
		"image_width":  1,
		"image_height": 1,
		"ocr_status":   "unavailable",
	})
	if err != nil {
		t.Fatalf("analyze image: %v", err)
	}
	if out["summary"] != "Diagram of roadmap phases" {
		t.Fatalf("unexpected analysis output: %+v", out)
	}
	messages, ok := got["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("unexpected request payload: %+v", got)
	}
	msg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message payload: %+v", got)
	}
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "Current deterministic image metadata") {
		t.Fatalf("expected deterministic metadata in prompt: %s", content)
	}
	images, ok := msg["images"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("expected base64 image payload: %+v", msg)
	}
	if requests != 1 {
		t.Fatalf("expected single strict request, got %d", requests)
	}
	if got["format"] != "json" {
		t.Fatalf("expected strict json format request: %+v", got)
	}
}

func TestOllamaVisionAnalyzer_RelaxedFallback(t *testing.T) {
	requests := 0
	var first, second map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests++
		if requests == 1 {
			first = payload
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{
					"content": `{}`,
				},
			})
			return
		}
		second = payload
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{
				"content": "Here is the result:\n{\"summary\":\"Color blocks\",\"tags\":[\"image\",\"blocks\"],\"entities\":[\"red\",\"green\"],\"text_present\":false,\"confidence\":0.61}\nThanks.",
			},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	imagePath := filepath.Join(root, "diagram.png")
	if err := os.WriteFile(imagePath, rawPNG, 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	analyzer := NewOllamaVisionAnalyzer(config.AttachmentAnalysisOllamaConfig{
		Host:  srv.URL,
		Model: "gemma3",
	})
	out, err := analyzer.AnalyzeImage(context.Background(), imagePath, nil)
	if err != nil {
		t.Fatalf("analyze image with fallback: %v", err)
	}
	if out["summary"] != "Color blocks" {
		t.Fatalf("unexpected fallback output: %+v", out)
	}
	if requests != 2 {
		t.Fatalf("expected strict+relaxed requests, got %d", requests)
	}
	if first["format"] != "json" {
		t.Fatalf("expected strict request to use json format: %+v", first)
	}
	if _, ok := second["format"]; ok {
		t.Fatalf("did not expect relaxed request to force json format: %+v", second)
	}
}

func TestOpenAIVisionAnalyzer(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if gotAuth := r.Header.Get("Authorization"); gotAuth != "Bearer test-key" {
			t.Fatalf("unexpected authorization header %q", gotAuth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output_text": `{"summary":"Simple diagram","tags":["diagram","blocks"],"entities":["red","green"],"text_present":false,"confidence":0.77}`,
		})
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")

	root := t.TempDir()
	rawPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aGxQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	imagePath := filepath.Join(root, "diagram.png")
	if err := os.WriteFile(imagePath, rawPNG, 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	analyzer := NewOpenAIVisionAnalyzer(config.AttachmentAnalysisOpenAIConfig{
		BaseURL: srv.URL,
		Model:   "gpt-5.4-mini",
		Detail:  "low",
	})
	out, err := analyzer.AnalyzeImage(context.Background(), imagePath, map[string]any{
		"image_format": "png",
		"image_width":  1,
		"image_height": 1,
		"ocr_status":   "unavailable",
	})
	if err != nil {
		t.Fatalf("analyze image: %v", err)
	}
	if out["summary"] != "Simple diagram" {
		t.Fatalf("unexpected analysis output: %+v", out)
	}
	if analyzer.Backend() != "openai" {
		t.Fatalf("unexpected backend %q", analyzer.Backend())
	}
	input, ok := got["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("unexpected input payload: %+v", got)
	}
	msg, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message payload: %+v", got)
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("unexpected content payload: %+v", msg)
	}
	imageItem, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected image content payload: %+v", content[1])
	}
	if imageItem["type"] != "input_image" {
		t.Fatalf("unexpected image content type: %+v", imageItem)
	}
	if imageItem["detail"] != "low" {
		t.Fatalf("unexpected detail value: %+v", imageItem)
	}
	textCfg, ok := got["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config: %+v", got)
	}
	formatCfg, ok := textCfg["format"].(map[string]any)
	if !ok || formatCfg["type"] != "json_schema" {
		t.Fatalf("expected json schema format config: %+v", textCfg)
	}
}
