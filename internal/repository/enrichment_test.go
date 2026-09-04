package repository

import (
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestObservationResolutionPrecedenceIsDeterministicAndAttributionAware(t *testing.T) {
	now := time.Now().UTC()
	confidence := 0.99
	items := []domain.EnrichmentObservation{
		{ID: "model", Attribution: domain.AttributionModel, AssertedAt: now.Add(time.Hour), Confidence: &confidence},
		{ID: "deterministic", Attribution: domain.AttributionDeterministic, AssertedAt: now.Add(2 * time.Hour)},
		{ID: "provider", Attribution: domain.AttributionProvider, AssertedAt: now.Add(3 * time.Hour)},
		{ID: "source", Attribution: domain.AttributionSourceMaterial, AssertedAt: now.Add(4 * time.Hour)},
		{ID: "user", Attribution: domain.AttributionUser, AssertedAt: now.Add(-time.Hour)},
	}
	sortObservations(items)
	want := []string{"user", "source", "provider", "deterministic", "model"}
	for index := range want {
		if items[index].ID != want[index] {
			t.Fatalf("precedence[%d] = %q, want %q", index, items[index].ID, want[index])
		}
	}

	items = []domain.EnrichmentObservation{
		{ID: "older-high", Attribution: domain.AttributionProvider, AssertedAt: now, Confidence: floatPointer(0.9)},
		{ID: "newer-low", Attribution: domain.AttributionProvider, AssertedAt: now.Add(time.Hour), Confidence: floatPointer(0.5)},
		{ID: "same-a", Attribution: domain.AttributionProvider, AssertedAt: now, Confidence: floatPointer(0.9)},
	}
	sortObservations(items)
	if items[0].ID != "older-high" || items[1].ID != "same-a" || items[2].ID != "newer-low" {
		t.Fatalf("confidence/time/id tie-break is not deterministic: %+v", items)
	}
}

func floatPointer(value float64) *float64 { return &value }
