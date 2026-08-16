package ingest

import (
	"context"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	directives "github.com/hollis-labs/go-directives"
)

// DirectiveEntityKind is the fragment_entities Kind used to tag fragments
// that contain inline ::command directives (loom-architecture.md §4).
const DirectiveEntityKind = "directive"

// directiveEntitySource identifies DirectiveStage as the writer of
// Kind:"directive" fragment_entities rows.
const directiveEntitySource = "go-directives"

// DirectiveStage detects inline ::command directives in a fragment's content
// using go-directives and tags each detected command as a presence-only
// fragment_entities row: Kind: DirectiveEntityKind, Value: the directive's
// canonical command name (e.g. "draft", "log-adr"), Source:
// directiveEntitySource, Confidence: 1.0.
//
// The tag is presence/command-name only: Prompt, Config, ContextRange, and
// Hash are intentionally not persisted. Loom re-parses the raw fragment
// content itself when it actually executes a directive downstream, so this
// stage's only job is detect + tag, not carry payload.
//
// DirectiveStage must run before RouteStage so directive tags exist in
// fragment_entities before routing decisions are made. It writes only its
// own Kind:"directive" rows via ReplaceFragmentEntitiesByKind, so it never
// disturbs entities other stages own (e.g. RecallStage's
// workspace/repo/model/tool tags, or manual intake's "tag" entities) even
// though those stages replace their own entities later in the same pipeline
// run.
//
// This stage is parsing/tagging only. Directive *execution* (draft, log-adr,
// reminder, extract, ...) happens downstream in Loom's generation flow.
type DirectiveStage struct {
	entities *repository.EntityRepository
}

func NewDirectiveStage(entities *repository.EntityRepository) *DirectiveStage {
	return &DirectiveStage{entities: entities}
}

func (s *DirectiveStage) Name() string {
	return "directive_tag"
}

func (s *DirectiveStage) Run(ctx context.Context, stageCtx *StageContext) error {
	if stageCtx.Outcome == repository.UpsertSkipped {
		return nil
	}
	if s.entities == nil {
		return nil
	}

	result := directives.Parse(stageCtx.Fragment.Content, directives.ParserConfig{
		Source: stageCtx.Fragment.ID,
	})

	tagged := make([]domain.FragmentEntity, 0, len(result.Directives))
	seen := make(map[string]struct{}, len(result.Directives))
	for _, d := range result.Directives {
		// Parse() already resolves Structure/Config commands internally
		// (context_start, zoom, config, ...) rather than emitting them into
		// result.Directives, so what reaches this loop is Action and Meta
		// only. Restrict to Action: those are the directives Loom will
		// actually execute (draft, log-adr, reminder, extract, ...); Meta
		// commands like "retry" steer the parser itself rather than naming
		// something for Loom to run, so they're not useful as
		// routing/presence tags here.
		if directives.ClassifyCommand(d.Command) != directives.CategoryAction {
			continue
		}
		if _, ok := seen[d.Command]; ok {
			continue
		}
		seen[d.Command] = struct{}{}
		tagged = append(tagged, domain.FragmentEntity{
			Kind:       DirectiveEntityKind,
			Value:      d.Command,
			Source:     directiveEntitySource,
			Confidence: 1.0,
		})
	}

	return s.entities.ReplaceFragmentEntitiesByKind(ctx, stageCtx.Fragment.ID, tagged, DirectiveEntityKind)
}
