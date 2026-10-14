package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Transcripts TranscriptConfig  `json:"transcripts" yaml:"transcripts"`
	UI          UIConfig          `json:"ui" yaml:"ui"`
	Database    DatabaseConfig    `json:"database" yaml:"database"`
	Recall      RecallConfig      `json:"recall" yaml:"recall"`
	Analysis    AnalysisConfig    `json:"analysis" yaml:"analysis"`
	LinkContent LinkContentConfig `json:"link_content" yaml:"link_content"`
	Delivery    DeliveryConfig    `json:"delivery" yaml:"delivery"`
	Queue       QueueConfig       `json:"queue" yaml:"queue"`
	Reviewer    ReviewerConfig    `json:"reviewer" yaml:"reviewer"`
	Ingests     []IngestConfig    `json:"ingests" yaml:"ingests"`
}

// TranscriptConfig names the explicit local owner-private destination. Blank
// means transcript ingestion is unavailable, with no shared-store fallback.
type TranscriptConfig struct {
	PrivateRoot string           `json:"private_root" yaml:"private_root"`
	Enrichment  EnrichmentConfig `json:"enrichment" yaml:"enrichment"`
}

// EnrichmentConfig drives transcript enrichment. Nothing leaves the machine
// unless a provider is listed in AllowedProviders: the default is empty.
type EnrichmentConfig struct {
	Gateway   EnrichmentGateway          `json:"gateway" yaml:"gateway"`
	Anthropic EnrichmentAnthropic        `json:"anthropic" yaml:"anthropic"`
	Stages    map[string]EnrichmentStage `json:"stages" yaml:"stages"`
	Embedding EnrichmentEmbedding        `json:"embedding" yaml:"embedding"`
	// AllowedProviders are the gateway provider ids that may receive redacted
	// transcript text. Empty means no model call is made.
	AllowedProviders []string `json:"allowed_providers" yaml:"allowed_providers"`
	Provider         string   `json:"provider" yaml:"provider"`
	Model            string   `json:"model" yaml:"model"`
	PromptVersion    string   `json:"prompt_version" yaml:"prompt_version"`
	// MaxCostUSD caps one run; MaxTotalCostUSD caps all runs together, from the
	// spend ledger. Both are hard stops enforced by FE regardless of any
	// gateway budget.
	MaxCostUSD      float64 `json:"max_cost_usd" yaml:"max_cost_usd"`
	MaxTotalCostUSD float64 `json:"max_total_cost_usd" yaml:"max_total_cost_usd"`
	// ExcludeSessions (native session ids) and ExcludeCWDPrefixes (project
	// paths) are the never-send switch: matching sessions are never given to a
	// model.
	ExcludeSessions    []string `json:"exclude_sessions" yaml:"exclude_sessions"`
	ExcludeCWDPrefixes []string `json:"exclude_cwd_prefixes" yaml:"exclude_cwd_prefixes"`
	MinIdleMinutes     int      `json:"min_idle_minutes" yaml:"min_idle_minutes"`
	MaxOutputTokens    int      `json:"max_output_tokens" yaml:"max_output_tokens"`
	// MapReduceMaxOutputTokens caps the part and combine calls of a long session
	// (default 4096); MaxOutputTokens still caps single-call sessions.
	MapReduceMaxOutputTokens int `json:"map_reduce_max_output_tokens" yaml:"map_reduce_max_output_tokens"`
}

// EnrichmentStage is a named, reusable selection for a staged run. Allocation
// maps "tool/bucket" (for example "claude/8-30k"; buckets are <1k, 1-8k, 8-30k,
// 30-60k, >60k of estimated prompt tokens) to how many pending sessions to take
// from that stratum; strata not listed are not selected. Sessions are taken in
// a reproducible hash order, or newest first when Order is "newest". Sessions
// already enriched are excluded before selection.
type EnrichmentStage struct {
	Tool       string         `json:"tool" yaml:"tool"`
	Order      string         `json:"order" yaml:"order"`
	Allocation map[string]int `json:"allocation" yaml:"allocation"`
}

// EnrichmentAnthropic configures the direct Anthropic provider (allow-list id
// "anthropic-direct"). The API key is read from the environment variable named
// by APIKeyEnv at startup; it is never stored in this file, logged or persisted.
type EnrichmentAnthropic struct {
	BaseURL   string `json:"base_url" yaml:"base_url"` // default https://api.anthropic.com; https only
	APIKeyEnv string `json:"api_key_env" yaml:"api_key_env"`
	Version   string `json:"version" yaml:"version"` // anthropic-version header; default 2023-06-01
	// Thinking is "disabled" (default) or "adaptive". Haiku 5.5 thinks by default
	// and thinking tokens count toward max_tokens; a bounded JSON summary does
	// not need it. Effort optionally sets output_config.effort.
	Thinking string            `json:"thinking" yaml:"thinking"`
	Effort   string            `json:"effort" yaml:"effort"`
	Pricing  EnrichmentPricing `json:"pricing" yaml:"pricing"`
}

