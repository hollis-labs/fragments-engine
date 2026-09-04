package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type AssetAcquisitionWrite struct {
	Request domain.AssetAcquisitionRequest
}

type AssetAcquisitionResult struct {
	Request          domain.AssetAcquisitionRequest
	Variant          domain.AssetVariant
	IdempotentReplay bool
}

// RequestAssetAcquisition records one explicit custody command and advances a
// single existing variant through the ordinary media lifecycle. It never
// downloads bytes. If no representation of the requested kind exists, it
// creates one deterministic, asset-owned pending variant for a future worker.
func (r *MediaRepository) RequestAssetAcquisition(ctx context.Context, write AssetAcquisitionWrite) (AssetAcquisitionResult, error) {
	request := write.Request
	request.ID = strings.TrimSpace(request.ID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.SemanticDigest = strings.TrimSpace(request.SemanticDigest)
	request.MediaAssetID = strings.TrimSpace(request.MediaAssetID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.ID == "" || request.IdempotencyKey == "" || request.SemanticDigest == "" ||
		request.MediaAssetID == "" || request.RequestedBy == "" || request.CreatedAt.IsZero() {
		return AssetAcquisitionResult{}, fmt.Errorf("request asset acquisition: command, idempotency, semantic digest, asset, actor, and timestamp are required")
	}
	if !request.VariantKind.Valid() {
		return AssetAcquisitionResult{}, fmt.Errorf("request asset acquisition: invalid variant kind %q", request.VariantKind)
	}
	if request.RequestedCustody != domain.CustodyCache && request.RequestedCustody != domain.CustodyMirror && request.RequestedCustody != domain.CustodyAdopted {
		return AssetAcquisitionResult{}, fmt.Errorf("request asset acquisition: custody must be cache, mirror, or adopted")
	}

	tx, err := r.BeginImmediate(ctx, "request asset acquisition")
	if err != nil {
		return AssetAcquisitionResult{}, err
	}
	defer tx.Rollback()
	q := tx.Conn()

	byID, idFound, err := loadAssetAcquisitionRequest(ctx, q, "id", request.ID)
	if err != nil {
		return AssetAcquisitionResult{}, err
	}
	byKey, keyFound, err := loadAssetAcquisitionRequest(ctx, q, "idempotency_key", request.IdempotencyKey)
	if err != nil {
		return AssetAcquisitionResult{}, err
	}
	if idFound || keyFound {
		existing := byID
		if !idFound {
			existing = byKey
		}
		if idFound && keyFound && byID.ID != byKey.ID {
			return AssetAcquisitionResult{}, &MediaConflictError{Kind: "asset acquisition", Identity: request.ID, Reason: "command ID and idempotency key identify different requests"}
		}
		if existing.ID != request.ID || existing.IdempotencyKey != request.IdempotencyKey ||
			existing.SemanticDigest != request.SemanticDigest || existing.MediaAssetID != request.MediaAssetID ||
			existing.VariantKind != request.VariantKind || existing.RequestedCustody != request.RequestedCustody ||
			existing.RequestedBy != request.RequestedBy || existing.Reason != request.Reason {
			return AssetAcquisitionResult{}, &MediaConflictError{Kind: "asset acquisition", Identity: request.IdempotencyKey, Reason: "command or idempotency identity was reused with different semantics"}
		}
		variant, err := loadAssetVariant(ctx, q, existing.AssetVariantID)
		if err != nil {
			return AssetAcquisitionResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return AssetAcquisitionResult{}, fmt.Errorf("commit asset acquisition replay: %w", err)
		}
		return AssetAcquisitionResult{Request: existing, Variant: variant, IdempotentReplay: true}, nil
	}

	if _, err := loadMediaAsset(ctx, q, request.MediaAssetID); err != nil {
		return AssetAcquisitionResult{}, fmt.Errorf("request asset acquisition: %w", err)
	}
	variant, err := resolveAcquisitionVariant(ctx, q, request.MediaAssetID, request.VariantKind, request.RequestedCustody, request.CreatedAt)
	if err != nil {
		return AssetAcquisitionResult{}, err
	}
	request.AssetVariantID = variant.ID
	if _, err := q.ExecContext(ctx, `
INSERT INTO asset_acquisition_requests (
  id, idempotency_key, semantic_digest, media_asset_id, asset_variant_id,
  variant_kind, requested_custody, requested_by, reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.ID, request.IdempotencyKey,
		request.SemanticDigest, request.MediaAssetID, request.AssetVariantID,
		string(request.VariantKind), string(request.RequestedCustody),
		request.RequestedBy, request.Reason, formatTime(request.CreatedAt)); err != nil {
		return AssetAcquisitionResult{}, fmt.Errorf("record asset acquisition request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AssetAcquisitionResult{}, fmt.Errorf("commit asset acquisition request: %w", err)
	}
	return AssetAcquisitionResult{Request: request, Variant: variant}, nil
}

func resolveAcquisitionVariant(ctx context.Context, q MediaWriteConn, assetID string, kind domain.AssetVariantKind, requested domain.CustodyMode, now time.Time) (domain.AssetVariant, error) {
	rows, err := q.QueryContext(ctx, `
SELECT id, variant_identity FROM asset_variants
WHERE media_asset_id = ? AND kind = ? ORDER BY created_at, id`, assetID, string(kind))
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("list acquisition variants: %w", err)
	}
	type candidate struct{ id, identity string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.identity); err != nil {
			rows.Close()
			return domain.AssetVariant{}, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return domain.AssetVariant{}, err
	}
	if len(candidates) == 0 {
		return upsertAssetVariant(ctx, q, domain.AssetVariant{
			MediaAssetID: assetID, VariantIdentity: domain.AcquisitionVariantIdentity(kind), Kind: kind,
			Custody: requested, AcquisitionState: domain.AcquisitionPending,
			Retention: retentionForRequestedCustody(requested),
		}, now)
	}
	selected := candidates[0]
	if len(candidates) > 1 {
		selected = candidate{}
		for _, item := range candidates {
			if item.identity == domain.AcquisitionVariantIdentity(kind) {
				if selected.id != "" {
					return domain.AssetVariant{}, &MediaConflictError{Kind: "asset acquisition", Identity: assetID, Reason: "multiple acquisition variants have the requested kind"}
				}
				selected = item
			}
		}
		if selected.id == "" {
			return domain.AssetVariant{}, &MediaConflictError{Kind: "asset acquisition", Identity: assetID, Reason: "variant kind is ambiguous; an explicit variant identity is required"}
		}
	}
	variant, err := loadAssetVariant(ctx, q, selected.id)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	if err := validateCustodyTransition(variant.Custody, requested); err != nil {
		return domain.AssetVariant{}, &MediaConflictError{Kind: "asset acquisition", Identity: variant.ID, Reason: err.Error()}
	}
	nextState := variant.AcquisitionState
	if nextState != domain.AcquisitionAvailable {
		nextState = domain.AcquisitionPending
	}
	retention := retentionForRequestedCustody(requested)
	if _, err := q.ExecContext(ctx, `
UPDATE asset_variants
SET custody = ?, acquisition_state = ?, retention = ?,
    failure_code = '', failure_message = '', failure_retryable = 0,
    updated_at = ?
WHERE id = ? AND media_asset_id = ?`, string(requested), string(nextState),
		string(retention), formatTime(now), variant.ID, assetID); err != nil {
		return domain.AssetVariant{}, fmt.Errorf("advance asset variant acquisition: %w", err)
	}
	if variant.BlobDigest != "" && retention == domain.RetentionIndefinite {
		if _, err := q.ExecContext(ctx, `UPDATE media_blobs SET retention = 'indefinite' WHERE digest = ?`, variant.BlobDigest); err != nil {
			return domain.AssetVariant{}, fmt.Errorf("retain requested media blob: %w", err)
		}
	}
	return loadAssetVariant(ctx, q, variant.ID)
}

func validateCustodyTransition(current, requested domain.CustodyMode) error {
	if current == requested || current == domain.CustodyReference {
		return nil
	}
	if current == domain.CustodyCache && requested == domain.CustodyMirror {
		return nil
	}
	return fmt.Errorf("custody transition from %s to %s is not allowed", current, requested)
}

func retentionForRequestedCustody(custody domain.CustodyMode) domain.RetentionPolicy {
	if custody == domain.CustodyCache {
		return domain.RetentionCache
	}
	return domain.RetentionIndefinite
}

func loadAssetAcquisitionRequest(ctx context.Context, q MediaWriteConn, column, value string) (domain.AssetAcquisitionRequest, bool, error) {
	if column != "id" && column != "idempotency_key" {
		return domain.AssetAcquisitionRequest{}, false, fmt.Errorf("invalid asset acquisition lookup")
	}
	var item domain.AssetAcquisitionRequest
	var kind, custody, createdAt string
	err := q.QueryRowContext(ctx, `
SELECT id, idempotency_key, semantic_digest, media_asset_id, asset_variant_id,
       variant_kind, requested_custody, requested_by, reason, created_at
FROM asset_acquisition_requests WHERE `+column+` = ?`, value).Scan(
		&item.ID, &item.IdempotencyKey, &item.SemanticDigest, &item.MediaAssetID,
		&item.AssetVariantID, &kind, &custody, &item.RequestedBy, &item.Reason,
		&createdAt)
	if err == sql.ErrNoRows {
		return domain.AssetAcquisitionRequest{}, false, nil
	}
	if err != nil {
		return domain.AssetAcquisitionRequest{}, false, fmt.Errorf("load asset acquisition request: %w", err)
	}
	item.VariantKind = domain.AssetVariantKind(kind)
	item.RequestedCustody = domain.CustodyMode(custody)
	item.CreatedAt = parseTime(createdAt)
	return item, true, nil
}
