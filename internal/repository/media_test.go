package repository

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestMediaManifestPersistsGeneralKindsOrderedRefsAndPartialVariantState(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "partial", nil)
	defer st.Close()
	repo := NewMediaRepository(st.DB)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	retryable := &domain.AssetFailure{Code: "fetch_failed", Message: "upstream unavailable", Retryable: true}
	items := []domain.MediaManifestItem{
		mediaTestItem("youtube", "video-1", "video:1", "https://youtube.example/video/1", domain.MediaVideo, domain.AttachmentPrimary, 0, []domain.AssetVariant{
			{VariantIdentity: "video:reference", Kind: domain.VariantOriginal, SourceURL: "https://youtube.example/watch/1", Custody: domain.CustodyReference, AcquisitionState: domain.AcquisitionReferenceOnly},
			{VariantIdentity: "poster", Kind: domain.VariantPoster, SourceURL: "https://cdn.example/poster.jpg", MIMEType: "image/jpeg", Width: 1280, Height: 720, Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending},
		}),
		mediaTestItem("instagram", "image-2", "carousel:1", "https://cdn.example/image-2.jpg", domain.MediaImage, domain.AttachmentGalleryItem, 1, []domain.AssetVariant{
			{VariantIdentity: "original", Kind: domain.VariantOriginal, SourceURL: "https://cdn.example/image-2.jpg", MIMEType: "image/jpeg", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionFailed, Failure: retryable},
			{VariantIdentity: "thumbnail", Kind: domain.VariantThumbnail, SourceURL: "https://cdn.example/image-2-thumb.jpg", MIMEType: "image/jpeg", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending},
		}),
	}
	items[0].Asset.DurationSeconds = 83.5
	items[0].Asset.AltText = "Video demonstration"
	items[0].Attachment.Caption = "Primary video"
	items[0].Variants[1].SourceExpiresAt = now.Add(15 * time.Minute)
	items[1].Attachment.SourceContext = "carousel slide 2"

	resolved, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, items, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 || resolved[0].Attachment.Position != 0 || resolved[1].Attachment.Position != 1 {
		t.Fatalf("unexpected resolved order: %+v", resolved)
	}
	listed, err := repo.ListByRevision(context.Background(), fragment.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Asset.Kind != domain.MediaVideo || listed[1].Asset.Kind != domain.MediaImage {
		t.Fatalf("unexpected media projection: %+v", listed)
	}
	if listed[0].Variants[0].AcquisitionState == listed[0].Variants[1].AcquisitionState {
		t.Fatalf("video variants did not retain independent states: %+v", listed[0].Variants)
	}
	var failed, pending bool
	for _, variant := range listed[1].Variants {
		failed = failed || (variant.AcquisitionState == domain.AcquisitionFailed && variant.Failure != nil && variant.Failure.Retryable)
		pending = pending || variant.AcquisitionState == domain.AcquisitionPending
	}
	if !failed || !pending {
		t.Fatalf("partial gallery states were not preserved: %+v", listed[1].Variants)
	}
	if listed[0].Attachment.Caption != "Primary video" || listed[1].Attachment.SourceContext != "carousel slide 2" {
		t.Fatalf("attachment context was lost: %+v", listed)
	}
	if listed[0].Asset.DurationSeconds != 83.5 || listed[0].Asset.AltText != "Video demonstration" || !listed[0].Variants[1].SourceExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("media dimensions/duration/alt/source expiry provenance was lost: %+v", listed[0])
	}
}

