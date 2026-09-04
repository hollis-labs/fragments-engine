package service

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

// ProviderMediaCompletion persists one provider-acquired asset representation
// through FE's generic media lifecycle. It deliberately does not publish an
// enrichment observation: the claiming worker must publish only after this
// method succeeds, through EnrichmentService.CompleteSuccess and its claim
// fence.
type ProviderMediaCompletion struct {
	media *repository.MediaRepository
	blobs *MediaService
	now   func() time.Time
}

func NewProviderMediaCompletion(media *repository.MediaRepository, blobs *MediaService) *ProviderMediaCompletion {
	return &ProviderMediaCompletion{media: media, blobs: blobs, now: time.Now}
}

// Complete creates or resolves the logical asset before inserting its
// independently acquired variant, so transcript-first and poster-first
// provider execution do not depend on original-media completion order.
func (s *ProviderMediaCompletion) Complete(ctx context.Context, asset domain.MediaAsset, variant domain.AssetVariant, content io.Reader) (domain.AssetVariant, error) {
	if s == nil || s.media == nil || s.blobs == nil {
		return domain.AssetVariant{}, fmt.Errorf("complete provider media: media repository and media service are required")
	}
	if variant.Custody == domain.CustodyReference && content != nil {
		return domain.AssetVariant{}, fmt.Errorf("complete provider media: reference custody cannot store content")
	}
	if variant.Custody != domain.CustodyReference && content == nil {
		return domain.AssetVariant{}, fmt.Errorf("complete provider media: managed custody requires content")
	}
	_, variants, err := s.media.UpsertAssetVariants(ctx, asset, []domain.AssetVariant{variant}, s.now().UTC())
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("persist provider media variant: %w", err)
	}
	if len(variants) != 1 {
		return domain.AssetVariant{}, fmt.Errorf("persist provider media variant: expected one resolved variant")
	}
	resolved := variants[0]
	if resolved.Custody == domain.CustodyReference {
		return resolved, nil
	}
	resolved, err = s.blobs.StoreVariantContent(ctx, resolved.ID, resolved.ExpectedDigest, content)
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("store provider media bytes: %w", err)
	}
	return resolved, nil
}
