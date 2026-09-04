package store

import (
	"path/filepath"
	"testing"
)

func TestReaderCommandMigrationEnforcesProvenanceStateAndJSON(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "reader-commands.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const stamp = "2026-09-03T12:00:00Z"
	if _, err := st.DB.Exec(`
INSERT INTO fragments (
  id, source, source_type, source_id, title, content, content_hash,
  created_at, ingested_at, status, ingest_name
) VALUES ('fragment-1', 'test', 'text', 'source-1', 'title', 'body',
          'hash', ?, ?, 'inbox', 'test')`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`
INSERT INTO reader_tag_overlay_events (
  command_id, principal_id, fragment_id, normalized_value, display_value,
  action, aggregate_revision, created_at
) VALUES ('missing-command', 'local-user', 'fragment-1', 'tag', 'Tag',
          'suppress', 1, ?)`, stamp); err == nil {
		t.Fatal("tag overlay accepted orphaned command provenance")
	}
	if _, err := st.DB.Exec(`
INSERT INTO reader_command_receipts (
  command_id, idempotency_key, semantic_digest, principal_id, fragment_id,
  command, state, aggregate_revision, created_at, updated_at
) VALUES ('command-1', 'key-1', ?, 'local-user', 'fragment-1',
          'remove_tag', 'succeeded', 1, ?, ?)`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`
INSERT INTO reader_tag_overlay_events (
  command_id, principal_id, fragment_id, normalized_value, display_value,
  action, aggregate_revision, created_at
) VALUES ('command-1', 'local-user', 'fragment-1', 'tag', 'Tag',
          'suppress', 1, ?)`, stamp); err != nil {
		t.Fatalf("valid tag overlay rejected: %v", err)
	}
	if _, err := st.DB.Exec(`
INSERT INTO reading_states (
  principal_id, fragment_id, state, position_kind, position_json, revision, updated_at
) VALUES ('bad-state', 'fragment-1', 'finished', 'none', '{}', 0, ?)`, stamp); err == nil {
		t.Fatal("reading state enum constraint was not enforced")
	}
	if _, err := st.DB.Exec(`
INSERT INTO reading_states (
  principal_id, fragment_id, state, position_kind, position_json, revision, updated_at
) VALUES ('bad-json', 'fragment-1', 'unread', 'none', '{', 0, ?)`, stamp); err == nil {
		t.Fatal("reading position JSON constraint was not enforced")
	}
	if _, err := st.DB.Exec(`
INSERT INTO reading_states (
  principal_id, fragment_id, state, position_kind, position_json, revision, updated_at
) VALUES ('bad-kind-json', 'fragment-1', 'unread', 'article', '{"kind":"none"}', 0, ?)`, stamp); err == nil {
		t.Fatal("reading position discriminator constraint was not enforced")
	}
	if _, err := st.DB.Exec(`
INSERT INTO reader_command_aggregates(principal_id, fragment_id, revision, updated_at)
VALUES ('bad-revision', 'fragment-1', -1, ?)`, stamp); err == nil {
		t.Fatal("aggregate revision constraint was not enforced")
	}
}
