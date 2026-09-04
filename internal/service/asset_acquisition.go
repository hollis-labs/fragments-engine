package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type AssetAcquisitionCommand struct {
	CommandID        string
	IdempotencyKey   string
	MediaAssetID     string
	VariantKind      domain.AssetVariantKind
	RequestedCustody domain.CustodyMode
	RequestedBy      string
	Reason           string
}

type AssetAcquisitionService struct {
	repository *repository.MediaRepository
	now        func() time.Time
}

func NewAssetAcquisitionService(repo *repository.MediaRepository) *AssetAcquisitionService {
	return &AssetAcquisitionService{repository: repo, now: time.Now}
}

// Request records an idempotent semantic command and only advances the target
// variant to pending custody. A future policy-aware worker performs any actual
// network acquisition and completes the existing MediaService lifecycle.
func (s *AssetAcquisitionService) Request(ctx context.Context, command AssetAcquisitionCommand) (repository.AssetAcquisitionResult, error) {
	if s == nil || s.repository == nil {
		return repository.AssetAcquisitionResult{}, fmt.Errorf("request asset acquisition: media repository is required")
	}
	command.CommandID = strings.TrimSpace(command.CommandID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.MediaAssetID = strings.TrimSpace(command.MediaAssetID)
	command.RequestedBy = strings.TrimSpace(command.RequestedBy)
	command.Reason = strings.TrimSpace(command.Reason)
	semanticMaterial, err := json.Marshal(struct {
		MediaAssetID     string                  `json:"media_asset_id"`
		VariantKind      domain.AssetVariantKind `json:"variant_kind"`
		RequestedCustody domain.CustodyMode      `json:"requested_custody"`
		RequestedBy      string                  `json:"requested_by"`
		Reason           string                  `json:"reason"`
	}{command.MediaAssetID, command.VariantKind, command.RequestedCustody, command.RequestedBy, command.Reason})
	if err != nil {
		return repository.AssetAcquisitionResult{}, fmt.Errorf("request asset acquisition: encode command semantics: %w", err)
	}
	semanticDigest := domain.DigestText(string(semanticMaterial))
	return s.repository.RequestAssetAcquisition(ctx, repository.AssetAcquisitionWrite{
		Request: domain.AssetAcquisitionRequest{
			ID: command.CommandID, IdempotencyKey: command.IdempotencyKey,
			SemanticDigest: semanticDigest, MediaAssetID: command.MediaAssetID,
			VariantKind: command.VariantKind, RequestedCustody: command.RequestedCustody,
			RequestedBy: command.RequestedBy, Reason: command.Reason, CreatedAt: s.now().UTC(),
		},
	})
}
