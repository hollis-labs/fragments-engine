// Package pinterest implements provider-neutral Pinterest identity, metadata,
// and media completion over the shared enrichment contracts.
package pinterest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
)

const (
	AdapterName    = "fe.provider.pinterest"
	AdapterVersion = "1.0.0"
)

// Client is the injected Pinterest access boundary. Implementations may use a
// public endpoint, oEmbed, or a fixture, but must not expose credentials here.
type Client interface {
	Lookup(context.Context, LookupRequest) (Snapshot, error)
}

type LookupRequest struct {
	SubmittedURL  string `json:"submitted_url"`
	DirectPinID   string `json:"direct_pin_id,omitempty"`
	ShortCode     string `json:"short_code,omitempty"`
	SourceItemKey string `json:"source_item_key"`
	Capability    string `json:"capability"`
}

// Snapshot is a typed provider response. OEmbedJSON is input-only: Adapter
// narrows it immediately and never returns the raw object.
type Snapshot struct {
	PinID        string          `json:"pin_id"`
	CanonicalURL string          `json:"canonical_url"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	Creator      string          `json:"creator,omitempty"`
	Board        string          `json:"board,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Image        ImageSnapshot   `json:"image"`
	OEmbedJSON   json.RawMessage `json:"oembed,omitempty"`
	ObservedAt   time.Time       `json:"observed_at"`
	Confidence   *float64        `json:"confidence,omitempty"`
}

// DecodeSnapshot applies the adapter's strict fixture/wire schema. Provider
// extensions are allowed only inside the deliberately narrowed oEmbed input.
func DecodeSnapshot(raw []byte) (Snapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode Pinterest snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return Snapshot{}, fmt.Errorf("decode Pinterest snapshot: %w", err)
	}
	return snapshot, nil
}

type ImageSnapshot struct {
	MediaID   string            `json:"media_id,omitempty"`
	AltText   string            `json:"alt_text,omitempty"`
	Original  *VariantCandidate `json:"original,omitempty"`
	Thumbnail *VariantCandidate `json:"thumbnail,omitempty"`
}

// VariantCandidate represents a provider-observed representation. The adapter
// never manufactures dimensions or upgrades a thumbnail into an original.
type VariantCandidate struct {
	SourceURL       string                `json:"source_url,omitempty"`
	SourceExpiresAt time.Time             `json:"source_expires_at,omitempty"`
	MIMEType        string                `json:"mime_type,omitempty"`
	Width           int                   `json:"width,omitempty"`
	Height          int                   `json:"height,omitempty"`
	ByteSize        int64                 `json:"byte_size,omitempty"`
	ExpectedDigest  *domain.ContentDigest `json:"expected_digest,omitempty"`
	Failure         *VariantFailure       `json:"failure,omitempty"`
}

// VariantFailure is intentionally closed and message-free. Provider response
// bodies cannot become durable error messages through this type.
type VariantFailure struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

type ClientFailureKind string

const (
	FailureNotFound       ClientFailureKind = "not_found"
	FailureUnsupported    ClientFailureKind = "unsupported"
	FailureAuthentication ClientFailureKind = "authentication"
	FailureRateLimited    ClientFailureKind = "rate_limited"
	FailureTransient      ClientFailureKind = "transient"
)

type ClientError struct {
	Kind  ClientFailureKind
	Cause error
}

func (e *ClientError) Error() string {
	if e == nil {
		return ""
	}
	return "pinterest client failure: " + string(e.Kind)
}

func (e *ClientError) Unwrap() error { return e.Cause }

type Adapter struct {
	client Client
}

func New(client Client) (*Adapter, error) {
	if client == nil {
		return nil, fmt.Errorf("pinterest adapter requires a client")
	}
	adapter := &Adapter{client: client}
	if err := adapter.Descriptor().Validate(); err != nil {
		return nil, err
	}
	return adapter, nil
}

