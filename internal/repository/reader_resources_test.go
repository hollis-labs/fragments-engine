package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestReaderResourceRepositoryResolvesAliasAndPinsRevisionOwnership(t *testing.T) {
	st, first := mediaTestStoreAndFragment(t, "reader-owner", nil)
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 16, 0, 0, 0, time.UTC)

	aliasCandidate, err := BuildFragment(domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "https://example.com/reader-alias",
		Title: "Alias", Content: "alias body", CreatedAt: now,
	}, "media-test", now)
	if err != nil {
		t.Fatal(err)
	}
	aliasFragment, _, err := NewFragmentRepository(st.DB).UpsertResolved(ctx, aliasCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO fragment_identity_aliases(alias_fragment_id, fragment_id, created_at) VALUES (?, ?, ?)`, aliasFragment.ID, first.ID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	media := mediaTestItem("youtube", "3RmtNXqnreI", "video", "https://www.youtube.com/watch?v=3RmtNXqnreI", domain.MediaVideo, domain.AttachmentPrimary, 0, []domain.AssetVariant{{
		VariantIdentity: "poster", Kind: domain.VariantPoster, MIMEType: "image/png",
		Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending,
	}})
	manifest, err := NewMediaRepository(st.DB).UpsertManifest(ctx, first.Revision.ID, []domain.MediaManifestItem{media}, now)
	if err != nil {
		t.Fatal(err)
	}
	variantID := manifest[0].Variants[0].ID

	repo := NewReaderResourceRepository(st.DB)
	ownedRevision, err := repo.GetRevision(ctx, aliasFragment.ID, first.Revision.ID)
	if err != nil || ownedRevision.CanonicalFragmentID != first.ID {
		t.Fatalf("alias revision lookup = %+v, %v", ownedRevision, err)
	}
	ownedVariant, err := repo.GetVariant(ctx, aliasFragment.ID, first.Revision.ID, variantID)
	if err != nil || ownedVariant.Asset.ID != manifest[0].Asset.ID {
		t.Fatalf("alias media lookup = %+v, %v", ownedVariant, err)
	}
	if _, err := repo.GetRevision(ctx, aliasFragment.ID, aliasFragment.Revision.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("alias widened revision ownership: %v", err)
	}
	if _, err := repo.GetVariant(ctx, aliasFragment.ID, aliasFragment.Revision.ID, variantID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("alias widened variant ownership: %v", err)
	}
}

func TestReaderResourceRepositoryDoesNotExposeNewRevisionMediaToOldRevision(t *testing.T) {
	st, first := mediaTestStoreAndFragment(t, "reader-history", nil)
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 17, 0, 0, 0, time.UTC)
	changed, err := BuildFragment(domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "https://example.com/reader-history",
		Title: "Media fixture", Content: "changed body", CreatedAt: now,
	}, "media-test", now)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := NewFragmentRepository(st.DB).UpsertResolved(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Revision.ID == first.Revision.ID {
		t.Fatalf("fixture did not create stable-fragment revision: first=%+v second=%+v", first, second)
	}
	media := mediaTestItem("provider", "media-reader-history", "history", "https://cdn.example/history.png", domain.MediaImage, domain.AttachmentPrimary, 0, []domain.AssetVariant{{
		VariantIdentity: "original", Kind: domain.VariantOriginal, MIMEType: "image/png",
		Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending,
	}})
	manifest, err := NewMediaRepository(st.DB).UpsertManifest(ctx, second.Revision.ID, []domain.MediaManifestItem{media}, now)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewReaderResourceRepository(st.DB)
	if _, err := repo.GetVariant(ctx, first.ID, first.Revision.ID, manifest[0].Variants[0].ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old revision exposed new media: %v", err)
	}
	if _, err := repo.GetVariant(ctx, first.ID, second.Revision.ID, manifest[0].Variants[0].ID); err != nil {
		t.Fatalf("new revision did not own media: %v", err)
	}
}
