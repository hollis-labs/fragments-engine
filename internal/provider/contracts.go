// Package provider defines the narrow, effect-declaring contracts used by FE
// provider adapters. It deliberately contains no provider implementations.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type SchemaRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type NetworkClass string

const (
	NetworkNone          NetworkClass = "none"
	NetworkPublicRead    NetworkClass = "public_read"
	NetworkAuthenticated NetworkClass = "authenticated_read"
)

func (n NetworkClass) Valid() bool {
	return n == NetworkNone || n == NetworkPublicRead || n == NetworkAuthenticated
}

type ExternalEffect string

const (
	EffectNone        ExternalEffect = "none"
	EffectNetworkRead ExternalEffect = "network_read"
	EffectBlobWrite   ExternalEffect = "blob_write"
)

func (e ExternalEffect) Valid() bool {
	return e == EffectNone || e == EffectNetworkRead || e == EffectBlobWrite
}

// CredentialReference contains an identifier and a narrow purpose only. There
// is intentionally no value/config/map field in which a secret can be carried.
type CredentialReference struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
}

// Descriptor is persisted on each job attempt so an old result remains
// interpretable after an adapter upgrade.
type Descriptor struct {
	Adapter              string                        `json:"adapter"`
	Version              string                        `json:"version"`
	SupportedProviders   []string                      `json:"supported_providers"`
	SupportedSourceKinds []string                      `json:"supported_source_kinds"`
	SupportedMediaKinds  []domain.MediaKind            `json:"supported_media_kinds"`
	Capabilities         []domain.EnrichmentCapability `json:"capabilities"`
	InputSchema          SchemaRef                     `json:"input_schema"`
	OutputSchema         SchemaRef                     `json:"output_schema"`
	Effects              []ExternalEffect              `json:"effects"`
	NetworkClass         NetworkClass                  `json:"network_class"`
	CredentialReferences []CredentialReference         `json:"credential_references"`
}

var referenceIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func (d Descriptor) Validate() error {
	if !referenceIdentifier.MatchString(d.Adapter) || !referenceIdentifier.MatchString(d.Version) {
		return fmt.Errorf("provider descriptor requires a valid adapter and version")
	}
	if len(d.SupportedProviders) == 0 || len(d.SupportedSourceKinds) == 0 || len(d.Capabilities) == 0 {
		return fmt.Errorf("provider descriptor must declare providers, source kinds, and capabilities")
	}
	if !referenceIdentifier.MatchString(d.InputSchema.ID) || !referenceIdentifier.MatchString(d.InputSchema.Version) ||
		!referenceIdentifier.MatchString(d.OutputSchema.ID) ||
		!referenceIdentifier.MatchString(d.OutputSchema.Version) {
		return fmt.Errorf("provider descriptor requires versioned input and output schemas")
	}
	if !d.NetworkClass.Valid() || len(d.Effects) == 0 {
		return fmt.Errorf("provider descriptor requires a valid network class and effects")
	}
	if err := uniqueStrings("provider", d.SupportedProviders); err != nil {
		return err
	}
	if err := uniqueStrings("source kind", d.SupportedSourceKinds); err != nil {
		return err
	}
	seenMedia := map[domain.MediaKind]struct{}{}
	for _, kind := range d.SupportedMediaKinds {
		if !kind.Valid() {
			return fmt.Errorf("provider descriptor has invalid media kind %q", kind)
		}
		if _, exists := seenMedia[kind]; exists {
			return fmt.Errorf("provider descriptor repeats media kind %q", kind)
		}
		seenMedia[kind] = struct{}{}
	}
	seenCapabilities := map[domain.EnrichmentCapability]struct{}{}
	for _, capability := range d.Capabilities {
		if !capability.Valid() {
			return fmt.Errorf("provider descriptor has invalid capability %q", capability)
		}
		if _, exists := seenCapabilities[capability]; exists {
			return fmt.Errorf("provider descriptor repeats capability %q", capability)
		}
		seenCapabilities[capability] = struct{}{}
	}
	seenEffects := map[ExternalEffect]struct{}{}
	for _, effect := range d.Effects {
		if !effect.Valid() {
			return fmt.Errorf("provider descriptor has invalid effect %q", effect)
		}
		if _, exists := seenEffects[effect]; exists {
			return fmt.Errorf("provider descriptor repeats effect %q", effect)
		}
		seenEffects[effect] = struct{}{}
	}
	if _, none := seenEffects[EffectNone]; none && len(seenEffects) != 1 {
		return fmt.Errorf("provider descriptor effect none cannot be combined with external effects")
	}
	_, networkRead := seenEffects[EffectNetworkRead]
	if (d.NetworkClass == NetworkNone) == networkRead {
		return fmt.Errorf("provider descriptor network class and network_read effect disagree")
	}
	if d.NetworkClass != NetworkAuthenticated && len(d.CredentialReferences) != 0 {
		return fmt.Errorf("credential references require authenticated_read network class")
	}
	seenCredentials := map[string]struct{}{}
	for _, ref := range d.CredentialReferences {
		if !referenceIdentifier.MatchString(ref.Name) || !referenceIdentifier.MatchString(ref.Purpose) {
			return fmt.Errorf("credential references must be opaque names and purposes, never values")
		}
		if _, exists := seenCredentials[ref.Name]; exists {
			return fmt.Errorf("provider descriptor repeats credential reference %q", ref.Name)
		}
		seenCredentials[ref.Name] = struct{}{}
	}
	return nil
}

