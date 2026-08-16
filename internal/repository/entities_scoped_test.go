package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// TestReplaceFragmentEntitiesByKind_ScopedReplace covers the entity-clobber
// fix (CW-20260816-0012): a caller that only owns one entity kind (e.g. a
// directive-tagging stage owning Kind:"directive") must be able to replace
// just its own rows without disturbing entity kinds owned by other callers
// (e.g. Kind:"tag" written by manual intake) for the same fragment.
func TestReplaceFragmentEntitiesByKind_ScopedReplace(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := NewFragmentRepository(st.DB)
	entityRepo := NewEntityRepository(st.DB)
	ctx := context.Background()

	fragment, err := BuildFragment(domain.PipelineFragment{
		Source:     "manual",
		SourceType: "text",
		SourceID:   "scoped-replace",
		Title:      "Scoped replace test",
		Content:    "content for scoped replace test",
		CreatedAt:  time.Now().UTC(),
	}, "test", time.Now().UTC())
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(ctx, fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	// Another owner (e.g. manual intake) writes Kind:"tag" entities via the
	// existing blanket ReplaceFragmentEntities.
	if err := entityRepo.ReplaceFragmentEntities(ctx, fragment.ID, []domain.FragmentEntity{
		{Kind: "tag", Value: "link", Source: "manual-intake", Confidence: 1.0},
	}); err != nil {
		t.Fatalf("write tag entity: %v", err)
	}

	// A directive stage writes its own kind scoped to "directive" only.
	if err := entityRepo.ReplaceFragmentEntitiesByKind(ctx, fragment.ID, []domain.FragmentEntity{
		{Kind: "directive", Value: "draft", Source: "go-directives", Confidence: 1.0},
	}, "directive"); err != nil {
		t.Fatalf("write directive entity: %v", err)
	}

	entities, err := entityRepo.ListByFragment(ctx, fragment.ID)
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	assertHasEntity(t, entities, "tag", "link")
	assertHasEntity(t, entities, "directive", "draft")

	// Re-running the scoped replace for "directive" with a different set
	// must only affect directive-kind rows: the old "draft" tag is gone,
	// the new "log-adr" tag is present, and the unrelated "tag" entity
	// written by the other owner is untouched.
	if err := entityRepo.ReplaceFragmentEntitiesByKind(ctx, fragment.ID, []domain.FragmentEntity{
		{Kind: "directive", Value: "log-adr", Source: "go-directives", Confidence: 1.0},
	}, "directive"); err != nil {
		t.Fatalf("re-write directive entity: %v", err)
	}

	entities, err = entityRepo.ListByFragment(ctx, fragment.ID)
	if err != nil {
		t.Fatalf("list entities after re-write: %v", err)
	}
	assertHasEntity(t, entities, "tag", "link")
	assertHasEntity(t, entities, "directive", "log-adr")
	for _, e := range entities {
		if e.Kind == "directive" && e.Value == "draft" {
			t.Fatalf("expected stale directive entity to be replaced, still found: %+v", e)
		}
	}
}

// TestReplaceFragmentEntitiesByKind_RejectsOutOfScopeEntity guards against a
// caller accidentally passing an entity whose Kind isn't in the declared
// scope -- that would silently write a row the delete step wouldn't know to
// clear on a future call, reintroducing the same clobber class of bug this
// method exists to prevent.
func TestReplaceFragmentEntitiesByKind_RejectsOutOfScopeEntity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	entityRepo := NewEntityRepository(st.DB)
	err = entityRepo.ReplaceFragmentEntitiesByKind(context.Background(), "some-fragment", []domain.FragmentEntity{
		{Kind: "tag", Value: "oops", Source: "manual-intake", Confidence: 1.0},
	}, "directive")
	if err == nil {
		t.Fatalf("expected error for out-of-scope entity kind, got nil")
	}
}

func assertHasEntity(t *testing.T, entities []domain.FragmentEntity, kind, value string) {
	t.Helper()
	for _, e := range entities {
		if e.Kind == kind && e.Value == value {
			return
		}
	}
	t.Fatalf("expected entity kind=%s value=%s in %+v", kind, value, entities)
}
