package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

const (
	// CallerID labels every gateway call so Tether can attribute usage.
	CallerID = "fragments-engine/transcript-enrichment"
	// defaultSingleCallTokens is the largest session sent in one call; bigger
	// ones need the map-reduce path.
	defaultSingleCallTokens = 60000
	factsPromptVersion      = "facts-v1"
)

// Options configure a run. Zero values are filled by Normalize.
type Options struct {
	DryRun          bool
	Limit           int
	NativeSessionID string
	Provider, Model string
	Force           bool
	PromptVersion   string
	MaxCostUSD      float64 // per run
	TotalCapUSD     float64 // across all runs, from the spend ledger; a hard stop
	// ExcludeSessions and ExcludeCWDPrefixes are the never-send switch: matching
	// sessions are never given to a model.
	ExcludeSessions    []string
	ExcludeCWDPrefixes []string
	MinIdle            time.Duration
	MaxOutputTokens    int
	AllowedProviders   []string
	Concurrency        int
	RunID              string
	SingleCallTokens   int
	ChunkTokens        int
	ChunkOverlapTurns  int
	Now                func() time.Time
	Backoff            func(attempt int) time.Duration
}

func (o *Options) normalize() {
	if o.PromptVersion == "" {
		o.PromptVersion = PromptVersion
	}
	if o.MaxOutputTokens <= 0 {
		o.MaxOutputTokens = 1500
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 1
	}
	if o.SingleCallTokens <= 0 {
		o.SingleCallTokens = defaultSingleCallTokens
	}
	if o.ChunkTokens <= 0 {
		o.ChunkTokens = 40000
	}
	if o.ChunkOverlapTurns <= 0 {
		o.ChunkOverlapTurns = 2
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Backoff == nil {
		o.Backoff = func(a int) time.Duration { return time.Duration(a) * time.Second }
	}
	if o.RunID == "" {
		o.RunID = "run-" + o.Now().UTC().Format("20060102T150405Z")
	}
}

// Plan is what a dry run reports: counts only, from deterministic arithmetic.
type Plan struct {
	Sessions       int // eligible main sessions
	AlreadyDone    int
	Pending        int // sessions that would be sent
	MapReduce      int // sessions too large for one call; sent as chunks plus a reduce
	Excluded       int // never-send switch
	Calls          int
	EstInputTokens int // characters/4: an estimate, not a tokenizer count
	MaxOutputToks  int
	TrimmedTurns   int
}

// Summary is a run's outcome. Cost is what the gateway reported, summed; FE
// stores tokens only.
type Summary struct {
	Plan
	Attempted, OK, Failed, Retries int
	TokensIn, TokensOut            int
	CostUSD                        float64
	PriorCostUSD                   float64 // already spent by earlier runs
	Errors                         map[string]int
	StoppedBy                      string
	FactsSaved                     int
}

type Runner struct {
	Store *transcript.Store
	GW    Gateway
	Opts  Options
}

type work struct {
	in     transcript.EnrichmentInput
	prompt Prompt
	chunks []Prompt // map prompts when the session is too large for one call
}

// Run plans, and unless DryRun, executes Tier 0 refresh and Tier 1 calls.
func (r *Runner) Run(ctx context.Context) (Summary, error) {
	o := r.Opts
	o.normalize()
	sum := Summary{Errors: map[string]int{}}
	if !o.DryRun {
		if o.Provider == "" || o.Model == "" {
			return sum, errors.New("enrichment: provider and model are required")
		}
		if !contains(o.AllowedProviders, o.Provider) {
			return sum, fmt.Errorf("enrichment: provider %q is not in enrichment.allowed_providers; nothing is sent", o.Provider)
		}
		if r.GW == nil {
			return sum, errors.New("enrichment: no gateway configured")
		}
	}
	refs, err := r.Store.MainSessions(ctx, o.MinIdle, o.Now(), o.NativeSessionID)
	if err != nil {
		return sum, err
	}
	legacy, err := r.Store.LegacySessions(ctx, o.NativeSessionID)
	if err != nil {
		return sum, err
	}
	refs = append(refs, legacy...)
	prior, err := r.Store.Spend(ctx, "")
	if err != nil {
		return sum, err
	}
	sum.PriorCostUSD = prior.CostUSD
	sum.Sessions = len(refs)
	var pending []work
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		in, err := r.Store.EnrichmentInput(ctx, ref)
		if err != nil {
			return sum, err
		}
		if excluded(o, in) {
			sum.Excluded++
			continue
		}
		if !o.DryRun {
			saved, err := r.saveFacts(ctx, in)
			if err != nil {
				return sum, err
			}
			if saved {
				sum.FactsSaved++
			}
		}
		if !o.Force && o.Model != "" {
			done, err := r.Store.EnrichmentDone(ctx, ref.SessionPK, 1, o.PromptVersion, o.Model, in.ContentDigest)
			if err != nil {
				return sum, err
			}
			if done {
				sum.AlreadyDone++
				continue
			}
		}
		p := Build(in)
		wk := work{in: in, prompt: p}
		sum.Pending++
		sum.TrimmedTurns += p.TrimmedTurns
		if p.EstTokens > o.SingleCallTokens {
			wk.chunks = BuildChunks(in, o.ChunkTokens, o.ChunkOverlapTurns)
			sum.MapReduce++
		}
		pending = append(pending, wk)
	}
	if o.Limit > 0 && len(pending) > o.Limit {
		pending = pending[:o.Limit]
		sum.Pending = o.Limit
	}
	// Calls and token estimates follow what will actually be sent.
	sum.Calls, sum.EstInputTokens, sum.MapReduce = 0, 0, 0
	for _, w := range pending {
		if len(w.chunks) == 0 {
			sum.Calls++
			sum.EstInputTokens += w.prompt.EstTokens
			continue
		}
		sum.MapReduce++
		sum.Calls += len(w.chunks) + 1
		for _, c := range w.chunks {
			sum.EstInputTokens += c.EstTokens
		}
		sum.EstInputTokens += len(w.chunks) * (o.MaxOutputTokens / 2) // reduce reads the partials
	}
	sum.MaxOutputToks = sum.Calls * o.MaxOutputTokens
	if o.DryRun {
		return sum, nil
	}
	return r.execute(ctx, o, pending, sum)
}

