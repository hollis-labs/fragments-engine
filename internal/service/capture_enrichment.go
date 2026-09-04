package service

import (
	"encoding/json"
	"strings"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
)

// buildInitialCaptureEnrichment is the single initialization matrix for the
// twelve canonical capabilities. Only typed envelope evidence can produce a
// provided state. Extraction.ObservedCapabilities remains durable extraction
// provenance but is never treated as evidence by itself.
func buildInitialCaptureEnrichment(envelope capturecontract.CaptureEnvelope, fragment domain.Fragment, media []domain.MediaManifestItem, now time.Time) ([]domain.EnrichmentObservation, []domain.CapabilityCoverage) {
	producer := domain.AdapterVersion{Adapter: envelope.Extraction.Adapter, Version: envelope.Extraction.AdapterVersion}
	userProducer := domain.AdapterVersion{Adapter: envelope.Client.Kind, Version: envelope.Client.Version}
	observations := make([]domain.EnrichmentObservation, 0, 8)
	states := make(map[domain.EnrichmentCapability]domain.CapabilityState, len(domain.AllEnrichmentCapabilities()))
	details := make(map[domain.EnrichmentCapability]string, len(domain.AllEnrichmentCapabilities()))
	for _, capability := range domain.AllEnrichmentCapabilities() {
		states[capability] = domain.CoverageMissing
		details[capability] = "typed capture evidence was not provided"
	}
	add := func(capability domain.EnrichmentCapability, attribution domain.AttributionSource, adapter domain.AdapterVersion, value any, actorID string) {
		raw, _ := json.Marshal(value)
		observations = append(observations, domain.EnrichmentObservation{
			CaptureID: envelope.CaptureID, Capability: capability,
			Attribution: attribution, Producer: adapter,
			InputMaterialDigest: fragment.Revision.MaterialDigest,
			InputAssetDigests:   []domain.ObservationAssetDigest{},
			ValueJSON:           string(raw), ActorID: actorID,
			ObservedAt: envelope.CapturedAt.UTC(), AssertedAt: now.UTC(), CreatedAt: now.UTC(),
		})
		states[capability] = domain.CoverageProvided
		details[capability] = "provided by typed browser capture evidence"
	}
	if title := normalizeCaptureText(envelope.Document.Title); strings.TrimSpace(title) != "" {
		add(domain.CapabilityTitle, domain.AttributionSourceMaterial, producer, provider.TextValue{Text: title}, "")
	}
	if description := normalizeCaptureText(envelope.Document.Description); strings.TrimSpace(description) != "" {
		add(domain.CapabilityDescription, domain.AttributionSourceMaterial, producer, provider.TextValue{Text: description}, "")
	}
	if envelope.Document.Content != nil && strings.TrimSpace(envelope.Document.Content.Body) != "" {
		add(domain.CapabilityBody, domain.AttributionSourceMaterial, producer,
			provider.TextValue{Text: normalizeCaptureText(envelope.Document.Content.Body), Format: envelope.Document.Content.Format, Language: envelope.Document.Language}, "")
	}

	mediaAssetIDs := make([]string, 0, len(media))
	originalVariantIDs := make([]string, 0)
	thumbnailVariantIDs := make([]string, 0)
	transcriptVariantIDs := make([]string, 0)
	hasVisual, hasTimedMedia := false, false
	for index := range media {
		item := media[index]
		assetID, _ := domain.StableMediaAssetID(item.Asset.SourceRegistrationID, item.Asset.Provider,
			item.Asset.ProviderMediaID, item.Asset.SourceMediaKey, item.Asset.SourceLocator)
		mediaAssetIDs = append(mediaAssetIDs, assetID)
		switch item.Asset.Kind {
		case domain.MediaImage, domain.MediaVideo, domain.MediaDocument:
			hasVisual = true
		}
		if item.Asset.Kind == domain.MediaVideo || item.Asset.Kind == domain.MediaAudio {
			hasTimedMedia = true
		}
		for variantIndex := range item.Variants {
			variant := item.Variants[variantIndex]
			variantID := domain.StableAssetVariantID(assetID, variant.VariantIdentity)
			switch variant.Kind {
			case domain.VariantOriginal:
				originalVariantIDs = append(originalVariantIDs, variantID)
			case domain.VariantPreview, domain.VariantThumbnail, domain.VariantPoster:
				thumbnailVariantIDs = append(thumbnailVariantIDs, variantID)
			case domain.VariantSubtitles, domain.VariantTranscript:
				transcriptVariantIDs = append(transcriptVariantIDs, variantID)
			}
		}
	}
	if len(media) > 1 {
		add(domain.CapabilityGalleryManifest, domain.AttributionSourceMaterial, producer,
			provider.ReferenceListValue{MediaAssetIDs: mediaAssetIDs}, "")
	} else if strings.EqualFold(envelope.Source.Provider, "youtube") {
		states[domain.CapabilityGalleryManifest] = domain.CoverageNotApplicable
		details[domain.CapabilityGalleryManifest] = "youtube capture identifies one video item"
	}
	if len(originalVariantIDs) != 0 {
		add(domain.CapabilityOriginalMedia, domain.AttributionSourceMaterial, producer,
			provider.ReferenceListValue{AssetVariantIDs: originalVariantIDs}, "")
	}
	if len(thumbnailVariantIDs) != 0 {
		add(domain.CapabilityThumbnailOrPoster, domain.AttributionSourceMaterial, producer,
			provider.ReferenceListValue{AssetVariantIDs: thumbnailVariantIDs}, "")
	}
	if len(transcriptVariantIDs) != 0 {
		add(domain.CapabilityTranscript, domain.AttributionSourceMaterial, producer,
			provider.TranscriptValue{Kind: "variant_refs", AssetVariantIDs: transcriptVariantIDs}, "")
	} else if !hasTimedMedia && !strings.EqualFold(envelope.Source.Provider, "youtube") && !strings.EqualFold(envelope.Source.Provider, "instagram") {
		states[domain.CapabilityTranscript] = domain.CoverageNotApplicable
		details[domain.CapabilityTranscript] = "capture has no timed media"
	}
	if !hasVisual {
		states[domain.CapabilityOCR] = domain.CoverageNotApplicable
		states[domain.CapabilityVision] = domain.CoverageNotApplicable
		details[domain.CapabilityOCR] = "capture has no visual media"
		details[domain.CapabilityVision] = "capture has no visual media"
	}
	if len(envelope.Tags) != 0 {
		add(domain.CapabilityTags, domain.AttributionUser, userProducer,
			provider.StringListValue{Values: append([]string(nil), envelope.Tags...)}, envelope.PrincipalID)
	}

	coverage := make([]domain.CapabilityCoverage, 0, len(domain.AllEnrichmentCapabilities()))
	for _, capability := range domain.AllEnrichmentCapabilities() {
		coverage = append(coverage, domain.CapabilityCoverage{Capability: capability, State: states[capability],
			Detail: details[capability], Version: 1, UpdatedAt: now.UTC()})
	}
	return observations, coverage
}

func projectCapabilityCoverage(items []domain.CapabilityCoverage) []capturecontract.CapabilityCoverage {
	out := make([]capturecontract.CapabilityCoverage, 0, len(items))
	for _, item := range items {
		out = append(out, capturecontract.CapabilityCoverage{Capability: string(item.Capability),
			State: string(item.State), ObservationID: item.SelectedObservationID, Detail: item.Detail})
	}
	return out
}
