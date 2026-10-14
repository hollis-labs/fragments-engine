package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

const goodJSON = `{"schema_version":1,"title":"Synthetic roadmap review","summary":"The person reviewed a synthetic roadmap. Two items were decided. One follow-up remains.","session_type":"planning","outcome":"completed","topics":["Roadmap Review","planning","planning"],"decisions":[{"text":"Ship the synthetic plan first","turns":[0,1,99]}],"follow_ups":[{"text":"Write the synthetic doc","turns":[1]}],"entities":{"projects":["synthetic-proj","not-mentioned-anywhere"],"repos":[],"tools":["Read"],"technologies":["go"]},"language":"en"}`

type fakeGW struct {
	mu    sync.Mutex
	calls []Request
	reply func(n int, r Request) (Response, error)
}

func (f *fakeGW) Chat(_ context.Context, r Request) (Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r)
	n := len(f.calls)
	f.mu.Unlock()
	if f.reply != nil {
		return f.reply(n, r)
	}
	return Response{Text: goodJSON, Provider: r.Provider, Model: r.Model, InputTokens: 100, OutputTokens: 50, CostUSD: 0.01}, nil
}
func (f *fakeGW) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func openStore(t *testing.T) *transcript.Store {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := transcript.Open(filepath.Join(dir, "private"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var old = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// addSession stores a main session with the given turn texts.
func addSession(t *testing.T, st *transcript.Store, id string, ended time.Time, texts ...string) {
	t.Helper()
	d := &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: id, Kind: "main", Model: "synthetic-model",
		StartedAt: old, EndedAt: ended, CWD: "/synthetic/synthetic-proj"}}
	for i, text := range texts {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		d.Turns = append(d.Turns, domain.TranscriptTurn{EventUUID: fmt.Sprintf("%s-e%d", id, i), Role: role, Text: text, TS: old.Add(time.Duration(i) * time.Minute)})
	}
	d.Tools = []domain.TranscriptToolCall{{ToolUseID: id + "-t1", Name: "Read", InputJSON: `{"file_path":"/synthetic/synthetic-proj/a.go"}`, StartedAt: old, HasResult: true, ResultSize: 5, EndedAt: old}}
	d.Usage = []domain.TranscriptUsage{{RequestID: id + "-r1", Input: 10, Output: 20, TS: old}}
	frag := domain.PipelineFragment{Source: "claude", SourceType: "chat", SourceID: id, Title: "Claude session: " + id, Metadata: map[string]any{"session_id": id}, Detail: d}
	if _, err := st.Accept(context.Background(), "reg", frag); err != nil {
		t.Fatal(err)
	}
}

func opts() Options {
	return Options{Provider: "anthropic-work", Model: "synthetic-model-a", AllowedProviders: []string{"anthropic-work"}, RunID: "run-test",
		Now: func() time.Time { return old.Add(24 * time.Hour) }, Backoff: func(int) time.Duration { return 0 }}
}

func TestParseValidatesCapsAndGrounding(t *testing.T) {
	valid := map[int]bool{0: true, 1: true}
	src := "we talked about synthetic-proj and go"
	r, q, err := Parse("```json\n"+goodJSON+"\n```", valid, src)
	if err != nil {
		t.Fatal(err)
	}
	if r.Title == "" || len(r.Topics) != 2 || r.Topics[0] != "roadmap-review" {
		t.Fatalf("topics not normalised: %v", r.Topics)
	}
	if q.UngroundedCites != 1 || len(r.Decisions[0].Turns) != 2 {
		t.Fatalf("citation 99 should be dropped and counted: %+v %v", q, r.Decisions)
	}
	if q.EntitiesChecked != 2 || q.UngroundedEntities != 1 {
		t.Fatalf("entity grounding: %+v", q)
	}
	for name, raw := range map[string]string{
		"not_json":         "sorry, here is a summary",
		"trailing_output":  goodJSON + " extra",
		"schema":           strings.Replace(goodJSON, `"language":"en"`, `"language":"en","surprise":1`, 1),
		"enum":             strings.Replace(goodJSON, `"planning","outcome"`, `"nonsense","outcome"`, 1),
		"missing_required": strings.Replace(goodJSON, `"summary":"The person reviewed a synthetic roadmap. Two items were decided. One follow-up remains."`, `"summary":""`, 1),
		"schema_type":      strings.Replace(goodJSON, `"topics":["Roadmap Review","planning","planning"]`, `"topics":"x"`, 1),
	} {
		_, _, err := Parse(raw, valid, src)
		var pe *ParseError
		if !errors.As(err, &pe) || pe.Class != name {
			t.Fatalf("%s: got %v", name, err)
		}
		if strings.Contains(err.Error(), "sorry") {
			t.Fatal("error leaks model output")
		}
	}
	long := strings.Replace(goodJSON, "Synthetic roadmap review", strings.Repeat("x", 200), 1)
	r, q, err = Parse(long, valid, src)
	if err != nil || len([]rune(r.Title)) != 80 || q.Truncated == 0 {
		t.Fatalf("title cap: %v %d", err, len([]rune(r.Title)))
	}
	echo := strings.Replace(goodJSON, "Two items were decided.", "token=synthetic-echoed-secret-value", 1)
	r, _, err = Parse(echo, valid, src)
	if err != nil || strings.Contains(r.Summary, "synthetic-echoed-secret-value") {
		t.Fatalf("model-echoed secret survived: %q", r.Summary)
	}
}

func TestDryRunMakesNoCallsAndPlansCounts(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "S1", old, "synthetic question", "synthetic answer")
	addSession(t, st, "S2", old, "another", "reply")
	big := make([]string, 40)
	for i := range big {
		big[i] = strings.Repeat("word ", 1000) // 5k chars each, below the per-turn trim
	}
	addSession(t, st, "S3", old, big...) // ~50k tokens in total
	gw := &fakeGW{}
	o := opts()
	o.DryRun = true
	o.AllowedProviders = nil // a dry run needs no allow-list
	o.SingleCallTokens = 10000
	sum, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gw.count() != 0 {
		t.Fatal("dry run called the model")
	}
	if sum.Sessions != 3 || sum.Pending != 2 || sum.NeedsMapReduce != 1 || sum.Calls != 2 || sum.EstInputTokens <= 0 || sum.MaxOutputToks != 2*1500 {
		t.Fatalf("plan %+v", sum.Plan)
	}
	// And it persisted nothing.
	if _, ok, _ := st.LatestEnrichment(context.Background(), sessionPK(t, st, "S1"), 0); ok {
		t.Fatal("dry run wrote tier-0 rows")
	}
}

