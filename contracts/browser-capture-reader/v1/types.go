// Package capturecontract contains transport-edge types bound to the canonical
// Browser Capture and Reader JSON Schemas in this directory. The JSON Schemas,
// not these Go declarations, are the language-neutral source of truth.
package capturecontract

import "time"

const (
	CaptureVersion           = "fe.capture.v1"
	CaptureResultVersion     = "fe.capture.result.v1"
	CaptureCompletionVersion = "fe.capture.completion.v1"
	CaptureStatusVersion     = "fe.capture.status.v1"
	ReaderItemVersion        = "fe.reader.item.v1"
	ReaderListVersion        = "fe.reader.list.v1"
	ReaderCommandVersion     = "fe.reader.command.v1"
	ReaderContextVersion     = "fe.reader.context.v1"
	ConversationRefVersion   = "fe.reader.conversation-ref.v1"
	CapabilitiesVersion      = "fe.capabilities.v1"
)

type ProviderExtensions map[string]map[string]any

type ClientInfo struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

type Canonicalizer struct {
	Adapter string `json:"adapter"`
	Version string `json:"version"`
}

type SourceIdentity struct {
	SourceRegistrationID string         `json:"source_registration_id,omitempty"`
	SubmittedURL         string         `json:"submitted_url,omitempty"`
	CanonicalURL         string         `json:"canonical_url,omitempty"`
	Provider             string         `json:"provider"`
	ProviderItemID       string         `json:"provider_item_id,omitempty"`
	SourceItemKey        string         `json:"source_item_key"`
	SourceLocator        string         `json:"source_locator,omitempty"`
	SegmentKey           string         `json:"segment_key"`
	Canonicalizer        *Canonicalizer `json:"canonicalizer,omitempty"`
}

type DocumentContent struct {
	Format string `json:"format"`
	Body   string `json:"body"`
}

type DocumentObservation struct {
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	Byline      string             `json:"byline,omitempty"`
	PublishedAt *time.Time         `json:"published_at,omitempty"`
	Language    string             `json:"language,omitempty"`
	Content     *DocumentContent   `json:"content,omitempty"`
	Extensions  ProviderExtensions `json:"extensions,omitempty"`
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
	AnnotationID string             `json:"annotation_id"`
	Kind         string             `json:"kind"`
	Text         string             `json:"text"`
	Selector     *TextQuoteSelector `json:"selector,omitempty"`
	Position     *DocumentPosition  `json:"position,omitempty"`
}

type ContractWarning struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	SubjectID string `json:"subject_id,omitempty"`
}

type Digest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

type CaptureAssetVariant struct {
	ClientVariantID    string             `json:"client_variant_id"`
	Kind               string             `json:"kind"`
	SourceURL          string             `json:"source_url,omitempty"`
	SourceExpiresAt    *time.Time         `json:"source_expires_at,omitempty"`
	MIMEType           string             `json:"mime_type,omitempty"`
	Width              *int               `json:"width,omitempty"`
	Height             *int               `json:"height,omitempty"`
	DurationSeconds    *float64           `json:"duration_seconds,omitempty"`
	ByteSize           *int64             `json:"byte_size,omitempty"`
	Digest             *Digest            `json:"digest,omitempty"`
	TransferPreference string             `json:"transfer_preference"`
	RequestedCustody   string             `json:"requested_custody"`
	Extensions         ProviderExtensions `json:"extensions,omitempty"`
}

type CaptureMediaItem struct {
	ClientMediaID   string                `json:"client_media_id"`
	ProviderMediaID string                `json:"provider_media_id,omitempty"`
	Kind            string                `json:"kind"`
	Role            string                `json:"role"`
	Position        int                   `json:"position"`
	SourceLocator   string                `json:"source_locator,omitempty"`
	Width           *int                  `json:"width,omitempty"`
	Height          *int                  `json:"height,omitempty"`
	DurationSeconds *float64              `json:"duration_seconds,omitempty"`
	PageCount       *int                  `json:"page_count,omitempty"`
	AltText         string                `json:"alt_text,omitempty"`
	Caption         string                `json:"caption,omitempty"`
	SourceContext   string                `json:"source_context,omitempty"`
	DefaultCustody  string                `json:"default_custody,omitempty"`
	Variants        []CaptureAssetVariant `json:"variants"`
	Extensions      ProviderExtensions    `json:"extensions,omitempty"`
}

