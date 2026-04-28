package ingest

import (
	"context"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type RecallIndexer interface {
	IndexFragment(context.Context, domain.Fragment) error
}

type RecallStage struct {
	indexer RecallIndexer
}

func NewRecallStage(indexer RecallIndexer) *RecallStage {
	return &RecallStage{indexer: indexer}
}

func (s *RecallStage) Name() string {
	return "recall_index"
}

func (s *RecallStage) Run(ctx context.Context, stageCtx *StageContext) error {
	if stageCtx.Outcome == repository.UpsertSkipped {
		return nil
	}
	return s.indexer.IndexFragment(ctx, stageCtx.Fragment)
}