func sessionPK(t *testing.T, st *transcript.Store, id string) string {
	t.Helper()
	refs, err := st.MainSessions(context.Background(), 0, time.Time{}, id)
	if err != nil || len(refs) != 1 {
		t.Fatalf("session %s: %v %d", id, err, len(refs))
	}
	return refs[0].SessionPK
}

func TestRunRefusesUnlistedProviderBeforeAnyCall(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "S1", old, "q", "a")
	gw := &fakeGW{}
	o := opts()
	o.AllowedProviders = nil
	if _, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background()); err == nil || gw.count() != 0 {
		t.Fatalf("empty allow-list must refuse: %v calls=%d", err, gw.count())
	}
	o.AllowedProviders = []string{"openai-work"}
	if _, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background()); err == nil || gw.count() != 0 {
		t.Fatal("provider not in list must refuse")
	}
}

func TestRunStoresResultsAndIsIdempotent(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "S1", old, "synthetic-proj question password=synthetic-input-secret", "synthetic answer")
	addSession(t, st, "S2", old, "second question", "second answer")
	gw := &fakeGW{}
	run := func(o Options) Summary {
		s, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := run(opts())
	if s.OK != 2 || s.Failed != 0 || gw.count() != 2 || s.TokensIn != 200 || s.TokensOut != 100 || s.CostUSD < 0.0199 || s.FactsSaved != 2 {
		t.Fatalf("first run %+v calls=%d", s, gw.count())
	}
	// The prompt carries redacted text only, and frames the transcript as data.
	for _, c := range gw.calls {
		if strings.Contains(c.User, "synthetic-input-secret") {
			t.Fatal("unredacted secret reached the model")
		}
		if c.CallerID != CallerID || !strings.HasPrefix(c.RequestID, "run-test/") || !strings.HasPrefix(c.SessionID, "fe-enrich/") || c.Model != "synthetic-model-a" {
			t.Fatalf("attribution fields: %+v", c)
		}
		if !strings.Contains(c.System, "DATA") || !strings.Contains(c.User, "<transcript>") {
			t.Fatal("transcript not framed as data")
		}
	}
	pk := sessionPK(t, st, "S1")
	rec, ok, err := st.LatestEnrichment(context.Background(), pk, 1)
	if err != nil || !ok || rec.Status != "ok" || rec.TokensIn != 100 || rec.Calls != 1 || rec.PromptVersion != PromptVersion {
		t.Fatalf("stored row %+v %v %v", rec, ok, err)
	}
	var res Result
	if json.Unmarshal([]byte(rec.ResultJSON), &res) != nil || res.Title == "" {
		t.Fatal("stored result is not the contract")
	}
	facts, ok, _ := st.LatestEnrichment(context.Background(), pk, 0)
	var f transcript.SessionFacts
	if !ok || json.Unmarshal([]byte(facts.ResultJSON), &f) != nil || f.UserTurns != 1 || f.AssistantTurns != 1 || f.ToolCalls != 1 || f.TokensIn != 10 || len(f.FilesTouched) != 1 {
		t.Fatalf("tier-0 facts %+v", f)
	}
	// Re-run: nothing pending, no calls.
	s = run(opts())
	if s.AlreadyDone != 2 || s.Pending != 0 || gw.count() != 2 || s.FactsSaved != 0 {
		t.Fatalf("second run %+v calls=%d", s, gw.count())
	}
	// The session grew: only that one is pending again.
	addSession(t, st, "S1", old, "synthetic-proj question password=synthetic-input-secret", "synthetic answer", "a follow-up", "more")
	s = run(opts())
	if s.Pending != 1 || gw.count() != 3 {
		t.Fatalf("growth should re-enrich one session: %+v calls=%d", s, gw.count())
	}
	// A prompt-version bump or a different model makes everything pending.
	o := opts()
	o.PromptVersion = "enrich-v2"
	if s = run(o); s.Pending != 2 {
		t.Fatalf("prompt bump: %+v", s)
	}
	o = opts()
	o.Model = "synthetic-model-b"
	if s = run(o); s.Pending != 2 {
		t.Fatalf("model change: %+v", s)
	}
	// --force repeats work for the same key.
	o = opts()
	o.Force = true
	before := gw.count()
	if run(o); gw.count() != before+2 {
		t.Fatal("force should re-run")
	}
}