type ExtractionReport struct {
	Adapter              string             `json:"adapter"`
	AdapterVersion       string             `json:"adapter_version"`
	ObservedCapabilities []string           `json:"observed_capabilities"`
	Warnings             []ContractWarning  `json:"warnings"`
	Extensions           ProviderExtensions `json:"extensions,omitempty"`
}

type PageContext struct {
	DocumentURL      string `json:"document_url,omitempty"`
	DocumentTitle    string `json:"document_title,omitempty"`
	SelectionPresent *bool  `json:"selection_present,omitempty"`
}

type CaptureEnvelope struct {
	SchemaVersion string              `json:"schema_version"`
	CaptureID     string              `json:"capture_id"`
	CapturedAt    time.Time           `json:"captured_at"`
	PrincipalID   string              `json:"principal_id"`
	Client        ClientInfo          `json:"client"`
	Source        SourceIdentity      `json:"source"`
	Document      DocumentObservation `json:"document"`
	Tags          []string            `json:"tags"`
	Annotations   []CaptureAnnotation `json:"annotations"`
	Media         []CaptureMediaItem  `json:"media"`
	Extraction    ExtractionReport    `json:"extraction"`
	PageContext   *PageContext        `json:"page_context,omitempty"`
	Extensions    ProviderExtensions  `json:"extensions,omitempty"`
}

type AssetInstruction struct {
	ClientVariantID string  `json:"client_variant_id"`
	AssetVariantID  string  `json:"asset_variant_id,omitempty"`
	Action          string  `json:"action"`
	UploadHref      string  `json:"upload_href,omitempty"`
	ExpectedDigest  *Digest `json:"expected_digest,omitempty"`
	Digest          *Digest `json:"digest,omitempty"`
	Reason          string  `json:"reason,omitempty"`
}

type AssetOutcome struct {
	ClientVariantID string  `json:"client_variant_id"`
	Outcome         string  `json:"outcome"`
	Digest          *Digest `json:"digest,omitempty"`
	ByteSize        *int64  `json:"byte_size,omitempty"`
	Reason          string  `json:"reason,omitempty"`
	Retryable       *bool   `json:"retryable,omitempty"`
}

type CaptureCompletionRequest struct {
	SchemaVersion  string            `json:"schema_version"`
	IdempotencyKey string            `json:"idempotency_key"`
	Assets         []AssetOutcome    `json:"assets"`
	Warnings       []ContractWarning `json:"warnings"`
}

type CaptureStatus struct {
	SchemaVersion        string            `json:"schema_version"`
	CaptureID            string            `json:"capture_id"`
	FragmentID           string            `json:"fragment_id"`
	FragmentRevisionID   string            `json:"fragment_revision_id"`
	CaptureAttemptID     string            `json:"capture_attempt_id"`
	Completion           string            `json:"completion"`
	Assets               []AssetOutcome    `json:"assets"`
	EnrichmentInProgress bool              `json:"enrichment_in_progress"`
	Warnings             []ContractWarning `json:"warnings"`
}

type CaptureManifestResponse struct {
	SchemaVersion      string             `json:"schema_version"`
	CaptureID          string             `json:"capture_id"`
	FragmentID         string             `json:"fragment_id"`
	FragmentRevisionID string             `json:"fragment_revision_id"`
	CaptureAttemptID   string             `json:"capture_attempt_id"`
	Completion         string             `json:"completion"`
	IdempotentReplay   bool               `json:"idempotent_replay"`
	AssetInstructions  []AssetInstruction `json:"asset_instructions"`
	Warnings           []ContractWarning  `json:"warnings"`
	ReaderItem         ReaderItem         `json:"reader_item"`
}

type PlaybackSpec struct {
	Kind           string   `json:"kind"`
	Provider       string   `json:"provider,omitempty"`
	ProviderItemID string   `json:"provider_item_id,omitempty"`
	AssetVariantID string   `json:"asset_variant_id,omitempty"`
	URL            string   `json:"url,omitempty"`
	MIMEType       string   `json:"mime_type,omitempty"`
	Policy         string   `json:"policy,omitempty"`
	StartSeconds   *float64 `json:"start_seconds,omitempty"`
}

