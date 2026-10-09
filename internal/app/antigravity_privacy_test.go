package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

func TestAppAntigravityPrivateAcceptanceAndRetry(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "archive")
	for _, section := range []string{"brain", "conversations"} {
		if err := os.MkdirAll(filepath.Join(archive, section), 0700); err != nil {
			t.Fatal(err)
		}
	}
	brainPath := filepath.Join(archive, "brain", "notes.md")
	raw := "roadmap password=synthetic-antigravity-secret"
	if err := os.WriteFile(brainPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	pb := func(field int, value []byte) []byte {
		out := binary.AppendUvarint(nil, uint64(field<<3|2))
		out = binary.AppendUvarint(out, uint64(len(value)))
		return append(out, value...)
	}
	dbPath := filepath.Join(archive, "conversations", "synthetic.db")
	source, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`CREATE TABLE steps(idx INTEGER,step_type INTEGER,step_format INTEGER,step_payload BLOB);`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO steps VALUES(0,14,0,?)`, pb(19, pb(2, []byte(raw)))); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Database: config.DatabaseConfig{Path: filepath.Join(root, "shared.db")}, Recall: config.RecallConfig{Backend: "sqlite"}, Transcripts: config.TranscriptConfig{PrivateRoot: filepath.Join(root, "private")}, Ingests: []config.IngestConfig{{Name: "antigravity-archive", Kind: "antigravity", Enabled: true, Source: config.IngestSource{Root: archive}}}}
	instance, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	for attempt := 0; attempt < 2; attempt++ {
		runs, err := instance.Fragments.RunAllIngests(context.Background(), cfg)
		if err != nil || len(runs) != 1 || attempt == 0 && runs[0].Inserted != 2 || attempt == 1 && (runs[0].Skipped != 2 || runs[0].Updated != 0 || runs[0].Inserted != 0) {
			t.Fatalf("private acceptance/retry: %+v %v", runs, err)
		}
	}
	private, err := sql.Open("sqlite", filepath.Join(cfg.Transcripts.PrivateRoot, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	var count int
	if err := private.QueryRow(`SELECT count(*) FROM transcripts WHERE material_json LIKE '%roadmap%' AND material_json NOT LIKE '%synthetic-antigravity-secret%'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("private canonical redaction: %d %v", count, err)
	}
	if err := private.QueryRow(`SELECT count(*) FROM transcript_search WHERE transcript_search MATCH 'roadmap'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("private index: %d %v", count, err)
	}
	if err := private.QueryRow(`SELECT count(*) FROM transcript_search WHERE content LIKE '%synthetic-antigravity-secret%'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("raw secret reached private index")
	}
	if results, err := instance.Fragments.Search(context.Background(), "roadmap", 10); err != nil || len(results) != 0 {
		t.Fatal("transcript reached shared recall")
	}
	if inbox, err := instance.Inbox.List(context.Background(), 10); err != nil || len(inbox) != 0 {
		t.Fatal("transcript reached shared inbox")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source database changed")
	}
	if after, err := os.ReadFile(brainPath); err != nil || string(after) != raw {
		t.Fatal("brain source changed")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatal("source sidecar created")
		}
	}
}

func TestAppAntigravityRefusesBeforeCollectionWithoutPrivateStore(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "archive", "conversations")
	if err := os.MkdirAll(archive, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "invalid.db"), []byte("synthetic malformed database"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Database: config.DatabaseConfig{Path: filepath.Join(root, "shared.db")}, Recall: config.RecallConfig{Backend: "sqlite"}, Ingests: []config.IngestConfig{{Name: "antigravity-archive", Kind: "antigravity", Enabled: true, Source: config.IngestSource{Root: filepath.Dir(archive)}}}}
	instance, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.Fragments.RunAllIngests(context.Background(), cfg); !errors.Is(err, transcript.ErrPrivateStoreRequired) || strings.Contains(err.Error(), "schema") {
		t.Fatalf("private store must be required before source access: %v", err)
	}
}
