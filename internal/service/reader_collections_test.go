package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// readerCollectionFixture seeds count fragments titled item-00, item-01, ...
// and returns their IDs in that order.
func readerCollectionFixture(t *testing.T, count int) (*store.Store, *ReaderCollectionService, *ReaderService, []string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "reader-collections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ids := seedReaderCollectionFragments(t, st, count)
	readerRepo := repository.NewReaderRepository(st.DB)
	return st, NewReaderCollectionService(repository.NewReaderCollectionRepository(st.DB), readerRepo),
		NewReaderService(readerRepo), ids
}

func seedReaderCollectionFragments(t *testing.T, st *store.Store, count int) []string {
	t.Helper()
	base := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("item-%02d", index)
		at := base.Add(time.Duration(index) * time.Minute)
		built, err := repository.BuildFragment(domain.PipelineFragment{Source: "url", SourceType: "article",
			SourceID: "https://example.test/" + name, Title: name, Content: name + " body",
			ContentFormat: "markdown", CreatedAt: at}, "reader-test", at)
		if err != nil {
			t.Fatal(err)
		}
		fragment, _, err := repository.NewFragmentRepository(st.DB).UpsertResolved(context.Background(), built)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, fragment.ID)
	}
	return ids
}

func collectionItemIDs(t *testing.T, collections *ReaderCollectionService, principal, collectionID string) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := collections.ListItems(context.Background(), principal, collectionID, cursor)
		if err != nil {
			t.Fatalf("list collection items: %v", err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.FragmentID)
		}
		if page.NextCursor == "" {
			return ids
		}
		cursor = page.NextCursor
	}
}

func assertCollectionRevisionConflict(t *testing.T, err error, expected, current int) {
	t.Helper()
	var conflict *repository.ReaderRevisionConflictError
	if !errors.As(err, &conflict) || conflict.Expected != expected || conflict.Current != current {
		t.Fatalf("error = %v, want reader collection revision conflict expected=%d current=%d", err, expected, current)
	}
}

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

