package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIsLogChunk(t *testing.T) {
	for rel, want := range map[string]bool{
		"brain/c1/.system_generated/logs/chunks/transcript/0.jsonl":       true,
		"brain/c1/.system_generated/logs/chunks/transcript_full/12.jsonl": true,
		"brain/c1/.system_generated/logs/transcript.jsonl":                false,
		"brain/c1/.system_generated/logs/transcript_full.jsonl":           false,
		"brain/c1/.system_generated/steps/3/output.txt":                   false,
		"brain/c1/scratch/chunks/notes.txt":                               false,
		"brain/c1/.system_generated/logs/chunks":                          false,
		"conversations/c1.db":                                             false,
	} {
		if got := isLogChunk(rel); got != want {
			t.Fatalf("%s: got %v want %v", rel, got, want)
		}
	}
}

func chunkFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	logs := filepath.Join(root, "brain", "c1", ".system_generated", "logs")
	for path, body := range map[string]string{
		filepath.Join(logs, "transcript.jsonl"):                "{\"step_index\":0}\n{\"step_index\":1}\n",
		filepath.Join(logs, "chunks", "transcript", "0.jsonl"): "{\"step_index\":0}\n{\"step_index\":1}\n",
		filepath.Join(logs, "chunks", "transcript", "1.jsonl"): "{\"step_index\":1}\n",
		filepath.Join(root, "brain", "c1", "scratch", "n.txt"): "scratch note",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLogChunksAreExcludedByDefaultAndCounted(t *testing.T) {
	root := chunkFixture(t)
	got, skips := collectOne(t, root, context.Background(), nil)
	if len(got) != 2 {
		t.Fatalf("expected the parent log and the scratch note, got %d", len(got))
	}
	for _, f := range got {
		if isLogChunk(f.SourceIdentity.SegmentKey) {
			t.Fatalf("a chunk was imported: %s", f.SourceIdentity.SegmentKey)
		}
	}
	if by := skips.ByClass(); by["log_chunks_excluded"] != 2 || len(by) != 1 {
		t.Fatalf("skip accounting: %v", by)
	}
}

func TestLogChunksCanBeIncludedAgain(t *testing.T) {
	root := chunkFixture(t)
	got, skips := collectOne(t, root, context.Background(), map[string]any{"include_log_chunks": true})
	if len(got) != 4 {
		t.Fatalf("expected all four artifacts with the override, got %d", len(got))
	}
	if by := skips.ByClass(); by["log_chunks_excluded"] != 0 {
		t.Fatalf("nothing should be excluded with the override: %v", by)
	}
}
