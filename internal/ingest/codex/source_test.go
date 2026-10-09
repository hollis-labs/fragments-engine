package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
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
		{"conflicting-meta", `{"type":"session_meta","payload":{"id":"one"}}
{"type":"session_meta","payload":{"id":"two"}}`, true},
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
