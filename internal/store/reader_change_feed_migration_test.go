package store

import (
	"path/filepath"
	"testing"
)

// A database that already holds fragments when 023 is applied must come out
// with every fragment in the change feed, oldest first, and with the triggers
// continuing the sequence from there.
func TestReaderChangeFeedMigrationSeedsExistingFragments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reader-change-feed.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.DB.Exec(query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	insertFragment := func(id, ingestedAt string) {
		exec(`INSERT INTO fragments (id, source, source_type, source_id, title, content, content_hash,
  created_at, ingested_at, status, ingest_name) VALUES (?, 'test', 'text', ?, 'title', 'body', ?, ?, ?, 'inbox', 'test')`,
			id, "source-"+id, "hash-"+id, ingestedAt, ingestedAt)
	}
	// Inserted newest first, so the seed order has to come from ingested_at.
	insertFragment("fragment-c", "2026-03-01T00:00:00Z")
	insertFragment("fragment-a", "2026-01-01T00:00:00Z")
	insertFragment("fragment-b", "2026-02-01T00:00:00Z")

	// Put the database back to how it was before the feed existed.
	rows, err := st.DB.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'reader_changes_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, name)
	}
	rows.Close()
	if len(triggers) == 0 {
		t.Fatal("the migration created no change triggers")
	}
	for _, name := range triggers {
		exec(`DROP TRIGGER ` + name)
	}
	exec(`DROP TABLE reader_changes`)
	exec(`DROP TABLE reader_change_sequence`)
	exec(`DELETE FROM schema_migrations WHERE name = '023_reader_change_feed.sql'`)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(dbPath)
	if err != nil {
		t.Fatalf("apply 023 to a populated database: %v", err)
	}
	defer st.Close()
	seeded := map[string]int64{}
	rows, err = st.DB.Query(`SELECT fragment_id, change_seq FROM reader_changes WHERE principal_id = '' AND kind = 'capture'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var seq int64
		if err := rows.Scan(&id, &seq); err != nil {
			t.Fatal(err)
		}
		seeded[id] = seq
	}
	rows.Close()
	if len(seeded) != 3 || seeded["fragment-a"] != 1 || seeded["fragment-b"] != 2 || seeded["fragment-c"] != 3 {
		t.Fatalf("seeded changes = %v, want a, b, c at 1, 2, 3", seeded)
	}

	insertFragment("fragment-d", "2026-04-01T00:00:00Z")
	var next int64
	if err := st.DB.QueryRow(`SELECT change_seq FROM reader_changes WHERE fragment_id = 'fragment-d'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != 4 {
		t.Fatalf("first change after the migration got sequence %d, want 4", next)
	}
}
