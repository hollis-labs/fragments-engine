package provider

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestDescriptorValidationAndSupport(t *testing.T) {
	descriptor := validDescriptor()
	if err := descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
	if !descriptor.Supports(domain.CapabilityTranscript, "youtube", "video", []domain.MediaKind{domain.MediaVideo}) {
		t.Fatal("valid descriptor did not advertise supported work")
	}
	if descriptor.Supports(domain.CapabilitySummary, "youtube", "video", []domain.MediaKind{domain.MediaVideo}) {
		t.Fatal("descriptor advertised undeclared capability")
	}

	cases := []struct {
		name string
		edit func(*Descriptor)
	}{
		{"missing version", func(d *Descriptor) { d.Version = "" }},
		{"missing input schema", func(d *Descriptor) { d.InputSchema = SchemaRef{} }},
		{"bad effect", func(d *Descriptor) { d.Effects = []ExternalEffect{"shell"} }},
		{"network mismatch", func(d *Descriptor) { d.NetworkClass = NetworkNone }},
		{"credential value-like name", func(d *Descriptor) { d.CredentialReferences[0].Name = "TOKEN=secret" }},
		{"credential without auth", func(d *Descriptor) { d.NetworkClass = NetworkPublicRead }},
		{"duplicate capability", func(d *Descriptor) { d.Capabilities = append(d.Capabilities, domain.CapabilityTranscript) }},
		{"none plus network", func(d *Descriptor) { d.Effects = append(d.Effects, EffectNone) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := validDescriptor()
			tc.edit(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid descriptor accepted")
			}
		})
	}
}

func TestProviderInputRequiresRevisionAndSourceProvenance(t *testing.T) {
	input := Input{FragmentID: "fragment", FragmentRevisionID: "revision", Capability: domain.CapabilityTranscript,
		MaterialDigest: strings.Repeat("a", 64), Source: domain.SourceIdentity{SourceRegistrationID: "browser", Provider: "youtube",
			SourceItemKey: "youtube:id", SegmentKey: "root", SourceAdapter: domain.AdapterVersion{Adapter: "clipper", Version: "1"},
			Canonicalizer: domain.AdapterVersion{Adapter: "youtube", Version: "1"}},
		AssetDigests: []domain.ObservationAssetDigest{{AssetVariantID: "variant", Digest: domain.ContentDigest{Algorithm: "sha256", Value: strings.Repeat("b", 64)}}}}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	input.Source.Canonicalizer.Version = ""
	if err := input.Validate(); err == nil {
		t.Fatal("incomplete source provenance accepted")
	}
}

func TestCapabilityValuesAreStrictTypedAndNonHTML(t *testing.T) {
	values := map[domain.EnrichmentCapability]any{
		domain.CapabilityTitle:             TextValue{Text: "title"},
		domain.CapabilityDescription:       TextValue{Text: "description", Format: "plain_text"},
		domain.CapabilityBody:              TextValue{Text: "# body", Format: "markdown"},
		domain.CapabilityGalleryManifest:   ReferenceListValue{AttachmentIDs: []string{"attachment-1"}},
		domain.CapabilityOriginalMedia:     ReferenceListValue{AssetVariantIDs: []string{"variant-1"}},
		domain.CapabilityThumbnailOrPoster: ReferenceListValue{AssetVariantIDs: []string{"variant-2"}},
		domain.CapabilityTranscript:        TranscriptValue{Kind: "text", Text: "spoken words", Format: "plain_text", Language: "en"},
		domain.CapabilityOCR:               TextValue{Text: "letters", Format: "plain_text"},
		domain.CapabilityVision:            TextValue{Text: "an image", Format: "plain_text"},
		domain.CapabilitySummary:           TextValue{Text: "summary", Format: "plain_text"},
		domain.CapabilityTags:              StringListValue{Values: []string{"one"}},
		domain.CapabilityEntities:          EntityListValue{Entities: []EntityValue{{Kind: "channel", Value: "Example"}}},
	}
	for capability, value := range values {
		raw, _ := json.Marshal(value)
		if err := ValidateCapabilityValue(capability, raw); err != nil {
			t.Fatalf("%s value rejected: %v", capability, err)
		}
	}
	for _, raw := range []string{
		`{"text":"<script>alert(1)</script>","format":"html"}`,
		`{"text":"ok","html":"<iframe src=x></iframe>"}`,
		`{"attachment_ids":["same","same"]}`,
		`{"attachment_ids":["a"],"asset_variant_ids":["b"]}`,
		`{"asset_variant_ids":[""]}`,
		`{"text":"ok"} {"text":"second"}`,
	} {
		capability := domain.CapabilityBody
		if strings.Contains(raw, "attachment_ids") {
			capability = domain.CapabilityGalleryManifest
		} else if strings.Contains(raw, "asset_variant_ids") {
			capability = domain.CapabilityOriginalMedia
		}
		if err := ValidateCapabilityValue(capability, []byte(raw)); err == nil {
			t.Fatalf("unsafe/ambiguous value accepted: %s", raw)
		}
	}
	markdown, _ := json.Marshal(TextValue{Text: "See <https://example.com> for details.", Format: "markdown"})
	if err := ValidateCapabilityValue(domain.CapabilityBody, markdown); err != nil {
		t.Fatalf("valid Markdown autolink rejected: %v", err)
	}
	for _, value := range []TranscriptValue{
		{Kind: "variant_refs", AssetVariantIDs: []string{"variant-1"}},
		{Kind: "text", Text: "caption", Format: "plain_text"},
	} {
		raw, _ := json.Marshal(value)
		if err := ValidateCapabilityValue(domain.CapabilityTranscript, raw); err != nil {
			t.Fatalf("valid transcript union rejected: %v", err)
		}
	}
}