func TestMediaManifestSupportsEveryContractKindRoleAndVariant(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "all-kinds", nil)
	defer st.Close()
	mediaKinds := []domain.MediaKind{domain.MediaImage, domain.MediaVideo, domain.MediaAudio, domain.MediaDocument, domain.MediaTimedText, domain.MediaOther, domain.MediaImage}
	roles := []domain.AttachmentRole{domain.AttachmentPrimary, domain.AttachmentGalleryItem, domain.AttachmentHero, domain.AttachmentInline, domain.AttachmentPoster, domain.AttachmentTranscript, domain.AttachmentOther}
	variantKinds := []domain.AssetVariantKind{domain.VariantOriginal, domain.VariantPreview, domain.VariantThumbnail, domain.VariantPoster, domain.VariantAudio, domain.VariantSubtitles, domain.VariantTranscript}
	custodies := []domain.CustodyMode{domain.CustodyReference, domain.CustodyCache, domain.CustodyMirror, domain.CustodyAdopted}
	items := make([]domain.MediaManifestItem, 0, len(variantKinds))
	for i := range variantKinds {
		custody := custodies[i%len(custodies)]
		state := domain.AcquisitionPending
		retention := domain.RetentionPolicy("")
		sourcePath := ""
		if custody == domain.CustodyReference {
			state = domain.AcquisitionReferenceOnly
		}
		if custody == domain.CustodyAdopted {
			state = domain.AcquisitionAvailable
			sourcePath = "/adopted/item"
		}
		items = append(items, mediaTestItem("provider", "media-"+string(rune('a'+i)), "client-"+string(rune('a'+i)), "https://cdn.example/item-"+string(rune('a'+i)), mediaKinds[i], roles[i], i, []domain.AssetVariant{{
			VariantIdentity: string(variantKinds[i]), Kind: variantKinds[i], SourcePath: sourcePath,
			Custody: custody, AcquisitionState: state, Retention: retention,
		}}))
	}
	resolved, err := NewMediaRepository(st.DB).UpsertManifest(context.Background(), fragment.Revision.ID, items, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != len(items) {
		t.Fatalf("resolved %d media items, want %d", len(resolved), len(items))
	}
	for i, item := range resolved {
		if item.Asset.Kind != mediaKinds[i] || item.Attachment.Role != roles[i] || item.Variants[0].Kind != variantKinds[i] {
			t.Fatalf("contract kind/role/variant %d changed: %+v", i, item)
		}
		if (item.Variants[0].Custody == domain.CustodyMirror || item.Variants[0].Custody == domain.CustodyAdopted) && item.Variants[0].Retention != domain.RetentionIndefinite {
			t.Fatalf("custody %s did not default to indefinite retention", item.Variants[0].Custody)
		}
	}
}

func TestProviderIdentityCompletionReusesLocatorKeyedAsset(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "provider-upgrade", nil)
	defer st.Close()
	repo := NewMediaRepository(st.DB)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	first := mediaTestItem("instagram", "", "generic:image:0", "https://cdn.example/stable-image.jpg", domain.MediaImage, domain.AttachmentPrimary, 0, []domain.AssetVariant{{
		VariantIdentity: "original", Kind: domain.VariantOriginal, SourceURL: "https://cdn.example/first-signed.jpg", Custody: domain.CustodyMirror,
	}})
	one, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{first}, now)
	if err != nil {
		t.Fatal(err)
	}
	completed := first
	completed.Asset.ProviderMediaID = "provider-media-123"
	completed.Asset.SourceMediaKey = "instagram:item:rotated"
	completed.Variants[0].SourceURL = "https://cdn.example/second-signed.jpg"
	two, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{completed}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if one[0].Asset.ID != two[0].Asset.ID || two[0].Asset.ProviderMediaID != "provider-media-123" {
		t.Fatalf("provider completion duplicated or failed to strengthen asset: first=%+v second=%+v", one[0].Asset, two[0].Asset)
	}
	var assets, aliases, observations, variantObservations int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_asset_identity_aliases`).Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_asset_source_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM asset_variant_source_observations`).Scan(&variantObservations); err != nil {
		t.Fatal(err)
	}
	if assets != 1 || aliases != 1 || observations != 2 || variantObservations != 2 {
		t.Fatalf("provider identity upgrade counts: assets=%d aliases=%d observations=%d variant_observations=%d", assets, aliases, observations, variantObservations)
	}
}

