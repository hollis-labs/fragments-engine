package claude

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestCollectParsesClaudeSession(t *testing.T) {
	root := filepath.Join("..", "..", "..", "testdata", "claude")
	source := Source{}
	fragments, err := source.Collect(context.Background(), config.IngestConfig{
		Name:    "claude-test",
		Kind:    "claude_code",
		Enabled: true,
		Source: config.IngestSource{
			Root: root,
		},
		Rules: map[string]any{"max_file_size_mb": 1},
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(fragments))
	}
	got := fragments[0]
	if got.SourceID != "session-123" {
		t.Fatalf("unexpected source id: %s", got.SourceID)
	}
	if got.Title != "Claude session: roadmap-review" {
		t.Fatalf("unexpected title: %s", got.Title)
	}
	if got.Content == "" {
		t.Fatal("expected content")
	}
}
