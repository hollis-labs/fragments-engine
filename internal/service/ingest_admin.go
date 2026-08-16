package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/ingest/chatgpt"
	"github.com/hollis-labs/fragments-engine/internal/ingest/sourceutil"
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

// Get returns the complete config record for a single ingest source, keyed by
// name, including the raw rules map. Unlike List — which projects onto
// IngestSummary and drops rules — this lets the Sysop edit UI round-trip rules
// without data loss. Unknown name → ValidationError (HTTP 400).
func (s *IngestAdminService) Get(_ context.Context, name string) (domain.IngestRecord, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return domain.IngestRecord{}, err
	}
	ingestCfg, _, err := config.FindIngest(cfg, name)
	if err != nil {
		return domain.IngestRecord{}, ValidationError{Msg: err.Error()}
	}
	return ingestRecord(ingestCfg), nil
}

// ingestRecord projects an IngestConfig onto the full API record shape,
// carrying the raw rules map verbatim.
func ingestRecord(ic config.IngestConfig) domain.IngestRecord {
	return domain.IngestRecord{
		Name:       ic.Name,
		Kind:       ic.Kind,
		Enabled:    ic.Enabled,
		SourceRoot: ic.Source.Root,
		Namespace:  ic.Routing.Namespace,
		Rules:      ic.Rules,
		Labels:     ic.Labels,
	}
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
	switch ic.Kind {
	case "claude_code":
		_, err := config.DecodeRules[config.ClaudeCodeRules](ic)
		return err
	case "chatgpt_export":
		_, err := config.DecodeRules[config.ChatGPTExportRules](ic)
		return err
	case "url_source":
		_, err := config.DecodeRules[config.URLSourceRules](ic)
		return err
	case "filesystem_docs":
		rules, err := config.DecodeRules[config.FilesystemDocsRules](ic)
		if err != nil {
			return err
		}
		if err := sourceutil.ValidateGlobs(rules.Include); err != nil {
			return err
		}
		return sourceutil.ValidateGlobs(rules.Exclude)
	case "git_changes":
		rules, err := config.DecodeRules[config.GitChangesRules](ic)
		if err != nil {
			return err
		}
		if err := sourceutil.ValidateGlobs(rules.Include); err != nil {
			return err
		}
		if err := sourceutil.ValidateGlobs(rules.Exclude); err != nil {
			return err
		}
		return validateGitRepoRoots(config.ExpandHome(ic.Source.Root), rules.Repos)
	case "nil_vault":
		rules, err := config.DecodeRules[config.NilVaultRules](ic)
		if err != nil {
			return err
		}
		_, err = nilVaultConfigMatches(config.ExpandHome(ic.Source.Root), rules)
		return err
	}
	return nil
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
	case "filesystem_docs":
		rules, err := config.DecodeRules[config.FilesystemDocsRules](ingestCfg)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if err := sourceutil.ValidateGlobs(rules.Include); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if err := sourceutil.ValidateGlobs(rules.Exclude); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if len(filesystemDocMatches(root, rules)) == 0 {
			result.Warnings = append(result.Warnings, "no matching filesystem docs found")
		}
	case "git_changes":
		rules, err := config.DecodeRules[config.GitChangesRules](ingestCfg)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if err := sourceutil.ValidateGlobs(rules.Include); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if err := sourceutil.ValidateGlobs(rules.Exclude); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if err := validateGitRepoRoots(root, rules.Repos); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if count := len(gitRepoMatches(root, rules.Repos)); count == 0 {
			result.Warnings = append(result.Warnings, "no git repositories found")
		}
	case "nil_vault":
		rules, err := config.DecodeRules[config.NilVaultRules](ingestCfg)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		matches, err := nilVaultConfigMatches(root, rules)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if len(matches) == 0 {
			result.Warnings = append(result.Warnings, "no nil vaults found")
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

func filesystemDocMatches(root string, rules config.FilesystemDocsRules) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		include, includeErr := sourceutil.ShouldIncludePath(rel, rules.Include, rules.Exclude)
		if includeErr == nil && include {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func validateGitRepoRoots(root string, repos []string) error {
	for _, repoRoot := range gitRepoMatches(root, repos) {
		info, err := os.Stat(filepath.Join(repoRoot, ".git"))
		if err == nil && info.IsDir() {
			continue
		}
		return fmt.Errorf("git repo not found: %s", repoRoot)
	}
	if len(repos) > 0 && len(gitRepoMatches(root, repos)) == 0 {
		return fmt.Errorf("no git repos configured under %s", root)
	}
	return nil
}

func gitRepoMatches(root string, repos []string) []string {
	if len(repos) == 0 {
		return []string{root}
	}
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		if filepath.IsAbs(repo) {
			out = append(out, repo)
			continue
		}
		out = append(out, filepath.Join(root, repo))
	}
	return out
}

// nilVaultConfigMatches reads root/config.json (Nil's vault registry) and
// returns the names of vaults selected by rules' include/exclude lists
// (matched by either vault ID or name, mirroring nilvault's own filtering).
// An unreadable or unparseable config.json is an error, not an empty match
// list, since that means the ingest can never find any vaults at all.
func nilVaultConfigMatches(root string, rules config.NilVaultRules) ([]string, error) {
	configPath := filepath.Join(root, "config.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read nil config %s: %w", configPath, err)
	}
	var parsed struct {
		Vaults []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"vaults"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode nil config %s: %w", configPath, err)
	}

	include := make(map[string]struct{}, len(rules.IncludeVaults))
	for _, v := range rules.IncludeVaults {
		if v = strings.TrimSpace(v); v != "" {
			include[v] = struct{}{}
		}
	}
	exclude := make(map[string]struct{}, len(rules.ExcludeVaults))
	for _, v := range rules.ExcludeVaults {
		if v = strings.TrimSpace(v); v != "" {
			exclude[v] = struct{}{}
		}
	}

	var out []string
	for _, vault := range parsed.Vaults {
		if len(include) > 0 {
			_, byID := include[vault.ID]
			_, byName := include[vault.Name]
			if !byID && !byName {
				continue
			}
		}
		if _, byID := exclude[vault.ID]; byID {
			continue
		}
		if _, byName := exclude[vault.Name]; byName {
			continue
		}
		out = append(out, vault.Name)
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
