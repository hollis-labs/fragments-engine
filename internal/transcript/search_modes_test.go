package transcript

import (
	"context"
	"strings"
	"testing"
)

// seedEnriched adds synthetic Tier 1 results to the browse fixture.
func seedEnriched(t *testing.T, st *Store) {
	t.Helper()
	seedBrowse(t, st)
	insert := func(pk, result string) {
		t.Helper()
		if _, err := st.db.Exec(`INSERT INTO tx_enrichment (session_pk, tier, prompt_version, model, content_digest, status, result_json, created_at)
 VALUES (?, 1, 'p', 'm', ?, 'ok', ?, '2026-10-13T00:00:00Z')`, pk, "d-"+pk, result); err != nil {
			t.Fatal(err)
		}
	}
	insert("main1", `{"title":"Wiring the quartz exporter","summary":"We replaced the polling loop with a queue.","topics":["export","queue"],
 "entities":{"tools":["sqlite"],"repos":["acme/quartz"]},
 "decisions":[{"text":"Adopt a bounded worker pool for the exporter","turns":[1]}],
 "follow_ups":[{"text":"Benchmark the vacuum pass","turns":[2]}]}`)
	insert("cdx1", `{"title":"Unrelated chores","summary":"Rotated logs and tidied the backlog.","topics":["ops"]}`)
}

func TestSearchSummariesMatchesEveryEnrichmentFieldAndReportsWhichOne(t *testing.T) {
	st := openStore(t)
	seedEnriched(t, st)
	ctx := context.Background()
	for _, tc := range []struct{ q, field string }{
		{"quartz", "title"}, {"queue", "topics"}, {"sqlite", "entities"}, {"polling", "summary"},
		{"worker pool", "decisions"}, {"vacuum", "follow_ups"},
	} {
		page, err := st.SearchSummaries(ctx, tc.q, SessionFilter{})
		if err != nil || len(page.Items) == 0 || page.Items[0].ID != "main1" || page.Items[0].Match == nil {
			t.Fatalf("%q: %+v %v", tc.q, page, err)
		}
		m := page.Items[0].Match
		if tc.q == "queue" {
			// "queue" is also in the summary; the topic outranks the prose.
			if m.Field != "topics" {
				t.Fatalf("%q matched %s", tc.q, m.Field)
			}
		} else if m.Field != tc.field {
			t.Fatalf("%q matched %s, want %s", tc.q, m.Field, tc.field)
		}
		if !strings.Contains(m.Excerpt, "[") || !strings.Contains(m.Excerpt, "]") {
			t.Fatalf("%q excerpt not highlighted: %q", tc.q, m.Excerpt)
		}
		if page.Mode != ModeSummary {
			t.Fatalf("mode %q", page.Mode)
		}
	}
}

