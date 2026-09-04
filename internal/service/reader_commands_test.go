package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

type commandProjector struct {
	repo *repository.ReaderCommandRepository
}

func (p commandProjector) ProjectReaderItem(ctx context.Context, principalID, fragmentID string) (capturecontract.ReaderItem, error) {
	revision, err := p.repo.GetAggregateRevision(ctx, principalID, fragmentID)
	if err != nil {
		return capturecontract.ReaderItem{}, err
	}
	return capturecontract.ReaderItem{FragmentID: fragmentID, Revision: revision}, nil
}

type countingReaderEffects struct {
	mu          sync.Mutex
	routes      int
	materialize int
	routeErr    error
}

func (f *countingReaderEffects) RouteFragment(_ context.Context, fragmentID, _ string) (domain.RouteApplyItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes++
	return domain.RouteApplyItem{FragmentID: fragmentID, Status: "routed"}, f.routeErr
}

func (f *countingReaderEffects) MaterializeFragmentToDestinationID(_ context.Context, fragmentID, destinationID string) (domain.FragmentMaterializeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.materialize++
	return domain.FragmentMaterializeResult{FragmentID: fragmentID, DestinationID: destinationID, WrittenPath: "/tmp/result"}, nil
}

func TestReaderCommandServiceExecutesFrozenUnionAndResolvesAliases(t *testing.T) {
	st, fragment, asset := readerCommandServiceFixture(t)
	defer st.Close()
	repo := repository.NewReaderCommandRepository(st.DB)
	effects := &countingReaderEffects{}
	svc := NewReaderCommandService(repo, NewAssetAcquisitionService(repository.NewMediaRepository(st.DB)), effects, commandProjector{repo})
	now := time.Date(2026, 9, 3, 21, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	aliasID := insertReaderAlias(t, st.DB, fragment.ID)

	commands := []capturecontract.ReaderCommand{
		readerContractCommand("add", "add_tag", 0, func(c *capturecontract.ReaderCommand) { c.Tag = "Research" }),
		readerContractCommand("append", "append_capture_note", 0, func(c *capturecontract.ReaderCommand) {
			c.AnnotationID, c.Text = "reader-annotation", "A capture note"
		}),
		readerContractCommand("curated", "update_curated_note", 0, func(c *capturecontract.ReaderCommand) {
			c.ExpectedNoteRevision, c.BodyMarkdown = readerIntPtr(0), "# Curated"
		}),
		readerContractCommand("progress", "set_reading_progress", 0, func(c *capturecontract.ReaderCommand) {
			c.Position = &capturecontract.ReadingPosition{Kind: "article", Progress: readerFloatPtr(.4)}
		}),
		readerContractCommand("read", "mark_read", 1, nil),
		readerContractCommand("unread", "mark_unread", 2, nil),
		readerContractCommand("acquire", "request_asset_acquisition", 4, func(c *capturecontract.ReaderCommand) {
			c.MediaAssetID, c.VariantKind, c.RequestedCustody = asset.ID, "original", "mirror"
		}),
		readerContractCommand("route", "route", 7, func(c *capturecontract.ReaderCommand) { c.RouteID = "route-reader" }),
		readerContractCommand("materialize", "materialize", 8, func(c *capturecontract.ReaderCommand) { c.DestinationID = "destination-reader" }),
		readerContractCommand("remove", "remove_tag", 9, func(c *capturecontract.ReaderCommand) { c.Tag = "Research" }),
	}
	// Add and append are commutative and intentionally carry stale aggregate
	// revisions. Reading commands carry the nested ReadingState revision.
	for index, command := range commands {
		pathID := fragment.ID
		if index == 0 {
			pathID = aliasID
		}
		result, err := svc.Execute(context.Background(), LocalReaderPrincipal, pathID, command)
		if err != nil {
			t.Fatalf("%s: %v", command.Command, err)
		}
		if result.CanonicalID != fragment.ID {
			t.Fatalf("%s did not resolve alias: %+v", command.Command, result)
		}
	}
	if effects.routes != 1 || effects.materialize != 1 {
		t.Fatalf("effect calls route=%d materialize=%d", effects.routes, effects.materialize)
	}
	var annotations, acquisitionRequests int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM capture_annotations WHERE id = 'reader-annotation'`).Scan(&annotations); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM asset_acquisition_requests`).Scan(&acquisitionRequests); err != nil {
		t.Fatal(err)
	}
	if annotations != 1 || acquisitionRequests != 1 {
		t.Fatalf("union side effects annotation=%d acquisition=%d", annotations, acquisitionRequests)
	}
	reading, err := repo.GetReadingState(context.Background(), LocalReaderPrincipal, fragment.ID)
	if err != nil || reading.State != domain.ReadingUnread || reading.Revision != 3 {
		t.Fatalf("reading state = %+v, %v", reading, err)
	}

	// Alias and canonical paths share the same semantic identity on replay.
	replay, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, commands[0])
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("canonical alias replay = %+v, %v", replay, err)
	}
	var receipts int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM reader_command_receipts`).Scan(&receipts); err != nil || receipts != len(commands) {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
}

func TestReaderEffectConcurrentReplayClaimsOnceAndNeverReclaimsUncertain(t *testing.T) {
	st, fragment, _ := readerCommandServiceFixture(t)
	defer st.Close()
	repo := repository.NewReaderCommandRepository(st.DB)
	effects := &countingReaderEffects{}
	svc := NewReaderCommandService(repo, NewAssetAcquisitionService(repository.NewMediaRepository(st.DB)), effects, commandProjector{repo})
	command := readerContractCommand("same-route", "route", 0, func(c *capturecontract.ReaderCommand) { c.RouteID = "route-reader" })
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, command)
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if effects.routes != 1 {
		t.Fatalf("exact concurrent replay invoked route %d times", effects.routes)
	}
	materialize := readerContractCommand("same-materialize", "materialize", 1, func(c *capturecontract.ReaderCommand) { c.DestinationID = "destination-reader" })
	start = make(chan struct{})
	errs = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, materialize)
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if effects.materialize != 1 {
		t.Fatalf("exact concurrent replay invoked materialize %d times", effects.materialize)
	}

	// A process observing a previously claimed command must never reclaim it.
	executing := readerContractCommand("executing", "route", 2, func(c *capturecontract.ReaderCommand) { c.RouteID = "route-reader" })
	write, err := normalizeReaderCommand(LocalReaderPrincipal, fragment.ID, executing, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReserveEffect(context.Background(), write); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := repo.ClaimEffect(context.Background(), write.CommandID, time.Now().UTC()); err != nil || !claimed {
		t.Fatalf("manual effect claim claimed=%v err=%v", claimed, err)
	}
	if _, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, executing); err != nil {
		t.Fatal(err)
	}
	if effects.routes != 1 {
		t.Fatalf("executing effect was reclaimed %d times", effects.routes)
	}

	uncertainEffects := &countingReaderEffects{routeErr: context.Canceled}
	uncertainSvc := NewReaderCommandService(repo, NewAssetAcquisitionService(repository.NewMediaRepository(st.DB)), uncertainEffects, commandProjector{repo})
	uncertain := readerContractCommand("uncertain", "route", 2, func(c *capturecontract.ReaderCommand) { c.RouteID = "route-reader" })
	if _, err := uncertainSvc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, uncertain); err != nil {
		t.Fatal(err)
	}
	if _, err := uncertainSvc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, uncertain); err != nil {
		t.Fatal(err)
	}
	if uncertainEffects.routes != 1 {
		t.Fatalf("uncertain effect was reclaimed %d times", uncertainEffects.routes)
	}
	effectRows, err := repo.ListEffects(context.Background(), LocalReaderPrincipal, fragment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(effectRows) != 4 || effectRows[2].State != domain.ReaderCommandExecuting || effectRows[3].State != domain.ReaderCommandUncertain {
		t.Fatalf("effect states = %+v", effectRows)
	}
	failedEffects := &countingReaderEffects{routeErr: errors.New("delivery failed")}
	failedSvc := NewReaderCommandService(repo, NewAssetAcquisitionService(repository.NewMediaRepository(st.DB)), failedEffects, commandProjector{repo})
	failed := readerContractCommand("failed", "route", 3, func(c *capturecontract.ReaderCommand) { c.RouteID = "route-reader" })
	if _, err := failedSvc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, failed); err != nil {
		t.Fatal(err)
	}
	if _, err := failedSvc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, failed); err != nil {
		t.Fatal(err)
	}
	if failedEffects.routes != 1 {
		t.Fatalf("failed effect replay invoked route %d times", failedEffects.routes)
	}
	if revision, _ := repo.GetAggregateRevision(context.Background(), LocalReaderPrincipal, fragment.ID); revision != 4 {
		t.Fatalf("failed effect aggregate revision = %d, want 4", revision)
	}
}

func TestReaderCommandKeyConflictOwnershipAndFutureRevision(t *testing.T) {
	st, fragment, asset := readerCommandServiceFixture(t)
	defer st.Close()
	repo := repository.NewReaderCommandRepository(st.DB)
	svc := NewReaderCommandService(repo, NewAssetAcquisitionService(repository.NewMediaRepository(st.DB)), &countingReaderEffects{}, commandProjector{repo})
	base := readerContractCommand("tag-conflict", "add_tag", 0, func(c *capturecontract.ReaderCommand) { c.Tag = "one" })
	if _, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, base); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Tag = "two"
	if _, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, changed); err == nil {
		t.Fatal("changed payload reused command identity")
	} else {
		var conflict *repository.ReaderCommandConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("changed identity error=%T %v", err, err)
		}
	}
	reusedKey := readerContractCommand("different-command-id", "add_tag", 0, func(c *capturecontract.ReaderCommand) { c.Tag = "two" })
	reusedKey.IdempotencyKey = base.IdempotencyKey
	if _, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, reusedKey); err == nil {
		t.Fatal("changed payload reused idempotency key")
	} else {
		var conflict *repository.ReaderCommandConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("reused key error=%T %v", err, err)
		}
	}
	future := readerContractCommand("future", "add_tag", 50, func(c *capturecontract.ReaderCommand) { c.Tag = "future" })
	if _, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, future); err == nil {
		t.Fatal("future aggregate revision accepted")
	}
	wrongAsset := readerContractCommand("wrong-asset", "request_asset_acquisition", 1, func(c *capturecontract.ReaderCommand) {
		c.MediaAssetID, c.VariantKind, c.RequestedCustody = "not-owned", "original", "mirror"
	})
	if _, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, wrongAsset); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-fragment asset error=%T %v", err, err)
	}
	owned := readerContractCommand("owned-asset", "request_asset_acquisition", 0, func(c *capturecontract.ReaderCommand) {
		c.MediaAssetID, c.VariantKind, c.RequestedCustody = asset.ID, "original", "mirror"
	})
	if _, err := svc.Execute(context.Background(), "other-user", fragment.ID, owned); err != nil {
		t.Fatal(err)
	}
	if revision, _ := repo.GetAggregateRevision(context.Background(), LocalReaderPrincipal, fragment.ID); revision != 1 {
		t.Fatalf("other principal changed local aggregate: %d", revision)
	}
	principalConflict := base
	if _, err := svc.Execute(context.Background(), "other-user", fragment.ID, principalConflict); err == nil {
		t.Fatal("global command identity was reused by another principal")
	} else {
		var conflict *repository.ReaderCommandConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("principal conflict error=%T %v", err, err)
		}
	}
	missingRoute := readerContractCommand("missing-route", "route", 1, func(c *capturecontract.ReaderCommand) { c.RouteID = "missing" })
	if _, err := svc.Execute(context.Background(), LocalReaderPrincipal, fragment.ID, missingRoute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing route error=%T %v", err, err)
	}
	var missingReceipt int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM reader_command_receipts WHERE command_id = 'missing-route'`).Scan(&missingReceipt); err != nil || missingReceipt != 0 {
		t.Fatalf("invalid effect left intent: count=%d err=%v", missingReceipt, err)
	}
}

