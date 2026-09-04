package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestAssetAcquisitionRequestTransitionsReferenceVariantAndReplaysExactly(t *testing.T) {
	st, repo, _, variant := mediaServiceFixture(t, domain.CustodyReference, "")
	defer st.Close()
	svc := NewAssetAcquisitionService(repo)
	svc.now = func() time.Time { return time.Date(2026, 9, 3, 15, 0, 0, 0, time.UTC) }
	command := AssetAcquisitionCommand{
		CommandID: "command-1", IdempotencyKey: "request-1", MediaAssetID: variant.MediaAssetID,
		VariantKind: domain.VariantOriginal, RequestedCustody: domain.CustodyMirror,
		RequestedBy: "reader-user", Reason: "keep this video",
	}
	first, err := svc.Request(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotentReplay || first.Variant.ID != variant.ID || first.Variant.Custody != domain.CustodyMirror || first.Variant.AcquisitionState != domain.AcquisitionPending {
		t.Fatalf("explicit request did not advance existing lifecycle: %+v", first)
	}
	second, err := svc.Request(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !second.IdempotentReplay || second.Request.ID != first.Request.ID || second.Variant.ID != first.Variant.ID {
		t.Fatalf("exact replay changed request: first=%+v second=%+v", first, second)
	}
	conflict := command
	conflict.RequestedCustody = domain.CustodyCache
	if _, err := svc.Request(context.Background(), conflict); err == nil {
		t.Fatal("idempotency key reuse with changed custody was accepted")
	} else {
		var typed *repository.MediaConflictError
		if !errors.As(err, &typed) {
			t.Fatalf("changed replay error = %T %v", err, err)
		}
	}
}

func TestAssetAcquisitionConcurrentDifferentKeysConvergeOnOneTarget(t *testing.T) {
	st1, repo1, _, asset := acquisitionAssetWithoutOriginal(t)
	defer st1.Close()
	st2, err := store.Open(st1.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	services := []*AssetAcquisitionService{
		NewAssetAcquisitionService(repo1),
		NewAssetAcquisitionService(repository.NewMediaRepository(st2.DB)),
	}
	start := make(chan struct{})
	results := make(chan repository.AssetAcquisitionResult, 8)
	errs := make(chan error, 8)
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result, err := services[index%2].Request(context.Background(), AssetAcquisitionCommand{
				CommandID: "command-" + string(rune('a'+index)), IdempotencyKey: "key-" + string(rune('a'+index)),
				MediaAssetID: asset.ID, VariantKind: domain.VariantOriginal,
				RequestedCustody: domain.CustodyMirror, RequestedBy: "reader-user",
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	wantVariantID := domain.StableAssetVariantID(asset.ID, domain.AcquisitionVariantIdentity(domain.VariantOriginal))
	for result := range results {
		if result.Variant.ID != wantVariantID {
			t.Fatalf("concurrent request selected %q, want %q", result.Variant.ID, wantVariantID)
		}
	}
	var requestCount, variantCount int
	if err := st1.DB.QueryRow(`SELECT COUNT(*) FROM asset_acquisition_requests`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if err := st1.DB.QueryRow(`SELECT COUNT(*) FROM asset_variants WHERE media_asset_id = ? AND kind = 'original'`, asset.ID).Scan(&variantCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 8 || variantCount != 1 {
		t.Fatalf("concurrent commands produced requests=%d variants=%d", requestCount, variantCount)
	}
}

func TestAssetAcquisitionConcurrentSameKeyReplaysOneRequest(t *testing.T) {
	st1, repo1, _, variant := mediaServiceFixture(t, domain.CustodyReference, "")
	defer st1.Close()
	st2, err := store.Open(st1.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	services := []*AssetAcquisitionService{
		NewAssetAcquisitionService(repo1),
		NewAssetAcquisitionService(repository.NewMediaRepository(st2.DB)),
	}
	command := AssetAcquisitionCommand{
		CommandID: "same-command", IdempotencyKey: "same-key", MediaAssetID: variant.MediaAssetID,
		VariantKind: domain.VariantOriginal, RequestedCustody: domain.CustodyMirror,
		RequestedBy: "reader-user",
	}
	start := make(chan struct{})
	results := make(chan repository.AssetAcquisitionResult, 8)
	errs := make(chan error, 8)
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result, err := services[index%2].Request(context.Background(), command)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	created := 0
	for result := range results {
		if result.Request.ID != command.CommandID || result.Variant.ID != variant.ID {
			t.Fatalf("same-key replay diverged: %+v", result)
		}
		if !result.IdempotentReplay {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("same-key concurrency created %d first results, want 1", created)
	}
	var count int
	if err := st1.DB.QueryRow(`SELECT COUNT(*) FROM asset_acquisition_requests WHERE idempotency_key = 'same-key'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("same-key concurrency persisted %d requests", count)
	}
}

func TestAssetAcquisitionCreatesMissingVariantWithoutOrphanAndUsesMediaLifecycle(t *testing.T) {
	st, repo, blobs, asset := acquisitionAssetWithoutOriginal(t)
	defer st.Close()
	svc := NewAssetAcquisitionService(repo)
	result, err := svc.Request(context.Background(), AssetAcquisitionCommand{
		CommandID: "command-create", IdempotencyKey: "key-create", MediaAssetID: asset.ID,
		VariantKind: domain.VariantOriginal, RequestedCustody: domain.CustodyMirror,
		RequestedBy: "reader-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantID := domain.StableAssetVariantID(asset.ID, domain.AcquisitionVariantIdentity(domain.VariantOriginal))
	if result.Variant.ID != wantID || result.Variant.AcquisitionState != domain.AcquisitionPending {
		t.Fatalf("missing original target = %+v", result.Variant)
	}
	media := NewMediaService(repo, blobs)
	stored, err := media.StoreVariantContent(context.Background(), result.Variant.ID, domain.ContentDigest{}, bytes.NewBufferString("future explicitly acquired bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if stored.AcquisitionState != domain.AcquisitionAvailable || stored.BlobDigest == "" {
		t.Fatalf("future worker could not use ordinary media lifecycle: %+v", stored)
	}
}

func acquisitionAssetWithoutOriginal(t *testing.T) (*store.Store, *repository.MediaRepository, *blobstore.FileStore, domain.MediaAsset) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "asset-acquisition.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewMediaRepository(st.DB)
	asset, _, err := repo.UpsertAssetVariants(context.Background(), domain.MediaAsset{
		SourceRegistrationID: "browser-capture", Provider: "youtube", ProviderMediaID: "3RmtNXqnreI",
		SourceMediaKey: "youtube:3RmtNXqnreI", SourceLocator: "https://www.youtube.com/watch?v=3RmtNXqnreI",
		Kind: domain.MediaVideo, SourceAuthority: "youtube", DefaultCustody: domain.CustodyReference,
	}, []domain.AssetVariant{{
		VariantIdentity: "image-candidate:poster", Kind: domain.VariantPoster,
		SourceURL: "https://i.ytimg.com/vi/3RmtNXqnreI/poster.jpg", MIMEType: "image/jpeg",
		Width: 640, Height: 360, Custody: domain.CustodyReference,
		AcquisitionState: domain.AcquisitionReferenceOnly, Retention: domain.RetentionExternal,
	}}, time.Now().UTC())
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	blobs, err := blobstore.NewFileStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, repo, blobs, asset
}

func TestAssetAcquisitionPreservesAvailableBytesAndRejectsCustodyRegression(t *testing.T) {
	payload := "already available"
	st, repo, blobs, variant := mediaServiceFixture(t, domain.CustodyCache, domain.DigestText(payload))
	defer st.Close()
	stored, err := NewMediaService(repo, blobs).StoreVariantContent(context.Background(), variant.ID, domain.ContentDigest{}, bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAssetAcquisitionService(repo)
	result, err := svc.Request(context.Background(), AssetAcquisitionCommand{
		CommandID: "command-available", IdempotencyKey: "key-available", MediaAssetID: variant.MediaAssetID,
		VariantKind: domain.VariantOriginal, RequestedCustody: domain.CustodyMirror,
		RequestedBy: "reader-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variant.AcquisitionState != domain.AcquisitionAvailable || result.Variant.Digest != stored.Digest || result.Variant.BlobDigest != stored.BlobDigest ||
		result.Variant.Custody != domain.CustodyMirror || result.Variant.Retention != domain.RetentionIndefinite {
		t.Fatalf("request degraded available variant: before=%+v after=%+v", stored, result.Variant)
	}
	var blobRetention string
	if err := st.DB.QueryRow(`SELECT retention FROM media_blobs WHERE digest = ?`, stored.BlobDigest).Scan(&blobRetention); err != nil {
		t.Fatal(err)
	}
	if blobRetention != string(domain.RetentionIndefinite) {
		t.Fatalf("cache-to-mirror request left shared blob retention %q", blobRetention)
	}
	_, err = svc.Request(context.Background(), AssetAcquisitionCommand{
		CommandID: "command-downgrade", IdempotencyKey: "key-downgrade", MediaAssetID: variant.MediaAssetID,
		VariantKind: domain.VariantOriginal, RequestedCustody: domain.CustodyCache,
		RequestedBy: "reader-user",
	})
	if err == nil {
		t.Fatal("mirror-to-cache custody regression was accepted")
	}
}

func TestAssetAcquisitionMigrationEnforcesVariantAssetOwnership(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ownership.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Seed both owners directly in this database to isolate the composite FK.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"asset-a", "asset-b"} {
		if _, err := st.DB.Exec(`INSERT INTO media_assets(id, identity_key, source_registration_id, provider, source_media_key, kind, source_authority, default_custody, created_at, updated_at) VALUES (?, ?, 'test', 'web', ?, 'video', 'web', 'reference', ?, ?)`, id, "identity-"+id, id, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB.Exec(`INSERT INTO asset_variants(id, media_asset_id, variant_identity, kind, custody, acquisition_state, retention, created_at, updated_at) VALUES ('variant-a', 'asset-a', 'original', 'original', 'reference', 'reference_only', 'external', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(`INSERT INTO asset_acquisition_requests(id, idempotency_key, semantic_digest, media_asset_id, asset_variant_id, variant_kind, requested_custody, requested_by, created_at) VALUES ('bad', 'bad', 'digest', 'asset-b', 'variant-a', 'original', 'mirror', 'actor', ?)`, now)
	if err == nil {
		t.Fatal("composite ownership FK accepted variant-a under asset-b")
	}
}
