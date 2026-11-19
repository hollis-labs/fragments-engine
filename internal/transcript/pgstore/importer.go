package pgstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/libs/core/store"
)

// MappingVersion identifies the legacy-to-Postgres mapping in import receipts.
const MappingVersion = "v4-to-pg-1"

// Importer copies a SQLite transcripts.db (schema v4) into the Postgres
// corpus. It is idempotent (every insert is ON CONFLICT DO NOTHING) and
// resumable (per-table progress commits with each page). It only reads the
// source and never touches the live file: point it at a backup copy.
type Importer struct {
	Src *sql.DB
	Dst *store.Store
	// RunID names the import. Re-running with the same RunID resumes it.
	RunID string
	// SourceIdentity is checksum-bound provenance of the source database (the
	// backup's sha256), reused across retries.
	SourceIdentity string
	Batch          int
	Log            func(format string, args ...any)

	now time.Time
	ev  evidence
}

// TableStat is the per-table reconciliation record.
type TableStat struct {
	SourceRows   int64            `json:"source_rows"`
	Imported     int64            `json:"imported"`
	Rejected     int64            `json:"rejected"`
	Pending      int64            `json:"pending"`
	TruncatedNS  int64            `json:"timestamps_truncated_below_microsecond"`
	HashRows     int64            `json:"hash_rows"`
	HashSum      uint64           `json:"hash_sum"`
	TargetRows   int64            `json:"target_rows,omitempty"`
	TargetSum    uint64           `json:"target_sum,omitempty"`
	Verified     *bool            `json:"verified,omitempty"`
	LastRowID    int64            `json:"last_rowid"`
	Done         bool             `json:"done"`
	RejectReason map[string]int64 `json:"reject_reasons,omitempty"`
}

type evidence struct {
	Tables     map[string]*TableStat `json:"tables"`
	SourceMeta map[string]string     `json:"source_meta,omitempty"`
}

func (im *Importer) logf(f string, a ...any) {
	if im.Log != nil {
		im.Log(f, a...)
	}
}

// ---- canonical row hashing, identical on the source and target side ----

// canon hashes a tuple with length prefixes so field boundaries cannot collide.
func canon(fields ...string) uint64 {
	h := sha256.New()
	var lb [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint64(lb[:], uint64(len(f)))
		h.Write(lb[:])
		h.Write([]byte(f))
	}
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}

// ---- time and value handling ----

// parseTS maps a legacy RFC3339 text to a microsecond-truncated UTC time.
// null: the source was empty. lossy: sub-microsecond digits were dropped.
func parseTS(s string) (t time.Time, null, lossy bool, err error) {
	if s == "" {
		return time.Time{}, true, false, nil
	}
	p, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false, false, err
	}
	p = p.UTC()
	tr := p.Truncate(time.Microsecond)
	return tr, false, !tr.Equal(p), nil
}

func fmtTime(t time.Time) string { return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano) }

// tsArg parses s for insertion: a nil argument is NULL.
func tsArg(s string, st *TableStat) (arg any, canonical string, err error) {
	t, null, lossy, err := parseTS(s)
	if err != nil {
		return nil, "", err
	}
	if null {
		return nil, "", nil
	}
	if lossy {
		st.TruncatedNS++
	}
	return t, fmtTime(t), nil
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func i2s(i int64) string { return fmt.Sprint(i) }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func hasNUL(ss ...string) bool {
	for _, s := range ss {
		if strings.IndexByte(s, 0) >= 0 {
			return true
		}
	}
	return false
}

// ---- run bookkeeping ----

func (im *Importer) begin(ctx context.Context) error {
	im.now = time.Now().UTC()
	im.ev = evidence{Tables: map[string]*TableStat{}}
	var raw []byte
	err := im.Dst.QueryRowContext(ctx, `SELECT evidence FROM import_runs WHERE run_id = ?`, im.RunID).Scan(&raw)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &im.ev); err != nil {
			return err
		}
		if im.ev.Tables == nil {
			im.ev.Tables = map[string]*TableStat{}
		}
		return nil
	case err != sql.ErrNoRows:
		return err
	}
	return im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		b, _ := json.Marshal(im.ev)
		_, err := tx.ExecContext(ctx, `INSERT INTO import_runs (run_id, source_db_identity, source_manifest_digest, mapping_version, state, evidence, started_at)
VALUES (?, ?, '', ?, 'running', ?::jsonb, ?) ON CONFLICT (run_id) DO NOTHING`, im.RunID, im.SourceIdentity, MappingVersion, string(b), im.now)
		return err
	})
}

