package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// Candidate describes one session the harness could enrich, from Tier 0 facts
// and the prompt plan only. It holds no transcript text.
type Candidate struct {
	NativeSessionID, Tool string
	EstTokens, Calls      int
	MapReduce             bool
	ToolCalls, ToolErrors int
	Subagents             int
}

// SampleItem is a Candidate chosen for a bucket.
type SampleItem struct {
	Bucket string
	Candidate
}

type bucketSpec struct {
	name  string
	count int
	match func(c Candidate) bool
}

// Buckets in the order they are filled; a session belongs to the first bucket
// that takes it, so the sample covers distinct shapes.
func sampleBuckets() []bucketSpec {
	return []bucketSpec{
		{"map-reduce", 2, func(c Candidate) bool { return c.Tool == "claude" && c.MapReduce }},
		{"error-heavy", 1, func(c Candidate) bool {
			return c.Tool == "claude" && !c.MapReduce && c.ToolCalls >= 20 && c.ToolErrors*5 >= c.ToolCalls
		}},
		{"tool-heavy", 2, func(c Candidate) bool { return c.Tool == "claude" && !c.MapReduce && c.ToolCalls > 200 }},
		{"subagent-heavy", 2, func(c Candidate) bool { return c.Tool == "claude" && !c.MapReduce && c.Subagents >= 5 }},
		{"long", 3, func(c Candidate) bool { return c.Tool == "claude" && !c.MapReduce && c.EstTokens >= 20000 }},
		{"medium", 4, func(c Candidate) bool { return c.Tool == "claude" && c.EstTokens >= 2000 && c.EstTokens < 8000 }},
		{"short", 3, func(c Candidate) bool { return c.Tool == "claude" && c.EstTokens < 1000 }},
		{"codex-long", 1, func(c Candidate) bool { return c.Tool == "codex" && c.EstTokens >= 8000 }},
		{"codex-medium", 1, func(c Candidate) bool { return c.Tool == "codex" && c.EstTokens >= 1500 && c.EstTokens < 8000 }},
		{"codex-short", 1, func(c Candidate) bool { return c.Tool == "codex" && c.EstTokens < 1000 }},
	}
}

// Candidates lists in-scope sessions that are not excluded, with their plan.
// It writes nothing and calls no model.
func (r *Runner) Candidates(ctx context.Context) ([]Candidate, error) {
	o := r.Opts
	o.normalize()
	refs, err := r.sessions(ctx, o)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, ref := range refs {
		in, err := r.Store.EnrichmentInput(ctx, ref)
		if err != nil {
			return nil, err
		}
		if excluded(o, in) {
			continue
		}
		p := Build(in)
		c := Candidate{NativeSessionID: in.Facts.NativeSessionID, Tool: in.Facts.Tool, EstTokens: p.EstTokens, Calls: 1,
			ToolCalls: in.Facts.ToolCalls, ToolErrors: in.Facts.ToolErrors, Subagents: in.Facts.Subagents}
		if p.EstTokens > o.SingleCallTokens {
			c.MapReduce = true
			c.Calls = len(BuildChunks(in, o.ChunkTokens, o.ChunkOverlapTurns)) + 1
		}
		out = append(out, c)
	}
	return out, nil
}

// PickSample chooses a reproducible sample: within each bucket, candidates are
// ordered by a hash of their id so the choice is stable and unbiased.
func PickSample(cands []Candidate) []SampleItem {
	taken := map[string]bool{}
	var out []SampleItem
	for _, b := range sampleBuckets() {
		var pool []Candidate
		for _, c := range cands {
			if !taken[c.NativeSessionID] && b.match(c) {
				pool = append(pool, c)
			}
		}
		sort.Slice(pool, func(i, j int) bool { return idHash(pool[i].NativeSessionID) < idHash(pool[j].NativeSessionID) })
		for i := 0; i < b.count && i < len(pool); i++ {
			taken[pool[i].NativeSessionID] = true
			out = append(out, SampleItem{Bucket: b.name, Candidate: pool[i]})
		}
	}
	return out
}

func idHash(id string) string {
	sum := sha256.Sum256([]byte("fe-enrich-sample/" + id))
	return hex.EncodeToString(sum[:])
}
