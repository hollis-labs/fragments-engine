package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/ingest/chatgpt"
)

type IngestAdminService struct {
	cfgPath string
}

func NewIngestAdminService(cfgPath string) *IngestAdminService {
	return &IngestAdminService{cfgPath: cfgPath}
}

// ValidationError marks caller-supplied input as invalid. API handlers map it
// to HTTP 400 rather than 500.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// IngestSourceInput is the mutable surface of an ingest source accepted by
// Create and Update.
type IngestSourceInput struct {
	Name       string
	Kind       string
	Enabled    bool
	SourceRoot string
	Namespace  string
	Rules      map[string]any
	Labels     map[string]string
}

func (in IngestSourceInput) toConfig() config.IngestConfig {
	return config.IngestConfig{
		Name:    strings.TrimSpace(in.Name),
		Kind:    strings.TrimSpace(in.Kind),
		Enabled: in.Enabled,
		Source:  config.IngestSource{Root: strings.TrimSpace(in.SourceRoot)},
		Routing: config.IngestRouting{Namespace: strings.TrimSpace(in.Namespace)},
		Rules:   in.Rules,
		Labels:  in.Labels,
	}
}

func (s *IngestAdminService) List(_ context.Context) ([]domain.IngestSummary, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, err
	}
	items := make([]domain.IngestSummary, 0, len(cfg.Ingests))
	for _, ingestCfg := range cfg.Ingests {
		items = append(items, ingestSummary(ingestCfg))
	}
	return items, nil
}

// ingestSummary projects an IngestConfig onto the API summary shape, decoding
// chatgpt_export archive fields when present.
func ingestSummary(ic config.IngestConfig) domain.IngestSummary {
	item := domain.IngestSummary{
		Name:       ic.Name,
		Kind:       ic.Kind,
		Enabled:    ic.Enabled,
		SourceRoot: ic.Source.Root,
		Namespace:  ic.Routing.Namespace,
		Labels:     ic.Labels,
	}
	if ic.Kind == "chatgpt_export" {
		if rules, err := config.DecodeRules[config.ChatGPTExportRules](ic); err == nil {
			item.ArchiveRoot = rules.ArchiveRoot
			item.CopyTextExports = rules.CopyTextExports
			item.DeleteCopiedSource = rules.DeleteCopiedSource
		}
	}
	return item
}

// Create adds a new ingest source to the config and persists it.
func (s *IngestAdminService) Create(_ context.Context, input IngestSourceInput) (domain.IngestSummary, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return domain.IngestSummary{}, err
	}
	ic := input.toConfig()
	if err := validateIngestSource(ic); err != nil {
		return domain.IngestSummary{}, err
	}
	if _, _, err := config.FindIngest(cfg, ic.Name); err == nil {
		return domain.IngestSummary{}, ValidationError{Msg: fmt.Sprintf("ingest %q already exists", ic.Name)}
	}
	cfg.Ingests = append(cfg.Ingests, ic)
	if err := config.Save(s.cfgPath, cfg); err != nil {
		return domain.IngestSummary{}, err
	}
	return ingestSummary(ic), nil
}

// Update replaces an existing ingest source, keyed by name. This is a
// full-record (PUT-style) replace: every field is taken from the input, so a
// caller must send the complete desired config — omitted fields are cleared,
// not preserved. Name is the lookup key and cannot be changed here.
func (s *IngestAdminService) Update(_ context.Context, input IngestSourceInput) (domain.IngestSummary, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return domain.IngestSummary{}, err
	}
	ic := input.toConfig()
	if _, idx, err := config.FindIngest(cfg, ic.Name); err != nil {
		return domain.IngestSummary{}, ValidationError{Msg: err.Error()}
	} else if err := validateIngestSource(ic); err != nil {
		return domain.IngestSummary{}, err
	} else {
		cfg.Ingests[idx] = ic
	}
	if err := config.Save(s.cfgPath, cfg); err != nil {
		return domain.IngestSummary{}, err
	}
	return ingestSummary(ic), nil
}

// Delete removes an ingest source by name.
func (s *IngestAdminService) Delete(_ context.Context, name string) error {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return err
	}
	_, idx, err := config.FindIngest(cfg, name)
	if err != nil {
		return ValidationError{Msg: err.Error()}
	}
	// config.Validate (invoked by config.Save) requires at least one ingest,
	// so removing the final source is rejected up front with a clear message
	// rather than letting Save fail opaquely.
	if len(cfg.Ingests) == 1 {
		return ValidationError{Msg: "cannot delete the last ingest source"}
	}
	cfg.Ingests = append(cfg.Ingests[:idx], cfg.Ingests[idx+1:]...)
	return config.Save(s.cfgPath, cfg)
}