func TestReaderCollectionLifecycleOrderAndRevisions(t *testing.T) {
	_, collections, reader, ids := readerCollectionFixture(t, 4)
	ctx := context.Background()
	const principal = "reader-a"

	created, err := collections.Create(ctx, principal, capturecontract.ReaderCollectionCreate{Name: "  Watch later ", Kind: "playlist"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Watch later" || created.Kind != "playlist" || created.Revision != 1 || created.ItemCount != 0 {
		t.Fatalf("created = %+v", created)
	}
	if defaulted, err := collections.Create(ctx, principal, capturecontract.ReaderCollectionCreate{Name: "Plain"}); err != nil || defaulted.Kind != "collection" {
		t.Fatalf("default kind: %+v %v", defaulted, err)
	}
	var nameConflict *repository.ReaderCollectionConflictError
	if _, err := collections.Create(ctx, principal, capturecontract.ReaderCollectionCreate{Name: "watch LATER"}); !errors.As(err, &nameConflict) {
		t.Fatalf("duplicate name error = %v", err)
	}
	for _, bad := range []capturecontract.ReaderCollectionCreate{{Name: "  "}, {Name: "x", Kind: "Not A Slug"}} {
		if _, err := collections.Create(ctx, principal, bad); !IsInvalidReaderRequest(err) {
			t.Fatalf("create %+v error = %v, want invalid request", bad, err)
		}
	}
	id := created.CollectionID

	// Appends are accepted from a revision behind the current one, as add_tag is.
	revision := created.Revision
	for _, fragmentID := range ids[:3] {
		updated, err := collections.AddItem(ctx, principal, id, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 1, FragmentID: fragmentID})
		if err != nil {
			t.Fatal(err)
		}
		revision = updated.Revision
	}
	if revision != 4 {
		t.Fatalf("revision after three appends = %d, want 4", revision)
	}
	if got := collectionItemIDs(t, collections, principal, id); !reflect.DeepEqual(got, ids[:3]) {
		t.Fatalf("append order = %v, want %v", got, ids[:3])
	}
	// A revision ahead of the current one is always a conflict.
	_, err = collections.AddItem(ctx, principal, id, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 9, FragmentID: ids[3]})
	assertCollectionRevisionConflict(t, err, 9, 4)
	// Adding a member again changes nothing.
	again, err := collections.AddItem(ctx, principal, id, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 4, FragmentID: ids[0]})
	if err != nil || again.Revision != 4 || again.ItemCount != 3 {
		t.Fatalf("re-add = %+v %v", again, err)
	}
	// A positioned add must carry the current revision.
	_, err = collections.AddItem(ctx, principal, id, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 3, FragmentID: ids[3], Position: intPtr(1)})
	assertCollectionRevisionConflict(t, err, 3, 4)
	inserted, err := collections.AddItem(ctx, principal, id, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 4, FragmentID: ids[3], Position: intPtr(1)})
	if err != nil || inserted.Revision != 5 || inserted.ItemCount != 4 {
		t.Fatalf("positioned add = %+v %v", inserted, err)
	}
	want := []string{ids[0], ids[3], ids[1], ids[2]}
	if got := collectionItemIDs(t, collections, principal, id); !reflect.DeepEqual(got, want) {
		t.Fatalf("order after positioned add = %v, want %v", got, want)
	}

	removed, err := collections.RemoveItem(ctx, principal, id, ids[3], 5)
	if err != nil || removed.Revision != 6 || removed.ItemCount != 3 {
		t.Fatalf("remove = %+v %v", removed, err)
	}
	_, err = collections.RemoveItem(ctx, principal, id, ids[0], 5)
	assertCollectionRevisionConflict(t, err, 5, 6)
	if noop, err := collections.RemoveItem(ctx, principal, id, ids[3], 6); err != nil || noop.Revision != 6 {
		t.Fatalf("remove non-member = %+v %v", noop, err)
	}

	// Reordering three items yields the new order on the next list.
	newOrder := []string{ids[2], ids[0], ids[1]}
	_, err = collections.Reorder(ctx, principal, id, capturecontract.ReaderCollectionOrder{ExpectedRevision: 5, FragmentIDs: newOrder})
	assertCollectionRevisionConflict(t, err, 5, 6)
	reordered, err := collections.Reorder(ctx, principal, id, capturecontract.ReaderCollectionOrder{ExpectedRevision: 6, FragmentIDs: newOrder})
	if err != nil || reordered.Revision != 7 {
		t.Fatalf("reorder = %+v %v", reordered, err)
	}
	if got := collectionItemIDs(t, collections, principal, id); !reflect.DeepEqual(got, newOrder) {
		t.Fatalf("order after reorder = %v, want %v", got, newOrder)
	}
	if same, err := collections.Reorder(ctx, principal, id, capturecontract.ReaderCollectionOrder{ExpectedRevision: 7, FragmentIDs: newOrder}); err != nil || same.Revision != 7 {
		t.Fatalf("same-order reorder = %+v %v", same, err)
	}
	for name, order := range map[string][]string{
		"missing member": {ids[2], ids[0]},
		"non-member":     {ids[2], ids[0], ids[3]},
		"duplicate":      {ids[2], ids[0], ids[0]},
	} {
		if _, err := collections.Reorder(ctx, principal, id, capturecontract.ReaderCollectionOrder{ExpectedRevision: 7, FragmentIDs: order}); !IsInvalidReaderRequest(err) {
			t.Errorf("reorder with %s error = %v, want invalid request", name, err)
		}
	}

	// Rename and describe; an unchanged update does not advance the revision.
	_, err = collections.Update(ctx, principal, id, capturecontract.ReaderCollectionUpdate{ExpectedRevision: 6, Name: strPtr("Queue")})
	assertCollectionRevisionConflict(t, err, 6, 7)
	renamed, err := collections.Update(ctx, principal, id, capturecontract.ReaderCollectionUpdate{ExpectedRevision: 7,
		Name: strPtr("Queue"), Description: strPtr("Talks"), Kind: strPtr("research")})
	if err != nil || renamed.Name != "Queue" || renamed.Description != "Talks" || renamed.Kind != "research" || renamed.Revision != 8 {
		t.Fatalf("update = %+v %v", renamed, err)
	}
	if same, err := collections.Update(ctx, principal, id, capturecontract.ReaderCollectionUpdate{ExpectedRevision: 8, Name: strPtr("Queue")}); err != nil || same.Revision != 8 {
		t.Fatalf("unchanged update = %+v %v", same, err)
	}
	if _, err := collections.Update(ctx, principal, id, capturecontract.ReaderCollectionUpdate{ExpectedRevision: 8, Name: strPtr("plain")}); !errors.As(err, &nameConflict) {
		t.Fatalf("rename onto another collection error = %v", err)
	}

	// The list carries item counts and is ordered by name.
	list, err := collections.List(ctx, principal)
	if err != nil || len(list.Collections) != 2 || list.Collections[0].Name != "Plain" || list.Collections[1].Name != "Queue" ||
		list.Collections[0].ItemCount != 0 || list.Collections[1].ItemCount != 3 {
		t.Fatalf("list = %+v %v", list, err)
	}

	// The Reader item says which collections it is in.
	item, err := reader.Get(ctx, ReaderGetRequest{FragmentID: ids[0], PrincipalID: principal})
	if err != nil {
		t.Fatal(err)
	}
	if want := []capturecontract.ReaderCollectionRef{{CollectionID: id, Name: "Queue", Kind: "research"}}; !reflect.DeepEqual(item.Collections, want) {
		t.Fatalf("item collections = %+v, want %+v", item.Collections, want)
	}
	if outside, err := reader.Get(ctx, ReaderGetRequest{FragmentID: ids[3], PrincipalID: principal}); err != nil || outside.Collections != nil {
		t.Fatalf("non-member collections = %+v %v", outside.Collections, err)
	}
	listed, err := reader.List(ctx, ReaderListRequest{Scope: "all", PrincipalID: principal})
	if err != nil {
		t.Fatal(err)
	}
	for _, listedItem := range listed.Items {
		if got, want := len(listedItem.Collections), 1; listedItem.FragmentID != ids[3] && got != want {
			t.Fatalf("listed item %s collections = %+v", listedItem.FragmentID, listedItem.Collections)
		}
	}
}