func TestProviderIdentityCompletionReusesClientKeyedAssetWhenLocatorArrivesLater(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "provider-client-upgrade", nil)
	defer st.Close()
	repo := NewMediaRepository(st.DB)
	now := time.Now().UTC()
	first := mediaTestItem("pinterest", "", "browser-media-key", "", domain.MediaImage, domain.AttachmentPrimary, 0, []domain.AssetVariant{{
		VariantIdentity: "original", Kind: domain.VariantOriginal, Custody: domain.CustodyMirror,
	}})
	one, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{first}, now)
	if err != nil {
		t.Fatal(err)
	}
	completed := first
	completed.Asset.ProviderMediaID = "pin-image-123"
	completed.Asset.SourceLocator = "https://cdn.example/pin-image.jpg"
	two, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{completed}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if one[0].Asset.ID != two[0].Asset.ID || two[0].Asset.SourceLocator != completed.Asset.SourceLocator {
		t.Fatalf("client-key identity was not upgraded in place: first=%+v second=%+v", one[0].Asset, two[0].Asset)
	}
}

func TestProviderIdentityClaimRejectsSplitBrainAssets(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "provider-split", nil)
	defer st.Close()
	repo := NewMediaRepository(st.DB)
	now := time.Now().UTC()
	items := []domain.MediaManifestItem{
		mediaTestItem("instagram", "", "generic:image:0", "https://cdn.example/a.jpg", domain.MediaImage, domain.AttachmentGalleryItem, 0, []domain.AssetVariant{{VariantIdentity: "original", Kind: domain.VariantOriginal, Custody: domain.CustodyReference}}),
		mediaTestItem("instagram", "", "generic:image:1", "https://cdn.example/b.jpg", domain.MediaImage, domain.AttachmentGalleryItem, 1, []domain.AssetVariant{{VariantIdentity: "original", Kind: domain.VariantOriginal, Custody: domain.CustodyReference}}),
	}
	if _, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, items, now); err != nil {
		t.Fatal(err)
	}
	claimed := append([]domain.MediaManifestItem(nil), items...)
	claimed[0].Asset.ProviderMediaID = "same-provider-id"
	claimed[1].Asset.ProviderMediaID = "same-provider-id"
	_, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, claimed, now.Add(time.Minute))
	var conflict *MediaConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected split-brain provider identity conflict, got %v", err)
	}
	var aliases int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_asset_identity_aliases`).Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if aliases != 0 {
		t.Fatalf("conflicting provider identity partially committed %d aliases", aliases)
	}
}

func TestMediaAssetKindRefinesFromOtherButDoesNotRegress(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "kind-refinement", nil)
	defer st.Close()
	repo := NewMediaRepository(st.DB)
	now := time.Now().UTC()
	unknown := mediaTestItem("web", "", "media", "https://cdn.example/media", domain.MediaOther, domain.AttachmentPrimary, 0, []domain.AssetVariant{{
		VariantIdentity: "original", Kind: domain.VariantOriginal, Custody: domain.CustodyReference,
	}})
	first, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{unknown}, now)
	if err != nil {
		t.Fatal(err)
	}
	refined := unknown
	refined.Asset.Kind = domain.MediaImage
	second, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{refined}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Asset.ID != second[0].Asset.ID || second[0].Asset.Kind != domain.MediaImage {
		t.Fatalf("other kind did not refine in place: first=%+v second=%+v", first[0].Asset, second[0].Asset)
	}
	regressed := refined
	regressed.Asset.Kind = domain.MediaOther
	_, err = repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{regressed}, now.Add(2*time.Minute))
	var conflict *MediaConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("concrete kind regressed to other: %v", err)
	}
	changed := refined
	changed.Asset.Kind = domain.MediaVideo
	_, err = repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{changed}, now.Add(3*time.Minute))
	if !errors.As(err, &conflict) {
		t.Fatalf("concrete kind changed to different concrete kind: %v", err)
	}
}

func TestVariantIdentityRejectsConflictingDigestAndSemantics(t *testing.T) {
	st, fragment := mediaTestStoreAndFragment(t, "variant-conflict", nil)
	defer st.Close()
	repo := NewMediaRepository(st.DB)
	now := time.Now().UTC()
	first := mediaTestItem("web", "", "hero", "https://cdn.example/hero.jpg", domain.MediaImage, domain.AttachmentHero, 0, []domain.AssetVariant{{
		VariantIdentity: "original", Kind: domain.VariantOriginal, MIMEType: "image/jpeg", Custody: domain.CustodyMirror,
		ExpectedDigest: domain.ContentDigest{Algorithm: "sha256", Value: digestOf("one")},
	}})
	if _, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{first}, now); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Variants[0].ExpectedDigest.Value = digestOf("two")
	_, err := repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{conflict}, now)
	var mediaConflict *MediaConflictError
	if !errors.As(err, &mediaConflict) {
		t.Fatalf("expected digest conflict, got %v", err)
	}
	conflict = first
	conflict.Variants[0].Kind = domain.VariantThumbnail
	_, err = repo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{conflict}, now)
	if !errors.As(err, &mediaConflict) {
		t.Fatalf("expected semantic conflict, got %v", err)
	}
}

func TestLegacyReplaceCreatesNewRevisionAndPreservesOldAttachmentRefs(t *testing.T) {
	firstAttachments := []domain.PipelineAttachment{{Kind: "image", Role: "content", Name: "first.jpg", ExternalURL: "https://cdn.example/first.jpg", Source: "web"}}
	st, fragment := mediaTestStoreAndFragment(t, "immutable-refs", firstAttachments)
	defer st.Close()
	attachments := NewAttachmentRepository(st.DB)
	media := NewMediaRepository(st.DB)
	now := time.Now().UTC()
	if err := attachments.ReplaceFragmentRevisionAttachments(context.Background(), fragment.ID, fragment.Revision.ID, firstAttachments, now); err != nil {
		t.Fatal(err)
	}
	old, err := media.ListByRevision(context.Background(), fragment.Revision.ID)
	if err != nil || len(old) != 1 {
		t.Fatalf("old refs before replacement: %+v, %v", old, err)
	}
	changed := []domain.PipelineAttachment{
		{Kind: "image", Role: "content", Name: "second.jpg", ExternalURL: "https://cdn.example/second.jpg", Source: "web"},
		{Kind: "image", Role: "content", Name: "first.jpg", ExternalURL: "https://cdn.example/first.jpg", Source: "web"},
	}
	if err := attachments.ReplaceFragmentAttachments(context.Background(), fragment.ID, changed, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	current, err := NewFragmentRepository(st.DB).GetByID(context.Background(), fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision.ID == fragment.Revision.ID {
		t.Fatal("changed legacy manifest did not create a new revision")
	}
	oldAgain, err := media.ListByRevision(context.Background(), fragment.Revision.ID)
	if err != nil || len(oldAgain) != 1 || oldAgain[0].Asset.ID != old[0].Asset.ID {
		t.Fatalf("old revision refs mutated: before=%+v after=%+v err=%v", old, oldAgain, err)
	}
	newRefs, err := media.ListByRevision(context.Background(), current.Revision.ID)
	if err != nil || len(newRefs) != 2 || newRefs[0].Asset.SourceLocator != "https://cdn.example/second.jpg" {
		t.Fatalf("new ordered refs = %+v, %v", newRefs, err)
	}
	if err := attachments.ReplaceFragmentAttachments(context.Background(), fragment.ID, changed, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	revisions, err := NewFragmentRepository(st.DB).ListRevisions(context.Background(), fragment.ID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("idempotent replacement created another revision: %d, %v", len(revisions), err)
	}
}

func TestLegacyAttachmentRevisionUsesImmutableSourceFieldsAndPreservesDisplayProjection(t *testing.T) {
	firstAttachments := []domain.PipelineAttachment{{Kind: "image", Role: "content", Name: "first.jpg", ExternalURL: "https://cdn.example/first.jpg", Source: "web"}}
	st, fragment := mediaTestStoreAndFragment(t, "source-owned-attachment-revision", firstAttachments)
	defer st.Close()
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `
UPDATE fragments SET title = 'provider display title', content = 'provider display body',
  metadata_json = '{"provider_summary":"derived","user_note":"annotation"}'
