package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestStoreVariantContentDigestMismatchLeavesNoDatabaseOrFileResidue(t *testing.T) {
	st, repo, blobs, variant := mediaServiceFixture(t, domain.CustodyMirror, domain.DigestText("expected"))
	defer st.Close()
	svc := NewMediaService(repo, blobs)
	_, err := svc.StoreVariantContent(context.Background(), variant.ID, domain.ContentDigest{}, bytes.NewBufferString("actual"))
	if !IsDigestMismatch(err) {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
	assertMediaNotAcquired(t, st, repo, blobs, variant.ID)
}

func TestStoreVariantContentCommitFailureCompensatesInstalledBlob(t *testing.T) {
	payload := "bytes installed before commit"
	st, repo, blobs, variant := mediaServiceFixture(t, domain.CustodyMirror, domain.DigestText(payload))
	defer st.Close()
	svc := NewMediaService(repo, blobs)
	svc.commit = func(context.Context, *repository.MediaTransaction) error {
		return errors.New("injected commit failure")
	}
	_, err := svc.StoreVariantContent(context.Background(), variant.ID, domain.ContentDigest{}, bytes.NewBufferString(payload))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("injected commit failure")) {
		t.Fatalf("expected injected commit failure, got %v", err)
	}
	assertMediaNotAcquired(t, st, repo, blobs, variant.ID)
}

