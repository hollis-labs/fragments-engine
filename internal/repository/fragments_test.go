package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestFragmentSearch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	repo := NewFragmentRepository(st.DB)
	fragment, err := BuildFragment(domain.PipelineFragment{
		Source:     "claude",
		SourceType: "chat",
		SourceID:   "session-1",
		Title:      "Claude session: roadmap",
		Content:    "Discuss the roadmap and ingestion pipeline design.",
		CreatedAt:  time.Now().UTC(),
	}, "claude-test", time.Now().UTC())
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	outcome, err := repo.Upsert(context.Background(), fragment)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if outcome != UpsertInserted {
		t.Fatalf("unexpected outcome: %s", outcome)
	}

	results, err := repo.Search(context.Background(), "roadmap", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Fragment.SourceID != "session-1" {
		t.Fatalf("unexpected source id: %s", results[0].Fragment.SourceID)
	}
}

func TestBuildFragmentRejectsEmptyMaterialWithOpaqueSourceID(t *testing.T) {
	_, err := BuildFragment(domain.PipelineFragment{
		Source: "legacy", SourceType: "unknown", SourceID: "opaque-item-id",
	}, "legacy-test", time.Now().UTC())
	if err == nil || err.Error() != "build fragment: source material is required" {
		t.Fatalf("expected empty material rejection, got %v", err)
	}
}

func TestFragmentList(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	repo := NewFragmentRepository(st.DB)
	ctx := context.Background()
	base := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// Insert three fragments with ascending created_at; the middle one is routed.
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		f, err := BuildFragment(domain.PipelineFragment{
			Source:     "claude",
			SourceType: "chat",
			SourceID:   "session-" + string(rune('a'+i)),
			Title:      "Fragment",
			Content:    "List ordering body number " + string(rune('a'+i)),
			CreatedAt:  base.Add(time.Duration(i) * time.Hour),
		}, "claude-test", time.Now().UTC())
		if err != nil {
			t.Fatalf("build fragment %d: %v", i, err)
		}
		if _, err := repo.Upsert(ctx, f); err != nil {
			t.Fatalf("upsert fragment %d: %v", i, err)
		}
		ids[i] = f.ID
	}
	if err := repo.UpdateStatus(ctx, ids[1], domain.FragmentStatusRouted); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// All statuses, newest-first.
	items, total, err := repo.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected 3 items/total, got %d items / total %d", len(items), total)
	}
	if items[0].ID != ids[2] || items[2].ID != ids[0] {
		t.Fatalf("expected newest-first ordering, got %v", []string{items[0].ID, items[1].ID, items[2].ID})
	}

	// Status filter.
	inbox, inboxTotal, err := repo.List(ctx, ListOptions{Status: domain.FragmentStatusInbox})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if inboxTotal != 2 || len(inbox) != 2 {
		t.Fatalf("expected 2 inbox items/total, got %d / %d", len(inbox), inboxTotal)
	}
	for _, f := range inbox {
		if f.Status != domain.FragmentStatusInbox {
			t.Fatalf("status filter leaked %s", f.Status)
		}
	}

	// Limit + offset paginate while total reflects the full set.
	page, pageTotal, err := repo.List(ctx, ListOptions{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("list paginated: %v", err)
	}
	if pageTotal != 3 || len(page) != 1 {
		t.Fatalf("expected 1 item / total 3, got %d / %d", len(page), pageTotal)
	}
	if page[0].ID != ids[1] {
		t.Fatalf("expected offset to land on middle fragment, got %s", page[0].ID)
	}
}
