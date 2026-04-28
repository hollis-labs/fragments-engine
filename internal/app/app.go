package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/ingest/chatgpt"
	"github.com/hollis-labs/fragments-engine/internal/ingest/claude"
	"github.com/hollis-labs/fragments-engine/internal/ingest/urlsource"
	"github.com/hollis-labs/fragments-engine/internal/recall"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

type App struct {
	store     *store.Store
	recall    recall.Indexer
	Fragments *service.FragmentService
	Inbox     *service.InboxService
	Routing   *service.RoutingService
	Queue     *service.DeliveryQueueService
}

func Open(ctx context.Context, cfg config.Config) (*App, error) {
	dbPath := config.ExpandHome(cfg.Database.Path)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	attachmentRepo := repository.NewAttachmentRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	deliveryQueue, err := service.NewDeliveryQueueService(st.DB, routingRepo, fragmentRepo, entityRepo, inboxRepo, cfg.Delivery, cfg.Queue)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	deliveryQueue.SetAttachmentRepository(attachmentRepo)
	recallIndex, err := openRecallIndexer(ctx, fragmentRepo, entityRepo, cfg)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	routingSvc := service.NewRoutingService(routingRepo, fragmentRepo, entityRepo, inboxRepo, cfg.Delivery, cfg.Queue)
	routingSvc.SetDeliveryQueue(deliveryQueue)
	routingSvc.SetAttachmentRepository(attachmentRepo)
	visionAnalyzer := analyze.NewVisionAnalyzer(cfg.Analysis.Attachments)
	pipeline := ingest.NewPipeline(fragmentRepo, visionAnalyzer, []ingest.Stage{
		ingest.NewAttachmentStage(attachmentRepo),
		ingest.NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, deliveryQueue),
		ingest.NewInboxStage(inboxRepo),
		ingest.NewRecallStage(recallIndex),
	}, claude.Source{}, chatgpt.Source{}, urlsource.Source{})
	return &App{
		store:     st,
		recall:    recallIndex,
		Fragments: service.NewFragmentService(fragmentRepo, entityRepo, attachmentRepo, routingRepo, recallIndex, pipeline, visionAnalyzer),
		Inbox:     service.NewInboxService(inboxRepo),
		Routing:   routingSvc,
		Queue:     deliveryQueue,
	}, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	if a.recall != nil {
		_ = a.recall.Close()
	}
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}

func (a *App) RecallStatus() recall.Status {
	if a == nil || a.recall == nil {
		return recall.Status{}
	}
	return a.recall.Status()
}

func InitDB(cfg config.Config) error {
	dbPath := config.ExpandHome(cfg.Database.Path)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return fmt.Errorf("mkdir db dir: %w", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	return st.Close()
}

func openRecallIndexer(ctx context.Context, fragmentRepo *repository.FragmentRepository, entityRepo *repository.EntityRepository, cfg config.Config) (recall.Indexer, error) {
	switch cfg.RecallBackend() {
	case "sqlite":
		return recall.NewSQLiteIndexer(fragmentRepo, entityRepo), nil
	case "vanta":
		return recall.NewVantaIndexer(ctx, fragmentRepo, entityRepo, cfg)
	default:
		return nil, fmt.Errorf("unsupported recall backend %q", cfg.RecallBackend())
	}
}