func (a *Adapter) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		Adapter: AdapterName, Version: AdapterVersion,
		SupportedProviders:   []string{"pinterest"},
		SupportedSourceKinds: []string{"image", "gallery", "article", "text"},
		SupportedMediaKinds:  []domain.MediaKind{domain.MediaImage},
		Capabilities: []domain.EnrichmentCapability{
			domain.CapabilityTitle, domain.CapabilityDescription,
			domain.CapabilityGalleryManifest, domain.CapabilityOriginalMedia,
			domain.CapabilityThumbnailOrPoster, domain.CapabilityTags,
			domain.CapabilityEntities,
		},
		InputSchema:  provider.SchemaRef{ID: "fe.provider.pinterest.input", Version: "1"},
		OutputSchema: provider.SchemaRef{ID: "fe.provider.pinterest.result", Version: "1"},
		Effects:      []provider.ExternalEffect{provider.EffectNetworkRead},
		NetworkClass: provider.NetworkPublicRead,
	}
}

func (a *Adapter) ResolveCanonicalIdentity(ctx context.Context, input provider.Input) (provider.CanonicalIdentityResult, error) {
	if input.Capability != domain.CapabilityEntities {
		return provider.CanonicalIdentityResult{}, permanent("capability_mismatch", "identity resolution requires an entities claim", nil)
	}
	snapshot, _, err := a.lookup(ctx, input)
	if err != nil {
		return provider.CanonicalIdentityResult{}, err
	}
	identity := input.Source
	identity.Provider = "pinterest"
	identity.ProviderItemID = snapshot.PinID
	identity.SourceItemKey = "pinterest:pin:" + snapshot.PinID
	identity.CanonicalURL = canonicalPinURL(snapshot.PinID)
	identity.SegmentKey = domain.DefaultSegmentKey
	identity.Canonicalizer = domain.AdapterVersion{Adapter: AdapterName, Version: AdapterVersion}
	draft, err := observation(domain.CapabilityEntities,
		provider.EntityListValue{Entities: []provider.EntityValue{{Kind: "pinterest_pin", Value: snapshot.PinID}}}, snapshot)
	if err != nil {
		return provider.CanonicalIdentityResult{}, err
	}
	return provider.CanonicalIdentityResult{Identity: identity, Observation: draft}, nil
}

func (a *Adapter) ProvideMetadata(ctx context.Context, input provider.Input) (provider.MetadataResult, error) {
	snapshot, oembed, err := a.lookup(ctx, input)
	if err != nil {
		return provider.MetadataResult{}, err
	}
	var value any
	switch input.Capability {
	case domain.CapabilityTitle:
		if snapshot.Title == "" {
			return provider.MetadataResult{}, unavailable(input.Capability)
		}
		value = provider.TextValue{Text: snapshot.Title, Format: "plain_text"}
	case domain.CapabilityDescription:
		if snapshot.Description == "" {
			return provider.MetadataResult{}, unavailable(input.Capability)
		}
		value = provider.TextValue{Text: snapshot.Description, Format: "plain_text"}
	case domain.CapabilityTags:
		if len(snapshot.Tags) == 0 {
			return provider.MetadataResult{}, unavailable(input.Capability)
		}
		value = provider.StringListValue{Values: snapshot.Tags}
	case domain.CapabilityEntities:
		entities := pinterestEntities(snapshot)
		if len(entities) == 0 {
			return provider.MetadataResult{}, unavailable(input.Capability)
		}
		value = provider.EntityListValue{Entities: entities}
	default:
		return provider.MetadataResult{}, permanent("capability_mismatch", "metadata adapter does not produce the claimed capability", nil)
	}
	draft, err := observation(input.Capability, value, snapshot)
	if err != nil {
		return provider.MetadataResult{}, err
	}
	return provider.MetadataResult{Observations: []provider.ObservationDraft{draft}, OEmbed: oembed}, nil
}