// EnrichmentPricing is USD per million tokens. There are no built-in defaults:
// the values must be confirmed against Anthropic's published pricing before the
// first live call, and spend caps are computed from them.
type EnrichmentPricing struct {
	InputPerMTok      float64 `json:"input_per_mtok" yaml:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok" yaml:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok" yaml:"cache_read_per_mtok"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok" yaml:"cache_write_per_mtok"`
	// LongPromptThresholdTokens and Long describe a higher tier for requests whose
	// prompt (input plus cache reads and writes) exceeds the threshold.
	LongPromptThresholdTokens int                `json:"long_prompt_threshold_tokens" yaml:"long_prompt_threshold_tokens"`
	Long                      *EnrichmentPricing `json:"long" yaml:"long"`
}

// EnrichmentEmbedding selects a local embedding server. Provider is
// "openai_compat_local" (an OpenAI-style /v1/embeddings server on this machine,
// e.g. llama.cpp) or "ollama" (the default). Both refuse non-local hosts.
type EnrichmentEmbedding struct {
	Provider         string `json:"provider" yaml:"provider"`
	BaseURL          string `json:"base_url" yaml:"base_url"`
	Model            string `json:"model" yaml:"model"`
	Dim              int    `json:"dim" yaml:"dim"`
	QueryInstruction string `json:"query_instruction" yaml:"query_instruction"`
	BatchSize        int    `json:"batch_size" yaml:"batch_size"`
}

// EnrichmentGateway locates the Tether AI gateway. Address is "unix:/path" or
// an http://127.0.0.1:PORT loopback URL. The bearer token, if the daemon
// enforces identity, is read from the environment variable named by TokenEnv,
// never from this file.
type EnrichmentGateway struct {
	Address  string `json:"address" yaml:"address"`
	TokenEnv string `json:"token_env" yaml:"token_env"`
}

// UIConfig gates owner-only browser surfaces. Every flag is off by default.
type UIConfig struct {
	Transcripts UITranscriptsConfig `json:"transcripts" yaml:"transcripts"`
}

// UITranscriptsConfig enables the read-only routes that browse the private
// transcript store. Enabled routes refuse to serve unless the API listener is
// loopback. There is deliberately no credential yet.
type UITranscriptsConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
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
	// ToolResultExcerptBytes keeps a redacted excerpt of each tool result, up to
	// this many bytes (hard cap 2048). Zero, the default, keeps only the result's
	// status and size.
	ToolResultExcerptBytes int `json:"tool_result_excerpt_bytes" yaml:"tool_result_excerpt_bytes"`
}

type CodexSessionRules struct {
	MaxFileSizeMB int `json:"max_file_size_mb" yaml:"max_file_size_mb"`
	// ToolResultExcerptBytes keeps a redacted excerpt of each tool result, up to
	// this many bytes (hard cap 2048). Zero, the default, keeps only the result's
	// status and size.
	ToolResultExcerptBytes int `json:"tool_result_excerpt_bytes" yaml:"tool_result_excerpt_bytes"`
}