func TestInvalidOutputRetriesOnceThenFailsAndIsRetriedNextRun(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "S1", old, "q", "a")
	gw := &fakeGW{reply: func(n int, r Request) (Response, error) {
		if n == 1 {
			return Response{Text: "not json at all password=synthetic-model-output", InputTokens: 10, OutputTokens: 5}, nil
		}
		return Response{Text: goodJSON, InputTokens: 10, OutputTokens: 5}, nil
	}}
	s, err := (&Runner{Store: st, GW: gw, Opts: opts()}).Run(context.Background())
	if err != nil || s.OK != 1 || s.Retries != 1 || gw.count() != 2 || !strings.Contains(gw.calls[1].User, "rejected") {
		t.Fatalf("retry path %+v %v calls=%d", s, err, gw.count())
	}
	rec, _, _ := st.LatestEnrichment(context.Background(), sessionPK(t, st, "S1"), 1)
	if rec.Calls != 2 || rec.TokensIn != 20 {
		t.Fatalf("tokens must cover both attempts: %+v", rec)
	}
	// Two bad replies: failed, with a class and no model output stored.
	st2 := openStore(t)
	addSession(t, st2, "S1", old, "q", "a")
	bad := &fakeGW{reply: func(int, Request) (Response, error) { return Response{Text: "nope", InputTokens: 1}, nil }}
	s, err = (&Runner{Store: st2, GW: bad, Opts: opts()}).Run(context.Background())
	if err != nil || s.Failed != 1 || s.Errors["parse_not_json"] != 1 || bad.count() != 2 {
		t.Fatalf("failure path %+v %v", s, err)
	}
	if _, ok, _ := st2.LatestEnrichment(context.Background(), sessionPK(t, st2, "S1"), 1); ok {
		t.Fatal("a failed row must not count as the latest ok result")
	}
	good := &fakeGW{}
	if s, _ = (&Runner{Store: st2, GW: good, Opts: opts()}).Run(context.Background()); s.OK != 1 {
		t.Fatalf("failed sessions must be retried: %+v", s)
	}
}

