package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
)

const adapterName = "youtube-provider"

type Adapter struct {
	client             Client
	descriptor         provider.Descriptor
	posterCustody      domain.CustodyMode
	transcriptCustody  domain.CustodyMode
	preferredLanguages []string
	now                func() time.Time
}

func New(client Client, options Options) (*Adapter, error) {
	if client == nil {
		return nil, fmt.Errorf("youtube provider client is required")
	}
	version := strings.TrimSpace(options.Version)
	if version == "" {
		version = "1.0.0"
	}
	posterCustody := options.PosterCustody
	if posterCustody == "" {
		posterCustody = domain.CustodyMirror
	}
	transcriptCustody := options.TranscriptCustody
	if transcriptCustody == "" {
		transcriptCustody = domain.CustodyMirror
	}
	for label, custody := range map[string]domain.CustodyMode{"poster": posterCustody, "transcript": transcriptCustody} {
		if custody != domain.CustodyReference && custody != domain.CustodyCache && custody != domain.CustodyMirror {
			return nil, fmt.Errorf("youtube %s custody must be reference, cache, or mirror", label)
		}
	}
	networkClass := options.NetworkClass
	if networkClass == "" {
		if len(options.CredentialReferences) == 0 {
			networkClass = provider.NetworkPublicRead
		} else {
			networkClass = provider.NetworkAuthenticated
		}
	}
	descriptor := provider.Descriptor{
		Adapter: adapterName, Version: version,
		SupportedProviders: []string{"youtube"}, SupportedSourceKinds: []string{"video"},
		SupportedMediaKinds: []domain.MediaKind{domain.MediaVideo},
		Capabilities: []domain.EnrichmentCapability{
			domain.CapabilityTitle, domain.CapabilityDescription,
			domain.CapabilityOriginalMedia, domain.CapabilityThumbnailOrPoster,
			domain.CapabilityTranscript, domain.CapabilityTags, domain.CapabilityEntities,
		},
		InputSchema:  provider.SchemaRef{ID: "fe.provider.input", Version: "1"},
		OutputSchema: provider.SchemaRef{ID: "fe.provider.youtube", Version: "1"},
		Effects:      []provider.ExternalEffect{provider.EffectNetworkRead}, NetworkClass: networkClass,
		CredentialReferences: append([]provider.CredentialReference(nil), options.CredentialReferences...),
	}
	if err := descriptor.Validate(); err != nil {
		return nil, fmt.Errorf("youtube provider descriptor: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Adapter{
		client: client, descriptor: descriptor, posterCustody: posterCustody,
		transcriptCustody:  transcriptCustody,
		preferredLanguages: append([]string(nil), options.PreferredLanguages...), now: now,
	}, nil
}

func (a *Adapter) Descriptor() provider.Descriptor {
	out := a.descriptor
	out.SupportedProviders = append([]string(nil), out.SupportedProviders...)
	out.SupportedSourceKinds = append([]string(nil), out.SupportedSourceKinds...)
	out.SupportedMediaKinds = append([]domain.MediaKind(nil), out.SupportedMediaKinds...)
	out.Capabilities = append([]domain.EnrichmentCapability(nil), out.Capabilities...)
	out.Effects = append([]provider.ExternalEffect(nil), out.Effects...)
	out.CredentialReferences = append([]provider.CredentialReference(nil), out.CredentialReferences...)
	return out
}

func (a *Adapter) ResolveCanonicalIdentity(_ context.Context, input provider.Input) (provider.CanonicalIdentityResult, error) {
	if err := input.Validate(); err != nil {
		return provider.CanonicalIdentityResult{}, permanent("invalid_input", "provider input is incomplete", err)
	}
	if input.Capability != domain.CapabilityEntities {
		return provider.CanonicalIdentityResult{}, permanent("capability_mismatch", "YouTube identity resolution emits only entities capability", nil)
	}
	id, err := resolveInputVideoID(input)
	if err != nil {
		return provider.CanonicalIdentityResult{}, permanent("invalid_video_identity", err.Error(), err)
	}
	canonicalURL, _ := CanonicalWatchURL(id)
	identity := input.Source
	identity.Provider = "youtube"
	identity.ProviderItemID = id
	if strings.TrimSpace(identity.SourceLocator) == "" {
		identity.SourceLocator = canonicalURL
	}
	identity.CanonicalURL = canonicalURL
	identity.Canonicalizer = domain.AdapterVersion{Adapter: adapterName, Version: a.descriptor.Version}
	observation, err := a.observation(domain.CapabilityEntities, provider.EntityListValue{Entities: []provider.EntityValue{{Kind: "youtube_video_id", Value: id}}})
	if err != nil {
		return provider.CanonicalIdentityResult{}, err
	}
	return provider.CanonicalIdentityResult{Identity: identity, Observation: observation}, nil
}

func (a *Adapter) ProvideMetadata(ctx context.Context, input provider.Input) (provider.MetadataResult, error) {
	if err := input.Validate(); err != nil {
		return provider.MetadataResult{}, permanent("invalid_input", "provider input is incomplete", err)
	}
	switch input.Capability {
	case domain.CapabilityTitle, domain.CapabilityDescription, domain.CapabilityTags, domain.CapabilityEntities:
	default:
		return provider.MetadataResult{}, permanent("unsupported_capability", "YouTube metadata does not provide the requested capability", nil)
	}
	video, _, err := a.lookupVideo(ctx, input)
	if err != nil {
		return provider.MetadataResult{}, err
	}
	var value any
	switch input.Capability {
	case domain.CapabilityTitle:
		if strings.TrimSpace(video.Title) == "" {
			return provider.MetadataResult{}, permanent("metadata_missing", "YouTube title is unavailable", nil)
		}
		value = provider.TextValue{Text: strings.TrimSpace(video.Title)}
	case domain.CapabilityDescription:
		if strings.TrimSpace(video.Description) == "" {
			return provider.MetadataResult{}, permanent("metadata_missing", "YouTube description is unavailable", nil)
		}
		value = provider.TextValue{Text: strings.TrimSpace(video.Description), Format: "plain_text"}
	case domain.CapabilityTags:
		tags, err := normalizeTags(video.Tags)
		if err != nil || len(tags) == 0 {
			return provider.MetadataResult{}, permanent("metadata_missing", "YouTube tags are unavailable or invalid", err)
		}
		value = provider.StringListValue{Values: tags}
	case domain.CapabilityEntities:
		entities, err := metadataEntities(video)
		if err != nil || len(entities) == 0 {
			return provider.MetadataResult{}, permanent("metadata_missing", "YouTube channel and chapter metadata is unavailable or invalid", err)
		}
		value = provider.EntityListValue{Entities: entities}
	}
	observation, err := a.observation(input.Capability, value)
	if err != nil {
		return provider.MetadataResult{}, permanent("unsafe_metadata", "YouTube metadata failed typed validation", err)
	}
	return provider.MetadataResult{Observations: []provider.ObservationDraft{observation}}, nil
}

func (a *Adapter) AcquireMedia(ctx context.Context, request provider.MediaRequest) (provider.MediaResult, error) {
	if err := request.Input.Validate(); err != nil {
		return provider.MediaResult{}, permanent("invalid_input", "media request is incomplete", err)
	}
	if request.VariantKind == "" {
		switch request.Input.Capability {
		case domain.CapabilityOriginalMedia:
			request.VariantKind = domain.VariantOriginal
		case domain.CapabilityThumbnailOrPoster:
			request.VariantKind = domain.VariantPoster
		default:
			return provider.MediaResult{}, permanent("unsupported_capability", "YouTube media acquisition cannot infer a variant for this capability", nil)
		}
	}
	id, err := resolveInputVideoID(request.Input)
	if err != nil {
		return provider.MediaResult{}, permanent("invalid_video_identity", err.Error(), err)
	}
	canonicalURL, _ := CanonicalWatchURL(id)
	stableAssetID, _ := domain.StableMediaAssetID(request.Input.Source.SourceRegistrationID, "youtube", id, "youtube:"+id, canonicalURL)
	if supplied := strings.TrimSpace(request.MediaAssetID); supplied != "" && supplied != stableAssetID {
		return provider.MediaResult{}, permanent("media_asset_identity_mismatch", "media asset ID does not match the resolved YouTube video", nil)
	}
	request.MediaAssetID = stableAssetID
	asset := domain.MediaAsset{
		ID: request.MediaAssetID, SourceRegistrationID: request.Input.Source.SourceRegistrationID,
		Provider: "youtube", ProviderMediaID: id, SourceMediaKey: "youtube:" + id,
		SourceLocator: canonicalURL, Kind: domain.MediaVideo,
		SourceAuthority: "youtube", DefaultCustody: domain.CustodyReference,
	}
	item := domain.MediaManifestItem{Asset: asset, Attachment: domain.AttachmentRef{
		FragmentRevisionID: request.Input.FragmentRevisionID, MediaAssetID: request.MediaAssetID,
		Role: domain.AttachmentPrimary, Position: 0, SourceContext: "youtube_provider",
	}}
	switch request.VariantKind {
	case domain.VariantOriginal:
		if request.Input.Capability != domain.CapabilityOriginalMedia {
			return provider.MediaResult{}, permanent("capability_mismatch", "original media request is not pinned to original_media capability", nil)
		}
		if request.RequestedCustody != "" && request.RequestedCustody != domain.CustodyReference {
			return provider.MediaResult{}, permanent("automatic_video_download_disabled", "YouTube video bytes require an explicit asset acquisition command", nil)
		}
		variant := domain.AssetVariant{
			MediaAssetID: request.MediaAssetID, VariantIdentity: "video:reference",
			Kind: domain.VariantOriginal, SourceURL: canonicalURL,
			Custody: domain.CustodyReference, AcquisitionState: domain.AcquisitionReferenceOnly,
			Retention: domain.RetentionExternal,
		}
		variant.ID = domain.StableAssetVariantID(request.MediaAssetID, variant.VariantIdentity)
		item.Variants = []domain.AssetVariant{variant}
		observation, err := a.observation(domain.CapabilityOriginalMedia, provider.ReferenceListValue{AssetVariantIDs: []string{variant.ID}})
		if err != nil {
			return provider.MediaResult{}, err
		}
		return provider.MediaResult{Manifest: []domain.MediaManifestItem{item}, Observations: []provider.ObservationDraft{observation}}, nil
	case domain.VariantPoster, domain.VariantThumbnail:
		if request.Input.Capability != domain.CapabilityThumbnailOrPoster {
			return provider.MediaResult{}, permanent("capability_mismatch", "poster request is not pinned to thumbnail_or_poster capability", nil)
		}
		video, _, err := a.lookupVideo(ctx, request.Input)
		if err != nil {
			return provider.MediaResult{}, err
		}
		duration := video.DurationSeconds
		if duration < 0 || math.Signbit(duration) || math.IsNaN(duration) || math.IsInf(duration, 0) {
			duration = 0
		}
		item.Asset.DurationSeconds = duration
		custody := request.RequestedCustody
		if custody == "" {
			custody = a.posterCustody
		}
		if custody != domain.CustodyReference && custody != domain.CustodyCache && custody != domain.CustodyMirror {
			return provider.MediaResult{}, permanent("invalid_custody", "provider posters support reference, cache, or mirror custody", nil)
		}
		variants, err := posterVariants(request.MediaAssetID, video.Posters, request.VariantKind, custody)
		if err != nil {
			return provider.MediaResult{}, permanent("invalid_poster", "YouTube poster metadata is invalid", err)
		}
		item.Variants = variants
		ids := make([]string, 0, len(variants))
		for _, variant := range variants {
			ids = append(ids, variant.ID)
		}
		observation, err := a.observation(domain.CapabilityThumbnailOrPoster, provider.ReferenceListValue{AssetVariantIDs: ids})
		if err != nil {
			return provider.MediaResult{}, err
		}
		return provider.MediaResult{Manifest: []domain.MediaManifestItem{item}, Observations: []provider.ObservationDraft{observation}}, nil
	default:
		return provider.MediaResult{}, permanent("unsupported_variant", "YouTube provider supports original references and poster variants", nil)
	}
}

func (a *Adapter) ProvideTranscript(ctx context.Context, input provider.Input) (provider.TranscriptResult, error) {
	acquisition, err := a.AcquireTranscript(ctx, input)
	return acquisition.Result, err
}

// AcquireTranscript is the content-bearing composition seam used by a worker:
// the W2.2 contract result remains unchanged, while verified bytes can be sent
// through MediaService before publishing the returned observation.
func (a *Adapter) AcquireTranscript(ctx context.Context, input provider.Input) (TranscriptAcquisition, error) {
	if err := input.Validate(); err != nil {
		return TranscriptAcquisition{}, permanent("invalid_input", "provider input is incomplete", err)
	}
	if input.Capability != domain.CapabilityTranscript {
		return TranscriptAcquisition{}, permanent("unsupported_capability", "transcript provider requires transcript capability", nil)
	}
	id, err := resolveInputVideoID(input)
	if err != nil {
		return TranscriptAcquisition{}, permanent("invalid_video_identity", err.Error(), err)
	}
	transcript, err := a.client.LookupTranscript(ctx, id, append([]string(nil), a.preferredLanguages...))
	if err != nil {
		return TranscriptAcquisition{}, classifyClientError(err, "transcript_unavailable", "YouTube transcript could not be acquired")
	}
	if transcript.VideoID != id || !videoIDPattern.MatchString(transcript.VideoID) {
		return TranscriptAcquisition{}, permanent("provider_identity_mismatch", "YouTube transcript belongs to a different video", nil)
	}
	transcript.TrackID = strings.TrimSpace(transcript.TrackID)
	transcript.Language = strings.TrimSpace(transcript.Language)
	transcript.Format = strings.ToLower(strings.TrimSpace(transcript.Format))
	transcript.Text = strings.TrimSpace(transcript.Text)
	if transcript.TrackID == "" || transcript.Language == "" || transcript.Text == "" ||
		len(transcript.TrackID) > 128 || len(transcript.Language) > 64 ||
		strings.ContainsAny(transcript.TrackID, "\t\r\n") || strings.ContainsAny(transcript.Language, "\t\r\n") ||
		(transcript.Kind != domain.VariantTranscript && transcript.Kind != domain.VariantSubtitles) {
		return TranscriptAcquisition{}, permanent("invalid_transcript", "YouTube transcript identity, kind, language, and text are required", nil)
	}
	switch transcript.Format {
	case "", "plain_text", "webvtt", "srt":
	default:
		return TranscriptAcquisition{}, permanent("invalid_transcript", "YouTube transcript format is unsupported", nil)
	}
	if transcript.SourceURL != "" && !safeHTTPURL(transcript.SourceURL) {
		return TranscriptAcquisition{}, permanent("invalid_transcript_url", "YouTube transcript source URL is unsafe", nil)
	}
	if !transcript.SourceExpiresAt.IsZero() && transcript.SourceURL == "" {
		return TranscriptAcquisition{}, permanent("invalid_transcript_expiry", "transcript expiry requires a source URL", nil)
	}
	content := []byte(transcript.Text)
	digest := domain.ContentDigest{Algorithm: "sha256", Value: domain.DigestText(transcript.Text)}
	canonicalURL, _ := CanonicalWatchURL(id)
	assetID, _ := domain.StableMediaAssetID(input.Source.SourceRegistrationID, "youtube", id, "youtube:"+id, canonicalURL)
	asset := domain.MediaAsset{
		ID: assetID, SourceRegistrationID: input.Source.SourceRegistrationID,
		Provider: "youtube", ProviderMediaID: id, SourceMediaKey: "youtube:" + id,
		SourceLocator: canonicalURL, Kind: domain.MediaVideo,
		SourceAuthority: "youtube", DefaultCustody: domain.CustodyReference,
	}
	variant := domain.AssetVariant{
		MediaAssetID: assetID, VariantIdentity: "caption-track:" + transcript.TrackID,
		Kind: transcript.Kind, SourceURL: strings.TrimSpace(transcript.SourceURL),
		SourceExpiresAt: transcript.SourceExpiresAt.UTC(), MIMEType: transcriptMIME(transcript.Format),
		ByteSize: int64(len(content)), ExpectedDigest: digest, Custody: a.transcriptCustody,
		Retention: retentionForCustody(a.transcriptCustody),
	}
	if a.transcriptCustody == domain.CustodyReference {
		if variant.SourceURL == "" {
			return TranscriptAcquisition{}, permanent("invalid_transcript_custody", "reference transcript custody requires a source URL", nil)
		}
		variant.AcquisitionState = domain.AcquisitionReferenceOnly
	} else {
		variant.AcquisitionState = domain.AcquisitionPending
	}
	variant.ID = domain.StableAssetVariantID(assetID, variant.VariantIdentity)
	observation, err := a.observation(domain.CapabilityTranscript, provider.TranscriptValue{
		Kind: "text", Text: transcript.Text, Format: transcript.Format, Language: transcript.Language,
	})
	if err != nil {
		return TranscriptAcquisition{}, permanent("unsafe_transcript", "YouTube transcript failed typed validation", err)
	}
	return TranscriptAcquisition{Asset: asset, Result: provider.TranscriptResult{Variant: variant, Observation: observation}, Content: content}, nil
}

func (a *Adapter) ProvidePlayback(_ context.Context, input provider.Input) (provider.PlaybackResult, error) {
	if err := input.Validate(); err != nil {
		return provider.PlaybackResult{}, permanent("invalid_input", "provider input is incomplete", err)
	}
	if input.Capability != domain.CapabilityEntities {
		return provider.PlaybackResult{}, permanent("capability_mismatch", "YouTube playback emits only entities capability", nil)
	}
	id, err := resolveInputVideoID(input)
	if err != nil {
		return provider.PlaybackResult{}, permanent("invalid_video_identity", err.Error(), err)
	}
	spec := provider.PlaybackSpec{Kind: provider.PlaybackProviderEmbed, Provider: "youtube", ProviderItemID: id}
	if err := provider.ValidatePlaybackSpec(spec); err != nil {
		return provider.PlaybackResult{}, permanent("invalid_playback", "YouTube playback identity is invalid", err)
	}
	observation, err := a.observation(domain.CapabilityEntities, provider.EntityListValue{Entities: []provider.EntityValue{{Kind: "youtube_playback_id", Value: id}}})
	if err != nil {
		return provider.PlaybackResult{}, err
	}
	return provider.PlaybackResult{Spec: spec, Observation: observation}, nil
}

func (a *Adapter) lookupVideo(ctx context.Context, input provider.Input) (Video, string, error) {
	id, err := resolveInputVideoID(input)
	if err != nil {
		return Video{}, "", permanent("invalid_video_identity", err.Error(), err)
	}
	video, err := a.client.LookupVideo(ctx, id)
	if err != nil {
		return Video{}, "", classifyClientError(err, "metadata_unavailable", "YouTube video metadata could not be acquired")
	}
	if video.ID != id || !videoIDPattern.MatchString(video.ID) {
		return Video{}, "", permanent("provider_identity_mismatch", "YouTube provider returned a different video identity", nil)
	}
	return video, id, nil
}

func (a *Adapter) observation(capability domain.EnrichmentCapability, value any) (provider.ObservationDraft, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return provider.ObservationDraft{}, err
	}
	draft := provider.ObservationDraft{Capability: capability, Attribution: domain.AttributionProvider, ValueJSON: string(raw), ObservedAt: a.now().UTC()}
	if err := draft.Validate(); err != nil {
		return provider.ObservationDraft{}, err
	}
	return draft, nil
}

