package service

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

// changeFeedClient is a client of the change feed: it keeps one cursor per
// principal and reads from it to caught_up.
type changeFeedClient struct {
	t       *testing.T
	reader  *ReaderService
	limit   int
	cursors map[string]string
}

func newChangeFeedClient(t *testing.T, reader *ReaderService, limit int) *changeFeedClient {
	return &changeFeedClient{t: t, reader: reader, limit: limit, cursors: map[string]string{}}
}

// sync reads the principal's feed from its stored cursor to caught_up and
// returns every entry seen, in order.
func (c *changeFeedClient) sync(principal string) []capturecontract.ReaderChange {
	c.t.Helper()
	var entries []capturecontract.ReaderChange
	for page := 0; ; page++ {
		if page > 1000 {
			c.t.Fatal("change feed never reported caught_up")
		}
		result, err := c.reader.Changes(context.Background(), ReaderChangesRequest{
			PrincipalID: principal, After: c.cursors[principal], Limit: c.limit})
		if err != nil {
			c.t.Fatalf("changes for %s: %v", principal, err)
		}
		if result.NextCursor == "" {
			c.t.Fatal("change feed page has no next_cursor")
		}
		if !result.CaughtUp && len(result.Items) != c.limit {
			c.t.Fatalf("page of %d items is not caught up at limit %d", len(result.Items), c.limit)
		}
		entries = append(entries, result.Items...)
		c.cursors[principal] = result.NextCursor
		if result.CaughtUp {
			break
		}
	}
	for index := 1; index < len(entries); index++ {
		if entries[index].ChangeSeq < entries[index-1].ChangeSeq {
			c.t.Fatalf("change feed out of sequence order: %d after %d", entries[index].ChangeSeq, entries[index-1].ChangeSeq)
		}
	}
	return entries
}

func changedFragmentIDs(entries []capturecontract.ReaderChange) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.FragmentID)
	}
	sort.Strings(ids)
	return ids
}

func TestReaderChangesFullSyncReturnsEveryItemOnceThenOnlyWhatChanged(t *testing.T) {
	st, collections, reader, ids := readerCollectionFixture(t, 7)
	_ = collections
	ctx := context.Background()
	const principal = "reader-a"
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)

	// State that exists before the first sync must be on the synced items.
	if _, err := commands.Execute(ctx, principal, ids[2], readerContractCommand("pre-read", "mark_read", 0, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Execute(ctx, principal, ids[4], readerContractCommand("pre-tag", "add_tag", 0, func(c *capturecontract.ReaderCommand) { c.Tag = "Keep" })); err != nil {
		t.Fatal(err)
	}

	client := newChangeFeedClient(t, reader, 3)
	first := client.sync(principal)
	want := append([]string(nil), ids...)
	sort.Strings(want)
	if got := changedFragmentIDs(first); !reflect.DeepEqual(got, want) {
		t.Fatalf("first sync returned %v, want every item exactly once %v", got, want)
	}
	for _, entry := range first {
		current, err := reader.Get(ctx, ReaderGetRequest{FragmentID: entry.FragmentID, PrincipalID: principal})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(entry.Item, current) {
			t.Fatalf("synced item %s is not at its latest state", entry.FragmentID)
		}
		if entry.ChangeSeq < 1 || len(entry.Kinds) == 0 {
			t.Fatalf("entry %s has change_seq=%d kinds=%v", entry.FragmentID, entry.ChangeSeq, entry.Kinds)
		}
	}
	if again := client.sync(principal); len(again) != 0 {
		t.Fatalf("second sync with nothing changed returned %v", changedFragmentIDs(again))
	}

	// One change afterwards appears on the next call, and nothing else does.
	if _, err := commands.Execute(ctx, principal, ids[5], readerContractCommand("later-read", "mark_read", 0, nil)); err != nil {
		t.Fatal(err)
	}
	after := client.sync(principal)
	if len(after) != 1 || after[0].FragmentID != ids[5] || !reflect.DeepEqual(after[0].Kinds, []string{"reading_state"}) ||
		after[0].Item.ReadingState.State != "read" {
		t.Fatalf("after one change the feed returned %+v", after)
	}
	if after[0].ChangeSeq <= first[len(first)-1].ChangeSeq {
		t.Fatalf("change_seq %d did not advance past %d", after[0].ChangeSeq, first[len(first)-1].ChangeSeq)
	}
	if again := client.sync(principal); len(again) != 0 {
		t.Fatalf("sync after the change was consumed returned %v", changedFragmentIDs(again))
	}
}

func TestReaderChangesTwoChangesToOneItemYieldOneEntryWithBothKinds(t *testing.T) {
	st, _, reader, ids := readerCollectionFixture(t, 3)
	ctx := context.Background()
	const principal = "reader-a"
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)
	client := newChangeFeedClient(t, reader, 2)
	client.sync(principal)

	if _, err := commands.Execute(ctx, principal, ids[1], readerContractCommand("both-tag", "add_tag", 0, func(c *capturecontract.ReaderCommand) { c.Tag = "Go" })); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Execute(ctx, principal, ids[1], readerContractCommand("both-note", "update_curated_note", 1, func(c *capturecontract.ReaderCommand) {
		c.ExpectedNoteRevision, c.BodyMarkdown = readerIntPtr(0), "Remember this."
	})); err != nil {
		t.Fatal(err)
	}
	entries := client.sync(principal)
	if len(entries) != 1 || entries[0].FragmentID != ids[1] || !reflect.DeepEqual(entries[0].Kinds, []string{"note", "tags"}) {
		t.Fatalf("two changes to one item returned %+v", entries)
	}
	if entries[0].Item.CuratedNote == nil || !containsString(entries[0].Item.Tags.Combined, "Go") {
		t.Fatalf("entry is not the item's latest state: %+v", entries[0].Item)
	}
}