func TestGatewayErrorsRetryOrAbort(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "S1", old, "q", "a")
	addSession(t, st, "S2", old, "q2", "a2")
	flaky := &fakeGW{reply: func(n int, r Request) (Response, error) {
		if n == 1 {
			return Response{}, &GatewayError{Class: "http_429", Retryable: true}
		}
		return Response{Text: goodJSON, InputTokens: 1, OutputTokens: 1}, nil
	}}
	s, err := (&Runner{Store: st, GW: flaky, Opts: opts()}).Run(context.Background())
	if err != nil || s.OK != 2 {
		t.Fatalf("429 should be retried: %+v %v", s, err)
	}
	st2 := openStore(t)
	addSession(t, st2, "S1", old, "q", "a")
	addSession(t, st2, "S2", old, "q2", "a2")
	down := &fakeGW{reply: func(int, Request) (Response, error) { return Response{}, &GatewayError{Class: "unauthorized"} }}
	s, err = (&Runner{Store: st2, GW: down, Opts: opts()}).Run(context.Background())
	var ge *GatewayError
	if !errors.As(err, &ge) || s.StoppedBy != "gateway_unauthorized" || down.count() != 1 {
		t.Fatalf("auth failure should stop the run: %v %+v calls=%d", err, s, down.count())
	}
}

func TestLimitBudgetAndRecentSessions(t *testing.T) {
	st := openStore(t)
	for i := 0; i < 5; i++ {
		addSession(t, st, fmt.Sprintf("S%d", i), old, fmt.Sprintf("q%d", i), "a")
	}
	gw := &fakeGW{}
	o := opts()
	o.Limit = 2
	s, _ := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background())
	if s.OK != 2 || gw.count() != 2 || s.Pending != 2 {
		t.Fatalf("limit: %+v calls=%d", s, gw.count())
	}
	gw2 := &fakeGW{reply: func(int, Request) (Response, error) {
		return Response{Text: goodJSON, InputTokens: 1, OutputTokens: 1, CostUSD: 0.03}, nil
	}}
	st2 := openStore(t)
	for i := 0; i < 5; i++ {
		addSession(t, st2, fmt.Sprintf("S%d", i), old, fmt.Sprintf("q%d", i), "a")
	}
	o = opts()
	o.MaxCostUSD = 0.05
	s, _ = (&Runner{Store: st2, GW: gw2, Opts: o}).Run(context.Background())
	if s.StoppedBy != "budget" || gw2.count() != 2 {
		t.Fatalf("budget should stop after crossing 0.05: %+v calls=%d", s, gw2.count())
	}
	// A session written in the last MinIdle is left alone.
	st3 := openStore(t)
	now := old.Add(24 * time.Hour)
	addSession(t, st3, "FRESH", now.Add(-5*time.Minute), "q", "a")
	addSession(t, st3, "IDLE", now.Add(-2*time.Hour), "q", "a")
	o = opts()
	o.DryRun = true
	o.MinIdle = 30 * time.Minute
	s, _ = (&Runner{Store: st3, Opts: o}).Run(context.Background())
	if s.Sessions != 1 {
		t.Fatalf("min idle: %+v", s.Plan)
	}
}

func TestLongTurnsAreTrimmedAndFramed(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "S1", old, strings.Repeat("x", 20000), "short reply")
	refs, _ := st.MainSessions(context.Background(), 0, time.Time{}, "S1")
	in, err := st.EnrichmentInput(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	p := Build(in)
	if p.TrimmedTurns != 1 || !strings.Contains(p.User, "characters omitted") || len(p.User) > 12000 {
		t.Fatalf("trim: %d %d", p.TrimmedTurns, len(p.User))
	}
	if !p.ValidSeqs[0] || !p.ValidSeqs[1] {
		t.Fatal("turn numbers missing")
	}
}