func metadataEntities(video Video) ([]provider.EntityValue, error) {
	duration := video.DurationSeconds
	if duration < 0 || math.Signbit(duration) || math.IsNaN(duration) || math.IsInf(duration, 0) {
		duration = 0
	}
	var entities []provider.EntityValue
	if id := strings.TrimSpace(video.ChannelID); id != "" {
		if strings.ContainsAny(id, "\t\r\n") {
			return nil, fmt.Errorf("channel ID contains control separators")
		}
		entities = append(entities, provider.EntityValue{Kind: "youtube_channel_id", Value: id})
	}
	if name := strings.TrimSpace(video.ChannelName); name != "" {
		if strings.ContainsAny(name, "\t\r\n") {
			return nil, fmt.Errorf("channel name contains control separators")
		}
		entities = append(entities, provider.EntityValue{Kind: "youtube_channel", Value: name})
	}
	last := -1.0
	for _, chapter := range video.Chapters {
		title := strings.TrimSpace(chapter.Title)
		millis := chapter.StartSeconds * 1000
		if title == "" || strings.ContainsAny(title, "\t\r\n") || chapter.StartSeconds < 0 || math.Signbit(chapter.StartSeconds) ||
			math.IsNaN(chapter.StartSeconds) || math.IsInf(chapter.StartSeconds, 0) || math.Abs(millis-math.Round(millis)) > 1e-7 || chapter.StartSeconds <= last ||
			(duration > 0 && chapter.StartSeconds >= duration) {
			return nil, fmt.Errorf("chapters require non-empty titles and finite, millisecond-precise, strictly increasing in-range times")
		}
		last = chapter.StartSeconds
		// Stable representation: decimal seconds with millisecond precision,
		// one TAB separator, then the plain-text title. It is not JSON or a
		// hidden provider payload and preserves provider order.
		value := strconv.FormatFloat(chapter.StartSeconds, 'f', 3, 64) + "\t" + title
		entities = append(entities, provider.EntityValue{Kind: "youtube_chapter", Value: value})
	}
	return entities, nil
}

