package store

import (
	"path/filepath"
	"testing"
)

func TestEnrichmentSchemaEnforcesRevisionCaptureAndObservationOwnership(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "enrichment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedEnrichmentOwners(t, st)
	const stamp = "2026-09-03T12:00:00Z"

	insertObservation := `INSERT INTO enrichment_observations (
  id, fragment_id, fragment_revision_id, capture_id, capability,
  attribution_source, producer, producer_version, input_material_digest,
  value_json, observed_at, asserted_at, created_at
) VALUES (?, ?, ?, ?, ?, 'source', 'test', '1', ?, ?, ?, ?, ?)`
	if _, err := st.DB.Exec(insertObservation, "observation-a", "fragment-a", "revision-a", "capture-a", "title", "material-a", `{"text":"A"}`, stamp, stamp, stamp); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	if _, err := st.DB.Exec(insertObservation, "wrong-fragment", "fragment-b", "revision-a", nil, "title", "material-a", `{"text":"A"}`, stamp, stamp, stamp); err == nil {
		t.Fatal("observation accepted mismatched fragment/revision")
	}
	if _, err := st.DB.Exec(insertObservation, "wrong-capture", "fragment-b", "revision-b", "capture-a", "title", "material-b", `{"text":"B"}`, stamp, stamp, stamp); err == nil {
		t.Fatal("observation accepted capture owned by another revision")
	}
	if _, err := st.DB.Exec(insertObservation, "bad-capability", "fragment-a", "revision-a", nil, "everything", "material-a", `{"text":"A"}`, stamp, stamp, stamp); err == nil {
		t.Fatal("observation accepted unknown capability")
	}

	insertCoverage := `INSERT INTO fragment_capability_coverage (
  fragment_id, fragment_revision_id, capability, state,
  selected_observation_id, updated_at
) VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := st.DB.Exec(insertCoverage, "fragment-a", "revision-a", "title", "provided", "observation-a", stamp); err != nil {
		t.Fatalf("valid coverage rejected: %v", err)
	}
	if _, err := st.DB.Exec(insertCoverage, "fragment-a", "revision-a", "description", "provided", "observation-a", stamp); err == nil {
		t.Fatal("coverage selected an observation for another capability")
	}
	if _, err := st.DB.Exec(insertCoverage, "fragment-a", "revision-a", "summary", "provided", nil, stamp); err == nil {
		t.Fatal("provided coverage accepted without supplying observation")
	}
	if _, err := st.DB.Exec(insertCoverage, "fragment-a", "revision-a", "summary", "complete", nil, stamp); err == nil {
		t.Fatal("coverage accepted blanket complete state")
	}
}

func seedEnrichmentOwners(t *testing.T, st *Store) {
	t.Helper()
	const stamp = "2026-09-03T12:00:00Z"
	for _, suffix := range []string{"a", "b"} {
		if _, err := st.DB.Exec(`
INSERT INTO fragments (
  id, source, source_type, source_id, title, content, content_hash,
  created_at, ingested_at, status, ingest_name
) VALUES (?, 'browser', 'text', ?, ?, '', ?, ?, ?, 'inbox', 'test')`,
			"fragment-"+suffix, "source-"+suffix, "title-"+suffix,
			"content-"+suffix, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.Exec(`
INSERT INTO fragment_source_identities (
  fragment_id, source_registration_id, provider, source_item_key, segment_key,
  source_adapter, source_adapter_version, canonicalizer_adapter,
  canonicalizer_version, created_at, updated_at
) VALUES (?, 'browser', 'web', ?, 'root', 'test', '1', 'test', '1', ?, ?)`,
			"fragment-"+suffix, "source-"+suffix, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.Exec(`
INSERT INTO fragment_revisions (
  id, fragment_id, ordinal, material_digest, content_digest, title,
  content, content_format, ordered_media_digest, normalizer_adapter,
  normalizer_version, observed_at, committed_at
) VALUES (?, ?, 1, ?, ?, ?, '', 'markdown', ?, 'test', '1', ?, ?)`,
			"revision-"+suffix, "fragment-"+suffix, "material-"+suffix,
			"content-"+suffix, "title-"+suffix, "media-"+suffix, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.Exec(`
INSERT INTO capture_attempts (
  id, capture_id, idempotency_key, semantic_digest, fragment_id,
  fragment_revision_id, fragment_outcome, accepted_capture_count,
  principal_id, actor_id, client_kind, client_version, captured_at,
  extraction_adapter, extraction_adapter_version, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'inserted', 1, 'principal', 'actor',
          'test', '1', ?, 'test', '1', ?, ?)`,
			"attempt-"+suffix, "capture-"+suffix, "key-"+suffix,
			"semantic-"+suffix, "fragment-"+suffix, "revision-"+suffix,
			stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}