func (a *Adapter) AcquireMedia(ctx context.Context, request provider.MediaRequest) (provider.MediaResult, error) {
	if request.Input.Capability != domain.CapabilityGalleryManifest &&
		request.Input.Capability != domain.CapabilityOriginalMedia &&
		request.Input.Capability != domain.CapabilityThumbnailOrPoster {
		return provider.MediaResult{}, permanent("capability_mismatch", "media adapter does not produce the claimed capability", nil)
	}
	if request.VariantKind != "" && !request.VariantKind.Valid() {
		return provider.MediaResult{}, permanent("invalid_request", "requested media variant kind is invalid", nil)
	}
	if request.RequestedCustody != "" && !request.RequestedCustody.Valid() {
		return provider.MediaResult{}, permanent("invalid_request", "requested custody is invalid", nil)
	}
	if request.Input.Capability == domain.CapabilityGalleryManifest && (request.MediaAssetID != "" || request.VariantKind != "") {
		return provider.MediaResult{}, permanent("invalid_request", "gallery completion requires the complete ordered media result", nil)
	}
	snapshot, _, err := a.lookup(ctx, request.Input)
	if err != nil {
		return provider.MediaResult{}, err
	}
	manifest, err := buildManifest(request.Input, snapshot, request.RequestedCustody)
	if err != nil {
		return provider.MediaResult{}, err
	}
	if request.MediaAssetID != "" && manifest[0].Asset.ID != request.MediaAssetID {
		return provider.MediaResult{}, permanent("media_not_found", "requested media asset is not part of the pin", nil)
	}
	if request.VariantKind != "" {
		filtered := manifest[0].Variants[:0]
		for _, variant := range manifest[0].Variants {
			if variant.Kind == request.VariantKind {
				filtered = append(filtered, variant)
			}
		}
		manifest[0].Variants = filtered
		if len(filtered) == 0 {
			return provider.MediaResult{}, unavailable(request.Input.Capability)
		}
	}
	refs := mediaReferences(manifest, request.Input.Capability)
	if len(refs) == 0 {
		return provider.MediaResult{Manifest: manifest}, mediaUnavailable(manifest, request.Input.Capability)
	}
	var value any
	if request.Input.Capability == domain.CapabilityGalleryManifest {
		value = provider.ReferenceListValue{MediaAssetIDs: refs}
	} else {
		value = provider.ReferenceListValue{AssetVariantIDs: refs}
	}
	draft, err := observation(request.Input.Capability, value, snapshot)
	if err != nil {
		return provider.MediaResult{}, err
	}
	draft.ExpiresAt = earliestReferenceExpiry(manifest, refs)
	if err := draft.Validate(); err != nil {
		return provider.MediaResult{}, permanent("invalid_response", "Pinterest media expiry is invalid", err)
	}
	return provider.MediaResult{Manifest: manifest, Observations: []provider.ObservationDraft{draft}}, nil
}

type urlReference struct {
	SubmittedURL string
	DirectPinID  string
	ShortCode    string
}

var (
	pinIDPattern     = regexp.MustCompile(`^[0-9]{6,30}$`)
	shortCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)
)

var pinterestHosts = map[string]struct{}{
	"pinterest.com": {}, "www.pinterest.com": {}, "m.pinterest.com": {},
	"pinterest.ca": {}, "www.pinterest.ca": {}, "m.pinterest.ca": {},
	"pinterest.co.uk": {}, "www.pinterest.co.uk": {}, "m.pinterest.co.uk": {},
}

func ParseURL(raw string) (pinID, shortCode string, err error) {
	reference, err := parseURL(raw)
	return reference.DirectPinID, reference.ShortCode, err
}

func parseURL(raw string) (urlReference, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Port() != "" || parsed.RawPath != "" {
		return urlReference{}, fmt.Errorf("unsupported Pinterest URL")
	}
	host := strings.ToLower(parsed.Hostname())
	segments := pathSegments(parsed.Path)
	if host == "pin.it" || host == "www.pin.it" {
		if len(segments) != 1 || !shortCodePattern.MatchString(segments[0]) {
			return urlReference{}, fmt.Errorf("invalid Pinterest short URL")
		}
		return urlReference{SubmittedURL: raw, ShortCode: segments[0]}, nil
	}
	if _, ok := pinterestHosts[host]; !ok || len(segments) != 2 || segments[0] != "pin" || !pinIDPattern.MatchString(segments[1]) {
		return urlReference{}, fmt.Errorf("unsupported Pinterest pin URL")
	}
	return urlReference{SubmittedURL: raw, DirectPinID: segments[1]}, nil
}

func canonicalPinURL(pinID string) string { return "https://www.pinterest.com/pin/" + pinID + "/" }

func pathSegments(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts

}

