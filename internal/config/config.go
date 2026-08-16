package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database    DatabaseConfig    `json:"database" yaml:"database"`
	Recall      RecallConfig      `json:"recall" yaml:"recall"`
	Analysis    AnalysisConfig    `json:"analysis" yaml:"analysis"`
	LinkContent LinkContentConfig `json:"link_content" yaml:"link_content"`
	Delivery    DeliveryConfig    `json:"delivery" yaml:"delivery"`
	Queue       QueueConfig       `json:"queue" yaml:"queue"`
	Reviewer    ReviewerConfig    `json:"reviewer" yaml:"reviewer"`
	Ingests     []IngestConfig    `json:"ingests" yaml:"ingests"`
}

type DatabaseConfig struct {
	Path string `json:"path" yaml:"path"`
}

type RecallConfig struct {
	Backend string            `json:"backend" yaml:"backend"`
	Vanta   RecallVantaConfig `json:"vanta" yaml:"vanta"`
}

type RecallVantaConfig struct {
	Root              string `json:"root" yaml:"root"`
	EmbeddingProvider string `json:"embedding_provider" yaml:"embedding_provider"`
	EmbeddingModel    string `json:"embedding_model" yaml:"embedding_model"`
}

type AnalysisConfig struct {
	Attachments AttachmentAnalysisConfig `json:"attachments" yaml:"attachments"`
}

type AttachmentAnalysisConfig struct {
	Backend         string                         `json:"backend" yaml:"backend"`
	FallbackBackend string                         `json:"fallback_backend" yaml:"fallback_backend"`
	MinConfidence   float64                        `json:"min_confidence" yaml:"min_confidence"`
	Ollama          AttachmentAnalysisOllamaConfig `json:"ollama" yaml:"ollama"`
	OpenAI          AttachmentAnalysisOpenAIConfig `json:"openai" yaml:"openai"`
}

type AttachmentAnalysisOllamaConfig struct {
	Host           string `json:"host" yaml:"host"`
	Model          string `json:"model" yaml:"model"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`
	Prompt         string `json:"prompt" yaml:"prompt"`
}

type AttachmentAnalysisOpenAIConfig struct {
	BaseURL        string `json:"base_url" yaml:"base_url"`
	APIKeyEnv      string `json:"api_key_env" yaml:"api_key_env"`
	Model          string `json:"model" yaml:"model"`
	Detail         string `json:"detail" yaml:"detail"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`
	Prompt         string `json:"prompt" yaml:"prompt"`
}

type LinkContentConfig struct {
	Backend         string                     `json:"backend" yaml:"backend"`
	FallbackBackend string                     `json:"fallback_backend" yaml:"fallback_backend"`
	Local           LinkContentLocalConfig     `json:"local" yaml:"local"`
	Firecrawl       LinkContentFirecrawlConfig `json:"firecrawl" yaml:"firecrawl"`
}

type LinkContentLocalConfig struct {
	RequestTimeoutSeconds int    `json:"request_timeout_seconds" yaml:"request_timeout_seconds"`
	MaxBodyMB             int    `json:"max_body_mb" yaml:"max_body_mb"`
	UserAgent             string `json:"user_agent" yaml:"user_agent"`
}

type LinkContentFirecrawlConfig struct {
	BaseURL        string `json:"base_url" yaml:"base_url"`
	APIKeyEnv      string `json:"api_key_env" yaml:"api_key_env"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`
}

type DeliveryConfig struct {
	File     DeliveryRetryDefaults `json:"file" yaml:"file"`
	MCP      DeliveryRetryDefaults `json:"mcp" yaml:"mcp"`
	API      DeliveryRetryDefaults `json:"api" yaml:"api"`
	CLI      DeliveryRetryDefaults `json:"cli" yaml:"cli"`
	Callback DeliveryRetryDefaults `json:"callback" yaml:"callback"`
}

type DeliveryRetryDefaults struct {
	MaxAttempts int `json:"max_attempts" yaml:"max_attempts"`
	BackoffMS   int `json:"backoff_ms" yaml:"backoff_ms"`
}

type QueueConfig struct {
	AutoDrain                bool `json:"auto_drain" yaml:"auto_drain"`
	PollIntervalSeconds      int  `json:"poll_interval_seconds" yaml:"poll_interval_seconds"`
	BatchSize                int  `json:"batch_size" yaml:"batch_size"`
	ReplayCooldownSeconds    int  `json:"replay_cooldown_seconds" yaml:"replay_cooldown_seconds"`
	MaxReplaysPerHour        int  `json:"max_replays_per_hour" yaml:"max_replays_per_hour"`
	AlertPendingThreshold    int  `json:"alert_pending_threshold" yaml:"alert_pending_threshold"`
	AlertDeadLetterThreshold int  `json:"alert_dead_letter_threshold" yaml:"alert_dead_letter_threshold"`
}

