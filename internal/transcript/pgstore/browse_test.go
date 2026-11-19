package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
	"github.com/hollis-labs/libs/core/store"
)

type browseFixture struct {
	svc        *Service
	st         *store.Store
	s1, s2, s3 string // session ids: claude main, codex main, claude subagent
	t0         time.Time
}

func newBrowseFixture(t *testing.T) (browseFixture, func()) {
	t.Helper()
	w, s, done := openWriter(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	accept := func(native string, d *domain.TranscriptDetail) {
		t.Helper()
		if _, err := w.Accept(ctx, "mac-"+d.Session.Tool, frag(native, d)); err != nil {
			t.Fatal(err)
		}
	}
	accept("n1", &domain.TranscriptDetail{
		Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n1", Kind: "main", Model: "m1", CWD: "/work/alpha", GitBranch: "main", StartedAt: t0, EndedAt: t0.Add(time.Minute)},
		Turns: []domain.TranscriptTurn{
			{EventUUID: "e1", Role: "user", TS: t0, Text: "please plan the quasar migration"},
			{EventUUID: "e2", ParentUUID: "e1", Role: "assistant", TS: t0.Add(time.Second), Text: "sure, running checks", HasThinking: true, Thinking: "pondering nebulaword", ThinkingBytes: 20},
			{EventUUID: "e3", Role: "user", TS: t0.Add(2 * time.Second), Text: "thanks"},
		},
		Tools: []domain.TranscriptToolCall{
			{ToolUseID: "c1", TurnUUID: "e2", Name: "Bash", InputJSON: `{"command":"grep needlepattern src"}`, StartedAt: t0.Add(time.Second), HasResult: true, IsError: true, ResultSize: 12,
				Results: []domain.TranscriptResultPart{{PartID: "0", MediaType: "text/plain", Text: "boom failure", Complete: true}}},
			{ToolUseID: "c2", TurnUUID: "e2", Name: "Read", InputJSON: `{"file_path":"/work/alpha/a.go"}`, StartedAt: t0.Add(2 * time.Second), HasResult: true, ResultSize: 18,
				Results: []domain.TranscriptResultPart{{PartID: "0", MediaType: "text/plain", Text: "contents zebraunique", Complete: true}}},
		},
		Usage: []domain.TranscriptUsage{{RequestID: "r1", Model: "m1", TS: t0, Input: 100, Output: 50, CacheRead: 10, CacheCreation: 5}},
	})
	accept("n2", &domain.TranscriptDetail{
		Session: domain.TranscriptSession{Tool: "codex", NativeSessionID: "n2", Kind: "main", Model: "m2", CWD: "/work/beta", StartedAt: t0.Add(time.Hour), EndedAt: t0.Add(time.Hour + time.Minute)},
		Turns:   []domain.TranscriptTurn{{EventUUID: "e1", Role: "user", TS: t0.Add(time.Hour), Text: "hello beta world"}},
		Usage:   []domain.TranscriptUsage{{RequestID: "r1", Model: "m2", TS: t0.Add(time.Hour), Input: 1, Output: 2}},
	})
	accept("n1-sub", &domain.TranscriptDetail{
		Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n1-sub", Kind: "subagent", ParentNativeID: "n1", AgentType: "explorer", Model: "m1", CWD: "/work/alpha", StartedAt: t0.Add(time.Second)},
		Turns:   []domain.TranscriptTurn{{EventUUID: "e1", Role: "user", TS: t0.Add(time.Second), Text: "explore the tree"}},
	})
	svc := NewService(s)
	id := func(native, kind string) string {
		return scalar(t, s, `SELECT session_id FROM sessions WHERE native_session_id = ? AND kind = ?`, native, kind)
	}
	return browseFixture{svc: svc, st: s, s1: id("n1", "main"), s2: id("n2", "main"), s3: id("n1-sub", "subagent"), t0: t0}, done
}

func TestBrowseListFilterAndPaging(t *testing.T) {
	f, done := newBrowseFixture(t)
	defer done()
	ctx := context.Background()
	all, err := f.svc.ListSessions(ctx, transcript.SessionFilter{Desc: true})
	if err != nil || len(all.Items) != 3 || all.Items[0].ID != f.s2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	// summary numbers for the claude main session
	var got transcript.SessionSummary
	for _, it := range all.Items {
		if it.ID == f.s1 {
			got = it
		}
	}
	if got.Tool != "claude" || got.Turns != 3 || got.Tokens != 165 || got.ToolCalls != 2 || got.Errors != 1 || got.Project != "/work/alpha" || got.StartedAt == "" || got.Title != "please plan the quasar migration" {
		t.Fatalf("summary: %+v", got)
	}
	// filters
	for name, c := range map[string]struct {
		flt  transcript.SessionFilter
		want int
	}{
		"tool":    {transcript.SessionFilter{Tool: "codex"}, 1},
		"project": {transcript.SessionFilter{Project: "ALPHA"}, 2},
		"model":   {transcript.SessionFilter{Model: "m2"}, 1},
		"kind":    {transcript.SessionFilter{Kind: "subagent"}, 1},
		"since":   {transcript.SessionFilter{Since: f.t0.Add(30 * time.Minute)}, 1},
		"until":   {transcript.SessionFilter{Until: f.t0.Add(30 * time.Minute)}, 2},
	} {
		p, err := f.svc.ListSessions(ctx, c.flt)
		if err != nil || len(p.Items) != c.want {
			t.Errorf("%s: %d items, want %d (%v)", name, len(p.Items), c.want, err)
		}
	}
	yes := true
	if p, _ := f.svc.ListSessions(ctx, transcript.SessionFilter{HasError: &yes}); len(p.Items) != 1 || p.Items[0].ID != f.s1 {
		t.Errorf("has_error: %+v", p.Items)
	}
	// paging and sort
	p1, _ := f.svc.ListSessions(ctx, transcript.SessionFilter{Limit: 2, Sort: "tokens", Desc: true})
	if len(p1.Items) != 2 || p1.NextCursor == "" || p1.Items[0].ID != f.s1 {
		t.Fatalf("page1: %+v", p1)
	}
	p2, _ := f.svc.ListSessions(ctx, transcript.SessionFilter{Limit: 2, Sort: "tokens", Desc: true, Cursor: p1.NextCursor})
	if len(p2.Items) != 1 || p2.NextCursor != "" {
		t.Fatalf("page2: %+v", p2)
	}
}

func TestBrowseSearchFindsToolResultsThinkingAndTurns(t *testing.T) {
	f, done := newBrowseFixture(t)
	defer done()
	ctx := context.Background()
	for _, c := range []struct{ word, field string }{
		{"quasar", "transcript"}, {"nebulaword", "thinking"}, {"zebraunique", "tool_result"}, {"needlepattern", "tool_input"},
	} {
		p, err := f.svc.SearchSessions(ctx, c.word, transcript.SessionFilter{})
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != f.s1 {
			t.Fatalf("%s: %+v %v", c.word, p, err)
		}
		it := p.Items[0]
		if it.Match == nil || it.Match.Field != c.field || !strings.Contains(it.Snippet, "["+c.word) {
			t.Errorf("%s: match %+v snippet %q", c.word, it.Match, it.Snippet)
		}
		if p.Mode != transcript.ModeText {
			t.Errorf("mode %q", p.Mode)
		}
	}
	// a tool-result hit points at the turn that owns the call (e2 = third? offset 1)
	p, _ := f.svc.SearchSessions(ctx, "zebraunique", transcript.SessionFilter{})
	it := p.Items[0]
	if it.TurnOffset == nil || *it.TurnOffset != 1 || it.TurnSeq == nil || it.Highlight == nil || len(it.Highlight.Ranges) != 1 {
		t.Fatalf("hit location: %+v", it)
	}
	r := []rune(it.Highlight.Text)[it.Highlight.Ranges[0].Start:it.Highlight.Ranges[0].End]
	if string(r) != "zebraunique" {
		t.Errorf("highlight range covers %q", string(r))
	}
	// filter narrows; a miss is empty
	if p, _ := f.svc.SearchSessions(ctx, "quasar", transcript.SessionFilter{Tool: "codex"}); len(p.Items) != 0 {
		t.Errorf("filtered search: %+v", p.Items)
	}
	if p, _ := f.svc.SearchSessions(ctx, "doesnotexistxyz", transcript.SessionFilter{}); len(p.Items) != 0 || p.Items == nil {
		t.Errorf("miss: %+v", p)
	}
	if p, _ := f.svc.SearchSessions(ctx, "hello beta", transcript.SessionFilter{}); len(p.Items) != 1 || p.Items[0].ID != f.s2 {
		t.Errorf("multi word: %+v", p.Items)
	}
}

func TestBrowseSessionDetailPagingTreeAndErrors(t *testing.T) {
	f, done := newBrowseFixture(t)
	defer done()
	ctx := context.Background()
	d, err := f.svc.SessionDetail(ctx, f.s1, 0, 2)
	if err != nil || d.TurnTotal != 3 || len(d.Turns) != 2 || d.NextTurn != 2 {
		t.Fatalf("page1: %+v %v", d, err)
	}
	if d.Usage.Requests != 1 || d.Usage.Input != 100 || d.Usage.Output != 50 || d.Usage.CacheRead != 10 || d.Usage.CacheCreation != 5 {
		t.Errorf("usage %+v", d.Usage)
	}
	asst := d.Turns[1]
	if asst.Role != "assistant" || !asst.HasThinking || len(asst.ToolCalls) != 2 || asst.EventID != "e2" {
		t.Fatalf("assistant turn: %+v", asst)
	}
	var bash transcript.ToolCallMeta
	for _, c := range asst.ToolCalls {
		if c.Name == "Bash" {
			bash = c
		}
	}
	if bash.Status != "error" || bash.ErrorExcerpt != "boom failure" || bash.Target == "" {
		t.Errorf("bash call: %+v", bash)
	}
	d2, _ := f.svc.SessionDetail(ctx, f.s1, 2, 2)
	if len(d2.Turns) != 1 || d2.NextTurn != 0 || d2.Turns[0].Text != "thanks" {
		t.Errorf("page2: %+v", d2.Turns)
	}
	if len(d.Subagents) != 1 || d.Subagents[0].ID != f.s3 || d.Subagents[0].AgentType != "explorer" {
		t.Errorf("subagents: %+v", d.Subagents)
	}
	if _, err := f.svc.SessionDetail(ctx, "nope", 0, 10); err != transcript.ErrSessionNotFound {
		t.Errorf("missing session: %v", err)
	}
	// errors list
	el, err := f.svc.SessionErrors(ctx, f.s1)
	if err != nil || el.Total != 1 || len(el.Items) != 1 {
		t.Fatalf("errors: %+v %v", el, err)
	}
	it := el.Items[0]
	if it.Tool != "Bash" || it.ErrorExcerpt != "boom failure" || it.TurnOffset == nil || *it.TurnOffset != 1 || it.Seq == nil {
		t.Errorf("error item: %+v", it)
	}
	if _, err := f.svc.SessionErrors(ctx, "nope"); err != transcript.ErrSessionNotFound {
		t.Errorf("errors missing session: %v", err)
	}
}

func TestBrowseFindInSession(t *testing.T) {
	f, done := newBrowseFixture(t)
	defer done()
	ctx := context.Background()
	r, err := f.svc.FindInSession(ctx, f.s1, "quasar", -1, 10)
	if err != nil || r.Total != 1 || len(r.Matches) != 1 || r.Matches[0].TurnOffset != 0 || r.Matches[0].Role != "user" || len(r.Matches[0].Highlight.Ranges) != 1 {
		t.Fatalf("turn text: %+v %v", r, err)
	}
	// found through the tool result of a call in turn 1
	r, _ = f.svc.FindInSession(ctx, f.s1, "zebraunique", -1, 10)
	if r.Total != 1 || r.Matches[0].TurnOffset != 1 || r.Matches[0].Role != "assistant" {
		t.Fatalf("tool result: %+v", r)
	}
	// thinking, and "after" paging
	r, _ = f.svc.FindInSession(ctx, f.s1, "nebulaword", -1, 10)
	if r.Total != 1 || r.Matches[0].TurnOffset != 1 {
		t.Fatalf("thinking: %+v", r)
	}
	if r, _ = f.svc.FindInSession(ctx, f.s1, "nebulaword", 1, 10); len(r.Matches) != 0 || r.Total != 1 {
		t.Errorf("after offset: %+v", r)
	}
	if _, err := f.svc.FindInSession(ctx, "nope", "x", -1, 5); err != transcript.ErrSessionNotFound {
		t.Errorf("missing: %v", err)
	}
	if r, _ := f.svc.FindInSession(ctx, f.s1, "   ", -1, 5); len(r.Matches) != 0 {
		t.Errorf("blank query: %+v", r)
	}
}

func TestBrowseStatsFacetsSuggestIngest(t *testing.T) {
	f, done := newBrowseFixture(t)
	defer done()
	ctx := context.Background()
	st, err := f.svc.Stats(ctx, transcript.SessionFilter{})
	if err != nil || st.Totals.Sessions != 3 || st.Totals.Turns != 5 || st.Totals.Tokens != 168 || st.Totals.ToolCalls != 2 || st.Totals.Errors != 1 {
		t.Fatalf("totals: %+v %v", st.Totals, err)
	}
	if len(st.ByDay) != 1 || st.ByDay[0].Key != "2026-11-19" || st.ByDay[0].Sessions != 3 {
		t.Errorf("by day: %+v", st.ByDay)
	}
	if len(st.ByTool) != 2 || st.ByTool[0].Key != "claude" || st.ByTool[0].Sessions != 2 {
		t.Errorf("by tool: %+v", st.ByTool)
	}
	if st2, _ := f.svc.Stats(ctx, transcript.SessionFilter{Tool: "codex"}); st2.Totals.Sessions != 1 {
		t.Errorf("filtered stats: %+v", st2.Totals)
	}
	fc, err := f.svc.SessionFacets(ctx)
	if err != nil || fc.Total != 3 || len(fc.Tools) != 2 || len(fc.Models) != 2 || fc.ProjectCount != 2 {
		t.Fatalf("facets: %+v %v", fc, err)
	}
	ps, err := f.svc.SuggestProjects(ctx, "alp", 5)
	if err != nil || len(ps) != 1 || ps[0].Project != "/work/alpha" || ps[0].Sessions != 2 {
		t.Errorf("suggest: %+v %v", ps, err)
	}
	// no checkpoints written yet -> never; then one source's checkpoint -> ok
	in, err := f.svc.Ingest(ctx)
	if err != nil || in.State != "never" || in.Sources == nil {
		t.Fatalf("ingest before: %+v %v", in, err)
	}
	w := NewWriter(f.st)
	d := &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n9", Kind: "main"},
		Turns:      []domain.TranscriptTurn{{EventUUID: "e1", Role: "user", Text: "x"}},
		Cursor:     domain.TranscriptCursor{FileSize: 1, ByteOffset: 1, LineNo: 1, TailDigest: "t", ModTime: f.t0, SourceID: "n9"},
		CursorPath: "p/n9.jsonl"}
	if _, err := w.Accept(ctx, "mac-claude", frag("n9", d)); err != nil {
		t.Fatal(err)
	}
	in, _ = f.svc.Ingest(ctx)
	if in.State != "ok" || in.LastIngestAt == "" || len(in.Sources) != 1 || in.Sources[0].Tool != "claude" {
		t.Errorf("ingest after: %+v", in)
	}
}