func TestReaderCollectionDeleteLeavesFragmentsUntouched(t *testing.T) {
	st, collections, reader, ids := readerCollectionFixture(t, 3)
	ctx := context.Background()
	const principal = "reader-a"
	created, err := collections.Create(ctx, principal, capturecontract.ReaderCollectionCreate{Name: "Reading list", Kind: "reading_list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragmentID := range ids {
		if _, err := collections.AddItem(ctx, principal, created.CollectionID, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 1, FragmentID: fragmentID}); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string]capturecontract.ReaderItem{}
	for _, fragmentID := range ids {
		item, err := reader.Get(ctx, ReaderGetRequest{FragmentID: fragmentID, PrincipalID: principal})
		if err != nil {
			t.Fatal(err)
		}
		before[fragmentID] = item
	}

	err = collections.Delete(ctx, principal, created.CollectionID, 2)
	assertCollectionRevisionConflict(t, err, 2, 4)
	if err := collections.Delete(ctx, principal, created.CollectionID, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := collections.Get(ctx, principal, created.CollectionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get deleted collection error = %v", err)
	}
	var fragments, members int
	if err := st.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM fragments), (SELECT COUNT(*) FROM reader_collection_items)`).Scan(&fragments, &members); err != nil {
		t.Fatal(err)
	}
	if fragments != len(ids) || members != 0 {
		t.Fatalf("after delete fragments=%d members=%d, want %d and 0", fragments, members, len(ids))
	}
	for _, fragmentID := range ids {
		after, err := reader.Get(ctx, ReaderGetRequest{FragmentID: fragmentID, PrincipalID: principal})
		if err != nil {
			t.Fatalf("fragment %s after collection delete: %v", fragmentID, err)
		}
		want := before[fragmentID]
		want.Collections = nil
		if !reflect.DeepEqual(after, want) {
			t.Fatalf("fragment %s changed by collection delete:\n got %+v\nwant %+v", fragmentID, after, want)
		}
	}
}

func TestReaderCollectionsAreInvisibleToOtherPrincipals(t *testing.T) {
	_, collections, reader, ids := readerCollectionFixture(t, 2)
	ctx := context.Background()
	mine, err := collections.Create(ctx, "reader-a", capturecontract.ReaderCollectionCreate{Name: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	added, err := collections.AddItem(ctx, "reader-a", mine.CollectionID, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 1, FragmentID: ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	id, revision := mine.CollectionID, added.Revision

	// The same name is free for another principal, and their list is their own.
	if _, err := collections.Create(ctx, "reader-b", capturecontract.ReaderCollectionCreate{Name: "Mine"}); err != nil {
		t.Fatalf("other principal reusing a name: %v", err)
	}
	if list, err := collections.List(ctx, "reader-b"); err != nil || len(list.Collections) != 1 || list.Collections[0].CollectionID == id {
		t.Fatalf("other principal's list = %+v %v", list, err)
	}

	attempts := map[string]func() error{
		"get": func() error { _, err := collections.Get(ctx, "reader-b", id); return err },
		"items": func() error {
			_, err := collections.ListItems(ctx, "reader-b", id, "")
			return err
		},
		"update": func() error {
			_, err := collections.Update(ctx, "reader-b", id, capturecontract.ReaderCollectionUpdate{ExpectedRevision: revision, Name: strPtr("Taken")})
			return err
		},
		"add": func() error {
			_, err := collections.AddItem(ctx, "reader-b", id, capturecontract.ReaderCollectionAddItem{ExpectedRevision: revision, FragmentID: ids[1]})
			return err
		},
		"remove": func() error {
			_, err := collections.RemoveItem(ctx, "reader-b", id, ids[0], revision)
			return err
		},
		"reorder": func() error {
			_, err := collections.Reorder(ctx, "reader-b", id, capturecontract.ReaderCollectionOrder{ExpectedRevision: revision, FragmentIDs: []string{ids[0]}})
			return err
		},
		"delete": func() error { return collections.Delete(ctx, "reader-b", id, revision) },
	}
	for name, attempt := range attempts {
		if err := attempt(); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("other principal %s error = %v, want sql.ErrNoRows", name, err)
		}
	}

	// Nothing the other principal tried took effect, and membership does not
	// show on their Reader item.
	unchanged, err := collections.Get(ctx, "reader-a", id)
	if err != nil || unchanged.Name != "Mine" || unchanged.Revision != revision || unchanged.ItemCount != 1 {
		t.Fatalf("owner's collection after foreign attempts = %+v %v", unchanged, err)
	}
	if got := collectionItemIDs(t, collections, "reader-a", id); !reflect.DeepEqual(got, ids[:1]) {
		t.Fatalf("owner's items = %v", got)
	}
	if item, err := reader.Get(ctx, ReaderGetRequest{FragmentID: ids[0], PrincipalID: "reader-b"}); err != nil || item.Collections != nil {
		t.Fatalf("other principal's item collections = %+v %v", item.Collections, err)
	}
	if item, err := reader.Get(ctx, ReaderGetRequest{FragmentID: ids[0], PrincipalID: "reader-a"}); err != nil || len(item.Collections) != 1 {
		t.Fatalf("owner's item collections = %+v %v", item.Collections, err)
	}
}

func TestReaderCollectionItemsPageInPositionOrderWithCursor(t *testing.T) {
	total := readerPageSize*2 + 5
	_, collections, _, ids := readerCollectionFixture(t, total)
	ctx := context.Background()
	const principal = "reader-a"
	created, err := collections.Create(ctx, principal, capturecontract.ReaderCollectionCreate{Name: "Long"})
	if err != nil {
		t.Fatal(err)
	}
	// Position order is deliberately the reverse of capture order.
	want := make([]string, 0, total)
	for index := total - 1; index >= 0; index-- {
		want = append(want, ids[index])
		if _, err := collections.AddItem(ctx, principal, created.CollectionID, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 1, FragmentID: ids[index]}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	cursor, pages := "", 0
	for {
		page, err := collections.ListItems(ctx, principal, created.CollectionID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Collection.CollectionID != created.CollectionID || page.Collection.ItemCount != total || page.Collection.Revision != total+1 {
			t.Fatalf("page collection = %+v", page.Collection)
		}
		if page.NextCursor != "" && len(page.Items) != readerPageSize {
			t.Fatalf("page %d has %d items and a cursor", pages, len(page.Items))
		}
		for _, item := range page.Items {
			got = append(got, item.FragmentID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if pages != 3 || !reflect.DeepEqual(got, want) {
		t.Fatalf("pages=%d items=%d; order mismatch or wrong page count", pages, len(got))
	}

	other, err := collections.Create(ctx, principal, capturecontract.ReaderCollectionCreate{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"not-a-cursor", cursor} {
		if _, err := collections.ListItems(ctx, principal, other.CollectionID, bad); !IsInvalidReaderRequest(err) {
			t.Errorf("cursor %q on another collection error = %v, want invalid request", bad, err)
		}
	}
}

func TestReaderCollectionResolvesAliasesAndRejectsUnknownFragments(t *testing.T) {
	st, collections, _, ids := readerCollectionFixture(t, 1)
	ctx := context.Background()
	const principal = "reader-a"
	alias := insertReaderAlias(t, st.DB, ids[0])
	created, err := collections.Create(ctx, principal, capturecontract.ReaderCollectionCreate{Name: "Aliases"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collections.AddItem(ctx, principal, created.CollectionID, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 1, FragmentID: "no-such-fragment"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown fragment error = %v", err)
	}
	added, err := collections.AddItem(ctx, principal, created.CollectionID, capturecontract.ReaderCollectionAddItem{ExpectedRevision: 1, FragmentID: alias})
	if err != nil || added.ItemCount != 1 {
		t.Fatalf("add by alias = %+v %v", added, err)
	}
	if got := collectionItemIDs(t, collections, principal, created.CollectionID); !reflect.DeepEqual(got, ids) {
		t.Fatalf("alias stored as %v, want canonical %v", got, ids)
	}
	if removed, err := collections.RemoveItem(ctx, principal, created.CollectionID, alias, added.Revision); err != nil || removed.ItemCount != 0 {
		t.Fatalf("remove by alias = %+v %v", removed, err)
	}
}