func (r *Runner) saveFacts(ctx context.Context, in transcript.EnrichmentInput) (bool, error) {
	raw, err := json.Marshal(in.Facts)
	if err != nil {
		return false, err
	}
	prev, ok, err := r.Store.LatestEnrichment(ctx, in.Ref.SessionPK, 0)
	if err != nil {
		return false, err
	}
	// The store re-serialises JSON with sorted keys, so compare normalised forms.
	if ok && sameJSON(prev.ResultJSON, string(raw)) {
		return false, nil
	}
	rec := transcript.EnrichmentRecord{SessionPK: in.Ref.SessionPK, Tier: 0, PromptVersion: factsPromptVersion, ContentDigest: "",
		Status: "ok", ResultJSON: string(raw), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	// A changed fact set replaces the single tier-0 row for the session.
	return true, r.Store.SaveEnrichment(ctx, rec)
}

func (r *Runner) execute(ctx context.Context, o Options, pending []work, sum Summary) (Summary, error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, o.Concurrency)
	var fatal error
	stop := func(reason string) {
		if sum.StoppedBy == "" {
			sum.StoppedBy = reason
		}
	}
	for _, w := range pending {
		// Take a worker slot first: a finished call may have changed the budget
		// or hit a fatal error while this loop was waiting for it.
		sem <- struct{}{}
		mu.Lock()
		halted := sum.StoppedBy != "" || fatal != nil
		over := o.MaxCostUSD > 0 && sum.CostUSD >= o.MaxCostUSD
		if over {
			stop("budget")
		}
		if !over && o.TotalCapUSD > 0 && sum.PriorCostUSD+sum.CostUSD >= o.TotalCapUSD {
			over = true
			stop("total_cap")
		}
		mu.Unlock()
		if halted || over || ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func(w work) {
			defer wg.Done()
			defer func() { <-sem }()
			res := r.enrichOne(ctx, o, w)
			mu.Lock()
			defer mu.Unlock()
			sum.Attempted++
			sum.Retries += res.retries
			sum.TokensIn += res.tokensIn
			sum.TokensOut += res.tokensOut
			sum.CostUSD += res.cost
			switch {
			case res.fatal != nil:
				fatal = res.fatal
				stop("gateway_" + res.class)
			case res.class != "":
				sum.Failed++
				sum.Errors[res.class]++
			default:
				sum.OK++
			}
		}(w)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return sum, err
	}
	return sum, fatal
}