func TestBrowseToolCallsAndUsage(t *testing.T) {
	f, done := newBrowseFixture(t)
	defer done()
	ctx := context.Background()
	calls, err := f.svc.QueryToolCalls(ctx, transcript.ToolCallFilter{NativeSessionID: "n1"})
	if err != nil || len(calls) != 2 || calls[0].ToolUseID != "c1" || calls[0].Status != "error" || calls[0].SessionKind != "main" || calls[0].ResultBytes != 12 || calls[0].StartedAt == "" {
		t.Fatalf("calls: %+v %v", calls, err)
	}
	if c, _ := f.svc.QueryToolCalls(ctx, transcript.ToolCallFilter{Name: "Read"}); len(c) != 1 || c[0].ToolUseID != "c2" {
		t.Errorf("by name: %+v", c)
	}
	if c, _ := f.svc.QueryToolCalls(ctx, transcript.ToolCallFilter{Status: "error", Since: f.t0}); len(c) != 1 {
		t.Errorf("by status: %+v", c)
	}
	u, err := f.svc.SessionUsage(ctx, "n1")
	if err != nil || u.Requests != 1 || u.Input != 100 || u.Output != 50 || u.CacheRead != 10 || u.CacheCreation != 5 {
		t.Errorf("usage: %+v %v", u, err)
	}
}