type ReadingPosition struct {
	Kind            string   `json:"kind"`
	Progress        *float64 `json:"progress,omitempty"`
	BlockAnchor     string   `json:"block_anchor,omitempty"`
	LocalOffset     *int     `json:"local_offset,omitempty"`
	ElapsedSeconds  *float64 `json:"elapsed_seconds,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	ProviderMediaID string   `json:"provider_media_id,omitempty"`
	AttachmentID    string   `json:"attachment_id,omitempty"`
	Index           *int     `json:"index,omitempty"`
	Page            *int     `json:"page,omitempty"`
}

type ReadingState struct {
	PrincipalID  string          `json:"principal_id"`
	FragmentID   string          `json:"fragment_id"`
	State        string          `json:"state"`
	Position     ReadingPosition `json:"position"`
	LastOpenedAt *time.Time      `json:"last_opened_at,omitempty"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	Revision     int             `json:"revision"`
}

type AttachmentRef struct {
	AttachmentID       string `json:"attachment_id"`
	FragmentRevisionID string `json:"fragment_revision_id"`
	MediaAssetID       string `json:"media_asset_id"`
	Role               string `json:"role"`
	Position           int    `json:"position"`
	Caption            string `json:"caption,omitempty"`
	SourceContext      string `json:"source_context,omitempty"`
}

type AssetFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type ReaderAssetVariant struct {
	AssetVariantID   string        `json:"asset_variant_id"`
	Kind             string        `json:"kind"`
	Custody          string        `json:"custody"`
	AcquisitionState string        `json:"acquisition_state"`
	MIMEType         string        `json:"mime_type,omitempty"`
	Width            *int          `json:"width,omitempty"`
	Height           *int          `json:"height,omitempty"`
	DurationSeconds  *float64      `json:"duration_seconds,omitempty"`
	ByteSize         *int64        `json:"byte_size,omitempty"`
	Digest           *Digest       `json:"digest,omitempty"`
	ContentHref      string        `json:"content_href,omitempty"`
	SourceURL        string        `json:"source_url,omitempty"`
	Failure          *AssetFailure `json:"failure,omitempty"`
}

type ReaderMediaItem struct {
	Attachment      AttachmentRef        `json:"attachment"`
	MediaAssetID    string               `json:"media_asset_id"`
	ProviderMediaID string               `json:"provider_media_id,omitempty"`
	Kind            string               `json:"kind"`
	AltText         string               `json:"alt_text,omitempty"`
	Variants        []ReaderAssetVariant `json:"variants"`
}

type ResolvedText struct {
	Value         string `json:"value"`
	Source        string `json:"source"`
	ObservationID string `json:"observation_id,omitempty"`
}

type ReaderDisplay struct {
	Title       ResolvedText  `json:"title"`
	Description *ResolvedText `json:"description,omitempty"`
	Byline      *ResolvedText `json:"byline,omitempty"`
	PublishedAt *time.Time    `json:"published_at,omitempty"`
	Summary     ResolvedText  `json:"summary"`
}

type ReaderArticle struct {
	PreviewMarkdown      string `json:"preview_markdown"`
	FullContentAvailable bool   `json:"full_content_available"`
	FullContentHref      string `json:"full_content_href,omitempty"`
}

type AttributedTag struct {
	Value         string `json:"value"`
	Source        string `json:"source"`
	ObservationID string `json:"observation_id,omitempty"`
}

type ReaderTags struct {
	Combined   []string        `json:"combined"`
	Attributed []AttributedTag `json:"attributed"`
}

type ReaderAnnotation struct {
	AnnotationID string             `json:"annotation_id"`
	CaptureID    string             `json:"capture_id"`
	Kind         string             `json:"kind"`
	Text         string             `json:"text"`
	CapturedAt   time.Time          `json:"captured_at"`
	Selector     *TextQuoteSelector `json:"selector,omitempty"`
	Position     *DocumentPosition  `json:"position,omitempty"`
}