func uniqueStrings(label string, values []string) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "*" && !referenceIdentifier.MatchString(value) {
			return fmt.Errorf("provider descriptor has empty %s", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("provider descriptor repeats %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func (d Descriptor) Supports(capability domain.EnrichmentCapability, providerName, sourceKind string, mediaKinds []domain.MediaKind) bool {
	if d.Validate() != nil || !containsCapability(d.Capabilities, capability) ||
		!containsFold(d.SupportedProviders, providerName) || !containsFold(d.SupportedSourceKinds, sourceKind) {
		return false
	}
	if len(d.SupportedMediaKinds) == 0 || len(mediaKinds) == 0 {
		return true
	}
	for _, actual := range mediaKinds {
		for _, supported := range d.SupportedMediaKinds {
			if actual == supported {
				return true
			}
		}
	}
	return false
}

func containsCapability(values []domain.EnrichmentCapability, target domain.EnrichmentCapability) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, value := range values {
		if value == "*" || strings.ToLower(strings.TrimSpace(value)) == target {
			return true
		}
	}
	return false
}

type Input struct {
	FragmentID         string                          `json:"fragment_id"`
	FragmentRevisionID string                          `json:"fragment_revision_id"`
	Capability         domain.EnrichmentCapability     `json:"capability"`
	Source             domain.SourceIdentity           `json:"source"`
	MaterialDigest     string                          `json:"material_digest"`
	AssetDigests       []domain.ObservationAssetDigest `json:"asset_digests"`
}

func (i Input) Validate() error {
	if strings.TrimSpace(i.FragmentID) == "" || strings.TrimSpace(i.FragmentRevisionID) == "" ||
		!i.Capability.Valid() || strings.TrimSpace(i.MaterialDigest) == "" {
		return fmt.Errorf("provider input must be pinned to a fragment revision, capability, and material digest")
	}
	if strings.TrimSpace(i.Source.SourceRegistrationID) == "" || strings.TrimSpace(i.Source.SourceItemKey) == "" ||
		strings.TrimSpace(i.Source.SegmentKey) == "" || strings.TrimSpace(i.Source.Provider) == "" ||
		strings.TrimSpace(i.Source.SourceAdapter.Adapter) == "" || strings.TrimSpace(i.Source.SourceAdapter.Version) == "" ||
		strings.TrimSpace(i.Source.Canonicalizer.Adapter) == "" || strings.TrimSpace(i.Source.Canonicalizer.Version) == "" {
		return fmt.Errorf("provider input requires complete versioned source identity provenance")
	}
	for _, asset := range i.AssetDigests {
		if strings.TrimSpace(asset.AssetVariantID) == "" {
			return fmt.Errorf("provider input asset digest requires a variant ID")
		}
		if _, err := asset.Digest.Normalized(); err != nil {
			return fmt.Errorf("provider input asset %q: %w", asset.AssetVariantID, err)
		}
	}
	return nil
}

// ObservationDraft contains only one typed provider result. The service adds
// authoritative revision/input provenance from the fenced claim.
type ObservationDraft struct {
	Capability  domain.EnrichmentCapability `json:"capability"`
	Attribution domain.AttributionSource    `json:"attribution"`
	ValueJSON   string                      `json:"value_json"`
	Confidence  *float64                    `json:"confidence,omitempty"`
	ObservedAt  time.Time                   `json:"observed_at"`
	ExpiresAt   time.Time                   `json:"expires_at,omitempty"`
}

func (o ObservationDraft) Validate() error {
	if !o.Capability.Valid() || !o.Attribution.ValidDescriptionSource() || o.Attribution == domain.AttributionSourceMaterial || o.Attribution == domain.AttributionUser {
		return fmt.Errorf("provider observation requires provider, deterministic, or model attribution")
	}
	if !json.Valid([]byte(o.ValueJSON)) || strings.TrimSpace(o.ValueJSON) == "null" || strings.TrimSpace(o.ValueJSON) == "{}" {
		return fmt.Errorf("provider observation requires a non-empty typed JSON value")
	}
	if err := ValidateCapabilityValue(o.Capability, []byte(o.ValueJSON)); err != nil {
		return err
	}
	if o.Confidence != nil && (*o.Confidence < 0 || *o.Confidence > 1) {
		return fmt.Errorf("provider observation confidence must be between zero and one")
	}
	if o.ObservedAt.IsZero() || (!o.ExpiresAt.IsZero() && o.ExpiresAt.Before(o.ObservedAt)) {
		return fmt.Errorf("provider observation has invalid observed/expiry times")
	}
	return nil
}

type TextValue struct {
	Text     string `json:"text"`
	Format   string `json:"format,omitempty"`
	Language string `json:"language,omitempty"`
}

type ReferenceListValue struct {
	AttachmentIDs   []string `json:"attachment_ids,omitempty"`
	MediaAssetIDs   []string `json:"media_asset_ids,omitempty"`
	AssetVariantIDs []string `json:"asset_variant_ids,omitempty"`
}

type StringListValue struct {
	Values []string `json:"values"`
}

type TranscriptValue struct {
	Kind            string   `json:"kind"`
	Text            string   `json:"text,omitempty"`
	Format          string   `json:"format,omitempty"`
	Language        string   `json:"language,omitempty"`
	AssetVariantIDs []string `json:"asset_variant_ids,omitempty"`
}

type EntityValue struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type EntityListValue struct {
	Entities []EntityValue `json:"entities"`
}

// ValidateCapabilityValue is the common safety boundary for both adapter
// results and persistence. It admits only capability-specific typed shapes, so
// raw oEmbed HTML or arbitrary provider maps cannot masquerade as observations.
func ValidateCapabilityValue(capability domain.EnrichmentCapability, raw []byte) error {
	switch capability {
	case domain.CapabilityTitle, domain.CapabilityDescription, domain.CapabilityBody,
		domain.CapabilityOCR, domain.CapabilityVision, domain.CapabilitySummary:
		var value TextValue
		if err := decodeStrict(raw, &value); err != nil || strings.TrimSpace(value.Text) == "" {
			return fmt.Errorf("%s observation requires a typed non-empty text value", capability)
		}
		if containsExecutableMarkup(value.Text) {
			return fmt.Errorf("%s observation contains executable or embed markup", capability)
		}
		if !validTextFormat(capability, value.Format) {
			return fmt.Errorf("%s observation has unsupported non-text format %q", capability, value.Format)
		}
	case domain.CapabilityTranscript:
		var value TranscriptValue
		if err := decodeStrict(raw, &value); err != nil {
			return fmt.Errorf("transcript observation requires typed text or variant references")
		}
		switch value.Kind {
		case "text":
			if strings.TrimSpace(value.Text) == "" || len(value.AssetVariantIDs) != 0 || containsExecutableMarkup(value.Text) || !validTranscriptFormat(value.Format) {
				return fmt.Errorf("transcript text observation requires only safe non-empty text")
			}
		case "variant_refs":
			if value.Text != "" || value.Format != "" || value.Language != "" {
				return fmt.Errorf("transcript variant observation accepts only asset_variant_ids")
			}
			if err := validateReferences(value.AssetVariantIDs); err != nil {
				return fmt.Errorf("transcript observation: %w", err)
			}
		default:
			return fmt.Errorf("transcript observation requires kind text or variant_refs")
		}
	case domain.CapabilityGalleryManifest, domain.CapabilityOriginalMedia, domain.CapabilityThumbnailOrPoster:
		var value ReferenceListValue
		if err := decodeStrict(raw, &value); err != nil {
			return fmt.Errorf("%s observation requires typed attachment/media/variant references", capability)
		}
		var refs []string
		switch capability {
		case domain.CapabilityGalleryManifest:
			if len(value.AssetVariantIDs) != 0 || (len(value.AttachmentIDs) == 0) == (len(value.MediaAssetIDs) == 0) {
				return fmt.Errorf("gallery_manifest observation requires exactly one attachment_ids or media_asset_ids list")
			}
			refs = append(value.AttachmentIDs, value.MediaAssetIDs...)
		case domain.CapabilityOriginalMedia, domain.CapabilityThumbnailOrPoster:
			if len(value.AttachmentIDs) != 0 || len(value.MediaAssetIDs) != 0 {
				return fmt.Errorf("%s observation accepts only asset_variant_ids", capability)
			}
			refs = value.AssetVariantIDs
		}
		if err := validateReferences(refs); err != nil {
			return fmt.Errorf("%s observation: %w", capability, err)
		}
	case domain.CapabilityTags:
		var value StringListValue
		if err := decodeStrict(raw, &value); err != nil || len(value.Values) == 0 {
			return fmt.Errorf("tags observation requires a typed non-empty value list")
		}
		for _, tag := range value.Values {
			if strings.TrimSpace(tag) == "" || containsExecutableMarkup(tag) {
				return fmt.Errorf("tags observation contains an invalid value")
			}
		}
	case domain.CapabilityEntities:
		var value EntityListValue
		if err := decodeStrict(raw, &value); err != nil || len(value.Entities) == 0 {
			return fmt.Errorf("entities observation requires a typed non-empty entity list")
		}
		for _, entity := range value.Entities {
			if strings.TrimSpace(entity.Kind) == "" || strings.TrimSpace(entity.Value) == "" || containsExecutableMarkup(entity.Value) {
				return fmt.Errorf("entities observation contains an invalid entity")
			}
		}
	default:
		return fmt.Errorf("invalid enrichment capability %q", capability)
	}
	return nil
}

func validTextFormat(capability domain.EnrichmentCapability, format string) bool {
	format = strings.ToLower(strings.TrimSpace(format))
	if capability == domain.CapabilityBody {
		return format == "" || format == "plain_text" || format == "markdown"
	}
	return format == "" || format == "plain_text"
}

func validTranscriptFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "plain_text", "webvtt", "srt":
		return true
	default:
		return false
	}
}