func TestValidateReadingPositionRejectsMixedAndInvalidVariants(t *testing.T) {
	tests := []domain.ReadingPosition{
		{Kind: domain.ReadingPositionNone, Page: readerIntPtr(1)},
		{Kind: domain.ReadingPositionArticle},
		{Kind: domain.ReadingPositionArticle, Progress: readerFloatPtr(1.1)},
		{Kind: domain.ReadingPositionVideo, ElapsedSeconds: readerFloatPtr(11), DurationSeconds: readerFloatPtr(10)},
		{Kind: domain.ReadingPositionGallery, AttachmentID: "asset"},
		{Kind: domain.ReadingPositionDocument, Page: readerIntPtr(0)},
		{Kind: domain.ReadingPositionAudio, ElapsedSeconds: readerFloatPtr(1), ProviderMediaID: "foreign"},
	}
	for _, position := range tests {
		if err := validateReadingPosition(position); err == nil {
			t.Fatalf("invalid position accepted: %+v", position)
		}
	}
}

func readerCommandServiceFixture(t *testing.T) (*store.Store, domain.Fragment, domain.MediaAsset) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "reader-service.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 20, 30, 0, 0, time.UTC)
	built, err := repository.BuildFragment(domain.PipelineFragment{Source: "url", SourceType: "article",
		SourceID: "https://example.test/reader", Title: "Reader", Content: "Body", CreatedAt: now}, "reader-test", now)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	fragment, _, err := repository.NewFragmentRepository(st.DB).UpsertResolved(context.Background(), built)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	media, err := repository.NewMediaRepository(st.DB).UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{{
		Asset: domain.MediaAsset{SourceRegistrationID: "browser", Provider: "web", ProviderMediaID: "media-reader",
			SourceMediaKey: "media-reader", SourceLocator: "https://example.test/media", Kind: domain.MediaVideo,
			SourceAuthority: "web", DefaultCustody: domain.CustodyReference},
		Variants: []domain.AssetVariant{{VariantIdentity: "original", Kind: domain.VariantOriginal,
			Custody: domain.CustodyReference, AcquisitionState: domain.AcquisitionReferenceOnly}},
		Attachment: domain.AttachmentRef{Role: domain.AttachmentPrimary, Position: 0},
	}}, now)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := st.DB.Exec(`
INSERT INTO capture_attempts (
 id, capture_id, idempotency_key, semantic_digest, fragment_id,
 fragment_revision_id, fragment_outcome, accepted_capture_count,
 principal_id, actor_id, client_kind, client_version, captured_at, created_at, updated_at
) VALUES ('reader-attempt', 'reader-capture', 'reader-capture-key', 'reader-capture-digest',
 ?, ?, 'inserted', 1, 'local-user', 'local-user', 'test', '1', ?, ?, ?)`,
		fragment.ID, fragment.Revision.ID, stamp, stamp, stamp); err != nil {
		st.Close()
		t.Fatal(err)
	}
	routing := repository.NewRoutingRepository(st.DB)
	if _, err := routing.AddDestination(context.Background(), domain.Destination{ID: "destination-reader", Name: "reader-destination", Kind: "file", ConfigJSON: `{}`}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if _, err := routing.AddRoute(context.Background(), domain.Route{ID: "route-reader", Name: "reader-route", DestinationID: "destination-reader"}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, fragment, media[0].Asset
}

func insertReaderAlias(t *testing.T, db *sql.DB, canonicalID string) string {
	t.Helper()
	const aliasID = "reader-legacy-alias"
	stamp := time.Date(2026, 9, 3, 19, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`
INSERT INTO fragments (
 id, source, source_type, source_id, title, content, content_hash,
 created_at, ingested_at, status, ingest_name
) VALUES (?, 'legacy', 'text', 'alias-source', 'Alias', 'body', 'alias-hash',
          ?, ?, 'inbox', 'test')`, aliasID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO fragment_identity_aliases(alias_fragment_id, fragment_id, created_at)
VALUES (?, ?, ?)`, aliasID, canonicalID, stamp); err != nil {
		t.Fatal(err)
	}
	return aliasID
}

func readerContractCommand(id, command string, expected int, mutate func(*capturecontract.ReaderCommand)) capturecontract.ReaderCommand {
	item := capturecontract.ReaderCommand{SchemaVersion: capturecontract.ReaderCommandVersion,
		Command: command, CommandID: id, IdempotencyKey: "key-" + id, ExpectedRevision: expected}
	if mutate != nil {
		mutate(&item)
	}
	return item
}

func readerIntPtr(value int) *int           { return &value }
func readerFloatPtr(value float64) *float64 { return &value }
