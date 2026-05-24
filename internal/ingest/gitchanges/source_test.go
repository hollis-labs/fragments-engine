package gitchanges

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestSourceCollect_GitChanges(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "sample-repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo root: %v", err)
	}
	runGit(t, repoRoot, "init", "-b", "main")
	runGit(t, repoRoot, "config", "user.name", "Fragments Test")
	runGit(t, repoRoot, "config", "user.email", "fragments@example.com")

	writeFile(t, filepath.Join(repoRoot, "README.md"), "# First Doc\n\nInitial content.\n")
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "Initial docs")

	writeFile(t, filepath.Join(repoRoot, "README.md"), "# First Doc\n\nUpdated content for recall.\n")
	writeFile(t, filepath.Join(repoRoot, "internal", "service.go"), "package internal\n\nconst Version = \"1\"\n")
	runGit(t, repoRoot, "add", "README.md", "internal/service.go")
	runGit(t, repoRoot, "commit", "-m", "Update docs", "-m", "Adds deterministic ingest coverage.")

	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	fragments, err := Source{now: func() time.Time { return now }}.Collect(context.Background(), config.IngestConfig{
		Name:    "git-test",
		Kind:    kind,
		Enabled: true,
		Source:  config.IngestSource{Root: root},
		Routing: config.IngestRouting{Namespace: "fragments/repos/git"},
		Rules: map[string]any{
			"repos":                   []string{"sample-repo"},
			"branch":                  "main",
			"max_commits":             10,
			"include":                 []string{"**/*.md", "**/*.go"},
			"emit_doc_file_fragments": true,
		},
	})
	if err != nil {
		t.Fatalf("collect git changes: %v", err)
	}
	if len(fragments) != 5 {
		t.Fatalf("expected 5 fragments, got %d", len(fragments))
	}
	var foundCommit bool
	var foundChangedDoc bool
	for _, fragment := range fragments {
		if fragment.SourceID != "" && !strings.Contains(fragment.SourceID, ":") && strings.Contains(fragment.Content, "Adds deterministic ingest coverage.") {
			foundCommit = true
			if fragment.Metadata["git_branch"] != "main" {
				t.Fatalf("expected git branch metadata, got %+v", fragment.Metadata)
			}
			if !strings.Contains(fragment.Content, "README.md") || !strings.Contains(fragment.Content, "internal/service.go") {
				t.Fatalf("expected changed files in commit content, got %q", fragment.Content)
			}
		}
		if fragment.Metadata["relative_path"] == "README.md" && strings.Contains(fragment.Content, "Updated content for recall.") {
			foundChangedDoc = true
		}
	}
	if !foundCommit {
		t.Fatalf("expected updated commit fragment in %+v", fragments)
	}
	if !foundChangedDoc {
		t.Fatalf("expected changed README fragment in %+v", fragments)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-05-24T10:00:00Z",
		"GIT_COMMITTER_DATE=2026-05-24T10:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
