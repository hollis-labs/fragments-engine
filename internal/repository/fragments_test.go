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
