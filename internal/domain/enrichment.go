package domain

import (
	"fmt"
	"strings"
	"time"
)

// EnrichmentCapability is a single independently planned unit of coverage.
// Values intentionally match the canonical Browser Capture and Reader schema.
type EnrichmentCapability string

const (
	CapabilityTitle             EnrichmentCapability = "title"
	CapabilityDescription       EnrichmentCapability = "description"
	CapabilityBody              EnrichmentCapability = "body"
	CapabilityGalleryManifest   EnrichmentCapability = "gallery_manifest"
	CapabilityOriginalMedia     EnrichmentCapability = "original_media"
	CapabilityThumbnailOrPoster EnrichmentCapability = "thumbnail_or_poster"
	CapabilityTranscript        EnrichmentCapability = "transcript"
	CapabilityOCR               EnrichmentCapability = "OCR"
	CapabilityVision            EnrichmentCapability = "vision"
	CapabilitySummary           EnrichmentCapability = "summary"
	CapabilityTags              EnrichmentCapability = "tags"
	CapabilityEntities          EnrichmentCapability = "entities"
)

var enrichmentCapabilities = []EnrichmentCapability{
	CapabilityTitle,
	CapabilityDescription,
	CapabilityBody,
	CapabilityGalleryManifest,
	CapabilityOriginalMedia,
	CapabilityThumbnailOrPoster,
	CapabilityTranscript,
	CapabilityOCR,
	CapabilityVision,
	CapabilitySummary,
	CapabilityTags,
	CapabilityEntities,
}

func AllEnrichmentCapabilities() []EnrichmentCapability {
	return append([]EnrichmentCapability(nil), enrichmentCapabilities...)
}

func (c EnrichmentCapability) Valid() bool {
	for _, candidate := range enrichmentCapabilities {
		if c == candidate {
			return true
		}
	}
	return false
}

type CapabilityState string

const (
	CoverageProvided      CapabilityState = "provided"
	CoverageMissing       CapabilityState = "missing"
	CoveragePending       CapabilityState = "pending"
	CoverageFailed        CapabilityState = "failed"
	CoverageStale         CapabilityState = "stale"
	CoverageNotApplicable CapabilityState = "not_applicable"
)

func (s CapabilityState) Valid() bool {
	switch s {
	case CoverageProvided, CoverageMissing, CoveragePending, CoverageFailed, CoverageStale, CoverageNotApplicable:
		return true
	default:
		return false
	}
}

// ObservationAssetDigest pins a derived observation to the exact physical
// inputs it used. VariantID is retained even when several variants share bytes.
type ObservationAssetDigest struct {
	AssetVariantID string        `json:"asset_variant_id"`
	Digest         ContentDigest `json:"digest"`
}

// EnrichmentObservation is append-only. ValueJSON is a typed result payload
// owned by the producer's declared output schema; it is never executable markup.
type EnrichmentObservation struct {
	ID                  string                   `json:"id"`
	FragmentID          string                   `json:"fragment_id"`
	FragmentRevisionID  string                   `json:"fragment_revision_id"`
	CaptureID           string                   `json:"capture_id,omitempty"`
	Capability          EnrichmentCapability     `json:"capability"`
	Attribution         AttributionSource        `json:"attribution"`
	Producer            AdapterVersion           `json:"producer"`
	InputMaterialDigest string                   `json:"input_material_digest"`
	InputAssetDigests   []ObservationAssetDigest `json:"input_asset_digests"`
	ValueJSON           string                   `json:"value_json"`
	Confidence          *float64                 `json:"confidence,omitempty"`
	ActorID             string                   `json:"actor_id,omitempty"`
	ObservedAt          time.Time                `json:"observed_at"`
	AssertedAt          time.Time                `json:"asserted_at"`
	ExpiresAt           time.Time                `json:"expires_at,omitempty"`
	CreatedAt           time.Time                `json:"created_at"`
}

func (o EnrichmentObservation) Expired(at time.Time) bool {
	return !o.ExpiresAt.IsZero() && !o.ExpiresAt.After(at)
}

// CapabilityCoverage separates work state from display selection. A pending,
// stale, or failed refresh may retain SelectedObservationID so useful source or
// user data never disappears while background work is retried.
type CapabilityCoverage struct {
	FragmentID            string               `json:"fragment_id"`
	FragmentRevisionID    string               `json:"fragment_revision_id"`
	Capability            EnrichmentCapability `json:"capability"`
	State                 CapabilityState      `json:"state"`
	SelectedObservationID string               `json:"selected_observation_id,omitempty"`
	Detail                string               `json:"detail,omitempty"`
	ErrorClass            EnrichmentErrorClass `json:"error_class,omitempty"`
	ErrorCode             string               `json:"error_code,omitempty"`
	ErrorRetryable        bool                 `json:"error_retryable,omitempty"`
	RequestedGeneration   int                  `json:"requested_generation"`
	SatisfiedGeneration   int                  `json:"satisfied_generation"`
	Version               int                  `json:"version"`
	UpdatedAt             time.Time            `json:"updated_at"`
}

