package store

import (
	"path/filepath"
	"testing"
)

func TestCaptureFollowUpOutboxRequiresCaptureRevisionPair(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "capture-protocol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const stamp = "2026-09-03T12:00:00Z"
	for _, suffix := range []string{"a", "b"} {
		if _, err := st.DB.Exec(`
INSERT INTO fragments (
  id, source, source_type, source_id, title, content, content_hash,
  created_at, ingested_at, status, ingest_name
) VALUES (?, 'browser', 'text', ?, ?, '', ?, ?, ?, 'inbox', 'test')`,
			"fragment-"+suffix, "source-"+suffix, "title-"+suffix,
			"content-"+suffix, stamp, stamp); err != nil {
			t.Fatalf("insert fragment %s: %v", suffix, err)
		}
		if _, err := st.DB.Exec(`
INSERT INTO fragment_revisions (
  id, fragment_id, ordinal, material_digest, content_digest, title,
  content, content_format, ordered_media_digest, normalizer_adapter,
  normalizer_version, observed_at, committed_at
) VALUES (?, ?, 1, ?, ?, ?, '', 'markdown', ?, 'test', '1', ?, ?)`,
			"revision-"+suffix, "fragment-"+suffix, "material-"+suffix,
			"content-"+suffix, "title-"+suffix, "media-"+suffix, stamp, stamp); err != nil {
			t.Fatalf("insert revision %s: %v", suffix, err)
		}
		if _, err := st.DB.Exec(`
INSERT INTO capture_attempts (
  id, capture_id, idempotency_key, semantic_digest, fragment_id,
  fragment_revision_id, fragment_outcome, accepted_capture_count,
  principal_id, actor_id, client_kind, client_version, captured_at,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'inserted', 1, 'principal', 'actor',
          'test', '1', ?, ?, ?)`,
			"attempt-"+suffix, "capture-"+suffix, "key-"+suffix,
			"semantic-"+suffix, "fragment-"+suffix, "revision-"+suffix,
			stamp, stamp, stamp); err != nil {
			t.Fatalf("insert attempt %s: %v", suffix, err)
		}
	}

	if _, err := st.DB.Exec(`
INSERT INTO capture_followup_outbox (
  id, capture_id, fragment_revision_id, kind, created_at, updated_at
) VALUES ('matching', 'capture-a', 'revision-a', 'capture_enrichment', ?, ?)`, stamp, stamp); err != nil {
		t.Fatalf("matching capture/revision pair rejected: %v", err)
	}
	if _, err := st.DB.Exec(`DELETE FROM capture_followup_outbox WHERE id = 'matching'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`
INSERT INTO capture_followup_outbox (
  id, capture_id, fragment_revision_id, kind, created_at, updated_at
) VALUES ('mismatched', 'capture-a', 'revision-b', 'capture_enrichment', ?, ?)`, stamp, stamp); err == nil {
		t.Fatal("outbox accepted a revision owned by another capture")
	}
}