func normalizeTags(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, "\r\n\t") {
			return nil, fmt.Errorf("tag is blank or contains control separators")
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func posterVariants(assetID string, posters []Poster, kind domain.AssetVariantKind, custody domain.CustodyMode) ([]domain.AssetVariant, error) {
	if len(posters) == 0 {
		return nil, fmt.Errorf("no poster candidates")
	}
	sorted := append([]Poster(nil), posters...)
	seen := make(map[string]struct{}, len(sorted))
	for i := range sorted {
		sorted[i].ID = strings.TrimSpace(sorted[i].ID)
		sorted[i].URL = strings.TrimSpace(sorted[i].URL)
		sorted[i].MIMEType = strings.ToLower(strings.TrimSpace(sorted[i].MIMEType))
		if sorted[i].ID == "" || strings.ContainsAny(sorted[i].ID, "\r\n\t") || !safeHTTPURL(sorted[i].URL) ||
			sorted[i].Width <= 0 || sorted[i].Height <= 0 || !strings.HasPrefix(sorted[i].MIMEType, "image/") {
			return nil, fmt.Errorf("poster requires safe identity, URL, image MIME type, and positive dimensions")
		}
		if _, duplicate := seen[sorted[i].ID]; duplicate {
			return nil, fmt.Errorf("duplicate poster identity %q", sorted[i].ID)
		}
		seen[sorted[i].ID] = struct{}{}
	}
	sort.Slice(sorted, func(i, j int) bool {
		left, right := int64(sorted[i].Width)*int64(sorted[i].Height), int64(sorted[j].Width)*int64(sorted[j].Height)
		if left == right {
			return sorted[i].ID < sorted[j].ID
		}
		return left > right
	})
	variants := make([]domain.AssetVariant, 0, len(sorted))
	for _, item := range sorted {
		state := domain.AcquisitionPending
		if custody == domain.CustodyReference {
			state = domain.AcquisitionReferenceOnly
		}
		variant := domain.AssetVariant{
			MediaAssetID: assetID, VariantIdentity: "image-candidate:" + item.ID,
			Kind: kind, SourceURL: item.URL, SourceExpiresAt: item.SourceExpiresAt.UTC(), MIMEType: item.MIMEType,
			Width: item.Width, Height: item.Height, Custody: custody,
			AcquisitionState: state, Retention: retentionForCustody(custody),
		}
		variant.ID = domain.StableAssetVariantID(assetID, variant.VariantIdentity)
		variants = append(variants, variant)
	}
	return variants, nil
}

func retentionForCustody(custody domain.CustodyMode) domain.RetentionPolicy {
	switch custody {
	case domain.CustodyReference:
		return domain.RetentionExternal
	case domain.CustodyCache:
		return domain.RetentionCache
	default:
		return domain.RetentionIndefinite
	}
}

func transcriptMIME(format string) string {
	switch format {
	case "webvtt":
		return "text/vtt"
	case "srt":
		return "application/x-subrip"
	default:
		return "text/plain"
	}
}

func safeHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed != nil && parsed.User == nil && parsed.Port() == "" &&
		(parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Hostname() != ""
}

func permanent(code, message string, cause error) *provider.AdapterError {
	return &provider.AdapterError{Class: domain.EnrichmentErrorPermanent, Code: code, Message: message, Cause: cause}
}

func classifyClientError(err error, code, message string) *provider.AdapterError {
	var typed *provider.AdapterError
	if errors.As(err, &typed) && typed.Validate() == nil {
		return typed
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: "request_interrupted", Message: "YouTube provider request was interrupted", Cause: err}
	}
	return &provider.AdapterError{Class: domain.EnrichmentErrorRetryable, Code: code, Message: message, Cause: err}
}

var (
	_ provider.CanonicalIdentityResolver = (*Adapter)(nil)
	_ provider.MetadataProvider          = (*Adapter)(nil)
	_ provider.MediaAcquirer             = (*Adapter)(nil)
	_ provider.TranscriptProvider        = (*Adapter)(nil)
	_ provider.PlaybackProvider          = (*Adapter)(nil)
)
