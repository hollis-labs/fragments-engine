package service

import (
	"context"
	"strings"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

// TestReaderChangesEveryNamedWritePathAdvancesTheSequence has one case per
// row of the write-path table in docs/reader-change-feed.md. Each case makes
// one write and then reads both principals' feeds: the fragment must appear
// with the named kind for reader-a, and for reader-b exactly when the change
// is shared. Cases run in order against one database and build on each other.
func TestReaderChangesEveryNamedWritePathAdvancesTheSequence(t *testing.T) {
	st, fragment, asset := readerCommandServiceFixture(t)
	defer st.Close()
	ctx := context.Background()
	readerRepo := repository.NewReaderRepository(st.DB)
	reader := NewReaderService(readerRepo)
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)
	collections := NewReaderCollectionService(repository.NewReaderCollectionRepository(st.DB), readerRepo)
	client := newChangeFeedClient(t, reader, 10)

	const stamp = "2026-11-14T10:00:00Z"
	fragmentID, revisionID := fragment.ID, fragment.Revision.ID
	exec := func(query string, args ...any) func() string {
		return func() string {
			mustExec(t, st.DB, query, args...)
			return fragmentID
		}
	}
	command := func(principal string, item capturecontract.ReaderCommand) func() string {
		return func() string {
			if _, err := commands.Execute(ctx, principal, fragmentID, item); err != nil {
				t.Fatalf("command %s: %v", item.Command, err)
			}
			return fragmentID
		}
	}
	revision := func() int {
		current, err := reader.Get(ctx, ReaderGetRequest{FragmentID: fragmentID, PrincipalID: "reader-a"})
		if err != nil {
			t.Fatal(err)
		}
		return current.Revision
	}
	receipt := func(id, name string) {
		mustExec(t, st.DB, `
INSERT INTO reader_command_receipts (
  command_id, idempotency_key, semantic_digest, principal_id, fragment_id,
  command, state, aggregate_revision, created_at, updated_at
) VALUES (?, ?, ?, 'reader-a', ?, ?, 'pending', 0, ?, ?)`,
			id, "key-"+id, strings.Repeat("a", 64), fragmentID, name, stamp, stamp)
	}

	var secondFragment, collectionID string
	collectionRevision := 0
	cases := []struct {
		path   string
		kind   string
		shared bool
		// act makes the write and returns the fragment expected in the feed.
		act func() string
	}{
		// capture
		{"fragments insert", "capture", true, func() string {
			at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
			built, err := repository.BuildFragment(domain.PipelineFragment{Source: "url", SourceType: "article",
				SourceID: "https://example.test/second", Title: "Second", Content: "Second body", CreatedAt: at}, "reader-test", at)
			if err != nil {
				t.Fatal(err)
			}
			created, _, err := repository.NewFragmentRepository(st.DB).UpsertResolved(ctx, built)
			if err != nil {
				t.Fatal(err)
			}
			secondFragment = created.ID
			return secondFragment
		}},
		{"capture_attempts insert", "capture", true, exec(`
INSERT INTO capture_attempts (
 id, capture_id, idempotency_key, semantic_digest, fragment_id,
 fragment_revision_id, fragment_outcome, accepted_capture_count,
 principal_id, actor_id, client_kind, client_version, captured_at, created_at, updated_at
) VALUES ('second-attempt', 'second-capture', 'second-capture-key', 'second-capture-digest',
 ?, ?, 'skipped', 2, 'local-user', 'local-user', 'test', '1', ?, ?, ?)`, fragmentID, revisionID, stamp, stamp, stamp)},
		{"fragment_source_identities insert", "capture", true, func() string {
			// Take the second fragment's identity away and put it back. While
			// it is missing the fragment is not a Reader item.
			mustExec(t, st.DB, `CREATE TEMP TABLE parked_identity AS SELECT * FROM fragment_source_identities WHERE fragment_id = ?`, secondFragment)
			mustExec(t, st.DB, `DELETE FROM fragment_source_identities WHERE fragment_id = ?`, secondFragment)
			for _, principal := range []string{"reader-a", "reader-b"} {
				if entries := client.sync(principal); len(entries) != 0 {
					t.Fatalf("removing a source identity put %v in the feed", changedFragmentIDs(entries))
				}
			}
			mustExec(t, st.DB, `INSERT INTO fragment_source_identities SELECT * FROM parked_identity`)
			return secondFragment
		}},
		{"fragment_source_identities update", "capture", true,
			exec(`UPDATE fragment_source_identities SET canonical_url = 'https://example.test/moved' WHERE fragment_id = ?`, fragmentID)},
		// revision
		{"fragment_revisions update", "revision", true,
			exec(`UPDATE fragment_revisions SET title = 'Retitled' WHERE id = ?`, revisionID)},
		// tags
		{"fragment_tag_observations insert", "tags", true, exec(`
INSERT INTO fragment_tag_observations (id, fragment_id, value, normalized_value, attribution_source, producer, observed_at, created_at)
VALUES ('tag-shared', ?, 'Shared', 'shared', 'provider', 'test', ?, ?)`, fragmentID, stamp, stamp)},
		{"fragment_tag_observations update", "tags", true,
			exec(`UPDATE fragment_tag_observations SET value = 'SHARED' WHERE id = 'tag-shared'`)},
		{"fragment_tag_observations delete", "tags", true,
			exec(`DELETE FROM fragment_tag_observations WHERE id = 'tag-shared'`)},
		{"fragment_tag_observations insert by a Reader command", "tags", false, func() string {
			return command("reader-a", readerContractCommand("path-add-tag", "add_tag", revision(), func(c *capturecontract.ReaderCommand) { c.Tag = "Mine" }))()
		}},
		{"reader_tag_overlay_events insert", "tags", false, func() string {
			return command("reader-a", readerContractCommand("path-remove-tag", "remove_tag", revision(), func(c *capturecontract.ReaderCommand) { c.Tag = "Mine" }))()
		}},
		{"enrichment_observations insert (tags)", "tags", true, exec(`
INSERT INTO enrichment_observations (
  id, fragment_id, fragment_revision_id, capability, attribution_source, producer,
  producer_version, input_material_digest, value_json, observed_at, asserted_at, created_at
) VALUES ('obs-tags', ?, ?, 'tags', 'model', 'test', '1', 'digest', '["ai"]', ?, ?, ?)`, fragmentID, revisionID, stamp, stamp, stamp)},
		// annotations
		{"capture_annotations insert", "annotations", true, exec(`
INSERT INTO capture_annotations (id, capture_id, fragment_id, kind, text, actor_id, captured_at, created_at)
VALUES ('annotation-path', 'reader-capture', ?, 'capture_note', 'A note', 'local-user', ?, ?)`, fragmentID, stamp, stamp)},
		{"capture_annotations update", "annotations", true,
			exec(`UPDATE capture_annotations SET text = 'An edited note' WHERE id = 'annotation-path'`)},
		{"capture_annotations delete", "annotations", true,
			exec(`DELETE FROM capture_annotations WHERE id = 'annotation-path'`)},
		// note
		{"curated_notes insert", "note", true, exec(`
INSERT INTO curated_notes(fragment_id, body_markdown, revision, actor_id, updated_at) VALUES (?, 'First', 1, 'local-user', ?)`, fragmentID, stamp)},
		{"curated_notes update", "note", true,
			exec(`UPDATE curated_notes SET body_markdown = 'Second', revision = 2 WHERE fragment_id = ?`, fragmentID)},
		{"curated_notes delete", "note", true,
			exec(`DELETE FROM curated_notes WHERE fragment_id = ?`, fragmentID)},
		// reading_state
		{"reading_states insert", "reading_state", false,
			command("reader-a", readerContractCommand("path-read", "mark_read", 0, nil))},
		{"reading_states update", "reading_state", false,
			command("reader-a", readerContractCommand("path-later", "set_later", 1, func(c *capturecontract.ReaderCommand) { c.Value = boolPtr(true) }))},
		{"reading_states delete", "reading_state", false,
			exec(`DELETE FROM reading_states WHERE principal_id = 'reader-a' AND fragment_id = ?`, fragmentID)},
		// media
		{"attachment_refs insert", "media", true, exec(`
INSERT INTO attachment_refs (id, fragment_revision_id, media_asset_id, role, position, created_at)
VALUES ('attachment-path', ?, ?, 'other', 1, ?)`, revisionID, asset.ID, stamp)},
		{"attachment_refs update", "media", true,
			exec(`UPDATE attachment_refs SET caption = 'A caption' WHERE id = 'attachment-path'`)},
		{"attachment_refs delete", "media", true,
			exec(`DELETE FROM attachment_refs WHERE id = 'attachment-path'`)},
		{"media_assets update", "media", true,
			exec(`UPDATE media_assets SET alt_text = 'A described video' WHERE id = ?`, asset.ID)},
		{"asset_variants insert", "media", true, exec(`
INSERT INTO asset_variants (id, media_asset_id, variant_identity, kind, custody, acquisition_state, retention, created_at, updated_at)
VALUES ('variant-path', ?, 'poster', 'poster', 'cache', 'pending', 'cache', ?, ?)`, asset.ID, stamp, stamp)},
		{"asset_variants update (acquisition state)", "media", true,
			exec(`UPDATE asset_variants SET acquisition_state = 'failed', failure_code = 'fetch_failed' WHERE id = 'variant-path'`)},
		{"asset_variants delete", "media", true,
			exec(`DELETE FROM asset_variants WHERE id = 'variant-path'`)},
		{"reader_command_receipts state change (request_asset_acquisition)", "media", false, func() string {
			receipt("path-acquire", "request_asset_acquisition")
			for _, principal := range []string{"reader-a", "reader-b"} {
				if entries := client.sync(principal); len(entries) != 0 {
					t.Fatalf("a pending receipt put %v in the feed", changedFragmentIDs(entries))
				}
			}
			mustExec(t, st.DB, `UPDATE reader_command_receipts SET state = 'succeeded' WHERE command_id = 'path-acquire'`)
			return fragmentID
		}},
		// enrichment
		{"fragment_capability_coverage insert", "enrichment", true, exec(`
INSERT INTO fragment_capability_coverage (fragment_id, fragment_revision_id, capability, state, updated_at)
VALUES (?, ?, 'summary', 'missing', ?)`, fragmentID, revisionID, stamp)},
		{"fragment_capability_coverage update", "enrichment", true,
			exec(`UPDATE fragment_capability_coverage SET state = 'failed', detail = 'no model' WHERE fragment_revision_id = ? AND capability = 'summary'`, revisionID)},
		{"fragment_capability_coverage delete", "enrichment", true,
			exec(`DELETE FROM fragment_capability_coverage WHERE fragment_revision_id = ? AND capability = 'summary'`, revisionID)},
		// triage
		{"inbox insert", "triage", true,
			exec(`INSERT INTO inbox (fragment_id, reason, staged_at) VALUES (?, 'test', ?)`, fragmentID, stamp)},
		{"inbox delete", "triage", true,
			exec(`DELETE FROM inbox WHERE fragment_id = ?`, fragmentID)},
		// routing
		{"route_log insert", "routing", true, exec(`
INSERT INTO route_log (fragment_id, route_id, destination_id, decision, reason, created_at)
VALUES (?, 'route-reader', 'destination-reader', 'routed', 'test', ?)`, fragmentID, stamp)},
		{"reader_command_effects insert", "routing", false, func() string {
			receipt("path-route", "route")
			mustExec(t, st.DB, `
INSERT INTO reader_command_effects (command_id, principal_id, fragment_id, kind, target_id, state, created_at, updated_at)
VALUES ('path-route', 'reader-a', ?, 'route', 'route-reader', 'pending', ?, ?)`, fragmentID, stamp, stamp)
			return fragmentID
		}},
		{"reader_command_effects state change", "routing", false,
			exec(`UPDATE reader_command_effects SET state = 'succeeded' WHERE command_id = 'path-route'`)},
		// collections
		{"reader_collection_items insert", "collections", false, func() string {
			created, err := collections.Create(ctx, "reader-a", capturecontract.ReaderCollectionCreate{Name: "Paths"})
			if err != nil {
				t.Fatal(err)
			}
			collectionID = created.CollectionID
			for _, principal := range []string{"reader-a", "reader-b"} {
				if entries := client.sync(principal); len(entries) != 0 {
					t.Fatalf("creating an empty collection put %v in the feed", changedFragmentIDs(entries))
				}
			}
			added, err := collections.AddItem(ctx, "reader-a", collectionID, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 1, FragmentID: fragmentID})
			if err != nil {
				t.Fatal(err)
			}
			collectionRevision = added.Revision
			return fragmentID
		}},
		{"reader_collections update (name)", "collections", false, func() string {
			renamed, err := collections.Update(ctx, "reader-a", collectionID, capturecontract.ReaderCollectionUpdate{ExpectedRevision: collectionRevision, Name: strPtr("Renamed")})
			if err != nil {
				t.Fatal(err)
			}
			collectionRevision = renamed.Revision
			return fragmentID
		}},
		{"reader_collection_items delete", "collections", false, func() string {
			removed, err := collections.RemoveItem(ctx, "reader-a", collectionID, fragmentID, collectionRevision)
			if err != nil {
				t.Fatal(err)
			}
			collectionRevision = removed.Revision
			return fragmentID
		}},
		// revision, last because it moves the fragment to a new revision.
		{"fragments update of current_revision_id", "revision", true, func() string {
			mustExec(t, st.DB, `
INSERT INTO fragment_revisions (
  id, fragment_id, ordinal, material_digest, content_digest, title, description, content,
  content_format, ordered_media_digest, metadata_json, normalizer_adapter, normalizer_version,
  observed_at, committed_at
) SELECT 'revision-path', fragment_id, ordinal + 1, 'material-path', content_digest, 'Second revision',
  description, content, content_format, ordered_media_digest, metadata_json, normalizer_adapter,
  normalizer_version, ?, ? FROM fragment_revisions WHERE id = ?`, stamp, stamp, revisionID)
			for _, principal := range []string{"reader-a", "reader-b"} {
				if entries := client.sync(principal); len(entries) != 0 {
					t.Fatalf("a revision that is not current put %v in the feed", changedFragmentIDs(entries))
				}
			}
			mustExec(t, st.DB, `UPDATE fragments SET current_revision_id = 'revision-path' WHERE id = ?`, fragmentID)
			return fragmentID
		}},
	}

	client.sync("reader-a")
	client.sync("reader-b")
	for _, test := range cases {
		want := test.act()
		for _, principal := range []string{"reader-a", "reader-b"} {
			entries := client.sync(principal)
			if principal == "reader-b" && !test.shared {
				if len(entries) != 0 {
					t.Errorf("%s: reader-a's private change reached reader-b: %v", test.path, changedFragmentIDs(entries))
				}
				continue
			}
			if len(entries) != 1 || entries[0].FragmentID != want {
				t.Errorf("%s: %s feed returned %v, want only %s", test.path, principal, changedFragmentIDs(entries), want)
				continue
			}
			if !containsString(entries[0].Kinds, test.kind) {
				t.Errorf("%s: %s feed kinds = %v, want %q among them", test.path, principal, entries[0].Kinds, test.kind)
			}
		}
	}

	// Writes that change nothing a Reader item shows leave the feed alone.
	quiet := map[string]func(){
		"rewriting a variant with the same values": func() {
			mustExec(t, st.DB, `UPDATE asset_variants SET updated_at = ?, acquisition_state = acquisition_state WHERE media_asset_id = ?`, "2026-11-14T11:00:00Z", asset.ID)
		},
		"rewriting a source identity with the same values": func() {
			mustExec(t, st.DB, `UPDATE fragment_source_identities SET updated_at = ?, canonical_url = canonical_url WHERE fragment_id = ?`, "2026-11-14T11:00:00Z", fragmentID)
		},
		"reordering a collection": func() {
			for _, id := range []string{fragmentID, secondFragment} {
				added, err := collections.AddItem(ctx, "reader-a", collectionID, capturecontract.ReaderCollectionAddItem{ExpectedRevision: collectionRevision, FragmentID: id})
				if err != nil {
					t.Fatal(err)
				}
				collectionRevision = added.Revision
			}
			client.sync("reader-a")
			if _, err := collections.Reorder(ctx, "reader-a", collectionID, capturecontract.ReaderCollectionOrder{
				ExpectedRevision: collectionRevision, FragmentIDs: []string{secondFragment, fragmentID}}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, act := range quiet {
		act()
		for _, principal := range []string{"reader-a", "reader-b"} {
			if entries := client.sync(principal); len(entries) != 0 {
				t.Errorf("%s put %v in %s's feed", name, changedFragmentIDs(entries), principal)
			}
		}
	}
}
