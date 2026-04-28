package analyze

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

type OpenAIVisionAnalyzer struct {
	baseURL string
	apiKey  string
	model   string
	detail  string
	prompt  string
	client  *http.Client
}

func NewOpenAIVisionAnalyzer(cfg config.AttachmentAnalysisOpenAIConfig) *OpenAIVisionAnalyzer {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	apiKeyEnv := strings.TrimSpace(cfg.APIKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = "OPENAI_API_KEY"
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "gpt-5.4-mini"
	}
	detail := strings.TrimSpace(cfg.Detail)
	if detail == "" {
		detail = "low"
	}
	timeout := 30 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = "Analyze this image for a personal knowledge base and return JSON matching the requested schema."
	}
	return &OpenAIVisionAnalyzer{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(os.Getenv(apiKeyEnv)),
		model:   model,
		detail:  detail,
		prompt:  prompt,
		client:  &http.Client{Timeout: timeout},
	}
}

func (a *OpenAIVisionAnalyzer) Backend() string {
	return "openai"
}

func (a *OpenAIVisionAnalyzer) AnalyzeImage(ctx context.Context, path string, current map[string]any) (map[string]any, error) {
	if a.apiKey == "" {
		return nil, fmt.Errorf("missing openai api key")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image for vision analysis: %w", err)
	}
	mimeType := detectImageMIMEType(path, raw)
	body := map[string]any{
		"model": a.model,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": buildOpenAIVisionPrompt(a.prompt, current),
					},
					{
						"type":      "input_image",
						"image_url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(raw),
						"detail":    a.detail,
					},
				},
			},
		},
		"text": map[string]any{
			"format": map[string]any{
				"type":        "json_schema",
				"name":        "attachment_vision_analysis",
				"description": "Structured attachment vision analysis for Fragments Engine.",
				"strict":      true,
				"schema": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"summary": map[string]any{
							"type": "string",
						},
						"tags": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "string",
							},
						},
						"entities": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "string",
							},
						},
						"text_present": map[string]any{
							"type": "boolean",
						},
						"confidence": map[string]any{
							"type": "number",
						},
					},
					"required": []string{"summary", "tags", "entities", "text_present", "confidence"},
				},
			},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal openai vision request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build openai vision request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send openai vision request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if strings.TrimSpace(apiErr.Error.Message) != "" {
			return nil, fmt.Errorf("openai vision API error %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("openai vision API error %d", resp.StatusCode)
	}
	var result struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode openai vision response: %w", err)
	}
	outputText := strings.TrimSpace(result.OutputText)
	if outputText == "" {
		for _, item := range result.Output {
			for _, content := range item.Content {
				if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
					outputText = strings.TrimSpace(content.Text)
					break
				}
			}
			if outputText != "" {
				break
			}
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(outputText), &parsed); err != nil {
		return nil, fmt.Errorf("decode openai vision content: %w", err)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("decode openai vision content: empty object")
	}
	parsed["_provider_backend"] = a.Backend()
	return parsed, nil
}

func buildOpenAIVisionPrompt(base string, current map[string]any) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\nReturn JSON only.")
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

func detectImageMIMEType(path string, raw []byte) string {
	mimeType := http.DetectContentType(raw)
	if strings.HasPrefix(mimeType, "image/") {
		return mimeType
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != "" {
		if guessed := mime.TypeByExtension(ext); strings.HasPrefix(guessed, "image/") {
			return guessed
		}
	}
	return "image/png"
}
