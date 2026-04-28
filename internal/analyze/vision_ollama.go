package analyze

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

type OllamaVisionAnalyzer struct {
	host    string
	model   string
	prompt  string
	timeout time.Duration
	client  *http.Client
}

func NewOllamaVisionAnalyzer(cfg config.AttachmentAnalysisOllamaConfig) *OllamaVisionAnalyzer {
	host := strings.TrimRight(strings.TrimSpace(cfg.Host), "/")
	if host == "" {
		host = strings.TrimRight(strings.TrimSpace(os.Getenv("OLLAMA_HOST")), "/")
	}
	if host == "" {
		host = "http://localhost:11434"
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "gemma3"
	}
	timeout := 30 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = "Analyze this image for a personal knowledge base. Return compact JSON with keys summary, tags, entities, text_present, confidence. Keep tags short and literal."
	}
	return &OllamaVisionAnalyzer{
		host:    host,
		model:   model,
		prompt:  prompt,
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

func (a *OllamaVisionAnalyzer) Backend() string {
	return "ollama"
}

func (a *OllamaVisionAnalyzer) AnalyzeImage(ctx context.Context, path string, current map[string]any) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image for vision analysis: %w", err)
	}
	prompt := buildOllamaVisionPrompt(a.prompt, current)
	if parsed, err := a.requestAnalysis(ctx, raw, prompt, true); err == nil {
		return parsed, nil
	} else {
		relaxedPrompt := prompt + "\n\nIf exact JSON mode fails, reply with a short response that contains one JSON object and nothing else before or after it."
		fallback, fallbackErr := a.requestAnalysis(ctx, raw, relaxedPrompt, false)
		if fallbackErr == nil {
			return fallback, nil
		}
		return nil, fmt.Errorf("strict vision request failed: %v; relaxed vision request failed: %w", err, fallbackErr)
	}
}

func (a *OllamaVisionAnalyzer) requestAnalysis(ctx context.Context, raw []byte, prompt string, strictJSON bool) (map[string]any, error) {
	body := map[string]any{
		"model":  a.model,
		"stream": false,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": prompt,
				"images":  []string{base64.StdEncoding.EncodeToString(raw)},
			},
		},
	}
	if strictJSON {
		body["format"] = "json"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama vision request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.host+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build ollama vision request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send ollama vision request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama vision API error %d", resp.StatusCode)
	}
	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode ollama vision response: %w", err)
	}
	var parsed map[string]any
	content := strings.TrimSpace(result.Message.Content)
	if strictJSON {
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			return nil, fmt.Errorf("decode ollama vision content: %w", err)
		}
	} else {
		jsonText, err := firstJSONObject(content)
		if err != nil {
			return nil, fmt.Errorf("extract ollama vision json object: %w", err)
		}
		if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
			return nil, fmt.Errorf("decode relaxed ollama vision content: %w", err)
		}
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("decode ollama vision content: empty object")
	}
	parsed["_provider_backend"] = a.Backend()
	return parsed, nil
}