func TestStoreVariantContentRejectsConflictingRetryWithoutOrphanBlob(t *testing.T) {
	st, repo, blobs, variant := mediaServiceFixture(t, domain.CustodyMirror, "")
	defer st.Close()
	svc := NewMediaService(repo, blobs)
	first, err := svc.StoreVariantContent(context.Background(), variant.ID, domain.ContentDigest{}, bytes.NewBufferString("first payload"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.StoreVariantContent(context.Background(), variant.ID, domain.ContentDigest{}, bytes.NewBufferString("conflicting payload"))
	var conflict *repository.MediaConflictError
	if !errors.As(err, &conflict) && !IsDigestMismatch(err) {
		t.Fatalf("expected conflicting content retry, got %v", err)
	}
	after, err := repo.GetVariant(context.Background(), variant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Digest != first.Digest || after.BlobDigest != first.BlobDigest || countRegularFiles(t, blobs.Root()) != 1 {
		t.Fatalf("conflicting retry changed content or left an orphan: before=%+v after=%+v files=%d", first, after, countRegularFiles(t, blobs.Root()))
	}
}

func TestContentAddressedBlobIsReusedAcrossFragments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cross-fragment.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fragmentRepo := repository.NewFragmentRepository(st.DB)
	mediaRepo := repository.NewMediaRepository(st.DB)
	now := time.Now().UTC()
	payload := "cross-fragment bytes"
	expected := domain.ContentDigest{Algorithm: "sha256", Value: domain.DigestText(payload)}
	var variants []domain.AssetVariant
	for i, key := range []string{"one", "two"} {
		built, err := repository.BuildFragment(domain.PipelineFragment{
			Source: "url", SourceType: "article", SourceID: "https://example.com/" + key,
			Title: key, Content: key, CreatedAt: now,
		}, "cross-fragment", now)
		if err != nil {
			t.Fatal(err)
		}
		fragment, _, err := fragmentRepo.UpsertResolved(context.Background(), built)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := mediaRepo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{
			mediaServiceItem("asset-"+key, "https://cdn.example/"+key, domain.CustodyMirror, 0, expected),
		}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		variants = append(variants, resolved[0].Variants[0])
	}
	blobs, err := blobstore.NewFileStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewMediaService(mediaRepo, blobs)
	for _, variant := range variants {
		if _, err := svc.StoreVariantContent(context.Background(), variant.ID, domain.ContentDigest{}, bytes.NewBufferString(payload)); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_blobs`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || countRegularFiles(t, blobs.Root()) != 1 {
		t.Fatalf("cross-fragment reuse rows=%d files=%d", rows, countRegularFiles(t, blobs.Root()))
	}
}

func TestBlobReuseUpgradesSharedRetentionAndVariantFailuresRemainIndependent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "media.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fragmentRepo := repository.NewFragmentRepository(st.DB)
	now := time.Now().UTC()
	built, err := repository.BuildFragment(domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "https://example.com/shared",
		Title: "shared", Content: "shared", CreatedAt: now,
	}, "media-service-test", now)
	if err != nil {
		t.Fatal(err)
	}
	fragment, _, err := fragmentRepo.UpsertResolved(context.Background(), built)
	if err != nil {
		t.Fatal(err)
	}
	payload := "one blob reused by cache and mirror"
	expected := domain.ContentDigest{Algorithm: "sha256", Value: domain.DigestText(payload)}
	items := []domain.MediaManifestItem{
		mediaServiceItem("cache-asset", "https://cdn.example/cache", domain.CustodyCache, 0, expected),
		mediaServiceItem("mirror-asset", "https://cdn.example/mirror", domain.CustodyMirror, 1, expected),
		mediaServiceItem("failed-sibling", "https://cdn.example/fail", domain.CustodyMirror, 2, domain.ContentDigest{}),
	}
	items[2].Variants[0].AcquisitionState = domain.AcquisitionFailed
	items[2].Variants[0].Failure = &domain.AssetFailure{Code: "browser_fetch", Message: "denied", Retryable: true}
	mediaRepo := repository.NewMediaRepository(st.DB)
	resolved, err := mediaRepo.UpsertManifest(context.Background(), fragment.Revision.ID, items, now)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := blobstore.NewFileStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewMediaService(mediaRepo, blobs)
	cacheVariant := resolved[0].Variants[0]
	mirrorVariant := resolved[1].Variants[0]
	if _, err := svc.StoreVariantContent(context.Background(), cacheVariant.ID, domain.ContentDigest{}, bytes.NewBufferString(payload)); err != nil {
		t.Fatal(err)
	}
	// Retrying the same variant content is idempotent and reuses both the CAS
	// file and the existing verified variant binding.
	if _, err := svc.StoreVariantContent(context.Background(), cacheVariant.ID, domain.ContentDigest{}, bytes.NewBufferString(payload)); err != nil {
		t.Fatalf("idempotent variant retry: %v", err)
	}
	if _, err := svc.StoreVariantContent(context.Background(), mirrorVariant.ID, domain.ContentDigest{}, bytes.NewBufferString(payload)); err != nil {
		t.Fatal(err)
	}
	var blobCount int
	var retention string
	if err := st.DB.QueryRow(`SELECT COUNT(*), MIN(retention) FROM media_blobs`).Scan(&blobCount, &retention); err != nil {
		t.Fatal(err)
	}
	if blobCount != 1 || retention != string(domain.RetentionIndefinite) {
		t.Fatalf("shared blob count/retention = %d/%q", blobCount, retention)
	}
	files := countRegularFiles(t, blobs.Root())
	if files != 1 {
		t.Fatalf("content-addressed store has %d files, want 1", files)
	}
	failed, err := mediaRepo.GetVariant(context.Background(), resolved[2].Variants[0].ID)
	if err != nil || failed.AcquisitionState != domain.AcquisitionFailed {
		t.Fatalf("failed sibling changed during other acquisitions: %+v, %v", failed, err)
	}
	_, err = mediaRepo.MarkVariantFailed(context.Background(), mirrorVariant.ID, domain.AssetFailure{Code: "late", Message: "late failure"}, now.Add(time.Hour))
	var conflict *repository.MediaConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("available variant was degraded to failed: %v", err)
	}
	available, err := mediaRepo.GetVariant(context.Background(), mirrorVariant.ID)
	if err != nil || available.AcquisitionState != domain.AcquisitionAvailable {
		t.Fatalf("available variant state changed: %+v, %v", available, err)
	}
}

func mediaServiceFixture(t *testing.T, custody domain.CustodyMode, expectedValue string) (*store.Store, *repository.MediaRepository, *blobstore.FileStore, domain.AssetVariant) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "media.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	built, err := repository.BuildFragment(domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "https://example.com/service",
		Title: "service", Content: "body", CreatedAt: now,
	}, "media-service-test", now)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	fragment, _, err := repository.NewFragmentRepository(st.DB).UpsertResolved(context.Background(), built)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	expected := domain.ContentDigest{}
	if expectedValue != "" {
		expected = domain.ContentDigest{Algorithm: "sha256", Value: expectedValue}
	}
	mediaRepo := repository.NewMediaRepository(st.DB)
	resolved, err := mediaRepo.UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{
		mediaServiceItem("asset", "https://cdn.example/content", custody, 0, expected),
	}, now)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	blobs, err := blobstore.NewFileStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, mediaRepo, blobs, resolved[0].Variants[0]
}

func mediaServiceItem(key, locator string, custody domain.CustodyMode, position int, expected domain.ContentDigest) domain.MediaManifestItem {
	state := domain.AcquisitionPending
	if custody == domain.CustodyReference {
		state = domain.AcquisitionReferenceOnly
	}
	return domain.MediaManifestItem{
		Asset: domain.MediaAsset{
			SourceRegistrationID: "capture", Provider: "web", SourceMediaKey: key,
			SourceLocator: locator, Kind: domain.MediaImage, SourceAuthority: "web",
			DefaultCustody: custody,
		},
		Variants: []domain.AssetVariant{{
			VariantIdentity: "original", Kind: domain.VariantOriginal,
			SourceURL: locator, MIMEType: "image/jpeg", ExpectedDigest: expected,
			Custody: custody, AcquisitionState: state,
		}},
		Attachment: domain.AttachmentRef{Role: domain.AttachmentGalleryItem, Position: position},
	}
}

func assertMediaNotAcquired(t *testing.T, st *store.Store, repo *repository.MediaRepository, blobs *blobstore.FileStore, variantID string) {
	t.Helper()
	var blobCount int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_blobs`).Scan(&blobCount); err != nil {
		t.Fatal(err)
	}
	if blobCount != 0 {
		t.Fatalf("failed acquisition left %d blob rows", blobCount)
	}
	variant, err := repo.GetVariant(context.Background(), variantID)
	if err != nil {
		t.Fatal(err)
	}
	if variant.AcquisitionState != domain.AcquisitionPending || variant.BlobDigest != "" {
		t.Fatalf("failed acquisition changed variant: %+v", variant)
	}
	if files := countRegularFiles(t, blobs.Root()); files != 0 {
		t.Fatalf("failed acquisition left %d durable files", files)
	}
}

func countRegularFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}
