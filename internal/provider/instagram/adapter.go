// Package instagram implements provider-neutral Instagram identity, metadata,
// and media completion over the shared enrichment contracts.
package instagram

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
	AdapterName    = "fe.provider.instagram"
	AdapterVersion = "1.0.0"
)

type PostKind string

const (
	Post PostKind = "post"
	Reel PostKind = "reel"
)

type Client interface {
	Lookup(context.Context, LookupRequest) (Snapshot, error)
}

type LookupRequest struct {
	SubmittedURL  string   `json:"submitted_url"`
	ProviderID    string   `json:"provider_id"`
	Kind          PostKind `json:"kind"`
	SourceItemKey string   `json:"source_item_key"`
	Capability    string   `json:"capability"`
}

// AccessProfile describes only execution effects and credential references.
// The injected client, not this value, resolves credential bytes.
type AccessProfile struct {
	NetworkClass         provider.NetworkClass
	CredentialReferences []provider.CredentialReference
}

func PublicAccess() AccessProfile {
	return AccessProfile{NetworkClass: provider.NetworkPublicRead}
}

func AuthenticatedAccess(reference provider.CredentialReference) AccessProfile {
	return AccessProfile{NetworkClass: provider.NetworkAuthenticated,
		CredentialReferences: []provider.CredentialReference{reference}}
}

