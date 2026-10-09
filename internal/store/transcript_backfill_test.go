package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

func TestTranscriptLegacyBackfillRefusesBeforeCopy(t *testing.T) {
	for _, tc := range []struct{ source, kind string }{{"claude", "chat"}, {"chatgpt", "chat"}, {"synthetic-other", "transcript"}} {
		t.Run(tc.source, func(t *testing.T) {
			st, err := Open(filepath.Join(t.TempDir(), "shared.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			_, err = st.DB.Exec(`DELETE FROM fragment_identity_backfill_state;
INSERT INTO fragments(id,source,source_type,source_id,title,content,content_hash,created_at,ingested_at,status,ingest_name)
VALUES('synthetic-legacy',?,?,'session','title','password=synthetic-legacy-secret','hash','2026-10-09T00:00:00Z','2026-10-09T00:00:00Z','inbox','source')`, tc.source, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.backfillFragmentIdentity(context.Background()); !errors.Is(err, transcript.ErrHistoricalDispositionRequired) {
				t.Fatalf("legacy copy accepted: %v", err)
			}
			for _, table := range []string{"fragment_revisions", "fragment_source_identities", "fragment_identity_backfill_state"} {
				var count int
				if err := st.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("backfill persisted %s %d %v", table, count, err)
				}
			}
		})
	}
}

func TestTranscriptLegacyMediaBackfillRefusesBeforeCopy(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.DB.Exec(`DELETE FROM media_backfill_state;
INSERT INTO fragments(id,source,source_type,source_id,title,content,content_hash,created_at,ingested_at,status,ingest_name,current_revision_id)
VALUES('synthetic-legacy','claude','chat','session','title','safe','hash','2026-10-09T00:00:00Z','2026-10-09T00:00:00Z','inbox','source','synthetic-revision');
INSERT INTO fragment_revisions(id,fragment_id,ordinal,material_digest,content_digest,title,content,content_format,ordered_media_digest,normalizer_adapter,normalizer_version,observed_at,committed_at)
VALUES('synthetic-revision','synthetic-legacy',1,'material','content','title','safe','text','media','synthetic','1','2026-10-09T00:00:00Z','2026-10-09T00:00:00Z');
INSERT INTO attachments(id,kind,name,created_at) VALUES('synthetic-attachment','image','synthetic.png','2026-10-09T00:00:00Z');
INSERT INTO fragment_attachments(fragment_id,attachment_id,role,source,metadata_json,created_at)
VALUES('synthetic-legacy','synthetic-attachment','attachment','claude','{"token":"synthetic-media-secret"}','2026-10-09T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.backfillMedia(context.Background()); !errors.Is(err, transcript.ErrHistoricalDispositionRequired) {
		t.Fatalf("legacy media copy accepted: %v", err)
	}
	for _, table := range []string{"media_assets", "media_asset_source_observations", "media_backfill_state"} {
		var count int
		if err := st.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("backfill persisted %s %d %v", table, count, err)
		}
	}
}
