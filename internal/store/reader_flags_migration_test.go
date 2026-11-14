package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// The 021 rebuild of reader_command_receipts must keep the rows of the tables
// that reference it, and must accept the new command names afterwards.
func TestReaderFlagsMigrationRebuildKeepsDependentRows(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "reader-flags.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const stamp = "2026-09-03T12:00:00Z"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.DB.Exec(query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`INSERT INTO fragments (id, source, source_type, source_id, title, content, content_hash,
  created_at, ingested_at, status, ingest_name) VALUES ('fragment-1', 'test', 'text', 'source-1', 'title', 'body',
  'hash', ?, ?, 'inbox', 'test')`, stamp, stamp)
	for i, command := range []string{"remove_tag", "route"} {
		id := []string{"command-tag", "command-route"}[i]
		exec(`INSERT INTO reader_command_receipts (command_id, idempotency_key, semantic_digest, principal_id,
  fragment_id, command, state, aggregate_revision, created_at, updated_at)
  VALUES (?, ?, ?, 'local-user', 'fragment-1', ?, 'succeeded', 1, ?, ?)`, id, "key-"+id, digest, command, stamp, stamp)
	}
	exec(`INSERT INTO reader_tag_overlay_events (command_id, principal_id, fragment_id, normalized_value,
  display_value, action, aggregate_revision, created_at) VALUES ('command-tag', 'local-user', 'fragment-1', 'tag', 'Tag', 'suppress', 1, ?)`, stamp)
	exec(`INSERT INTO reader_command_effects (command_id, principal_id, fragment_id, kind, target_id, state, created_at, updated_at)
  VALUES ('command-route', 'local-user', 'fragment-1', 'route', 'route-1', 'succeeded', ?, ?)`, stamp, stamp)

	// Replay the rebuild half of the migration over the populated database.
	raw, err := migrationsFS.ReadFile("migrations/021_reader_flags.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	text = text[strings.Index(text, "CREATE TEMP TABLE"):]
	tx, err := st.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(text) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%v\n%s", err, stmt)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for table, want := range map[string]int{"reader_command_receipts": 2, "reader_tag_overlay_events": 1, "reader_command_effects": 1} {
		var got int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s rows = %d (err %v), want %d", table, got, err, want)
		}
	}
	for _, command := range []string{"set_later", "set_archived", "set_starred"} {
		exec(`INSERT INTO reader_command_receipts (command_id, idempotency_key, semantic_digest, principal_id,
  fragment_id, command, state, aggregate_revision, created_at, updated_at)
  VALUES (?, ?, ?, 'local-user', 'fragment-1', ?, 'succeeded', 1, ?, ?)`, "c-"+command, "k-"+command, digest, command, stamp, stamp)
	}
	if _, err := st.DB.Exec(`INSERT INTO reader_command_receipts (command_id, idempotency_key, semantic_digest, principal_id,
  fragment_id, command, state, aggregate_revision, created_at, updated_at)
  VALUES ('bad', 'k-bad', ?, 'local-user', 'fragment-1', 'set_everything', 'succeeded', 1, ?, ?)`, digest, stamp, stamp); err == nil {
		t.Fatal("unknown command name accepted")
	}
	// The children still enforce their foreign key to the rebuilt table.
	if _, err := st.DB.Exec(`INSERT INTO reader_command_effects (command_id, principal_id, fragment_id, kind, target_id, state, created_at, updated_at)
  VALUES ('missing', 'local-user', 'fragment-1', 'route', 'r', 'pending', ?, ?)`, stamp, stamp); err == nil {
		t.Fatal("effect accepted without a receipt")
	}
}