func (im *Importer) saveEvidenceTx(ctx context.Context, tx store.Tx, state string) error {
	b, err := json.Marshal(im.ev)
	if err != nil {
		return err
	}
	var done any
	if state == "complete" {
		done = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `UPDATE import_runs SET evidence = ?::jsonb, state = ?, completed_at = ? WHERE run_id = ?`, string(b), state, done, im.RunID)
	return err
}

func (im *Importer) saveEvidence(ctx context.Context, state string) error {
	return im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error { return im.saveEvidenceTx(ctx, tx, state) })
}

func (im *Importer) stat(table string) *TableStat {
	s := im.ev.Tables[table]
	if s == nil {
		s = &TableStat{RejectReason: map[string]int64{}}
		im.ev.Tables[table] = s
	}
	if s.RejectReason == nil {
		s.RejectReason = map[string]int64{}
	}
	return s
}

type outcome int

const (
	oImported outcome = iota
	oPending
	oRejected
)

// result is what applying one source row did. hash covers the row's canonical
// source tuple and is folded into the table's aggregate only when imported.
type result struct {
	out    outcome
	hash   uint64
	reason string
}

func (st *TableStat) fold(r result) {
	switch r.out {
	case oImported:
		st.Imported++
		st.HashRows++
		st.HashSum += r.hash
	case oPending:
		st.Pending++
	default:
		st.Rejected++
		st.RejectReason[r.reason]++
	}
}

// keep stores a source row in legacy_rows so nothing is dropped silently. The
// payload is the row as JSON; a JSON string preserves NUL and any text exactly.
func (im *Importer) keep(ctx context.Context, tx store.Tx, table, pk, reason, disposition string, row map[string]any) error {
	b, _ := json.Marshal(row)
	_, err := tx.ExecContext(ctx, `INSERT INTO legacy_rows (import_run_id, source_table, source_pk, source_row_digest, payload_redacted, encoding, mapped_targets, disposition, reason)
VALUES (?, ?, ?, ?, ?, 'json/utf8', '[]'::jsonb, ?, ?) ON CONFLICT DO NOTHING`, im.RunID, table, pk, sha(string(b)), b, disposition, reason)
	return err
}

// spec describes one source table.
type spec[T any] struct {
	table string
	cols  string // select list after rowid
	// scan reads one row (rowid first) into T.
	scan func(*sql.Rows) (rowid int64, v T, err error)
	// apply writes one row. Domain rejections are returned as results; a
	// database error is returned as err and triggers the row-by-row retry.
	apply func(ctx context.Context, tx store.Tx, st *TableStat, v T) (result, error)
	// describe names the row for legacy_rows.
	describe func(v T) (pk string, row map[string]any)
}

