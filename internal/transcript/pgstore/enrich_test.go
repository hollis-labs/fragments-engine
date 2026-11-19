package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

func enrichSession(t *testing.T, w *Writer, native string, ended time.Time, text string) string {
	t.Helper()
	d := &domain.TranscriptDetail{
		Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: native, Kind: "main", Model: "m", CWD: "/w", StartedAt: ended.Add(-time.Hour), EndedAt: ended},
		Turns: []domain.TranscriptTurn{
			{EventUUID: native + "-1", Role: "user", TS: ended.Add(-time.Hour), Text: text},
			{EventUUID: native + "-2", Role: "assistant", TS: ended, Text: "answer"},
		},
		Tools: []domain.TranscriptToolCall{{ToolUseID: native + "-c", TurnUUID: native + "-2", Name: "Read", InputJSON: `{"file_path":"/a/b.go"}`, StartedAt: ended, HasResult: true, IsError: true}},
		Usage: []domain.TranscriptUsage{{RequestID: "r1", Input: 10, Output: 5, CacheRead: 2}},
	}
	if _, err := w.Accept(context.Background(), "reg", frag(native, d)); err != nil {
		t.Fatal(err)
	}
	return transcript.SessionPK("reg", "claude-code", native, "")
}

func TestServiceEnrichmentInputAndIdleFilter(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	svc := NewService(s)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := enrichSession(t, w, "old", now.Add(-48*time.Hour), "first question")
	enrichSession(t, w, "fresh", now.Add(-time.Minute), "recent")

	refs, err := svc.MainSessions(ctx, time.Hour, now, "")
	if err != nil || len(refs) != 1 || refs[0].SessionPK != old || refs[0].NativeSessionID != "old" || refs[0].Tool != "claude" {
		t.Fatalf("idle filter: %+v %v", refs, err)
	}
	if all, _ := svc.MainSessions(ctx, 0, now, ""); len(all) != 2 {
		t.Fatalf("no idle filter: %d", len(all))
	}
	if one, _ := svc.MainSessions(ctx, 0, now, "fresh"); len(one) != 1 {
		t.Fatalf("native filter: %d", len(one))
	}
	if leg, err := svc.LegacySessions(ctx, ""); err != nil || leg != nil {
		t.Fatalf("legacy: %v %v", leg, err)
	}

	in, err := svc.EnrichmentInput(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	want := transcript.Digest([]byte("user\x00first question\x00assistant\x00answer\x00"))
	if in.ContentDigest != want || len(in.Turns) != 2 || in.TextBytes != len("first question")+len("answer") {
		t.Fatalf("input: digest=%s turns=%+v", in.ContentDigest, in.Turns)
	}
	f := in.Facts
	if f.UserTurns != 1 || f.AssistantTurns != 1 || f.ToolCalls != 1 || f.ToolErrors != 1 || f.Requests != 1 || f.TokensIn != 10 || f.TokensOut != 5 ||
		len(f.FilesTouched) != 1 || f.FilesTouched[0].Name != "/a/b.go" || f.DurationSec != 3600 || f.CWD != "/w" || f.Model != "m" {
		t.Fatalf("facts: %+v", f)
	}
}

func TestServiceEnrichmentSaveSearchSpend(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	svc := NewService(s)
	ctx := context.Background()
	pk := enrichSession(t, w, "n1", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "q")
	other := enrichSession(t, w, "n2", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "q2")

	rec := transcript.EnrichmentRecord{SessionPK: pk, Tier: 1, PromptVersion: "v1", Model: "mm", Provider: "p", ContentDigest: "d1", Status: "ok",
		ResultJSON: `{"title":"Quasar plan","summary":"We planned a quasar survey password=hunter2secret","topics":["astronomy"],"entities":{"projects":["orion"]}}`,
		TokensIn:   7, TokensOut: 3, Calls: 1, RunID: "run1"}
	if err := svc.SaveEnrichment(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveEnrichment(ctx, rec); err != nil { // upsert, not a duplicate
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT count(*) FROM enrichments`); got != "1" {
		t.Fatalf("upsert duplicated: %s", got)
	}
	if got := scalar(t, s, `SELECT count(*) FROM changes WHERE kind = 'enrichment_update'`); got != "2" {
		t.Fatalf("journal: %s", got)
	}
	if got := scalar(t, s, `SELECT CASE WHEN head = (SELECT max(seq) FROM changes) THEN 'ok' ELSE 'bad' END FROM feed_state`); got != "ok" {
		t.Fatalf("head not advanced")
	}
	if done, err := svc.EnrichmentDone(ctx, pk, 1, "v1", "mm", "d1"); err != nil || !done {
		t.Fatalf("done: %v %v", done, err)
	}
	if done, _ := svc.EnrichmentDone(ctx, pk, 1, "v1", "mm", "other"); done {
		t.Fatal("done for another digest")
	}
	got, ok, err := svc.LatestEnrichment(ctx, pk, 1)
	if err != nil || !ok || got.RunID != "run1" || got.TokensIn != 7 || strings.Contains(got.ResultJSON, "hunter2secret") || !strings.Contains(got.ResultJSON, "Quasar plan") {
		t.Fatalf("latest: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := svc.LatestEnrichment(ctx, other, 1); ok {
		t.Fatal("enrichment for the wrong session")
	}
	// a failed row is never "latest ok"
	if err := svc.SaveEnrichment(ctx, transcript.EnrichmentRecord{SessionPK: other, Tier: 1, PromptVersion: "v1", Model: "mm", Provider: "p", ContentDigest: "d2", Status: "failed", ErrorClass: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.LatestEnrichment(ctx, other, 1); ok {
		t.Fatal("failed row returned as latest ok")
	}

	hits, err := svc.SearchEnriched(ctx, "quasar orion", 10)
	if err != nil || len(hits) != 1 || hits[0].SessionPK != pk || hits[0].Title != "Quasar plan" || hits[0].NativeSessionID != "n1" {
		t.Fatalf("search: %+v %v", hits, err)
	}
	if hits, _ := svc.SearchEnriched(ctx, "quasar absentword", 10); len(hits) != 0 {
		t.Fatalf("terms must all match: %+v", hits)
	}
	if hits, _ := svc.SearchEnriched(ctx, `  "' ) `, 10); len(hits) != 0 {
		t.Fatalf("junk: %+v", hits)
	}
	// a replaced result re-indexes: old words disappear
	rec.ContentDigest = "d3"
	rec.ResultJSON = `{"title":"Nebula","summary":"different","topics":[]}`
	if err := svc.SaveEnrichment(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if hits, _ := svc.SearchEnriched(ctx, "nebula", 10); len(hits) != 1 {
		t.Fatalf("new text not indexed: %+v", hits)
	}
	if hits, _ := svc.SearchEnriched(ctx, "quasar", 10); len(hits) != 0 {
		t.Fatalf("stale text still indexed: %+v", hits)
	}

	texts, err := svc.SummaryTexts(ctx)
	if err != nil || len(texts) != 1 || texts[0].SessionPK != pk || !strings.HasPrefix(texts[0].Text, "Nebula") || texts[0].Digest != transcript.Digest([]byte(texts[0].Text)) {
		t.Fatalf("summary texts: %+v %v", texts, err)
	}

	for _, sp := range []transcript.SpendRecord{
		{RunID: "run1", SessionPK: pk, RequestID: "a", Provider: "p", Model: "m", TokensIn: 100, TokensOut: 10, CostUSD: 0.25},
		{RunID: "run1", SessionPK: pk, RequestID: "b", Provider: "p", Model: "m", TokensIn: 50, TokensOut: 5, CostUSD: 0.5},
		{RunID: "run2", SessionPK: other, RequestID: "c", Provider: "p", Model: "m", TokensIn: 1, TokensOut: 1, CostUSD: 1},
	} {
		if err := svc.AppendSpend(ctx, sp); err != nil {
			t.Fatal(err)
		}
	}
	if tot, err := svc.Spend(ctx, "run1"); err != nil || tot.Calls != 2 || tot.TokensIn != 150 || tot.TokensOut != 15 || tot.CostUSD != 0.75 {
		t.Fatalf("run1 spend: %+v %v", tot, err)
	}
	if tot, _ := svc.Spend(ctx, ""); tot.Calls != 3 || tot.CostUSD != 1.75 {
		t.Fatalf("total spend: %+v", tot)
	}
	if err := svc.AppendSpend(ctx, transcript.SpendRecord{RunID: "x", SessionPK: pk, RequestID: "n", CostUSD: -1}); err == nil {
		t.Fatal("negative cost accepted")
	}
}

func TestServiceEmbeddings(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	svc := NewService(s)
	ctx := context.Background()
	a := enrichSession(t, w, "na", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "q")
	b := enrichSession(t, w, "nb", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "q")
	c := enrichSession(t, w, "nc", time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), "q")
	for pk, title := range map[string]string{a: "A", b: "B", c: "C"} {
		if err := svc.SaveEnrichment(ctx, transcript.EnrichmentRecord{SessionPK: pk, Tier: 1, PromptVersion: "v1", Model: "m", Provider: "p", ContentDigest: "d-" + pk, Status: "ok",
			ResultJSON: `{"title":"` + title + `","summary":"s"}`}); err != nil {
			t.Fatal(err)
		}
	}
	save := func(pk, model, format string, v []float32) {
		t.Helper()
		if err := svc.SaveEmbedding(ctx, pk, "summary", 0, model, format, "dig", v); err != nil {
			t.Fatal(err)
		}
	}
	save(a, "emb", "f1", []float32{1, 0, 0, 0})
	save(b, "emb", "f1", []float32{0.9, 0.1, 0, 0})
	save(c, "emb", "f1", []float32{0, 1, 0, 0})
	save(a, "emb", "f2", []float32{0, 0, 1, 0}) // another format: never compared
	save(a, "emb", "f1", []float32{1, 0, 0, 0}) // same key again: replaced, not duplicated
	if got := scalar(t, s, `SELECT count(*) FROM embeddings`); got != "4" {
		t.Fatalf("embeddings = %s", got)
	}
	if ok, err := svc.EmbeddingCurrent(ctx, a, "summary", 0, "emb", "f1", "dig"); err != nil || !ok {
		t.Fatalf("current: %v %v", ok, err)
	}
	if ok, _ := svc.EmbeddingCurrent(ctx, a, "summary", 0, "emb", "f1", "changed"); ok {
		t.Fatal("current for changed text")
	}
	if ok, _ := svc.EmbeddingCurrent(ctx, b, "summary", 0, "emb", "f2", "dig"); ok {
		t.Fatal("current for a format never saved")
	}
	hits, err := svc.NearestSummaries(ctx, "emb", "f1", []float32{1, 0, 0, 0}, 3)
	if err != nil || len(hits) != 3 || hits[0].SessionPK != a || hits[1].SessionPK != b || hits[2].SessionPK != c || hits[0].Title != "A" {
		t.Fatalf("nearest: %+v %v", hits, err)
	}
	if hits[0].Rank < 0.999 || hits[1].Rank < 0.99 || hits[2].Rank > 0.01 {
		t.Fatalf("similarity values: %+v", hits)
	}
	if hits, _ := svc.NearestSummaries(ctx, "emb", "f1", []float32{1, 0, 0, 0}, 1); len(hits) != 1 {
		t.Fatalf("k: %+v", hits)
	}
	if hits, _ := svc.NearestSummaries(ctx, "emb", "f1", []float32{1, 0, 0}, 3); len(hits) != 0 {
		t.Fatalf("other dimension compared: %+v", hits)
	}
	if err := svc.SaveEmbedding(ctx, a, "summary", 0, "emb", "f1", "dig", []float32{0, 0, 0, 0}); err == nil {
		t.Fatal("zero vector accepted")
	}
	if err := svc.SaveEmbedding(ctx, "nosuch", "summary", 0, "emb", "f1", "dig", []float32{1, 0, 0, 0}); err == nil {
		t.Fatal("unknown session accepted")
	}
}
