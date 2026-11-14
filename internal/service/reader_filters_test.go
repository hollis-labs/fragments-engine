package service

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

type readerFilterSeed struct {
	name, provider, format, state, content string
	tags                                   []string
	at                                     time.Time
}

// readerFilterFixture seeds four fragments with distinct provider, renderer,
// state, tags and capture time, newest first: delta, charlie, bravo, alpha.
func readerFilterFixture(t *testing.T) (*ReaderService, map[string]string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "reader-filters.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	seeds := []readerFilterSeed{
		{"alpha", "web", "markdown", "", "zebra article text", []string{"go"}, base},
		{"bravo", "youtube", "markdown", "read", "bravo walkthrough", []string{"go", "video"}, base.Add(24 * time.Hour)},
		{"charlie", "web", "text", "in_progress", "charlie plain note", []string{"go", "rust"}, base.Add(48 * time.Hour)},
		{"delta", "web", "markdown", "", "delta zebra", nil, base.Add(72 * time.Hour)},
	}
	ids := map[string]string{}
	for _, seed := range seeds {
		built, err := repository.BuildFragment(domain.PipelineFragment{Source: "url", SourceType: "article",
			SourceID: "https://example.test/" + seed.name, Title: seed.name, Content: seed.content,
			ContentFormat: seed.format, CreatedAt: seed.at}, "reader-test", seed.at)
		if err != nil {
			t.Fatal(err)
		}
		fragment, _, err := repository.NewFragmentRepository(st.DB).UpsertResolved(context.Background(), built)
		if err != nil {
			t.Fatal(err)
		}
		ids[seed.name] = fragment.ID
		stamp := seed.at.Format(time.RFC3339Nano)
		mustExec(t, st.DB, `UPDATE fragments SET ingested_at = ? WHERE id = ?`, stamp, fragment.ID)
		mustExec(t, st.DB, `UPDATE fragment_source_identities SET provider = ? WHERE fragment_id = ?`, seed.provider, fragment.ID)
		if seed.state != "" {
			mustExec(t, st.DB, `INSERT INTO reading_states (principal_id, fragment_id, state, position_kind, position_json, revision, updated_at)
VALUES ('reader-user', ?, ?, 'none', '{"kind":"none"}', 1, ?)`, fragment.ID, seed.state, stamp)
		}
		for _, tag := range seed.tags {
			mustExec(t, st.DB, `INSERT INTO fragment_tag_observations (id, fragment_id, value, normalized_value, attribution_source, producer, observed_at, created_at)
VALUES (?, ?, ?, ?, 'user', 'reader-test', ?, ?)`, "tag-"+seed.name+"-"+tag, fragment.ID, strings.ToUpper(tag), tag, stamp, stamp)
		}
	}
	return NewReaderService(repository.NewReaderRepository(st.DB)), ids
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func listTitles(t *testing.T, reader *ReaderService, request ReaderListRequest) []string {
	t.Helper()
	request.Scope, request.PrincipalID = "all", "reader-user"
	list, err := reader.List(context.Background(), request)
	if err != nil {
		t.Fatalf("list %+v: %v", request, err)
	}
	titles := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		titles = append(titles, item.Display.Title.Value)
	}
	return titles
}

