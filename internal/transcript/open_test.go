package transcript

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// other opens a second, independent connection to the private database, the
// way a concurrent ingest, queue tick or inbox review would.
func other(t *testing.T, root string) *sql.Conn {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func freshRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(canonicalTempDir(t), "private")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// A writer holding an exclusive lock (a commit in progress) must delay Open,
// not fail it: the schema inspection is the first statement Open runs.
func TestOpenWaitsOutAnExclusiveLock(t *testing.T) {
	root := freshRoot(t)
	conn := other(t, root)
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		_, _ = conn.ExecContext(ctx, `COMMIT`)
	}()
	start := time.Now()
	st, err := Open(root)
	if err != nil {
		t.Fatalf("Open failed while another connection held the lock: %v", err)
	}
	defer st.Close()
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("Open did not wait for the lock; the test is not exercising it")
	}
}

// With the schema current, Open must not take a write lock at all: a writer
// that holds the reserved lock must not slow or fail it.
func TestOpenDoesNotWriteWhenSchemaIsCurrent(t *testing.T) {
	root := freshRoot(t)
	conn := other(t, root)
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	dbPath := filepath.Join(root, "transcripts.db")
	before, _ := os.Stat(dbPath)
	start := time.Now()
	st, err := Open(root)
	if err != nil {
		t.Fatalf("Open failed while a writer held the reserved lock: %v", err)
	}
	defer st.Close()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Open took %v: it is waiting on a write lock it does not need", d)
	}
	after, _ := os.Stat(dbPath)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("Open modified the database although the schema was current")
	}
}

// The real pattern: the drainer and the reviewer open the store on every tick
// while ingest keeps writing.
func TestConcurrentOpensWhileWriting(t *testing.T) {
	root := freshRoot(t)
	writer, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			in := domain.PipelineFragment{Source: "claude", SourceID: fmt.Sprintf("s%d", i), Title: "t", Content: fmt.Sprintf("text %d", i)}
			if _, err := writer.Accept(context.Background(), "reg", in); err != nil {
				errs <- fmt.Errorf("writer: %w", err)
				return
			}
		}
	}()
	var openers sync.WaitGroup
	for g := 0; g < 6; g++ {
		openers.Add(1)
		go func() {
			defer openers.Done()
			for i := 0; i < 15; i++ {
				st, err := Open(root)
				if err != nil {
					errs <- fmt.Errorf("open: %w", err)
					return
				}
				_ = st.Close()
			}
		}()
	}
	openers.Wait()
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Upgrades are real writes; racing openers must apply them once, safely.
func TestOpenUpgradesAnOldSchemaOnceUnderRace(t *testing.T) {
	root := freshRoot(t)
	conn := other(t, root)
	ctx := context.Background()
	// Return the database to its pre-derived state: base tables only.
	for _, table := range []string{"tx_cursors", "tx_usage", "tx_tool_calls", "tx_turns", "tx_sessions", "tx_meta"} {
		if _, err := conn.ExecContext(ctx, `DROP TABLE `+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	conn.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := Open(root)
			if err != nil {
				errs <- err
				return
			}
			_ = st.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("racing upgrade: %v", err)
	}
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var v string
	if err := st.db.QueryRow(`SELECT value FROM tx_meta WHERE key = 'schema_version'`).Scan(&v); err != nil || v != fmt.Sprint(derivedSchemaVersion) {
		t.Fatalf("schema version %q %v", v, err)
	}
	for _, table := range derivedTables {
		if n := count(t, st, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, table); n != 1 {
			t.Fatalf("table %s missing after upgrade", table)
		}
	}
}

// A database whose recorded version is behind, or that lost a table, is
// brought current on the next Open; a current one is left alone.
func TestOpenRepairsAStaleVersionAndMissingTable(t *testing.T) {
	root := freshRoot(t)
	conn := other(t, root)
	ctx := context.Background()
	for _, q := range []string{`DROP TABLE tx_usage`, `UPDATE tx_meta SET value = '0' WHERE key = 'schema_version'`} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	conn.Close()
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n := count(t, st, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'tx_usage'`); n != 1 {
		t.Fatal("missing table not recreated")
	}
	var v string
	if err := st.db.QueryRow(`SELECT value FROM tx_meta WHERE key = 'schema_version'`).Scan(&v); err != nil || v != fmt.Sprint(derivedSchemaVersion) {
		t.Fatalf("version %q %v", v, err)
	}
}

func TestBusyErrorsAreRecognisedAndOthersAreNot(t *testing.T) {
	for _, msg := range []string{"database is locked (5) (SQLITE_BUSY)", "database table is locked", "SQLITE_BUSY_SNAPSHOT"} {
		if !isBusy(fmt.Errorf("%s", msg)) {
			t.Fatalf("%q should be retried", msg)
		}
	}
	for _, msg := range []string{"no such table: x", "constraint failed", ""} {
		if isBusy(fmt.Errorf("%s", msg)) {
			t.Fatalf("%q must not be retried", msg)
		}
	}
	if isBusy(nil) {
		t.Fatal("nil is not busy")
	}
	calls := 0
	err := retryBusy(func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("database is locked (5)")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retry: %v calls=%d", err, calls)
	}
	calls = 0
	if err := retryBusy(func() error { calls++; return fmt.Errorf("syntax error") }); err == nil || calls != 1 {
		t.Fatalf("non-busy errors must not retry: %v calls=%d", err, calls)
	}
}
