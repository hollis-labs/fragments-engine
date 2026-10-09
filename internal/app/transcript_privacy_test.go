package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestAppTranscriptPrivateAcceptanceAndIsolation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source", "projects", "synthetic")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"type":"user","sessionId":"synthetic-session","timestamp":"2026-10-09T00:00:00Z","message":{"content":"roadmap password=synthetic-private-secret"}}`
	path := filepath.Join(source, "session.jsonl")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Database: config.DatabaseConfig{Path: filepath.Join(root, "shared.db")}, Recall: config.RecallConfig{Backend: "sqlite"}, Transcripts: config.TranscriptConfig{PrivateRoot: filepath.Join(root, "private")}, Ingests: []config.IngestConfig{{Name: "synthetic-claude", Kind: "claude_code", Enabled: true, Source: config.IngestSource{Root: filepath.Join(root, "source")}}}}
	instance, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	runs, err := instance.Fragments.RunAllIngests(context.Background(), cfg)
	if err != nil || len(runs) != 1 || runs[0].Inserted != 1 {
		t.Fatalf("private run %+v %v", runs, err)
	}
	results, err := instance.Fragments.Search(context.Background(), "roadmap", 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("shared search exposed private text %d %v", len(results), err)
	}
	inbox, err := instance.Inbox.List(context.Background(), 10)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("shared inbox exposed transcript %d %v", len(inbox), err)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.Transcripts.PrivateRoot, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var content string
	if err := db.QueryRow(`SELECT content FROM transcripts`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "roadmap") || strings.Contains(content, "synthetic-private-secret") {
		t.Fatal("private canonical record was not redacted")
	}
	sourceAfter, err := os.ReadFile(path)
	if err != nil || string(sourceAfter) != raw {
		t.Fatal("source archive was modified")
	}
}

func TestAppClaudeArchiveSubagentsPrivateAndIdempotent(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "archive")
	paths := []string{"project/session.jsonl", "project/session/subagents/child.jsonl"}
	raw := `{"type":"user","sessionId":"synthetic-parent","timestamp":"2026-10-09T00:00:00Z","message":{"content":"roadmap password=synthetic-archive-secret"}}`
	for _, rel := range paths {
		path := filepath.Join(archive, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Database: config.DatabaseConfig{Path: filepath.Join(root, "shared.db")}, Recall: config.RecallConfig{Backend: "sqlite"}, Transcripts: config.TranscriptConfig{PrivateRoot: filepath.Join(root, "private")}, Ingests: []config.IngestConfig{{Name: "archive-claude", Kind: "claude_code", Enabled: true, Source: config.IngestSource{Root: archive}}}}
	instance, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	for attempt := 0; attempt < 2; attempt++ {
		runs, err := instance.Fragments.RunAllIngests(context.Background(), cfg)
		if err != nil || len(runs) != 1 {
			t.Fatalf("run %d: %+v %v", attempt, runs, err)
		}
		if attempt == 0 && runs[0].Inserted != 2 || attempt == 1 && (runs[0].Skipped != 2 || runs[0].Inserted != 0 || runs[0].Updated != 0) {
			t.Fatalf("parent/subagent acceptance %d: %+v", attempt, runs[0])
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.Transcripts.PrivateRoot, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM transcripts WHERE content LIKE '%roadmap%' AND content NOT LIKE '%synthetic-archive-secret%'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("distinct redacted records: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM transcript_search WHERE transcript_search MATCH 'roadmap'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("private index: %d %v", count, err)
	}
	results, err := instance.Fragments.Search(context.Background(), "roadmap", 10)
	if err != nil || len(results) != 0 {
		t.Fatal("shared recall exposed transcripts")
	}
	inbox, err := instance.Inbox.List(context.Background(), 10)
	if err != nil || len(inbox) != 0 {
		t.Fatal("shared inbox exposed transcripts")
	}
	for _, rel := range paths {
		after, err := os.ReadFile(filepath.Join(archive, filepath.FromSlash(rel)))
		if err != nil || string(after) != raw {
			t.Fatal("synthetic archive changed")
		}
	}
}
