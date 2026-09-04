package repository_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

type countingReaderQueryer struct {
	db      *sql.DB
	queries int
}

func (q *countingReaderQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries++
	return q.db.QueryContext(ctx, query, args...)
}

func (q *countingReaderQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.queries++
	return q.db.QueryRowContext(ctx, query, args...)
}

func TestReaderRepositoryUsesSixQueriesIndependentOfPageCardinality(t *testing.T) {
	st, captureService := openReaderRepositoryFixture(t)
	accepted := make([]capturecontract.CaptureManifestResponse, 3)
	for index := range accepted {
		accepted[index] = acceptReaderRepositoryFixture(t, captureService, index)
	}
	counting := &countingReaderQueryer{db: st.DB}
	repo := repository.NewReaderRepository(counting)

	one, err := repo.List(context.Background(), repository.ReaderPageRequest{Scope: "all", Limit: 1, PrincipalID: "local-user"})
	if err != nil {
		t.Fatalf("one-item page: %v", err)
	}
	if len(one.Bases) != 1 || counting.queries != 6 {
		t.Fatalf("one-item page bases=%d queries=%d, want 1 and 6", len(one.Bases), counting.queries)
	}

	counting.queries = 0
	many, err := repo.List(context.Background(), repository.ReaderPageRequest{Scope: "all", Limit: 10, PrincipalID: "local-user"})
	if err != nil {
		t.Fatalf("multi-item page: %v", err)
	}
	if len(many.Bases) != 3 || counting.queries != 6 {
		t.Fatalf("multi-item page bases=%d queries=%d, want 3 and 6", len(many.Bases), counting.queries)
	}
	for _, base := range many.Bases {
		if len(many.Coverage[base.FragmentRevision.ID]) != 12 || len(many.Media[base.FragmentRevision.ID]) != 1 {
			t.Fatalf("base %q lacks batched coverage/media: %+v", base.FragmentID, base)
		}
	}
	firstPage, err := repo.List(context.Background(), repository.ReaderPageRequest{Scope: "all", Limit: 2, PrincipalID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	last := firstPage.Bases[len(firstPage.Bases)-1]
	secondPage, err := repo.List(context.Background(), repository.ReaderPageRequest{Scope: "all", Limit: 10,
		Cursor: &repository.ReaderPageCursor{SortAt: last.SortAt, FragmentID: last.FragmentID}, PrincipalID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Bases) != 1 || secondPage.Bases[0].FragmentID == firstPage.Bases[0].FragmentID || secondPage.Bases[0].FragmentID == last.FragmentID {
		t.Fatalf("keyset pages overlap: first=%+v second=%+v", firstPage.Bases, secondPage.Bases)
	}

	counting.queries = 0
	detail, err := repo.Get(context.Background(), accepted[0].FragmentID, accepted[0].FragmentRevisionID, "local-user")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(detail.Bases) != 1 || counting.queries != 6 {
		t.Fatalf("detail bases=%d queries=%d, want 1 and 6", len(detail.Bases), counting.queries)
	}

	counting.queries = 0
	empty, err := repo.List(context.Background(), repository.ReaderPageRequest{Scope: "inbox", Limit: 10, PrincipalID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Bases) != 0 || counting.queries != 6 {
		t.Fatalf("empty page bases=%d queries=%d, want 0 and 6", len(empty.Bases), counting.queries)
	}
}

func TestReaderRepositoryScopesAliasAndRevisionOwnership(t *testing.T) {
	st, captureService := openReaderRepositoryFixture(t)
	first := acceptReaderRepositoryFixture(t, captureService, 0)
	second := acceptReaderRepositoryFixture(t, captureService, 1)
	if _, err := st.DB.Exec(`INSERT INTO inbox (fragment_id, reason, staged_at) VALUES (?, 'test', '2026-09-03T12:00:00Z')`, first.FragmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`DELETE FROM inbox WHERE fragment_id = ?`, second.FragmentID); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewReaderRepository(st.DB)
	inbox, err := repo.List(context.Background(), repository.ReaderPageRequest{Scope: "inbox", Limit: 10, PrincipalID: "local-user"})
	if err != nil || len(inbox.Bases) != 1 || inbox.Bases[0].FragmentID != first.FragmentID {
		t.Fatalf("inbox scope = %+v err=%v", inbox.Bases, err)
	}
	library, err := repo.List(context.Background(), repository.ReaderPageRequest{Scope: "library", Limit: 10, PrincipalID: "local-user"})
	if err != nil || len(library.Bases) != 2 {
		t.Fatalf("library scope count=%d err=%v", len(library.Bases), err)
	}

	const aliasID = "legacy-reader-alias"
	if _, err := st.DB.Exec(`INSERT INTO fragments (
id, source, source_type, source_id, title, content, content_hash, created_at,
ingested_at, status, metadata_json, ingest_name, canonical_path,
accepted_revision_id, current_revision_id
) SELECT ?, source, source_type, source_id || ':alias', title, content,
content_hash || ':alias', created_at, ingested_at, status, metadata_json,
ingest_name, canonical_path, '', '' FROM fragments WHERE id = ?`, aliasID, first.FragmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO fragment_identity_aliases (alias_fragment_id, fragment_id, reason, created_at)
VALUES (?, ?, 'test', '2026-09-03T12:00:00Z')`, aliasID, first.FragmentID); err != nil {
		t.Fatal(err)
	}
	aliased, err := repo.Get(context.Background(), aliasID, first.FragmentRevisionID, "local-user")
	if err != nil || len(aliased.Bases) != 1 || aliased.Bases[0].FragmentID != first.FragmentID {
		t.Fatalf("alias detail = %+v err=%v", aliased.Bases, err)
	}
	if _, err := repo.Get(context.Background(), first.FragmentID, second.FragmentRevisionID, "local-user"); err != sql.ErrNoRows {
		t.Fatalf("foreign revision error = %v, want sql.ErrNoRows", err)
	}
}

func openReaderRepositoryFixture(t *testing.T) (*store.Store, *service.CaptureService) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "reader.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mediaRepo := repository.NewMediaRepository(st.DB)
	blobs, err := blobstore.NewFileStore(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return st, service.NewCaptureService(repository.NewCaptureRepository(st.DB), service.NewMediaService(mediaRepo, blobs))
}

func acceptReaderRepositoryFixture(t *testing.T, captureService *service.CaptureService, index int) capturecontract.CaptureManifestResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "browser-capture-reader", "v1", "fixtures", "valid-capture-youtube.json"))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := capturecontract.DecodeCaptureEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	providerID := fmt.Sprintf("video%06d", index)
	envelope.CaptureID = fmt.Sprintf("reader-capture-%d", index)
	envelope.Source.ProviderItemID = providerID
	envelope.Source.SourceItemKey = "youtube:" + providerID
	envelope.Source.SubmittedURL = "https://www.youtube.com/watch?v=" + providerID
	envelope.Source.CanonicalURL = envelope.Source.SubmittedURL
	envelope.Document.Title = fmt.Sprintf("Reader item %d", index)
	for annotationIndex := range envelope.Annotations {
		envelope.Annotations[annotationIndex].AnnotationID = fmt.Sprintf("reader-annotation-%d-%d", index, annotationIndex)
	}
	envelope.Media[0].ClientMediaID = fmt.Sprintf("media-%d", index)
	envelope.Media[0].ProviderMediaID = providerID
	envelope.Media[0].SourceLocator = envelope.Source.CanonicalURL
	envelope.Media[0].Variants[0].SourceURL = envelope.Source.CanonicalURL
	envelope.Media[0].Variants[1].SourceURL = "https://i.ytimg.com/vi/" + providerID + "/maxresdefault.jpg"
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := captureService.AcceptManifest(context.Background(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
