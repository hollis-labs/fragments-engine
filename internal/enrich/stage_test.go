package enrich

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// words returns n words of filler, about 6n characters.
func words(n int) string { return strings.Repeat("alpha ", n) }

func addCodex(t *testing.T, st *transcript.Store, id string, created time.Time, body string) {
	t.Helper()
	frag := domain.PipelineFragment{Source: "codex", SourceType: "transcript", SourceID: id, Title: "Codex session: " + id,
		Content:  "## user\n\n" + body + "\n\n## assistant\n\nok",
		Metadata: map[string]any{"cwd": "/synthetic/proj", "thread_id": id}, CreatedAt: created}
	if _, err := st.Accept(context.Background(), "reg", frag); err != nil {
		t.Fatal(err)
	}
}

// seed makes a corpus with known strata: claude tiny x6, claude mid x5 (1-8k),
// codex tiny x4, codex mid x3, with distinct last-activity times.
func seed(t *testing.T, st *transcript.Store) {
	t.Helper()
	for i := 0; i < 6; i++ {
		addSession(t, st, fmt.Sprintf("ct%d", i), old.Add(time.Duration(i)*time.Hour), "q", "a")
	}
	for i := 0; i < 5; i++ {
		addSession(t, st, fmt.Sprintf("cm%d", i), old.Add(time.Duration(10+i)*time.Hour), words(2500), "a")
	}
	for i := 0; i < 4; i++ {
		addCodex(t, st, fmt.Sprintf("xt%d", i), old.Add(time.Duration(20+i)*time.Hour), "q")
	}
	for i := 0; i < 3; i++ {
		addCodex(t, st, fmt.Sprintf("xm%d", i), old.Add(time.Duration(30+i)*time.Hour), words(2500))
	}
}

func dry(t *testing.T, st *transcript.Store, mutate func(*Options)) Summary {
	t.Helper()
	o := opts()
	o.DryRun, o.AllowedProviders = true, nil
	mutate(&o)
	s, err := (&Runner{Store: st, Opts: o}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSizeBucketsAndStrataOfTheSeededCorpus(t *testing.T) {
	for est, want := range map[int]string{0: "<1k", 999: "<1k", 1000: "1-8k", 7999: "1-8k", 8000: "8-30k", 29999: "8-30k", 30000: "30-60k", 59999: "30-60k", 60000: ">60k"} {
		if got := SizeBucket(est); got != want {
			t.Fatalf("SizeBucket(%d) = %s want %s", est, got, want)
		}
	}
	st := openStore(t)
	seed(t, st)
	s := dry(t, st, func(*Options) {})
	got := map[string]int{}
	for _, x := range s.Strata {
		got[x.Key] = x.Available
		if x.Selected != x.Available {
			t.Fatalf("with no allocation everything pending is selected: %+v", x)
		}
	}
	want := map[string]int{"claude/<1k": 6, "claude/1-8k": 5, "codex/<1k": 4, "codex/1-8k": 3}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("strata %v want %v", got, want)
		}
	}
}

func TestAllocationIsPerStratumReproducibleAndExcludesDoneSessions(t *testing.T) {
	st := openStore(t)
	seed(t, st)
	alloc := map[string]int{"claude/<1k": 2, "claude/1-8k": 3, "codex/<1k": 1, "codex/8-30k": 5}
	plan := func() Summary { return dry(t, st, func(o *Options) { o.Allocation = alloc }) }
	a, b := plan(), plan()
	if a.Pending != 6 || strings.Join(a.SelectedIDs, ",") != strings.Join(b.SelectedIDs, ",") {
		t.Fatalf("selection must be reproducible: %v vs %v", a.SelectedIDs, b.SelectedIDs)
	}
	by := map[string][2]int{}
	for _, x := range a.Strata {
		by[x.Key] = [2]int{x.Available, x.Selected}
	}
	if by["claude/<1k"] != [2]int{6, 2} || by["claude/1-8k"] != [2]int{5, 3} || by["codex/<1k"] != [2]int{4, 1} {
		t.Fatalf("strata %v", by)
	}
	if by["codex/1-8k"][1] != 0 {
		t.Fatalf("a stratum that is not in the allocation must select nothing: %v", by)
	}
	if x, ok := by["codex/8-30k"]; !ok || x != [2]int{0, 0} {
		t.Fatalf("an allocated stratum with nothing pending is still reported: %v", by)
	}
	// Run the stage for real, then ask again: the done sessions are excluded
	// before selection, so the same allocation now picks other sessions.
	gw := &fakeGW{}
	o := opts()
	o.Allocation = alloc
	if s, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background()); err != nil || s.OK != 6 || gw.count() != 6 {
		t.Fatalf("stage run %+v %v", s, err)
	}
	c := plan()
	// 3 of the 5 mid-size Claude sessions are done, so only 2 are left to take.
	if c.Pending != 5 || c.AlreadyDone != 6 {
		t.Fatalf("second stage %+v", c.Plan)
	}
	first := map[string]bool{}
	for _, id := range a.SelectedIDs {
		first[id] = true
	}
	for _, id := range c.SelectedIDs {
		if first[id] {
			t.Fatalf("%s was enriched in the first stage and must not be selected again", id)
		}
	}
	// A third stage is capped by what is still pending in each stratum.
	d := dry(t, st, func(o *Options) {
		o.Allocation = map[string]int{"claude/<1k": 99, "codex/<1k": 99}
	})
	if d.Pending != 4+3 {
		t.Fatalf("capped by what is pending: %+v", d.Plan)
	}
}