// SetEnabled toggles the enabled flag for an ingest source by name.
func (s *IngestAdminService) SetEnabled(_ context.Context, name string, enabled bool) (domain.IngestSummary, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return domain.IngestSummary{}, err
	}
	ingestCfg, idx, err := config.FindIngest(cfg, name)
	if err != nil {
		return domain.IngestSummary{}, ValidationError{Msg: err.Error()}
	}
	ingestCfg.Enabled = enabled
	cfg.Ingests[idx] = ingestCfg
	if err := config.Save(s.cfgPath, cfg); err != nil {
		return domain.IngestSummary{}, err
	}
	return ingestSummary(ingestCfg), nil
}

// validateIngestSource enforces name/kind/source-root/rules correctness for a
// create or update. Failures are ValidationError (HTTP 400).
func validateIngestSource(ic config.IngestConfig) error {
	if strings.TrimSpace(ic.Name) == "" {
		return ValidationError{Msg: "ingest name is required"}
	}
	if !supportsIngestKind(ic.Kind) {
		return ValidationError{Msg: fmt.Sprintf("unsupported ingest kind %q", ic.Kind)}
	}
	if strings.TrimSpace(ic.Source.Root) == "" {
		return ValidationError{Msg: "source.root is required"}
	}
	root := config.ExpandHome(ic.Source.Root)
	info, err := os.Stat(root)
	if err != nil {
		return ValidationError{Msg: fmt.Sprintf("source root error: %v", err)}
	}
	if !info.IsDir() {
		return ValidationError{Msg: fmt.Sprintf("source root is not a directory: %s", root)}
	}
	if err := validateIngestRules(ic); err != nil {
		return ValidationError{Msg: err.Error()}
	}
	return nil
}

// validateIngestRules confirms the rules map decodes cleanly into the typed
// rules struct for the ingest's kind.
func validateIngestRules(ic config.IngestConfig) error {
	var err error
	switch ic.Kind {
	case "claude_code":
		_, err = config.DecodeRules[config.ClaudeCodeRules](ic)
	case "chatgpt_export":
		_, err = config.DecodeRules[config.ChatGPTExportRules](ic)
	case "url_source":
		_, err = config.DecodeRules[config.URLSourceRules](ic)
	}
	return err
}

func (s *IngestAdminService) Validate(_ context.Context, name string) (domain.IngestValidationResult, error) {
	cfg, ingestCfg, _, err := s.loadNamedIngest(name)
	if err != nil {
		return domain.IngestValidationResult{}, err
	}
	_ = cfg

	result := domain.IngestValidationResult{
		Name:       ingestCfg.Name,
		Kind:       ingestCfg.Kind,
		Enabled:    ingestCfg.Enabled,
		SourceRoot: ingestCfg.Source.Root,
		Valid:      true,
	}
	if !supportsIngestKind(ingestCfg.Kind) {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("unsupported ingest kind %q", ingestCfg.Kind))
		return result, nil
	}

	root := config.ExpandHome(ingestCfg.Source.Root)
	info, err := os.Stat(root)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("source root error: %v", err))
	} else if !info.IsDir() {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("source root is not a directory: %s", root))
	}

	switch ingestCfg.Kind {
	case "claude_code":
		if _, err := config.DecodeRules[config.ClaudeCodeRules](ingestCfg); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		matches, _ := filepath.Glob(filepath.Join(root, "projects", "*", "*.jsonl"))
		if len(matches) == 0 {
			result.Warnings = append(result.Warnings, "no Claude session files found under projects/*/*.jsonl")
		}
	case "chatgpt_export":
		rules, err := config.DecodeRules[config.ChatGPTExportRules](ingestCfg)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		result.ArchiveRoot = rules.ArchiveRoot
		warnings, policyErr := chatgpt.ValidateArchivePolicy(ingestCfg.Source.Root, rules)
		result.Warnings = append(result.Warnings, warnings...)
		if policyErr != nil {
			result.Valid = false
			result.Errors = append(result.Errors, policyErr.Error())
		}
		if len(chatGPTConversationMatches(root)) == 0 {
			result.Warnings = append(result.Warnings, "no ChatGPT conversation files found")
		}
	case "url_source":
		if len(urlManifestMatches(root)) == 0 {
			result.Warnings = append(result.Warnings, "no URL manifest files found")
		}
	}
	return result, nil
}