type outcome struct {
	class                        string
	fatal                        error
	retries, tokensIn, tokensOut int
	calls                        int
	cost                         float64
}

// ask makes one logical model call: up to two attempts (the second names the
// rejection class), gateway retries with backoff, and a spend-ledger row for
// every reply received, whether or not it validates. It returns "" on success.
func (r *Runner) ask(ctx context.Context, o Options, w work, prefix, tag, system, user string, validate func(text string) error, acct *outcome) (string, error) {
	var lastClass string
	for attempt := 0; attempt < 2; attempt++ {
		reqID := fmt.Sprintf("%s/%s/%d", prefix, tag, attempt)
		resp, err := r.callWithBackoff(ctx, o, Request{
			Provider: o.Provider, Model: o.Model, System: system, User: user, MaxOutputTokens: o.MaxOutputTokens,
			CallerID: CallerID, RequestID: reqID, SessionID: "fe-enrich/" + shortPK(w.in.Ref.SessionPK),
		})
		if err != nil {
			var ge *GatewayError
			class := "gateway"
			if errors.As(err, &ge) {
				class = ge.Class
			}
			if ctx.Err() != nil || (errors.As(err, &ge) && (ge.Class == "unauthorized" || ge.Class == "unreachable")) {
				return class, err
			}
			return "gateway_" + class, nil
		}
		acct.calls++
		acct.tokensIn += resp.InputTokens
		acct.tokensOut += resp.OutputTokens
		acct.cost += resp.CostUSD
		// Written before the reply is judged: a rejected reply still cost tokens.
		_ = r.Store.AppendSpend(ctx, transcript.SpendRecord{RunID: o.RunID, SessionPK: w.in.Ref.SessionPK, RequestID: reqID,
			Provider: o.Provider, Model: o.Model, TokensIn: resp.InputTokens, TokensOut: resp.OutputTokens, CostUSD: resp.CostUSD})
		verr := validate(resp.Text)
		if verr == nil {
			return "", nil
		}
		lastClass = "parse"
		var pe *ParseError
		if errors.As(verr, &pe) {
			lastClass = "parse_" + pe.Class
		}
		acct.retries++
		user = user + "\n\nYour previous reply was rejected (" + lastClass + "). Reply with only the JSON object described in the instructions."
	}
	return lastClass, nil
}

func shortPK(pk string) string {
	if len(pk) > 12 {
		return pk[:12]
	}
	return pk
}