// Snapshot is a typed provider response. OEmbedJSON is narrowed immediately
// and never returned as raw data.
type Snapshot struct {
	ProviderID   string          `json:"provider_id"`
	Kind         PostKind        `json:"kind"`
	CanonicalURL string          `json:"canonical_url"`
	Caption      string          `json:"caption,omitempty"`
	Author       string          `json:"author,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Items        []MediaItem     `json:"items,omitempty"`
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
		return Snapshot{}, fmt.Errorf("decode Instagram snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return Snapshot{}, fmt.Errorf("decode Instagram snapshot: %w", err)
	}
	return snapshot, nil
}

// MediaItem has stable provider identity independent from its slice position.
// Slice order is the provider-observed carousel order.
type MediaItem struct {
	MediaID     string            `json:"media_id"`
	Kind        domain.MediaKind  `json:"kind"`
	AltText     string            `json:"alt_text,omitempty"`
	Width       int               `json:"width,omitempty"`
	Height      int               `json:"height,omitempty"`
	Duration    float64           `json:"duration_seconds,omitempty"`
	Original    *VariantCandidate `json:"original,omitempty"`
	Thumbnail   *VariantCandidate `json:"thumbnail,omitempty"`
	Poster      *VariantCandidate `json:"poster,omitempty"`
	Unavailable *VariantFailure   `json:"unavailable,omitempty"`
}

type VariantCandidate struct {
	SourceURL       string                `json:"source_url,omitempty"`
	SourceExpiresAt time.Time             `json:"source_expires_at,omitempty"`
	MIMEType        string                `json:"mime_type,omitempty"`
	Width           int                   `json:"width,omitempty"`
	Height          int                   `json:"height,omitempty"`
	Duration        float64               `json:"duration_seconds,omitempty"`
	ByteSize        int64                 `json:"byte_size,omitempty"`
	ExpectedDigest  *domain.ContentDigest `json:"expected_digest,omitempty"`
	Failure         *VariantFailure       `json:"failure,omitempty"`
}

// VariantFailure is a closed, message-free client result so response bodies or
// credential values cannot enter durable failure text.
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
	return "instagram client failure: " + string(e.Kind)
}

func (e *ClientError) Unwrap() error { return e.Cause }

type Adapter struct {
	client  Client
	profile AccessProfile
}

func New(client Client, profile AccessProfile) (*Adapter, error) {
	if client == nil {
		return nil, fmt.Errorf("instagram adapter requires a client")
	}
	if profile.NetworkClass != provider.NetworkPublicRead && profile.NetworkClass != provider.NetworkAuthenticated {
		return nil, fmt.Errorf("instagram access profile requires public_read or authenticated_read")
	}
	if profile.NetworkClass == provider.NetworkAuthenticated && len(profile.CredentialReferences) == 0 {
		return nil, fmt.Errorf("authenticated Instagram access requires a credential reference")
	}
	profile.CredentialReferences = append([]provider.CredentialReference(nil), profile.CredentialReferences...)
	adapter := &Adapter{client: client, profile: profile}
	if err := adapter.Descriptor().Validate(); err != nil {
		return nil, err
	}
	return adapter, nil
}

func (a *Adapter) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		Adapter: AdapterName, Version: AdapterVersion,
		SupportedProviders:   []string{"instagram"},
		SupportedSourceKinds: []string{"image", "gallery", "video", "article", "text"},
		SupportedMediaKinds:  []domain.MediaKind{domain.MediaImage, domain.MediaVideo},
		Capabilities: []domain.EnrichmentCapability{
			domain.CapabilityDescription, domain.CapabilityGalleryManifest,
			domain.CapabilityOriginalMedia, domain.CapabilityThumbnailOrPoster,
			domain.CapabilityTags, domain.CapabilityEntities,
		},
		InputSchema:  provider.SchemaRef{ID: "fe.provider.instagram.input", Version: "1"},
		OutputSchema: provider.SchemaRef{ID: "fe.provider.instagram.result", Version: "1"},
		Effects:      []provider.ExternalEffect{provider.EffectNetworkRead},
		NetworkClass: a.profile.NetworkClass,
		CredentialReferences: append([]provider.CredentialReference(nil),
			a.profile.CredentialReferences...),
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
	identity.Provider = "instagram"
	identity.ProviderItemID = snapshot.ProviderID
	identity.SourceItemKey = sourceItemKey(snapshot.Kind, snapshot.ProviderID)
	identity.CanonicalURL = canonicalURL(snapshot.Kind, snapshot.ProviderID)
	identity.SegmentKey = domain.DefaultSegmentKey
	identity.Canonicalizer = domain.AdapterVersion{Adapter: AdapterName, Version: AdapterVersion}
	draft, err := observation(domain.CapabilityEntities,
		provider.EntityListValue{Entities: []provider.EntityValue{{Kind: entityKind(snapshot.Kind), Value: snapshot.ProviderID}}}, snapshot)
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
	case domain.CapabilityDescription:
		if snapshot.Caption == "" {
			return provider.MetadataResult{}, unavailable(input.Capability)
		}
		value = provider.TextValue{Text: snapshot.Caption, Format: "plain_text"}
	case domain.CapabilityTags:
		if len(snapshot.Tags) == 0 {
			return provider.MetadataResult{}, unavailable(input.Capability)
		}
		value = provider.StringListValue{Values: snapshot.Tags}
	case domain.CapabilityEntities:
		entities := instagramEntities(snapshot)
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
	if request.MediaAssetID != "" {
		filtered := manifest[:0]
		for _, item := range manifest {
			if item.Asset.ID == request.MediaAssetID {
				filtered = append(filtered, item)
			}
		}
		manifest = filtered
		if len(manifest) == 0 {
			return provider.MediaResult{}, permanent("media_not_found", "requested media asset is not part of the Instagram item", nil)
		}
	}
	if request.VariantKind != "" {
		for index := range manifest {
			filtered := manifest[index].Variants[:0]
			for _, variant := range manifest[index].Variants {
				if variant.Kind == request.VariantKind {
					filtered = append(filtered, variant)
				}
			}
			manifest[index].Variants = filtered
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
		return provider.MediaResult{}, permanent("invalid_response", "Instagram media expiry is invalid", err)
	}
	return provider.MediaResult{Manifest: manifest, Observations: []provider.ObservationDraft{draft}}, nil
}

type urlReference struct {
	SubmittedURL string
	ProviderID   string
	Kind         PostKind
}

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,64}$`)

var instagramHosts = map[string]struct{}{
	"instagram.com": {}, "www.instagram.com": {}, "m.instagram.com": {},
	"instagr.am": {}, "www.instagr.am": {},
}

func ParseURL(raw string) (providerID string, kind PostKind, err error) {
	reference, err := parseURL(raw)
	return reference.ProviderID, reference.Kind, err
}