// copyTable pages the source by rowid. Each page commits together with its
// progress, so a crash resumes exactly. A page that fails in the database is
// retried row by row; a row that still fails is kept as rejected.
func copyTable[T any](ctx context.Context, im *Importer, sp spec[T]) error {
	st := im.stat(sp.table)
	if st.Done {
		im.logf("%s: already imported", sp.table)
		return nil
	}
	if err := im.Src.QueryRowContext(ctx, `SELECT count(*) FROM `+sp.table).Scan(&st.SourceRows); err != nil {
		return err
	}
	batch := im.Batch
	if batch <= 0 {
		batch = 2000
	}
	type item struct {
		id int64
		v  T
	}
	for {
		rows, err := im.Src.QueryContext(ctx, `SELECT rowid, `+sp.cols+` FROM `+sp.table+` WHERE rowid > ? ORDER BY rowid LIMIT ?`, st.LastRowID, batch)
		if err != nil {
			return err
		}
		var page []item
		for rows.Next() {
			id, v, err := sp.scan(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("%s: scan: %w", sp.table, err)
			}
			page = append(page, item{id, v})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		last := page[len(page)-1].id
		before := *st
		before.RejectReason = cloneMap(st.RejectReason)
		err = im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			// WriteTx may re-run this callback: start every attempt from the saved state.
			*st = before
			st.RejectReason = cloneMap(before.RejectReason)
			for _, it := range page {
				res, err := sp.apply(ctx, tx, st, it.v)
				if err != nil {
					return err
				}
				if res.out == oRejected {
					pk, row := sp.describe(it.v)
					if err := im.keep(ctx, tx, sp.table, pk, res.reason, "rejected", row); err != nil {
						return err
					}
				}
				st.fold(res)
			}
			st.LastRowID = last
			return im.saveEvidenceTx(ctx, tx, "running")
		})
		if err != nil {
			// Isolate the failing rows instead of failing the whole import.
			*st = before
			st.RejectReason = cloneMap(before.RejectReason)
			im.logf("%s: page ending at rowid %d failed (%v); retrying row by row", sp.table, last, err)
			for _, it := range page {
				if err := applyOne(ctx, im, sp, st, it.v); err != nil {
					return err
				}
			}
			st.LastRowID = last
			if err := im.saveEvidence(ctx, "running"); err != nil {
				return err
			}
		}
		im.logf("%s: %d/%d (rejected %d, pending %d)", sp.table, st.Imported+st.Pending+st.Rejected, st.SourceRows, st.Rejected, st.Pending)
	}
	st.Done = true
	return im.saveEvidence(ctx, "running")
}

// applyOne imports a single row in its own transaction. A database failure
// keeps the row in legacy_rows as rejected (reason db_error) instead of
// failing the import.
func applyOne[T any](ctx context.Context, im *Importer, sp spec[T], st *TableStat, v T) error {
	var res result
	var tmp TableStat
	tmp.RejectReason = map[string]int64{}
	err := im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		tmp = TableStat{RejectReason: map[string]int64{}}
		r, err := sp.apply(ctx, tx, &tmp, v)
		if err != nil {
			return err
		}
		if r.out == oRejected {
			pk, row := sp.describe(v)
			if err := im.keep(ctx, tx, sp.table, pk, r.reason, "rejected", row); err != nil {
				return err
			}
		}
		res = r
		return nil
	})
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		res = result{out: oRejected, reason: "db_error"}
		pk, row := sp.describe(v)
		row["_error"] = msg
		if kerr := im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			return im.keep(ctx, tx, sp.table, pk, "db_error", "rejected", row)
		}); kerr != nil {
			return kerr
		}
		tmp = TableStat{}
	}
	st.TruncatedNS += tmp.TruncatedNS
	st.fold(res)
	return nil
}

