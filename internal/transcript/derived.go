package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// Derived transcript data lives in the same owner-private database as the
// searchable text, and passes through the same redaction. The tx_* tables hold
// structure (who, when, which tool, how many tokens), not raw material:
// thinking text is never kept, tool results are status and size unless the
// ingest asked for a bounded redacted excerpt, and tool inputs are capped.
const derivedSchemaVersion = 4

const (
	maxToolInputBytes     = 4 << 10
	maxToolExcerptBytes   = 2 << 10
	maxSearchContentBytes = 12 << 20
	maxDescriptionBytes   = 1 << 10
)

const derivedSchema = `
CREATE TABLE IF NOT EXISTS tx_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tx_sessions (
 session_pk TEXT PRIMARY KEY, tool TEXT NOT NULL, native_session_id TEXT NOT NULL,
 segment_key TEXT NOT NULL, kind TEXT NOT NULL, parent_session_pk TEXT NOT NULL DEFAULT '',
 parent_native_id TEXT NOT NULL DEFAULT '', agent_type TEXT NOT NULL DEFAULT '',
 spawn_depth INTEGER NOT NULL DEFAULT 0, spawn_tool_use_id TEXT NOT NULL DEFAULT '',
 is_fork INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '', cwd TEXT NOT NULL DEFAULT '',
 git_branch TEXT NOT NULL DEFAULT '', entrypoint TEXT NOT NULL DEFAULT '',
 started_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '', turn_count INTEGER NOT NULL DEFAULT 0,
 description_redacted TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS tx_sessions_native ON tx_sessions (native_session_id);
CREATE INDEX IF NOT EXISTS tx_sessions_parent ON tx_sessions (parent_session_pk);
CREATE TABLE IF NOT EXISTS tx_turns (
 session_pk TEXT NOT NULL, seq INTEGER NOT NULL, event_uuid TEXT NOT NULL, parent_uuid TEXT NOT NULL DEFAULT '',
 role TEXT NOT NULL, ts TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '',
 is_sidechain INTEGER NOT NULL DEFAULT 0, stop_reason TEXT NOT NULL DEFAULT '',
 text_redacted TEXT NOT NULL DEFAULT '', has_thinking INTEGER NOT NULL DEFAULT 0, thinking_bytes INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (session_pk, event_uuid)
);
CREATE INDEX IF NOT EXISTS tx_turns_order ON tx_turns (session_pk, seq);
CREATE INDEX IF NOT EXISTS tx_turns_ts ON tx_turns (ts);
CREATE VIRTUAL TABLE IF NOT EXISTS tx_turn_search USING fts5(text, session_pk UNINDEXED, seq UNINDEXED);
CREATE TABLE IF NOT EXISTS tx_tool_calls (
 tool_use_id TEXT NOT NULL, session_pk TEXT NOT NULL, turn_uuid TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
 input_redacted TEXT NOT NULL DEFAULT '', result_status TEXT NOT NULL DEFAULT 'none', result_bytes INTEGER NOT NULL DEFAULT 0,
 result_excerpt_redacted TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '',
 target_redacted TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (session_pk, tool_use_id)
);
CREATE INDEX IF NOT EXISTS tx_tool_calls_name ON tx_tool_calls (name, started_at);
CREATE INDEX IF NOT EXISTS tx_tool_calls_time ON tx_tool_calls (started_at);
CREATE INDEX IF NOT EXISTS tx_tool_calls_status ON tx_tool_calls (result_status, started_at);
CREATE TABLE IF NOT EXISTS tx_usage (
 session_pk TEXT NOT NULL, request_id TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', ts TEXT NOT NULL DEFAULT '',
 input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
 cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
 effort TEXT NOT NULL DEFAULT '', PRIMARY KEY (session_pk, request_id)
);
CREATE INDEX IF NOT EXISTS tx_usage_ts ON tx_usage (ts);
CREATE TABLE IF NOT EXISTS tx_cursors (
 cursor_id TEXT PRIMARY KEY, session_pk TEXT NOT NULL, file_size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL,
 byte_offset INTEGER NOT NULL, line_no INTEGER NOT NULL, tail_digest TEXT NOT NULL, source_id TEXT NOT NULL DEFAULT ''
);`