func TestToolFilterOrderNewestAndLimit(t *testing.T) {
	st := openStore(t)
	seed(t, st)
	x := dry(t, st, func(o *Options) { o.Tool = "codex" })
	if x.Pending != 7 {
		t.Fatalf("codex only: %+v", x.Plan)
	}
	for _, id := range x.SelectedIDs {
		if !strings.HasPrefix(id, "x") {
			t.Fatalf("non-codex session %s selected", id)
		}
	}
	// Newest first: the codex sessions were created in ascending time, xm2 last.
	n := dry(t, st, func(o *Options) { o.Tool, o.Order, o.Limit = "codex", "newest", 3 })
	if strings.Join(n.SelectedIDs, ",") != "xm2,xm1,xm0" {
		t.Fatalf("newest first: %v", n.SelectedIDs)
	}
	cl := dry(t, st, func(o *Options) { o.Tool, o.Order, o.Limit = "claude", "newest", 2 })
	if strings.Join(cl.SelectedIDs, ",") != "cm4,cm3" {
		t.Fatalf("newest claude: %v", cl.SelectedIDs)
	}
	// Newest-first also orders inside each allocated stratum.
	both := dry(t, st, func(o *Options) {
		o.Tool, o.Order = "claude", "newest"
		o.Allocation = map[string]int{"claude/<1k": 2, "claude/1-8k": 1}
	})
	if strings.Join(both.SelectedIDs, ",") != "cm4,ct5,ct4" {
		t.Fatalf("allocation + newest: %v", both.SelectedIDs)
	}
}

func TestExpectedCostIsBelowTheUpperBound(t *testing.T) {
	st := openStore(t)
	seed(t, st)
	s := dry(t, st, func(o *Options) { o.EstPricing = testPricing })
	if s.EstExpectedCostUSD <= 0 || s.EstExpectedCostUSD >= s.EstMaxCostUSD {
		t.Fatalf("expected %v bound %v", s.EstExpectedCostUSD, s.EstMaxCostUSD)
	}
	if z := dry(t, st, func(*Options) {}); z.EstExpectedCostUSD != 0 {
		t.Fatal("no prices, no dollar figure")
	}
}

func TestGatesScoreAStage(t *testing.T) {
	good := Summary{Attempted: 100, OK: 98, Failed: 2, FirstTryValid: 93, Cites: 200, UngroundedCites: 4, EntitiesChecked: 50, UngroundedEntities: 4,
		CostUSD: 1.0, Plan: Plan{EstExpectedCostUSD: 1.1}}
	gates := EvaluateGates(good)
	for _, g := range gates {
		if !g.Pass || g.NA {
			t.Fatalf("expected all gates to pass: %+v", g)
		}
	}
	if !AllGatesPass(good, gates) {
		t.Fatal("a healthy stage passes")
	}
	byName := func(s Summary) map[string]Gate {
		m := map[string]Gate{}
		for _, g := range EvaluateGates(s) {
			m[g.Name] = g
		}
		return m
	}
	bad := good
	bad.FirstTryValid, bad.Failed, bad.UngroundedCites, bad.UngroundedEntities, bad.CostUSD = 80, 9, 20, 10, 1.4
	m := byName(bad)
	for _, name := range []string{"first_try_valid_json", "failure_rate", "citation_grounding", "entity_grounding", "spend_vs_estimate"} {
		if m[name].Pass {
			t.Fatalf("%s should fail: %+v", name, m[name])
		}
	}
	// Exactly on a threshold passes.
	edge := Summary{Attempted: 100, FirstTryValid: 90, Failed: 5, Cites: 100, UngroundedCites: 5, EntitiesChecked: 10, UngroundedEntities: 1, CostUSD: 1.2, Plan: Plan{EstExpectedCostUSD: 1.0}}
	for name, g := range byName(edge) {
		if !g.Pass {
			t.Fatalf("%s must pass on the threshold: %+v", name, g)
		}
	}
	// Nothing to measure is not a failure; a halted run never passes.
	empty := byName(Summary{Attempted: 3, FirstTryValid: 3})
	if !empty["citation_grounding"].NA || !empty["entity_grounding"].NA || !empty["spend_vs_estimate"].NA {
		t.Fatalf("not applicable gates: %+v", empty)
	}
	halted := good
	halted.StoppedBy = "budget"
	if AllGatesPass(halted, EvaluateGates(halted)) {
		t.Fatal("a run halted by a stop rule must not pass")
	}
}

func TestAllocationKeysAreValidated(t *testing.T) {
	if err := ValidateAllocation(map[string]int{"claude/<1k": 3, "codex/>60k": 1, "codex/8-30k": 0}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]int{
		{"claude/1k": 1}, {"gemini/<1k": 1}, {"claude": 1}, {"claude/<1k": -1}, {"Claude/<1k": 1}, {"/<1k": 1},
	} {
		if err := ValidateAllocation(bad); err == nil {
			t.Fatalf("%v must be refused", bad)
		}
	}
}
