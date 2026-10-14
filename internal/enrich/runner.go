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
	NeedsMapReduce int // too large for one call; not sent in this phase
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
		if p.EstTokens > o.SingleCallTokens {
			sum.NeedsMapReduce++
			continue
		}
		sum.Pending++
		sum.Calls++
		sum.EstInputTokens += p.EstTokens
		sum.TrimmedTurns += p.TrimmedTurns
		pending = append(pending, work{in, p})
	}
	if o.Limit > 0 && len(pending) > o.Limit {
		pending = pending[:o.Limit]
		sum.Pending, sum.Calls = o.Limit, o.Limit
		sum.EstInputTokens = 0
		for _, w := range pending {
			sum.EstInputTokens += w.prompt.EstTokens
		}
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
	cost                         float64
}

func (r *Runner) enrichOne(ctx context.Context, o Options, w work) outcome {
	var out outcome
	pk12 := w.in.Ref.SessionPK
	if len(pk12) > 12 {
		pk12 = pk12[:12]
	}
	prefix := o.RunID + "/" + pk12
	user := w.prompt.User
	var q Quality
	var lastClass string
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := r.callWithBackoff(ctx, o, Request{
			Provider: o.Provider, Model: o.Model, System: w.prompt.System, User: user, MaxOutputTokens: o.MaxOutputTokens,
			CallerID: CallerID, RequestID: fmt.Sprintf("%s/1/%d", prefix, attempt), SessionID: "fe-enrich/" + pk12,
		})
		if err != nil {
			var ge *GatewayError
			class := "gateway"
			if errors.As(err, &ge) {
				class = ge.Class
			}
			if errors.As(err, &ge) && (ge.Class == "unauthorized" || ge.Class == "unreachable") || ctx.Err() != nil {
				out.fatal, out.class = err, class
				return out
			}
			out.class = "gateway_" + class
			r.record(ctx, o, w, prefix, "failed", out.class, "", q, out)
			return out
		}
		out.tokensIn += resp.InputTokens
		out.tokensOut += resp.OutputTokens
		out.cost += resp.CostUSD
		// The ledger is written before the output is judged: a rejected reply
		// still cost tokens.
		_ = r.Store.AppendSpend(ctx, transcript.SpendRecord{RunID: o.RunID, SessionPK: w.in.Ref.SessionPK,
			RequestID: fmt.Sprintf("%s/1/%d", prefix, attempt), Provider: o.Provider, Model: o.Model,
			TokensIn: resp.InputTokens, TokensOut: resp.OutputTokens, CostUSD: resp.CostUSD})
		res, quality, perr := Parse(resp.Text, w.prompt.ValidSeqs, w.prompt.SourceLowered)
		quality.Retries = attempt
		quality.ResponseModel = resp.Model
		if perr == nil {
			out.retries = attempt
			raw, _ := json.Marshal(res)
			qraw, _ := json.Marshal(quality)
			rec := transcript.EnrichmentRecord{SessionPK: w.in.Ref.SessionPK, Tier: 1, PromptVersion: o.PromptVersion, Model: o.Model,
				Provider: o.Provider, ContentDigest: w.in.ContentDigest, Status: "ok", ResultJSON: string(raw), QualityJSON: string(qraw),
				TokensIn: out.tokensIn, TokensOut: out.tokensOut, Calls: attempt + 1, Chunks: 1, RunID: o.RunID, RequestPrefix: prefix}
			if err := r.Store.SaveEnrichment(ctx, rec); err != nil {
				out.class = "store"
			}
			return out
		}
		var pe *ParseError
		lastClass = "parse"
		if errors.As(perr, &pe) {
			lastClass = "parse_" + pe.Class
		}
		q = quality
		user = w.prompt.User + "\n\nYour previous reply was rejected (" + lastClass + "). Reply with only the JSON object described in the instructions."
		out.retries = attempt + 1
	}
	out.class = lastClass
	r.record(ctx, o, w, prefix, "failed", lastClass, "", q, out)
	return out
}

// record stores a failure so the harness can report it; it is retried on the
// next run because only ok rows count as done.
func (r *Runner) record(ctx context.Context, o Options, w work, prefix, status, class, result string, q Quality, out outcome) {
	qraw, _ := json.Marshal(q)
	_ = r.Store.SaveEnrichment(ctx, transcript.EnrichmentRecord{SessionPK: w.in.Ref.SessionPK, Tier: 1, PromptVersion: o.PromptVersion,
		Model: o.Model, Provider: o.Provider, ContentDigest: w.in.ContentDigest, Status: status, ErrorClass: class, ResultJSON: result,
		QualityJSON: string(qraw), TokensIn: out.tokensIn, TokensOut: out.tokensOut, Calls: out.retries + 1, Chunks: 1, RunID: o.RunID, RequestPrefix: prefix})
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
