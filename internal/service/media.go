package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type MediaBlobStore interface {
	Put(context.Context, domain.ContentDigest, io.Reader, domain.RetentionPolicy) (blobstore.PutResult, error)
	Remove(string) error
}

// MediaService composes filesystem custody and SQLite variant state. The DB
// write reservation is held while bytes are atomically installed so a failed
// commit can safely compensate a newly-created file before another variant
// transaction observes it.
type MediaService struct {
	repo   *repository.MediaRepository
	blobs  MediaBlobStore
	commit func(context.Context, *repository.MediaTransaction) error
	now    func() time.Time
}

func NewMediaService(repo *repository.MediaRepository, blobs MediaBlobStore) *MediaService {
	return &MediaService{
		repo: repo, blobs: blobs,
		commit: func(ctx context.Context, tx *repository.MediaTransaction) error { return tx.Commit(ctx) },
		now:    time.Now,
	}
}

func (s *MediaService) StoreVariantContent(ctx context.Context, variantID string, expected domain.ContentDigest, src io.Reader) (domain.AssetVariant, error) {
	return s.StoreVariantContentAtomic(ctx, variantID, expected, src, nil, nil)
}

// StoreVariantContentAtomic lets a higher-level service validate ownership and
// persist related state inside the same SQLite transaction as the blob link.
func (s *MediaService) StoreVariantContentAtomic(ctx context.Context, variantID string, expected domain.ContentDigest, src io.Reader, guard func(context.Context, repository.MediaWriteConn, domain.AssetVariant) error, finalize func(context.Context, repository.MediaWriteConn, domain.AssetVariant) error) (domain.AssetVariant, error) {
	if s == nil || s.repo == nil || s.blobs == nil {
		return domain.AssetVariant{}, fmt.Errorf("store variant content: media repository and blob store are required")
	}
	tx, err := s.repo.BeginImmediate(ctx, "store media variant content")
	if err != nil {
		return domain.AssetVariant{}, err
	}
	active := true
	defer func() {
		if active {
			_ = tx.Rollback()
		}
	}()
	variant, err := s.repo.GetVariantOn(ctx, tx.Conn(), variantID)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	if variant.Custody == domain.CustodyReference {
		return domain.AssetVariant{}, &repository.MediaConflictError{Kind: "asset variant", Identity: variant.ID, Reason: "reference-only custody cannot accept bytes"}
	}
	if guard != nil {
		if err := guard(ctx, tx.Conn(), variant); err != nil {
			return domain.AssetVariant{}, err
		}
	}
	if expected.Empty() {
		expected = variant.ExpectedDigest
	} else {
		expected, err = expected.Normalized()
		if err != nil {
			return domain.AssetVariant{}, fmt.Errorf("store variant content expected %w", err)
		}
		if !variant.ExpectedDigest.Empty() && variant.ExpectedDigest.Value != expected.Value {
			return domain.AssetVariant{}, &repository.MediaConflictError{Kind: "asset variant", Identity: variant.ID, Reason: "request digest conflicts with manifest digest"}
		}
	}
	retention := domain.RetentionIndefinite
	if variant.Custody == domain.CustodyCache {
		retention = domain.RetentionCache
	}
	put, err := s.blobs.Put(ctx, expected, src, retention)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	preexistingRefs, err := s.repo.BlobReferenceCountOn(ctx, tx.Conn(), put.Blob.Digest.Value)
	if err != nil {
		if put.Created {
			_ = s.blobs.Remove(put.Blob.StorageHandle)
		}
		return domain.AssetVariant{}, fmt.Errorf("inspect media blob references: %w", err)
	}
	stored, err := s.repo.AttachBlobOn(ctx, tx.Conn(), variant.ID, put.Blob, s.now().UTC())
	if err != nil {
		if put.Created && preexistingRefs == 0 {
			_ = s.blobs.Remove(put.Blob.StorageHandle)
		}
		active = false
		_ = tx.Rollback()
		return domain.AssetVariant{}, err
	}
	if finalize != nil {
		if err := finalize(ctx, tx.Conn(), stored); err != nil {
			if put.Created && preexistingRefs == 0 {
				_ = s.blobs.Remove(put.Blob.StorageHandle)
			}
			active = false
			_ = tx.Rollback()
			return domain.AssetVariant{}, err
		}
	}
	if err := s.commit(ctx, tx); err != nil {
		if put.Created && preexistingRefs == 0 {
			_ = s.blobs.Remove(put.Blob.StorageHandle)
		}
		active = false
		_ = tx.Rollback()
		return domain.AssetVariant{}, fmt.Errorf("commit stored media variant: %w", err)
	}
	active = false
	return stored, nil
}
func IsDigestMismatch(err error) bool {
	var mismatch *blobstore.DigestMismatchError
	return errors.As(err, &mismatch)
}