var derivedTables = []string{"tx_meta", "tx_sessions", "tx_turns", "tx_tool_calls", "tx_usage", "tx_cursors",
	"tx_enrichment", "tx_enrichment_spend", "tx_embeddings", "tx_enrichment_search", "tx_enrichment_search_data", "tx_enrichment_search_idx",
	"tx_enrichment_search_content", "tx_enrichment_search_docsize", "tx_enrichment_search_config",
	"tx_turn_search", "tx_turn_search_data", "tx_turn_search_idx", "tx_turn_search_content", "tx_turn_search_docsize", "tx_turn_search_config"}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func cursorID(registration, path string) string {
	raw, _ := json.Marshal([]string{registration, path})
	return digest(raw)
}

// Cursor returns the stored consumption cursor for a source-relative path.
func (s *Store) Cursor(ctx context.Context, registration, path string) (domain.TranscriptCursor, bool) {
	if s == nil {
		return domain.TranscriptCursor{}, false
	}
	var c domain.TranscriptCursor
	var mtime int64
	err := s.db.QueryRowContext(ctx, `SELECT file_size, mtime_ns, byte_offset, line_no, tail_digest, source_id FROM tx_cursors WHERE cursor_id = ?`,
		cursorID(registration, path)).Scan(&c.FileSize, &mtime, &c.ByteOffset, &c.LineNo, &c.TailDigest, &c.SourceID)
	if err != nil {
		return domain.TranscriptCursor{}, false
	}
	c.ModTime = time.Unix(0, mtime).UTC()
	return c, true
}

// sessionPK is the transcript record id: a digest of the source identity, so it
// reveals no raw locator and matches the searchable record for the same segment.
func sessionPK(registration, source, sourceID, segment string) string {
	identity, _ := json.Marshal([]string{registration, source, sourceID, segment})
	return digest(identity)
}

func clip(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// redactedInput redacts a tool input and caps it. Over the cap, only a
// fingerprint of the top-level keys and the original size survive.
func redactedInput(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return ""
	}
	clean, err := json.Marshal(redactValue(value))
	if err != nil {
		return ""
	}
	if len(clean) <= maxToolInputBytes {
		return string(clean)
	}
	var keys []string
	if m, ok := value.(map[string]any); ok {
		for k := range m {
			keys = append(keys, Redact(k))
		}
		sort.Strings(keys)
	}
	fp, _ := json.Marshal(map[string]any{"truncated": true, "bytes": len(clean), "keys": keys})
	return string(fp)
}