func cloneMap(m map[string]int64) map[string]int64 {
	c := make(map[string]int64, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// ---- table mappings ----

type sessionRow struct {
	pk, tool, native, segment, kind, parentPK, parentNative, agentType, spawnTool, model, cwd, branch, entrypoint, startedAt, endedAt, desc string
	spawnDepth, turnCount                                                                                                                   int64
	isFork                                                                                                                                  bool
}

func (im *Importer) importSessions(ctx context.Context) error {
	return copyTable(ctx, im, spec[sessionRow]{
		table: "tx_sessions",
		cols: `session_pk, tool, native_session_id, segment_key, kind, parent_session_pk, parent_native_id, agent_type,
 spawn_depth, spawn_tool_use_id, is_fork, model, cwd, git_branch, entrypoint, started_at, ended_at, turn_count, description_redacted`,
		scan: func(r *sql.Rows) (int64, sessionRow, error) {
			var id int64
			var s sessionRow
			var fork int64
			err := r.Scan(&id, &s.pk, &s.tool, &s.native, &s.segment, &s.kind, &s.parentPK, &s.parentNative, &s.agentType,
				&s.spawnDepth, &s.spawnTool, &fork, &s.model, &s.cwd, &s.branch, &s.entrypoint, &s.startedAt, &s.endedAt, &s.turnCount, &s.desc)
			s.isFork = fork != 0
			return id, s, err
		},
		describe: func(s sessionRow) (string, map[string]any) {
			return s.pk, map[string]any{"session_pk": s.pk, "tool": s.tool, "native_session_id": s.native, "started_at": s.startedAt, "ended_at": s.endedAt}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, s sessionRow) (result, error) {
			if hasNUL(s.pk, s.native, s.cwd, s.branch, s.desc, s.model) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			started, startedC, err := tsArg(s.startedAt, st)
			if err != nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			ended, endedC, err := tsArg(s.endedAt, st)
			if err != nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			meta := map[string]string{}
			if s.parentPK != "" {
				meta["legacy_parent_session_pk"] = s.parentPK
			}
			mb, _ := json.Marshal(meta)
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (session_id, harness, native_session_id, segment_key, kind, agent_type, spawn_depth, spawn_tool_use_id,
 entrypoint, parent_native_id, is_fork, model, cwd_redacted, git_branch_redacted, description_redacted, started_at, ended_at, legacy_turn_count,
 ingest_state, generation, content_revision, content_digest, metadata)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'current',1,1,'unset',?::jsonb) ON CONFLICT (session_id) DO NOTHING`,
				s.pk, s.tool, s.native, s.segment, s.kind, s.agentType, s.spawnDepth, s.spawnTool, s.entrypoint, nullStr(s.parentNative), s.isFork,
				nullStr(s.model), nullStr(s.cwd), nullStr(s.branch), nullStr(s.desc), started, ended, s.turnCount, string(mb)); err != nil {
				return result{}, err
			}
			return result{out: oImported, hash: canon(s.pk, s.tool, s.native, s.segment, s.kind, s.parentPK, s.parentNative, s.agentType,
				i2s(s.spawnDepth), s.spawnTool, b2s(s.isFork), s.model, s.cwd, s.branch, s.entrypoint, startedC, endedC, i2s(s.turnCount), s.desc)}, nil
		},
	})
}

type turnRow struct {
	pk, uuid, parent, role, ts, reqID, stop, text string
	seq, thinkBytes                               int64
	side, thinking                                bool
}

func (im *Importer) importTurns(ctx context.Context) error {
	return copyTable(ctx, im, spec[turnRow]{
		table: "tx_turns",
		cols:  `session_pk, seq, event_uuid, parent_uuid, role, ts, request_id, is_sidechain, stop_reason, text_redacted, has_thinking, thinking_bytes`,
		scan: func(r *sql.Rows) (int64, turnRow, error) {
			var id, side, th int64
			var t turnRow
			err := r.Scan(&id, &t.pk, &t.seq, &t.uuid, &t.parent, &t.role, &t.ts, &t.reqID, &side, &t.stop, &t.text, &th, &t.thinkBytes)
			t.side, t.thinking = side != 0, th != 0
			return id, t, err
		},
		describe: func(t turnRow) (string, map[string]any) {
			return t.pk + "|" + t.uuid, map[string]any{"session_pk": t.pk, "seq": t.seq, "event_uuid": t.uuid, "parent_uuid": t.parent, "role": t.role,
				"ts": t.ts, "request_id": t.reqID, "is_sidechain": t.side, "stop_reason": t.stop, "text_redacted": t.text, "has_thinking": t.thinking, "thinking_bytes": t.thinkBytes}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, t turnRow) (result, error) {
			if hasNUL(t.pk, t.uuid, t.parent, t.role, t.reqID, t.stop, t.text) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			var before = st.TruncatedNS
			at, atC, err := tsArg(t.ts, st)
			if err != nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			payload := "{}"
			if st.TruncatedNS != before { // keep the original text when precision was lost
				b, _ := json.Marshal(map[string]string{"legacy_ts": t.ts})
				payload = string(b)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO events (session_id, event_id, ordinal, parent_event_id, kind, role, event_at, ingested_at, event_revision,
 request_id, is_sidechain, stop_reason, has_thinking, thinking_bytes, text_redacted, payload_redacted, content_digest)
VALUES (?,?,?,?, 'turn', ?,?,?,1, ?,?,?,?,?,?,?::jsonb,?) ON CONFLICT (session_id, event_id) DO NOTHING`,
				t.pk, t.uuid, t.seq, nullStr(t.parent), t.role, at, im.now, t.reqID, t.side, t.stop, t.thinking, t.thinkBytes, t.text, payload, sha(t.text)); err != nil {
				return result{}, err
			}
			return result{out: oImported, hash: canon(t.pk, i2s(t.seq), t.uuid, t.parent, t.role, atC, t.reqID, b2s(t.side), t.stop, t.text, b2s(t.thinking), i2s(t.thinkBytes))}, nil
		},
	})
}

