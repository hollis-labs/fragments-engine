package service

import (
	"context"
	"errors"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

func flagCommand(id, name string, expected int, value *bool) capturecontract.ReaderCommand {
	return readerContractCommand(id, name, expected, func(c *capturecontract.ReaderCommand) { c.Value = value })
}

func boolPtr(v bool) *bool { return &v }

func TestReaderFlagCommandsSetClearReplayAndStayPerPrincipal(t *testing.T) {
	st, fragment, _ := readerCommandServiceFixture(t)
	defer st.Close()
	reader := NewReaderService(repository.NewReaderRepository(st.DB))
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)
	ctx := context.Background()

	starred, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("star-1", "set_starred", 0, boolPtr(true)))
	if err != nil {
		t.Fatal(err)
	}
	assertContractJSON(t, capturecontract.SchemaReaderItem, starred.Item)
	state := starred.Item.ReadingState
	if !state.Starred || state.StarredAt == nil || state.Later || state.Archived || state.Revision != 1 || state.State != "unread" {
		t.Fatalf("after set_starred: %+v", state)
	}
	setAt := *state.StarredAt

	// Same command and idempotency key replays without a second write.
	replay, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("star-1", "set_starred", 0, boolPtr(true)))
	if err != nil || !replay.IdempotentReplay || replay.Item.ReadingState.Revision != 1 {
		t.Fatalf("replay = %+v err=%v", replay, err)
	}
	// Setting an already-set flag changes nothing and keeps the original time.
	again, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("star-2", "set_starred", 1, boolPtr(true)))
	if err != nil || again.Item.ReadingState.Revision != 1 || !again.Item.ReadingState.StarredAt.Equal(setAt) {
		t.Fatalf("redundant set = %+v err=%v", again.Item.ReadingState, err)
	}

	// Flags are independent and reading commands keep them.
	if _, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("later-1", "set_later", 1, boolPtr(true))); err != nil {
		t.Fatal(err)
	}
	read, err := commands.Execute(ctx, "reader-a", fragment.ID, readerContractCommand("read-1", "mark_read", 2, nil))
	if err != nil {
		t.Fatal(err)
	}
	if rs := read.Item.ReadingState; rs.State != "read" || !rs.Starred || !rs.Later || rs.Archived || rs.Revision != 3 {
		t.Fatalf("mark_read lost flags: %+v", rs)
	}
	for _, name := range []string{"set_later", "set_archived", "set_starred"} {
		if !containsReaderAction(read.Item.Actions, name) {
			t.Fatalf("%s not advertised: %+v", name, read.Item.Actions)
		}
	}

	// A stale revision is refused with the revision conflict (409 at HTTP).
	_, err = commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("stale", "set_later", 0, boolPtr(false)))
	var conflict *repository.ReaderRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale revision error = %v", err)
	}

	// A missing value is invalid.
	if _, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("novalue", "set_later", 3, nil)); !IsInvalidReaderCommand(err) {
		t.Fatalf("missing value error = %v", err)
	}

	// Clearing removes the flag.
	cleared, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("star-off", "set_starred", 3, boolPtr(false)))
	if err != nil || cleared.Item.ReadingState.Starred || cleared.Item.ReadingState.StarredAt != nil || !cleared.Item.ReadingState.Later {
		t.Fatalf("clear = %+v err=%v", cleared.Item.ReadingState, err)
	}

	// Another principal sees none of it.
	other, err := reader.Get(ctx, ReaderGetRequest{FragmentID: fragment.ID, PrincipalID: "reader-b"})
	if err != nil {
		t.Fatal(err)
	}
	if rs := other.ReadingState; rs.Later || rs.Starred || rs.Archived || rs.State != "unread" {
		t.Fatalf("flags leaked across principals: %+v", rs)
	}
}

func TestReaderListFlagFiltersAndArchivedDefaultExclusion(t *testing.T) {
	st, fragment, _ := readerCommandServiceFixture(t)
	defer st.Close()
	if _, err := st.DB.Exec(`INSERT INTO inbox (fragment_id, reason, staged_at) VALUES (?, 'test', '2026-09-03T20:30:00Z')`, fragment.ID); err != nil {
		t.Fatal(err)
	}
	reader := NewReaderService(repository.NewReaderRepository(st.DB))
	commands := NewReaderCommandService(repository.NewReaderCommandRepository(st.DB), nil, nil, reader)
	ctx := context.Background()
	count := func(principal string, request ReaderListRequest) int {
		t.Helper()
		request.PrincipalID = principal
		list, err := reader.List(ctx, request)
		if err != nil {
			t.Fatalf("list %+v: %v", request, err)
		}
		return len(list.Items)
	}
	for _, scope := range []string{"inbox", "library", "all"} {
		if got := count("reader-a", ReaderListRequest{Scope: scope}); got != 1 {
			t.Fatalf("%s before flags = %d", scope, got)
		}
	}

	if _, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("archive", "set_archived", 0, boolPtr(true))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal string
		request   ReaderListRequest
		want      int
	}{
		{"inbox default hides archived", "reader-a", ReaderListRequest{Scope: "inbox"}, 0},
		{"library default hides archived", "reader-a", ReaderListRequest{Scope: "library"}, 0},
		{"all keeps archived", "reader-a", ReaderListRequest{Scope: "all"}, 1},
		{"archived=true inbox", "reader-a", ReaderListRequest{Scope: "inbox", Archived: "true"}, 1},
		{"archived=true library", "reader-a", ReaderListRequest{Scope: "library", Archived: "true"}, 1},
		{"archived=false library", "reader-a", ReaderListRequest{Scope: "library", Archived: "false"}, 0},
		{"archived=false all", "reader-a", ReaderListRequest{Scope: "all", Archived: "false"}, 0},
		{"other principal unaffected", "reader-b", ReaderListRequest{Scope: "library"}, 1},
		{"other principal archived=true", "reader-b", ReaderListRequest{Scope: "library", Archived: "true"}, 0},
	} {
		if got := count(test.principal, test.request); got != test.want {
			t.Errorf("%s: got %d, want %d", test.name, got, test.want)
		}
	}

	if _, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("unarchive", "set_archived", 1, boolPtr(false))); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Execute(ctx, "reader-a", fragment.ID, flagCommand("later", "set_later", 2, boolPtr(true))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		request ReaderListRequest
		want    int
	}{
		{"unarchived is back", ReaderListRequest{Scope: "library"}, 1},
		{"later=true", ReaderListRequest{Scope: "library", Later: "true"}, 1},
		{"later=false", ReaderListRequest{Scope: "library", Later: "false"}, 0},
		{"starred=true", ReaderListRequest{Scope: "library", Starred: "true"}, 0},
		{"starred=false", ReaderListRequest{Scope: "library", Starred: "false"}, 1},
		{"later+starred", ReaderListRequest{Scope: "library", Later: "true", Starred: "true"}, 0},
		{"later with state", ReaderListRequest{Scope: "library", Later: "true", State: "unread"}, 1},
	} {
		if got := count("reader-a", test.request); got != test.want {
			t.Errorf("%s: got %d, want %d", test.name, got, test.want)
		}
	}
	if _, err := reader.List(ctx, ReaderListRequest{Scope: "all", PrincipalID: "reader-a", Later: "maybe"}); !IsInvalidReaderRequest(err) {
		t.Fatalf("invalid flag value error = %v", err)
	}
}
