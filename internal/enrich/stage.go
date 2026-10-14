package enrich

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Stage selection. A staged run picks sessions by tool and size bucket, in a
// reproducible order, from what is still pending (already-enriched sessions are
// excluded before selection). The same logic serves every stage: the stratified
// pilot, the rest of one tool, newest first, and so on.

// SizeBucket names the size stratum of a session from its estimated prompt
// tokens (chars/4 plus the fixed prompt overhead, as Build computes it).
func SizeBucket(estTokens int) string {
	switch {
	case estTokens < 1000:
		return "<1k"
	case estTokens < 8000:
		return "1-8k"
	case estTokens < 30000:
		return "8-30k"
	case estTokens < 60000:
		return "30-60k"
	default:
		return ">60k"
	}
}

// StratumKey is "tool/bucket", for example "codex/8-30k".
func StratumKey(tool string, estTokens int) string { return tool + "/" + SizeBucket(estTokens) }

// Stratum reports how many pending sessions a stratum had and how many a stage
// selected from it.
type Stratum struct {
	Key                 string
	Available, Selected int
}

// sessionTime is the session's last activity, or its start when only that is
// known. Unparseable or missing times sort oldest.
func sessionTime(endedAt, startedAt string) time.Time {
	for _, s := range []string{endedAt, startedAt} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// selectWork applies, in order: the allocation (per stratum, hash-ordered or
// newest-first inside each), then the overall order, then the limit. It returns
// the selection and a per-stratum report.
func selectWork(o Options, ws []work) ([]work, []Stratum) {
	groups := map[string][]work{}
	for _, w := range ws {
		groups[w.stratum] = append(groups[w.stratum], w)
	}
	newest := o.Order == "newest"
	less := func(a, b work) bool {
		if newest && !a.when.Equal(b.when) {
			return a.when.After(b.when)
		}
		return idHash(a.nid) < idHash(b.nid)
	}
	var keys []string
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var report []Stratum
	var selected []work
	for _, k := range keys {
		g := groups[k]
		sort.SliceStable(g, func(i, j int) bool { return less(g[i], g[j]) })
		take := len(g)
		if len(o.Allocation) > 0 {
			take = o.Allocation[k]
			if take > len(g) {
				take = len(g)
			}
		}
		report = append(report, Stratum{Key: k, Available: len(g), Selected: take})
		selected = append(selected, g[:take]...)
	}
	// Strata named in the allocation that have nothing pending are reported too.
	for k, n := range o.Allocation {
		if _, ok := groups[k]; !ok && n > 0 {
			report = append(report, Stratum{Key: k})
		}
	}
	sort.Slice(report, func(i, j int) bool { return report[i].Key < report[j].Key })
	if newest {
		sort.SliceStable(selected, func(i, j int) bool { return less(selected[i], selected[j]) })
	}
	if o.Limit > 0 && len(selected) > o.Limit {
		selected = selected[:o.Limit]
	}
	return selected, report
}

// Gate thresholds agreed for staged runs.
const (
	gateFirstTryValid   = 0.90
	gateMaxFailureRate  = 0.05
	gateCitationGround  = 0.95
	gateEntityGround    = 0.90
	gateMaxSpendVsEstim = 1.20
)

// Gate is one stage check. Counts and ratios only.
type Gate struct {
	Name      string
	Value     float64
	Threshold float64
	AtMost    bool // the value must be <= Threshold (otherwise >=)
	Pass      bool
	NA        bool // nothing to measure (for example no citations)
}

// EvaluateGates scores a finished stage run against the agreed gates:
// first-try JSON-valid rate, failure rate, citation grounding, project/repo
// entity grounding, and spend against the stage's expected cost.
func EvaluateGates(s Summary) []Gate {
	ratio := func(num, den int) (float64, bool) {
		if den == 0 {
			return 0, false
		}
		return float64(num) / float64(den), true
	}
	var gates []Gate
	add := func(name string, v float64, ok bool, thr float64, atMost bool) {
		g := Gate{Name: name, Value: v, Threshold: thr, AtMost: atMost, NA: !ok}
		switch {
		case !ok:
			g.Pass = true
		case atMost:
			g.Pass = v <= thr
		default:
			g.Pass = v >= thr
		}
		gates = append(gates, g)
	}
	v, ok := ratio(s.FirstTryValid, s.Attempted)
	add("first_try_valid_json", v, ok, gateFirstTryValid, false)
	v, ok = ratio(s.Failed, s.Attempted)
	add("failure_rate", v, ok, gateMaxFailureRate, true)
	v, ok = ratio(s.Cites-s.UngroundedCites, s.Cites)
	add("citation_grounding", v, ok, gateCitationGround, false)
	v, ok = ratio(s.EntitiesChecked-s.UngroundedEntities, s.EntitiesChecked)
	add("entity_grounding", v, ok, gateEntityGround, false)
	if s.EstExpectedCostUSD > 0 {
		add("spend_vs_estimate", s.CostUSD/s.EstExpectedCostUSD, true, gateMaxSpendVsEstim, true)
	} else {
		add("spend_vs_estimate", 0, false, gateMaxSpendVsEstim, true)
	}
	return gates
}

// AllGatesPass is true when every measurable gate passed and the run was not
// halted by a stop rule.
func AllGatesPass(s Summary, gates []Gate) bool {
	if s.StoppedBy != "" {
		return false
	}
	for _, g := range gates {
		if !g.Pass {
			return false
		}
	}
	return true
}

// ValidateAllocation checks stage allocation keys: claude or codex, a slash, and
// a known size bucket. A typo would otherwise silently select nothing.
func ValidateAllocation(a map[string]int) error {
	buckets := map[string]bool{"<1k": true, "1-8k": true, "8-30k": true, "30-60k": true, ">60k": true}
	for k, n := range a {
		tool, bucket, ok := strings.Cut(k, "/")
		if !ok || (tool != "claude" && tool != "codex") || !buckets[bucket] {
			return fmt.Errorf("invalid allocation key %q: use claude or codex, a slash, and a bucket (<1k, 1-8k, 8-30k, 30-60k, >60k)", k)
		}
		if n < 0 {
			return fmt.Errorf("allocation for %q must not be negative", k)
		}
	}
	return nil
}