func (s *IngestAdminService) Preview(ctx context.Context, name string, limit int) (domain.IngestPreviewResult, error) {
	_, ingestCfg, _, err := s.loadNamedIngest(name)
	if err != nil {
		return domain.IngestPreviewResult{}, err
	}
	previewCfg, err := sanitizePreviewIngestConfig(ingestCfg)
	if err != nil {
		return domain.IngestPreviewResult{}, err
	}
	fragments, err := ingest.CollectWithDefaultSources(ctx, previewCfg)
	if err != nil {
		return domain.IngestPreviewResult{}, err
	}
	result := domain.IngestPreviewResult{
		Name:         ingestCfg.Name,
		Kind:         ingestCfg.Kind,
		Enabled:      ingestCfg.Enabled,
		SourceRoot:   ingestCfg.Source.Root,
		PreviewCount: len(fragments),
		Items:        make([]domain.IngestPreviewItem, 0, min(limit, len(fragments))),
	}
	for i, item := range fragments {
		if result.EarliestAt == nil || item.CreatedAt.Before(*result.EarliestAt) {
			ts := item.CreatedAt
			result.EarliestAt = &ts
		}
		if result.LatestAt == nil || item.CreatedAt.After(*result.LatestAt) {
			ts := item.CreatedAt
			result.LatestAt = &ts
		}
		if limit > 0 && i >= limit {
			continue
		}
		result.Items = append(result.Items, domain.IngestPreviewItem{
			SourceID:      item.SourceID,
			Title:         item.Title,
			Source:        item.Source,
			SourceType:    item.SourceType,
			CreatedAt:     item.CreatedAt,
			CanonicalPath: item.CanonicalPath,
			ContentBytes:  len(item.Content),
		})
	}
	return result, nil
}

func (s *IngestAdminService) UpdateArchivePolicy(_ context.Context, name, archiveRoot string, copyTextExports, deleteCopiedSource bool) (domain.IngestArchivePolicyResult, error) {
	cfg, ingestCfg, idx, err := s.loadNamedIngest(name)
	if err != nil {
		return domain.IngestArchivePolicyResult{}, err
	}
	if ingestCfg.Kind != "chatgpt_export" {
		return domain.IngestArchivePolicyResult{}, fmt.Errorf("archive policy updates currently only support chatgpt_export")
	}
	rules, err := config.DecodeRules[config.ChatGPTExportRules](ingestCfg)
	if err != nil {
		return domain.IngestArchivePolicyResult{}, err
	}
	rules.ArchiveRoot = archiveRoot
	rules.CopyTextExports = copyTextExports
	rules.DeleteCopiedSource = deleteCopiedSource
	if _, err := chatgpt.ValidateArchivePolicy(ingestCfg.Source.Root, rules); err != nil {
		return domain.IngestArchivePolicyResult{}, err
	}
	ingestCfg.Rules = map[string]any{
		"max_file_size_mb":     rules.MaxFileSizeMB,
		"archive_root":         rules.ArchiveRoot,
		"copy_text_exports":    rules.CopyTextExports,
		"delete_copied_source": rules.DeleteCopiedSource,
	}
	cfg.Ingests[idx] = ingestCfg
	if err := config.Save(s.cfgPath, cfg); err != nil {
		return domain.IngestArchivePolicyResult{}, err
	}
	return domain.IngestArchivePolicyResult{
		Name:               ingestCfg.Name,
		Kind:               ingestCfg.Kind,
		ArchiveRoot:        rules.ArchiveRoot,
		CopyTextExports:    rules.CopyTextExports,
		DeleteCopiedSource: rules.DeleteCopiedSource,
	}, nil
}

func (s *IngestAdminService) loadNamedIngest(name string) (config.Config, config.IngestConfig, int, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return config.Config{}, config.IngestConfig{}, -1, err
	}
	ingestCfg, idx, err := config.FindIngest(cfg, name)
	if err != nil {
		return config.Config{}, config.IngestConfig{}, -1, err
	}
	return cfg, ingestCfg, idx, nil
}

func sanitizePreviewIngestConfig(ingestCfg config.IngestConfig) (config.IngestConfig, error) {
	switch ingestCfg.Kind {
	case "chatgpt_export":
		rules, err := config.DecodeRules[config.ChatGPTExportRules](ingestCfg)
		if err != nil {
			return config.IngestConfig{}, err
		}
		rules.CopyTextExports = false
		rules.DeleteCopiedSource = false
		ingestCfg.Rules = map[string]any{
			"max_file_size_mb":     rules.MaxFileSizeMB,
			"archive_root":         rules.ArchiveRoot,
			"copy_text_exports":    false,
			"delete_copied_source": false,
		}
	}
	return ingestCfg, nil
}

func supportsIngestKind(kind string) bool {
	for _, source := range ingest.DefaultSources() {
		if source.Kind() == kind {
			return true
		}
	}
	return false
}

func chatGPTConversationMatches(root string) []string {
	if matched, _ := filepath.Glob(filepath.Join(root, "conversations-*.json")); len(matched) > 0 {
		return matched
	}
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if ok, _ := filepath.Match("conversations-*.json", filepath.Base(path)); ok {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func urlManifestMatches(root string) []string {
	var out []string
	for _, pattern := range []string{"*.txt", "*.json", "*.jsonl"} {
		matched, _ := filepath.Glob(filepath.Join(root, pattern))
		out = append(out, matched...)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