func (a *Adapter) lookup(ctx context.Context, input provider.Input) (Snapshot, *provider.OEmbedMetadata, error) {
	if err := input.Validate(); err != nil {
		return Snapshot{}, nil, permanent("invalid_input", "Pinterest input provenance is invalid", err)
	}
	if !strings.EqualFold(input.Source.Provider, "pinterest") {
		return Snapshot{}, nil, permanent("provider_mismatch", "source provider is not Pinterest", nil)
	}
	rawURL := input.Source.SubmittedURL
	if strings.TrimSpace(rawURL) == "" {
		rawURL = input.Source.CanonicalURL
	}
	reference, err := parseURL(rawURL)
	if err != nil {
		return Snapshot{}, nil, permanent("invalid_url", "source URL is not an allowlisted Pinterest pin URL", err)
	}
	snapshot, err := a.client.Lookup(ctx, LookupRequest{SubmittedURL: reference.SubmittedURL,
		DirectPinID: reference.DirectPinID, ShortCode: reference.ShortCode,
		SourceItemKey: input.Source.SourceItemKey, Capability: string(input.Capability)})
	if err != nil {
		return Snapshot{}, nil, classifyClientError(err)
	}
	snapshot, oembed, err := normalizeSnapshot(snapshot, reference, input.Source.ProviderItemID)
	if err != nil {
		return Snapshot{}, nil, err
	}
	return snapshot, oembed, nil
}

func normalizeSnapshot(snapshot Snapshot, reference urlReference, inputPinID string) (Snapshot, *provider.OEmbedMetadata, error) {
	snapshot.Tags = append([]string(nil), snapshot.Tags...)
	snapshot.PinID = strings.TrimSpace(snapshot.PinID)
	if !pinIDPattern.MatchString(snapshot.PinID) || snapshot.ObservedAt.IsZero() {
		return Snapshot{}, nil, permanent("invalid_response", "Pinterest response lacks a valid pin identity or observation time", nil)
	}
	if reference.DirectPinID != "" && snapshot.PinID != reference.DirectPinID {
		return Snapshot{}, nil, permanent("identity_conflict", "Pinterest response pin ID conflicts with the submitted URL", nil)
	}
	if strings.TrimSpace(inputPinID) != "" && snapshot.PinID != strings.TrimSpace(inputPinID) {
		return Snapshot{}, nil, permanent("identity_conflict", "Pinterest response pin ID conflicts with source provenance", nil)
	}
	finalRef, err := parseURL(snapshot.CanonicalURL)
	if err != nil || finalRef.DirectPinID == "" || finalRef.DirectPinID != snapshot.PinID {
		return Snapshot{}, nil, permanent("invalid_response", "Pinterest response did not provide an allowlisted final pin URL", err)
	}
	snapshot.CanonicalURL = canonicalPinURL(snapshot.PinID)
	snapshot.Title = strings.TrimSpace(snapshot.Title)
	snapshot.Description = strings.TrimSpace(snapshot.Description)
	snapshot.Creator = strings.TrimSpace(snapshot.Creator)
	snapshot.Board = strings.TrimSpace(snapshot.Board)
	snapshot.Tags = normalizedSet(snapshot.Tags)
	if snapshot.Confidence != nil && (*snapshot.Confidence < 0 || *snapshot.Confidence > 1) {
		return Snapshot{}, nil, permanent("invalid_response", "Pinterest response confidence is invalid", nil)
	}
	var safeOEmbed *provider.OEmbedMetadata
	if len(snapshot.OEmbedJSON) != 0 && string(snapshot.OEmbedJSON) != "null" {
		parsed, err := provider.ParseOEmbedMetadata(snapshot.OEmbedJSON)
		if err != nil {
			return Snapshot{}, nil, permanent("invalid_response", "Pinterest oEmbed metadata is invalid", err)
		}
		if parsed.ProviderName != "" && !strings.EqualFold(parsed.ProviderName, "Pinterest") {
			return Snapshot{}, nil, permanent("provider_mismatch", "oEmbed metadata is not attributed to Pinterest", nil)
		}
		if parsed.ProviderURL != "" && !allowlistedPinterestURL(parsed.ProviderURL) {
			return Snapshot{}, nil, permanent("provider_mismatch", "oEmbed provider URL is not Pinterest", nil)
		}
		if parsed.AuthorURL != "" && !allowlistedPinterestURL(parsed.AuthorURL) {
			return Snapshot{}, nil, permanent("invalid_response", "Pinterest oEmbed author URL is not allowlisted", nil)
		}
		if parsed.ThumbnailURL != "" && !safeMediaURL(parsed.ThumbnailURL) {
			return Snapshot{}, nil, permanent("invalid_response", "Pinterest oEmbed thumbnail URL is not allowlisted", nil)
		}
		if unsafeText(parsed.ProviderName) || unsafeText(parsed.Title) || unsafeText(parsed.AuthorName) {
			return Snapshot{}, nil, permanent("unsafe_response", "Pinterest oEmbed metadata contains executable markup", nil)
		}
		safeOEmbed = &parsed
		if snapshot.Title == "" {
			snapshot.Title = parsed.Title
		}
		if snapshot.Creator == "" {
			snapshot.Creator = parsed.AuthorName
		}
	}
	if err := validateCandidate("original", snapshot.Image.Original, snapshot.ObservedAt); err != nil {
		return Snapshot{}, nil, permanent("invalid_response", "Pinterest original candidate is invalid", err)
	}
	if err := validateCandidate("thumbnail", snapshot.Image.Thumbnail, snapshot.ObservedAt); err != nil {
		return Snapshot{}, nil, permanent("invalid_response", "Pinterest thumbnail candidate is invalid", err)
	}
	return snapshot, safeOEmbed, nil
}

func allowlistedPinterestURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	_, ok := pinterestHosts[strings.ToLower(parsed.Hostname())]
	return ok
}

func validateCandidate(label string, candidate *VariantCandidate, observedAt time.Time) error {
	if candidate == nil {
		return nil
	}
	if candidate.Width < 0 || candidate.Height < 0 || candidate.ByteSize < 0 {
		return fmt.Errorf("%s dimensions and size cannot be negative", label)
	}
	if candidate.SourceURL == "" && candidate.Failure == nil {
		return fmt.Errorf("%s candidate requires a source URL or classified failure", label)
	}
	if candidate.SourceURL != "" && candidate.Failure != nil {
		return fmt.Errorf("%s candidate cannot be both usable and failed", label)
	}
	if candidate.SourceURL != "" && !safeMediaURL(candidate.SourceURL) {
		return fmt.Errorf("%s source URL must be absolute HTTPS without credentials or port", label)
	}
	if candidate.Failure != nil && !failureCodePattern.MatchString(candidate.Failure.Code) {
		return fmt.Errorf("%s failure requires a safe code", label)
	}
	if !candidate.SourceExpiresAt.IsZero() && candidate.SourceExpiresAt.Before(observedAt) {
		return fmt.Errorf("%s source expiry predates the observation", label)
	}
	if candidate.ExpectedDigest != nil {
		if _, err := candidate.ExpectedDigest.Normalized(); err != nil {
			return err
		}
	}
	return nil
}

var failureCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func safeMediaURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "pinimg.com" || strings.HasSuffix(host, ".pinimg.com") {
		return true
	}
	_, ok := pinterestHosts[host]
	return ok
}

func unsafeText(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"<script", "<iframe", "javascript:", "onerror=", "onload="} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func buildManifest(input provider.Input, snapshot Snapshot, requested domain.CustodyMode) ([]domain.MediaManifestItem, error) {
	if snapshot.Image.Original == nil && snapshot.Image.Thumbnail == nil {
		return nil, unavailable(domain.CapabilityOriginalMedia)
	}
	mediaID := strings.TrimSpace(snapshot.Image.MediaID)
	if mediaID == "" {
		mediaID = snapshot.PinID + ":image"
	}
	if !opaqueIDPattern.MatchString(mediaID) {
		return nil, permanent("invalid_response", "Pinterest image lacks a stable media identity", nil)
	}
	assetID, _ := domain.StableMediaAssetID(input.Source.SourceRegistrationID, "pinterest", mediaID, "pinterest:"+snapshot.PinID+":image", firstCandidateURL(snapshot.Image))
	asset := domain.MediaAsset{ID: assetID, SourceRegistrationID: input.Source.SourceRegistrationID,
		Provider: "pinterest", ProviderMediaID: mediaID, SourceMediaKey: "pinterest:" + snapshot.PinID + ":image",
		SourceLocator: firstCandidateURL(snapshot.Image), Kind: domain.MediaImage,
		AltText: strings.TrimSpace(snapshot.Image.AltText), SourceAuthority: AdapterName,
		DefaultCustody: defaultImageCustody(requested), MetadataJSON: "{}",
		CreatedAt: snapshot.ObservedAt.UTC(), UpdatedAt: snapshot.ObservedAt.UTC()}
	variants := make([]domain.AssetVariant, 0, 2)
	if snapshot.Image.Original != nil {
		variants = append(variants, makeVariant(assetID, snapshot.PinID, domain.VariantOriginal, *snapshot.Image.Original, requested, snapshot.ObservedAt))
	}
	if snapshot.Image.Thumbnail != nil {
		variants = append(variants, makeVariant(assetID, snapshot.PinID, domain.VariantThumbnail, *snapshot.Image.Thumbnail, requested, snapshot.ObservedAt))
	}
	return []domain.MediaManifestItem{{Asset: asset, Variants: variants,
		Attachment: domain.AttachmentRef{MediaAssetID: assetID, Role: domain.AttachmentPrimary,
			Position: 0, CreatedAt: snapshot.ObservedAt.UTC()}}}, nil
}

