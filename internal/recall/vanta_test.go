package recall

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
	vmemory "github.com/hollis-labs/tesseract/memory"
)

func TestPublishedTesseractRoundTripWithoutEmbedder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := repository.NewFragmentRepository(st.DB)
	fragment, err := repository.BuildFragment(domain.PipelineFragment{Source: "fixture", SourceType: "document", SourceID: "synthetic-1", Title: "Migration fixture", Content: "Synthetic quartz migration example.", CreatedAt: time.Now().UTC()}, "fixture", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Upsert(ctx, fragment); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Database.Path = filepath.Join(dir, "fixture.db")
	cfg.Recall.Vanta.Root = filepath.Join(dir, "recall")
	index, err := NewVantaIndexer(ctx, repo, repository.NewEntityRepository(st.DB), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.IndexFragment(ctx, fragment); err != nil {
		t.Fatal(err)
	}
	rev, err := index.conduit.GetCurrentRevision(ctx, vantaNamespace, fragment.ID)
	if err != nil || rev.Payload.Body != fragment.Content || rev.MemoryKey != fragment.ID || rev.Namespace != vantaNamespace || rev.Facets.Source != fragment.Source || rev.Facets.Kind != "note" || !slices.Contains(rev.Tags, "fragment") || rev.Facets.Pointer == nil || rev.Facets.Pointer.Scheme != "fe" || rev.Facets.Pointer.Locator != "fragment/"+fragment.ID {
		t.Fatalf("roundtrip=%+v %v", rev, err)
	}
	// Model a pre-adoption projection in this temporary database only. The
	// fixture relocation seeds historical state without asserting a user actor
	// or changing the production write/authorization path.
	projectionDB := index.conduit.MemoryStore().DB()
	for _, table := range []string{"memory_revisions", "memory_state"} {
		if _, err := projectionDB.ExecContext(ctx, "UPDATE "+table+" SET namespace=? WHERE namespace=?", legacyVantaNamespace, vantaNamespace); err != nil {
			t.Fatal(err)
		}
	}
	if err := index.IndexFragment(ctx, fragment); err != nil {
		t.Fatal(err)
	}
	recalls, err := index.conduit.RecallMemory(ctx, vmemory.RecallInput{Namespaces: []string{vantaNamespace, legacyVantaNamespace}, Query: "quartz", Limit: 5})
	if err != nil || len(recalls) != 2 {
		t.Fatalf("old+new recall=%+v %v", recalls, err)
	}
	fragment, err = repository.BuildFragment(domain.PipelineFragment{Source: "fixture", SourceType: "document", SourceID: "synthetic-1", Title: "Migration fixture", Content: "Current quartz authoritative fixture body.", CreatedAt: time.Now().UTC()}, "fixture", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Upsert(ctx, fragment); err != nil {
		t.Fatal(err)
	}

	got, err := index.Search(ctx, "quartz", 5)
	if err != nil || len(got) != 1 || got[0].Fragment.ID != fragment.ID || got[0].Fragment.Content != fragment.Content {
		t.Fatalf("search=%+v %v", got, err)
	}
	second, err := repository.BuildFragment(domain.PipelineFragment{Source: "fixture", SourceType: "document", SourceID: "synthetic-2", Title: "Second quartz fixture", Content: "Another quartz example.", CreatedAt: time.Now().UTC()}, "fixture", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := index.IndexFragment(ctx, second); err != nil {
		t.Fatal(err)
	}
	limited, err := index.Search(ctx, "quartz", 2)
	if err != nil || len(limited) != 2 || limited[0].Fragment.ID == limited[1].Fragment.ID {
		t.Fatalf("deduplicated limit=%+v %v", limited, err)
	}

	if index.Status().EmbeddingsEnabled {
		t.Fatal("unexpected ambient embedder")
	}
}
