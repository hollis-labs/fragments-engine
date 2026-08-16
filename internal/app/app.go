package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	queue "github.com/hollis-labs/go-queue"
	queuesqlite "github.com/hollis-labs/go-queue/driver/sqlite"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/ingest/chatgpt"
	"github.com/hollis-labs/fragments-engine/internal/ingest/claude"
	"github.com/hollis-labs/fragments-engine/internal/ingest/filesystemdocs"
	"github.com/hollis-labs/fragments-engine/internal/ingest/gitchanges"
	"github.com/hollis-labs/fragments-engine/internal/ingest/urlsource"
	"github.com/hollis-labs/fragments-engine/internal/linkcontent"
	"github.com/hollis-labs/fragments-engine/internal/recall"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// go-queue tables for async ingest jobs. Created on demand by the sqlite
// driver; distinct from the hand-rolled delivery queue tables.
const (
	ingestJobsTable       = "ingest_jobs"
	ingestFailedJobsTable = "ingest_failed_jobs"
)

type App struct {
	store           *store.Store
	recall          recall.Indexer
	Fragments       *service.FragmentService
	Inbox           *service.InboxService
	Routing         *service.RoutingService
	Queue           *service.DeliveryQueueService
	IngestQueue     queue.Queue
	IngestSchedules *service.IngestScheduleService
	InboxReviewer   *service.InboxReviewerService
	Jobs            *service.JobsService
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
	ingestQueue, err := queuesqlite.New(st.DB, queuesqlite.Opts{
		Table:       ingestJobsTable,
		FailedTable: ingestFailedJobsTable,
	})
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	routingSvc := service.NewRoutingService(routingRepo, fragmentRepo, entityRepo, inboxRepo, cfg.Delivery, cfg.Queue)
	routingSvc.SetDeliveryQueue(deliveryQueue)
	routingSvc.SetAttachmentRepository(attachmentRepo)
	visionAnalyzer := analyze.NewVisionAnalyzer(cfg.Analysis.Attachments)
	manualEnricher := service.NewManualIntakeEnricher(visionAnalyzer, cfg.Reviewer.DownloadRoot)
	corpusWriter := service.NewPinterestCorpusWriter(cfg.Reviewer.CorpusRoot)
	manualEnricher.SetGitHubToken(os.Getenv(strings.TrimSpace(cfg.Reviewer.GitHubTokenEnv)))
	// Local-only link-content provider: per product decision, Firecrawl is
	// never invoked synchronously during intake, so this deliberately uses
	// NewLocalProvider directly rather than the fallback-wrapped
	// linkcontent.NewProvider(cfg.LinkContent), which retries a Firecrawl
	// backend and is reserved for callers that can tolerate that latency.
	manualEnricher.SetLinkProvider(linkcontent.NewLocalProvider(cfg.LinkContent.Local))
	stackExplorerClient := service.NewStackExplorerClient(cfg.Reviewer.StackExplorerAPIBase)
	pipeline := ingest.NewPipeline(fragmentRepo, visionAnalyzer, []ingest.Stage{
		ingest.NewAttachmentStage(attachmentRepo),
		ingest.NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, deliveryQueue),
		ingest.NewInboxStage(inboxRepo),
		ingest.NewRecallStage(recallIndex),
	}, claude.Source{}, chatgpt.Source{}, urlsource.Source{}, filesystemdocs.Source{}, gitchanges.Source{})
	scheduleRepo := repository.NewIngestScheduleRepository(st.DB)
	return &App{
		store:           st,
		recall:          recallIndex,
		Fragments:       service.NewFragmentService(fragmentRepo, entityRepo, attachmentRepo, routingRepo, recallIndex, pipeline, visionAnalyzer, manualEnricher, corpusWriter),
		Inbox:           service.NewInboxService(inboxRepo),
		Routing:         routingSvc,
		Queue:           deliveryQueue,
		IngestQueue:     ingestQueue,
		IngestSchedules: service.NewIngestScheduleService(scheduleRepo),
		InboxReviewer: service.NewInboxReviewerService(
			fragmentRepo,
			entityRepo,
			attachmentRepo,
			inboxRepo,
			manualEnricher,
			corpusWriter,
			stackExplorerClient,
			cfg.Reviewer.StackExplorerScan,
		),
		Jobs: service.NewJobsService(
			repository.NewIngestJobQueueRepository(st.DB),
			scheduleRepo,
			fragmentRepo,
			cfg,
		),
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

// openStoreWithRetry opens the store, retrying briefly to ride out the
// transient "database is locked" error that can occur when several runtime
// goroutines (queue drainer, ingest worker, scheduler) open the SQLite DB
// concurrently at startup. Long-lived runtimes open the store once, so a
// single transient failure must not kill them permanently.
func openStoreWithRetry(dbPath string) (*store.Store, error) {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		st, err := store.Open(dbPath)
		if err == nil {
			return st, nil
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	return nil, fmt.Errorf("open store after retries: %w", lastErr)
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