func TestSearchSummariesRequiresEveryTermAndHonoursFilters(t *testing.T) {
	st := openStore(t)
	seedEnriched(t, st)
	ctx := context.Background()
	if page, _ := st.SearchSummaries(ctx, "quartz backlog", SessionFilter{}); len(page.Items) != 0 {
		t.Fatalf("terms from two sessions matched one: %+v", page.Items)
	}
	if page, _ := st.SearchSummaries(ctx, "the", SessionFilter{}); len(page.Items) != 2 {
		t.Fatalf("common word: %d", len(page.Items))
	}
	if page, _ := st.SearchSummaries(ctx, "the", SessionFilter{Tool: "codex"}); len(page.Items) != 1 || page.Items[0].ID != "cdx1" {
		t.Fatalf("tool filter: %+v", page.Items)
	}
	// FTS syntax and quotes in the query are plain text.
	for _, q := range []string{`"`, `quartz OR`, `NEAR(`, `a*`, `)(`} {
		if _, err := st.SearchSummaries(ctx, q, SessionFilter{}); err != nil {
			t.Fatalf("%q errored: %v", q, err)
		}
	}
	// Only the newest ok result counts: a superseded title must not match.
	if _, err := st.db.Exec(`INSERT INTO tx_enrichment (session_pk, tier, prompt_version, model, content_digest, status, result_json, created_at)
 VALUES ('main1', 1, 'p2', 'm', 'd2', 'ok', '{"title":"Fresh title only"}', '2026-10-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if page, _ := st.SearchSummaries(ctx, "quartz", SessionFilter{}); len(page.Items) != 0 {
		t.Fatalf("superseded result matched: %+v", page.Items)
	}
}

func TestSearchSummariesPagesWithACursor(t *testing.T) {
	st := openStore(t)
	seedEnriched(t, st)
	ctx := context.Background()
	p1, _ := st.SearchSummaries(ctx, "the", SessionFilter{Limit: 1})
	if len(p1.Items) != 1 || p1.NextCursor == "" {
		t.Fatalf("page 1: %+v", p1)
	}
	p2, _ := st.SearchSummaries(ctx, "the", SessionFilter{Limit: 1, Cursor: p1.NextCursor})
	if len(p2.Items) != 1 || p2.NextCursor != "" || p2.Items[0].ID == p1.Items[0].ID {
		t.Fatalf("page 2: %+v", p2)
	}
}

func TestExcerptIsBoundedHighlightedAndRuneSafe(t *testing.T) {
	long := strings.Repeat("ünï ", 80) + "needle " + strings.Repeat("çhar ", 80)
	got := excerpt(long, []string{"needle"})
	if !strings.Contains(got, "[needle]") || len(got) > maxExcerptBytes+16 || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("excerpt: %q (%d bytes)", got, len(got))
	}
	if !isValidUTF8(got) {
		t.Fatalf("invalid utf-8: %q", got)
	}
	if got := excerpt("a [bracket] and\nNewline Needle", []string{"needle"}); strings.Contains(got, "\n") || !strings.Contains(got, "(bracket)") || !strings.Contains(got, "[Needle]") {
		t.Fatalf("sanitised: %q", got)
	}
}

func isValidUTF8(s string) bool { return strings.ToValidUTF8(s, "\x00") == s }

func TestSemanticSessionsRanksByCosineAndNeverMixesModels(t *testing.T) {
	st := openStore(t)
	seedEnriched(t, st)
	ctx := context.Background()
	save := func(pk, model, format string, v []float32) {
		t.Helper()
		if err := st.SaveEmbedding(ctx, pk, "summary", 0, model, format, "dg", v); err != nil {
			t.Fatal(err)
		}
	}
	save("main1", "emb", "doc-plain/v1", []float32{1, 0, 0})
	save("cdx1", "emb", "doc-plain/v1", []float32{0.6, 0.8, 0})
	save("sub1", "other", "doc-plain/v1", []float32{1, 0, 0}) // different model: never compared
	save("sub1", "emb", "doc-other/v9", []float32{1, 0, 0})   // different format: never compared
	page, err := st.SemanticSessions(ctx, "emb", "doc-plain/v1", []float32{1, 0, 0}, SessionFilter{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("semantic: %+v %v", page.Items, err)
	}
	if page.Items[0].ID != "main1" || page.Items[1].ID != "cdx1" || page.Items[0].Score < 0.999 || page.Items[1].Score < 0.59 || page.Items[1].Score > 0.61 {
		t.Fatalf("order/scores: %+v", page.Items)
	}
	if m := page.Items[0].Match; m == nil || m.Field != "semantic" || !strings.Contains(m.Excerpt, "polling loop") {
		t.Fatalf("semantic match: %+v", m)
	}
	if page, _ = st.SemanticSessions(ctx, "emb", "doc-plain/v1", []float32{1, 0}, SessionFilter{}); len(page.Items) != 0 {
		t.Fatalf("wrong dimension compared: %+v", page.Items)
	}
	if page, _ = st.SemanticSessions(ctx, "emb", "doc-plain/v1", []float32{1, 0, 0}, SessionFilter{Tool: "codex", Limit: 1}); len(page.Items) != 1 || page.Items[0].ID != "cdx1" {
		t.Fatalf("filtered: %+v", page.Items)
	}
	groups, err := st.EmbeddingGroups(ctx)
	if err != nil || len(groups) != 3 {
		t.Fatalf("groups: %+v %v", groups, err)
	}
}

func TestFuseRankingsRewardsAgreementAndKeepsTheBestExplanation(t *testing.T) {
	a := SessionSummary{ID: "a", Match: &Match{Field: "summary", Excerpt: "[x] summary"}}
	b := SessionSummary{ID: "b", Match: &Match{Field: "semantic"}}
	c := SessionSummary{ID: "c", Snippet: "[x] raw", Match: &Match{Field: "transcript", Excerpt: "[x] raw"}}
	semA := SessionSummary{ID: "a", Match: &Match{Field: "semantic"}}
	got := FuseRankings(10, []string{"summary", "semantic", "transcript"}, []SessionSummary{a}, []SessionSummary{b, semA}, []SessionSummary{c, a})
	if len(got) != 3 || got[0].ID != "a" {
		t.Fatalf("a appears in all lists and should lead: %+v", got)
	}
	if got[0].Match.Field != "summary" || len(got[0].Match.Sources) != 3 {
		t.Fatalf("best explanation / sources: %+v", got[0].Match)
	}
	if got[1].ID == "a" || got[0].Score <= got[1].Score {
		t.Fatalf("scores: %+v", got)
	}
	if limited := FuseRankings(1, []string{"s"}, []SessionSummary{a, b}); len(limited) != 1 {
		t.Fatalf("limit: %d", len(limited))
	}
}