func TestReaderListFiltersNarrowNewestFirst(t *testing.T) {
	reader, _ := readerFilterFixture(t)
	for _, test := range []struct {
		name    string
		request ReaderListRequest
		want    []string
	}{
		{"none", ReaderListRequest{}, []string{"delta", "charlie", "bravo", "alpha"}},
		{"renderer", ReaderListRequest{Renderers: []string{"text"}}, []string{"charlie"}},
		{"renderer any-of", ReaderListRequest{Renderers: []string{"text", "article"}}, []string{"delta", "charlie", "bravo", "alpha"}},
		{"renderer comma", ReaderListRequest{Renderers: []string{"text,image"}}, []string{"charlie"}},
		{"tag", ReaderListRequest{Tags: []string{"GO"}}, []string{"charlie", "bravo", "alpha"}},
		{"tags all-of", ReaderListRequest{Tags: []string{"go", "rust"}}, []string{"charlie"}},
		{"state read", ReaderListRequest{State: "read"}, []string{"bravo"}},
		{"state unread includes no row", ReaderListRequest{State: "unread"}, []string{"delta", "alpha"}},
		{"provider", ReaderListRequest{Provider: "YouTube"}, []string{"bravo"}},
		{"since", ReaderListRequest{Since: "2026-11-03T12:00:00Z"}, []string{"delta", "charlie"}},
		{"until", ReaderListRequest{Until: "2026-11-02T12:00:00Z"}, []string{"bravo", "alpha"}},
		{"since+until", ReaderListRequest{Since: "2026-11-02T00:00:00Z", Until: "2026-11-03T23:00:00Z"}, []string{"charlie", "bravo"}},
		{"q", ReaderListRequest{Query: "zebra"}, []string{"delta", "alpha"}},
		{"q quoted syntax is literal", ReaderListRequest{Query: `zebra" OR "bravo`}, []string{}},
		{"renderer+state+tag", ReaderListRequest{Renderers: []string{"text"}, State: "in_progress", Tags: []string{"go"}}, []string{"charlie"}},
		{"renderer+state+tag no match", ReaderListRequest{Renderers: []string{"article"}, State: "in_progress", Tags: []string{"go"}}, []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := listTitles(t, reader, test.request)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestReaderListFiltersRejectInvalidValues(t *testing.T) {
	reader, _ := readerFilterFixture(t)
	for name, request := range map[string]ReaderListRequest{
		"renderer": {Renderers: []string{"podcast"}},
		"state":    {State: "archived"},
		"since":    {Since: "yesterday"},
		"until":    {Until: "2026-13-01T00:00:00Z"},
		"range":    {Since: "2026-11-03T00:00:00Z", Until: "2026-11-02T00:00:00Z"},
		"tag":      {Tags: []string{" "}},
		"q":        {Query: "   "},
	} {
		request.Scope, request.PrincipalID = "all", "reader-user"
		if _, err := reader.List(context.Background(), request); !IsInvalidReaderRequest(err) {
			t.Fatalf("%s: error=%v, want invalid Reader request", name, err)
		}
	}
}

func TestReaderListCursorIsBoundToFilters(t *testing.T) {
	filtered := ReaderListRequest{Scope: "all", PrincipalID: "reader-user", Tags: []string{"go"}}
	cursor, err := encodeReaderCursorFiltered("all", mustFingerprint(t, filtered), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "fragment")
	if err != nil {
		t.Fatal(err)
	}
	reader, _ := readerFilterFixture(t)
	for name, request := range map[string]ReaderListRequest{
		"no filters":     {},
		"other filter":   {Tags: []string{"rust"}},
		"extra filter":   {Tags: []string{"go"}, State: "read"},
		"other renderer": {Tags: []string{"go"}, Renderers: []string{"video"}},
	} {
		request.Scope, request.PrincipalID, request.Cursor = "all", "reader-user", cursor
		_, err := reader.List(context.Background(), request)
		if !IsInvalidReaderRequest(err) || !strings.Contains(err.Error(), "different filters") {
			t.Fatalf("%s: error=%v, want 'different filters' refusal", name, err)
		}
	}
	// The same filters (in any order or case) accept it.
	same := ReaderListRequest{Scope: "all", PrincipalID: "reader-user", Tags: []string{"GO", "go"}, Cursor: cursor}
	if _, err := reader.List(context.Background(), same); err != nil {
		t.Fatalf("same filters rejected: %v", err)
	}
	// An unfiltered cursor is refused once filters are added.
	plain, err := encodeReaderCursor("all", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "fragment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.List(context.Background(), ReaderListRequest{Scope: "all", PrincipalID: "reader-user", State: "read", Cursor: plain}); !IsInvalidReaderRequest(err) {
		t.Fatalf("unfiltered cursor with filters: error=%v", err)
	}
}

func mustFingerprint(t *testing.T, request ReaderListRequest) string {
	t.Helper()
	filter, err := parseReaderListFilter(request)
	if err != nil {
		t.Fatal(err)
	}
	return filter.fingerprint
}

func TestReaderListFiltersPaginateAcrossPages(t *testing.T) {
	stub := &pagedReaderStub{}
	start := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	stub.snapshot.Coverage = map[string][]repository.ReaderCoverage{}
	stub.snapshot.Media = map[string][]domain.MediaManifestItem{}
	stub.snapshot.Tags = map[string][]repository.ReaderTagFact{}
	stub.snapshot.Annotations = map[string][]domain.CaptureAnnotation{}
	stub.snapshot.Effects = map[string][]repository.ReaderEffect{}
	const total = 330
	wantIDs := []string{}
	for index := 0; index < total; index++ {
		id := fmt.Sprintf("fragment-%03d", index)
		format, content := "markdown", "body"
		if index%3 == 0 {
			format = "text" // renders as "text"
			wantIDs = append(wantIDs, id)
		}
		stub.snapshot.Bases = append(stub.snapshot.Bases, repository.ReaderBase{FragmentID: id, CaptureCount: 1,
			SortAt: start.Add(-time.Duration(index) * time.Minute),
			FragmentRevision: domain.FragmentRevision{ID: "rev-" + id, FragmentID: id, Ordinal: 1,
				Title: id, Content: content, ContentFormat: format, MetadataJSON: `{}`},
			Source: domain.SourceIdentity{SourceRegistrationID: "reader-test", Provider: "web", SourceItemKey: id, SegmentKey: "root"}})
		stub.snapshot.Coverage["rev-"+id] = testReaderCoverage(id, "rev-"+id)
	}
	reader := NewReaderService(stub)
	var got []string
	cursor, pages := "", 0
	for {
		page, err := reader.List(context.Background(), ReaderListRequest{Scope: "all", Renderers: []string{"text"}, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Items) > readerPageSize {
			t.Fatalf("page %d has %d items", pages, len(page.Items))
		}
		for _, item := range page.Items {
			if item.Renderer != "text" {
				t.Fatalf("non-matching renderer %q leaked", item.Renderer)
			}
			got = append(got, item.FragmentID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("paged %d ids, want %d in order (pages=%d)", len(got), len(wantIDs), pages)
	}
	if pages < 3 {
		t.Fatalf("expected multiple pages, got %d", pages)
	}
}

// pagedReaderStub honours the keyset cursor and limit like the repository.
type pagedReaderStub struct{ snapshot repository.ReaderSnapshot }

func (s *pagedReaderStub) List(_ context.Context, request repository.ReaderPageRequest) (repository.ReaderSnapshot, error) {
	out := s.snapshot
	out.Bases = nil
	for _, base := range s.snapshot.Bases {
		if request.Cursor != nil && !(base.SortAt.Before(request.Cursor.SortAt) ||
			(base.SortAt.Equal(request.Cursor.SortAt) && base.FragmentID < request.Cursor.FragmentID)) {
			continue
		}
		out.Bases = append(out.Bases, base)
		if len(out.Bases) == request.Limit {
			break
		}
	}
	return out, nil
}

func (s *pagedReaderStub) Get(context.Context, string, string, string) (repository.ReaderSnapshot, error) {
	return repository.ReaderSnapshot{}, sql.ErrNoRows
}
