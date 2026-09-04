package legacycapture

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
)

func buildEnrichment(captureID string, fragment domain.Fragment, media []domain.MediaManifestItem, sourceTags, deterministicTags, userTags []string, actorID string, now time.Time) ([]domain.EnrichmentObservation, []domain.CapabilityCoverage, error) {
	sourceProducer := fragment.SourceIdentity.SourceAdapter
	if sourceProducer.Adapter == "" || sourceProducer.Version == "" {
		sourceProducer = domain.AdapterVersion{Adapter: adapterKind, Version: adapterVersion}
	}
	compatibilityProducer := domain.AdapterVersion{Adapter: adapterKind, Version: adapterVersion}
	observedAt := fragment.Revision.ObservedAt
	if observedAt.IsZero() {
		observedAt = now
	}
	observations := make([]domain.EnrichmentObservation, 0, 8)
	provided := make(map[domain.EnrichmentCapability]bool)
	add := func(capability domain.EnrichmentCapability, attribution domain.AttributionSource, producer domain.AdapterVersion, value any, actor string) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("legacy capture: encode %s observation: %w", capability, err)
		}
		if err := provider.ValidateCapabilityValue(capability, raw); err != nil {
			return fmt.Errorf("legacy capture: validate %s observation: %w", capability, err)
		}
		observations = append(observations, domain.EnrichmentObservation{
			ID:         domain.DigestText(strings.Join([]string{"legacy-observation", captureID, string(capability), string(attribution), string(raw)}, "\n")),
			Capability: capability, Attribution: attribution, Producer: producer,
			InputMaterialDigest: fragment.Revision.MaterialDigest,
			InputAssetDigests:   []domain.ObservationAssetDigest{}, ValueJSON: string(raw),
			ActorID: actor, ObservedAt: observedAt, AssertedAt: now, CreatedAt: now,
		})
		provided[capability] = true
		return nil
	}
	if strings.TrimSpace(fragment.Revision.Title) != "" {
		if err := add(domain.CapabilityTitle, domain.AttributionSourceMaterial, sourceProducer, provider.TextValue{Text: fragment.Revision.Title}, ""); err != nil {
			return nil, nil, err
		}
	}
	if strings.TrimSpace(fragment.Revision.Description) != "" {
		if err := add(domain.CapabilityDescription, domain.AttributionSourceMaterial, sourceProducer, provider.TextValue{Text: fragment.Revision.Description}, ""); err != nil {
			return nil, nil, err
		}
	}
	if strings.TrimSpace(fragment.Revision.Content) != "" {
		if err := add(domain.CapabilityBody, domain.AttributionSourceMaterial, sourceProducer, provider.TextValue{Text: fragment.Revision.Content, Format: fragment.Revision.ContentFormat}, ""); err != nil {
			return nil, nil, err
		}
	}

	mediaIDs := make([]string, 0, len(media))
	variantIDs := make([]string, 0, len(media))
	for _, item := range media {
		if item.Asset.ID != "" {
			mediaIDs = append(mediaIDs, item.Asset.ID)
		}
		for _, variant := range item.Variants {
			if variant.ID != "" && (variant.Kind == domain.VariantOriginal || variant.Kind == domain.VariantAudio) {
				variantIDs = append(variantIDs, variant.ID)
			}
		}
	}
	if len(mediaIDs) > 1 {
		if err := add(domain.CapabilityGalleryManifest, domain.AttributionSourceMaterial, sourceProducer, provider.ReferenceListValue{MediaAssetIDs: mediaIDs}, ""); err != nil {
			return nil, nil, err
		}
	}
	if len(variantIDs) != 0 {
		if err := add(domain.CapabilityOriginalMedia, domain.AttributionSourceMaterial, sourceProducer, provider.ReferenceListValue{AssetVariantIDs: variantIDs}, ""); err != nil {
			return nil, nil, err
		}
	}
	if transcript := legacyTranscriptText(fragment); transcript != "" {
		if err := add(domain.CapabilityTranscript, domain.AttributionSourceMaterial, sourceProducer,
			provider.TranscriptValue{Kind: "text", Text: transcript, Format: "plain_text"}, ""); err != nil {
			return nil, nil, err
		}
	}
	if len(sourceTags) != 0 {
		if err := add(domain.CapabilityTags, domain.AttributionSourceMaterial, sourceProducer, provider.StringListValue{Values: sourceTags}, ""); err != nil {
			return nil, nil, err
		}
	}
	if len(deterministicTags) != 0 {
		if err := add(domain.CapabilityTags, domain.AttributionDeterministic, compatibilityProducer, provider.StringListValue{Values: deterministicTags}, ""); err != nil {
			return nil, nil, err
		}
	}
	if len(userTags) != 0 {
		if err := add(domain.CapabilityTags, domain.AttributionUser, compatibilityProducer, provider.StringListValue{Values: userTags}, actorID); err != nil {
			return nil, nil, err
		}
	}

	coverage := make([]domain.CapabilityCoverage, 0, len(domain.AllEnrichmentCapabilities()))
	for _, capability := range domain.AllEnrichmentCapabilities() {
		state := domain.CoverageMissing
		detail := "legacy adapter received no typed evidence"
		if provided[capability] {
			state = domain.CoverageProvided
			detail = "provided by typed legacy adapter evidence"
		}
		coverage = append(coverage, domain.CapabilityCoverage{Capability: capability, State: state, Detail: detail, Version: 1, UpdatedAt: now})
	}
	return observations, coverage, nil
}

func legacyTranscriptText(fragment domain.Fragment) string {
	var metadata map[string]any
	if json.Unmarshal([]byte(fragment.Revision.MetadataJSON), &metadata) != nil {
		return ""
	}
	value, _ := metadata["transcript_text"].(string)
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[Transcript unavailable") {
		return ""
	}
	return value
}
