package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestIngestAdminService_ListValidatePreviewAndArchivePolicy(t *testing.T) {
	claudeRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "claude"))
	if err != nil {
		t.Fatalf("resolve claude fixture: %v", err)
	}
	chatGPTRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "chatgpt-export"))
	if err != nil {
		t.Fatalf("resolve chatgpt fixture: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "fragments.db")},
		Recall:   config.RecallConfig{Backend: "sqlite"},
		Ingests: []config.IngestConfig{
			{
				Name:    "claude-fixture",
				Kind:    "claude_code",
				Enabled: true,
				Source:  config.IngestSource{Root: claudeRoot},
				Routing: config.IngestRouting{Namespace: "fragments/chats/claude"},
				Rules:   map[string]any{"max_file_size_mb": 50},
			},
			{
				Name:    "chatgpt-fixture",
				Kind:    "chatgpt_export",
				Enabled: true,
				Source:  config.IngestSource{Root: chatGPTRoot},
				Routing: config.IngestRouting{Namespace: "fragments/chats/chatgpt"},
				Rules: map[string]any{
					"max_file_size_mb":     50,
					"archive_root":         "~/Documents/corpus/ai-chat-logs/chatgpt/logs/test-fixture",
					"copy_text_exports":    true,
					"delete_copied_source": false,
				},
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	svc := NewIngestAdminService(cfgPath)

	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("list ingests: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 ingests, got %d", len(list))
	}
	if list[1].ArchiveRoot != "~/Documents/corpus/ai-chat-logs/chatgpt/logs/test-fixture" || !list[1].CopyTextExports {
		t.Fatalf("expected chatgpt archive policy in list output: %+v", list[1])
	}

	validation, err := svc.Validate(context.Background(), "chatgpt-fixture")
	if err != nil {
		t.Fatalf("validate ingest: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("expected valid chatgpt ingest: %+v", validation)
	}

	preview, err := svc.Preview(context.Background(), "chatgpt-fixture", 5)
	if err != nil {
		t.Fatalf("preview ingest: %v", err)
	}
	if preview.PreviewCount != 1 {
		t.Fatalf("expected 1 preview fragment, got %d", preview.PreviewCount)
	}
	if len(preview.Items) != 1 || !strings.Contains(preview.Items[0].Title, "Roadmap planning") {
		t.Fatalf("unexpected preview items: %+v", preview.Items)
	}
	if _, err := os.Stat(config.ExpandHome(list[1].ArchiveRoot)); !os.IsNotExist(err) {
		t.Fatalf("preview should not create archive root, got err=%v", err)
	}

	policy, err := svc.UpdateArchivePolicy(context.Background(), "chatgpt-fixture", list[1].ArchiveRoot, true, false)
	if err != nil {
		t.Fatalf("update archive policy: %v", err)
	}
	if !policy.CopyTextExports || policy.DeleteCopiedSource {
		t.Fatalf("unexpected archive policy result: %+v", policy)
	}

	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	ingestCfg, _, err := config.FindIngest(reloaded, "chatgpt-fixture")
	if err != nil {
		t.Fatalf("find updated ingest: %v", err)
	}
	rules, err := config.DecodeRules[config.ChatGPTExportRules](ingestCfg)
	if err != nil {
		t.Fatalf("decode updated rules: %v", err)
	}
	if rules.ArchiveRoot != list[1].ArchiveRoot || !rules.CopyTextExports || rules.DeleteCopiedSource {
		t.Fatalf("unexpected persisted rules: %+v", rules)
	}
}

func TestIngestAdminService_Get(t *testing.T) {
	claudeRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "claude"))
	if err != nil {
		t.Fatalf("resolve claude fixture: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "fragments.db")},
		Recall:   config.RecallConfig{Backend: "sqlite"},
		Ingests: []config.IngestConfig{
			{
				Name:    "claude-fixture",
				Kind:    "claude_code",
				Enabled: true,
				Source:  config.IngestSource{Root: claudeRoot},
				Routing: config.IngestRouting{Namespace: "fragments/chats/claude"},
				Rules:   map[string]any{"max_file_size_mb": 50},
				Labels:  map[string]string{"team": "platform"},
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	svc := NewIngestAdminService(cfgPath)

	rec, err := svc.Get(context.Background(), "claude-fixture")
	if err != nil {
		t.Fatalf("get ingest: %v", err)
	}
	if rec.Name != "claude-fixture" || rec.Kind != "claude_code" || !rec.Enabled {
		t.Fatalf("unexpected record header: %+v", rec)
	}
	if rec.SourceRoot != claudeRoot || rec.Namespace != "fragments/chats/claude" {
		t.Fatalf("unexpected record source/namespace: %+v", rec)
	}
	if rec.Labels["team"] != "platform" {
		t.Fatalf("expected labels carried verbatim: %+v", rec.Labels)
	}
	// The raw rules map must survive — this is the whole point of Get vs List.
	if rec.Rules["max_file_size_mb"] != 50 {
		t.Fatalf("expected rules map carried verbatim: %+v", rec.Rules)
	}

	// Unknown name is caller error → ValidationError (HTTP 400).
	if _, err := svc.Get(context.Background(), "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown ingest name")
	} else if _, ok := err.(ValidationError); !ok {
		t.Fatalf("expected ValidationError for unknown name, got %T: %v", err, err)
	}
}

func TestIngestAdminService_ValidateRejectsUnsafeArchivePolicy(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "chatgpt-export"))
	if err != nil {
		t.Fatalf("resolve chatgpt fixture: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "fragments.db")},
		Recall:   config.RecallConfig{Backend: "sqlite"},
		Ingests: []config.IngestConfig{
			{
				Name:    "bad-chatgpt",
				Kind:    "chatgpt_export",
				Enabled: true,
				Source:  config.IngestSource{Root: root},
				Routing: config.IngestRouting{Namespace: "fragments/chats/chatgpt"},
				Rules: map[string]any{
					"delete_copied_source": true,
					"copy_text_exports":    false,
				},
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	svc := NewIngestAdminService(cfgPath)
	result, err := svc.Validate(context.Background(), "bad-chatgpt")
	if err != nil {
		t.Fatalf("validate bad ingest: %v", err)
	}
	if result.Valid {
		t.Fatalf("expected invalid result: %+v", result)
	}
	if len(result.Errors) == 0 || !strings.Contains(strings.Join(result.Errors, " "), "delete_copied_source requires copy_text_exports=true") {
		t.Fatalf("expected archive policy validation error: %+v", result)
	}
}

func TestIngestAdminService_ValidateFilesystemDocsAndGitChanges(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "sample-repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# Readme\n"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "fragments.db")},
		Recall:   config.RecallConfig{Backend: "sqlite"},
		Ingests: []config.IngestConfig{
			{
				Name:    "docs-fixture",
				Kind:    "filesystem_docs",
				Enabled: true,
				Source:  config.IngestSource{Root: root},
				Routing: config.IngestRouting{Namespace: "fragments/repos/docs"},
				Rules: map[string]any{
					"include": []string{"**/*.md"},
					"exclude": []string{"**/node_modules/**"},
				},
			},
			{
				Name:    "git-fixture",
				Kind:    "git_changes",
				Enabled: true,
				Source:  config.IngestSource{Root: root},
				Routing: config.IngestRouting{Namespace: "fragments/repos/git"},
				Rules: map[string]any{
					"repos":       []string{"sample-repo"},
					"branch":      "main",
					"max_commits": 25,
					"include":     []string{"**/*.md"},
				},
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	svc := NewIngestAdminService(cfgPath)

	docsResult, err := svc.Validate(context.Background(), "docs-fixture")
	if err != nil {
		t.Fatalf("validate docs ingest: %v", err)
	}
	if !docsResult.Valid {
		t.Fatalf("expected valid docs ingest: %+v", docsResult)
	}

	gitResult, err := svc.Validate(context.Background(), "git-fixture")
	if err != nil {
		t.Fatalf("validate git ingest: %v", err)
	}
	if !gitResult.Valid {
		t.Fatalf("expected valid git ingest: %+v", gitResult)
	}
}

func TestIngestAdminService_ValidateNilVault(t *testing.T) {
	populatedRoot := t.TempDir()
	nilConfigJSON := `{"activeVaultId":"vault-1","inboxPath":"","vaults":[{"id":"vault-1","name":"Personal","path":"` + filepath.ToSlash(t.TempDir()) + `","created_at":"2026-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(populatedRoot, "config.json"), []byte(nilConfigJSON), 0o600); err != nil {
		t.Fatalf("write nil config: %v", err)
	}

	emptyRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(emptyRoot, "config.json"), []byte(`{"activeVaultId":"","inboxPath":"","vaults":[]}`), 0o600); err != nil {
		t.Fatalf("write empty nil config: %v", err)
	}

	missingConfigRoot := t.TempDir()

	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "fragments.db")},
		Recall:   config.RecallConfig{Backend: "sqlite"},
		Ingests: []config.IngestConfig{
			{
				Name:    "nil-fixture",
				Kind:    "nil_vault",
				Enabled: true,
				Source:  config.IngestSource{Root: populatedRoot},
				Routing: config.IngestRouting{Namespace: "fragments/nil"},
			},
			{
				Name:    "nil-empty",
				Kind:    "nil_vault",
				Enabled: true,
				Source:  config.IngestSource{Root: emptyRoot},
				Routing: config.IngestRouting{Namespace: "fragments/nil"},
			},
			{
				Name:    "nil-missing-config",
				Kind:    "nil_vault",
				Enabled: true,
				Source:  config.IngestSource{Root: missingConfigRoot},
				Routing: config.IngestRouting{Namespace: "fragments/nil"},
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	svc := NewIngestAdminService(cfgPath)

	populatedResult, err := svc.Validate(context.Background(), "nil-fixture")
	if err != nil {
		t.Fatalf("validate nil ingest: %v", err)
	}
	if !populatedResult.Valid {
		t.Fatalf("expected valid nil ingest: %+v", populatedResult)
	}
	if len(populatedResult.Warnings) != 0 {
		t.Fatalf("expected no warnings for a vault with matches: %+v", populatedResult.Warnings)
	}

	emptyResult, err := svc.Validate(context.Background(), "nil-empty")
	if err != nil {
		t.Fatalf("validate empty nil ingest: %v", err)
	}
	if !emptyResult.Valid {
		t.Fatalf("expected valid (but warned) nil ingest: %+v", emptyResult)
	}
	if len(emptyResult.Warnings) == 0 || !strings.Contains(strings.Join(emptyResult.Warnings, " "), "no nil vaults found") {
		t.Fatalf("expected 'no nil vaults found' warning: %+v", emptyResult.Warnings)
	}

	missingResult, err := svc.Validate(context.Background(), "nil-missing-config")
	if err != nil {
		t.Fatalf("validate missing-config nil ingest: %v", err)
	}
	if missingResult.Valid {
		t.Fatalf("expected invalid result for missing config.json: %+v", missingResult)
	}
}
