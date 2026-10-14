package transcript

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedBrowse inserts three synthetic sessions: a main Claude session with one
// subagent, and a Codex session with an error. Text is fake.
func seedBrowse(t *testing.T, st *Store) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sess := func(pk, tool, kind, parent, model, cwd, started string, turns int) {
		exec(`INSERT INTO tx_sessions (session_pk, tool, native_session_id, segment_key, kind, parent_session_pk, model, cwd, started_at, turn_count)
 VALUES (?,?,?,?,?,?,?,?,?,?)`, pk, tool, "native-"+pk, "", kind, parent, model, cwd, started, turns)
		exec(`INSERT INTO transcripts (id, content_digest, redaction_version, material_json, title, content) VALUES (?,?,?,?,?,?)`,
			pk, "d", 1, "{}", "", "synthetic searchable content about "+pk)
		exec(`INSERT INTO transcript_search (id, title, content) VALUES (?,?,?)`, pk, "", "synthetic searchable content about "+pk+" zebra")
	}
	sess("main1", "claude", "main", "", "m-a", "/synthetic/alpha", "2026-10-10T10:00:00Z", 2)
	sess("sub1", "claude", "subagent", "main1", "m-a", "/synthetic/alpha", "2026-10-10T10:01:00Z", 1)
	sess("cdx1", "codex", "main", "", "m-b", "/synthetic/beta", "2026-10-11T10:00:00Z", 1)
	exec(`INSERT INTO tx_turns (session_pk, seq, event_uuid, role, text_redacted, has_thinking, thinking_bytes) VALUES
 ('main1',1,'e1','user','first synthetic question
second line',0,0), ('main1',2,'e2','assistant','synthetic answer',1,42), ('cdx1',1,'e3','user','codex synthetic prompt',0,0)`)
	exec(`INSERT INTO tx_tool_calls (tool_use_id, session_pk, turn_uuid, name, result_status, result_bytes, input_redacted) VALUES
 ('t1','main1','e2','Read','ok',10,'SHOULD-NOT-LEAK'), ('t2','cdx1','e3','Bash','error',3,'')`)
	exec(`INSERT INTO tx_usage (session_pk, request_id, input_tokens, output_tokens) VALUES ('main1','r1',100,50), ('cdx1','r2',5,5)`)
}

func TestListSessionsFiltersSortsAndPages(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	ctx := context.Background()

	page, err := st.ListSessions(ctx, SessionFilter{Sort: "tokens", Desc: true})
	if err != nil || len(page.Items) != 3 || page.Items[0].ID != "main1" || page.Items[0].Tokens != 150 {
		t.Fatalf("by tokens: %+v %v", page.Items, err)
	}
	if page.Items[0].Title != "first synthetic question" {
		t.Fatalf("title fallback = %q", page.Items[0].Title)
	}
	yes := true
	page, _ = st.ListSessions(ctx, SessionFilter{HasError: &yes})
	if len(page.Items) != 1 || page.Items[0].ID != "cdx1" || page.Items[0].Errors != 1 {
		t.Fatalf("has-error: %+v", page.Items)
	}
	page, _ = st.ListSessions(ctx, SessionFilter{Tool: "claude", Kind: "main", Project: "ALPHA"})
	if len(page.Items) != 1 || page.Items[0].ID != "main1" {
		t.Fatalf("tool/kind/project: %+v", page.Items)
	}
	p1, _ := st.ListSessions(ctx, SessionFilter{Limit: 2, Sort: "started"})
	if len(p1.Items) != 2 || p1.NextCursor == "" {
		t.Fatalf("page1: %+v", p1)
	}
	p2, _ := st.ListSessions(ctx, SessionFilter{Limit: 2, Sort: "started", Cursor: p1.NextCursor})
	if len(p2.Items) != 1 || p2.NextCursor != "" || p2.Items[0].ID != "cdx1" {
		t.Fatalf("page2: %+v", p2)
	}
}

