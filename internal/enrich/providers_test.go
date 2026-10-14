package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// syntheticKey is a fake credential used only to prove it never leaks.
const syntheticKey = "sk-synthetic-not-a-real-key-0123456789"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Round synthetic prices, USD per million tokens.
var testPricing = Pricing{InputPerMTok: 1, OutputPerMTok: 5, CacheReadPerMTok: 0.1, CacheWritePerMTok: 1.25}

type anthropicStub struct {
	mu      sync.Mutex
	reqs    []map[string]any
	headers []http.Header
	reply   func(n int) (int, string)
}

func (a *anthropicStub) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	a.mu.Lock()
	a.reqs = append(a.reqs, parsed)
	a.headers = append(a.headers, r.Header.Clone())
	n := len(a.reqs)
	a.mu.Unlock()
	if r.URL.Path != "/v1/messages" {
		w.WriteHeader(404)
		return
	}
	status, out := 200, fmt.Sprintf(`{"model":"claude-haiku-5-5","content":[{"type":"text","text":%q}],"usage":{"input_tokens":1000,"output_tokens":200,"cache_creation_input_tokens":400,"cache_read_input_tokens":8000}}`, goodJSON)
	if a.reply != nil {
		status, out = a.reply(n)
	}
	w.WriteHeader(status)
	fmt.Fprint(w, out)
}

func newAnthropic(t *testing.T, stub *anthropicStub, key string) (*AnthropicGateway, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(srv.Close)
	g, err := NewAnthropicGateway(srv.URL, "SYNTHETIC_KEY_VAR", "", testPricing, AnthropicOptions{}, env(map[string]string{"SYNTHETIC_KEY_VAR": key}))
	if err != nil {
		t.Fatal(err)
	}
	return g, srv
}

func TestAnthropicRequestShapeUsageAndCost(t *testing.T) {
	stub := &anthropicStub{}
	g, _ := newAnthropic(t, stub, syntheticKey)
	resp, err := g.Chat(context.Background(), Request{Model: "claude-haiku-5-5", System: "sys", User: "usr", MaxOutputTokens: 321})
	if err != nil {
		t.Fatal(err)
	}
	h := stub.headers[0]
	if h.Get("x-api-key") != syntheticKey || h.Get("anthropic-version") != "2023-06-01" || h.Get("content-type") != "application/json" {
		t.Fatalf("headers %v", h)
	}
	req := stub.reqs[0]
	msgs := req["messages"].([]any)
	if req["model"] != "claude-haiku-5-5" || req["system"] != "sys" || req["max_tokens"] != float64(321) || len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" || msgs[0].(map[string]any)["content"] != "usr" {
		t.Fatalf("body %v", req)
	}
	// Haiku 5.5 thinks by default and thinking counts toward max_tokens, so the
	// default request disables it; sampling parameters are omitted (they error).
	if th, _ := req["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Fatalf("thinking not disabled: %v", req["thinking"])
	}
	for _, k := range []string{"temperature", "top_p", "top_k", "output_config"} {
		if _, ok := req[k]; ok {
			t.Fatalf("%s must not be sent by default", k)
		}
	}
	if !strings.Contains(resp.Text, `"title"`) || resp.InputTokens != 1000 || resp.OutputTokens != 200 || resp.CacheReadTokens != 8000 || resp.CacheWriteTokens != 400 {
		t.Fatalf("usage %+v", resp)
	}
	// cost = tokens x configured price per MTok: 1000*1 + 200*5 + 8000*0.1 + 400*1.25 = 3300 per million
	want := 3300.0 / 1e6
	if math.Abs(resp.CostUSD-want) > 1e-12 {
		t.Fatalf("cost = %v want %v", resp.CostUSD, want)
	}
}