func TestBrowseSummarySemanticAndEnrichmentFields(t *testing.T) {
	f, done := newBrowseFixture(t)
	defer done()
	ctx := context.Background()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := f.st.ExecContext(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	result := `{"title":"Quasar migration plan","summary":"Planned the migration of the quasar service.","topics":["migration","planning"],"entities":{"projects":["alpha"]}}`
	exec(`INSERT INTO enrichments (enrichment_id, session_id, content_revision, tier, prompt_version, provider, model, content_digest, status, result_redacted, created_at)
 VALUES ('en1', ?, 1, 1, 'v1', 'p', 'm', 'dg', 'ok', ?::jsonb, now())`, f.s1, result)
	exec(`INSERT INTO embedding_groups (group_id, model, format, dimension, metric) VALUES ('g1', 'emb', 'fmt', 4, 'cosine')`)
	exec(`INSERT INTO embeddings (session_id, kind, chunk_idx, group_id, dimension, enrichment_id, content_revision, text_digest, embedding, created_at)
 VALUES (?, 'summary', 0, 'g1', 4, 'en1', 1, 'd1', '[1,0,0,0]', now()), (?, 'summary', 0, 'g1', 4, NULL, 1, 'd2', '[0,1,0,0]', now())`, f.s1, f.s2)

	// enrichment shows in list, with tags and title; tag/enrichment filters use it
	p, _ := f.svc.ListSessions(ctx, transcript.SessionFilter{Tag: "migration"})
	if len(p.Items) != 1 || p.Items[0].ID != f.s1 || !p.Items[0].Enriched || p.Items[0].Title != "Quasar migration plan" || len(p.Items[0].Tags) != 2 {
		t.Fatalf("tag filter: %+v", p.Items)
	}
	no := false
	if p, _ := f.svc.ListSessions(ctx, transcript.SessionFilter{HasEnrichment: &no}); len(p.Items) != 2 {
		t.Errorf("without enrichment: %d", len(p.Items))
	}
	d, _ := f.svc.SessionDetail(ctx, f.s1, 0, 10)
	if d.Enrichment == nil || d.Enrichment.Title != "Quasar migration plan" {
		t.Errorf("detail enrichment: %+v", d.Enrichment)
	}
	fc, _ := f.svc.SessionFacets(ctx)
	if fc.Enriched != 1 || len(fc.TagCounts) != 2 || fc.TagCounts[0].Count != 1 {
		t.Errorf("facets: %+v", fc)
	}
	// summary search
	sp, err := f.svc.SearchSummaries(ctx, "quasar migration", transcript.SessionFilter{})
	if err != nil || len(sp.Items) != 1 || sp.Items[0].ID != f.s1 || sp.Items[0].Match == nil || sp.Items[0].Match.Field != "title" || sp.Mode != transcript.ModeSummary {
		t.Fatalf("summary search: %+v %v", sp, err)
	}
	// embedding groups and semantic search
	gs, err := f.svc.EmbeddingGroups(ctx)
	if err != nil || len(gs) != 1 || gs[0].Model != "emb" || gs[0].Dim != 4 || gs[0].Count != 2 {
		t.Fatalf("groups: %+v %v", gs, err)
	}
	sem, err := f.svc.SemanticSessions(ctx, "emb", "fmt", []float32{0.9, 0.1, 0, 0}, transcript.SessionFilter{Limit: 5})
	if err != nil || len(sem.Items) != 2 || sem.Items[0].ID != f.s1 || sem.Items[0].Score < 0.99 || sem.Items[1].Score > 0.2 || sem.Items[0].Match.Field != "semantic" || sem.Items[0].Match.Excerpt == "" {
		t.Fatalf("semantic: %+v %v", sem, err)
	}
	// other model or dimension compares nothing
	if sem, _ := f.svc.SemanticSessions(ctx, "other", "fmt", []float32{1, 0, 0, 0}, transcript.SessionFilter{}); len(sem.Items) != 0 {
		t.Errorf("model mismatch: %+v", sem.Items)
	}
	if sem, _ := f.svc.SemanticSessions(ctx, "emb", "fmt", []float32{1, 0, 0}, transcript.SessionFilter{}); len(sem.Items) != 0 {
		t.Errorf("dimension mismatch: %+v", sem.Items)
	}
	// filter applies to semantic results
	if sem, _ := f.svc.SemanticSessions(ctx, "emb", "fmt", []float32{1, 0, 0, 0}, transcript.SessionFilter{Tool: "codex"}); len(sem.Items) != 1 || sem.Items[0].ID != f.s2 {
		t.Errorf("filtered semantic: %+v", sem.Items)
	}
}