WHERE id = ?`, fragment.ID); err != nil {
		t.Fatal(err)
	}
	changed := []domain.PipelineAttachment{{Kind: "image", Role: "content", Name: "second.jpg", ExternalURL: "https://cdn.example/second.jpg", Source: "web"}}
	if err := NewAttachmentRepository(st.DB).ReplaceFragmentAttachments(ctx, fragment.ID, changed, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	current, err := NewFragmentRepository(st.DB).GetByID(ctx, fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Title != "provider display title" || current.Content != "provider display body" {
		t.Fatalf("attachment revision overwrote mutable display projection: %+v", current)
	}
	wantMaterial := domain.NormalizeMaterial(
		fragment.Revision.Title, fragment.Revision.Description, fragment.Revision.Content,
		fragment.Revision.ContentFormat, changed,
	)
	if current.Revision.Title != fragment.Revision.Title || current.Revision.Content != fragment.Revision.Content || current.Revision.MaterialDigest != wantMaterial.Digest() {
		t.Fatalf("attachment revision mixed display/provider state into source material: got=%+v want_digest=%s", current.Revision, wantMaterial.Digest())
	}
	if current.Revision.MetadataJSON != fragment.Revision.MetadataJSON {
		t.Fatalf("attachment revision mixed mutable metadata into source evidence: got=%s want=%s", current.Revision.MetadataJSON, fragment.Revision.MetadataJSON)
	}
}

func TestMediaManifestConcurrentAcrossDatabaseHandlesConverges(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "media.db")
	st1, fragment := mediaTestStoreAndFragmentAt(t, dbPath, "concurrent", nil)
	defer st1.Close()
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	repos := []*MediaRepository{NewMediaRepository(st1.DB), NewMediaRepository(st2.DB)}
	item := mediaTestItem("pinterest", "pin-image-1", "pin-image", "https://cdn.example/pin.jpg", domain.MediaImage, domain.AttachmentPrimary, 0, []domain.AssetVariant{{
		VariantIdentity: "original", Kind: domain.VariantOriginal, SourceURL: "https://cdn.example/pin.jpg", Custody: domain.CustodyMirror,
	}})
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := repos[i%2].UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{item}, time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for table, want := range map[string]int{"media_assets": 1, "asset_variants": 1, "attachment_refs": 1} {
		var got int
		if err := st1.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
}

func TestMediaBackfillMapsDistinctAliasAttachmentsToExactHistoricalRevisions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-media.db")
	createLegacyIdentityFixture(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO attachments(id, kind, name, external_url, created_at)
VALUES ('old-attachment', 'image', 'old.jpg', 'https://example.com/old.jpg', '2026-01-01T00:00:00Z');
INSERT INTO fragment_attachments(fragment_id, attachment_id, role, source, source_item_id, storage_path, preview_storage_path, created_at)
VALUES ('legacy-a', 'old-attachment', 'hero', 'legacy', 'old', '/legacy/old.jpg', '/legacy/old.preview.jpg', '2026-01-01T00:00:00Z');
UPDATE fragment_attachments SET storage_path = '/legacy/latest.jpg', preview_storage_path = '/legacy/latest.preview.jpg'
WHERE fragment_id = 'legacy-b' AND attachment_id = 'latest-attachment';`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fragmentRepo := NewFragmentRepository(st.DB)
	revisions, err := fragmentRepo.ListRevisions(context.Background(), "legacy-b")
	if err != nil || len(revisions) != 2 {
		t.Fatalf("legacy revisions = %d, %v", len(revisions), err)
	}
	mediaRepo := NewMediaRepository(st.DB)
	oldMedia, err := mediaRepo.ListByRevision(context.Background(), revisions[0].ID)
	if err != nil || len(oldMedia) != 1 || oldMedia[0].Attachment.LegacyFragmentID != "legacy-a" || oldMedia[0].Attachment.LegacyAttachmentID != "old-attachment" {
		t.Fatalf("old alias attachment mapped incorrectly: %+v, %v", oldMedia, err)
	}
	latestMedia, err := mediaRepo.ListByRevision(context.Background(), revisions[1].ID)
	if err != nil || len(latestMedia) != 1 || latestMedia[0].Attachment.LegacyFragmentID != "legacy-b" {
		t.Fatalf("latest alias attachment mapped incorrectly: %+v, %v", latestMedia, err)
	}
	if len(oldMedia[0].Variants) != 2 || len(latestMedia[0].Variants) != 2 {
		t.Fatalf("legacy previews were not projected: old=%+v latest=%+v", oldMedia[0].Variants, latestMedia[0].Variants)
	}
	legacy, err := NewAttachmentRepository(st.DB).ListByFragment(context.Background(), "legacy-b")
	if err != nil || len(legacy) != 1 || legacy[0].StoragePath != "/legacy/latest.jpg" || legacy[0].PreviewStoragePath != "/legacy/latest.preview.jpg" {
		t.Fatalf("legacy API projection changed: %+v, %v", legacy, err)
	}
}

