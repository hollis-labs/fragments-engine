package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestCollectNativeAndArchiveSubagents(t *testing.T) {
	for _, layout := range []string{"native", "archive"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "synthetic-project")
			if layout == "native" {
				project = filepath.Join(root, "projects", "synthetic-project")
			}
			paths := []string{"session.jsonl", "session/subagents/agent-one.jsonl", "session/subagents/nested/agent-two.jsonl"}
			for _, rel := range paths {
				path := filepath.Join(project, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				raw := `{"type":"assistant","sessionId":"same-parent","timestamp":"2026-10-09T00:00:00Z","message":{"content":[{"type":"text","text":"synthetic answer"}]}}`
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Unrelated nested JSONL, oversize files and links are not sessions.
			unrelated := filepath.Join(project, "other", "cache.jsonl")
			if err := os.MkdirAll(filepath.Dir(unrelated), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unrelated, []byte(`{"type":"user","message":{"content":"not a session"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, "large.jsonl"), []byte(strings.Repeat("x", 1024*1024+1)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(project, "session.jsonl"), filepath.Join(project, "linked.jsonl")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(project, filepath.Join(root, "linked-project")); err != nil {
				t.Fatal(err)
			}
			cfg := config.IngestConfig{Kind: kind, Source: config.IngestSource{Root: root}, Rules: map[string]any{"max_file_size_mb": 1}}
			got, err := (Source{}).Collect(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Fatalf("expected parent and two subagents, got %d", len(got))
			}
			seen := map[string]bool{}
			for _, fragment := range got {
				if fragment.SourceID != "same-parent" || !strings.Contains(fragment.Content, "synthetic answer") {
					t.Fatal("session material or parent identity lost")
				}
				segment := fragment.SourceIdentity.SegmentKey
				if seen[segment] {
					t.Fatalf("duplicate segment %q", segment)
				}
				seen[segment] = true
			}
			for _, segment := range []string{"", "subagent/session/subagents/agent-one.jsonl", "subagent/session/subagents/nested/agent-two.jsonl"} {
				if !seen[segment] {
					t.Fatalf("missing stable segment %q", segment)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := (Source{}).Collect(ctx, cfg); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
