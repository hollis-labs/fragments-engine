package service

import (
	"context"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

func TestManualIntakeUsesResolvedLegacyIDForDownstreamWrites(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()
	svcs.fragments.enricher.SetLinkProvider(&countingLinkProvider{})

	const (
		legacyID  = "legacy-prefetched-fragment"
		sourceURL = "https://example.com/articles/stable"
	)
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	seed, err := repository.BuildFragment(domain.PipelineFragment{
		Source:     "manual",
		SourceType: "article",
		SourceID:   "legacy-content-address",
		SourceIdentity: domain.SourceIdentity{
			SourceRegistrationID: "manual-intake",
			Provider:             "web",
			SourceItemKey:        sourceURL,
			SegmentKey:           domain.DefaultSegmentKey,
			SubmittedURL:         sourceURL,
			CanonicalURL:         sourceURL,
			SourceAdapter:        domain.AdapterVersion{Adapter: "legacy-web-clipper", Version: "0.1.0"},
			Canonicalizer:        domain.AdapterVersion{Adapter: "legacy-web", Version: "0.1.0"},
		},
		Title:     "Legacy capture",
		Content:   "old extracted body",
		CreatedAt: now,
	}, "manual-intake", now)
	if err != nil {
		t.Fatal(err)
	}
	seed.ID = legacyID
	seed.Revision.FragmentID = legacyID
	seeded, outcome, err := svcs.fragments.repo.UpsertResolved(context.Background(), seed)
	if err != nil || outcome != repository.UpsertInserted || seeded.ID != legacyID {
		t.Fatalf("seed legacy identity = %+v, %s, %v", seeded, outcome, err)
	}

	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content:   "new extracted body",
		SourceURL: sourceURL,
		Tags:      []string{"resolved-id"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FragmentID != legacyID || result.Outcome != string(repository.UpsertUpdated) {
		t.Fatalf("manual intake did not return preserved ID: %+v", result)
	}
	revisions, err := svcs.fragments.repo.ListRevisions(context.Background(), legacyID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("manual revisions = %d, %v", len(revisions), err)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), legacyID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Fragment.ID != legacyID || detail.Fragment.Content != "new extracted body" {
		t.Fatalf("manual intake did not update canonical fragment: %+v", detail.Fragment)
	}
	identity := detail.Fragment.SourceIdentity
	if identity.SourceRegistrationID != "manual-intake" || identity.SourceItemKey != sourceURL || identity.SegmentKey != "root" {
		t.Fatalf("unexpected stable source identity: %+v", identity)
	}
	if identity.SubmittedURL != sourceURL || identity.CanonicalURL != sourceURL || identity.SourceAdapter.Version == "" || identity.Canonicalizer.Version == "" {
		t.Fatalf("missing URL/adapter provenance: %+v", identity)
	}
	if detail.Fragment.AcceptedRevisionID == "" || detail.Fragment.CurrentRevisionID == "" || detail.Fragment.Revision.ID != detail.Fragment.CurrentRevisionID {
		t.Fatalf("missing accepted/current revision projection: %+v", detail.Fragment)
	}
	assertEntityPresent(t, detail.Entities, "tag", "resolved-id")
	if len(detail.Attachments) != 1 || detail.Attachments[0].ExternalURL != sourceURL {
		t.Fatalf("attachment stage wrote to speculative ID: %+v", detail.Attachments)
	}

	const aliasID = "older-content-addressed-id"
	if _, err := svcs.db.Exec(`
INSERT INTO fragments (
  id, source, source_type, source_id, title, content, content_hash,
  created_at, ingested_at, status, metadata_json, ingest_name, canonical_path
) VALUES (?, 'manual', 'article', 'older-source-id', 'Older', 'older', ?, ?, ?, 'inbox', '{}', 'manual-intake', '')`,
		aliasID, domain.DigestText("older"), now.Format(time.RFC3339), now.Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := svcs.db.Exec(`
INSERT INTO fragment_identity_aliases(alias_fragment_id, fragment_id, created_at)
VALUES (?, ?, ?)`, aliasID, legacyID, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	aliasDetail, err := svcs.fragments.GetDetail(context.Background(), aliasID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if aliasDetail.Fragment.ID != legacyID || len(aliasDetail.Entities) == 0 || len(aliasDetail.Attachments) != 1 {
		t.Fatalf("legacy alias detail did not use canonical dependent state: %+v", aliasDetail)
	}
}