func parseURL(raw string) (urlReference, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Port() != "" || parsed.RawPath != "" {
		return urlReference{}, fmt.Errorf("unsupported Instagram URL")
	}
	if _, ok := instagramHosts[strings.ToLower(parsed.Hostname())]; !ok {
		return urlReference{}, fmt.Errorf("unsupported Instagram host")
	}
	segments := pathSegments(parsed.Path)
	if len(segments) != 2 || !providerIDPattern.MatchString(segments[1]) {
		return urlReference{}, fmt.Errorf("unsupported Instagram item URL")
	}
	var kind PostKind
	switch segments[0] {
	case "p":
		kind = Post
	case "reel", "reels":
		kind = Reel
	default:
		return urlReference{}, fmt.Errorf("unsupported Instagram item kind")
	}
	return urlReference{SubmittedURL: raw, ProviderID: segments[1], Kind: kind}, nil
}

func canonicalURL(kind PostKind, providerID string) string {
	path := "p"
	if kind == Reel {
		path = "reel"
	}
	return "https://www.instagram.com/" + path + "/" + providerID + "/"
}

func sourceItemKey(kind PostKind, providerID string) string {
	path := "p"
	if kind == Reel {
		path = "reel"
	}
	return "instagram:" + path + ":" + providerID
}

func entityKind(kind PostKind) string {
	if kind == Reel {
		return "instagram_reel"
	}
	return "instagram_post"
}