var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func makeVariant(assetID, pinID string, kind domain.AssetVariantKind, candidate VariantCandidate, requested domain.CustodyMode, observedAt time.Time) domain.AssetVariant {
	custody := defaultImageCustody(requested)
	state := domain.AcquisitionPending
	if custody == domain.CustodyReference {
		state = domain.AcquisitionReferenceOnly
	}
	var failure *domain.AssetFailure
	if candidate.Failure != nil {
		state = domain.AcquisitionFailed
		failure = &domain.AssetFailure{Code: candidate.Failure.Code,
			Message:   "Pinterest provider reported " + string(kind) + " variant unavailable",
			Retryable: candidate.Failure.Retryable}
	}
	variantIdentity := "pinterest:" + pinID + ":" + string(kind)
	variant := domain.AssetVariant{ID: domain.StableAssetVariantID(assetID, variantIdentity), MediaAssetID: assetID,
		VariantIdentity: variantIdentity, Kind: kind, SourceURL: strings.TrimSpace(candidate.SourceURL),
		SourceExpiresAt: candidate.SourceExpiresAt, MIMEType: strings.ToLower(strings.TrimSpace(candidate.MIMEType)),
		Width: candidate.Width, Height: candidate.Height, ByteSize: candidate.ByteSize,
		Custody: custody, AcquisitionState: state, Failure: failure, MetadataJSON: "{}",
		CreatedAt: observedAt.UTC(), UpdatedAt: observedAt.UTC()}
	if candidate.ExpectedDigest != nil {
		variant.ExpectedDigest, _ = candidate.ExpectedDigest.Normalized()
	}
	if custody == domain.CustodyReference {
		variant.Retention = domain.RetentionExternal
	} else if custody == domain.CustodyCache {
		variant.Retention = domain.RetentionCache
	} else {
		variant.Retention = domain.RetentionIndefinite
	}
	return variant
}

func defaultImageCustody(requested domain.CustodyMode) domain.CustodyMode {
	if requested.Valid() {
		return requested
	}
	return domain.CustodyMirror
}

func firstCandidateURL(image ImageSnapshot) string {
	if image.Original != nil && image.Original.SourceURL != "" {
		return strings.TrimSpace(image.Original.SourceURL)
	}
	if image.Thumbnail != nil {
		return strings.TrimSpace(image.Thumbnail.SourceURL)
	}
	return ""
}

func mediaReferences(manifest []domain.MediaManifestItem, capability domain.EnrichmentCapability) []string {
	var refs []string
	for _, item := range manifest {
		if capability == domain.CapabilityGalleryManifest {
			refs = append(refs, item.Asset.ID)
			continue
		}
		for _, variant := range item.Variants {
			if variant.AcquisitionState == domain.AcquisitionFailed {
				continue
			}
			if capability == domain.CapabilityOriginalMedia && variant.Kind == domain.VariantOriginal {
				refs = append(refs, variant.ID)
			}
			if capability == domain.CapabilityThumbnailOrPoster && (variant.Kind == domain.VariantThumbnail || variant.Kind == domain.VariantPoster || variant.Kind == domain.VariantPreview) {
				refs = append(refs, variant.ID)
			}
		}
	}
	return refs
}