func validateReferences(refs []string) error {
	if len(refs) == 0 {
		return fmt.Errorf("at least one reference is required")
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if !referenceIdentifier.MatchString(ref) {
			return fmt.Errorf("reference must be non-blank and opaque")
		}
		if _, duplicate := seen[ref]; duplicate {
			return fmt.Errorf("duplicate reference %q", ref)
		}
		seen[ref] = struct{}{}
	}
	return nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func containsExecutableMarkup(value string) bool {
	lower := strings.ToLower(value)
	for _, unsafe := range []string{"<script", "<iframe", "javascript:", "onerror=", "onload="} {
		if strings.Contains(lower, unsafe) {
			return true
		}
	}
	return false
}

type CanonicalIdentityResult struct {
	Identity    domain.SourceIdentity `json:"identity"`
	Observation ObservationDraft      `json:"observation"`
}

type MetadataResult struct {
	Observations []ObservationDraft `json:"observations"`
	OEmbed       *OEmbedMetadata    `json:"oembed,omitempty"`
}

type MediaRequest struct {
	Input            Input                   `json:"input"`
	MediaAssetID     string                  `json:"media_asset_id"`
	VariantKind      domain.AssetVariantKind `json:"variant_kind"`
	RequestedCustody domain.CustodyMode      `json:"requested_custody"`
}

type MediaResult struct {
	Manifest     []domain.MediaManifestItem `json:"manifest"`
	Observations []ObservationDraft         `json:"observations"`
}

type TranscriptResult struct {
	Variant     domain.AssetVariant `json:"variant"`
	Observation ObservationDraft    `json:"observation"`
}

type PlaybackKind string

const (
	PlaybackProviderEmbed PlaybackKind = "provider_embed"
	PlaybackBlobStream    PlaybackKind = "blob_stream"
)

// PlaybackSpec is closed and typed. Provider markup is deliberately absent.
type PlaybackSpec struct {
	Kind           PlaybackKind `json:"kind"`
	Provider       string       `json:"provider,omitempty"`
	ProviderItemID string       `json:"provider_item_id,omitempty"`
	AssetVariantID string       `json:"asset_variant_id,omitempty"`
	StartSeconds   float64      `json:"start_seconds,omitempty"`
}

type PlaybackResult struct {
	Spec        PlaybackSpec     `json:"spec"`
	Observation ObservationDraft `json:"observation"`
}

type CanonicalIdentityResolver interface {
	Descriptor() Descriptor
	ResolveCanonicalIdentity(context.Context, Input) (CanonicalIdentityResult, error)
}

type MetadataProvider interface {
	Descriptor() Descriptor
	ProvideMetadata(context.Context, Input) (MetadataResult, error)
}

type MediaAcquirer interface {
	Descriptor() Descriptor
	AcquireMedia(context.Context, MediaRequest) (MediaResult, error)
}

type TranscriptProvider interface {
	Descriptor() Descriptor
	ProvideTranscript(context.Context, Input) (TranscriptResult, error)
}

type PlaybackProvider interface {
	Descriptor() Descriptor
	ProvidePlayback(context.Context, Input) (PlaybackResult, error)
}

// AdapterError classifies provider failures without leaking credential values
// or unstructured provider response bodies into durable state.
type AdapterError struct {
	Class   domain.EnrichmentErrorClass `json:"class"`
	Code    string                      `json:"code"`
	Message string                      `json:"message"`
	Cause   error                       `json:"-"`
}

// CredentialResolver injects an external credential authority. The callback
// shape keeps resolved bytes ephemeral: only CredentialReference is eligible
// for descriptors, observation provenance, job snapshots, or logs.
type CredentialResolver interface {
	Use(context.Context, CredentialReference, func(secret []byte) error) error
}

func (e *AdapterError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("provider %s error %s: %s", e.Class, e.Code, e.Message)
}

func (e *AdapterError) Unwrap() error { return e.Cause }

func (e *AdapterError) Validate() error {
	if e == nil {
		return fmt.Errorf("provider error is required")
	}
	return (domain.EnrichmentFailure{Class: e.Class, Code: e.Code, Message: e.Message}).Validate()
}

// OEmbedMetadata is the complete persistence-safe subset accepted from an
// oEmbed response. It has no HTML field and no arbitrary extension container.
type OEmbedMetadata struct {
	ProviderName    string `json:"provider_name,omitempty"`
	ProviderURL     string `json:"provider_url,omitempty"`
	Title           string `json:"title,omitempty"`
	AuthorName      string `json:"author_name,omitempty"`
	AuthorURL       string `json:"author_url,omitempty"`
	ThumbnailURL    string `json:"thumbnail_url,omitempty"`
	ThumbnailWidth  int    `json:"thumbnail_width,omitempty"`
	ThumbnailHeight int    `json:"thumbnail_height,omitempty"`
}

// ParseOEmbedMetadata deliberately decodes through a private wire type whose
// HTML value is discarded. Only typed recognized fields can cross this seam.
func ParseOEmbedMetadata(raw []byte) (OEmbedMetadata, error) {
	var wire struct {
		ProviderName    string          `json:"provider_name"`
		ProviderURL     string          `json:"provider_url"`
		Title           string          `json:"title"`
		AuthorName      string          `json:"author_name"`
		AuthorURL       string          `json:"author_url"`
		ThumbnailURL    string          `json:"thumbnail_url"`
		ThumbnailWidth  int             `json:"thumbnail_width"`
		ThumbnailHeight int             `json:"thumbnail_height"`
		HTML            json.RawMessage `json:"html"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return OEmbedMetadata{}, fmt.Errorf("decode oEmbed metadata: %w", err)
	}
	out := OEmbedMetadata{ProviderName: strings.TrimSpace(wire.ProviderName), ProviderURL: strings.TrimSpace(wire.ProviderURL),
		Title: strings.TrimSpace(wire.Title), AuthorName: strings.TrimSpace(wire.AuthorName), AuthorURL: strings.TrimSpace(wire.AuthorURL),
		ThumbnailURL: strings.TrimSpace(wire.ThumbnailURL), ThumbnailWidth: wire.ThumbnailWidth, ThumbnailHeight: wire.ThumbnailHeight}
	for label, candidate := range map[string]string{"provider_url": out.ProviderURL, "author_url": out.AuthorURL, "thumbnail_url": out.ThumbnailURL} {
		if candidate != "" && !safeHTTPURL(candidate) {
			return OEmbedMetadata{}, fmt.Errorf("oEmbed %s must be an absolute HTTP(S) URL", label)
		}
	}
	if out.ThumbnailWidth < 0 || out.ThumbnailHeight < 0 {
		return OEmbedMetadata{}, fmt.Errorf("oEmbed thumbnail dimensions cannot be negative")
	}
	return out, nil
}

func safeHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
}

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func ValidatePlaybackSpec(spec PlaybackSpec) error {
	switch spec.Kind {
	case PlaybackProviderEmbed:
		if strings.ToLower(spec.Provider) != "youtube" || !youtubeID.MatchString(spec.ProviderItemID) || spec.AssetVariantID != "" {
			return fmt.Errorf("provider playback requires an allowlisted provider and validated item ID")
		}
	case PlaybackBlobStream:
		if !referenceIdentifier.MatchString(spec.AssetVariantID) || spec.Provider != "" || spec.ProviderItemID != "" {
			return fmt.Errorf("blob playback requires only a valid asset variant ID")
		}
	default:
		return fmt.Errorf("unsupported playback kind %q", spec.Kind)
	}
	if spec.StartSeconds < 0 {
		return fmt.Errorf("playback start cannot be negative")
	}
	return nil
}

func SortedDescriptorCapabilities(d Descriptor) []domain.EnrichmentCapability {
	out := append([]domain.EnrichmentCapability(nil), d.Capabilities...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
