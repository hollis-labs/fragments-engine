package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const syntheticRollout = `{"timestamp":"2026-10-09T01:00:00Z","type":"session_meta","payload":{"id":"synthetic-thread","session_id":"synthetic-root","timestamp":"2026-10-09T01:00:00Z","cwd":"/synthetic/project","cli_version":"synthetic"}}
{"timestamp":"2026-10-09T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"duplicate user"}}
{"timestamp":"2026-10-09T01:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"roadmap password=synthetic-codex-secret"},{"type":"input_image","image_url":"not imported"}]}}
{"timestamp":"2026-10-09T01:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic answer"}]}}
{"timestamp":"2026-10-09T01:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"duplicate assistant"}}
{"timestamp":"2026-10-09T01:00:02Z","type":"response_item","payload":{"type":"function_call","arguments":"not imported"}}
`

func TestCollectRolloutsAndDistinctPhysicalFiles(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"sessions/2026/10/09/rollout-first.jsonl", "archived_sessions/rollout-second.jsonl"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(syntheticRollout), 0600); err != nil {
			t.Fatal(err)
		}
	}
	partial := filepath.Join(root, "sessions", ".rsync-partial", "rollout.jsonl")
	if err := os.MkdirAll(filepath.Dir(partial), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte(syntheticRollout), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "sessions"), filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "archived_sessions", "rollout-second.jsonl"), filepath.Join(root, "linked.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "oversize.jsonl"), []byte(strings.Repeat("x", 1024*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.IngestConfig{Kind: kind, Source: config.IngestSource{Root: root}, Rules: map[string]any{"max_file_size_mb": 1}}
	got, err := (Source{}).Collect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two real files, got %d", len(got))
	}
	if got[0].SourceIdentity.SegmentKey == got[1].SourceIdentity.SegmentKey {
		t.Fatal("physical rollouts collide")
	}
	for _, fragment := range got {
		if fragment.SourceID != "synthetic-thread" || fragment.SourceType != "transcript" || fragment.CreatedAt.Format("2006-01-02") != "2026-10-09" {
			t.Fatal("session identity/time/classification lost")
		}
		if strings.Contains(fragment.Content, "duplicate") || strings.Contains(fragment.Content, "not imported") || !strings.Contains(fragment.Content, "synthetic answer") {
			t.Fatal("wrong message selection")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Source{}).Collect(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestEventOnlyFallbackAndSafeMalformedRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		wantErr   bool
	}{
		{"event-only", `{"type":"session_meta","payload":{"id":"synthetic-thread"}}
{"type":"event_msg","payload":{"type":"user_message","message":"synthetic question"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"synthetic answer"}}`, false},
		{"malformed", `password=synthetic-no-diagnostic`, true},
		{"missing-meta", `{"type":"event_msg","payload":{"type":"user_message","message":"synthetic-no-diagnostic"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "rollout.jsonl")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}})
			if tc.wantErr {
				if err == nil || strings.Contains(err.Error(), "synthetic-no-diagnostic") || len(got) != 0 {
					t.Fatal("malformed archive not safely refused")
				}
			} else if err != nil || len(got) != 1 || !strings.Contains(got[0].Content, "synthetic question") || !strings.Contains(got[0].Content, "synthetic answer") {
				t.Fatalf("fallback lost: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.raw {
				t.Fatal("source changed")
			}
		})
	}
}

func TestParserEnforcesReadBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(syntheticRollout), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSession(context.Background(), path, info.ModTime(), int64(len(syntheticRollout)-1)); err == nil {
		t.Fatal("read beyond budget accepted")
	}
}

// A forked rollout carries its own session_meta first and replays the parent's
// second. The file keeps its own identity; the parent is recorded as ancestry.
func TestForkedRolloutKeepsOwnIdentityAndRecordsParent(t *testing.T) {
	raw := `{"timestamp":"2026-10-09T01:00:00Z","type":"session_meta","payload":{"id":"child-thread","forked_from_id":"parent-thread","timestamp":"2026-10-09T01:00:00Z"}}
{"timestamp":"2026-10-09T00:00:00Z","type":"session_meta","payload":{"id":"parent-thread","timestamp":"2026-10-08T00:00:00Z"}}
{"timestamp":"2026-10-09T01:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic question"}]}}
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rollout.jsonl"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}})
	if err != nil || len(got) != 1 {
		t.Fatalf("fork refused: %v", err)
	}
	if got[0].SourceID != "child-thread" || got[0].Metadata["parent_thread_id"] != "parent-thread" {
		t.Fatal("fork identity or ancestry wrong")
	}
	if got[0].CreatedAt.Format("2006-01-02") != "2026-10-09" {
		t.Fatalf("parent timestamp leaked into fork: %v", got[0].CreatedAt)
	}
}

// Failures expose a class, a hash of the relative path, a line and a byte
// offset, and nothing from the record.
func TestCollectionFailuresCarrySanitizedDiagnostics(t *testing.T) {
	long := strings.Repeat("x", 11*1024*1024)
	for _, tc := range []struct {
		name, raw, class string
		line             int
		offset           int64
	}{
		{"malformed", "{\"type\":\"session_meta\",\"payload\":{\"id\":\"a\"}}\npassword=synthetic-no-diagnostic\n", "invalid_record", 2, 45},
		{"bad-meta", `{"type":"session_meta","payload":{"id":""}}` + "\n", "invalid_session_meta", 1, 0},
		{"long-line", `{"type":"session_meta","payload":{"id":"a"}}` + "\n" + long + "\n", "unreadable_line", 2, 45},
		{"no-meta", `{"type":"event_msg","payload":{"type":"user_message","message":"synthetic-no-diagnostic"}}` + "\n", "missing_session_meta", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "sub"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "sub", "rollout.jsonl"), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}})
			var diag *domain.CollectionError
			if !errors.As(err, &diag) {
				t.Fatalf("no diagnostic: %v", err)
			}
			if diag.Class != tc.class || diag.Line != tc.line || diag.Offset != tc.offset || diag.PathHash != domain.HashPath("sub/rollout.jsonl") {
				t.Fatalf("diagnostic %+v", diag)
			}
			if strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "rollout") {
				t.Fatal("diagnostic leaks content or path")
			}
		})
	}
}

// Over-cap inputs are skipped and counted, never silently dropped and never a
// reason to abort the rest of the archive.
func TestOverCapFilesAreSkippedAndCounted(t *testing.T) {
	old := maxRetainedText
	maxRetainedText = 64
	defer func() { maxRetainedText = old }()
	root := t.TempDir()
	write := func(name, raw string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.jsonl", `{"type":"session_meta","payload":{"id":"small"}}
{"type":"event_msg","payload":{"type":"user_message","message":"short"}}
`)
	write("raw-big.jsonl", strings.Repeat("x", 1024*1024+1))
	write("text-big.jsonl", `{"type":"session_meta","payload":{"id":"big"}}
{"type":"event_msg","payload":{"type":"user_message","message":"`+strings.Repeat("y", 100)+`"}}
`)
	skips := &domain.SkipCollector{}
	ctx := domain.WithSkipCollector(context.Background(), skips)
	cfg := config.IngestConfig{Kind: kind, Source: config.IngestSource{Root: root}, Rules: map[string]any{"max_file_size_mb": 1}}
	got, err := (Source{}).Collect(ctx, cfg)
	if err != nil || len(got) != 1 || got[0].SourceID != "small" {
		t.Fatalf("collect: %v %d", err, len(got))
	}
	if n := skips.ByClass()["size_limit"]; n != 2 {
		t.Fatalf("skips = %v", skips.ByClass())
	}
}