// applyDetail writes one segment's derived rows inside the caller's transaction.
// Every string is redacted here, so a source cannot persist anything unredacted.
func applyDetail(ctx context.Context, tx *sql.Tx, registration, source, sourceID, segment string, d *domain.TranscriptDetail) (string, error) {
	pk := sessionPK(registration, source, sourceID, segment)
	if d.Reset {
		for _, table := range []string{"tx_turns", "tx_tool_calls", "tx_usage", "tx_turn_search"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE session_pk = ?`, pk); err != nil {
				return "", err
			}
		}
	}
	parentPK := ""
	if d.Session.ParentNativeID != "" {
		parentPK = sessionPK(registration, source, d.Session.ParentNativeID, "")
		// A tool whose sessions carry a segment key (Codex: one per rollout file)
		// has no empty-segment parent id; resolve its parent by native id when the
		// parent is already stored. An unresolved parent keeps the computed id and
		// parent_native_id, which a reader can still join on.
		var present int
		if tx.QueryRowContext(ctx, `SELECT 1 FROM tx_sessions WHERE session_pk = ?`, parentPK).Scan(&present) != nil {
			var found string
			if tx.QueryRowContext(ctx, `SELECT session_pk FROM tx_sessions WHERE native_session_id = ? AND tool = ? AND kind = 'main' ORDER BY started_at, session_pk LIMIT 1`,
				Redact(d.Session.ParentNativeID), Redact(d.Session.Tool)).Scan(&found) == nil {
				parentPK = found
			}
		}
	}
	ss := d.Session
	if _, err := tx.ExecContext(ctx, `INSERT INTO tx_sessions (session_pk, tool, native_session_id, segment_key, kind, parent_session_pk, parent_native_id,
 agent_type, spawn_depth, spawn_tool_use_id, is_fork, model, cwd, git_branch, entrypoint, started_at, ended_at, description_redacted)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(session_pk) DO UPDATE SET
  model = CASE WHEN tx_sessions.model = '' THEN excluded.model ELSE tx_sessions.model END,
  cwd = CASE WHEN tx_sessions.cwd = '' THEN excluded.cwd ELSE tx_sessions.cwd END,
  git_branch = CASE WHEN tx_sessions.git_branch = '' THEN excluded.git_branch ELSE tx_sessions.git_branch END,
  entrypoint = CASE WHEN tx_sessions.entrypoint = '' THEN excluded.entrypoint ELSE tx_sessions.entrypoint END,
  description_redacted = CASE WHEN excluded.description_redacted <> '' THEN excluded.description_redacted ELSE tx_sessions.description_redacted END,
  agent_type = excluded.agent_type, spawn_depth = excluded.spawn_depth, spawn_tool_use_id = excluded.spawn_tool_use_id,
  is_fork = excluded.is_fork, parent_session_pk = excluded.parent_session_pk, parent_native_id = excluded.parent_native_id,
  started_at = CASE WHEN tx_sessions.started_at = '' OR (excluded.started_at <> '' AND excluded.started_at < tx_sessions.started_at) THEN excluded.started_at ELSE tx_sessions.started_at END,
  ended_at = CASE WHEN excluded.ended_at > tx_sessions.ended_at THEN excluded.ended_at ELSE tx_sessions.ended_at END`,
		pk, Redact(ss.Tool), Redact(ss.NativeSessionID), Redact(segment), Redact(ss.Kind), parentPK, Redact(ss.ParentNativeID),
		Redact(ss.AgentType), ss.SpawnDepth, Redact(ss.SpawnToolUseID), boolInt(ss.IsFork), Redact(ss.Model), Redact(ss.CWD),
		Redact(ss.GitBranch), Redact(ss.Entrypoint), stamp(ss.StartedAt), stamp(ss.EndedAt), clip(Redact(ss.Description), maxDescriptionBytes)); err != nil {
		return "", err
	}
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), -1) FROM tx_turns WHERE session_pk = ?`, pk).Scan(&seq); err != nil {
		return "", err
	}
	for _, t := range d.Turns {
		// ON CONFLICT(session_pk, event_uuid) would silently merge turns that
		// share an empty id; a source must always supply one.
		if strings.TrimSpace(t.EventUUID) == "" {
			return "", errors.New("transcript: turn without an event id")
		}
		text := Redact(t.Text)
		res, err := tx.ExecContext(ctx, `INSERT INTO tx_turns (session_pk, seq, event_uuid, parent_uuid, role, ts, request_id, is_sidechain,
 stop_reason, text_redacted, has_thinking, thinking_bytes) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_pk, event_uuid) DO NOTHING`,
			pk, seq+1, Redact(t.EventUUID), Redact(t.ParentUUID), Redact(t.Role), stamp(t.TS), Redact(t.RequestID), boolInt(t.IsSidechain),
			Redact(t.StopReason), text, boolInt(t.HasThinking), t.ThinkingBytes)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			seq++
			// The same redacted text is indexed per turn, so a hit can point at its turn.
			if text != "" {
				if _, err := tx.ExecContext(ctx, `INSERT INTO tx_turn_search (text, session_pk, seq) VALUES (?,?,?)`, text, pk, seq); err != nil {
					return "", err
				}
			}
		}
	}
	for _, c := range d.Tools {
		status := "none"
		if c.HasResult {
			status = "ok"
			if c.IsError {
				status = "error"
			}
		}
		input := redactedInput(c.InputJSON)
		excerpt := ""
		if c.MaxExcerpt > 0 && c.ResultExcerpt != "" {
			limit := c.MaxExcerpt
			if limit > maxToolExcerptBytes {
				limit = maxToolExcerptBytes
			}
			excerpt = clip(Redact(c.ResultExcerpt), limit)
		}
		// A call and its result can arrive in different batches; merge, never lose.
		if _, err := tx.ExecContext(ctx, `INSERT INTO tx_tool_calls (tool_use_id, session_pk, turn_uuid, name, input_redacted, result_status,
 result_bytes, result_excerpt_redacted, started_at, ended_at, target_redacted) VALUES (?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(session_pk, tool_use_id) DO UPDATE SET
  turn_uuid = CASE WHEN excluded.turn_uuid <> '' THEN excluded.turn_uuid ELSE tx_tool_calls.turn_uuid END,
  name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE tx_tool_calls.name END,
  input_redacted = CASE WHEN excluded.input_redacted <> '' THEN excluded.input_redacted ELSE tx_tool_calls.input_redacted END,
  target_redacted = CASE WHEN excluded.target_redacted <> '' THEN excluded.target_redacted ELSE tx_tool_calls.target_redacted END,
  started_at = CASE WHEN excluded.started_at <> '' THEN excluded.started_at ELSE tx_tool_calls.started_at END,
  result_status = CASE WHEN excluded.result_status <> 'none' THEN excluded.result_status ELSE tx_tool_calls.result_status END,
  result_bytes = CASE WHEN excluded.result_status <> 'none' THEN excluded.result_bytes ELSE tx_tool_calls.result_bytes END,
  result_excerpt_redacted = CASE WHEN excluded.result_status <> 'none' THEN excluded.result_excerpt_redacted ELSE tx_tool_calls.result_excerpt_redacted END,
  ended_at = CASE WHEN excluded.ended_at <> '' THEN excluded.ended_at ELSE tx_tool_calls.ended_at END`,
			Redact(c.ToolUseID), pk, Redact(c.TurnUUID), Redact(c.Name), input, status, c.ResultSize, excerpt,
			stamp(c.StartedAt), stamp(c.EndedAt), Redact(toolTarget(c.Name, input))); err != nil {
			return "", err
		}
	}
	// One row per request: a response split across several lines repeats its
	// usage, so merge by maximum rather than summing lines.
	for _, u := range d.Usage {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tx_usage (session_pk, request_id, model, ts, input_tokens, output_tokens,
 cache_read_tokens, cache_creation_tokens, effort) VALUES (?,?,?,?,?,?,?,?,?)
 ON CONFLICT(session_pk, request_id) DO UPDATE SET
  model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE tx_usage.model END,
  effort = CASE WHEN excluded.effort <> '' THEN excluded.effort ELSE tx_usage.effort END,
  input_tokens = MAX(tx_usage.input_tokens, excluded.input_tokens), output_tokens = MAX(tx_usage.output_tokens, excluded.output_tokens),
  cache_read_tokens = MAX(tx_usage.cache_read_tokens, excluded.cache_read_tokens),
  cache_creation_tokens = MAX(tx_usage.cache_creation_tokens, excluded.cache_creation_tokens)`,
			pk, Redact(u.RequestID), Redact(u.Model), stamp(u.TS), u.Input, u.Output, u.CacheRead, u.CacheCreation, Redact(u.Effort)); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tx_sessions SET turn_count = (SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?) WHERE session_pk = ?`, pk, pk); err != nil {
		return "", err
	}
	if d.CursorPath != "" {
		c := d.Cursor
		if _, err := tx.ExecContext(ctx, `INSERT INTO tx_cursors VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(cursor_id) DO UPDATE SET
 session_pk=excluded.session_pk, file_size=excluded.file_size, mtime_ns=excluded.mtime_ns, byte_offset=excluded.byte_offset,
 line_no=excluded.line_no, tail_digest=excluded.tail_digest, source_id=excluded.source_id`,
			cursorID(registration, d.CursorPath), pk, c.FileSize, c.ModTime.UnixNano(), c.ByteOffset, c.LineNo, c.TailDigest, Redact(c.SourceID)); err != nil {
			return "", err
		}
	}
	return pk, nil
}

