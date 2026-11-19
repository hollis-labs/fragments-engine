package ingest

import (
	"context"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// drainSource reports batch_continues for its first few passes, like a source that
// reads a large file one bounded slice at a time, and yields one segment per pass.
type drainSource struct {
	passes    int
	continue_ int
}

func (s *drainSource) Kind() string { return "claude_code" }
func (s *drainSource) Collect(ctx context.Context, _ config.IngestConfig) ([]domain.PipelineFragment, error) {
	s.passes++
	if s.passes <= s.continue_ {
		domain.NoteSkip(ctx, "batch_continues", "big.jsonl")
	}
	return []domain.PipelineFragment{{Source: "claude", SourceID: "s", SourceType: "chat",
		Detail: &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "s"}}}}, nil
}

type countingWriter struct{ accepted int }

func (w *countingWriter) Accept(context.Context, string, domain.PipelineFragment) (transcript.Outcome, error) {
	w.accepted++
	return transcript.Inserted, nil
}
func (*countingWriter) Cursor(context.Context, string, string) (domain.TranscriptCursor, bool) {
	return domain.TranscriptCursor{}, false
}

func TestTranscriptRunDrainsBoundedPasses(t *testing.T) {
	src := &drainSource{continue_: 3}
	w := &countingWriter{}
	p := NewPipeline(nil, nil, nil, src)
	p.SetTranscriptStore(w)
	run, err := p.RunOnce(context.Background(), config.IngestConfig{Name: "n", Kind: "claude_code"})
	if err != nil {
		t.Fatal(err)
	}
	// three passes asked to continue, the fourth finished: all four were applied
	if src.passes != 4 || w.accepted != 4 || run.Inserted != 4 {
		t.Fatalf("passes=%d accepted=%d inserted=%d", src.passes, w.accepted, run.Inserted)
	}
	if run.SkipReasons["batch_continues"] != 0 {
		t.Fatalf("run still reports work remaining: %v", run.SkipReasons)
	}
}

func TestTranscriptRunWithoutContinuationRunsOnce(t *testing.T) {
	src := &drainSource{continue_: 0}
	w := &countingWriter{}
	p := NewPipeline(nil, nil, nil, src)
	p.SetTranscriptStore(w)
	if _, err := p.RunOnce(context.Background(), config.IngestConfig{Name: "n", Kind: "claude_code"}); err != nil {
		t.Fatal(err)
	}
	if src.passes != 1 {
		t.Fatalf("passes = %d", src.passes)
	}
}
