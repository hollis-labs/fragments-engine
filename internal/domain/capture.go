package domain

import "time"

type CaptureCompletion string

const (
	CaptureAccepting    CaptureCompletion = "accepting"
	CaptureTransferring CaptureCompletion = "transferring"
	CaptureComplete     CaptureCompletion = "complete"
	CapturePartial      CaptureCompletion = "partial"
	CaptureFailed       CaptureCompletion = "failed"
)

func (c CaptureCompletion) Valid() bool {
	switch c {
	case CaptureAccepting, CaptureTransferring, CaptureComplete, CapturePartial, CaptureFailed:
		return true
	default:
		return false
	}
}

func (c CaptureCompletion) Terminal() bool {
	return c == CaptureComplete || c == CapturePartial || c == CaptureFailed
}

type CaptureClient struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

// CaptureAttempt is one deliberate capture action. SemanticDigest and the
// accepted outcome fields never change; only Completion and UpdatedAt advance.
type CaptureAttempt struct {
	ID                   string            `json:"id"`
	CaptureID            string            `json:"capture_id"`
	IdempotencyKey       string            `json:"idempotency_key"`
	SemanticDigest       string            `json:"semantic_digest"`
	FragmentID           string            `json:"fragment_id"`
	FragmentRevisionID   string            `json:"fragment_revision_id"`
	FragmentOutcome      string            `json:"fragment_outcome"`
	AcceptedCaptureCount int               `json:"accepted_capture_count"`
	AcceptanceResultJSON string            `json:"acceptance_result_json"`
	PrincipalID          string            `json:"principal_id"`
	ActorID              string            `json:"actor_id"`
	Client               CaptureClient     `json:"client"`
	CapturedAt           time.Time         `json:"captured_at"`
	SubmittedURL         string            `json:"submitted_url,omitempty"`
	PageContextJSON      string            `json:"page_context_json"`
	ExtractionAdapter    AdapterVersion    `json:"extraction_adapter"`
	ExtractionJSON       string            `json:"extraction_json"`
	Completion           CaptureCompletion `json:"completion"`
	WarningsJSON         string            `json:"warnings_json"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
}

type CaptureAnnotationKind string

const (
	CaptureAnnotationHighlight CaptureAnnotationKind = "highlight"
	CaptureAnnotationNote      CaptureAnnotationKind = "capture_note"
)

func (k CaptureAnnotationKind) Valid() bool {
	return k == CaptureAnnotationHighlight || k == CaptureAnnotationNote
}

type TextQuoteSelector struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

type DocumentPosition struct {
	BlockAnchor string `json:"block_anchor,omitempty"`
	StartOffset *int   `json:"start_offset,omitempty"`
	EndOffset   *int   `json:"end_offset,omitempty"`
}

type CaptureAnnotation struct {
	ID         string                `json:"annotation_id"`
	CaptureID  string                `json:"capture_id"`
	FragmentID string                `json:"fragment_id"`
	Kind       CaptureAnnotationKind `json:"kind"`
	Text       string                `json:"text"`
	Selector   *TextQuoteSelector    `json:"selector,omitempty"`
	Position   *DocumentPosition     `json:"position,omitempty"`
	ActorID    string                `json:"actor_id"`
	CapturedAt time.Time             `json:"captured_at"`
	CreatedAt  time.Time             `json:"created_at"`
}

type AttributionSource string

const (
	AttributionSourceMaterial AttributionSource = "source"
	AttributionUser           AttributionSource = "user"
	AttributionProvider       AttributionSource = "provider"
	AttributionDeterministic  AttributionSource = "deterministic"
	AttributionModel          AttributionSource = "model"
)

func (s AttributionSource) ValidDescriptionSource() bool {
	switch s {
	case AttributionSourceMaterial, AttributionUser, AttributionProvider, AttributionDeterministic, AttributionModel:
		return true
	default:
		return false
	}
}

func (s AttributionSource) ValidTagSource() bool {
	return s != AttributionSourceMaterial && s.ValidDescriptionSource()
}

type AttributedTag struct {
	ID              string            `json:"id"`
	FragmentID      string            `json:"fragment_id"`
	Value           string            `json:"value"`
	NormalizedValue string            `json:"normalized_value"`
	Source          AttributionSource `json:"source"`
	ObservationID   string            `json:"observation_id,omitempty"`
	CaptureID       string            `json:"capture_id,omitempty"`
	Producer        AdapterVersion    `json:"producer"`
	ActorID         string            `json:"actor_id,omitempty"`
	ObservedAt      time.Time         `json:"observed_at"`
	CreatedAt       time.Time         `json:"created_at"`
}

type DescriptionObservation struct {
	ID                 string            `json:"id"`
	FragmentID         string            `json:"fragment_id"`
	FragmentRevisionID string            `json:"fragment_revision_id,omitempty"`
	CaptureID          string            `json:"capture_id,omitempty"`
	Value              string            `json:"value"`
	Source             AttributionSource `json:"source"`
	Producer           AdapterVersion    `json:"producer"`
	ActorID            string            `json:"actor_id,omitempty"`
	ObservedAt         time.Time         `json:"observed_at"`
	CreatedAt          time.Time         `json:"created_at"`
}

// CuratedNote is deliberately separate from immutable source material. An
// absent note has optimistic revision zero; the first explicit update is one.
type CuratedNote struct {
	FragmentID   string    `json:"fragment_id"`
	BodyMarkdown string    `json:"body_markdown"`
	Revision     int       `json:"revision"`
	ActorID      string    `json:"actor_id"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CaptureAcceptance struct {
	Fragment         Fragment                 `json:"fragment"`
	ObservedRevision FragmentRevision         `json:"observed_revision"`
	Attempt          CaptureAttempt           `json:"attempt"`
	Annotations      []CaptureAnnotation      `json:"annotations"`
	Tags             []AttributedTag          `json:"tags"`
	Descriptions     []DescriptionObservation `json:"descriptions"`
	Media            []MediaManifestItem      `json:"media"`
	AssetBindings    []CaptureAssetBinding    `json:"asset_bindings"`
	IdempotentReplay bool                     `json:"idempotent_replay"`
}

type AssetInstructionAction string

const (
	AssetRequestUpload AssetInstructionAction = "request_upload"
	AssetReuseBlob     AssetInstructionAction = "reuse_blob"
	AssetServerAcquire AssetInstructionAction = "server_acquire"
	AssetReferenceOnly AssetInstructionAction = "reference_only"
	AssetRejected      AssetInstructionAction = "rejected"
)

func (a AssetInstructionAction) Valid() bool {
	switch a {
	case AssetRequestUpload, AssetReuseBlob, AssetServerAcquire, AssetReferenceOnly, AssetRejected:
		return true
	default:
		return false
	}
}

// CaptureAssetBinding pins a client correlation ID to the resolved immutable
// revision and physical variant. MediaPosition and VariantIdentity are write-
// time resolution hints and are not persisted.
type CaptureAssetBinding struct {
	CaptureID          string                 `json:"capture_id"`
	FragmentRevisionID string                 `json:"fragment_revision_id"`
	ClientVariantID    string                 `json:"client_variant_id"`
	AssetVariantID     string                 `json:"asset_variant_id"`
	Action             AssetInstructionAction `json:"action"`
	ExpectedDigest     ContentDigest          `json:"expected_digest,omitempty"`
	Digest             ContentDigest          `json:"digest,omitempty"`
	Reason             string                 `json:"reason,omitempty"`
	MediaPosition      int                    `json:"-"`
	VariantIdentity    string                 `json:"-"`
	CreatedAt          time.Time              `json:"created_at"`
}

type CaptureAssetOutcome string

const (
	AssetOutcomeUploaded         CaptureAssetOutcome = "uploaded"
	AssetOutcomeAlreadyAvailable CaptureAssetOutcome = "already_available"
	AssetOutcomeNotAvailable     CaptureAssetOutcome = "not_available"
	AssetOutcomeFailed           CaptureAssetOutcome = "failed"
	AssetOutcomeDeferred         CaptureAssetOutcome = "deferred"
)

func (o CaptureAssetOutcome) Valid() bool {
	switch o {
	case AssetOutcomeUploaded, AssetOutcomeAlreadyAvailable, AssetOutcomeNotAvailable, AssetOutcomeFailed, AssetOutcomeDeferred:
		return true
	default:
		return false
	}
}

type CaptureVariantOutcome struct {
	CaptureID       string              `json:"capture_id"`
	ClientVariantID string              `json:"client_variant_id"`
	Outcome         CaptureAssetOutcome `json:"outcome"`
	Digest          ContentDigest       `json:"digest,omitempty"`
	ByteSize        int64               `json:"byte_size,omitempty"`
	Reason          string              `json:"reason,omitempty"`
	Retryable       bool                `json:"retryable,omitempty"`
	UpdatedAt       time.Time           `json:"updated_at"`
}
