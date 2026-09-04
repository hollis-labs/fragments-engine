package ingest

import (
	"context"
	"strings"

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
	if stageCtx.LegacyCaptureApplied {
		return nil
	}
	if stageCtx.Outcome == repository.UpsertSkipped {
		return nil
	}
	// A few direct stage tests and compatibility callers retain the candidate
	// returned by BuildFragment rather than UpsertResolved. Resolve its persisted
	// current revision through the compatibility wrapper in that case.
	if strings.TrimSpace(stageCtx.Fragment.Revision.ID) == "" {
		return s.repo.ReplaceFragmentAttachments(ctx, stageCtx.Fragment.ID, stageCtx.Candidate.Attachments, stageCtx.Now)
	}
	return s.repo.ReplaceFragmentRevisionAttachments(ctx, stageCtx.Fragment.ID, stageCtx.Fragment.Revision.ID, stageCtx.Candidate.Attachments, stageCtx.Now)
}
