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

func TestLoad_AnchorsRelativeDatabasePathToConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: "./data/fragments-engine.db"},
		Ingests: []config.IngestConfig{
			{Name: "n", Kind: "k", Source: config.IngestSource{Root: "/tmp"}},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := filepath.Join(dir, "data", "fragments-engine.db")
	if reloaded.Database.Path != want {
		t.Fatalf("database.path not anchored to config dir: got %q, want %q", reloaded.Database.Path, want)
	}
}

func TestLoad_AnchorsRelativePathsAcrossConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: "./data/fragments-engine.db"},
		Recall: config.RecallConfig{
			Backend: "sqlite",
			Vanta:   config.RecallVantaConfig{Root: "./data/vanta"},
		},
		Reviewer: config.ReviewerConfig{
			DownloadRoot: "./data/inbox-reviewer",
			CorpusRoot:   "~/Documents/ffs/media/pins", // ~-relative: must pass through untouched
		},
		Ingests: []config.IngestConfig{
			{
				Name:   "relative-source",
				Kind:   "filesystem_docs",
				Source: config.IngestSource{Root: "./corpus"},
			},
			{
				Name:   "chatgpt-export",
				Kind:   "chatgpt_export",
				Source: config.IngestSource{Root: "/absolute/already"},
				Rules:  map[string]any{"archive_root": "./archive/chatgpt"},
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if want := filepath.Join(dir, "data", "vanta"); reloaded.Recall.Vanta.Root != want {
		t.Fatalf("recall.vanta.root not anchored: got %q, want %q", reloaded.Recall.Vanta.Root, want)
	}
	if want := filepath.Join(dir, "data", "inbox-reviewer"); reloaded.Reviewer.DownloadRoot != want {
		t.Fatalf("reviewer.download_root not anchored: got %q, want %q", reloaded.Reviewer.DownloadRoot, want)
	}
	if want := "~/Documents/ffs/media/pins"; reloaded.Reviewer.CorpusRoot != want {
		t.Fatalf("reviewer.corpus_root (~-relative) should pass through unchanged: got %q, want %q", reloaded.Reviewer.CorpusRoot, want)
	}
	if want := filepath.Join(dir, "corpus"); reloaded.Ingests[0].Source.Root != want {
		t.Fatalf("ingest source.root not anchored: got %q, want %q", reloaded.Ingests[0].Source.Root, want)
	}
	if want := "/absolute/already"; reloaded.Ingests[1].Source.Root != want {
		t.Fatalf("absolute ingest source.root should pass through unchanged: got %q, want %q", reloaded.Ingests[1].Source.Root, want)
	}
	if want := filepath.Join(dir, "archive", "chatgpt"); reloaded.Ingests[1].Rules["archive_root"] != want {
		t.Fatalf("ingest rules.archive_root not anchored: got %v, want %q", reloaded.Ingests[1].Rules["archive_root"], want)
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