func (r *Runner) enrichOne(ctx context.Context, o Options, w work) outcome {
	var out outcome
	prefix := o.RunID + "/" + shortPK(w.in.Ref.SessionPK)
	var res Result
	var q Quality
	chunks := 1
	if len(w.chunks) == 0 {
		class, fatal := r.ask(ctx, o, w, prefix, "1", w.prompt.System, w.prompt.User, func(text string) error {
			var err error
			res, q, err = Parse(text, w.prompt.ValidSeqs, w.prompt.SourceLowered)
			return err
		}, &out)
		if fatal != nil || class != "" {
			return r.finish(ctx, o, w, prefix, out, q, 1, class, fatal)
		}
	} else {
		chunks = len(w.chunks)
		var partials []Partial
		for i, c := range w.chunks {
			var part Partial
			class, fatal := r.ask(ctx, o, w, prefix, fmt.Sprintf("m%d", i), c.System, c.User, func(text string) error {
				var err error
				part, err = ParsePartial(text, w.prompt.ValidSeqs)
				return err
			}, &out)
			if fatal != nil || class != "" {
				if class != "" && fatal == nil {
					class = "map_" + class
				}
				return r.finish(ctx, o, w, prefix, out, q, chunks, class, fatal)
			}
			partials = append(partials, part)
		}
		red := BuildReduce(w.in, partials)
		class, fatal := r.ask(ctx, o, w, prefix, "r", red.System, red.User, func(text string) error {
			var err error
			res, q, err = Parse(text, w.prompt.ValidSeqs, w.prompt.SourceLowered)
			return err
		}, &out)
		if fatal != nil || class != "" {
			if class != "" && fatal == nil {
				class = "reduce_" + class
			}
			return r.finish(ctx, o, w, prefix, out, q, chunks, class, fatal)
		}
	}
	q.Retries = out.retries
	raw, _ := json.Marshal(res)
	qraw, _ := json.Marshal(q)
	rec := transcript.EnrichmentRecord{SessionPK: w.in.Ref.SessionPK, Tier: 1, PromptVersion: o.PromptVersion, Model: o.Model,
		Provider: o.Provider, ContentDigest: w.in.ContentDigest, Status: "ok", ResultJSON: string(raw), QualityJSON: string(qraw),
		TokensIn: out.tokensIn, TokensOut: out.tokensOut, Calls: out.calls, Chunks: chunks, RunID: o.RunID, RequestPrefix: prefix}
	if err := r.Store.SaveEnrichment(ctx, rec); err != nil {
		out.class = "store"
	}
	return out
}

// finish closes a session that did not produce a result. Auth and reachability
// failures are fatal for the whole run and write nothing; anything else is
// recorded as a failed row (retried next run, since only ok rows count as done).
func (r *Runner) finish(ctx context.Context, o Options, w work, prefix string, out outcome, q Quality, chunks int, class string, fatal error) outcome {
	out.class = class
	if fatal != nil {
		out.fatal = fatal
		return out
	}
	qraw, _ := json.Marshal(q)
	_ = r.Store.SaveEnrichment(ctx, transcript.EnrichmentRecord{SessionPK: w.in.Ref.SessionPK, Tier: 1, PromptVersion: o.PromptVersion,
		Model: o.Model, Provider: o.Provider, ContentDigest: w.in.ContentDigest, Status: "failed", ErrorClass: class,
		QualityJSON: string(qraw), TokensIn: out.tokensIn, TokensOut: out.tokensOut, Calls: out.calls, Chunks: chunks, RunID: o.RunID, RequestPrefix: prefix})
	return out
}

func (r *Runner) callWithBackoff(ctx context.Context, o Options, req Request) (Response, error) {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		var resp Response
		resp, err = r.GW.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		var ge *GatewayError
		if !errors.As(err, &ge) || !ge.Retryable || attempt == 3 {
			return Response{}, err
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(o.Backoff(attempt)):
		}
	}
	return Response{}, err
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	ra, _ := json.Marshal(x)
	rb, _ := json.Marshal(y)
	return string(ra) == string(rb)
}

func excluded(o Options, in transcript.EnrichmentInput) bool {
	if contains(o.ExcludeSessions, in.Facts.NativeSessionID) {
		return true
	}
	for _, p := range o.ExcludeCWDPrefixes {
		if p != "" && strings.HasPrefix(in.Facts.CWD, p) {
			return true
		}
	}
	return false
}
