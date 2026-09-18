package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

// fixtureConfigYAML mirrors the shape of a real hand-seeded fragments.yaml:
// comments a human wrote, plus two ingests in a deliberate order.
const fixtureConfigYAML = `database:
  path: ./data/fragments-engine.db # lives beside the config file

# Ingests are added through FE itself via CLI/API/MCP; this list is the
# starting point copied from the template.
ingests:
  - name: claude-default
    kind: claude_code
    enabled: true
    source:
      root: ~/.claude
    routing:
      namespace: fragments/chats/claude
  - name: chatgpt-export
    kind: chatgpt_export
    enabled: false
    source:
      root: ~/Downloads
    routing:
      namespace: fragments/chats/chatgpt
`

// TestSave_PreservesCommentsAndUntouchedEntriesOnCRUDStyleEdit is the direct
// regression test for the documented fragments.yaml wart: the ingest CRUD
// write path (Load -> mutate one ingest, add another -> Save) used to
// round-trip through a full yaml.Marshal, destroying every comment and
// reordering everything to Go struct-field order. Save must now touch only
// what actually changed.
func TestSave_PreservesCommentsAndUntouchedEntriesOnCRUDStyleEdit(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	if err := os.WriteFile(cfgPath, []byte(fixtureConfigYAML), 0o600); err != nil {
		t.Fatalf("seed fixture config: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Ingests) != 2 {
		t.Fatalf("expected 2 seeded ingests, got %d", len(cfg.Ingests))
	}

	// A CRUD-style edit: disable one existing ingest, add a brand new one --
	// exactly the shape of IngestAdminService.SetEnabled followed by Create.
	cfg.Ingests[0].Enabled = false
	cfg.Ingests = append(cfg.Ingests, config.IngestConfig{
		Name:    "git-changes",
		Kind:    "git_changes",
		Enabled: true,
		Source:  config.IngestSource{Root: "~/dev"},
		Routing: config.IngestRouting{Namespace: "fragments/repos/git"},
	})

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	written := string(raw)

	// The comments a human wrote must survive -- this is the whole point.
	for _, want := range []string{
		"# lives beside the config file",
		"# Ingests are added through FE itself via CLI/API/MCP; this list is the",
		"# starting point copied from the template.",
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("expected comment %q to survive the save, got:\n%s", want, written)
		}
	}

	// The untouched ingest (chatgpt-export) must survive byte-for-byte in
	// shape -- still present, still disabled, still pointed at ~/Downloads.
	if !strings.Contains(written, "name: chatgpt-export") {
		t.Fatalf("expected untouched ingest chatgpt-export to survive, got:\n%s", written)
	}
	if !strings.Contains(written, "root: ~/Downloads") {
		t.Fatalf("expected untouched ingest's source root to survive unexpanded (~), got:\n%s", written)
	}

	// The actual edits must be reflected.
	if !strings.Contains(written, "name: git-changes") {
		t.Fatalf("expected new ingest git-changes to be appended, got:\n%s", written)
	}

	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload after save: %v", err)
	}
	if len(reloaded.Ingests) != 3 {
		t.Fatalf("expected 3 ingests after save, got %d", len(reloaded.Ingests))
	}
	var claude, chatgpt *config.IngestConfig
	for i := range reloaded.Ingests {
		switch reloaded.Ingests[i].Name {
		case "claude-default":
			claude = &reloaded.Ingests[i]
		case "chatgpt-export":
			chatgpt = &reloaded.Ingests[i]
		}
	}
	if claude == nil || claude.Enabled {
		t.Fatalf("expected claude-default disabled after edit, got %+v", claude)
	}
	if chatgpt == nil || chatgpt.Enabled {
		t.Fatalf("expected chatgpt-export unchanged (still disabled), got %+v", chatgpt)
	}
}

// TestSave_RemovesIngestDroppedFromDesiredConfig covers IngestAdminService's
// Delete path: an ingest present on disk but absent from the desired Config
// must be dropped, not left behind as an orphaned entry.
func TestSave_RemovesIngestDroppedFromDesiredConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "fragments.yaml")
	if err := os.WriteFile(cfgPath, []byte(fixtureConfigYAML), 0o600); err != nil {
		t.Fatalf("seed fixture config: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Ingests = cfg.Ingests[:1] // drop chatgpt-export, keep claude-default

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(raw), "chatgpt-export") {
		t.Fatalf("expected chatgpt-export removed, got:\n%s", raw)
	}
	if !strings.Contains(string(raw), "claude-default") {
		t.Fatalf("expected claude-default to remain, got:\n%s", raw)
	}
}

// TestSave_NoExistingFile_FallsBackToPlainMarshal covers the first-write
// case (no prior file to merge against, e.g. a fresh install outside the
// normal seed-config.sh flow): Save must still succeed.
func TestSave_NoExistingFile_FallsBackToPlainMarshal(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "fresh", "fragments.yaml")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "fragments.db")},
		Ingests: []config.IngestConfig{
			{Name: "claude-default", Kind: "claude_code", Enabled: true, Source: config.IngestSource{Root: "~/.claude"}},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Ingests) != 1 || reloaded.Ingests[0].Name != "claude-default" {
		t.Fatalf("unexpected reloaded ingests: %+v", reloaded.Ingests)
	}
}
