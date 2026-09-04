package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/legacycapture"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestPipelineAllLegacyIngestKindsUseCaptureAdapter(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	kinds := []string{"claude_code", "chatgpt_export", "url_source", "filesystem_docs", "git_changes", "nil_vault"}
	sources := make([]Source, 0, len(kinds))
	for _, kind := range kinds {
		sources = append(sources, staticLegacySource{kind: kind, candidate: domain.PipelineFragment{
			Source: kind, SourceType: "document", SourceID: "item-1",
			Title: "source title", Content: "source content for " + kind,
			CreatedAt: now, Metadata: map[string]any{"labels": []string{"source-label"}},
		}})
	}
	pipeline := NewPipeline(repository.NewFragmentRepository(st.DB), nil, nil, sources...)
	pipeline.now = func() time.Time { return now }
	pipeline.SetLegacyCaptureService(legacycapture.NewService(repository.NewCaptureRepository(st.DB)))

	for _, kind := range kinds {
		cfg := config.IngestConfig{Name: "registration-" + kind, Kind: kind, Enabled: true}
		first, err := pipeline.RunOnce(context.Background(), cfg)
		if err != nil {
			t.Fatalf("%s first run: %v", kind, err)
		}
		if first.Inserted != 1 || first.Updated != 0 || first.Skipped != 0 {
			t.Fatalf("%s first run=%+v", kind, first)
		}
		replay, err := pipeline.RunOnce(context.Background(), cfg)
		if err != nil {
			t.Fatalf("%s replay: %v", kind, err)
		}
		if replay.Inserted != 0 || replay.Updated != 0 || replay.Skipped != 1 {
			t.Fatalf("%s replay=%+v", kind, replay)
		}
	}
	assertPipelineCount(t, st, `SELECT COUNT(*) FROM fragments`, len(kinds))
	assertPipelineCount(t, st, `SELECT COUNT(*) FROM fragment_revisions`, len(kinds))
	assertPipelineCount(t, st, `SELECT COUNT(*) FROM capture_attempts`, len(kinds))
	assertPipelineCount(t, st, `SELECT COUNT(*) FROM fragment_capability_coverage`, len(kinds)*len(domain.AllEnrichmentCapabilities()))
	assertPipelineCount(t, st, `SELECT COUNT(*) FROM enrichment_observations WHERE capability = 'tags' AND attribution_source = 'source'`, len(kinds))
}

type staticLegacySource struct {
	kind      string
	candidate domain.PipelineFragment
}

func (s staticLegacySource) Kind() string { return s.kind }

func (s staticLegacySource) Collect(context.Context, config.IngestConfig) ([]domain.PipelineFragment, error) {
	return []domain.PipelineFragment{s.candidate}, nil
}

func assertPipelineCount(t *testing.T, st *store.Store, query string, want int) {
	t.Helper()
	var got int
	if err := st.DB.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d want %d", query, got, want)
	}
}