func TestAnthropicFailsClosedWithoutKeyPricesOrHTTPS(t *testing.T) {
	good := map[string]string{"K": syntheticKey}
	if _, err := NewAnthropicGateway("", "", "", testPricing, AnthropicOptions{}, env(good)); err == nil {
		t.Fatal("an unset api_key_env must be refused")
	}
	_, err := NewAnthropicGateway("", "K_MISSING", "", testPricing, AnthropicOptions{}, env(good))
	if err == nil || !strings.Contains(err.Error(), "K_MISSING") || strings.Contains(err.Error(), syntheticKey) {
		t.Fatalf("missing key error should name the variable only: %v", err)
	}
	if _, err := NewAnthropicGateway("", "K", "", Pricing{}, AnthropicOptions{}, env(good)); err == nil || !strings.Contains(err.Error(), "pricing") {
		t.Fatalf("missing prices must be refused: %v", err)
	}
	if _, err := NewAnthropicGateway("", "K", "", Pricing{InputPerMTok: 1}, AnthropicOptions{}, env(good)); err == nil {
		t.Fatal("an output price is required too")
	}
	for _, bad := range []string{"http://api.example.invalid", "ftp://x", "://", "http://192.0.2.1:80"} {
		if _, err := NewAnthropicGateway(bad, "K", "", testPricing, AnthropicOptions{}, env(good)); err == nil {
			t.Fatalf("%q must be refused: transcript text must not travel in the clear", bad)
		}
	}
	if _, err := NewAnthropicGateway("https://api.anthropic.com", "K", "", testPricing, AnthropicOptions{}, env(good)); err != nil {
		t.Fatalf("the default endpoint must be accepted: %v", err)
	}
}

