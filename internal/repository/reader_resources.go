package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// ReaderRevisionResource is an immutable source revision after resolving a
// possibly historical fragment alias to its canonical fragment.
type ReaderRevisionResource struct {
	CanonicalFragmentID string
	Revision            domain.FragmentRevision
}

// ReaderVariantResource is a variant whose logical asset is attached to one
// exact immutable revision. Blob is nil for partial and compatibility states
// that do not own content-addressed bytes.
type ReaderVariantResource struct {
	CanonicalFragmentID string
	RevisionID          string
	Asset               domain.MediaAsset
	Variant             domain.AssetVariant
	Blob                *domain.MediaBlob
}

// ReaderResourceRepository performs the ownership checks needed before any
// Reader content is opened. Callers never authorize a variant by its ID alone.
type ReaderResourceRepository struct {
	db *sql.DB
}

func NewReaderResourceRepository(db *sql.DB) *ReaderResourceRepository {
	return &ReaderResourceRepository{db: db}
}

func (r *ReaderResourceRepository) GetRevision(ctx context.Context, fragmentID, revisionID string) (ReaderRevisionResource, error) {
	if r == nil || r.db == nil {
		return ReaderRevisionResource{}, fmt.Errorf("get reader revision: database is required")
	}
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, strings.TrimSpace(fragmentID))
	if err != nil {
		return ReaderRevisionResource{}, fmt.Errorf("get reader revision owner: %w", err)
	}
	revision, err := NewFragmentRepository(r.db).GetRevision(ctx, strings.TrimSpace(revisionID))
	if err != nil {
		return ReaderRevisionResource{}, err
	}
	if revision.FragmentID != canonicalID {
		return ReaderRevisionResource{}, sql.ErrNoRows
	}
	return ReaderRevisionResource{CanonicalFragmentID: canonicalID, Revision: revision}, nil
}

func (r *ReaderResourceRepository) GetVariant(ctx context.Context, fragmentID, revisionID, variantID string) (ReaderVariantResource, error) {
	if r == nil || r.db == nil {
		return ReaderVariantResource{}, fmt.Errorf("get reader media: database is required")
	}
	ownedRevision, err := r.GetRevision(ctx, fragmentID, revisionID)
	if err != nil {
		return ReaderVariantResource{}, err
	}
	variantID = strings.TrimSpace(variantID)
	var assetID string
	err = r.db.QueryRowContext(ctx, `
SELECT v.media_asset_id
FROM asset_variants v
WHERE v.id = ?
  AND EXISTS (
    SELECT 1 FROM attachment_refs ar
    WHERE ar.fragment_revision_id = ?
      AND ar.media_asset_id = v.media_asset_id
  )`, variantID, ownedRevision.Revision.ID).Scan(&assetID)
	if err != nil {
		return ReaderVariantResource{}, fmt.Errorf("get reader media ownership: %w", err)
	}
	media := NewMediaRepository(r.db)
	variant, err := media.GetVariant(ctx, variantID)
	if err != nil {
		return ReaderVariantResource{}, err
	}
	asset, err := loadMediaAsset(ctx, r.db, assetID)
	if err != nil {
		return ReaderVariantResource{}, err
	}
	var blob *domain.MediaBlob
	if variant.BlobDigest != "" {
		stored, found, err := media.FindBlobByDigestOn(ctx, r.db, variant.BlobDigest)
		if err != nil {
			return ReaderVariantResource{}, fmt.Errorf("get reader media blob: %w", err)
		}
		if !found {
			return ReaderVariantResource{}, fmt.Errorf("get reader media blob: %w", sql.ErrNoRows)
		}
		blob = &stored
	}
	return ReaderVariantResource{
		CanonicalFragmentID: ownedRevision.CanonicalFragmentID,
		RevisionID:          ownedRevision.Revision.ID,
		Asset:               asset,
		Variant:             variant,
		Blob:                blob,
	}, nil
}
