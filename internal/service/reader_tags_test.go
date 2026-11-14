package service

import (
	"reflect"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

func captureTagFact(value, source, observationID string) repository.ReaderTagFact {
	return repository.ReaderTagFact{FragmentID: "fragment", Value: value, NormalizedValue: value,
		Source: domain.AttributionSource(source), ObservationID: observationID}
}

func enrichmentTagFact(source, observationID, valueJSON string) repository.ReaderTagFact {
	return repository.ReaderTagFact{FragmentID: "fragment", FragmentRevisionID: "revision",
		Source: domain.AttributionSource(source), ObservationID: observationID, ValueJSON: valueJSON}
}

func TestReaderTagsAttributedListsEachTagOncePerSource(t *testing.T) {
	// The shape of a capture sent once with tags link and test: the capture's
	// own tag rows, then one enrichment observation carrying both tags.
	facts := []repository.ReaderTagFact{
		captureTagFact("link", "user", "basalisk-1"),
		captureTagFact("test", "user", "basalisk-1"),
		enrichmentTagFact("user", "digest-of-tags", `{"values":["link","test"]}`),
	}
	combined, attributed := projectReaderTags("fragment", "revision", facts)
	if !reflect.DeepEqual(combined, []string{"link", "test"}) {
		t.Fatalf("combined = %v", combined)
	}
	want := []string{"link", "test"}
	if len(attributed) != 2 {
		t.Fatalf("attributed has %d entries, want 2: %+v", len(attributed), attributed)
	}
	for i, tag := range attributed {
		if tag.Value != want[i] || tag.Source != "user" || tag.ObservationID != "basalisk-1" ||
			!reflect.DeepEqual(tag.ObservationIDs, []string{"basalisk-1", "digest-of-tags"}) {
			t.Fatalf("entry %d = %+v", i, tag)
		}
	}
}

func TestReaderTagsAttributedPrefersTagObservationRowRegardlessOfOrder(t *testing.T) {
	facts := []repository.ReaderTagFact{
		enrichmentTagFact("user", "digest-of-tags", `{"values":["link"]}`),
		captureTagFact("link", "user", "basalisk-1"),
	}
	_, attributed := projectReaderTags("fragment", "revision", facts)
	if len(attributed) != 1 || attributed[0].ObservationID != "basalisk-1" ||
		!reflect.DeepEqual(attributed[0].ObservationIDs, []string{"digest-of-tags", "basalisk-1"}) {
		t.Fatalf("attributed = %+v", attributed)
	}
}

func TestReaderTagsAttributedKeepsDifferentSourcesAndSingleObservations(t *testing.T) {
	facts := []repository.ReaderTagFact{
		captureTagFact("link", "user", "basalisk-1"),
		enrichmentTagFact("model", "model-obs", `{"values":["Link"]}`),
		enrichmentTagFact("model", "model-obs", `{"values":["Link"]}`),
	}
	combined, attributed := projectReaderTags("fragment", "revision", facts)
	if !reflect.DeepEqual(combined, []string{"Link"}) {
		t.Fatalf("combined = %v", combined)
	}
	if len(attributed) != 2 {
		t.Fatalf("user and model entries must both remain: %+v", attributed)
	}
	for _, tag := range attributed {
		if tag.ObservationIDs != nil {
			t.Fatalf("a single observation must not list observation_ids: %+v", tag)
		}
	}
	if attributed[0].Source != "user" || attributed[0].ObservationID != "basalisk-1" ||
		attributed[1].Source != "model" || attributed[1].ObservationID != "model-obs" {
		t.Fatalf("attributed = %+v", attributed)
	}
}
