package transcript

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func turnSearchStore(t *testing.T) (*Store, string) {
	t.Helper()
	st := openStore(t)
	root := t.TempDir()
	two := strings.Replace(lAsst2, "answer two", "see [brackets] and answer two", 1)
	writeLines(t, filepath.Join(root, "proj", "S1.jsonl"), lUser, lAsst1, two, lResult, lAsst3, lResult2)
	pass(t, st, root, map[string]any{"tool_result_excerpt_on_error_bytes": 64})
	var id string
	if err := st.db.QueryRow(`SELECT session_pk FROM tx_sessions`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return st, id
}

func TestFindInSessionMapsHitsToTurnsAndRanges(t *testing.T) {
	st, id := turnSearchStore(t)
	ctx := context.Background()
	res, err := st.FindInSession(ctx, id, "answer", -1, 20)
	if err != nil || res.Total != 3 || len(res.Matches) != 3 || res.Truncated {
		t.Fatalf("%+v %v", res, err)
	}
	for i, m := range res.Matches {
		if m.TurnOffset != m.Seq || (i > 0 && m.TurnOffset <= res.Matches[i-1].TurnOffset) || m.Role == "" {
			t.Fatalf("turn mapping %+v", m)
		}
		var got []string
		for _, r := range m.Highlight.Ranges {
			got = append(got, string([]rune(m.Highlight.Text)[r.Start:r.End]))
		}
		if len(got) == 0 || !strings.EqualFold(got[0], "answer") || !strings.Contains(m.Snippet, "[") {
			t.Fatalf("ranges %v in %q (%q)", got, m.Highlight.Text, m.Snippet)
		}
	}
	// Paging: after the first match, then limit and truncated.
	next, _ := st.FindInSession(ctx, id, "answer", res.Matches[0].TurnOffset, 1)
	if len(next.Matches) != 1 || next.Matches[0].TurnOffset != res.Matches[1].TurnOffset || !next.Truncated || next.Total != 3 {
		t.Fatalf("%+v", next)
	}
	// A bracket in the transcript is text, not a match.
	br, _ := st.FindInSession(ctx, id, "brackets", -1, 5)
	if len(br.Matches) != 1 || len(br.Matches[0].Highlight.Ranges) != 1 || !strings.Contains(br.Matches[0].Highlight.Text, "[brackets]") {
		t.Fatalf("%+v", br)
	}
	if none, _ := st.FindInSession(ctx, id, "zzzabsent", -1, 5); none.Total != 0 || len(none.Matches) != 0 {
		t.Fatalf("%+v", none)
	}
	if _, err := st.FindInSession(ctx, strings.Repeat("0", 64), "answer", -1, 5); err != ErrSessionNotFound {
		t.Fatal(err)
	}
}

func TestSessionErrorsListsFailedCalls(t *testing.T) {
	st, id := turnSearchStore(t)
	res, err := st.SessionErrors(context.Background(), id)
	if err != nil || res.Total != 1 || len(res.Items) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	it := res.Items[0]
	if it.Tool != "Bash" || it.Target != "ls" || it.ErrorExcerpt != "boom" || it.TurnOffset == nil || it.Seq == nil {
		t.Fatalf("%+v", it)
	}
}

func TestSearchSessionsPointsAtTheBestTurn(t *testing.T) {
	st, id := turnSearchStore(t)
	page, err := st.SearchSessions(context.Background(), "brackets", SessionFilter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	it := page.Items[0]
	if it.ID != id || it.TurnOffset == nil || it.TurnSeq == nil || it.Highlight == nil || len(it.Highlight.Ranges) != 1 {
		t.Fatalf("%+v", it)
	}
	detail, _ := st.SessionDetail(context.Background(), id, *it.TurnOffset, 1)
	if len(detail.Turns) != 1 || !strings.Contains(detail.Turns[0].Text, "brackets") {
		t.Fatalf("offset does not land on the turn: %+v", detail.Turns)
	}
}

func TestTurnIndexSurvivesReuploadAndBackfills(t *testing.T) {
	st, id := turnSearchStore(t)
	before := count(t, st, `SELECT COUNT(*) FROM tx_turn_search WHERE session_pk = ?`, id)
	if before == 0 || before != count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ? AND text_redacted <> ''`, id) {
		t.Fatalf("index not maintained at ingest: %d", before)
	}
	if _, err := st.db.Exec(`DELETE FROM tx_turn_search`); err != nil {
		t.Fatal(err)
	}
	conn, err := st.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := backfillTurnSearch(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()
	if after := count(t, st, `SELECT COUNT(*) FROM tx_turn_search WHERE session_pk = ?`, id); after != before {
		t.Fatalf("backfill %d want %d", after, before)
	}
}