func TestTextFormatsAreClosedAndNeverHTML(t *testing.T) {
	for _, tc := range []struct {
		capability domain.EnrichmentCapability
		format     string
		valid      bool
	}{
		{domain.CapabilityBody, "markdown", true},
		{domain.CapabilityBody, "plain_text", true},
		{domain.CapabilityTitle, "", true},
		{domain.CapabilityTitle, "plain_text", true},
		{domain.CapabilityTitle, "markdown", false},
		{domain.CapabilityBody, "html", false},
		{domain.CapabilitySummary, "application/custom", false},
	} {
		raw, _ := json.Marshal(TextValue{Text: "safe", Format: tc.format})
		err := ValidateCapabilityValue(tc.capability, raw)
		if (err == nil) != tc.valid {
			t.Fatalf("%s format %q validity = %v, want %v (err=%v)", tc.capability, tc.format, err == nil, tc.valid, err)
		}
	}
}

func TestOEmbedDropsHTMLAndPlaybackRequiresTrustedID(t *testing.T) {
	metadata, err := ParseOEmbedMetadata([]byte(`{"provider_name":"YouTube","title":"Safe title","thumbnail_url":"https://img.example/x.jpg","html":"<script>steal()</script><iframe src=evil></iframe>","unknown":{"token":"secret"}}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(metadata)
	if strings.Contains(strings.ToLower(string(raw)), "html") || strings.Contains(string(raw), "steal") || strings.Contains(string(raw), "secret") {
		t.Fatalf("raw oEmbed field crossed typed boundary: %s", raw)
	}
	if err := ValidatePlaybackSpec(PlaybackSpec{Kind: PlaybackProviderEmbed, Provider: "youtube", ProviderItemID: "3RmtNXqnreI"}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []PlaybackSpec{
		{Kind: PlaybackProviderEmbed, Provider: "evil", ProviderItemID: "3RmtNXqnreI"},
		{Kind: PlaybackProviderEmbed, Provider: "youtube", ProviderItemID: `<iframe src=x>`},
	} {
		if err := ValidatePlaybackSpec(spec); err == nil {
			t.Fatalf("unsafe playback accepted: %+v", spec)
		}
	}
}

func TestObservationDraftRejectsRawEmbedMarkup(t *testing.T) {
	now := time.Now().UTC()
	for _, raw := range []string{
		`{"text":"<iframe src='evil'></iframe>","format":"plain_text"}`,
		`{"text":"javascript:alert(1)","format":"plain_text"}`,
		`{"html":"<script>x</script>","text":"safe"}`,
	} {
		draft := ObservationDraft{Capability: domain.CapabilitySummary, Attribution: domain.AttributionProvider, ValueJSON: raw, ObservedAt: now}
		if err := draft.Validate(); err == nil {
			t.Fatalf("unsafe draft accepted: %s", raw)
		}
	}
}

func validDescriptor() Descriptor {
	return Descriptor{Adapter: "youtube-metadata", Version: "1.0.0", SupportedProviders: []string{"youtube"},
		SupportedSourceKinds: []string{"video"}, SupportedMediaKinds: []domain.MediaKind{domain.MediaVideo},
		Capabilities: []domain.EnrichmentCapability{domain.CapabilityTranscript},
		InputSchema:  SchemaRef{ID: "fe.provider.input", Version: "1"}, OutputSchema: SchemaRef{ID: "fe.provider.transcript", Version: "1"},
		Effects: []ExternalEffect{EffectNetworkRead}, NetworkClass: NetworkAuthenticated,
		CredentialReferences: []CredentialReference{{Name: "youtube-api", Purpose: "metadata-read"}}}
}
