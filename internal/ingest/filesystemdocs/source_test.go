package filesystemdocs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestSourceCollect_FilesystemDocs(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "sample-repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo marker: %v", err)
	}
	docsDir := filepath.Join(repoRoot, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "guide.md"), []byte("---\ntitle: System Guide\nowner: platform\n---\n# Guide\n\nDeterministic docs ingest.\n"), 0o600); err != nil {
		t.Fatalf("write markdown doc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# Readme\n\nTop level readme.\n"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "node_modules", "skip.md"), []byte("# Skip"), 0o600); err != nil {
		t.Fatalf("write skipped file: %v", err)
	}

	fragments, err := Source{}.Collect(context.Background(), config.IngestConfig{
		Name:    "docs-test",
		Kind:    kind,
		Enabled: true,
		Source:  config.IngestSource{Root: root},
		Routing: config.IngestRouting{Namespace: "fragments/repos/docs"},
		Rules: map[string]any{
			"include":           []string{"**/*.md"},
			"exclude":           []string{"**/node_modules/**"},
			"max_file_size_mb":  1,
			"project_from_path": true,
		},
	})
	if err != nil {
		t.Fatalf("collect filesystem docs: %v", err)
	}
	if len(fragments) != 2 {
		t.Fatalf("expected 2 fragments, got %d", len(fragments))
	}
	doc := fragments[1]
	if doc.Title != "System Guide" {
		t.Fatalf("unexpected title: %s", doc.Title)
	}
	if doc.SourceType != kind {
		t.Fatalf("unexpected source type: %s", doc.SourceType)
	}
	if doc.Metadata["repo_name"] != "sample-repo" {
		t.Fatalf("expected repo metadata, got %+v", doc.Metadata)
	}
	if doc.Metadata["relative_path"] != "docs/guide.md" {
		t.Fatalf("unexpected relative_path: %+v", doc.Metadata)
	}
	if _, ok := doc.Metadata["frontmatter"]; !ok {
		t.Fatalf("expected frontmatter metadata, got %+v", doc.Metadata)
	}
	if doc.CanonicalPath != "fragments/repos/docs/sample-repo/docs/guide.md" {
		t.Fatalf("unexpected canonical path: %s", doc.CanonicalPath)
	}
}
