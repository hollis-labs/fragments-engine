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
	Database DatabaseConfig `yaml:"database"`
	Recall   RecallConfig   `yaml:"recall"`
	Analysis AnalysisConfig `yaml:"analysis"`
	Delivery DeliveryConfig `yaml:"delivery"`
	Queue    QueueConfig    `yaml:"queue"`
	Ingests  []IngestConfig `yaml:"ingests"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type RecallConfig struct {
	Backend string            `yaml:"backend"`
	Vanta   RecallVantaConfig `yaml:"vanta"`
}

type RecallVantaConfig struct {
	Root              string `yaml:"root"`
	EmbeddingProvider string `yaml:"embedding_provider"`
	EmbeddingModel    string `yaml:"embedding_model"`
}

type AnalysisConfig struct {
	Attachments AttachmentAnalysisConfig `yaml:"attachments"`
}

type AttachmentAnalysisConfig struct {
	Backend         string                         `yaml:"backend"`
	FallbackBackend string                         `yaml:"fallback_backend"`
	MinConfidence   float64                        `yaml:"min_confidence"`
	Ollama          AttachmentAnalysisOllamaConfig `yaml:"ollama"`
	OpenAI          AttachmentAnalysisOpenAIConfig `yaml:"openai"`
}

type AttachmentAnalysisOllamaConfig struct {
	Host           string `yaml:"host"`
	Model          string `yaml:"model"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	Prompt         string `yaml:"prompt"`
}

type AttachmentAnalysisOpenAIConfig struct {
	BaseURL        string `yaml:"base_url"`
	APIKeyEnv      string `yaml:"api_key_env"`
	Model          string `yaml:"model"`
	Detail         string `yaml:"detail"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	Prompt         string `yaml:"prompt"`
}

type DeliveryConfig struct {
	File DeliveryRetryDefaults `yaml:"file"`
	MCP  DeliveryRetryDefaults `yaml:"mcp"`
	API  DeliveryRetryDefaults `yaml:"api"`
	CLI  DeliveryRetryDefaults `yaml:"cli"`
}

type DeliveryRetryDefaults struct {
	MaxAttempts int `yaml:"max_attempts"`
	BackoffMS   int `yaml:"backoff_ms"`
}

type QueueConfig struct {
	AutoDrain                bool `yaml:"auto_drain"`
	PollIntervalSeconds      int  `yaml:"poll_interval_seconds"`
	BatchSize                int  `yaml:"batch_size"`
	ReplayCooldownSeconds    int  `yaml:"replay_cooldown_seconds"`
	MaxReplaysPerHour        int  `yaml:"max_replays_per_hour"`
	AlertPendingThreshold    int  `yaml:"alert_pending_threshold"`
	AlertDeadLetterThreshold int  `yaml:"alert_dead_letter_threshold"`
}

type IngestConfig struct {
	Name    string            `yaml:"name"`
	Kind    string            `yaml:"kind"`
	Enabled bool              `yaml:"enabled"`
	Source  IngestSource      `yaml:"source"`
	Routing IngestRouting     `yaml:"routing"`
	Rules   map[string]any    `yaml:"rules"`
	Labels  map[string]string `yaml:"labels"`
}

type IngestSource struct {
	Root string `yaml:"root"`
}

type IngestRouting struct {
	Namespace string `yaml:"namespace"`
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