type callRow struct {
	id, pk, turn, name, input, status, excerpt, startedAt, endedAt, target string
	resultBytes                                                            int64
}

func inputState(in string) string {
	switch {
	case in == "":
		return "unknown"
	case strings.HasPrefix(in, `{"truncated":true`):
		return "partial"
	}
	return "complete"
}

func (im *Importer) importToolCalls(ctx context.Context) error {
	return copyTable(ctx, im, spec[callRow]{
		table: "tx_tool_calls",
		cols:  `tool_use_id, session_pk, turn_uuid, name, input_redacted, result_status, result_bytes, result_excerpt_redacted, started_at, ended_at, target_redacted`,
		scan: func(r *sql.Rows) (int64, callRow, error) {
			var id int64
			var c callRow
			err := r.Scan(&id, &c.id, &c.pk, &c.turn, &c.name, &c.input, &c.status, &c.resultBytes, &c.excerpt, &c.startedAt, &c.endedAt, &c.target)
			return id, c, err
		},
		describe: func(c callRow) (string, map[string]any) {
			return c.pk + "|" + c.id, map[string]any{"tool_use_id": c.id, "session_pk": c.pk, "turn_uuid": c.turn, "name": c.name, "input_redacted": c.input,
				"result_status": c.status, "result_bytes": c.resultBytes, "result_excerpt_redacted": c.excerpt, "started_at": c.startedAt, "ended_at": c.endedAt, "target_redacted": c.target}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, c callRow) (result, error) {
			if hasNUL(c.id, c.pk, c.turn, c.name, c.input, c.status, c.excerpt, c.target) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			started, startedC, err := tsArg(c.startedAt, st)
			if err != nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			ended, endedC, err := tsArg(c.endedAt, st)
			if err != nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			pend := func(reason string) (result, error) {
				b, _ := json.Marshal(map[string]any{"tool_use_id": c.id, "session_pk": c.pk, "turn_uuid": c.turn, "name": c.name, "input_redacted": c.input,
					"result_status": c.status, "result_bytes": c.resultBytes, "result_excerpt_redacted": c.excerpt, "started_at": c.startedAt, "ended_at": c.endedAt, "target_redacted": c.target})
				refs, _ := json.Marshal(map[string]string{"session": c.pk, "event": c.turn})
				_, err := tx.ExecContext(ctx, `INSERT INTO pending_records (pending_id, session_hint, record_kind, native_key, required_refs, payload_redacted, encoding,
 payload_digest, parser_version, reason, first_seen_at, last_seen_at, attempts, state)
VALUES (?,?, 'tool_call', ?, ?::jsonb, ?, 'json/utf8', ?, ?, ?, ?, ?, 0, 'pending') ON CONFLICT DO NOTHING`,
					im.RunID+"/tx_tool_calls/"+c.pk+"/"+c.id, c.pk, c.id, string(refs), b, sha(string(b)), MappingVersion, reason, im.now, im.now)
				return result{out: oPending}, err
			}
			if c.turn == "" {
				return pend("missing_event: empty turn_uuid")
			}
			state := inputState(c.input)
			var js any
			if c.input != "" && json.Valid([]byte(c.input)) && !strings.Contains(c.input, `\u0000`) {
				js = c.input
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO tool_calls (session_id, call_id, event_id, tool_name, target_redacted, input_text_redacted, input_json_redacted,
 input_complete, input_state, reported_result_bytes, started_at, ended_at, status)
SELECT ?,?,?,?,?,?,?::jsonb,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM events WHERE session_id = ? AND event_id = ?)
ON CONFLICT (session_id, call_id) DO NOTHING`,
				c.pk, c.id, c.turn, c.name, nullStr(c.target), c.input, js, state == "complete", state, c.resultBytes, started, ended, c.status, c.pk, c.turn)
			if err != nil {
				return result{}, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				var one int
				switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM tool_calls WHERE session_id = ? AND call_id = ?`, c.pk, c.id).Scan(&one); err {
				case sql.ErrNoRows:
					return pend("missing_event: turn not in events")
				case nil: // already imported by an earlier run
				default:
					return result{}, err
				}
			}
			if c.excerpt != "" {
				if _, err := tx.ExecContext(ctx, `INSERT INTO tool_results (session_id, call_id, part_id, ordinal, media_type, text_redacted, result_complete, content_digest)
VALUES (?,?, 'legacy-excerpt', 0, 'text/plain', ?, false, ?) ON CONFLICT (session_id, call_id, part_id) DO NOTHING`, c.pk, c.id, c.excerpt, sha(c.excerpt)); err != nil {
					return result{}, err
				}
			}
			return result{out: oImported, hash: canon(c.id, c.pk, c.turn, c.name, c.input, c.status, i2s(c.resultBytes), c.excerpt, startedC, endedC, c.target)}, nil
		},
	})
}