func TestSearchSessionsIsSafeAndSnippets(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	ctx := context.Background()
	page, err := st.SearchSessions(ctx, "zebra", SessionFilter{Tool: "codex"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "cdx1" || !strings.Contains(page.Items[0].Snippet, "[zebra]") {
		t.Fatalf("search: %+v %v", page.Items, err)
	}
	// FTS operators in input are neutralised rather than erroring.
	for _, q := range []string{`"`, `zebra AND (`, `NEAR(`, `a:b*`, `   `} {
		if _, err := st.SearchSessions(ctx, q, SessionFilter{}); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
	}
}

func TestSessionDetailShape(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	d, err := st.SessionDetail(context.Background(), "main1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.TurnTotal != 2 || len(d.Turns) != 2 || d.Usage.Input != 100 || len(d.Subagents) != 1 || d.Subagents[0].ID != "sub1" {
		t.Fatalf("detail: %+v", d)
	}
	if !d.Turns[1].HasThinking || d.Turns[1].ThinkingBytes != 42 || len(d.Turns[1].ToolCalls) != 1 || d.Turns[1].ToolCalls[0].Name != "Read" {
		t.Fatalf("turn 2: %+v", d.Turns[1])
	}
	if _, err := st.SessionDetail(context.Background(), "nope", 0, 0); err != ErrSessionNotFound {
		t.Fatalf("missing: %v", err)
	}
	blob, _ := jsonString(d)
	for _, banned := range []string{"SHOULD-NOT-LEAK", "native-main1", "input_redacted"} {
		if strings.Contains(blob, banned) {
			t.Fatalf("detail leaks %q", banned)
		}
	}
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func TestEnrichmentIsOptionalAndJoinsWhenPresent(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	ctx := context.Background()
	no := false
	page, _ := st.ListSessions(ctx, SessionFilter{HasEnrichment: &no})
	if len(page.Items) != 3 || page.Items[0].Enriched {
		t.Fatalf("unenriched list: %+v", page.Items)
	}
	d, _ := st.SessionDetail(ctx, "main1", 0, 0)
	if d.Enrichment != nil {
		t.Fatalf("enrichment without a row: %+v", d.Enrichment)
	}

	insert := func(prompt, status, result, created string) {
		if _, err := st.db.Exec(`INSERT INTO tx_enrichment (session_pk, tier, prompt_version, model, content_digest, status, result_json, created_at)
 VALUES ('main1', 1, ?, 'm', 'd', ?, ?, ?)`, prompt, status, result, created); err != nil {
			t.Fatal(err)
		}
	}
	insert("old", "ok", `{"title":"Old title","topics":["stale"]}`, "2026-10-12T00:00:00Z")
	insert("new", "ok", `{"title":"Synthetic enriched title","summary":"synthetic summary","topics":["alpha","beta"],"outcome":"completed",
 "decisions":[{"text":"chose X","turns":[2]}],"follow_ups":[{"text":"do Y","turns":[1]}]}`, "2026-10-13T00:00:00Z")
	insert("failed", "error", `{"title":"Failed run"}`, "2026-10-14T00:00:00Z")

	yes := true
	page, err := st.ListSessions(ctx, SessionFilter{HasEnrichment: &yes})
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "Synthetic enriched title" || len(page.Items[0].Tags) != 2 {
		t.Fatalf("enriched list: %+v %v", page.Items, err)
	}
	if page, _ = st.ListSessions(ctx, SessionFilter{Tag: "beta"}); len(page.Items) != 1 {
		t.Fatalf("tag beta: %+v", page.Items)
	}
	if page, _ = st.ListSessions(ctx, SessionFilter{Tag: "stale"}); len(page.Items) != 0 {
		t.Fatalf("stale tag from a superseded row matched: %+v", page.Items)
	}
	d, _ = st.SessionDetail(ctx, "main1", 0, 0)
	if d.Enrichment == nil || d.Enrichment.Summary != "synthetic summary" || len(d.Enrichment.Decisions) != 1 ||
		d.Enrichment.Decisions[0].Turns[0] != 2 || d.Enrichment.FollowUps[0].Text != "do Y" || d.Enrichment.Outcome != "completed" {
		t.Fatalf("detail enrichment: %+v", d.Enrichment)
	}
	f, _ := st.SessionFacets(ctx)
	if len(f.Tags) != 2 || f.Tags[0] != "alpha" {
		t.Fatalf("facet tags: %v", f.Tags)
	}
}

func TestSearchRanksFirstThenFillsSnippetsInRankOrder(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	page, err := st.SearchSessions(context.Background(), "synthetic", SessionFilter{})
	if err != nil || len(page.Items) != 3 || page.Hint != "" || page.SnippetsSkipped != 0 {
		t.Fatalf("search: %+v %v", page, err)
	}
	for _, it := range page.Items {
		if it.Snippet == "" {
			t.Fatalf("missing snippet on %s", it.ID)
		}
	}
	// A page boundary still carries a cursor and the right snippets.
	p1, _ := st.SearchSessions(context.Background(), "synthetic", SessionFilter{Limit: 2})
	p2, _ := st.SearchSessions(context.Background(), "synthetic", SessionFilter{Limit: 2, Cursor: p1.NextCursor})
	if len(p1.Items) != 2 || p1.NextCursor == "" || len(p2.Items) != 1 || p2.Items[0].Snippet == "" {
		t.Fatalf("paging: %+v / %+v", p1, p2)
	}
}

func TestSearchPastItsDeadlineReturnsAHintNotAnError(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err := st.SearchSessions(ctx, "synthetic", SessionFilter{})
	if err == nil {
		t.Fatalf("a cancelled caller is an error, got %+v", page)
	}
	// An expired deadline (not a cancellation) is the too-broad hint.
	dctx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dcancel()
	page, err = searchFailure(dctx)
	if err != nil || page.Hint != HintTooBroad || len(page.Items) != 0 {
		t.Fatalf("deadline: %+v %v", page, err)
	}
}

func TestSuggestProjectsFindsEveryDirectoryBySubstring(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	for i := 0; i < 700; i++ {
		if _, err := st.db.Exec(`INSERT INTO tx_sessions (session_pk, tool, native_session_id, segment_key, kind, cwd) VALUES (?, 'claude', ?, '', 'main', ?)`,
			fmt.Sprintf("bulk%d", i), fmt.Sprintf("n%d", i), fmt.Sprintf("/synthetic/bulk/project-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	got, err := st.SuggestProjects(ctx, "PROJECT-0699", 20)
	if err != nil || len(got) != 1 || got[0].Project != "/synthetic/bulk/project-0699" {
		t.Fatalf("deep project unreachable: %+v %v", got, err)
	}
	if got, _ = st.SuggestProjects(ctx, "bulk", 20); len(got) != 20 {
		t.Fatalf("limit not applied: %d", len(got))
	}
	if got, _ = st.SuggestProjects(ctx, "alpha", 20); len(got) != 1 || got[0].Sessions != 2 {
		t.Fatalf("alpha: %+v", got)
	}
	if got, _ = st.SuggestProjects(ctx, "", 500); len(got) != maxSuggestLimit {
		t.Fatalf("hard cap: %d", len(got))
	}
	f, _ := st.SessionFacets(ctx)
	if f.ProjectCount != 702 {
		t.Fatalf("project count = %d", f.ProjectCount)
	}
}

func TestUntitledSessionsGetAReadableFallback(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	page, _ := st.ListSessions(context.Background(), SessionFilter{Kind: "subagent"})
	if len(page.Items) != 1 || page.Items[0].Title != "Untitled session · claude · 2026-10-10" {
		t.Fatalf("fallback title: %+v", page.Items)
	}
	if _, err := st.db.Exec(`INSERT INTO tx_turns (session_pk, seq, event_uuid, role, text_redacted) VALUES ('sub1', 1, 'e9', 'user', '

   first real line   
second')`); err != nil {
		t.Fatal(err)
	}
	if page, _ = st.ListSessions(context.Background(), SessionFilter{Kind: "subagent"}); page.Items[0].Title != "first real line" {
		t.Fatalf("user line preferred: %q", page.Items[0].Title)
	}
}

func TestSearchSkipsSnippetsForOversizedDocuments(t *testing.T) {
	st := openStore(t)
	seedBrowse(t, st)
	big := strings.Repeat("filler words here ", maxSnippetDocBytes/8) + " zebra"
	if _, err := st.db.Exec(`UPDATE transcripts SET content = ? WHERE id = 'cdx1'`, big); err != nil {
		t.Fatal(err)
	}
	page, err := st.SearchSessions(context.Background(), "zebra", SessionFilter{})
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("search: %+v %v", page, err)
	}
	if page.SnippetsSkipped != 1 {
		t.Fatalf("skipped = %d", page.SnippetsSkipped)
	}
	for _, it := range page.Items {
		if (it.ID == "cdx1") != (it.Snippet == "") {
			t.Fatalf("%s snippet=%q", it.ID, it.Snippet)
		}
	}
}