func TestAnthropicErrorsAreClassifiedAndNeverLeakBodiesOrKeys(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		class     string
		retryable bool
	}{
		{401, `{"type":"error","error":{"type":"authentication_error","message":"bad key sk-synthetic-not-a-real-key-0123456789"}}`, "unauthorized", false},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, "http_429:rate_limit_error", true},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, "http_529:overloaded_error", true},
		{500, `not json at all, includes the prompt text synthetic-prompt`, "http_500", true},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"echoes synthetic-prompt"}}`, "http_400:invalid_request_error", false},
		{400, `{"type":"error","error":{"type":"Not Safe/Chars!","message":"x"}}`, "http_400", false},
	}
	for _, c := range cases {
		stub := &anthropicStub{reply: func(int) (int, string) { return c.status, c.body }}
		g, _ := newAnthropic(t, stub, syntheticKey)
		_, err := g.Chat(context.Background(), Request{Model: "m", System: "s", User: "synthetic-prompt", MaxOutputTokens: 10})
		var ge *GatewayError
		if !errors.As(err, &ge) || ge.Class != c.class || ge.Retryable != c.retryable {
			t.Fatalf("%d: %v", c.status, err)
		}
		if strings.Contains(err.Error(), syntheticKey) || strings.Contains(err.Error(), "synthetic-prompt") || strings.Contains(err.Error(), "echoes") {
			t.Fatalf("error leaks key or body: %v", err)
		}
	}
	// A transport failure is retryable and also leaks nothing.
	g, srv := newAnthropic(t, &anthropicStub{}, syntheticKey)
	srv.Close()
	_, err := g.Chat(context.Background(), Request{Model: "m", System: "s", User: "u", MaxOutputTokens: 10})
	var ge *GatewayError
	if !errors.As(err, &ge) || ge.Class != "unreachable" || strings.Contains(err.Error(), syntheticKey) {
		t.Fatalf("transport error: %v", err)
	}
}

// The ledger and caps are driven by the configured prices: with $0.0033 per
// call (3300 per million tokens), a $0.005 total cap allows exactly two calls.
func TestAnthropicSpendCapsAreComputedFromConfiguredPrices(t *testing.T) {
	st := openStore(t)
	for i := 0; i < 4; i++ {
		addSession(t, st, fmt.Sprintf("S%d", i), old, fmt.Sprintf("question %d", i), "answer")
	}
	stub := &anthropicStub{}
	g, _ := newAnthropic(t, stub, syntheticKey)
	o := opts()
	o.Provider, o.AllowedProviders, o.Model = AnthropicDirectProvider, []string{AnthropicDirectProvider}, "claude-haiku-5-5"
	o.TotalCapUSD = 0.005
	s, err := (&Runner{Store: st, GW: g, Opts: o}).Run(context.Background())
	if err != nil || s.StoppedBy != "total_cap" || len(stub.reqs) != 2 || s.OK != 2 {
		t.Fatalf("%+v %v calls=%d", s, err, len(stub.reqs))
	}
	if math.Abs(s.CostUSD-2*0.0033) > 1e-9 {
		t.Fatalf("cost %v", s.CostUSD)
	}
	tot, err := st.Spend(context.Background(), "")
	if err != nil || tot.Calls != 2 || tot.TokensIn != 2000 || tot.TokensOut != 400 || math.Abs(tot.CostUSD-0.0066) > 1e-9 {
		t.Fatalf("ledger %+v %v", tot, err)
	}
	// Cache tokens are recorded separately in the ledger.
	var cr, cw int
	if err := rawDB(t, st).QueryRow(`SELECT SUM(tokens_cache_read), SUM(tokens_cache_write) FROM tx_enrichment_spend`).Scan(&cr, &cw); err != nil || cr != 16000 || cw != 800 {
		t.Fatalf("cache tokens %d %d %v", cr, cw, err)
	}
	// The key is nowhere in what was stored.
	for _, tbl := range []string{"tx_enrichment", "tx_enrichment_spend"} {
		rows, _ := rawDB(t, st).Query(`SELECT * FROM ` + tbl)
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			_ = rows.Scan(ptrs...)
			for _, v := range vals {
				if strings.Contains(fmt.Sprint(v), syntheticKey) {
					t.Fatalf("%s stores the key", tbl)
				}
			}
		}
		rows.Close()
	}
}

func TestDryRunPrintsAnUpperBoundFromConfiguredPrices(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "S1", old, "a question", "an answer")
	o := opts()
	o.DryRun, o.EstPricing = true, testPricing
	s, err := (&Runner{Store: st, Opts: o}).Run(context.Background())
	want := testPricing.Cost(s.EstInputTokens*17/10, s.MaxOutputToks, 0, 0) // one call here; includes the tokenizer allowance
	if err != nil || s.Calls != 1 || s.EstMaxCostUSD <= 0 || math.Abs(s.EstMaxCostUSD-want) > 1e-12 {
		t.Fatalf("%+v %v", s, err)
	}
	o.EstPricing = Pricing{}
	if s, _ = (&Runner{Store: st, Opts: o}).Run(context.Background()); s.EstMaxCostUSD != 0 {
		t.Fatal("no prices configured, so no dollar figure may be printed")
	}
}

// --- local OpenAI-compatible embedder ---

func embedServer(t *testing.T, dim int, hook func(r map[string]any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			w.WriteHeader(404)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if hook != nil {
			hook(req)
		}
		inputs := req["input"].([]any)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		// Reply out of order, as a server may.
		for i := len(inputs) - 1; i >= 0; i-- {
			v := make([]float32, dim)
			v[i%dim] = 1
			data = append(data, item{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenAICompatEmbedderBatchesOrdersAndFormatsQueries(t *testing.T) {
	var mu sync.Mutex
	var seen []map[string]any
	srv := embedServer(t, 4, func(r map[string]any) { mu.Lock(); seen = append(seen, r); mu.Unlock() })
	e, err := NewOpenAICompatEmbedder(srv.URL, 4, "Find the matching conversation summary", 3)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"a", "b", "c", "d", "e"}
	vecs, err := e.Embed(context.Background(), "qwen3-embedding-0.6b", texts)
	if err != nil || len(vecs) != 5 {
		t.Fatalf("%v %d", err, len(vecs))
	}
	if len(seen) != 2 || len(seen[0]["input"].([]any)) != 3 || seen[0]["model"] != "qwen3-embedding-0.6b" {
		t.Fatalf("batching: %v", seen)
	}
	for i, v := range vecs {
		if v[i%3] != 1 { // position within its own batch
			t.Fatalf("vector %d is out of order: %v", i, v)
		}
	}
	// Documents are plain; queries carry the instruction.
	if got := seen[0]["input"].([]any)[0]; got != "a" {
		t.Fatalf("document was altered: %v", got)
	}
	if _, err := e.EmbedQuery(context.Background(), "qwen3-embedding-0.6b", "how did we speed up queries"); err != nil {
		t.Fatal(err)
	}
	q := seen[2]["input"].([]any)[0].(string)
	if q != "Instruct: Find the matching conversation summary\nQuery: how did we speed up queries" {
		t.Fatalf("query format: %q", q)
	}
	if e.Format() != "doc-plain/v1" {
		t.Fatal("format must be recorded with vectors")
	}
}

func TestOpenAICompatEmbedderRefusesNonLocalAndBadReplies(t *testing.T) {
	for _, bad := range []string{"https://api.openai.com", "http://192.0.2.9:8183", "http://embeddings.example.invalid:8183", ""} {
		if _, err := NewOpenAICompatEmbedder(bad, 0, "", 0); err == nil {
			t.Fatalf("%q must be refused: embeddings stay on this machine", bad)
		}
	}
	srv := embedServer(t, 4, nil)
	e, _ := NewOpenAICompatEmbedder(srv.URL, 1024, "", 0)
	if _, err := e.Embed(context.Background(), "m", []string{"x"}); err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("a dimension mismatch must fail: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[]}]}`)
	}))
	defer bad.Close()
	e, _ = NewOpenAICompatEmbedder(bad.URL, 0, "", 0)
	if _, err := e.Embed(context.Background(), "m", []string{"x"}); err == nil {
		t.Fatal("an empty vector must fail")
	}
}