func (c CapabilityCoverage) ExplicitRequestPending() bool {
	return c.RequestedGeneration > c.SatisfiedGeneration
}

type EnrichmentErrorClass string

const (
	EnrichmentErrorRetryable EnrichmentErrorClass = "retryable"
	EnrichmentErrorPermanent EnrichmentErrorClass = "permanent"
)

func (c EnrichmentErrorClass) Valid() bool {
	return c == EnrichmentErrorRetryable || c == EnrichmentErrorPermanent
}

type EnrichmentFailure struct {
	Class   EnrichmentErrorClass `json:"class"`
	Code    string               `json:"code"`
	Message string               `json:"message"`
}

func (f EnrichmentFailure) Validate() error {
	if !f.Class.Valid() || strings.TrimSpace(f.Code) == "" || strings.TrimSpace(f.Message) == "" {
		return fmt.Errorf("enrichment failure requires class, code, and message")
	}
	return nil
}

type EnrichmentJobTrigger string

const (
	EnrichmentTriggerCapture  EnrichmentJobTrigger = "capture"
	EnrichmentTriggerMissing  EnrichmentJobTrigger = "missing"
	EnrichmentTriggerRetry    EnrichmentJobTrigger = "retry"
	EnrichmentTriggerStale    EnrichmentJobTrigger = "stale"
	EnrichmentTriggerExplicit EnrichmentJobTrigger = "explicit"
)

func (t EnrichmentJobTrigger) Valid() bool {
	switch t {
	case EnrichmentTriggerCapture, EnrichmentTriggerMissing, EnrichmentTriggerRetry, EnrichmentTriggerStale, EnrichmentTriggerExplicit:
		return true
	default:
		return false
	}
}

type EnrichmentJobStatus string

const (
	EnrichmentJobQueued    EnrichmentJobStatus = "queued"
	EnrichmentJobRunning   EnrichmentJobStatus = "running"
	EnrichmentJobSucceeded EnrichmentJobStatus = "succeeded"
	EnrichmentJobFailed    EnrichmentJobStatus = "failed"
)

type EnrichmentJob struct {
	ID                 string               `json:"id"`
	FragmentID         string               `json:"fragment_id"`
	FragmentRevisionID string               `json:"fragment_revision_id"`
	Capability         EnrichmentCapability `json:"capability"`
	Trigger            EnrichmentJobTrigger `json:"trigger"`
	CoverageVersion    int                  `json:"coverage_version"`
	RequestGeneration  int                  `json:"request_generation"`
	Status             EnrichmentJobStatus  `json:"status"`
	AttemptCount       int                  `json:"attempt_count"`
	ClaimToken         string               `json:"claim_token,omitempty"`
	ClaimedBy          string               `json:"claimed_by,omitempty"`
	LeaseExpiresAt     time.Time            `json:"lease_expires_at,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
}

type EnrichmentJobAttempt struct {
	ID                    string              `json:"id"`
	JobID                 string              `json:"job_id"`
	AttemptNumber         int                 `json:"attempt_number"`
	ClaimToken            string              `json:"claim_token"`
	Adapter               AdapterVersion      `json:"adapter"`
	InputSchemaID         string              `json:"input_schema_id"`
	InputSchemaVersion    string              `json:"input_schema_version"`
	OutputSchemaID        string              `json:"output_schema_id"`
	OutputSchemaVersion   string              `json:"output_schema_version"`
	NetworkClass          string              `json:"network_class"`
	EffectsJSON           string              `json:"effects_json"`
	CredentialRefsJSON    string              `json:"credential_refs_json"`
	DescriptorJSON        string              `json:"descriptor_json"`
	InputMaterialDigest   string              `json:"input_material_digest"`
	InputAssetDigestsJSON string              `json:"input_asset_digests_json"`
	Status                EnrichmentJobStatus `json:"status"`
	Failure               *EnrichmentFailure  `json:"failure,omitempty"`
	ClaimedAt             time.Time           `json:"claimed_at"`
	CompletedAt           time.Time           `json:"completed_at,omitempty"`
}

type EnrichmentClaim struct {
	Job          EnrichmentJob            `json:"job"`
	Attempt      EnrichmentJobAttempt     `json:"attempt"`
	Source       SourceIdentity           `json:"source"`
	Material     FragmentRevision         `json:"material"`
	AssetDigests []ObservationAssetDigest `json:"asset_digests"`
}