type CuratedNote struct {
	BodyMarkdown string    `json:"body_markdown"`
	Revision     int       `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type EffectSummary struct {
	State      string   `json:"state"`
	References []string `json:"references"`
}

type TriageSummary struct {
	CaseIDs         []string `json:"case_ids"`
	UnresolvedCount int      `json:"unresolved_count"`
}

type CapabilityCoverage struct {
	Capability    string `json:"capability"`
	State         string `json:"state"`
	ObservationID string `json:"observation_id,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

type AcquisitionSummary struct {
	Pending       int `json:"pending"`
	Available     int `json:"available"`
	ReferenceOnly int `json:"reference_only"`
	Failed        int `json:"failed"`
}

type OperationalSummaries struct {
	Triage          TriageSummary        `json:"triage"`
	Routing         EffectSummary        `json:"routing"`
	Materialization EffectSummary        `json:"materialization"`
	Enrichment      []CapabilityCoverage `json:"enrichment"`
	Acquisition     AcquisitionSummary   `json:"acquisition"`
}

type CommandCapability struct {
	Command                  string `json:"command"`
	InputSchema              string `json:"input_schema"`
	ExpectedRevisionRequired bool   `json:"expected_revision_required"`
}

type ReaderItem struct {
	SchemaVersion      string               `json:"schema_version"`
	FragmentID         string               `json:"fragment_id"`
	FragmentRevisionID string               `json:"fragment_revision_id"`
	Revision           int                  `json:"revision"`
	Source             SourceIdentity       `json:"source"`
	Renderer           string               `json:"renderer"`
	Display            ReaderDisplay        `json:"display"`
	Article            ReaderArticle        `json:"article"`
	Media              []ReaderMediaItem    `json:"media"`
	Playback           *PlaybackSpec        `json:"playback,omitempty"`
	Tags               ReaderTags           `json:"tags"`
	Annotations        []ReaderAnnotation   `json:"annotations"`
	CuratedNote        *CuratedNote         `json:"curated_note,omitempty"`
	CaptureCount       int                  `json:"capture_count"`
	ReadingState       ReadingState         `json:"reading_state"`
	Operations         OperationalSummaries `json:"operations"`
	Actions            []CommandCapability  `json:"actions"`
}

type ReaderItemList struct {
	SchemaVersion string       `json:"schema_version"`
	Scope         string       `json:"scope"`
	Items         []ReaderItem `json:"items"`
	NextCursor    string       `json:"next_cursor,omitempty"`
}

type ReaderContentReference struct {
	Kind        string  `json:"kind"`
	ReferenceID string  `json:"reference_id"`
	ContentHref string  `json:"content_href"`
	Digest      *Digest `json:"digest,omitempty"`
}

type ReaderAttachmentContext struct {
	AttachmentID    string   `json:"attachment_id"`
	MediaAssetID    string   `json:"media_asset_id"`
	AssetVariantIDs []string `json:"asset_variant_ids"`
	Digests         []Digest `json:"digests"`
}

type ReaderSelection struct {
	Text     string             `json:"text"`
	Selector *TextQuoteSelector `json:"selector,omitempty"`
}

type ReaderContextProvenance struct {
	SourceObservationIDs []string `json:"source_observation_ids"`
	CaptureIDs           []string `json:"capture_ids"`
}

type SensitivityPolicy struct {
	PolicyID string   `json:"policy_id"`
	Labels   []string `json:"labels"`
}

// ReaderContext is immutable-revision-pinned context for an external agent or
// session system. It contains references and provenance, never message state.
type ReaderContext struct {
	SchemaVersion         string                    `json:"schema_version"`
	FragmentID            string                    `json:"fragment_id"`
	FragmentRevisionID    string                    `json:"fragment_revision_id"`
	ReadableContent       []ReaderContentReference  `json:"readable_content"`
	Attachments           []ReaderAttachmentContext `json:"attachments"`
	DerivedObservationIDs []string                  `json:"derived_observation_ids"`
	Selection             *ReaderSelection          `json:"selection,omitempty"`
	Provenance            ReaderContextProvenance   `json:"provenance"`
	SensitivityPolicy     SensitivityPolicy         `json:"sensitivity_policy"`
}

// ConversationRef links a fragment to a conversation owned elsewhere. FE does
// not own the referenced system's messages, tools, or session lifecycle.
type ConversationRef struct {
	SchemaVersion            string    `json:"schema_version"`
	FragmentID               string    `json:"fragment_id"`
	OwningApplication        string    `json:"owning_application"`
	Provider                 string    `json:"provider,omitempty"`
	ConversationID           string    `json:"conversation_id"`
	PinnedStartingRevisionID string    `json:"pinned_starting_revision_id"`
	CreatedByActor           string    `json:"created_by_actor"`
	CreatedAt                time.Time `json:"created_at"`
}

// ReaderCommand is the transport-edge representation of the closed semantic
// command union. Validate it with SchemaReaderCommand before dispatching on
// Command; only the fields belonging to that discriminator will be accepted.
type ReaderCommand struct {
	SchemaVersion        string             `json:"schema_version"`
	Command              string             `json:"command"`
	CommandID            string             `json:"command_id"`
	IdempotencyKey       string             `json:"idempotency_key"`
	ExpectedRevision     int                `json:"expected_revision"`
	Tag                  string             `json:"tag,omitempty"`
	AnnotationID         string             `json:"annotation_id,omitempty"`
	Text                 string             `json:"text,omitempty"`
	Selector             *TextQuoteSelector `json:"selector,omitempty"`
	ExpectedNoteRevision *int               `json:"expected_note_revision,omitempty"`
	BodyMarkdown         string             `json:"body_markdown,omitempty"`
	Position             *ReadingPosition   `json:"position,omitempty"`
	MediaAssetID         string             `json:"media_asset_id,omitempty"`
	VariantKind          string             `json:"variant_kind,omitempty"`
	RequestedCustody     string             `json:"requested_custody,omitempty"`
	RouteID              string             `json:"route_id,omitempty"`
	DestinationID        string             `json:"destination_id,omitempty"`
}

type FieldViolation struct {
	Pointer string `json:"pointer"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type APIProblem struct {
	Type      string           `json:"type"`
	Title     string           `json:"title"`
	Status    int              `json:"status"`
	Detail    string           `json:"detail,omitempty"`
	Instance  string           `json:"instance,omitempty"`
	Code      string           `json:"code"`
	Errors    []FieldViolation `json:"errors,omitempty"`
	Retryable *bool            `json:"retryable,omitempty"`
	RequestID string           `json:"request_id,omitempty"`
}

type ContractCapability struct {
	Name             string   `json:"name"`
	AcceptedVersions []string `json:"accepted_versions"`
	PreferredVersion string   `json:"preferred_version"`
	SchemaID         string   `json:"schema_id"`
}

type OperationReadiness struct {
	CaptureManifest       bool `json:"capture_manifest"`
	AssetUpload           bool `json:"asset_upload"`
	CaptureCompletion     bool `json:"capture_completion"`
	ReaderQuery           bool `json:"reader_query"`
	ReaderCommands        bool `json:"reader_commands"`
	ReaderContext         bool `json:"reader_context"`
	ConversationReference bool `json:"conversation_reference"`
}

type CaptureCapabilities struct {
	MediaKinds          []string `json:"media_kinds"`
	VariantKinds        []string `json:"variant_kinds"`
	CustodyModes        []string `json:"custody_modes"`
	TransferPreferences []string `json:"transfer_preferences"`
	MaxManifestBytes    *int64   `json:"max_manifest_bytes,omitempty"`
	MaxAssetBytes       *int64   `json:"max_asset_bytes,omitempty"`
}

type ReaderCapabilities struct {
	SchemaVersions []string `json:"schema_versions"`
	Scopes         []string `json:"scopes"`
	Renderers      []string `json:"renderers"`
	PlaybackKinds  []string `json:"playback_kinds"`
	Commands       []string `json:"commands"`
}

type ProviderCapability struct {
	Provider     string   `json:"provider"`
	Capabilities []string `json:"capabilities"`
	Available    bool     `json:"available"`
}

type OptionalToolCapability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

type CapabilityDiscovery struct {
	SchemaVersion string                   `json:"schema_version"`
	ServerVersion string                   `json:"server_version"`
	Contracts     []ContractCapability     `json:"contracts"`
	Operations    OperationReadiness       `json:"operations"`
	Capture       CaptureCapabilities      `json:"capture"`
	Reader        ReaderCapabilities       `json:"reader"`
	Providers     []ProviderCapability     `json:"providers"`
	OptionalTools []OptionalToolCapability `json:"optional_tools"`
}