// A model or prompt-format change must be a clean reindex, never a mix.
func TestEmbeddingFormatOrModelChangeReindexes(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "A", old, "q", "a")
	gw := &fakeGW{reply: replyWith("Fix the roadmap parser", "Reworked the roadmap parser.", "roadmap")}
	if _, err := (&Runner{Store: st, GW: gw, Opts: opts()}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := embedServer(t, 4, nil)
	e, _ := NewOpenAICompatEmbedder(srv.URL, 4, "", 0)
	ctx := context.Background()
	if s, err := EmbedSummaries(ctx, st, e, "model-a", 0); err != nil || s.Embedded != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	if s, _ := EmbedSummaries(ctx, st, e, "model-a", 0); s.Embedded != 0 || s.Skipped != 1 {
		t.Fatalf("unchanged: %+v", s)
	}
	if s, _ := EmbedSummaries(ctx, st, e, "model-b", 0); s.Embedded != 1 {
		t.Fatalf("a new model must embed again: %+v", s)
	}
	var dim int
	var format string
	if err := rawDB(t, st).QueryRow(`SELECT dim, format FROM tx_embeddings WHERE model = 'model-a'`).Scan(&dim, &format); err != nil || dim != 4 || format != "doc-plain/v1" {
		t.Fatalf("recorded %d %q %v", dim, format, err)
	}
	// Search never crosses formats: the same model with another format finds nothing.
	vec, _ := EmbedQuery(ctx, e, "model-a", "roadmap")
	if hits, _ := st.NearestSummaries(ctx, "model-a", "doc-plain/v1", vec, 3); len(hits) != 1 {
		t.Fatalf("hits %v", hits)
	}
	if hits, _ := st.NearestSummaries(ctx, "model-a", "doc-other/v9", vec, 3); len(hits) != 0 {
		t.Fatal("vectors of another format must not be compared")
	}
}

