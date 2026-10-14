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

func collectWithSkips(t *testing.T, root string) ([]domain.PipelineFragment, *domain.SkipCollector, error) {
	t.Helper()
	skips := &domain.SkipCollector{}
	got, err := (Source{}).Collect(domain.WithSkipCollector(context.Background(), skips), config.IngestConfig{Source: config.IngestSource{Root: root}})
	return got, skips, err
}

func TestEventOnlyFallbackAndSafeMalformedHandling(t *testing.T) {
	const meta = `{"type":"session_meta","payload":{"id":"synthetic-thread"}}`
	for _, tc := range []struct {
		name, raw, skipClass string
		wantFragment         bool
	}{
		{"event-only", meta + "\n" + `{"type":"event_msg","payload":{"type":"user_message","message":"synthetic question"}}` + "\n" +
			`{"type":"event_msg","payload":{"type":"agent_message","message":"synthetic answer"}}`, "", true},
		// No trailing newline and not valid JSON: a half-written final line.
		{"half-written-only-line", `password=synthetic-no-diagnostic`, "incomplete_tail", false},
		// The same text WITH a newline is a complete, invalid record.
		{"invalid-complete-line", "password=synthetic-no-diagnostic\n", "invalid_record", false},
		{"missing-meta", `{"type":"event_msg","payload":{"type":"user_message","message":"synthetic-no-diagnostic"}}` + "\n", "missing_session_meta", false},
		// Valid JSON that is not a record is complete but wrong, not a tail.
		{"complete-json-wrong-shape", meta + "\n" + `{"surprise":true}`, "invalid_record", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "rollout.jsonl")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			got, skips, err := collectWithSkips(t, root)
			if err != nil {
				t.Fatalf("a bad file must never fail the run: %v", err)
			}
			if tc.wantFragment != (len(got) == 1) {
				t.Fatalf("fragments = %d", len(got))
			}
			if tc.wantFragment && (!strings.Contains(got[0].Content, "synthetic question") || !strings.Contains(got[0].Content, "synthetic answer")) {
				t.Fatal("fallback lost")
			}
			by := skips.ByClass()
			if tc.skipClass == "" && len(skips.Skips) != 0 || tc.skipClass != "" && (by[tc.skipClass] != 1 || len(skips.Skips) != 1) {
				t.Fatalf("skips = %v", by)
			}
			for _, sk := range skips.Skips {
				if strings.Contains(sk.Error(), "synthetic") || strings.Contains(sk.Error(), root) || strings.Contains(sk.Error(), "rollout") {
					t.Fatal("skip detail leaks content or path")
				}
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
	if _, _, _, err := collectSegment(context.Background(), path, "rollout.jsonl", info, int64(len(syntheticRollout)-1), 0); err == nil {
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
// Failures are counted per file with a class, the path hash, a line and a byte
// offset, and nothing from the record.
func TestPerFileFailuresCarrySanitizedPositions(t *testing.T) {
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
			_, skips, err := collectWithSkips(t, root)
			if err != nil || len(skips.Skips) != 1 {
				t.Fatalf("%v %v", err, skips.Skips)
			}
			sk := skips.Skips[0]
			if sk.Class != tc.class || sk.Line != tc.line || sk.Offset != tc.offset || sk.PathHash != domain.HashPath("sub/rollout.jsonl") {
				t.Fatalf("skip %+v", sk)
			}
			if strings.Contains(sk.Error(), "synthetic") || strings.Contains(sk.Error(), root) || strings.Contains(sk.Error(), "rollout") {
				t.Fatal("skip detail leaks content or path")
			}
		})
	}
}

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

const goodMeta = `{"timestamp":"2026-10-09T01:00:00Z","type":"session_meta","payload":{"id":"thread-ID"}}`
const goodMsg = `{"timestamp":"2026-10-09T01:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"MSG"}]}}`

func rollout(id string, msgs ...string) string {
	out := strings.Replace(goodMeta, "thread-ID", id, 1) + "\n"
	for _, m := range msgs {
		out += strings.Replace(goodMsg, "MSG", m, 1) + "\n"
	}
	return out
}

func write(t *testing.T, root, rel, raw string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A session still being written when it was copied ends in a half record. The
// complete records are imported, the file is counted as incomplete_tail, and a
// later copy that completes it is picked up because its bytes change.
func TestHalfWrittenTailIsImportedThenCompletedLater(t *testing.T) {
	root := t.TempDir()
	complete := rollout("t1", "first message", "second message")
	half := `{"timestamp":"2026-10-09T01:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"third mess`
	path := write(t, root, "sessions/rollout-a.jsonl", complete+half)
	got, skips, err := collectWithSkips(t, root)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %d", err, len(got))
	}
	if !strings.Contains(got[0].Content, "first message") || !strings.Contains(got[0].Content, "second message") || strings.Contains(got[0].Content, "third") {
		t.Fatalf("content %q", got[0].Content)
	}
	if len(skips.Skips) != 1 || skips.Skips[0].Class != "incomplete_tail" || skips.Skips[0].Line != 4 || skips.Skips[0].Offset != int64(len(complete)) {
		t.Fatalf("tail skip %+v", skips.Skips)
	}
	first := got[0].Content
	// Run again on the unchanged file: same result, same note (it is still half-written).
	got, skips, _ = collectWithSkips(t, root)
	if len(got) != 1 || got[0].Content != first || skips.ByClass()["incomplete_tail"] != 1 {
		t.Fatal("an unchanged half-written file must behave the same on the next run")
	}
	// The writer finishes the record and appends another.
	done := complete + `{"timestamp":"2026-10-09T01:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"third message"}]}}` + "\n" + strings.Replace(goodMsg, "MSG", "fourth message", 1) + "\n"
	if err := os.WriteFile(path, []byte(done), 0600); err != nil {
		t.Fatal(err)
	}
	got, skips, err = collectWithSkips(t, root)
	if err != nil || len(got) != 1 || len(skips.Skips) != 0 {
		t.Fatalf("completed file: %v %d %v", err, len(got), skips.Skips)
	}
	if !strings.Contains(got[0].Content, "third message") || !strings.Contains(got[0].Content, "fourth message") || got[0].Content == first {
		t.Fatal("the completed record was not picked up")
	}
}

// A final line without a newline is still a record if it is whole.
func TestUnterminatedButCompleteFinalRecordIsImported(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.jsonl", strings.TrimSuffix(rollout("t1", "alpha", "omega"), "\n"))
	got, skips, err := collectWithSkips(t, root)
	if err != nil || len(got) != 1 || len(skips.Skips) != 0 || !strings.Contains(got[0].Content, "omega") {
		t.Fatalf("%v %d %v", err, len(got), skips.Skips)
	}
}

// A brand-new file whose first line is still being written has nothing to
// import yet; it is noted, not an error.
func TestNewFileWithOnlyAHalfWrittenFirstLine(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.jsonl", `{"timestamp":"2026-10-09T01:00:00Z","type":"session_me`)
	write(t, root, "b.jsonl", rollout("t2", "still imported"))
	got, skips, err := collectWithSkips(t, root)
	if err != nil || len(got) != 1 || skips.ByClass()["incomplete_tail"] != 1 {
		t.Fatalf("%v %d %v", err, len(got), skips.ByClass())
	}
}

// One bad file never blocks the others: it is counted with its class, hash, line
// and offset, and every other file is still collected.
func TestBadFileDoesNotBlockOtherFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a-good.jsonl", rollout("good1", "one"))
	bad := rollout("bad", "keep") + "this is not json\n" + strings.Replace(goodMsg, "MSG", "after", 1) + "\n"
	write(t, root, "b-bad.jsonl", bad)
	write(t, root, "c-good.jsonl", rollout("good2", "two"))
	got, skips, err := collectWithSkips(t, root)
	if err != nil || len(got) != 2 || got[0].SourceID != "good1" || got[1].SourceID != "good2" {
		t.Fatalf("%v %d", err, len(got))
	}
	if len(skips.Skips) != 1 {
		t.Fatalf("skips %v", skips.Skips)
	}
	sk := skips.Skips[0]
	wantOffset := int64(len(rollout("bad", "keep")))
	if sk.Class != "invalid_record" || sk.Line != 3 || sk.Offset != wantOffset || sk.PathHash != domain.HashPath("b-bad.jsonl") {
		t.Fatalf("skip %+v want offset %d", sk, wantOffset)
	}
}

func TestCompleteFileIsUnchangedAcrossRuns(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.jsonl", rollout("t1", "one", "two"))
	first, s1, err := collectWithSkips(t, root)
	if err != nil || len(first) != 1 || len(s1.Skips) != 0 {
		t.Fatal(err)
	}
	second, s2, _ := collectWithSkips(t, root)
	if len(second) != 1 || second[0].Content != first[0].Content || len(s2.Skips) != 0 {
		t.Fatal("a complete file must collect identically every run")
	}
}

// Files and directories the importer cannot read, or that vanish, are counted
// and skipped; they do not abort the source.
func TestUnreadableFileAndDirectoryAreSkippedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	root := t.TempDir()
	write(t, root, "a-good.jsonl", rollout("good", "ok"))
	locked := write(t, root, "b-locked.jsonl", rollout("locked", "x"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0600)
	write(t, root, "dir/c-hidden.jsonl", rollout("hidden", "y"))
	if err := os.Chmod(filepath.Join(root, "dir"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root, "dir"), 0700)
	got, skips, err := collectWithSkips(t, root)
	if err != nil || len(got) != 1 || got[0].SourceID != "good" {
		t.Fatalf("%v %d", err, len(got))
	}
	by := skips.ByClass()
	if by["unreadable_file"] != 1 || by["unreadable_dir"] != 1 {
		t.Fatalf("skips %v", by)
	}
}