type ReviewerConfig struct {
	Enabled              bool   `json:"enabled" yaml:"enabled"`
	PollIntervalSeconds  int    `json:"poll_interval_seconds" yaml:"poll_interval_seconds"`
	BatchSize            int    `json:"batch_size" yaml:"batch_size"`
	DownloadRoot         string `json:"download_root" yaml:"download_root"`
	CorpusRoot           string `json:"corpus_root" yaml:"corpus_root"`
	GitHubTokenEnv       string `json:"github_token_env" yaml:"github_token_env"`
	StackExplorerAPIBase string `json:"stack_explorer_api_base" yaml:"stack_explorer_api_base"`
	StackExplorerScan    string `json:"stack_explorer_scan" yaml:"stack_explorer_scan"`
}

type IngestConfig struct {
	Name    string            `json:"name" yaml:"name"`
	Kind    string            `json:"kind" yaml:"kind"`
	Enabled bool              `json:"enabled" yaml:"enabled"`
	Source  IngestSource      `json:"source" yaml:"source"`
	Routing IngestRouting     `json:"routing" yaml:"routing"`
	Rules   map[string]any    `json:"rules" yaml:"rules"`
	Labels  map[string]string `json:"labels" yaml:"labels"`
}

type IngestSource struct {
	Root string `json:"root" yaml:"root"`
}

type IngestRouting struct {
	Namespace string `json:"namespace" yaml:"namespace"`
}

type ClaudeCodeRules struct {
	MaxFileSizeMB int `json:"max_file_size_mb" yaml:"max_file_size_mb"`
}

type ChatGPTExportRules struct {
	MaxFileSizeMB      int    `json:"max_file_size_mb" yaml:"max_file_size_mb"`
	ArchiveRoot        string `json:"archive_root" yaml:"archive_root"`
	CopyTextExports    bool   `json:"copy_text_exports" yaml:"copy_text_exports"`
	DeleteCopiedSource bool   `json:"delete_copied_source" yaml:"delete_copied_source"`
}

type URLSourceRules struct {
	RequestTimeoutSeconds int    `json:"request_timeout_seconds" yaml:"request_timeout_seconds"`
	MaxBodyMB             int    `json:"max_body_mb" yaml:"max_body_mb"`
	UserAgent             string `json:"user_agent" yaml:"user_agent"`
}

type FilesystemDocsRules struct {
	Include         []string `json:"include" yaml:"include"`
	Exclude         []string `json:"exclude" yaml:"exclude"`
	MaxFileSizeMB   int      `json:"max_file_size_mb" yaml:"max_file_size_mb"`
	ProjectFromPath bool     `json:"project_from_path" yaml:"project_from_path"`
}

