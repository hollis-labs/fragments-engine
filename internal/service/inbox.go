package service

import (
	"context"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type InboxService struct {
	repo *repository.InboxRepository
}

func NewInboxService(repo *repository.InboxRepository) *InboxService {
	return &InboxService{repo: repo}
}

func (s *InboxService) List(ctx context.Context, limit int) ([]domain.InboxItem, error) {
	return s.repo.List(ctx, limit)
}

func (s *InboxService) ListDetailed(ctx context.Context, limit int) ([]domain.InboxItemDetail, error) {
	return s.repo.ListDetailed(ctx, limit)
}

func (s *InboxService) ListByEntityDetailed(ctx context.Context, kind, value string, limit int) ([]domain.InboxItemDetail, error) {
	return s.repo.ListByEntityDetailed(ctx, kind, value, limit)
}

func (s *InboxService) ListEntityGroups(ctx context.Context, kind string, limit int) ([]domain.InboxEntityGroup, error) {
	return s.repo.ListEntityGroups(ctx, kind, limit)
}

func (s *InboxService) ListByEntity(ctx context.Context, kind, value string, limit int) ([]domain.InboxItem, error) {
	return s.repo.ListByEntity(ctx, kind, value, limit)
}
