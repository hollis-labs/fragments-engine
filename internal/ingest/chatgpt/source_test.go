package chatgpt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestSourceCollect(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "chatgpt-export"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}

	fragments, err := Source{}.Collect(context.Background(), config.IngestConfig{
		Name:   "chatgpt-test",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
	})
	if err != nil {
		t.Fatalf("collect chatgpt export: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(fragments))
	}
	item := fragments[0]
	if item.Source != "chatgpt" || item.SourceType != "chat" {
		t.Fatalf("unexpected source fields: %+v", item)
	}
	if item.SourceID != "conv-123" {
		t.Fatalf("unexpected source id: %s", item.SourceID)
	}
	if item.Title != "ChatGPT chat: Roadmap planning" {
		t.Fatalf("unexpected title: %s", item.Title)
	}
	if !strings.Contains(item.Content, "## user") || !strings.Contains(item.Content, "## assistant") {
		t.Fatalf("expected user and assistant blocks: %s", item.Content)
	}
	if !strings.Contains(item.Content, "deterministic ingest and search") {
		t.Fatalf("expected assistant content: %s", item.Content)
	}
	if item.Metadata["user_email"] != "tester@example.com" {
		t.Fatalf("expected user email metadata, got %#v", item.Metadata["user_email"])
	}
	if len(item.Attachments) != 3 {
		t.Fatalf("expected 3 attachments, got %d", len(item.Attachments))
	}
	if item.Attachments[0].Kind != "image" || item.Attachments[0].SourcePath == "" {
		t.Fatalf("expected local image attachment, got %+v", item.Attachments[0])
	}
	if item.Attachments[1].Kind != "markdown" || !strings.HasSuffix(item.Attachments[1].SourcePath, "roadmap-note.md") {
		t.Fatalf("expected local markdown attachment, got %+v", item.Attachments[1])
	}
	if item.Attachments[2].Kind != "pdf" || item.Attachments[2].ExternalURL != "https://example.com/roadmap.pdf" {
		t.Fatalf("expected referenced pdf attachment, got %+v", item.Attachments[2])
	}
}

func TestSourceCollect_WithArchiveCopy(t *testing.T) {
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "chatgpt-export", "export-001"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	sourceRoot := filepath.Join(t.TempDir(), "download-export")
	if err := os.MkdirAll(sourceRoot, 0o750); err != nil {
		t.Fatalf("mkdir source root: %v", err)
	}
	for _, name := range []string{"conversations-000.json", "user.json"} {
		raw, err := os.ReadFile(filepath.Join(fixtureRoot, name))
		if err != nil {
			t.Fatalf("read fixture file %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(sourceRoot, name), raw, 0o600); err != nil {
			t.Fatalf("write source file %s: %v", name, err)
		}
	}

	archiveRoot := filepath.Join(config.ExpandHome("~/Documents/corpus/ai-chat-logs/chatgpt/logs"), "fe-test-archive-copy")
	fragments, err := Source{}.Collect(context.Background(), config.IngestConfig{
		Name:   "chatgpt-archive-test",
		Kind:   kind,
		Source: config.IngestSource{Root: sourceRoot},
		Rules: map[string]any{
			"archive_root":         archiveRoot,
			"copy_text_exports":    true,
			"delete_copied_source": false,
		},
	})
	if err != nil {
		t.Fatalf("collect with archive copy: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(fragments))
	}
	copied := filepath.Join(archiveRoot, filepath.Base(sourceRoot), "conversations-000.json")
	if _, err := os.Stat(copied); err != nil {
		t.Fatalf("expected copied archive file at %s: %v", copied, err)
	}
}

func TestValidateArchivePolicy(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "downloads", "chatgpt-export")
	if err := os.MkdirAll(sourceRoot, 0o750); err != nil {
		t.Fatalf("mkdir source root: %v", err)
	}

	_, err := ValidateArchivePolicy(sourceRoot, config.ChatGPTExportRules{
		ArchiveRoot:     filepath.Join(t.TempDir(), "random-root"),
		CopyTextExports: true,
	})
	if err == nil || !strings.Contains(err.Error(), "archive_root must live under") {
		t.Fatalf("expected canonical archive root error, got %v", err)
	}

	canonicalRoot := config.ExpandHome("~/Documents/corpus/ai-chat-logs/chatgpt/logs")
	_, err = ValidateArchivePolicy(filepath.Join(canonicalRoot, "export-123"), config.ChatGPTExportRules{
		ArchiveRoot:     canonicalRoot,
		CopyTextExports: true,
	})
	if err == nil || !strings.Contains(err.Error(), "source.root must not be inside archive_root") {
		t.Fatalf("expected nested source error, got %v", err)
	}
}