func earliestReferenceExpiry(manifest []domain.MediaManifestItem, refs []string) time.Time {
	selected := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		selected[ref] = struct{}{}
	}
	var earliest time.Time
	for _, item := range manifest {
		for _, variant := range item.Variants {
			if _, ok := selected[variant.ID]; !ok || variant.SourceExpiresAt.IsZero() {
				continue
			}
			if earliest.IsZero() || variant.SourceExpiresAt.Before(earliest) {
				earliest = variant.SourceExpiresAt.UTC()
			}
		}
	}
	return earliest
}

func mediaUnavailable(manifest []domain.MediaManifestItem, capability domain.EnrichmentCapability) *provider.AdapterError {
	foundFailure, retryable := false, false
	for _, item := range manifest {
		for _, variant := range item.Variants {
			matches := capability == domain.CapabilityOriginalMedia && variant.Kind == domain.VariantOriginal ||
				capability == domain.CapabilityThumbnailOrPoster && (variant.Kind == domain.VariantThumbnail || variant.Kind == domain.VariantPoster || variant.Kind == domain.VariantPreview)
			if !matches || variant.AcquisitionState != domain.AcquisitionFailed || variant.Failure == nil {
				continue
			}
			foundFailure = true
			retryable = retryable || variant.Failure.Retryable
		}
	}
	if retryable {
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "media_temporarily_unavailable", Message: "Pinterest media representation is temporarily unavailable"}
	}
	if foundFailure {
		return permanent("media_unavailable", "Pinterest media representation is unavailable", nil)
	}
	return unavailable(capability)
}

func pinterestEntities(snapshot Snapshot) []provider.EntityValue {
	entities := []provider.EntityValue{{Kind: "pinterest_pin", Value: snapshot.PinID}}
	if snapshot.Creator != "" {
		entities = append(entities, provider.EntityValue{Kind: "creator", Value: snapshot.Creator})
	}
	if snapshot.Board != "" {
		entities = append(entities, provider.EntityValue{Kind: "pinterest_board", Value: snapshot.Board})
	}
	return entities
}

func normalizedSet(values []string) []string {
	seen := make(map[string]string, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "#"))
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; !ok {
			seen[key] = value
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out
}

func observation(capability domain.EnrichmentCapability, value any, snapshot Snapshot) (provider.ObservationDraft, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return provider.ObservationDraft{}, permanent("invalid_response", "Pinterest result could not be encoded", err)
	}
	draft := provider.ObservationDraft{Capability: capability, Attribution: domain.AttributionProvider,
		ValueJSON: string(raw), Confidence: snapshot.Confidence, ObservedAt: snapshot.ObservedAt.UTC()}
	if err := draft.Validate(); err != nil {
		return provider.ObservationDraft{}, permanent("unsafe_response", "Pinterest result failed typed safety validation", err)
	}
	return draft, nil
}

func unavailable(capability domain.EnrichmentCapability) *provider.AdapterError {
	return permanent("capability_unavailable", "Pinterest did not supply the claimed "+string(capability)+" capability", nil)
}

func permanent(code, message string, cause error) *provider.AdapterError {
	return &provider.AdapterError{Class: domain.EnrichmentErrorPermanent, Code: code, Message: message, Cause: cause}
}

func classifyClientError(err error) *provider.AdapterError {
	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "upstream_failure", Message: "Pinterest provider request failed", Cause: err}
	}
	switch clientErr.Kind {
	case FailureNotFound:
		return permanent("not_found", "Pinterest pin was not found", err)
	case FailureUnsupported:
		return permanent("unsupported", "Pinterest provider does not support this pin", err)
	case FailureAuthentication:
		return permanent("authentication_required", "Pinterest provider authentication is unavailable", err)
	case FailureRateLimited:
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "rate_limited", Message: "Pinterest provider rate limit was reached", Cause: err}
	default:
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "transient", Message: "Pinterest provider is temporarily unavailable", Cause: err}
	}
}

var (
	_ provider.CanonicalIdentityResolver = (*Adapter)(nil)
	_ provider.MetadataProvider          = (*Adapter)(nil)
	_ provider.MediaAcquirer             = (*Adapter)(nil)
)
