package nilvault

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// fixtureSchema is a trimmed copy of Nil's store/schema.sql (the tables this
// source actually reads: todos, taxonomy tables, and refs). Kept minimal --
// no FTS5 table, no triggers, no kinds registry -- since only read access to
// these tables is under test here.
const fixtureSchema = `
CREATE TABLE todos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL,
  priority TEXT DEFAULT NULL,
  completed INTEGER NOT NULL DEFAULT 0,
  archived INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  due_at TEXT DEFAULT NULL,
  notes_doc TEXT DEFAULT '',
  section TEXT DEFAULT 'anytime',
  pinned INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL DEFAULT 'todo',
  api_source TEXT DEFAULT NULL
);
CREATE TABLE projects (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE);
CREATE TABLE contexts (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE);
CREATE TABLE tags (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE);
CREATE TABLE todo_projects (
  todo_id INTEGER, project_id INTEGER,
  PRIMARY KEY (todo_id, project_id)
);
CREATE TABLE todo_contexts (
  todo_id INTEGER, context_id INTEGER,
  PRIMARY KEY (todo_id, context_id)
);
CREATE TABLE todo_tags (
  todo_id INTEGER, tag_id INTEGER,
  PRIMARY KEY (todo_id, tag_id)
);
CREATE TABLE refs (
  source_id INTEGER NOT NULL,
  target_id INTEGER NOT NULL,
  PRIMARY KEY (source_id, target_id)
);
`

// scratchDoc / noteDoc / todoDoc are notes_doc PM-JSON fixtures. noteDoc
// deliberately exercises heading, paragraph, wikilink, and bulletList
// (TipTap's standard node types) in one document.
const scratchDoc = `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Quick scratch thought."}]}]}`

const noteDoc = `{"type":"doc","content":[` +
	`{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Project Note"}]},` +
	`{"type":"paragraph","content":[{"type":"text","text":"See "},{"type":"wikilink","attrs":{"id":1,"refType":"note","label":"Scratch pad"}},{"type":"text","text":" for details."}]},` +
	`{"type":"bulletList","content":[` +
	`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"First point"}]}]},` +
	`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Second point"}]}]}` +
	`]}]}`

const todoDoc = `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Milk, eggs, bread."}]}]}`