type AntigravityRules struct {
	MaxFileSizeMB int `json:"max_file_size_mb" yaml:"max_file_size_mb"`
	// ToolResultExcerptBytes keeps a redacted excerpt of each tool result, up to
	// this many bytes (hard cap 2048). Zero, the default, keeps only the result's
	// status and size.
	ToolResultExcerptBytes int `json:"tool_result_excerpt_bytes" yaml:"tool_result_excerpt_bytes"`
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

// NilVaultRules configures the nil_vault ingest source. Source.Root should
// point at Nil's config directory (e.g. ~/.config/nil), which holds
// config.json -- the registry of named vaults, each a standalone SQLite file
// at <vault.path>/todo.db. IncludeVaults/ExcludeVaults select vaults by
// either name or ID; both empty (the default) means "ingest every vault
// found in config.json".
type NilVaultRules struct {
	IncludeVaults []string `json:"include_vaults" yaml:"include_vaults"`
	ExcludeVaults []string `json:"exclude_vaults" yaml:"exclude_vaults"`
}

// installDir backs InstallDir/SetInstallDir. Load() is called from
// concurrent per-request handlers (internal/api/server.go, internal/mcp
// tool handlers all call config.Load() inline), so the ambient anchor below
// must be safe for concurrent read/write rather than a bare package var.
var (
	installDirMu sync.RWMutex
	installDir   string
)

// InstallDir returns the absolute directory containing the most recently
// loaded config file, set as a side effect of Load(). It is the ambient
// anchor for relative filesystem paths that don't come from the config file
// itself — notably per-destination "root"/"working_dir" values, which are
// DB-stored (added via CLI/API/MCP) rather than declared in fragments.yaml,
// so there is no config-file directory to anchor them to at the point
// they're decoded. Same rationale as ExpandHome leaning on the ambient
// os.UserHomeDir() instead of threading a home dir through every caller:
// fragments-engine only ever runs against one config file per process, so a
// package-level anchor is safe and avoids cascading a new parameter through
// the delivery/probe call chains. Empty until the first successful Load().
func InstallDir() string {
	installDirMu.RLock()
	defer installDirMu.RUnlock()
	return installDir
}

// SetInstallDir sets the ambient anchor returned by InstallDir. Called by
// Load() as a side effect; exported so tests can set/restore it around
// cases that exercise the ambient-anchor path directly.
func SetInstallDir(dir string) {
	installDirMu.Lock()
	defer installDirMu.Unlock()
	installDir = dir
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
	resolveRelativePaths(path, &cfg)
	return cfg, nil
}

// resolveRelativePaths anchors filesystem paths in cfg that are relative
// (e.g. "./data/x.db") to the config file's own directory instead of
// leaving them to resolve against the process's working directory. A
// process spawned by an external supervisor (e.g. an MCP client pointed at
// this config via an absolute --config path) rarely shares a working
// directory with the config file itself; without this, a relative path
// silently resolves to the wrong location — for database.path, store.Open's
// MkdirAll+migrate creates a fresh empty database there instead of erroring.
// Every relative filesystem path sourced from this config file is anchored
// here so no MCP tool call is CWD-dependent. It also sets the InstallDir
// package var so the same anchor is available to destination code that
// resolves DB-stored (not config-file) paths.
func resolveRelativePaths(configPath string, cfg *Config) {
	dir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return
	}
	SetInstallDir(dir)
	cfg.Database.Path = AnchorPath(dir, cfg.Database.Path)
	cfg.Transcripts.PrivateRoot = AnchorPath(dir, cfg.Transcripts.PrivateRoot)
	cfg.Recall.Vanta.Root = AnchorPath(dir, cfg.Recall.Vanta.Root)
	cfg.Reviewer.DownloadRoot = AnchorPath(dir, cfg.Reviewer.DownloadRoot)
	cfg.Reviewer.CorpusRoot = AnchorPath(dir, cfg.Reviewer.CorpusRoot)
	for i := range cfg.Ingests {
		cfg.Ingests[i].Source.Root = AnchorPath(dir, cfg.Ingests[i].Source.Root)
		// archive_root (chatgpt_export rules) lives in the untyped Rules
		// map rather than a static struct field; resolve it by key when
		// present so it gets the same treatment as every other root.
		if raw, ok := cfg.Ingests[i].Rules["archive_root"]; ok {
			if s, ok := raw.(string); ok {
				cfg.Ingests[i].Rules["archive_root"] = AnchorPath(dir, s)
			}
		}
	}
}

// AnchorPath joins a relative path onto dir. Empty, ~-prefixed (handled by
// ExpandHome), and already-absolute paths pass through unchanged. If dir is
// empty (no config has been Load()ed yet), a relative p is returned as-is,
// preserving the old CWD-relative behavior as a safe fallback.
func AnchorPath(dir, p string) string {
	if p == "" || p[0] == '~' || filepath.IsAbs(p) || dir == "" {
		return p
	}
	return filepath.Join(dir, p)
}

// Save persists cfg to path. When path already holds a config file, the
// write is a byte-preserving merge (see mergeYAML) rather than a full
// re-encode: only keys cfg's schema actually changed are touched, so
// comments, key order, and any key the schema doesn't model survive. A
// missing path (first write) falls back to a plain marshal -- there's
// nothing to preserve yet.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	var raw []byte
	original, err := os.ReadFile(path)
	switch {
	case err == nil:
		merged, mergeErr := mergeYAML(original, cfg)
		if mergeErr != nil {
			return fmt.Errorf("merge config: %w", mergeErr)
		}
		raw = merged
	case errors.Is(err, os.ErrNotExist):
		full, marshalErr := yaml.Marshal(&cfg)
		if marshalErr != nil {
			return fmt.Errorf("encode config: %w", marshalErr)
		}
		raw = full
	default:
		return fmt.Errorf("read existing config: %w", err)
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
