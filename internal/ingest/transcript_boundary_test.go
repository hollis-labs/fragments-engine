package ingest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/legacycapture"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

type boundarySource struct {
	collected int
	candidate domain.PipelineFragment
	kind      string
}

func (s *boundarySource) Kind() string { return s.kind }
func (s *boundarySource) Collect(context.Context, config.IngestConfig) ([]domain.PipelineFragment, error) {
	s.collected++
	return []domain.PipelineFragment{s.candidate}, nil
}

type boundaryStage struct{ called int }

func (*boundaryStage) Name() string { return "must-not-route" }
func (s *boundaryStage) Run(context.Context, *StageContext) error {
	s.called++
	return errors.New("raw routing reached")
}

func TestTranscriptPrivateBoundaryBeforeCollectionAndSharedEffects(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	source := &boundarySource{kind: "claude_code", candidate: domain.PipelineFragment{Source: "claude", SourceType: "chat", SourceID: "session", Content: "safe roadmap password=synthetic-secret"}}
	stage := &boundaryStage{}
	p := NewPipeline(repository.NewFragmentRepository(st.DB), nil, []Stage{stage}, source)
	p.SetLegacyCaptureService(legacycapture.NewService(repository.NewCaptureRepository(st.DB)))
	cfg := config.IngestConfig{Name: "source", Kind: "claude_code"}
	if _, err := p.RunOnce(context.Background(), cfg); !errors.Is(err, transcript.ErrPrivateStoreRequired) || source.collected != 0 {
		t.Fatalf("unowned collection reached %d %v", source.collected, err)
	}
	private, err := transcript.Open(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	p.SetTranscriptStore(private)
	for _, want := range []int{1, 0} {
		run, err := p.RunOnce(context.Background(), cfg)
		if err != nil || run.Inserted != want {
			t.Fatalf("private run %+v %v", run, err)
		}
	}
	if stage.called != 0 {
		t.Fatal("private transcript reached shared routing/index")
	}
	for _, table := range []string{"fragments", "fragment_revisions", "capture_attempts", "fragment_attachments", "fragment_entities", "inbox"} {
		var count int
		if err := st.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("transcript leaked into %s", table)
		}
	}
	// Bypass attempts must be refused at both canonical transaction boundaries.
	built, err := repository.BuildFragment(source.candidate, "source", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NewFragmentRepository(st.DB).Upsert(context.Background(), built); !errors.Is(err, transcript.ErrPrivateStoreRequired) {
		t.Fatalf("direct fragment bypass: %v", err)
	}
	if _, err := repository.NewCaptureRepository(st.DB).Accept(context.Background(), repository.CaptureWrite{Fragment: built}); !errors.Is(err, transcript.ErrPrivateStoreRequired) {
		t.Fatalf("direct capture bypass: %v", err)
	}
}

func TestTranscriptCopyOptionsRefusedBeforeCollection(t *testing.T) {
	private, err := transcript.Open(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	source := &boundarySource{kind: "chatgpt_export"}
	p := NewPipeline(nil, nil, nil, source)
	p.SetTranscriptStore(private)
	for _, key := range []string{"copy_text_exports", "delete_copied_source"} {
		_, err := p.RunOnce(context.Background(), config.IngestConfig{Name: "source", Kind: source.kind, Rules: map[string]any{key: true}})
		if err == nil || source.collected != 0 {
			t.Fatal("raw archive mutation reached collection")
		}
	}
}

func TestTranscriptCandidateCannotUseAnUnclassifiedSourceToBypass(t *testing.T) {
	source := &boundarySource{kind: "filesystem_docs", candidate: domain.PipelineFragment{Source: "file:///synthetic", SourceType: "transcript", SourceID: "session", Content: "safe"}}
	p := NewPipeline(nil, nil, nil, source)
	if _, err := p.RunOnce(context.Background(), config.IngestConfig{Name: "source", Kind: source.kind}); !errors.Is(err, transcript.ErrPrivateStoreRequired) {
		t.Fatalf("candidate escaped boundary: %v", err)
	}
}
