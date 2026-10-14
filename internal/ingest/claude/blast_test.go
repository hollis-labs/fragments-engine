package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const line = `{"type":"user","uuid":"u1","sessionId":"SID","timestamp":"2026-10-10T10:00:00Z","message":{"content":"synthetic question"}}` + "\n"

func session(t *testing.T, path, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(line, "SID", id)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// One unreadable file, or one directory that cannot be listed (rsync is writing
// there, or permissions changed), must cost only itself: the other sessions are
// still collected and the problem is counted with its class and path hash.
func TestUnreadableFileOrDirectoryDoesNotAbortTheSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	session(t, filepath.Join(proj, "a-good.jsonl"), "good-a")
	locked := filepath.Join(proj, "b-locked.jsonl")
	session(t, locked, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o600)
	session(t, filepath.Join(proj, "c-good.jsonl"), "good-c")
	session(t, filepath.Join(proj, "sess", "subagents", "agent-x.jsonl"), "sub")
	hidden := filepath.Join(proj, "sess", "subagents")
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(hidden, 0o700)

	skips := &domain.SkipCollector{}
	got, err := (Source{}).Collect(domain.WithSkipCollector(context.Background(), skips), config.IngestConfig{Kind: kind, Source: config.IngestSource{Root: root}})
	if err != nil {
		t.Fatalf("one bad entry aborted the source: %v", err)
	}
	ids := map[string]bool{}
	for _, f := range got {
		ids[f.SourceID] = true
	}
	if len(got) != 2 || !ids["good-a"] || !ids["good-c"] {
		t.Fatalf("collected %v", ids)
	}
	by := skips.ByClass()
	if by["unreadable_file"] != 1 || by["unreadable_dir"] != 1 {
		t.Fatalf("skips %v", by)
	}
	for _, sk := range skips.Skips {
		if sk.PathHash == "" || strings.Contains(sk.Error(), root) || strings.Contains(sk.Error(), "locked") {
			t.Fatalf("skip detail %q", sk.Error())
		}
	}
}
