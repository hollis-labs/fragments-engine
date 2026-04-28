package ingest

import (
	"context"

	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type AttachmentStage struct {
	repo *repository.AttachmentRepository
}

func NewAttachmentStage(repo *repository.AttachmentRepository) *AttachmentStage {
	return &AttachmentStage{repo: repo}
}

func (s *AttachmentStage) Name() string {
	return "attachments_store"
}

func (s *AttachmentStage) Run(ctx context.Context, stageCtx *StageContext) error {
	if stageCtx.Outcome == repository.UpsertSkipped {
		return nil
	}
	return s.repo.ReplaceFragmentAttachments(ctx, stageCtx.Fragment.ID, stageCtx.Candidate.Attachments, stageCtx.Now)
}