type usageRow struct {
	pk, req, model, ts, effort string
	in, out, cacheRead, cacheC int64
}

func (im *Importer) importUsage(ctx context.Context) error {
	return copyTable(ctx, im, spec[usageRow]{
		table: "tx_usage",
		cols:  `session_pk, request_id, model, ts, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, effort`,
		scan: func(r *sql.Rows) (int64, usageRow, error) {
			var id int64
			var u usageRow
			err := r.Scan(&id, &u.pk, &u.req, &u.model, &u.ts, &u.in, &u.out, &u.cacheRead, &u.cacheC, &u.effort)
			return id, u, err
		},
		describe: func(u usageRow) (string, map[string]any) {
			return u.pk + "|" + u.req, map[string]any{"session_pk": u.pk, "request_id": u.req, "model": u.model, "ts": u.ts, "input_tokens": u.in, "output_tokens": u.out,
				"cache_read_tokens": u.cacheRead, "cache_creation_tokens": u.cacheC, "effort": u.effort}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, u usageRow) (result, error) {
			if hasNUL(u.pk, u.req, u.model, u.effort) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			at, atC, err := tsArg(u.ts, st)
			if err != nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			if u.in < 0 || u.out < 0 || u.cacheRead < 0 || u.cacheC < 0 {
				return result{out: oRejected, reason: "negative_counter"}, nil
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage (session_id, request_id, model, event_at, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, effort)
VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT (session_id, request_id) DO NOTHING`, u.pk, u.req, nullStr(u.model), at, u.in, u.out, u.cacheRead, u.cacheC, u.effort); err != nil {
				return result{}, err
			}
			return result{out: oImported, hash: canon(u.pk, u.req, u.model, atC, i2s(u.in), i2s(u.out), i2s(u.cacheRead), i2s(u.cacheC), u.effort)}, nil
		},
	})
}

// ---- orchestration ----

// Run imports the core corpus tables, resolves parent links, computes session
// digests and reconciles the result. Re-running resumes.
func (im *Importer) Run(ctx context.Context) error {
	if err := im.begin(ctx); err != nil {
		return err
	}
	for _, step := range []struct {
		name string
		fn   func(context.Context) error
	}{
		{"sessions", im.importSessions},
		{"turns", im.importTurns},
		{"tool_calls", im.importToolCalls},
		{"usage", im.importUsage},
		{"enrichment", im.importEnrichment},
		{"embeddings", im.importEmbeddings},
		{"spend", im.importSpend},
		{"cursors", im.importCursors},
		{"legacy_material", im.importLegacyMaterial},
		{"source_meta", im.importMeta},
		{"search", im.rebuildSearch},
	} {
		if err := step.fn(ctx); err != nil {
			_ = im.saveEvidence(ctx, "failed")
			return fmt.Errorf("import %s: %w", step.name, err)
		}
	}
	if err := im.finalize(ctx); err != nil {
		return err
	}
	if err := im.Reconcile(ctx); err != nil {
		return err
	}
	return im.saveEvidence(ctx, "imported")
}

// finalize resolves parent links by exact legacy session key (never by native
// id) and computes each session's content digest from its ordered events.
func (im *Importer) finalize(ctx context.Context) error {
	return im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions c SET parent_session_id = p.session_id
FROM sessions p WHERE c.parent_session_id IS NULL AND c.session_id <> p.session_id AND c.metadata->>'legacy_parent_session_pk' = p.session_id`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions s SET content_digest = d.dg
FROM (SELECT s2.session_id, encode(sha256(convert_to(coalesce(string_agg(e.content_digest, '' ORDER BY e.ordinal, e.event_id), ''), 'UTF8')), 'hex') AS dg
      FROM sessions s2 LEFT JOIN events e ON e.session_id = s2.session_id GROUP BY s2.session_id) d
WHERE s.session_id = d.session_id AND s.content_digest = 'unset'`)
		return err
	})
}

// Reconcile recomputes every table's canonical hash from Postgres, in the
// source's own representation, and compares it with the source-side hash of the
// rows that were imported. It also checks that imported + pending + rejected
// equals the source row count.
func (im *Importer) Reconcile(ctx context.Context) error {
	type tgt struct {
		table, query string
		cols         int
		kinds        map[int]string // column index -> ts | json | vec | cost
	}
	targets := []tgt{
		{"tx_sessions", `SELECT s.session_id, s.harness, s.native_session_id, s.segment_key, s.kind, coalesce(s.metadata->>'legacy_parent_session_pk',''), coalesce(s.parent_native_id,''), s.agent_type,
 s.spawn_depth::text, s.spawn_tool_use_id, CASE WHEN s.is_fork THEN '1' ELSE '0' END, coalesce(s.model,''), coalesce(s.cwd_redacted,''), coalesce(s.git_branch_redacted,''), s.entrypoint,
 s.started_at, s.ended_at, s.legacy_turn_count::text, coalesce(s.description_redacted,'') FROM sessions s`, 19, map[int]string{15: "ts", 16: "ts"}},
		{"tx_turns", `SELECT e.session_id, e.ordinal::text, e.event_id, coalesce(e.parent_event_id,''), e.role, e.event_at, e.request_id, CASE WHEN e.is_sidechain THEN '1' ELSE '0' END,
 e.stop_reason, e.text_redacted, CASE WHEN e.has_thinking THEN '1' ELSE '0' END, e.thinking_bytes::text FROM events e`, 12, map[int]string{5: "ts"}},
		{"tx_tool_calls", `SELECT c.call_id, c.session_id, c.event_id, c.tool_name, c.input_text_redacted, c.status, c.reported_result_bytes::text, coalesce(r.text_redacted,''), c.started_at, c.ended_at, coalesce(c.target_redacted,'')
 FROM tool_calls c LEFT JOIN tool_results r ON r.session_id = c.session_id AND r.call_id = c.call_id AND r.part_id = 'legacy-excerpt'`, 11, map[int]string{8: "ts", 9: "ts"}},
		{"tx_usage", `SELECT u.session_id, u.request_id, coalesce(u.model,''), u.event_at, u.input_tokens::text, u.output_tokens::text, u.cache_read_tokens::text, u.cache_creation_tokens::text, u.effort FROM usage u`, 9, map[int]string{3: "ts"}},
		{"tx_enrichment", `SELECT split_part(e.enrichment_id, ':', 2), e.session_id, e.tier::text, e.prompt_version, e.model, e.provider, e.content_digest, e.status, coalesce(e.error_class,''),
 coalesce(e.result_redacted::text,''), coalesce(e.quality::text,''), e.tokens_in::text, e.tokens_out::text, e.calls::text, e.chunks::text, coalesce(e.run_id,''), e.request_prefix, e.created_at FROM enrichments e`, 18, map[int]string{9: "json", 10: "json", 17: "ts"}},
		{"tx_embeddings", `SELECT m.session_id, m.kind, m.chunk_idx::text, g.model, m.dimension::text, g.format, m.text_digest, m.embedding::text, m.created_at
 FROM embeddings m JOIN embedding_groups g USING (group_id)`, 9, map[int]string{7: "vec", 8: "ts"}},
		{"tx_enrichment_spend", `SELECT split_part(p.spend_id, ':', 2), p.run_id, p.session_id, p.request_id, p.provider, p.model, p.tokens_in::text, p.tokens_out::text, p.tokens_cache_read::text,
 p.tokens_cache_write::text, p.cost_usd::text, p.occurred_at FROM enrichment_spend p`, 12, map[int]string{10: "cost", 11: "ts"}},
		{"transcripts", `SELECT legacy_id, content_digest, redaction_version::text, material_json_text, title, content FROM legacy_material`, 6, nil},
	}
	for _, t := range targets {
		st := im.stat(t.table)
		rows, err := im.Dst.QueryContext(ctx, t.query)
		if err != nil {
			return fmt.Errorf("reconcile %s: %w", t.table, err)
		}
		var n int64
		var sum uint64
		for rows.Next() {
			vals := make([]any, t.cols)
			ptrs := make([]any, t.cols)
			for i := range vals {
				if t.kinds[i] == "ts" {
					vals[i] = new(sql.NullTime)
				} else {
					vals[i] = new(string)
				}
				ptrs[i] = vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return err
			}
			f := make([]string, t.cols)
			for i, v := range vals {
				switch t.kinds[i] {
				case "ts":
					if nt := v.(*sql.NullTime); nt.Valid {
						f[i] = fmtTime(nt.Time)
					}
				case "json":
					if s := *v.(*string); s != "" {
						c, err := canonJSON(s)
						if err != nil {
							rows.Close()
							return fmt.Errorf("reconcile %s: json: %w", t.table, err)
						}
						f[i] = c
					}
				case "vec":
					b, err := vectorBytes(*v.(*string))
					if err != nil {
						rows.Close()
						return err
					}
					f[i] = sha(string(b))
				case "cost":
					c, err := strconv.ParseFloat(*v.(*string), 64)
					if err != nil {
						rows.Close()
						return err
					}
					f[i] = fmtCost(c)
				default:
					f[i] = *v.(*string)
				}
			}
			n++
			sum += canon(f...)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		ok := n == st.HashRows && sum == st.HashSum && st.Imported+st.Pending+st.Rejected == st.SourceRows
		st.TargetRows, st.TargetSum, st.Verified = n, sum, &ok
		im.logf("reconcile %s: source %d rows, target %d rows, hash match %v, accounted %d/%d", t.table, st.HashRows, n, sum == st.HashSum, st.Imported+st.Pending+st.Rejected, st.SourceRows)
	}
	// cursors are held, not mapped: every one must be in legacy_rows as pending
	cs := im.stat("tx_cursors")
	var held int64
	if err := im.Dst.QueryRowContext(ctx, `SELECT count(*) FROM legacy_rows WHERE import_run_id = ? AND source_table = 'tx_cursors' AND disposition = 'pending'`, im.RunID).Scan(&held); err != nil {
		return err
	}
	ok := held == cs.SourceRows && cs.Pending == cs.SourceRows
	cs.TargetRows, cs.Verified = held, &ok
	im.logf("reconcile tx_cursors: %d held of %d", held, cs.SourceRows)
	if err := im.verifySearch(ctx); err != nil {
		return err
	}
	return im.saveEvidence(ctx, "running")
}

// Tables returns the per-table reconciliation evidence of the last Run.
func (im *Importer) Tables() map[string]*TableStat { return im.ev.Tables }

// RotateEpoch gives the change feed a new epoch. Readers bind their cursors to
// the epoch, so rotating after a bulk import or a cutover invalidates every
// cursor taken against the old history instead of letting it skip rows.
func RotateEpoch(ctx context.Context, st *store.Store) error {
	return st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE feed_state SET epoch = replace(gen_random_uuid()::text, '-', '') WHERE singleton`)
		return err
	})
}