// buildFixtureVault creates a todo.db at dir/todo.db with Nil's schema
// (trimmed) and three items: a scratch (id 1), a note (id 2, wikilinking
// and refs-linking to item 1, tagged with taxonomy), and a todo (id 3, which
// must never appear in Collect's output). Returns the open writer
// connection so callers can simulate Collect running against a live,
// concurrently-open WAL database, matching the real risk this source must
// tolerate (Nil's own GUI/CLI may have the vault open at the same time).
func buildFixtureVault(t *testing.T, dir string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir vault dir: %v", err)
	}
	dbPath := filepath.Join(dir, "todo.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	exec(`INSERT INTO todos (id, title, notes_doc, kind, created_at, updated_at, pinned, completed, archived)
		VALUES (1, 'Scratch pad', ?, 'scratch', '2026-08-01 10:00:00', '2026-08-01 10:00:00', 0, 0, 0)`, scratchDoc)
	exec(`INSERT INTO todos (id, title, notes_doc, kind, created_at, updated_at, pinned, completed, archived, priority, section, due_at)
		VALUES (2, 'Project Note', ?, 'note', '2026-08-02 11:00:00', '2026-08-02 11:30:00', 1, 0, 0, 'A', 'today', '2026-08-10 00:00:00')`, noteDoc)
	exec(`INSERT INTO todos (id, title, notes_doc, kind, created_at, updated_at)
		VALUES (3, 'Buy groceries', ?, 'todo', '2026-08-03 09:00:00', '2026-08-03 09:00:00')`, todoDoc)

	exec(`INSERT INTO projects (id, name) VALUES (1, 'Fragments Engine')`)
	exec(`INSERT INTO contexts (id, name) VALUES (1, '@computer')`)
	exec(`INSERT INTO tags (id, name) VALUES (1, 'important')`)
	exec(`INSERT INTO todo_projects (todo_id, project_id) VALUES (2, 1)`)
	exec(`INSERT INTO todo_contexts (todo_id, context_id) VALUES (2, 1)`)
	exec(`INSERT INTO todo_tags (todo_id, tag_id) VALUES (2, 1)`)
	exec(`INSERT INTO refs (source_id, target_id) VALUES (2, 1)`)

	return db
}

func writeNilConfig(t *testing.T, configDir string, vaults []nilVault) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := nilConfig{
		ActiveVaultID: vaults[0].ID,
		InboxPath:     filepath.Join(configDir, "inbox"),
		Vaults:        vaults,
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal nil config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), raw, 0o644); err != nil {
		t.Fatalf("write nil config: %v", err)
	}
}

func TestSourceCollect_NilVault(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, "vaults", "personal")
	writerDB := buildFixtureVault(t, vaultDir)
	// Simulate Nil's own GUI/CLI holding the vault open (WAL, concurrent
	// writer) for the duration of the read -- Collect must tolerate this.
	t.Cleanup(func() { _ = writerDB.Close() })

	missingVaultDir := filepath.Join(root, "vaults", "does-not-exist")

	configDir := filepath.Join(root, "config")
	writeNilConfig(t, configDir, []nilVault{
		{ID: "vault-personal", Name: "Personal", Path: vaultDir, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "vault-missing", Name: "Missing", Path: missingVaultDir, CreatedAt: "2026-01-01T00:00:00Z"},
	})

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})

	fragments, err := Source{}.Collect(context.Background(), config.IngestConfig{
		Name:    "nil-test",
		Kind:    kind,
		Enabled: true,
		Source:  config.IngestSource{Root: configDir},
		Routing: config.IngestRouting{Namespace: "fragments/nil"},
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Missing") {
		t.Fatalf("expected missing-vault skip to be logged, got log output: %q", logOutput)
	}

	if len(fragments) != 2 {
		t.Fatalf("expected 2 fragments (todo excluded), got %d: %+v", len(fragments), fragments)
	}

	byType := make(map[string]domain.PipelineFragment, len(fragments))
	for _, f := range fragments {
		if f.SourceType == "todo" {
			t.Fatalf("todo item leaked into results: %+v", f)
		}
		byType[f.SourceType] = f
	}

	scratch, ok := byType["scratch"]
	if !ok {
		t.Fatalf("expected a scratch fragment, got %+v", fragments)
	}
	if scratch.Source != "nil" {
		t.Fatalf("expected source %q, got %q", "nil", scratch.Source)
	}
	if scratch.SourceID != "vault-personal:1" {
		t.Fatalf("unexpected scratch source id: %s", scratch.SourceID)
	}
	if scratch.Content != "Quick scratch thought." {
		t.Fatalf("unexpected scratch content: %q", scratch.Content)
	}
	if scratch.CanonicalPath != "fragments/nil/Personal/scratch/1" {
		t.Fatalf("unexpected scratch canonical path: %s", scratch.CanonicalPath)
	}
	wantCreated := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if !scratch.CreatedAt.Equal(wantCreated) {
		t.Fatalf("unexpected scratch created_at: %v", scratch.CreatedAt)
	}

	note, ok := byType["note"]
	if !ok {
		t.Fatalf("expected a note fragment, got %+v", fragments)
	}
	if note.SourceID != "vault-personal:2" {
		t.Fatalf("unexpected note source id: %s", note.SourceID)
	}
	wantContent := "## Project Note\n\nSee Scratch pad for details.\n\n- First point\n- Second point"
	if note.Content != wantContent {
		t.Fatalf("unexpected note content:\n got: %q\nwant: %q", note.Content, wantContent)
	}
	if note.CanonicalPath != "fragments/nil/Personal/note/2" {
		t.Fatalf("unexpected note canonical path: %s", note.CanonicalPath)
	}
	if note.Metadata["vault_id"] != "vault-personal" || note.Metadata["vault_name"] != "Personal" {
		t.Fatalf("unexpected vault metadata: %+v", note.Metadata)
	}
	if note.Metadata["nil_item_id"] != int64(2) {
		t.Fatalf("unexpected nil_item_id: %+v (%T)", note.Metadata["nil_item_id"], note.Metadata["nil_item_id"])
	}
	if pinned, ok := note.Metadata["pinned"].(bool); !ok || !pinned {
		t.Fatalf("expected pinned=true, got %+v", note.Metadata["pinned"])
	}
	if completed, ok := note.Metadata["completed"].(bool); !ok || completed {
		t.Fatalf("expected completed=false, got %+v", note.Metadata["completed"])
	}
	if note.Metadata["priority"] != "A" {
		t.Fatalf("expected priority A, got %+v", note.Metadata["priority"])
	}
	if note.Metadata["due_at"] != "2026-08-10 00:00:00" {
		t.Fatalf("unexpected due_at: %+v", note.Metadata["due_at"])
	}
	projects, _ := note.Metadata["projects"].([]string)
	if len(projects) != 1 || projects[0] != "Fragments Engine" {
		t.Fatalf("unexpected projects: %+v", note.Metadata["projects"])
	}
	contexts, _ := note.Metadata["contexts"].([]string)
	if len(contexts) != 1 || contexts[0] != "@computer" {
		t.Fatalf("unexpected contexts: %+v", note.Metadata["contexts"])
	}
	tags, _ := note.Metadata["tags"].([]string)
	if len(tags) != 1 || tags[0] != "important" {
		t.Fatalf("unexpected tags: %+v", note.Metadata["tags"])
	}
	linked, _ := note.Metadata["nil_linked_item_ids"].([]string)
	if len(linked) != 1 || linked[0] != "1" {
		t.Fatalf("unexpected nil_linked_item_ids (want wikilink+refs to dedup to [\"1\"]): %+v", note.Metadata["nil_linked_item_ids"])
	}
}

// TestSourceCollect_NilVault_Dedup exercises the real dedup mechanics
// (repository.BuildFragment + FragmentRepository.Upsert, keyed on source +
// source_id + content_hash) rather than re-testing Collect's own output:
// per CW-20260816-0053, nil_vault does a full rescan every run with no
// incremental/since-last-run logic, so correctness relies entirely on
// Upsert's idempotency for an unchanged second run.
func TestSourceCollect_NilVault_Dedup(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, "vaults", "personal")
	writerDB := buildFixtureVault(t, vaultDir)
	t.Cleanup(func() { _ = writerDB.Close() })

	configDir := filepath.Join(root, "config")
	writeNilConfig(t, configDir, []nilVault{
		{ID: "vault-personal", Name: "Personal", Path: vaultDir, CreatedAt: "2026-01-01T00:00:00Z"},
	})

	dbPath := filepath.Join(t.TempDir(), "fragments-engine.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open fragments-engine store: %v", err)
	}
	defer st.Close()
	repo := repository.NewFragmentRepository(st.DB)

	ingestCfg := config.IngestConfig{
		Name:    "nil-test",
		Kind:    kind,
		Enabled: true,
		Source:  config.IngestSource{Root: configDir},
		Routing: config.IngestRouting{Namespace: "fragments/nil"},
	}

	runOnce := func() []repository.UpsertOutcome {
		fragments, err := Source{}.Collect(context.Background(), ingestCfg)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		sort.Slice(fragments, func(i, j int) bool { return fragments[i].SourceID < fragments[j].SourceID })
		now := time.Now().UTC()
		outcomes := make([]repository.UpsertOutcome, 0, len(fragments))
		for _, candidate := range fragments {
			fragment, err := repository.BuildFragment(candidate, ingestCfg.Name, now)
			if err != nil {
				t.Fatalf("build fragment: %v", err)
			}
			outcome, err := repo.Upsert(context.Background(), fragment)
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			outcomes = append(outcomes, outcome)
		}
		return outcomes
	}

	first := runOnce()
	if len(first) != 2 {
		t.Fatalf("expected 2 items on first run, got %d", len(first))
	}
	for i, outcome := range first {
		if outcome != repository.UpsertInserted {
			t.Fatalf("expected UpsertInserted on first run item %d, got %s", i, outcome)
		}
	}

	second := runOnce()
	if len(second) != 2 {
		t.Fatalf("expected 2 items on second run, got %d", len(second))
	}
	for i, outcome := range second {
		if outcome != repository.UpsertSkipped {
			t.Fatalf("expected UpsertSkipped on second identical run item %d, got %s", i, outcome)
		}
	}
}
