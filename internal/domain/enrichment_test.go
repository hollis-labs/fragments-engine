package domain

import "testing"

func TestEnrichmentCapabilityAndCoverageStateClosedSets(t *testing.T) {
	wantCapabilities := []EnrichmentCapability{
		CapabilityTitle, CapabilityDescription, CapabilityBody,
		CapabilityGalleryManifest, CapabilityOriginalMedia, CapabilityThumbnailOrPoster,
		CapabilityTranscript, CapabilityOCR, CapabilityVision, CapabilitySummary,
		CapabilityTags, CapabilityEntities,
	}
	got := AllEnrichmentCapabilities()
	if len(got) != len(wantCapabilities) {
		t.Fatalf("capability count = %d, want %d", len(got), len(wantCapabilities))
	}
	for index, capability := range wantCapabilities {
		if got[index] != capability || !capability.Valid() {
			t.Fatalf("capability[%d] = %q, want valid %q", index, got[index], capability)
		}
	}
	if EnrichmentCapability("poster").Valid() {
		t.Fatal("unknown capability accepted")
	}
	for _, state := range []CapabilityState{CoverageProvided, CoverageMissing, CoveragePending, CoverageFailed, CoverageStale, CoverageNotApplicable} {
		if !state.Valid() {
			t.Fatalf("canonical coverage state %q rejected", state)
		}
	}
	if CapabilityState("complete").Valid() {
		t.Fatal("blanket complete state accepted")
	}
}
