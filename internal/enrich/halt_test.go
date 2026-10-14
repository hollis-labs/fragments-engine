package enrich

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// holdWriteLock takes the reserved (write) lock on a test store's database from a
// second connection, as a writer in another process would: other writers wait,
// readers are unaffected. It returns a
// release function; the lock is also released at test end.
func holdWriteLock(t *testing.T, st *transcript.Store) func() {
	t.Helper()
	p, ok := storePaths.Load(st)
	if !ok {
		t.Fatal("unknown store")
	}
	db, err := sql.Open("sqlite", p.(string))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			_ = conn.Close()
			_ = db.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func quickLocks(t *testing.T) {
	t.Helper()
	restore := transcript.SetLockWaits(20*time.Millisecond, 300*time.Millisecond)
	prevRow := failedRowDeadline
	failedRowDeadline = 150 * time.Millisecond
	t.Cleanup(func() { restore(); failedRowDeadline = prevRow; testHookAfterLedger = nil })
}

// Rule (a): the ledger row for a paid call is written before the result is
// saved. If that write ultimately fails, the run halts at once and stops paying;
// the result is dropped, only the session id is logged, the gap is reported, and
// the halt is not counted as a provider failure.
func TestLedgerWriteFailureHaltsTheRunAndStopsPaying(t *testing.T) {
	quickLocks(t)
	st := openStore(t)
	for i := 0; i < 5; i++ {
		addSession(t, st, fmt.Sprintf("S%d", i), old, fmt.Sprintf("q%d", i), "a")
	}
	var release func()
	// The hook runs only after a SUCCESSFUL ledger write. If the result were
	// validated or saved after a failed ledger write, it would run (or the save
	// would be attempted); rule (a) says neither may happen.
	hookCalls := 0
	testHookAfterLedger = func() { hookCalls++ }
	gw := &fakeGW{reply: func(n int, r Request) (Response, error) {
		if n == 1 {
			release = holdWriteLock(t, st) // from now on every write contends
		}
		return Response{Text: goodJSON, InputTokens: 100, OutputTokens: 50, CostUSD: 0.01}, nil
	}}
	o := opts()
	o.Concurrency = 1
	s, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background())
	var we *transcript.WriteError
	if !errors.As(err, &we) || !we.Busy {
		t.Fatalf("want a busy WriteError, got %v", err)
	}
	if gw.count() != 1 {
		t.Fatalf("a halted run must stop paying: %d calls", gw.count())
	}
	if s.StoppedBy != "store_write" || s.StoreWriteFailures != 1 || len(s.StoreFailedIDs) != 1 {
		t.Fatalf("summary %+v", s)
	}
	if s.Failed != 0 || len(s.Errors) != 0 {
		t.Fatalf("a store halt is not a provider failure: failed=%d errors=%v", s.Failed, s.Errors)
	}
	if s.CostUSD < 0.0099 || s.LedgerGapUSD < 0.0099 {
		t.Fatalf("the unrecorded spend must be reported: cost %v gap %v", s.CostUSD, s.LedgerGapUSD)
	}
	if hookCalls != 0 {
		t.Fatalf("the result was processed after a failed ledger write (%d)", hookCalls)
	}
	release()
	if tot, _ := st.Spend(context.Background(), ""); tot.Calls != 0 {
		t.Fatalf("the ledger write failed, so there is no row: %+v", tot)
	}
	if _, ok, _ := st.LatestEnrichment(context.Background(), sessionPK(t, st, s.StoreFailedIDs[0]), 1); ok {
		t.Fatal("no result may be stored when the ledger could not be written")
	}
}

// Rule (b): if the ledger row was written but the Tier-1 result cannot be saved,
// the session is marked failed-with-cost (best effort) and the run halts.
func TestResultSaveFailureAfterTheLedgerMarksFailedWithCostAndHalts(t *testing.T) {
	quickLocks(t)
	st := openStore(t)
	for i := 0; i < 4; i++ {
		addSession(t, st, fmt.Sprintf("S%d", i), old, fmt.Sprintf("q%d", i), "a")
	}
	var release func()
	// The lock appears right after the ledger row is written.
	testHookAfterLedger = func() {
		if release == nil {
			release = holdWriteLock(t, st)
		}
	}
	gw := &fakeGW{}
	o := opts()
	o.Concurrency = 1
	s, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background())
	var we *transcript.WriteError
	if !errors.As(err, &we) {
		t.Fatalf("want a WriteError, got %v", err)
	}
	if gw.count() != 1 || s.StoppedBy != "store_write" || s.StoreWriteFailures != 1 || s.Failed != 0 || len(s.Errors) != 0 {
		t.Fatalf("%+v calls=%d", s, gw.count())
	}
	if s.LedgerGapUSD != 0 {
		t.Fatalf("the ledger row exists, so there is no gap: %v", s.LedgerGapUSD)
	}
	release()
	tot, _ := st.Spend(context.Background(), "")
	if tot.Calls != 1 || tot.CostUSD < 0.0099 {
		t.Fatalf("the paid call must be in the ledger: %+v", tot)
	}
	if _, ok, _ := st.LatestEnrichment(context.Background(), sessionPK(t, st, s.StoreFailedIDs[0]), 1); ok {
		t.Fatal("no ok result was saved")
	}
}

// A persistent failure while refreshing Tier-0 facts stops the run before any call.
func TestTierZeroWriteFailureStopsBeforeAnyPaidCall(t *testing.T) {
	quickLocks(t)
	st := openStore(t)
	for i := 0; i < 3; i++ {
		addSession(t, st, fmt.Sprintf("S%d", i), old, fmt.Sprintf("q%d", i), "a")
	}
	holdWriteLock(t, st)
	gw := &fakeGW{}
	s, err := (&Runner{Store: st, GW: gw, Opts: opts()}).Run(context.Background())
	var we *transcript.WriteError
	if !errors.As(err, &we) || gw.count() != 0 || s.StoppedBy != "store_write" || s.Attempted != 0 {
		t.Fatalf("%v calls=%d %+v", err, gw.count(), s)
	}
}

// Tier-0 facts are refreshed only for the sessions a run will enrich.
func TestTierZeroIsRefreshedOnlyForSelectedSessions(t *testing.T) {
	st := openStore(t)
	seed(t, st)
	gw := &fakeGW{}
	o := opts()
	o.Allocation = map[string]int{"claude/<1k": 2, "codex/<1k": 1}
	s, err := (&Runner{Store: st, GW: gw, Opts: o}).Run(context.Background())
	if err != nil || s.OK != 3 || s.FactsSaved != 3 {
		t.Fatalf("%+v %v", s, err)
	}
	n := 0
	if err := rawDB(t, st).QueryRow(`SELECT COUNT(*) FROM tx_enrichment WHERE tier = 0`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("tier-0 rows = %d, want only the 3 selected sessions (%v)", n, err)
	}
	// A dry run writes nothing.
	d := dry(t, st, func(*Options) {})
	if d.FactsSaved != 0 {
		t.Fatalf("dry run saved facts: %+v", d)
	}
}