func TestMediaBackfillDoesNotMultiplyMultipleLegacyAttachments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-multiple-media.db")
	createLegacyIdentityFixture(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO attachments(id, kind, name, external_url, created_at)
VALUES ('second-attachment', 'image', 'second.jpg', 'https://example.com/second.jpg', '2026-01-03T00:00:00Z');
INSERT INTO fragment_attachments(fragment_id, attachment_id, role, source, source_item_id, created_at)
VALUES ('legacy-b', 'second-attachment', 'content', 'legacy', 'second', '2026-01-03T00:00:00Z');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fragment, err := NewFragmentRepository(st.DB).GetByID(context.Background(), "legacy-b")
	if err != nil {
		t.Fatal(err)
	}
	media, err := NewMediaRepository(st.DB).ListByRevision(context.Background(), fragment.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 2 || media[0].Attachment.Position != 0 || media[1].Attachment.Position != 1 {
		t.Fatalf("legacy two-item manifest was multiplied or misordered: %+v", media)
	}
}

func TestMediaBackfillChoosesPreferredLegacySnapshotWhenRowsCollapseToOneRevision(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-collapsed.db")
	createLegacyIdentityFixture(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the historical content_hash unique while making normalized source
	// material and the attachment manifest identical. Identity backfill must
	// collapse the observations to one revision whose legacy_fragment_id is
	// the first deterministic observation (legacy-a).
	if _, err := db.Exec(`
UPDATE fragments SET title = '  Second 	', content = '  second material

' WHERE id = 'legacy-a';
INSERT INTO fragment_attachments(fragment_id, attachment_id, role, source, source_item_id, created_at)
SELECT 'legacy-a', attachment_id, role, source, source_item_id, '2026-01-01T00:00:00Z'
FROM fragment_attachments WHERE fragment_id = 'legacy-b';`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var revisions, refs int
	var legacyOwner string
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM fragment_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM attachment_refs`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT legacy_fragment_id FROM attachment_refs`).Scan(&legacyOwner); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 || refs != 1 || legacyOwner != "legacy-a" {
		t.Fatalf("collapsed backfill revisions=%d refs=%d legacy_owner=%q", revisions, refs, legacyOwner)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.DB.QueryRow(`SELECT COUNT(*) FROM attachment_refs`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("media backfill was not idempotent after reopen: %d refs", refs)
	}
}

func TestLegacyStorageAndPreviewUpdatesDualWriteWithoutChangingAPIProjection(t *testing.T) {
	attachmentsInput := []domain.PipelineAttachment{{
		Kind: "image", Role: "hero", Name: "hero.png", MIMEType: "image/png",
		ExternalURL: "https://cdn.example/hero.png", Source: "web",
	}}
	st, fragment := mediaTestStoreAndFragment(t, "preview-dual-write", attachmentsInput)
	defer st.Close()
	attachments := NewAttachmentRepository(st.DB)
	if err := attachments.ReplaceFragmentRevisionAttachments(context.Background(), fragment.ID, fragment.Revision.ID, attachmentsInput, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	legacy, err := attachments.ListByFragment(context.Background(), fragment.ID)
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy attachment = %+v, %v", legacy, err)
	}
	if err := attachments.UpdateFragmentAttachmentStoragePaths(context.Background(), fragment.ID, map[string]domain.PublishedAttachmentInfo{
		legacy[0].ID: {StoragePath: "/published/hero.png", PreviewStoragePath: "/published/hero.preview.jpg"},
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err = attachments.ListByFragment(context.Background(), fragment.ID)
	if err != nil || legacy[0].StoragePath != "/published/hero.png" || legacy[0].PreviewStoragePath != "/published/hero.preview.jpg" {
		t.Fatalf("legacy media API paths changed: %+v, %v", legacy, err)
	}
	media, err := NewMediaRepository(st.DB).ListByRevision(context.Background(), fragment.Revision.ID)
	if err != nil || len(media) != 1 || len(media[0].Variants) != 2 {
		t.Fatalf("new media projection missing preview: %+v, %v", media, err)
	}
	var foundPreview bool
	var foundOriginal bool
	for _, variant := range media[0].Variants {
		if variant.Kind == domain.VariantPreview && variant.LegacyStoragePath == "/published/hero.preview.jpg" && variant.AcquisitionState == domain.AcquisitionAvailable {
			foundPreview = true
		}
		if variant.Kind == domain.VariantOriginal && variant.LegacyStoragePath == "/published/hero.png" && variant.Custody == domain.CustodyAdopted && variant.AcquisitionState == domain.AcquisitionAvailable && variant.Retention == domain.RetentionIndefinite {
			foundOriginal = true
		}
	}
	if !foundPreview || !foundOriginal {
		t.Fatalf("published variants not dual-written: %+v", media[0].Variants)
	}
	if err := attachments.UpdateFragmentAttachmentStoragePaths(context.Background(), fragment.ID, map[string]domain.PublishedAttachmentInfo{
		legacy[0].ID: {StoragePath: "/published-v2/hero.png", PreviewStoragePath: "/published-v2/hero.preview.jpg"},
	}); err != nil {
		t.Fatal(err)
	}
	media, err = NewMediaRepository(st.DB).ListByRevision(context.Background(), fragment.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundPreview = false
	for _, variant := range media[0].Variants {
		if variant.Kind == domain.VariantPreview && variant.SourcePath == "/published-v2/hero.preview.jpg" && variant.LegacyStoragePath == "/published-v2/hero.preview.jpg" {
			foundPreview = true
		}
	}
	if !foundPreview {
		t.Fatalf("preview rotation left a stale projection: %+v", media[0].Variants)
	}
}

func mediaTestStoreAndFragment(t *testing.T, key string, attachments []domain.PipelineAttachment) (*store.Store, domain.Fragment) {
	t.Helper()
	return mediaTestStoreAndFragmentAt(t, filepath.Join(t.TempDir(), "media.db"), key, attachments)
}

func mediaTestStoreAndFragmentAt(t *testing.T, dbPath, key string, attachments []domain.PipelineAttachment) (*store.Store, domain.Fragment) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	built, err := BuildFragment(domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "https://example.com/" + key,
		Title: "Media fixture", Content: "body", CreatedAt: now, Attachments: attachments,
	}, "media-test", now)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	fragment, _, err := NewFragmentRepository(st.DB).UpsertResolved(context.Background(), built)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, fragment
}

func mediaTestItem(provider, providerID, sourceKey, locator string, kind domain.MediaKind, role domain.AttachmentRole, position int, variants []domain.AssetVariant) domain.MediaManifestItem {
	return domain.MediaManifestItem{
		Asset: domain.MediaAsset{
			SourceRegistrationID: "browser-capture", Provider: provider,
			ProviderMediaID: providerID, SourceMediaKey: sourceKey,
			SourceLocator: locator, Kind: kind, SourceAuthority: provider,
			DefaultCustody: domain.CustodyReference,
		},
		Variants:   variants,
		Attachment: domain.AttachmentRef{Role: role, Position: position},
	}
}

func digestOf(value string) string {
	return domain.DigestText(value)
}
