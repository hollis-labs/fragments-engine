package enrich

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLocalEmbeddingServer measures a real local embedding server. It only runs
// when FE_LOCAL_EMBED_URL is set (for example http://127.0.0.1:8183) and uses
// synthetic summaries from the fake gateway, never transcript text:
//
//	FE_LOCAL_EMBED_URL=http://127.0.0.1:8183 FE_LOCAL_EMBED_MODEL=qwen3-embedding-0.6b \
//	  go test ./internal/enrich -run LocalEmbeddingServer -v -count=1
func TestLocalEmbeddingServer(t *testing.T) {
	url := os.Getenv("FE_LOCAL_EMBED_URL")
	if url == "" {
		t.Skip("FE_LOCAL_EMBED_URL not set")
	}
	model := os.Getenv("FE_LOCAL_EMBED_MODEL")
	if model == "" {
		model = "qwen3-embedding-0.6b"
	}
	const instruction = "Given a search query about a past coding conversation, retrieve the conversation summaries that answer it"
	e, err := NewOpenAICompatEmbedder(url, 0, instruction, 16)
	if err != nil {
		t.Fatal(err)
	}
	topics := []struct{ key, title, summary, query string }{
		{"db", "Tune database indexes", "Added composite indexes to the orders table and rewrote slow SQL queries to cut latency.", "how did we speed up slow database queries"},
		{"net", "Debug flaky network retries", "Investigated timeouts and retry storms in the HTTP client and added exponential backoff.", "why were requests timing out and retrying"},
		{"ui", "Redesign the settings page", "Reworked the settings screen layout, spacing and dark mode colors in the React front end.", "changes to the user interface styling"},
		{"ci", "Fix the failing build pipeline", "Repaired the continuous integration workflow, cached dependencies and pinned a tool version.", "what broke the CI build and how was it fixed"},
		{"doc", "Write the release notes", "Drafted the changelog and upgrade guide describing breaking changes for the next version.", "documentation for the upcoming release"},
	}
	st := openStore(t)
	owner := map[string]string{} // session id -> topic key
	for ti, tp := range topics {
		for k := 0; k < 4; k++ {
			id := fmt.Sprintf("%s-%d", tp.key, k)
			owner[id] = tp.key
			addSession(t, st, id, old, fmt.Sprintf("%s request %d", tp.key, k), "reply")
		}
		_ = ti
	}
	gw := &fakeGW{reply: func(n int, r Request) (Response, error) {
		for _, tp := range topics {
			if strings.Contains(r.User, tp.key+" request") {
				return replyWith(tp.title, tp.summary+" Variant text "+fmt.Sprint(n)+".", tp.key)(n, r)
			}
		}
		return Response{}, fmt.Errorf("unrouted")
	}}
	if s, err := (&Runner{Store: st, GW: gw, Opts: opts()}).Run(context.Background()); err != nil || s.OK != 20 {
		t.Fatalf("enrich %+v %v", s, err)
	}
	ctx := context.Background()
	start := time.Now()
	sum, err := EmbedSummaries(ctx, st, e, model, 0)
	embedDur := time.Since(start)
	if err != nil || sum.Embedded != 20 || sum.Failed != 0 {
		t.Fatalf("embed %+v %v", sum, err)
	}
	// Vector sanity from the store.
	var dims, minDim, maxDim int
	row := rawDB(t, st).QueryRow(`SELECT COUNT(DISTINCT dim), MIN(dim), MAX(dim) FROM tx_embeddings WHERE model = ?`, model)
	if err := row.Scan(&dims, &minDim, &maxDim); err != nil || dims != 1 {
		t.Fatalf("dims %d %d %d %v", dims, minDim, maxDim, err)
	}
	// Idempotent second pass.
	if s2, _ := EmbedSummaries(ctx, st, e, model, 0); s2.Embedded != 0 || s2.Skipped != 20 {
		t.Fatalf("second pass %+v", s2)
	}
	// Retrieval: each topic's query should rank one of its own sessions first.
	hit1, hit4 := 0, 0
	var qDur time.Duration
	for _, tp := range topics {
		qs := time.Now()
		vec, err := EmbedQuery(ctx, e, model, tp.query)
		qDur += time.Since(qs)
		if err != nil {
			t.Fatal(err)
		}
		norm := 0.0
		for _, f := range vec {
			norm += float64(f) * float64(f)
		}
		if math.Abs(math.Sqrt(norm)-1) > 0.05 {
			t.Logf("note: query vector norm %.3f (not unit length)", math.Sqrt(norm))
		}
		hits, err := st.NearestSummaries(ctx, model, e.Format(), vec, 4)
		if err != nil || len(hits) != 4 {
			t.Fatalf("search %v %d", err, len(hits))
		}
		if owner[hits[0].NativeSessionID] == tp.key {
			hit1++
		}
		all := true
		for _, h := range hits {
			if owner[h.NativeSessionID] != tp.key {
				all = false
			}
		}
		if all {
			hit4++
		}
	}
	t.Logf("model=%s dim=%d vectors=%d embed_total=%s embed_per_summary=%s query_avg=%s top1_correct=%d/5 top4_all_correct=%d/5",
		model, maxDim, sum.Embedded, embedDur.Round(time.Millisecond), (embedDur / 20).Round(time.Millisecond), (qDur / 5).Round(time.Millisecond), hit1, hit4)
	if hit1 < 4 {
		t.Fatalf("retrieval is weak: top1 %d/5", hit1)
	}
}