func TestAnthropicThinkingAndEffortOptions(t *testing.T) {
	stub := &anthropicStub{}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	mk := func(o AnthropicOptions) (*AnthropicGateway, error) {
		return NewAnthropicGateway(srv.URL, "K", "", testPricing, o, env(map[string]string{"K": syntheticKey}))
	}
	g, err := mk(AnthropicOptions{Thinking: "adaptive", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Chat(context.Background(), Request{Model: "m", System: "s", User: "u", MaxOutputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	req := stub.reqs[0]
	if _, ok := req["thinking"]; ok {
		t.Fatal("adaptive thinking is the default and is requested by omitting the field")
	}
	if oc, _ := req["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("effort not sent: %v", req["output_config"])
	}
	if _, err := mk(AnthropicOptions{Effort: "max"}); err == nil {
		t.Fatal("effort max with disabled thinking is a 400 on the API and must be refused up front")
	}
	if _, err := mk(AnthropicOptions{Thinking: "adaptive", Effort: "max"}); err != nil {
		t.Fatalf("max with adaptive thinking is valid: %v", err)
	}
	for _, bad := range []AnthropicOptions{{Thinking: "extended"}, {Effort: "turbo"}} {
		if _, err := mk(bad); err == nil {
			t.Fatalf("%+v must be refused", bad)
		}
	}
}

// Claude Haiku 5.5 prices a request by its prompt length: input plus cache
// reads and writes over the threshold pays the higher tier, per request.
func TestPricingTiersByPromptLength(t *testing.T) {
	p := Pricing{InputPerMTok: 0.10, OutputPerMTok: 0.50, CacheReadPerMTok: 0.01, CacheWritePerMTok: 0.125, LongThreshold: 100000,
		Long: &Pricing{InputPerMTok: 0.50, OutputPerMTok: 2.50, CacheReadPerMTok: 0.05, CacheWritePerMTok: 0.625}}
	if !p.Configured() {
		t.Fatal("a complete two-tier price list is configured")
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
	// Exactly at the threshold is still the base tier.
	if got, want := p.Cost(60000, 1000, 40000, 0), (60000*0.10+1000*0.50+40000*0.01)/1e6; !near(got, want) {
		t.Fatalf("at threshold: %v want %v", got, want)
	}
	// One token over (cache reads count toward the prompt) switches the whole request.
	if got, want := p.Cost(60000, 1000, 40001, 0), (60000*0.50+1000*2.50+40001*0.05)/1e6; !near(got, want) {
		t.Fatalf("over threshold: %v want %v", got, want)
	}
	if got, want := p.Cost(90000, 0, 0, 10001), (90000*0.50+10001*0.625)/1e6; !near(got, want) {
		t.Fatalf("cache writes count toward prompt length too: %v want %v", got, want)
	}
	if (Pricing{InputPerMTok: 1, OutputPerMTok: 1, LongThreshold: 100}).Configured() {
		t.Fatal("a threshold without a long tier is a misconfiguration and must not count as configured")
	}
	if _, err := NewAnthropicGateway("", "K", "", Pricing{InputPerMTok: 1, OutputPerMTok: 1, LongThreshold: 100}, AnthropicOptions{}, env(map[string]string{"K": syntheticKey})); err == nil {
		t.Fatal("the gateway must refuse an incomplete tier table")
	}
}

// Prompt-length tiers apply per request, so a plan made of many modest calls
// must not be priced as one giant prompt.
func TestDryRunBoundPricesEachCallOnItsOwnTier(t *testing.T) {
	st := openStore(t)
	for i := 0; i < 5; i++ {
		addSession(t, st, fmt.Sprintf("S%d", i), old, strings.Repeat("word ", 4000), "ok")
	}
	tiered := Pricing{InputPerMTok: 1, OutputPerMTok: 1, LongThreshold: 8000, Long: &Pricing{InputPerMTok: 1000, OutputPerMTok: 1000}}
	o := opts()
	o.DryRun, o.EstPricing = true, tiered
	s, err := (&Runner{Store: st, Opts: o}).Run(context.Background())
	if err != nil || s.Calls != 5 {
		t.Fatalf("%+v %v", s, err)
	}
	per := s.EstInputTokens / s.Calls
	if per*17/10 > tiered.LongThreshold {
		t.Fatalf("fixture too big (%d tokens per call)", per)
	}
	want := float64(s.Calls) * tiered.Cost(per*17/10, o.MaxOutputTokens, 0, 0)
	if want > 1 || math.Abs(s.EstMaxCostUSD-want) > 0.05 {
		t.Fatalf("bound %v want about %v (each call is under the threshold)", s.EstMaxCostUSD, want)
	}
}