func pathSegments(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

func (a *Adapter) lookup(ctx context.Context, input provider.Input) (Snapshot, *provider.OEmbedMetadata, error) {
	if err := input.Validate(); err != nil {
		return Snapshot{}, nil, permanent("invalid_input", "Instagram input provenance is invalid", err)
	}
	if !strings.EqualFold(input.Source.Provider, "instagram") {
		return Snapshot{}, nil, permanent("provider_mismatch", "source provider is not Instagram", nil)
	}
	rawURL := input.Source.SubmittedURL
	if strings.TrimSpace(rawURL) == "" {
		rawURL = input.Source.CanonicalURL
	}
	reference, err := parseURL(rawURL)
	if err != nil {
		return Snapshot{}, nil, permanent("invalid_url", "source URL is not an allowlisted Instagram post or reel URL", err)
	}
	snapshot, err := a.client.Lookup(ctx, LookupRequest{SubmittedURL: reference.SubmittedURL,
		ProviderID: reference.ProviderID, Kind: reference.Kind,
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

func normalizeSnapshot(snapshot Snapshot, reference urlReference, inputProviderID string) (Snapshot, *provider.OEmbedMetadata, error) {
	snapshot.Tags = append([]string(nil), snapshot.Tags...)
	snapshot.Items = append([]MediaItem(nil), snapshot.Items...)
	snapshot.ProviderID = strings.TrimSpace(snapshot.ProviderID)
	if !providerIDPattern.MatchString(snapshot.ProviderID) || (snapshot.Kind != Post && snapshot.Kind != Reel) || snapshot.ObservedAt.IsZero() {
		return Snapshot{}, nil, permanent("invalid_response", "Instagram response lacks a valid identity, kind, or observation time", nil)
	}
	if snapshot.ProviderID != reference.ProviderID || snapshot.Kind != reference.Kind {
		return Snapshot{}, nil, permanent("identity_conflict", "Instagram response identity conflicts with the submitted URL", nil)
	}
	if strings.TrimSpace(inputProviderID) != "" && snapshot.ProviderID != strings.TrimSpace(inputProviderID) {
		return Snapshot{}, nil, permanent("identity_conflict", "Instagram response identity conflicts with source provenance", nil)
	}
	finalRef, err := parseURL(snapshot.CanonicalURL)
	if err != nil || finalRef.ProviderID != snapshot.ProviderID || finalRef.Kind != snapshot.Kind {
		return Snapshot{}, nil, permanent("invalid_response", "Instagram response did not provide an allowlisted final item URL", err)
	}
	snapshot.CanonicalURL = canonicalURL(snapshot.Kind, snapshot.ProviderID)
	snapshot.Caption = strings.TrimSpace(snapshot.Caption)
	snapshot.Author = strings.TrimSpace(snapshot.Author)
	snapshot.Tags = normalizedSet(snapshot.Tags)
	if snapshot.Confidence != nil && (*snapshot.Confidence < 0 || *snapshot.Confidence > 1) {
		return Snapshot{}, nil, permanent("invalid_response", "Instagram response confidence is invalid", nil)
	}
	var safeOEmbed *provider.OEmbedMetadata
	if len(snapshot.OEmbedJSON) != 0 && string(snapshot.OEmbedJSON) != "null" {
		parsed, err := provider.ParseOEmbedMetadata(snapshot.OEmbedJSON)
		if err != nil {
			return Snapshot{}, nil, permanent("invalid_response", "Instagram oEmbed metadata is invalid", err)
		}
		if parsed.ProviderName != "" && !strings.EqualFold(parsed.ProviderName, "Instagram") {
			return Snapshot{}, nil, permanent("provider_mismatch", "oEmbed metadata is not attributed to Instagram", nil)
		}
		if parsed.ProviderURL != "" && !allowlistedInstagramURL(parsed.ProviderURL) {
			return Snapshot{}, nil, permanent("provider_mismatch", "oEmbed provider URL is not Instagram", nil)
		}
		if parsed.AuthorURL != "" && !allowlistedInstagramURL(parsed.AuthorURL) {
			return Snapshot{}, nil, permanent("invalid_response", "Instagram oEmbed author URL is not allowlisted", nil)
		}
		if parsed.ThumbnailURL != "" && !safeMediaURL(parsed.ThumbnailURL) {
			return Snapshot{}, nil, permanent("invalid_response", "Instagram oEmbed thumbnail URL is not allowlisted", nil)
		}
		if unsafeText(parsed.ProviderName) || unsafeText(parsed.Title) || unsafeText(parsed.AuthorName) {
			return Snapshot{}, nil, permanent("unsafe_response", "Instagram oEmbed metadata contains executable markup", nil)
		}
		safeOEmbed = &parsed
		if snapshot.Caption == "" {
			snapshot.Caption = parsed.Title
		}
		if snapshot.Author == "" {
			snapshot.Author = parsed.AuthorName
		}
	}
	seen := make(map[string]struct{}, len(snapshot.Items))
	for index := range snapshot.Items {
		item := &snapshot.Items[index]
		item.MediaID = strings.TrimSpace(item.MediaID)
		if !opaqueIDPattern.MatchString(item.MediaID) || (item.Kind != domain.MediaImage && item.Kind != domain.MediaVideo) || item.Width < 0 || item.Height < 0 || item.Duration < 0 {
			return Snapshot{}, nil, permanent("invalid_response", "Instagram media item has invalid stable identity or dimensions", nil)
		}
		if _, duplicate := seen[item.MediaID]; duplicate {
			return Snapshot{}, nil, permanent("invalid_response", "Instagram response repeats a media identity", nil)
		}
		seen[item.MediaID] = struct{}{}
		if item.Unavailable != nil {
			if item.Original != nil || item.Thumbnail != nil || item.Poster != nil || !failureCodePattern.MatchString(item.Unavailable.Code) {
				return Snapshot{}, nil, permanent("invalid_response", "Instagram unavailable media item is ambiguous", nil)
			}
			continue
		}
		if item.Original == nil && item.Thumbnail == nil && item.Poster == nil {
			return Snapshot{}, nil, permanent("invalid_response", "Instagram media item has no variants or classified failure", nil)
		}
		if item.Kind == domain.MediaImage && item.Poster != nil {
			return Snapshot{}, nil, permanent("invalid_response", "Instagram image cannot carry a video poster", nil)
		}
		for label, candidate := range map[string]*VariantCandidate{"original": item.Original, "thumbnail": item.Thumbnail, "poster": item.Poster} {
			if err := validateCandidate(label, candidate, snapshot.ObservedAt); err != nil {
				return Snapshot{}, nil, permanent("invalid_response", "Instagram "+label+" candidate is invalid", err)
			}
		}
	}
	return snapshot, safeOEmbed, nil
}

func allowlistedInstagramURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	_, ok := instagramHosts[strings.ToLower(parsed.Hostname())]
	return ok
}

func validateCandidate(label string, candidate *VariantCandidate, observedAt time.Time) error {
	if candidate == nil {
		return nil
	}
	if candidate.Width < 0 || candidate.Height < 0 || candidate.Duration < 0 || candidate.ByteSize < 0 {
		return fmt.Errorf("%s dimensions, duration, and size cannot be negative", label)
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

var (
	failureCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	opaqueIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

func safeMediaURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "cdninstagram.com" || strings.HasSuffix(host, ".cdninstagram.com") ||
		host == "fbcdn.net" || strings.HasSuffix(host, ".fbcdn.net") {
		return true
	}
	_, ok := instagramHosts[host]
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
	if len(snapshot.Items) == 0 {
		return nil, unavailable(domain.CapabilityGalleryManifest)
	}
	manifest := make([]domain.MediaManifestItem, 0, len(snapshot.Items))
	for position, observed := range snapshot.Items {
		assetID, _ := domain.StableMediaAssetID(input.Source.SourceRegistrationID, "instagram", observed.MediaID,
			"instagram:"+snapshot.ProviderID+":media:"+observed.MediaID, firstCandidateURL(observed))
		asset := domain.MediaAsset{ID: assetID, SourceRegistrationID: input.Source.SourceRegistrationID,
			Provider: "instagram", ProviderMediaID: observed.MediaID,
			SourceMediaKey: "instagram:" + snapshot.ProviderID + ":media:" + observed.MediaID,
			SourceLocator:  firstCandidateURL(observed), Kind: observed.Kind,
			Width: observed.Width, Height: observed.Height, DurationSeconds: observed.Duration,
			AltText: strings.TrimSpace(observed.AltText), SourceAuthority: AdapterName,
			DefaultCustody: defaultCustody(observed.Kind, requested), MetadataJSON: "{}",
			CreatedAt: snapshot.ObservedAt.UTC(), UpdatedAt: snapshot.ObservedAt.UTC()}
		variants := make([]domain.AssetVariant, 0, 3)
		if observed.Unavailable != nil {
			variants = append(variants, unavailableVariant(assetID, snapshot.ProviderID, observed, snapshot.ObservedAt))
		} else {
			if observed.Original != nil {
				variants = append(variants, makeVariant(assetID, snapshot.ProviderID, observed, domain.VariantOriginal, *observed.Original, requested, snapshot.ObservedAt))
			}
			if observed.Thumbnail != nil {
				variants = append(variants, makeVariant(assetID, snapshot.ProviderID, observed, domain.VariantThumbnail, *observed.Thumbnail, requested, snapshot.ObservedAt))
			}
			if observed.Poster != nil {
				variants = append(variants, makeVariant(assetID, snapshot.ProviderID, observed, domain.VariantPoster, *observed.Poster, requested, snapshot.ObservedAt))
			}
		}
		role := domain.AttachmentPrimary
		if len(snapshot.Items) > 1 {
			role = domain.AttachmentGalleryItem
		}
		manifest = append(manifest, domain.MediaManifestItem{Asset: asset, Variants: variants,
			Attachment: domain.AttachmentRef{MediaAssetID: assetID, Role: role, Position: position,
				CreatedAt: snapshot.ObservedAt.UTC()}})
	}
	return manifest, nil
}

func defaultCustody(kind domain.MediaKind, requested domain.CustodyMode) domain.CustodyMode {
	if kind == domain.MediaVideo {
		return domain.CustodyReference
	}
	if requested.Valid() {
		return requested
	}
	return domain.CustodyMirror
}

func unavailableVariant(assetID, providerID string, item MediaItem, observedAt time.Time) domain.AssetVariant {
	candidate := VariantCandidate{Failure: item.Unavailable}
	return makeVariant(assetID, providerID, item, domain.VariantOriginal, candidate, "", observedAt)
}

func makeVariant(assetID, providerID string, item MediaItem, kind domain.AssetVariantKind, candidate VariantCandidate, requested domain.CustodyMode, observedAt time.Time) domain.AssetVariant {
	custody := defaultCustody(item.Kind, requested)
	if item.Kind == domain.MediaVideo && kind == domain.VariantPoster {
		if requested.Valid() {
			custody = requested
		} else {
			custody = domain.CustodyMirror
		}
	}
	state := domain.AcquisitionPending
	if custody == domain.CustodyReference {
		state = domain.AcquisitionReferenceOnly
	}
	var failure *domain.AssetFailure
	if candidate.Failure != nil {
		state = domain.AcquisitionFailed
		failure = &domain.AssetFailure{Code: candidate.Failure.Code,
			Message:   "Instagram provider reported " + string(kind) + " variant unavailable",
			Retryable: candidate.Failure.Retryable}
	}
	variantIdentity := "instagram:" + providerID + ":" + item.MediaID + ":" + string(kind)
	variant := domain.AssetVariant{ID: domain.StableAssetVariantID(assetID, variantIdentity), MediaAssetID: assetID,
		VariantIdentity: variantIdentity, Kind: kind, SourceURL: strings.TrimSpace(candidate.SourceURL),
		SourceExpiresAt: candidate.SourceExpiresAt, MIMEType: strings.ToLower(strings.TrimSpace(candidate.MIMEType)),
		Width: candidate.Width, Height: candidate.Height, DurationSeconds: candidate.Duration,
		ByteSize: candidate.ByteSize, Custody: custody, AcquisitionState: state,
		Failure: failure, MetadataJSON: "{}", CreatedAt: observedAt.UTC(), UpdatedAt: observedAt.UTC()}
	if candidate.ExpectedDigest != nil {
		variant.ExpectedDigest, _ = candidate.ExpectedDigest.Normalized()
	}
	switch custody {
	case domain.CustodyReference:
		variant.Retention = domain.RetentionExternal
	case domain.CustodyCache:
		variant.Retention = domain.RetentionCache
	default:
		variant.Retention = domain.RetentionIndefinite
	}
	return variant
}

func firstCandidateURL(item MediaItem) string {
	for _, candidate := range []*VariantCandidate{item.Original, item.Poster, item.Thumbnail} {
		if candidate != nil && candidate.SourceURL != "" {
			return strings.TrimSpace(candidate.SourceURL)
		}
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
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "media_temporarily_unavailable", Message: "Instagram media representation is temporarily unavailable"}
	}
	if foundFailure {
		return permanent("media_unavailable", "Instagram media representation is unavailable", nil)
	}
	return unavailable(capability)
}

func instagramEntities(snapshot Snapshot) []provider.EntityValue {
	entities := []provider.EntityValue{{Kind: entityKind(snapshot.Kind), Value: snapshot.ProviderID}}
	if snapshot.Author != "" {
		entities = append(entities, provider.EntityValue{Kind: "instagram_author", Value: snapshot.Author})
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
		return provider.ObservationDraft{}, permanent("invalid_response", "Instagram result could not be encoded", err)
	}
	draft := provider.ObservationDraft{Capability: capability, Attribution: domain.AttributionProvider,
		ValueJSON: string(raw), Confidence: snapshot.Confidence, ObservedAt: snapshot.ObservedAt.UTC()}
	if err := draft.Validate(); err != nil {
		return provider.ObservationDraft{}, permanent("unsafe_response", "Instagram result failed typed safety validation", err)
	}
	return draft, nil
}

func unavailable(capability domain.EnrichmentCapability) *provider.AdapterError {
	return permanent("capability_unavailable", "Instagram did not supply the claimed "+string(capability)+" capability", nil)
}

func permanent(code, message string, cause error) *provider.AdapterError {
	return &provider.AdapterError{Class: domain.EnrichmentErrorPermanent, Code: code, Message: message, Cause: cause}
}

func classifyClientError(err error) *provider.AdapterError {
	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "upstream_failure", Message: "Instagram provider request failed", Cause: err}
	}
	switch clientErr.Kind {
	case FailureNotFound:
		return permanent("not_found", "Instagram item was not found", err)
	case FailureUnsupported:
		return permanent("unsupported", "Instagram access does not support this item", err)
	case FailureAuthentication:
		return permanent("authentication_required", "Instagram provider authentication is unavailable", err)
	case FailureRateLimited:
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "rate_limited", Message: "Instagram provider rate limit was reached", Cause: err}
	default:
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "transient", Message: "Instagram provider is temporarily unavailable", Cause: err}
	}
}

var (
	_ provider.CanonicalIdentityResolver = (*Adapter)(nil)
	_ provider.MetadataProvider          = (*Adapter)(nil)
	_ provider.MediaAcquirer             = (*Adapter)(nil)
)