type GitChangesRules struct {
	Repos                []string `json:"repos" yaml:"repos"`
	Branch               string   `json:"branch" yaml:"branch"`
	Since                string   `json:"since" yaml:"since"`
	Until                string   `json:"until" yaml:"until"`
	MaxCommits           int      `json:"max_commits" yaml:"max_commits"`
	Include              []string `json:"include" yaml:"include"`
	Exclude              []string `json:"exclude" yaml:"exclude"`
	EmitDocFileFragments bool     `json:"emit_doc_file_fragments" yaml:"emit_doc_file_fragments"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Database.Path) == "" {
		return errors.New("config: database.path is required")
	}
	if c.Queue.PollIntervalSeconds <= 0 {
		c.Queue.PollIntervalSeconds = 5
	}
	if c.Queue.BatchSize <= 0 {
		c.Queue.BatchSize = 20
	}
	if c.Queue.ReplayCooldownSeconds < 0 {
		c.Queue.ReplayCooldownSeconds = 0
	}
	if c.Queue.MaxReplaysPerHour <= 0 {
		c.Queue.MaxReplaysPerHour = 10
	}
	if c.Queue.AlertPendingThreshold <= 0 {
		c.Queue.AlertPendingThreshold = 10
	}
	if c.Queue.AlertDeadLetterThreshold <= 0 {
		c.Queue.AlertDeadLetterThreshold = 3
	}
	if c.Reviewer.PollIntervalSeconds <= 0 {
		c.Reviewer.PollIntervalSeconds = 300
	}
	if c.Reviewer.BatchSize <= 0 {
		c.Reviewer.BatchSize = 10
	}
	if strings.TrimSpace(c.Reviewer.DownloadRoot) == "" {
		c.Reviewer.DownloadRoot = "./data/inbox-reviewer"
	}
	if strings.TrimSpace(c.Reviewer.CorpusRoot) == "" {
		c.Reviewer.CorpusRoot = "~/Documents/ffs/media/pins"
	}
	if strings.TrimSpace(c.Reviewer.GitHubTokenEnv) == "" {
		c.Reviewer.GitHubTokenEnv = "GITHUB_TOKEN"
	}
	if c.Delivery.File.MaxAttempts <= 0 {
		c.Delivery.File.MaxAttempts = 1
	}
	if c.Delivery.File.BackoffMS < 0 {
		c.Delivery.File.BackoffMS = 0
	}
	if c.Delivery.MCP.MaxAttempts <= 0 {
		c.Delivery.MCP.MaxAttempts = 3
	}
	if c.Delivery.MCP.BackoffMS <= 0 {
		c.Delivery.MCP.BackoffMS = 500
	}
	if c.Delivery.API.MaxAttempts <= 0 {
		c.Delivery.API.MaxAttempts = 3
	}
	if c.Delivery.API.BackoffMS <= 0 {
		c.Delivery.API.BackoffMS = 500
	}
	if c.Delivery.CLI.MaxAttempts <= 0 {
		c.Delivery.CLI.MaxAttempts = 3
	}
	if c.Delivery.CLI.BackoffMS <= 0 {
		c.Delivery.CLI.BackoffMS = 500
	}
	if c.Delivery.Callback.MaxAttempts <= 0 {
		c.Delivery.Callback.MaxAttempts = 3
	}
	if c.Delivery.Callback.BackoffMS <= 0 {
		c.Delivery.Callback.BackoffMS = 500
	}
	if strings.TrimSpace(c.Recall.Backend) == "" {
		c.Recall.Backend = "sqlite"
	}
	switch c.Recall.Backend {
	case "", "sqlite", "vanta":
	default:
		return fmt.Errorf("config: unsupported recall backend %q", c.Recall.Backend)
	}
	switch strings.ToLower(strings.TrimSpace(c.Recall.Vanta.EmbeddingProvider)) {
	case "", "none", "openai", "ollama", "llama":
	default:
		return fmt.Errorf("config: unsupported vanta embedding provider %q", c.Recall.Vanta.EmbeddingProvider)
	}
	switch strings.ToLower(strings.TrimSpace(c.Analysis.Attachments.Backend)) {
	case "", "none", "ollama", "openai":
	default:
		return fmt.Errorf("config: unsupported attachments analysis backend %q", c.Analysis.Attachments.Backend)
	}
	switch strings.ToLower(strings.TrimSpace(c.Analysis.Attachments.FallbackBackend)) {
	case "", "none", "ollama", "openai":
	default:
		return fmt.Errorf("config: unsupported attachments fallback backend %q", c.Analysis.Attachments.FallbackBackend)
	}
	if c.Analysis.Attachments.MinConfidence < 0 || c.Analysis.Attachments.MinConfidence > 1 {
		return fmt.Errorf("config: attachments min_confidence must be between 0 and 1")
	}
	switch strings.ToLower(strings.TrimSpace(c.Analysis.Attachments.OpenAI.Detail)) {
	case "", "low", "high", "auto", "original":
	default:
		return fmt.Errorf("config: unsupported openai attachment detail %q", c.Analysis.Attachments.OpenAI.Detail)
	}
	switch strings.ToLower(strings.TrimSpace(c.LinkContent.Backend)) {
	case "", "none", "local", "firecrawl":
	default:
		return fmt.Errorf("config: unsupported link_content backend %q", c.LinkContent.Backend)
	}
	switch strings.ToLower(strings.TrimSpace(c.LinkContent.FallbackBackend)) {
	case "", "none", "local", "firecrawl":
	default:
		return fmt.Errorf("config: unsupported link_content fallback backend %q", c.LinkContent.FallbackBackend)
	}
	if len(c.Ingests) == 0 {
		return errors.New("config: at least one ingest is required")
	}
	seen := map[string]struct{}{}
	for _, ingest := range c.Ingests {
		if strings.TrimSpace(ingest.Name) == "" {
			return errors.New("config: ingest.name is required")
		}
		if _, ok := seen[ingest.Name]; ok {
			return fmt.Errorf("config: duplicate ingest name %q", ingest.Name)
		}
		seen[ingest.Name] = struct{}{}
		if strings.TrimSpace(ingest.Kind) == "" {
			return fmt.Errorf("config: ingest %q kind is required", ingest.Name)
		}
		if strings.TrimSpace(ingest.Source.Root) == "" {
			return fmt.Errorf("config: ingest %q source.root is required", ingest.Name)
		}
	}
	return nil
}

func (c Config) RecallBackend() string {
	backend := strings.TrimSpace(c.Recall.Backend)
	if backend == "" {
		return "sqlite"
	}
	return backend
}

func ExpandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}

func DecodeRules[T any](cfg IngestConfig) (T, error) {
	var out T
	if len(cfg.Rules) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(cfg.Rules)
	if err != nil {
		return out, fmt.Errorf("encode ingest rules: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode ingest rules: %w", err)
	}
	return out, nil
}

func FindIngest(cfg Config, name string) (IngestConfig, int, error) {
	for i, ingest := range cfg.Ingests {
		if ingest.Name == name {
			return ingest, i, nil
		}
	}
	return IngestConfig{}, -1, fmt.Errorf("ingest %q not found", name)
}
