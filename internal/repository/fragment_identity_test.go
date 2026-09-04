package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestStableIdentityReusesAndRevisesAcrossExistingIngestKinds(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fragments.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := NewFragmentRepository(st.DB)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		ingestName string
		candidate  domain.PipelineFragment
	}{
		{"manual", "manual-intake", domain.PipelineFragment{Source: "manual", SourceType: "text", SourceID: "manual-note-1"}},
		{"url", "reading-list", domain.PipelineFragment{Source: "url", SourceType: "article", SourceID: "https://example.com/a", Metadata: map[string]any{"source_url": "https://example.com/a"}}},
		{"filesystem", "project-docs", domain.PipelineFragment{Source: "file:///repo/README.md", SourceType: "document", SourceID: "/repo/README.md"}},
		{"claude", "claude-sessions", domain.PipelineFragment{Source: "claude", SourceType: "chat", SourceID: "session-1"}},
		{"chatgpt", "chatgpt-export", domain.PipelineFragment{Source: "chatgpt", SourceType: "chat", SourceID: "conversation-1"}},
		{"git-commit", "git-history", domain.PipelineFragment{Source: "git://repo/abc", SourceType: "git_change", SourceID: "abc"}},
		{"git-changed-file", "git-history", domain.PipelineFragment{Source: "git://repo/abc/README.md", SourceType: "git_change", SourceID: "abc:README.md", Metadata: map[string]any{"ingest_mode": "changed_doc", "repo_name": "repo", "relative_path": "README.md"}}},
		{"nil", "nil-vault", domain.PipelineFragment{Source: "nil", SourceType: "note", SourceID: "vault:42"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			firstInput := tt.candidate
			firstInput.Title = "Original title"
			firstInput.Content = "original body"
			firstInput.CreatedAt = now
			first, err := BuildFragment(firstInput, tt.ingestName, now)
			if err != nil {
				t.Fatal(err)
			}
			persisted, outcome, err := repo.UpsertResolved(ctx, first)
			if err != nil || outcome != UpsertInserted {
				t.Fatalf("first upsert = %s, %v", outcome, err)
			}
			repeat, err := BuildFragment(firstInput, tt.ingestName, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			repeated, outcome, err := repo.UpsertResolved(ctx, repeat)
			if err != nil || outcome != UpsertSkipped {
				t.Fatalf("repeat upsert = %s, %v", outcome, err)
			}
			if repeated.ID != persisted.ID || repeated.CurrentRevisionID != persisted.CurrentRevisionID {
				t.Fatalf("exact repeat changed stable identity/revision: first=%+v repeat=%+v", persisted, repeated)
			}

			changedInput := firstInput
			changedInput.Content = "changed body"
			changed, err := BuildFragment(changedInput, tt.ingestName, now.Add(2*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			updated, outcome, err := repo.UpsertResolved(ctx, changed)
			if err != nil || outcome != UpsertUpdated {
				t.Fatalf("changed upsert = %s, %v", outcome, err)
			}
			if updated.ID != persisted.ID || updated.CurrentRevisionID == persisted.CurrentRevisionID {
				t.Fatalf("changed material did not revise stable fragment: first=%+v updated=%+v", persisted, updated)
			}
			revisions, err := repo.ListRevisions(ctx, persisted.ID)
			if err != nil || len(revisions) != 2 {
				t.Fatalf("revisions = %d, %v", len(revisions), err)
			}
		})
	}
}

func TestMaterialRevisionDigestIncludesSourceFieldsAndOrderedMediaButNotMetadata(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fragments.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := NewFragmentRepository(st.DB)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	base := domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "https://example.com/material",
		Title: "Title", Description: "Description", Content: "Body", CreatedAt: now,
		Metadata: map[string]any{"request_id": "not-source-material"},
		Attachments: []domain.PipelineAttachment{
			{Kind: "image", Role: "content", Name: "one", ExternalURL: "https://example.com/1.jpg"},
			{Kind: "image", Role: "content", Name: "two", ExternalURL: "https://example.com/2.jpg"},
		},
	}
	whitespaceInput := base
	whitespaceInput.Title = "  Title \r\n"
	whitespaceInput.Description = "\tDescription \r\n"
	whitespaceInput.Content = "  Body \r\n\n"
	whitespaceBuilt, err := BuildFragment(whitespaceInput, "material-test", now)
	if err != nil {
		t.Fatal(err)
	}
	if whitespaceBuilt.Revision.Title != "  Title \n" ||
		whitespaceBuilt.Revision.Description != "\tDescription \n" ||
		whitespaceBuilt.Revision.Content != "  Body \n\n" {
		t.Fatalf("source material whitespace was destructively normalized: %+v", whitespaceBuilt.Revision)
	}
	upsert := func(input domain.PipelineFragment, want UpsertOutcome) domain.Fragment {
		t.Helper()
		built, err := BuildFragment(input, "material-test", now)
		if err != nil {
			t.Fatal(err)
		}
		resolved, outcome, err := repo.UpsertResolved(ctx, built)
		if err != nil || outcome != want {
			t.Fatalf("upsert outcome = %s, %v; want %s", outcome, err, want)
		}
		return resolved
	}

	first := upsert(base, UpsertInserted)
	metadataOnly := base
	metadataOnly.Metadata = map[string]any{"request_id": "changed-but-not-material"}
	metadataRepeat := upsert(metadataOnly, UpsertSkipped)
	if metadataRepeat.CurrentRevisionID != first.CurrentRevisionID {
		t.Fatal("metadata-only update created a source-material revision")
	}
	title := base
	title.Title = "Changed title"
	upsert(title, UpsertUpdated)
	description := title
	description.Description = "Changed description"
	upsert(description, UpsertUpdated)
	reordered := description
	reordered.Attachments = []domain.PipelineAttachment{description.Attachments[1], description.Attachments[0]}
	latest := upsert(reordered, UpsertUpdated)
	revisions, err := repo.ListRevisions(ctx, latest.ID)
	if err != nil || len(revisions) != 4 {
		t.Fatalf("material revisions = %d, %v", len(revisions), err)
	}
	if revisions[0].Title != "Title" || revisions[0].Content != "Body" || revisions[3].Ordinal != 4 {
		t.Fatalf("immutable history was not preserved: %+v", revisions)
	}
}

func TestSourceRegistrationSeparatesOtherwiseIdenticalLegacyTuples(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fragments.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := NewFragmentRepository(st.DB)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	input := domain.PipelineFragment{
		Source: "file:///shared/doc.md", SourceType: "document", SourceID: "/shared/doc.md",
		Title: "Shared", Content: "same bytes", CreatedAt: now,
	}
	a, err := BuildFragment(input, "registration-a", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildFragment(input, "registration-b", now)
	if err != nil {
		t.Fatal(err)
	}
	storedA, outcome, err := repo.UpsertResolved(ctx, a)
	if err != nil || outcome != UpsertInserted {
		t.Fatalf("registration a = %s, %v", outcome, err)
	}
	storedB, outcome, err := repo.UpsertResolved(ctx, b)
	if err != nil || outcome != UpsertInserted {
		t.Fatalf("registration b = %s, %v", outcome, err)
	}
	if storedA.ID == storedB.ID || storedA.ContentHash != storedB.ContentHash {
		t.Fatalf("registrations did not retain distinct stable IDs and authoritative content digest: a=%+v b=%+v", storedA, storedB)
	}
	items, total, err := repo.List(ctx, ListOptions{})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("registered fragments = %d/%d, %v", len(items), total, err)
	}
	results, err := repo.Search(ctx, "same", 10)
	if err != nil || len(results) != 2 {
		t.Fatalf("registered fragment search = %d, %v", len(results), err)
	}
	for _, result := range results {
		if result.Fragment.ContentHash != domain.DigestText("same bytes") {
			t.Fatalf("search exposed salted compatibility hash for %s: %s", result.Fragment.ID, result.Fragment.ContentHash)
		}
		if result.Fragment.CurrentRevisionID == "" || result.Fragment.SourceIdentity.SourceRegistrationID == "" {
			t.Fatalf("search omitted stable identity/revision projection: %+v", result.Fragment)
		}
	}
}

func TestConcurrentExactUpsertCreatesOneFragmentAndRevision(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	otherStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherStore.Close()
	repositories := []*FragmentRepository{
		NewFragmentRepository(st.DB),
		NewFragmentRepository(otherStore.DB),
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	built, err := BuildFragment(domain.PipelineFragment{
		Source: "claude", SourceType: "chat", SourceID: "concurrent-session",
		Title: "Concurrent", Content: "same material", CreatedAt: now,
	}, "concurrent-ingest", now)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 12
	outcomes := make(chan UpsertOutcome, workers)
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(repo *FragmentRepository) {
			defer wg.Done()
			_, outcome, err := repo.UpsertResolved(context.Background(), built)
			outcomes <- outcome
			errors <- err
		}(repositories[i%len(repositories)])
	}
	wg.Wait()
	close(outcomes)
	close(errors)
	inserted, skipped := 0, 0
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent upsert: %v", err)
		}
	}
	for outcome := range outcomes {
		switch outcome {
		case UpsertInserted:
			inserted++
		case UpsertSkipped:
			skipped++
		}
	}
	if inserted != 1 || skipped != workers-1 {
		t.Fatalf("concurrent outcomes inserted=%d skipped=%d", inserted, skipped)
	}
	var fragments, revisions int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM fragments`).Scan(&fragments); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM fragment_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if fragments != 1 || revisions != 1 {
		t.Fatalf("concurrent counts fragments=%d revisions=%d", fragments, revisions)
	}
}

func TestMigrationBackfillsLegacyDuplicatesAsOneCanonicalFragment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	createLegacyIdentityFixture(t, dbPath)

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	repo := NewFragmentRepository(st.DB)

	var physicalRows, identityRows, aliasRows, historyRows int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM fragments`:                                              &physicalRows,
		`SELECT COUNT(*) FROM fragment_source_identities`:                             &identityRows,
		`SELECT COUNT(*) FROM fragment_identity_aliases`:                              &aliasRows,
		`SELECT COUNT(*) FROM legacy_fragment_history WHERE fragment_id = 'legacy-b'`: &historyRows,
	} {
		if err := st.DB.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if physicalRows != 2 || identityRows != 1 || aliasRows != 1 || historyRows != 1 {
		t.Fatalf("unexpected compatibility counts: fragments=%d identities=%d aliases=%d history=%d", physicalRows, identityRows, aliasRows, historyRows)
	}

	items, total, err := repo.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "legacy-b" {
		t.Fatalf("ordinary list exposed legacy alias: total=%d items=%+v", total, items)
	}
	legacyLookup, err := repo.GetByID(ctx, "legacy-a")
	if err != nil {
		t.Fatal(err)
	}
	if legacyLookup.ID != "legacy-b" || legacyLookup.Title != "  Second \t" || legacyLookup.Content != "  second material\n\n" {
		t.Fatalf("legacy alias did not resolve to canonical current fragment: %+v", legacyLookup)
	}
	revisions, err := repo.ListRevisions(ctx, "legacy-a")
	if err != nil || len(revisions) != 2 {
		t.Fatalf("legacy revision history = %d, %v", len(revisions), err)
	}
	if revisions[0].Content != "\tfirst material \n" || revisions[1].Content != "  second material\n\n" {
		t.Fatalf("legacy revisions not preserved in order: %+v", revisions)
	}
	canonicalAttachments, err := NewAttachmentRepository(st.DB).ListByFragment(ctx, legacyLookup.ID)
	if err != nil || len(canonicalAttachments) != 1 || canonicalAttachments[0].Name != "latest.jpg" {
		t.Fatalf("canonical attachment state was lost: %+v, %v", canonicalAttachments, err)
	}
	canonicalEntities, err := NewEntityRepository(st.DB).ListByFragment(ctx, legacyLookup.ID)
	if err != nil || len(canonicalEntities) != 1 || canonicalEntities[0].Value != "latest-state" {
		t.Fatalf("canonical entity state was lost: %+v, %v", canonicalEntities, err)
	}

	input := domain.PipelineFragment{
		Source: "claude", SourceType: "chat", SourceID: "session-legacy",
		Title: "  Second \t", Content: "  second material\n\n",
		CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Attachments: []domain.PipelineAttachment{{
			Kind: "image", Role: "content", Name: "latest.jpg",
			ExternalURL: "https://example.com/latest.jpg", Source: "legacy", SourceItemID: "latest",
		}},
	}
	repeat, err := BuildFragment(input, "claude-test", time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if repeat.ID == "legacy-a" {
		t.Fatal("test fixture must exercise a candidate ID different from the preserved legacy ID")
	}
	resolved, outcome, err := repo.UpsertResolved(ctx, repeat)
	if err != nil || outcome != UpsertSkipped {
		t.Fatalf("legacy exact reingest = %s, %v", outcome, err)
	}
	if resolved.ID != "legacy-b" {
		t.Fatalf("legacy exact reingest returned speculative ID %q", resolved.ID)
	}
	search, err := repo.Search(ctx, "second", 10)
	if err != nil || len(search) != 1 || search[0].Fragment.ID != "legacy-b" {
		t.Fatalf("search exposed duplicate alias: %+v, %v", search, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer reopened.Close()
	var revisionsAfterReopen int
	if err := reopened.DB.QueryRow(`SELECT COUNT(*) FROM fragment_revisions`).Scan(&revisionsAfterReopen); err != nil {
		t.Fatal(err)
	}
	if revisionsAfterReopen != 2 {
		t.Fatalf("backfill was not idempotent; revisions after reopen=%d", revisionsAfterReopen)
	}
}

func createLegacyIdentityFixture(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl := `
CREATE TABLE schema_migrations(name TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE fragments (
 id TEXT PRIMARY KEY, source TEXT NOT NULL, source_type TEXT NOT NULL,
 source_id TEXT NOT NULL, title TEXT NOT NULL, content TEXT NOT NULL,
 content_hash TEXT NOT NULL, created_at TEXT NOT NULL, ingested_at TEXT NOT NULL,
 status TEXT NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}', ingest_name TEXT NOT NULL,
 canonical_path TEXT NOT NULL DEFAULT '', summary_text TEXT NOT NULL DEFAULT '',
 indexed_at TEXT NOT NULL DEFAULT '', UNIQUE(source, source_id, content_hash)
);
CREATE VIRTUAL TABLE fragments_fts USING fts5(fragment_id UNINDEXED, title, content, tokenize = 'porter unicode61');
CREATE TRIGGER fragments_ai AFTER INSERT ON fragments BEGIN
 INSERT INTO fragments_fts(fragment_id, title, content) VALUES (new.id, new.title, new.content);
END;
CREATE TRIGGER fragments_au AFTER UPDATE ON fragments BEGIN
 DELETE FROM fragments_fts WHERE fragment_id = old.id;
 INSERT INTO fragments_fts(fragment_id, title, content) VALUES (new.id, new.title, new.content);
END;
CREATE TABLE attachments (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', mime_type TEXT NOT NULL DEFAULT '',
 source_path TEXT NOT NULL DEFAULT '', external_url TEXT NOT NULL DEFAULT '', storage_path TEXT NOT NULL DEFAULT '',
 size_bytes INTEGER NOT NULL DEFAULT 0, metadata_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL,
 UNIQUE(kind, source_path, external_url, name)
);
CREATE TABLE fragment_attachments (
 fragment_id TEXT NOT NULL, attachment_id TEXT NOT NULL, role TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '',
 source_item_id TEXT NOT NULL DEFAULT '', metadata_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL,
 storage_path TEXT NOT NULL DEFAULT '', preview_storage_path TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(fragment_id, attachment_id, role, source, source_item_id),
 FOREIGN KEY(fragment_id) REFERENCES fragments(id), FOREIGN KEY(attachment_id) REFERENCES attachments(id)
);
CREATE TABLE legacy_fragment_history (
 fragment_id TEXT NOT NULL, detail TEXT NOT NULL, FOREIGN KEY(fragment_id) REFERENCES fragments(id)
);
CREATE TABLE entities (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, value TEXT NOT NULL, created_at TEXT NOT NULL,
 UNIQUE(kind, value)
);
CREATE TABLE fragment_entities (
 fragment_id TEXT NOT NULL, entity_id TEXT NOT NULL, source TEXT NOT NULL DEFAULT '',
 confidence REAL NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
 PRIMARY KEY(fragment_id, entity_id, source),
 FOREIGN KEY(fragment_id) REFERENCES fragments(id), FOREIGN KEY(entity_id) REFERENCES entities(id)
);`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"001_init.sql", "002_recall.sql", "003_entities.sql", "004_route_entities.sql",
		"005_queue_events.sql", "006_attachments.sql", "007_fragment_attachment_storage.sql",
		"008_fragment_attachment_preview.sql", "009_ingest_run_status.sql", "010_ingest_schedules.sql",
	} {
		if _, err := db.Exec(`INSERT INTO schema_migrations(name, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, name); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct {
		id, title, content, hash, created, ingested string
	}{
		{"legacy-a", "First", "\tfirst material \n", domain.DigestText("\tfirst material \n"), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"},
		{"legacy-b", "  Second \t", "  second material\n\n", domain.DigestText("  second material\n\n"), "2026-01-02T00:00:00Z", "2026-01-02T00:00:00Z"},
	}
	for _, row := range rows {
		if _, err := db.Exec(`
INSERT INTO fragments(id, source, source_type, source_id, title, content, content_hash,
 created_at, ingested_at, status, metadata_json, ingest_name, canonical_path)
VALUES (?, 'claude', 'chat', 'session-legacy', ?, ?, ?, ?, ?, 'inbox', '{}', 'claude-test', '')`,
			row.id, row.title, row.content, row.hash, row.created, row.ingested); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO legacy_fragment_history(fragment_id, detail) VALUES ('legacy-b', 'preserved')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO attachments(id, kind, name, external_url, created_at)
VALUES ('latest-attachment', 'image', 'latest.jpg', 'https://example.com/latest.jpg', '2026-01-02T00:00:00Z');
INSERT INTO fragment_attachments(fragment_id, attachment_id, role, source, source_item_id, created_at)
VALUES ('legacy-b', 'latest-attachment', 'content', 'legacy', 'latest', '2026-01-02T00:00:00Z');
INSERT INTO entities(id, kind, value, created_at)
VALUES ('latest-entity', 'tag', 'latest-state', '2026-01-02T00:00:00Z');
INSERT INTO fragment_entities(fragment_id, entity_id, source, confidence, created_at)
VALUES ('legacy-b', 'latest-entity', 'legacy', 1.0, '2026-01-02T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
}
