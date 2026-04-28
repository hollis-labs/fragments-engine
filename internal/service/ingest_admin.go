package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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

func (s *IngestAdminService) List(_ context.Context) ([]domain.IngestSummary, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, err
	}
	items := make([]domain.IngestSummary, 0, len(cfg.Ingests))
	for _, ingestCfg := range cfg.Ingests {
		item := domain.IngestSummary{
			Name:       ingestCfg.Name,
			Kind:       ingestCfg.Kind,
			Enabled:    ingestCfg.Enabled,
			SourceRoot: ingestCfg.Source.Root,
			Namespace:  ingestCfg.Routing.Namespace,
			Labels:     ingestCfg.Labels,
		}
		if ingestCfg.Kind == "chatgpt_export" {
			rules, err := config.DecodeRules[config.ChatGPTExportRules](ingestCfg)
			if err == nil {
				item.ArchiveRoot = rules.ArchiveRoot
				item.CopyTextExports = rules.CopyTextExports
				item.DeleteCopiedSource = rules.DeleteCopiedSource
			}
		}
		items = append(items, item)
	}
	return items, nil
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