func TestHTTPGatewayShapeAndErrors(t *testing.T) {
	var got map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		switch r.Header.Get("X-Test") {
		}
		if strings.Contains(r.URL.RawQuery, "limit") {
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{"response":{"provider":"anthropic-work","model":"m","output":[{"role":"assistant","parts":[{"type":"text","text":"hello"}]}],"usage":{"input_tokens":7,"output_tokens":3,"estimated_cost_usd":0.002}}}`)
	}))
	defer srv.Close()
	gw, err := NewHTTPGateway(strings.Replace(srv.URL, "127.0.0.1", "127.0.0.1", 1), "tth_synthetic")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := gw.Chat(context.Background(), Request{Provider: "anthropic-work", Model: "m", System: "sys", User: "usr", MaxOutputTokens: 9, CallerID: "c", RequestID: "r", SessionID: "s"})
	if err != nil || resp.Text != "hello" || resp.InputTokens != 7 || resp.OutputTokens != 3 || resp.CostUSD != 0.002 {
		t.Fatalf("response %+v %v", resp, err)
	}
	req := got["request"].(map[string]any)
	if path != "/ai/chat" || auth != "Bearer tth_synthetic" || req["provider_hint"] != "anthropic-work" || req["model_hint"] != "m" || req["caller_id"] != "c" || req["request_id"] != "r" || req["session_id"] != "s" || req["operation"] != "chat" {
		t.Fatalf("request envelope %v %s %s", got, path, auth)
	}
	if len(req["input"].([]any)) != 2 {
		t.Fatal("system and user messages expected")
	}
	for _, bad := range []string{"https://example.invalid", "http://192.0.2.1:80", "tcp://127.0.0.1:1", "unix:"} {
		if _, err := NewHTTPGateway(bad, ""); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestNeverSendSwitchAndCumulativeCap(t *testing.T) {
	st := openStore(t)
	addSession(t, st, "KEEP", old, "q", "a")
	addSession(t, st, "SECRET-ID", old, "q", "a")
	d := &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "PRIVATE-PROJ", Kind: "main", EndedAt: old, CWD: "/synthetic/private-area/app"},
		Turns: []domain.TranscriptTurn{{EventUUID: "p0", Role: "user", Text: "q", TS: old}}}
	if _, err := st.Accept(context.Background(), "reg", domain.PipelineFragment{Source: "claude", SourceType: "chat", SourceID: "PRIVATE-PROJ", Title: "t", Detail: d}); err != nil {
		t.Fatal(err)
	}
	gw := &fakeGW{}
	o := opts()
	o.ExcludeSessions = []string{"SECRET-ID"}
	o.ExcludeCWDPrefixes = []string{"/synthetic/private-area"}
	s, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background())
	if err != nil || s.Excluded != 2 || s.OK != 1 || gw.count() != 1 {
		t.Fatalf("exclusions %+v %v calls=%d", s, err, gw.count())
	}
	if strings.Contains(gw.calls[0].User, "SECRET-ID") {
		t.Fatal("excluded session reached the model")
	}
	// The ledger holds one row per call with tokens and gateway-observed cost.
	tot, err := st.Spend(context.Background(), "")
	if err != nil || tot.Calls != 1 || tot.TokensIn != 100 || tot.CostUSD < 0.0099 {
		t.Fatalf("ledger %+v %v", tot, err)
	}
	// A later run sees prior spend: with a 0.015 total cap and 0.01 already
	// spent, exactly one more call is allowed.
	st2 := openStore(t)
	for i := 0; i < 4; i++ {
		addSession(t, st2, fmt.Sprintf("S%d", i), old, fmt.Sprintf("q%d", i), "a")
	}
	gw2 := &fakeGW{}
	o = opts()
	o.TotalCapUSD = 0.015
	o.Limit = 1
	if s, _ = (&Runner{Store: st2, GW: gw2, Opts: o}).Run(context.Background()); s.OK != 1 {
		t.Fatalf("first: %+v", s)
	}
	o.Limit = 0
	o.RunID = "run-two"
	s, _ = (&Runner{Store: st2, GW: gw2, Opts: o}).Run(context.Background())
	if s.StoppedBy != "total_cap" || s.PriorCostUSD < 0.0099 || gw2.count() != 2 {
		t.Fatalf("cumulative cap: %+v calls=%d", s, gw2.count())
	}
	if all, _ := st2.Spend(context.Background(), ""); all.Calls != 2 {
		t.Fatalf("ledger rows across runs: %+v", all)
	}
}

func TestCodexTextRecordsAreEnrichedFromStoredText(t *testing.T) {
	st := openStore(t)
	frag := domain.PipelineFragment{Source: "codex", SourceType: "transcript", SourceID: "thread-1", Title: "Codex session: thread-1",
		Content:  "## user\n\nplan the synthetic refactor\n\n## assistant\n\nhere is the plan\n\nwith a second paragraph\n\n## user\n\nship it",
		Metadata: map[string]any{"cwd": "/synthetic/proj", "thread_id": "thread-1"}, CreatedAt: old}
	if _, err := st.Accept(context.Background(), "reg", frag); err != nil {
		t.Fatal(err)
	}
	gw := &fakeGW{}
	s, err := (&Runner{Store: st, GW: gw, Opts: opts()}).Run(context.Background())
	if err != nil || s.OK != 1 || gw.count() != 1 {
		t.Fatalf("codex run %+v %v", s, err)
	}
	u := gw.calls[0].User
	for _, want := range []string{"[0] user: plan the synthetic refactor", "[1] assistant: here is the plan\n\nwith a second paragraph", "[2] user: ship it", `"tool":"codex"`, `"user_turns":2`} {
		if !strings.Contains(u, want) {
			t.Fatalf("prompt missing %q:\n%s", want, u)
		}
	}
}