// rebuildContent regenerates a segment's searchable text from its stored turns,
// so growth needs no re-read of the source. Past the cap the tail is dropped
// from the search text only; tx_turns keeps every turn.
func rebuildContent(ctx context.Context, tx *sql.Tx, pk string) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT role, text_redacted FROM tx_turns WHERE session_pk = ? AND text_redacted <> '' ORDER BY seq`, pk)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var b strings.Builder
	truncated := false
	for rows.Next() {
		var role, text string
		if err := rows.Scan(&role, &text); err != nil {
			return "", false, err
		}
		piece := "## " + role + "\n\n" + text
		if b.Len()+len(piece)+2 > maxSearchContentBytes {
			truncated = true
			break
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(piece)
	}
	return b.String(), truncated, rows.Err()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ToolCallFilter selects stored tool calls. Zero fields match everything.
type ToolCallFilter struct {
	Name            string
	NativeSessionID string
	Since, Until    time.Time
	Status          string // ok, error or none
	Limit           int
}

// ToolCall is the metadata view of a stored call: no input or result text.
type ToolCall struct {
	Name, NativeSessionID, SessionKind, Status string
	ToolUseID                                  string
	StartedAt, EndedAt                         string
	ResultBytes                                int
}

// QueryToolCalls lists tool-call metadata by name, session, time and status.
func (s *Store) QueryToolCalls(ctx context.Context, f ToolCallFilter) ([]ToolCall, error) {
	if s == nil {
		return nil, ErrPrivateStoreRequired
	}
	q := `SELECT c.name, s.native_session_id, s.kind, c.result_status, c.tool_use_id, c.started_at, c.ended_at, c.result_bytes
 FROM tx_tool_calls c JOIN tx_sessions s ON s.session_pk = c.session_pk WHERE 1=1`
	var args []any
	if f.Name != "" {
		q += ` AND c.name = ?`
		args = append(args, f.Name)
	}
	if f.NativeSessionID != "" {
		q += ` AND s.native_session_id = ?`
		args = append(args, f.NativeSessionID)
	}
	if !f.Since.IsZero() {
		q += ` AND c.started_at >= ?`
		args = append(args, stamp(f.Since))
	}
	if !f.Until.IsZero() {
		q += ` AND c.started_at < ?`
		args = append(args, stamp(f.Until))
	}
	if f.Status != "" {
		q += ` AND c.result_status = ?`
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q += ` ORDER BY c.started_at, c.tool_use_id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errors.New("transcript: cannot query tool calls")
	}
	defer rows.Close()
	var out []ToolCall
	for rows.Next() {
		var c ToolCall
		if err := rows.Scan(&c.Name, &c.NativeSessionID, &c.SessionKind, &c.Status, &c.ToolUseID, &c.StartedAt, &c.EndedAt, &c.ResultBytes); err != nil {
			return nil, errors.New("transcript: cannot read tool calls")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UsageTotals sums stored token usage for a native session, subagents included.
type UsageTotals struct {
	Requests                                int
	Input, Output, CacheRead, CacheCreation int64
}

func (s *Store) SessionUsage(ctx context.Context, nativeSessionID string) (UsageTotals, error) {
	if s == nil {
		return UsageTotals{}, ErrPrivateStoreRequired
	}
	var t UsageTotals
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(u.input_tokens),0), COALESCE(SUM(u.output_tokens),0),
 COALESCE(SUM(u.cache_read_tokens),0), COALESCE(SUM(u.cache_creation_tokens),0)
 FROM tx_usage u JOIN tx_sessions s ON s.session_pk = u.session_pk WHERE s.native_session_id = ?`, nativeSessionID).
		Scan(&t.Requests, &t.Input, &t.Output, &t.CacheRead, &t.CacheCreation)
	if err != nil {
		return UsageTotals{}, errors.New("transcript: cannot read usage")
	}
	return t, nil
}
