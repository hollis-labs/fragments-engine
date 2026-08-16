package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/recall"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// TestDirectiveStage_TagsActionDirectivesOnly covers CW-20260816-0012: a
// fragment containing inline ::command directives gets each Action-category
// command tagged as a presence-only Kind:"directive" fragment_entities row
// (Value = canonical command name), while Structure/Config/Meta commands are
// not tagged.
func TestDirectiveStage_TagsActionDirectivesOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	ctx := context.Background()

	content := `::ctx Sprint planning
::draft Write a launch post about the Loom pilot
::retry
::log-adr Record the directive-tagging decision
::ctx_end`

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:     "manual",
		SourceType: "text",
		SourceID:   "directive-fragment",
		Title:      "Directive fragment",
		Content:    content,
		CreatedAt:  time.Now().UTC(),
	}, "test", time.Now().UTC())
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	outcome, err := fragmentRepo.Upsert(ctx, fragment)
	if err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	stageCtx := &StageContext{
		Fragment: fragment,
		Outcome:  outcome,
		Now:      time.Now().UTC(),
	}

	stage := NewDirectiveStage(entityRepo)
	if got := stage.Name(); got != "directive_tag" {
		t.Fatalf("unexpected stage name: %s", got)
	}
	if err := stage.Run(ctx, stageCtx); err != nil {
		t.Fatalf("run directive stage: %v", err)
	}

	entities, err := entityRepo.ListByFragment(ctx, fragment.ID)
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}

	assertHasEntity(t, entities, "directive", "draft")
	assertHasEntity(t, entities, "directive", "log-adr")

	for _, e := range entities {
		if e.Kind != "directive" {
			t.Fatalf("unexpected non-directive entity written by DirectiveStage: %+v", e)
		}
		if e.Value == "retry" || e.Value == "context_start" || e.Value == "context_end" {
			t.Fatalf("expected Meta/Structure command %q not to be tagged, entities: %+v", e.Value, entities)
		}
		if e.Source != "go-directives" {
			t.Fatalf("unexpected entity source: %+v", e)
		}
		if e.Confidence != 1.0 {
			t.Fatalf("expected presence-only confidence 1.0, got %+v", e)
		}
	}
	if len(entities) != 2 {
		t.Fatalf("expected exactly 2 directive entities (draft, log-adr), got %+v", entities)
	}
}

// TestDirectiveStage_SkippedOutcomeNoOp covers the same UpsertSkipped guard
// every other stage uses: a fragment that was skipped during upsert (e.g.
// unchanged content on re-ingest) should not have its entities touched.
func TestDirectiveStage_SkippedOutcomeNoOp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	ctx := context.Background()

	fragment, err := repository.BuildFragment(domain.PipelineFragment{
		Source:     "manual",
		SourceType: "text",
		SourceID:   "skipped-fragment",
		Title:      "Skipped fragment",
		Content:    "::draft Should not be tagged because outcome is skipped",
		CreatedAt:  time.Now().UTC(),
	}, "test", time.Now().UTC())
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	if _, err := fragmentRepo.Upsert(ctx, fragment); err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	stageCtx := &StageContext{
		Fragment: fragment,
		Outcome:  repository.UpsertSkipped,
		Now:      time.Now().UTC(),
	}
	stage := NewDirectiveStage(entityRepo)
	if err := stage.Run(ctx, stageCtx); err != nil {
		t.Fatalf("run directive stage: %v", err)
	}

	entities, err := entityRepo.ListByFragment(ctx, fragment.ID)
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	if len(entities) != 0 {
		t.Fatalf("expected no entities written for skipped outcome, got %+v", entities)
	}
}

// TestPipeline_DirectiveEntitiesSurviveRecallStage is the regression test
// for the entity-clobber bug this task fixes: DirectiveStage runs before
// RouteStage/RecallStage in the real app.go stage order, writing
// Kind:"directive" rows. RecallStage runs last and used to call a blanket
// ReplaceFragmentEntities that deleted *every* entity row for the fragment
// before reinserting only what extract.FromFragment produces -- silently
// wiping out DirectiveStage's earlier write. After the fix (RecallStage's
// backing SQLiteIndexer scopes its replace to extract.Kinds() via
// ReplaceFragmentEntitiesByKind), both the directive-kind entity and
// whatever extract.FromFragment normally produces must be present once the
// full stage list has run.
func TestPipeline_DirectiveEntitiesSurviveRecallStage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	attachmentRepo := repository.NewAttachmentRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	recallIndex := recall.NewSQLiteIndexer(fragmentRepo, entityRepo)
	ctx := context.Background()

	// "sqlite" is one of extract's knownTools keywords, so
	// extract.FromFragment will tag this fragment with a Kind:"tool" entity
	// -- giving us a second, independently-owned entity kind to assert
	// alongside the directive tag.
	content := "::draft Write about migrating the recall index to sqlite\n\nWe use sqlite for local recall storage."

	candidate := domain.PipelineFragment{
		Source:     "manual",
		SourceType: "text",
		SourceID:   "pipeline-regression",
		Title:      "Pipeline regression fragment",
		Content:    content,
		CreatedAt:  time.Now().UTC(),
	}
	fragment, err := repository.BuildFragment(candidate, "test", time.Now().UTC())
	if err != nil {
		t.Fatalf("build fragment: %v", err)
	}
	outcome, err := fragmentRepo.Upsert(ctx, fragment)
	if err != nil {
		t.Fatalf("upsert fragment: %v", err)
	}

	stageCtx := &StageContext{
		Candidate: candidate,
		Fragment:  fragment,
		Outcome:   outcome,
		Now:       time.Now().UTC(),
	}

	// Mirrors internal/app/app.go's stage order: DirectiveStage before
	// RouteStage, RecallStage last.
	stages := []Stage{
		NewAttachmentStage(attachmentRepo),
		NewDirectiveStage(entityRepo),
		NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, nil),
		NewInboxStage(inboxRepo),
		NewRecallStage(recallIndex),
	}

	for _, stage := range stages {
		if err := stage.Run(ctx, stageCtx); err != nil {
			t.Fatalf("stage %s: %v", stage.Name(), err)
		}
		if stage.Name() == "directive_tag" {
			mid, err := entityRepo.ListByFragment(ctx, fragment.ID)
			if err != nil {
				t.Fatalf("list entities after directive stage: %v", err)
			}
			assertHasEntity(t, mid, "directive", "draft")
		}
	}

	final, err := entityRepo.ListByFragment(ctx, fragment.ID)
	if err != nil {
		t.Fatalf("list entities after full pipeline: %v", err)
	}

	assertHasEntity(t, final, "directive", "draft")
	assertHasEntity(t, final, "tool", "sqlite")
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
