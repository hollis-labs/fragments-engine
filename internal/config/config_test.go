package config_test

import (
	"path/filepath"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestLoadSaveRoundTrip_LinkContentAndAttachmentAnalysis(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "fragments.db")},
		Recall:   config.RecallConfig{Backend: "sqlite"},
		Analysis: config.AnalysisConfig{
			Attachments: config.AttachmentAnalysisConfig{
				Backend:         "openai",
				FallbackBackend: "ollama",
				MinConfidence:   0.5,
				Ollama: config.AttachmentAnalysisOllamaConfig{
					Host:           "http://localhost:11434",
					Model:          "gemma3",
					TimeoutSeconds: 30,
				},
				OpenAI: config.AttachmentAnalysisOpenAIConfig{
					BaseURL:        "https://api.openai.com/v1",
					APIKeyEnv:      "OPENAI_API_KEY",
					Model:          "gpt-5.4-mini",
					Detail:         "low",
					TimeoutSeconds: 30,
				},
			},
		},
		LinkContent: config.LinkContentConfig{
			Backend:         "local",
			FallbackBackend: "firecrawl",
			Local: config.LinkContentLocalConfig{
				RequestTimeoutSeconds: 20,
				MaxBodyMB:             5,
				UserAgent:             "FragmentsEngine/0.1 (+link-content)",
			},
			Firecrawl: config.LinkContentFirecrawlConfig{
				BaseURL:        "https://api.firecrawl.dev",
				APIKeyEnv:      "FIRECRAWL_API_KEY",
				TimeoutSeconds: 30,
			},
		},
		Ingests: []config.IngestConfig{
			{
				Name:    "claude-fixture",
				Kind:    "claude_code",
				Enabled: true,
				Source:  config.IngestSource{Root: t.TempDir()},
				Routing: config.IngestRouting{Namespace: "fragments/chats/claude"},
			},
		},
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	if reloaded.LinkContent != cfg.LinkContent {
		t.Fatalf("link_content did not round-trip: got %+v, want %+v", reloaded.LinkContent, cfg.LinkContent)
	}
	if reloaded.Analysis.Attachments != cfg.Analysis.Attachments {
		t.Fatalf("analysis.attachments did not round-trip: got %+v, want %+v", reloaded.Analysis.Attachments, cfg.Analysis.Attachments)
	}
}

func TestValidate_LinkContentBackend(t *testing.T) {
	base := func() config.Config {
		return config.Config{
			Database: config.DatabaseConfig{Path: "./data/fragments-engine.db"},
			Ingests: []config.IngestConfig{
				{Name: "n", Kind: "k", Source: config.IngestSource{Root: "/tmp"}},
			},
		}
	}

	for _, valid := range []string{"", "none", "local", "firecrawl", "LOCAL"} {
		cfg := base()
		cfg.LinkContent.Backend = valid
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected backend %q to be valid, got error: %v", valid, err)
		}
	}
	for _, valid := range []string{"", "none", "local", "firecrawl"} {
		cfg := base()
		cfg.LinkContent.FallbackBackend = valid
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected fallback backend %q to be valid, got error: %v", valid, err)
		}
	}

	cfg := base()
	cfg.LinkContent.Backend = "bogus"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for unsupported link_content backend")
	}

	cfg = base()
	cfg.LinkContent.FallbackBackend = "bogus"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for unsupported link_content fallback backend")
	}
}
