package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
	_ "modernc.org/sqlite"
)

const secretQuery = "zxqvortex"

// fakeEmbedder is an OpenAI-style /v1/embeddings server on loopback. Queries
// that mention "river" embed to e1, everything else to e2.
type fakeEmbedder struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls int
	last  string
	dim   int
}

func newFakeEmbedder(t *testing.T) *fakeEmbedder {
	f := &fakeEmbedder{dim: 3}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls++
		f.last = req.Input[0]
		f.mu.Unlock()
		type d struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var out []d
		for i, in := range req.Input {
			v := make([]float32, f.dim)
			if strings.Contains(in, "river") {
				v[0] = 1
			} else {
				v[1] = 1
			}
			out = append(out, d{i, v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type semEnv struct {
	handler http.Handler
	emb     *fakeEmbedder
}

// setupSemantic writes a config pointing at the fake embedder and seeds two
// synthetic enriched sessions with vectors made by model "emb-model".
func setupSemantic(t *testing.T, embeddedModel string) semEnv {
	t.Helper()
	cfgPath := writeTranscriptAPIConfig(t, true)
	emb := newFakeEmbedder(t)
	raw, _ := os.ReadFile(cfgPath)
	root := filepath.Dir(cfgPath)
	cfg := strings.Replace(string(raw), "transcripts:\n  private_root: "+filepath.Join(root, "private")+"\n",
		"transcripts:\n  private_root: "+filepath.Join(root, "private")+"\n  enrichment:\n    embedding:\n      provider: openai_compat_local\n      base_url: "+
			emb.srv.URL+"\n      model: emb-model\n      dim: 3\n      query_instruction: find matching sessions\n", 1)
	if cfg == string(raw) {
		t.Fatal("config not rewritten")
	}
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "private")
	st, err := transcript.Open(private)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", filepath.Join(private, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []struct{ pk, title, summary, text string }{
		{"s1", "River crossing logistics", "Planned the river ferry schedule.", "the ferry timetable alpha"},
		{"s2", "Ledger cleanup", "Reconciled the ledger totals.", "the ledger columns beta"},
	} {
		for _, q := range []struct {
			q string
			a []any
		}{
			{`INSERT INTO tx_sessions (session_pk, tool, native_session_id, segment_key, kind, parent_session_pk, model, cwd, started_at, turn_count) VALUES (?, 'claude', ?, '', 'main', '', 'm', '/synthetic/p', '2026-10-10T10:00:00Z', 1)`, []any{s.pk, "n-" + s.pk}},
			{`INSERT INTO transcripts (id, content_digest, redaction_version, material_json, title, content) VALUES (?, 'd', 1, '{}', '', ?)`, []any{s.pk, s.text}},
			{`INSERT INTO transcript_search (id, title, content) VALUES (?, '', ?)`, []any{s.pk, s.text}},
			{`INSERT INTO tx_enrichment (session_pk, tier, prompt_version, model, content_digest, status, result_json, created_at) VALUES (?, 1, 'p', 'm', 'd', 'ok', ?, '2026-10-13T00:00:00Z')`,
				[]any{s.pk, fmt.Sprintf(`{"title":%q,"summary":%q,"topics":["t"]}`, s.title, s.summary)}},
		} {
			if _, err := db.Exec(q.q, q.a...); err != nil {
				t.Fatal(err)
			}
		}
	}
	for pk, v := range map[string][]float32{"s1": {1, 0, 0}, "s2": {0, 1, 0}} {
		if err := st.SaveEmbedding(t.Context(), pk, "summary", 0, embeddedModel, "doc-plain/v1", "dg", v); err != nil {
			t.Fatal(err)
		}
	}
	return semEnv{handler: NewServer(cfgPath).Handler(), emb: emb}
}

func getJSON(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	r := transcriptGet(h, path, "127.0.0.1:1234", "127.0.0.1:8091")
	var out map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	return r.Code, out
}

func ids(page map[string]any) []string {
	var out []string
	items, _ := page["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func search(mode, q string) string {
	return "/v1/transcripts/sessions?mode=" + mode + "&q=" + url.QueryEscape(q)
}

func TestSemanticSearchFramesTheQueryAndRanksByVector(t *testing.T) {
	env := setupSemantic(t, "emb-model")
	code, page := getJSON(t, env.handler, search("semantic", "river trip"))
	if code != 200 || page["mode"] != "semantic" || page["notice"] != nil {
		t.Fatalf("semantic: %d %+v", code, page)
	}
	if got := ids(page); len(got) != 2 || got[0] != "s1" {
		t.Fatalf("ranking: %v", got)
	}
	top := page["items"].([]any)[0].(map[string]any)
	if top["score"].(float64) < 0.99 || top["match"].(map[string]any)["field"] != "semantic" {
		t.Fatalf("top hit: %+v", top)
	}
	if env.emb.last != "Instruct: find matching sessions\nQuery: river trip" {
		t.Fatalf("query was not framed with the instruction: %q", env.emb.last)
	}
}

func TestSummaryAndHybridModes(t *testing.T) {
	env := setupSemantic(t, "emb-model")
	code, page := getJSON(t, env.handler, search("summary", "ferry"))
	if code != 200 || len(ids(page)) != 1 || ids(page)[0] != "s1" || page["items"].([]any)[0].(map[string]any)["match"].(map[string]any)["field"] != "summary" {
		t.Fatalf("summary: %d %+v", code, page)
	}
	// "ledger" matches s2 by text and summary; "river" pulls s1 semantically.
	_, page = getJSON(t, env.handler, search("hybrid", "river ledger"))
	if page["mode"] != "hybrid" || page["notice"] != nil {
		t.Fatalf("hybrid: %+v", page)
	}
	if got := ids(page); len(got) != 2 {
		t.Fatalf("hybrid ids: %v", got)
	}
	for _, it := range page["items"].([]any) {
		m := it.(map[string]any)["match"].(map[string]any)
		if len(m["sources"].([]any)) == 0 {
			t.Fatalf("hybrid hit without sources: %+v", m)
		}
	}
	if _, page = getJSON(t, env.handler, search("text", "ferry")); ids(page)[0] != "s1" {
		t.Fatalf("text mode: %+v", page)
	}
}

func TestStatusReportsSemanticOnlyWhenUsable(t *testing.T) {
	env := setupSemantic(t, "emb-model")
	_, st := getJSON(t, env.handler, "/v1/transcripts/status")
	if st["semantic"] != true || st["semantic_notice"] != nil {
		t.Fatalf("usable: %+v", st)
	}
	down := setupSemantic(t, "emb-model")
	down.emb.srv.Close()
	_, st = getJSON(t, down.handler, "/v1/transcripts/status")
	if st["semantic"] != false || st["semantic_notice"] != "semantic_unavailable" {
		t.Fatalf("down: %+v", st)
	}
	mism := setupSemantic(t, "some-older-model")
	_, st = getJSON(t, mism.handler, "/v1/transcripts/status")
	if st["semantic"] != false || st["semantic_notice"] != "model_mismatch" {
		t.Fatalf("mismatch: %+v", st)
	}
	// With no embedding config and no vectors the flag stays false.
	plain := NewServer(writeTranscriptAPIConfig(t, true)).Handler()
	_, st = getJSON(t, plain, "/v1/transcripts/status")
	if st["semantic"] != false || st["semantic_notice"] == nil {
		t.Fatalf("unconfigured: %+v", st)
	}
}

func TestEmbedderDownFallsBackWithAClearMessage(t *testing.T) {
	env := setupSemantic(t, "emb-model")
	env.emb.srv.Close()
	code, page := getJSON(t, env.handler, search("semantic", "ferry"))
	if code != 200 || page["notice"] != "semantic_unavailable" || len(ids(page)) != 0 {
		t.Fatalf("semantic with embedder down: %d %+v", code, page)
	}
	// Hybrid degrades to summary + transcript text and says so.
	code, page = getJSON(t, env.handler, search("hybrid", "ferry"))
	if code != 200 || page["notice"] != "semantic_unavailable" || page["mode"] != "hybrid" {
		t.Fatalf("hybrid with embedder down: %d %+v", code, page)
	}
	if got := ids(page); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("hybrid fallback results: %v", got)
	}
}

func TestModelMismatchIsReportedNotSilentlyEmpty(t *testing.T) {
	env := setupSemantic(t, "some-older-model")
	_, page := getJSON(t, env.handler, search("semantic", "river"))
	if page["notice"] != "model_mismatch" || len(ids(page)) != 0 {
		t.Fatalf("mismatch: %+v", page)
	}
	if env.emb.calls != 0 {
		t.Fatalf("embedder called %d times although no comparable vectors exist", env.emb.calls)
	}
}

func TestDimensionMismatchIsRefused(t *testing.T) {
	env := setupSemantic(t, "emb-model")
	env.emb.dim = 5 // the server now answers with a different dimension
	_, page := getJSON(t, env.handler, search("semantic", "river"))
	if page["notice"] != "dim_mismatch" || len(ids(page)) != 0 {
		t.Fatalf("dim mismatch: %+v", page)
	}
}

func TestSearchLogsNeverContainTheQueryText(t *testing.T) {
	env := setupSemantic(t, "emb-model")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	for _, mode := range []string{"text", "summary", "semantic", "hybrid"} {
		getJSON(t, env.handler, search(mode, secretQuery))
	}
	env.emb.srv.Close()
	for _, mode := range []string{"semantic", "hybrid"} {
		getJSON(t, env.handler, search(mode, secretQuery))
	}
	out := buf.String()
	if strings.Count(out, "transcripts-read") < 6 {
		t.Fatalf("expected an audit line per search, got:\n%s", out)
	}
	if strings.Contains(out, secretQuery) {
		t.Fatalf("query text reached the logs:\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "route=search") || !strings.Contains(line, "count=") {
			t.Fatalf("unexpected audit shape: %s", line)
		}
	}
}