func TestReaderChangesKeepPrincipalStatePrivate(t *testing.T) {
	st, _, reader, ids := readerCollectionFixture(t, 2)
	ctx := context.Background()
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)
	client := newChangeFeedClient(t, reader, 10)
	client.sync("reader-a")
	client.sync("reader-b")

	for index, command := range []capturecontract.ReaderCommand{
		readerContractCommand("a-read", "mark_read", 0, nil),
		readerContractCommand("a-star", "set_starred", 1, func(c *capturecontract.ReaderCommand) { c.Value = boolPtr(true) }),
		readerContractCommand("a-tag", "add_tag", 0, func(c *capturecontract.ReaderCommand) { c.Tag = "private" }),
	} {
		if _, err := commands.Execute(ctx, "reader-a", ids[0], command); err != nil {
			t.Fatalf("command %d: %v", index, err)
		}
	}
	if entries := client.sync("reader-b"); len(entries) != 0 {
		t.Fatalf("reader-a's reading state and tag overlay reached reader-b's feed: %+v", entries)
	}
	mine := client.sync("reader-a")
	if len(mine) != 1 || mine[0].FragmentID != ids[0] || !reflect.DeepEqual(mine[0].Kinds, []string{"reading_state", "tags"}) {
		t.Fatalf("reader-a's own feed = %+v", mine)
	}
	// A shared change reaches both.
	if _, err := commands.Execute(ctx, "reader-a", ids[1], readerContractCommand("a-note", "update_curated_note", 0, func(c *capturecontract.ReaderCommand) {
		c.ExpectedNoteRevision, c.BodyMarkdown = readerIntPtr(0), "Shared note."
	})); err != nil {
		t.Fatal(err)
	}
	for _, principal := range []string{"reader-a", "reader-b"} {
		if entries := client.sync(principal); len(entries) != 1 || entries[0].FragmentID != ids[1] || !reflect.DeepEqual(entries[0].Kinds, []string{"note"}) {
			t.Fatalf("%s feed after a shared change = %+v", principal, entries)
		}
	}
	// A cursor belongs to the principal it was issued to.
	if _, err := reader.Changes(ctx, ReaderChangesRequest{PrincipalID: "reader-b", After: client.cursors["reader-a"]}); !IsInvalidReaderRequest(err) {
		t.Fatalf("foreign cursor error = %v, want invalid request", err)
	}
}

func TestReaderChangesCursorSurvivesAStoreReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reader-changes.db")
	open := func() (*store.Store, *ReaderService) {
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return st, NewReaderService(repository.NewReaderRepository(st.DB))
	}
	st, reader := open()
	ids := seedReaderCollectionFragments(t, st, 5)
	client := newChangeFeedClient(t, reader, 2)
	const principal = "reader-a"

	// Stop part-way through the first sync, then restart the process.
	page, err := reader.Changes(context.Background(), ReaderChangesRequest{PrincipalID: principal, Limit: 2})
	if err != nil || page.CaughtUp || len(page.Items) != 2 {
		t.Fatalf("first page = %+v %v", page, err)
	}
	seen := changedFragmentIDs(page.Items)
	client.cursors[principal] = page.NextCursor
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, reader = open()
	client.reader = reader
	seen = append(seen, changedFragmentIDs(client.sync(principal))...)
	sort.Strings(seen)
	want := append([]string(nil), ids...)
	sort.Strings(want)
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("sync resumed across a restart returned %v, want each item once %v", seen, want)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening alone changes nothing, and a change made after it is found
	// from the cursor issued before it.
	st, reader = open()
	defer st.Close()
	client.reader = reader
	if entries := client.sync(principal); len(entries) != 0 {
		t.Fatalf("reopening the store put items back in the feed: %v", changedFragmentIDs(entries))
	}
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)
	if _, err := commands.Execute(context.Background(), principal, ids[3], readerContractCommand("restart-read", "mark_read", 0, nil)); err != nil {
		t.Fatal(err)
	}
	if entries := client.sync(principal); len(entries) != 1 || entries[0].FragmentID != ids[3] {
		t.Fatalf("change after a restart = %+v", entries)
	}
}

func TestReaderChangesRejectsInvalidCursorsAndLimits(t *testing.T) {
	_, _, reader, _ := readerCollectionFixture(t, 1)
	ctx := context.Background()
	for name, request := range map[string]ReaderChangesRequest{
		"garbage cursor":  {After: "not-a-cursor"},
		"list cursor":     {After: mustEncodeReaderListCursor(t)},
		"negative limit":  {Limit: -1},
		"oversized limit": {Limit: readerChangesMaxLimit + 1},
	} {
		request.PrincipalID = "reader-a"
		if _, err := reader.Changes(ctx, request); !IsInvalidReaderRequest(err) {
			t.Errorf("%s error = %v, want invalid request", name, err)
		}
	}
	if page, err := reader.Changes(ctx, ReaderChangesRequest{PrincipalID: "reader-a", Limit: readerChangesMaxLimit}); err != nil || !page.CaughtUp || len(page.Items) != 1 {
		t.Fatalf("largest limit = %+v %v", page, err)
	}
}

func mustEncodeReaderListCursor(t *testing.T) string {
	t.Helper()
	cursor, err := encodeReaderCollectionCursor("rc-any", 3)
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}
