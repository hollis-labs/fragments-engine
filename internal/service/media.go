package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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
	// Keep the first bytes for the serve check below, then hand the whole
	// stream to the blob store.
	sample := make([]byte, readerMediaSniffBytes)
	sampled, err := io.ReadFull(src, sample)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return domain.AssetVariant{}, fmt.Errorf("read media variant content: %w", err)
	}
	sample = sample[:sampled]
	src = io.MultiReader(bytes.NewReader(sample), src)
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
	stored, err = s.recordServeCheck(ctx, tx.Conn(), stored, sample)
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

// recordServeCheck decides, from the first bytes of a stored variant, whether
// the Reader media route will serve it, and records the answer so the Reader
// projection can tell a client before it asks. The rule is the media route's
// own (safeReaderMediaType); nothing is served that it would refuse.
func (s *MediaService) recordServeCheck(ctx context.Context, q repository.MediaWriteConn, variant domain.AssetVariant, sample []byte) (domain.AssetVariant, error) {
	asset, err := s.repo.GetAssetOn(ctx, q, variant.MediaAssetID)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	served, reason := readerServeDecision(asset, variant, sample)
	return s.repo.RecordServeCheckOn(ctx, q, variant, served, reason, s.now().UTC())
}

// readerServeDecision returns the type the media route would serve the bytes
// as, or an empty type and the reason it would refuse them. The reason names
// what the bytes look like, so an unserved file can be told apart from a
// wrong declaration without opening it.
func readerServeDecision(asset domain.MediaAsset, variant domain.AssetVariant, sample []byte) (served, reason string) {
	served, err := safeReaderMediaType(asset, variant, sample)
	if err == nil {
		return served, ""
	}
	sniffed := normalizedMediaType(http.DetectContentType(sample))
	return "", fmt.Sprintf("the Reader does not serve this file: %v (the bytes look like %s)", err, sniffed)
}

// ReconcileServeChecks runs the serve check over variants stored before the
// check existed. It makes one pass and returns how many variants it checked
// and how many of those the Reader will not serve. A variant whose blob
// cannot be opened is left unchecked.
func (s *MediaService) ReconcileServeChecks(ctx context.Context) (checked, unserved int, err error) {
	if s == nil || s.repo == nil {
		return 0, 0, fmt.Errorf("reconcile media serve checks: media repository is required")
	}
	opener, ok := s.blobs.(interface {
		Open(string) (*os.File, error)
	})
	if !ok {
		return 0, 0, fmt.Errorf("reconcile media serve checks: blob store cannot open blobs")
	}
	after := ""
	for {
		batch, err := s.repo.ListUncheckedServeVariants(ctx, after, 200)
		if err != nil {
			return checked, unserved, err
		}
		if len(batch) == 0 {
			return checked, unserved, nil
		}
		for _, item := range batch {
			after = item.Variant.ID
			if err := ctx.Err(); err != nil {
				return checked, unserved, err
			}
			file, err := opener.Open(item.StorageHandle)
			if err != nil {
				log.Printf("media serve check: variant %s: blob cannot be opened", item.Variant.ID)
				continue
			}
			sample := make([]byte, readerMediaSniffBytes)
			n, readErr := io.ReadFull(file, sample)
			_ = file.Close()
			if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
				log.Printf("media serve check: variant %s: blob cannot be read", item.Variant.ID)
				continue
			}
			served, reason := readerServeDecision(item.Asset, item.Variant, sample[:n])
			if _, err := s.repo.RecordServeCheck(ctx, item.Variant, served, reason, s.now().UTC()); err != nil {
				return checked, unserved, err
			}
			checked++
			if served == "" {
				unserved++
			}
		}
	}
}

// checkStoredMediaOn checks deduplicated capture blobs under the acceptance
// write reservation, including blobs reused with a different MIME declaration.
func (s *MediaService) checkStoredMediaOn(ctx context.Context, q repository.MediaWriteConn, items []domain.MediaManifestItem) error {
	opener, ok := s.blobs.(interface {
		Open(string) (*os.File, error)
	})
	for _, item := range items {
		for _, variant := range item.Variants {
			if variant.BlobDigest == "" || variant.AcquisitionState != domain.AcquisitionAvailable {
				continue
			}
			if !ok {
				return fmt.Errorf("capture media: blob store cannot open blobs")
			}
			blob, found, err := s.repo.FindBlobByDigestOn(ctx, q, variant.BlobDigest)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("capture media: stored blob missing")
			}
			file, err := opener.Open(blob.StorageHandle)
			if err != nil {
				return err
			}
			sample := make([]byte, readerMediaSniffBytes)
			n, err := io.ReadFull(file, sample)
			file.Close()
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return err
			}
			if _, err := s.recordServeCheck(ctx, q, variant, sample[:n]); err != nil {
				return err
			}
		}
	}
	return nil
}
