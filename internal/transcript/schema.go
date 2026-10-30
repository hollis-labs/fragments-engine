package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Open is called on every queue tick and inbox review as well as by ingest, so
// it must be cheap and must not contend with a concurrent writer. It reads the
// schema first and writes only when the schema is genuinely behind, and it
// waits for locks instead of failing on them.

// busyTimeoutMS is how long one statement waits for a lock. A var so tests can
// shorten it.
var busyTimeoutMS = 5000

// isBusy reports whether err is SQLite's lock contention, which is transient.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "(5)") || strings.Contains(msg, "(6)")
}

// retryBusy repeats fn briefly while it reports lock contention. busy_timeout
// already waits inside SQLite; this covers the cases where SQLite returns BUSY
// at once (for example when a read cannot be upgraded), without waiting.
func retryBusy(fn func() error) error {
	delay := 25 * time.Millisecond
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		if err = fn(); !isBusy(err) {
			return err
		}
		time.Sleep(delay)
		if delay < 400*time.Millisecond {
			delay *= 2
		}
	}
	return err
}

// requiredTables are the tables a current database must already contain.
func requiredTables() []string {
	return append([]string{"transcripts", "transcript_search"}, derivedTables...)
}

// inspectSchema lists the database's table names and the recorded schema
// version ("" when none), in read-only statements.
func inspectSchema(db *sql.DB) (names map[string]bool, version string, err error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	names = map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, "", err
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if names["tx_meta"] {
		err := db.QueryRow(`SELECT value FROM tx_meta WHERE key = 'schema_version'`).Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", err
		}
	}
	return names, version, nil
}

func schemaCurrent(names map[string]bool, version string) bool {
	if version != fmt.Sprint(derivedSchemaVersion) {
		return false
	}
	for _, t := range requiredTables() {
		if !names[t] {
			return false
		}
	}
	return true
}

// upgradeSchema creates or repairs the schema inside one IMMEDIATE
// transaction, so racing openers serialize and the loser sees the winner's
// work. It re-checks under the lock and does nothing if the schema is current.
func upgradeSchema(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// DELETE avoids a separately permissioned persistent WAL/SHM. It is set
	// here, outside the transaction, and only when we are going to write.
	if _, err := conn.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	// Re-check now that this connection holds the write lock.
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names[n] = true
	}
	rows.Close()
	var version string
	if names["tx_meta"] {
		if err := conn.QueryRowContext(ctx, `SELECT value FROM tx_meta WHERE key = 'schema_version'`).Scan(&version); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if schemaCurrent(names, version) {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS transcripts (
 id TEXT PRIMARY KEY, content_digest TEXT NOT NULL, redaction_version INTEGER NOT NULL,
 material_json TEXT NOT NULL, title TEXT NOT NULL, content TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS transcript_search USING fts5(id UNINDEXED, title, content);`+derivedSchema+enrichmentSchema); err != nil {
		return err
	}
	// v1 -> v2: the subagent purpose line. ADD COLUMN has no IF NOT EXISTS.
	var hasDescription int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('tx_sessions') WHERE name = 'description_redacted'`).Scan(&hasDescription); err != nil {
		return err
	}
	if hasDescription == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE tx_sessions ADD COLUMN description_redacted TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// v2 -> v3: the tool call's target. It is derived from the stored redacted input, so existing rows gain it here.
	var hasTarget int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('tx_tool_calls') WHERE name = 'target_redacted'`).Scan(&hasTarget); err != nil {
		return err
	}
	if hasTarget == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE tx_tool_calls ADD COLUMN target_redacted TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if err := backfillTargets(ctx, conn); err != nil {
		return err
	}
	// v3 -> v4: the per-turn search index. Turn text is stored a second time in it; existing turns are indexed here.
	if err := backfillTurnSearch(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO tx_meta VALUES ('schema_version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(derivedSchemaVersion)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// writeDeadline bounds how long a write after a paid call keeps retrying on
// lock contention. A var so tests can shorten it.
var writeDeadline = 60 * time.Second

// retryBusyFor retries fn while it reports lock contention, with backoff from
// 25 ms up to 1 s, until the deadline. busy_timeout still waits inside each
// attempt; the deadline bounds the whole retry loop.
func retryBusyFor(deadline time.Duration, fn func() error) error {
	end := time.Now().Add(deadline)
	delay := 25 * time.Millisecond
	for {
		err := fn()
		if !isBusy(err) {
			return err
		}
		remaining := time.Until(end)
		if remaining <= 0 {
			return err
		}
		// Sleep the backoff, or whatever is left, then try once more: the last
		// attempt happens at the deadline, never before it.
		wait := delay
		if wait > remaining {
			wait = remaining
		}
		time.Sleep(wait)
		if delay < time.Second {
			delay *= 2
			if delay > time.Second {
				delay = time.Second
			}
		}
	}
}

// WriteError is a store write that failed after its retries. The text is fixed
// and content-free, but names whether the cause was lock contention (Busy), the
// one failure an operator can act on. Callers that have already paid for a
// result must treat it as a hard stop, never as a provider failure.
type WriteError struct {
	What string // for example "save enrichment"
	Busy bool
}

func (e *WriteError) Error() string {
	class := "error"
	if e.Busy {
		class = "busy"
	}
	return fmt.Sprintf("transcript: cannot %s (%s)", e.What, class)
}

func writeFail(what string, err error) error { return &WriteError{What: what, Busy: isBusy(err)} }

// SetLockWaits tunes how long the private store waits for a lock (per statement)
// and how long a write keeps retrying on contention. It affects stores opened
// afterwards and returns a function that restores the previous values. Meant for
// tests that must simulate a persistent lock without waiting a minute.
func SetLockWaits(busy, deadline time.Duration) (restore func()) {
	prevBusy, prevDeadline := busyTimeoutMS, writeDeadline
	busyTimeoutMS, writeDeadline = int(busy/time.Millisecond), deadline
	return func() { busyTimeoutMS, writeDeadline = prevBusy, prevDeadline }
}

// backfillTargets fills target_redacted for rows that have an input but no target. It is idempotent.
func backfillTargets(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT session_pk, tool_use_id, name, input_redacted FROM tx_tool_calls WHERE target_redacted = '' AND input_redacted <> ''`)
	if err != nil {
		return err
	}
	type row struct{ pk, id, target string }
	var todo []row
	for rows.Next() {
		var pk, id, name, input string
		if err := rows.Scan(&pk, &id, &name, &input); err != nil {
			rows.Close()
			return err
		}
		if t := toolTarget(name, input); t != "" {
			todo = append(todo, row{pk, id, Redact(t)})
		}
	}
	rows.Close()
	for _, r := range todo {
		if _, err := conn.ExecContext(ctx, `UPDATE tx_tool_calls SET target_redacted = ? WHERE session_pk = ? AND tool_use_id = ?`, r.target, r.pk, r.id); err != nil {
			return err
		}
	}
	return nil
}

// backfillTurnSearch indexes the existing turns the first time the per-turn index is empty.
func backfillTurnSearch(ctx context.Context, conn *sql.Conn) error {
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_turn_search`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO tx_turn_search (text, session_pk, seq) SELECT text_redacted, session_pk, seq FROM tx_turns WHERE text_redacted <> ''`)
	return err
}
