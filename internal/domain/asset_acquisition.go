package domain

import "time"

// AssetAcquisitionRequest is an immutable receipt for an explicit custody
// request. AssetVariant remains the source of truth for acquisition progress.
type AssetAcquisitionRequest struct {
	ID               string           `json:"id"`
	IdempotencyKey   string           `json:"idempotency_key"`
	SemanticDigest   string           `json:"semantic_digest"`
	MediaAssetID     string           `json:"media_asset_id"`
	AssetVariantID   string           `json:"asset_variant_id"`
	VariantKind      AssetVariantKind `json:"variant_kind"`
	RequestedCustody CustodyMode      `json:"requested_custody"`
	RequestedBy      string           `json:"requested_by"`
	Reason           string           `json:"reason,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
}

func AcquisitionVariantIdentity(kind AssetVariantKind) string {
	return "acquisition:" + string(kind)
}
