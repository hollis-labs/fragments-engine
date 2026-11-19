package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
	"github.com/hollis-labs/libs/core/store"
)

// Writer is Fragments Engine's transcript writer for the Postgres corpus. It is
// the only writer: readers hold SELECT only. One Accept is one transaction that
// applies a parsed segment (session, turns, tool calls and results, usage, cursor),
// refreshes the search projection and appends to the change journal, so a reader
// never sees a half-applied segment.
type Writer struct {
	st *store.Store
	// HostFor maps an ingest registration to the host that produced it. Nil or an
	// empty answer records host "unknown": a host is never guessed.
	HostFor func(registration string) string
	now     func() time.Time
}

// NewWriter returns a writer over an opened (and verified) store.
func NewWriter(st *store.Store) *Writer { return &Writer{st: st, now: time.Now} }

// Outcome mirrors the SQLite store's result so the pipeline can count it.
type Outcome = transcript.Outcome

// ErrNoFeed means the journal row is missing: the schema was not migrated.
var ErrNoFeed = errors.New("transcript: change journal is not initialised (run the migrations)")

// journal allocates commit-ordered sequence numbers. The feed_state row is
// locked for the whole transaction, so concurrent writers serialise here and
// seq order equals commit order.
type journal struct {
	head    int64
	pending int64
	tx      store.Tx
	session string
	gen     int64
	rev     int64
}

func (j *journal) note(ctx context.Context, kind, entity string) error {
	j.pending++
	_, err := j.tx.ExecContext(ctx, `INSERT INTO changes (seq, session_id, generation, content_revision, kind, entity_key) VALUES (?,?,?,?,?,?)`,
		j.head+j.pending, j.session, j.gen, j.rev, kind, nullStr(entity))
	return err
}

// pgText makes s storable in a Postgres text column. NUL cannot be stored; it is
// replaced with U+FFFD and reported so the event can say so.
func pgText(s string) (string, bool) {
	if strings.IndexByte(s, 0) < 0 {
		return s, false
	}
	return strings.ReplaceAll(s, "\x00", "�"), true
}

func tsPtr(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Truncate(time.Microsecond)
}

// Cursor returns the stored cursor for a source-relative path.
func (w *Writer) Cursor(ctx context.Context, registration, path string) (domain.TranscriptCursor, bool) {
	var c domain.TranscriptCursor
	var meta []byte
	err := w.st.QueryRowContext(ctx, `SELECT byte_offset, line_no, tail_digest, metadata FROM ingest_checkpoints WHERE source_id = ? AND source_file_key = ?`,
		registration, path).Scan(&c.ByteOffset, &c.LineNo, &c.TailDigest, &meta)
	if err != nil {
		return domain.TranscriptCursor{}, false
	}
	var m struct {
		FileSize int64  `json:"file_size"`
		MTimeNS  int64  `json:"mtime_ns"`
		SourceID string `json:"source_id"`
	}
	_ = json.Unmarshal(meta, &m)
	c.FileSize, c.SourceID = m.FileSize, m.SourceID
	if m.MTimeNS != 0 {
		c.ModTime = time.Unix(0, m.MTimeNS).UTC()
	}
	return c, true
}

// Accept applies one parsed segment. A fragment without structured detail is not
// a transcript segment and is refused.
func (w *Writer) Accept(ctx context.Context, registration string, in domain.PipelineFragment) (Outcome, error) {
	if w == nil || w.st == nil {
		return "", transcript.ErrPrivateStoreRequired
	}
	if in.Detail == nil {
		return "", fmt.Errorf("%w: structured transcript detail is required", transcript.ErrRecordRejected)
	}
	if strings.TrimSpace(registration) == "" || strings.TrimSpace(in.Source) == "" || strings.TrimSpace(in.SourceID) == "" {
		return "", errors.New("transcript: source identity is required")
	}
	if len(in.Attachments) != 0 {
		return "", fmt.Errorf("%w: %w", transcript.ErrRecordRejected, transcript.ErrAttachments)
	}
	var outcome Outcome
	err := w.st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		outcome, err = w.apply(ctx, tx, registration, in)
		return err
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

func (w *Writer) apply(ctx context.Context, tx store.Tx, registration string, in domain.PipelineFragment) (Outcome, error) {
	d := in.Detail
	segment := in.SourceIdentity.SegmentKey
	pk := transcript.SessionPK(registration, in.Source, in.SourceID, segment)

	j := &journal{tx: tx, session: pk}
	if err := tx.QueryRowContext(ctx, `SELECT head FROM feed_state WHERE singleton FOR UPDATE`).Scan(&j.head); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoFeed
		}
		return "", err
	}

	var known int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE session_id = ?`, pk).Scan(&known); err != nil {
		return "", err
	}
	if known > 0 && d.Reset {
		if err := resetSession(ctx, tx, pk); err != nil {
			return "", err
		}
	}
	if err := w.upsertSession(ctx, tx, registration, in, pk); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT generation, content_revision FROM sessions WHERE session_id = ?`, pk).Scan(&j.gen, &j.rev); err != nil {
		return "", err
	}
	if known > 0 && d.Reset {
		if err := j.note(ctx, "session_reset", ""); err != nil {
			return "", err
		}
	}

	changed := 0
	var ordinal int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), -1) FROM events WHERE session_id = ?`, pk).Scan(&ordinal); err != nil {
		return "", err
	}
	now := w.now().UTC()
	for _, t := range d.Turns {
		if strings.TrimSpace(t.EventUUID) == "" {
			return "", errors.New("transcript: turn without an event id")
		}
		text, nul1 := pgText(transcript.Redact(t.Text))
		thinking, nul2 := pgText(transcript.Redact(t.Thinking))
		eventID, _ := pgText(transcript.Redact(t.EventUUID))
		payload := "{}"
		if nul1 || nul2 {
			payload = `{"nul_replaced":true}`
		}
		thinkBytes := int64(t.ThinkingBytes)
		if l := int64(len(t.Thinking)); l > thinkBytes {
			thinkBytes = l
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO events (session_id, event_id, ordinal, parent_event_id, kind, role, event_at, ingested_at, event_revision,
 request_id, is_sidechain, stop_reason, has_thinking, thinking_bytes, text_redacted, thinking_redacted, payload_redacted, content_digest)
VALUES (?,?,?,?, 'turn', ?,?,?,1, ?,?,?,?,?,?,?,?::jsonb,?) ON CONFLICT (session_id, event_id) DO NOTHING`,
			pk, eventID, ordinal+1, nullStr(transcript.Redact(t.ParentUUID)), transcript.Redact(t.Role), tsPtr(t.TS), now,
			transcript.Redact(t.RequestID), t.IsSidechain, transcript.Redact(t.StopReason), t.HasThinking || thinking != "", thinkBytes, text, thinking, payload, eventDigest(text, thinking))
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		ordinal++
		changed++
		if err := j.note(ctx, "event_upsert", eventID); err != nil {
			return "", err
		}
		for field, v := range map[string]string{"text": text, "thinking": thinking} {
			if err := upsertDoc(ctx, tx, searchDoc{session: pk, kind: "event", entity: eventID, field: field, digest: sha(v), generation: j.gen, text: v}); err != nil {
				return "", err
			}
		}
	}

	for _, c := range d.Tools {
		n, err := w.applyTool(ctx, tx, j, pk, c)
		if err != nil {
			return "", err
		}
		changed += n
	}

	for _, u := range d.Usage {
		res, err := tx.ExecContext(ctx, `INSERT INTO usage (session_id, request_id, model, event_at, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, effort)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT (session_id, request_id) DO UPDATE SET
 model = COALESCE(NULLIF(excluded.model, ''), usage.model),
 effort = CASE WHEN excluded.effort <> '' THEN excluded.effort ELSE usage.effort END,
 input_tokens = GREATEST(usage.input_tokens, excluded.input_tokens), output_tokens = GREATEST(usage.output_tokens, excluded.output_tokens),
 cache_read_tokens = GREATEST(usage.cache_read_tokens, excluded.cache_read_tokens),
 cache_creation_tokens = GREATEST(usage.cache_creation_tokens, excluded.cache_creation_tokens)`,
			pk, transcript.Redact(u.RequestID), nullStr(transcript.Redact(u.Model)), tsPtr(u.TS), u.Input, u.Output, u.CacheRead, u.CacheCreation, transcript.Redact(u.Effort))
		if err != nil {
			return "", err
		}
		_ = res
	}

	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET legacy_turn_count = (SELECT count(*) FROM events WHERE session_id = ?),
 content_digest = (SELECT encode(sha256(convert_to(coalesce(string_agg(content_digest, '' ORDER BY ordinal, event_id), ''), 'UTF8')), 'hex') FROM events WHERE session_id = ?),
 content_revision = content_revision + ? WHERE session_id = ?`, pk, pk, boolToInt(changed > 0), pk); err != nil {
		return "", err
	}
	if changed > 0 {
		if err := j.note(ctx, "session_update", ""); err != nil {
			return "", err
		}
	}

	if d.CursorPath != "" {
		if err := w.saveCursor(ctx, tx, registration, in, pk); err != nil {
			return "", err
		}
	}
	if j.pending > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE feed_state SET head = ? WHERE singleton`, j.head+j.pending); err != nil {
			return "", err
		}
	}
	switch {
	case known == 0:
		return transcript.Inserted, nil
	case changed > 0 || d.Reset:
		return transcript.Updated, nil
	}
	return transcript.Skipped, nil
}

// eventDigest is sha256 of the visible text, extended with the thinking text only
// when there is any, so an event hashes the same whether it was imported or written live.
func eventDigest(text, thinking string) string {
	if thinking == "" {
		return sha(text)
	}
	return sha(text + "\x00" + thinking)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (w *Writer) hostOf(registration string) string {
	if w.HostFor != nil {
		if h := w.HostFor(registration); h != "" {
			return h
		}
	}
	return "unknown"
}

func (w *Writer) upsertSession(ctx context.Context, tx store.Tx, registration string, in domain.PipelineFragment, pk string) error {
	d := in.Detail
	ss := d.Session
	segment := in.SourceIdentity.SegmentKey
	parentPK := ""
	if ss.ParentNativeID != "" {
		parentPK = transcript.SessionPK(registration, in.Source, ss.ParentNativeID, "")
		var present int
		if tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE session_id = ?`, parentPK).Scan(&present) != nil {
			var found string
			if tx.QueryRowContext(ctx, `SELECT session_id FROM sessions WHERE native_session_id = ? AND harness = ? AND kind = 'main' ORDER BY started_at NULLS LAST, session_id LIMIT 1`,
				transcript.Redact(ss.ParentNativeID), transcript.Redact(ss.Tool)).Scan(&found) == nil {
				parentPK = found
			} else {
				parentPK = "" // unresolved: keep the native hint only, never a dangling key
			}
		}
	}
	meta := "{}"
	if ss.ParentNativeID != "" && parentPK == "" {
		b, _ := json.Marshal(map[string]string{"unresolved_parent_native_id": transcript.Redact(ss.ParentNativeID)})
		meta = string(b)
	}
	if err := w.ensureSource(ctx, tx, registration, ss.Tool); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions (session_id, harness, native_session_id, segment_key, host_id, kind, agent_type, spawn_depth, spawn_tool_use_id, entrypoint,
 parent_session_id, parent_native_id, is_fork, model, cwd_redacted, git_branch_redacted, description_redacted, started_at, ended_at, ingest_state, generation, content_revision, content_digest, metadata)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'current',1,1,'unset',?::jsonb)
ON CONFLICT (session_id) DO UPDATE SET
 model = COALESCE(sessions.model, excluded.model), cwd_redacted = COALESCE(sessions.cwd_redacted, excluded.cwd_redacted),
 git_branch_redacted = COALESCE(sessions.git_branch_redacted, excluded.git_branch_redacted),
 entrypoint = CASE WHEN sessions.entrypoint = '' THEN excluded.entrypoint ELSE sessions.entrypoint END,
 description_redacted = COALESCE(excluded.description_redacted, sessions.description_redacted),
 agent_type = excluded.agent_type, spawn_depth = excluded.spawn_depth, spawn_tool_use_id = excluded.spawn_tool_use_id, is_fork = excluded.is_fork,
 parent_session_id = COALESCE(excluded.parent_session_id, sessions.parent_session_id), parent_native_id = COALESCE(excluded.parent_native_id, sessions.parent_native_id),
 started_at = LEAST(sessions.started_at, excluded.started_at), ended_at = GREATEST(sessions.ended_at, excluded.ended_at)`,
		pk, transcript.Redact(ss.Tool), transcript.Redact(ss.NativeSessionID), transcript.Redact(segment), w.hostOf(registration), transcript.Redact(ss.Kind), transcript.Redact(ss.AgentType),
		ss.SpawnDepth, transcript.Redact(ss.SpawnToolUseID), transcript.Redact(ss.Entrypoint), nullStr(parentPK), nullStr(transcript.Redact(ss.ParentNativeID)), ss.IsFork,
		nullStr(transcript.Redact(ss.Model)), nullStr(transcript.Redact(ss.CWD)), nullStr(transcript.Redact(ss.GitBranch)), nullStr(transcript.Redact(ss.Description)),
		tsPtr(ss.StartedAt), tsPtr(ss.EndedAt), meta)
	return err
}

func (w *Writer) ensureSource(ctx context.Context, tx store.Tx, registration, harness string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO sources (source_id, harness, host_id, source_kind, canonical_root_ref, parser_version) VALUES (?,?,?,'archive',?,?)
ON CONFLICT (source_id) DO NOTHING`, registration, transcript.Redact(harness), w.hostOf(registration), registration, MappingVersion)
	return err
}

func (w *Writer) saveCursor(ctx context.Context, tx store.Tx, registration string, in domain.PipelineFragment, pk string) error {
	d := in.Detail
	c := d.Cursor
	meta, _ := json.Marshal(map[string]any{"file_size": c.FileSize, "mtime_ns": c.ModTime.UnixNano(), "source_id": transcript.Redact(c.SourceID), "session_id": pk})
	_, err := tx.ExecContext(ctx, `INSERT INTO ingest_checkpoints (source_id, source_file_key, file_generation, byte_offset, line_no, tail_digest, parser_version, checkpoint_at, metadata)
VALUES (?,?, ?, ?,?,?,?,?, ?::jsonb)
ON CONFLICT (source_id, source_file_key) DO UPDATE SET file_generation = excluded.file_generation, byte_offset = excluded.byte_offset, line_no = excluded.line_no,
 tail_digest = excluded.tail_digest, parser_version = excluded.parser_version, checkpoint_at = excluded.checkpoint_at, metadata = excluded.metadata`,
		registration, d.CursorPath, fmt.Sprint(c.FileSize), c.ByteOffset, c.LineNo, c.TailDigest, MappingVersion, w.now().UTC(), string(meta))
	return err
}

// resetSession drops a session's derived rows before a re-parse and starts a new
// generation, so a reader that cached the old one knows to reload.
func resetSession(ctx context.Context, tx store.Tx, pk string) error {
	for _, q := range []string{
		`DELETE FROM search_documents WHERE session_id = ?`, // chunks cascade
		`DELETE FROM tool_results WHERE session_id = ?`,
		`DELETE FROM tool_calls WHERE session_id = ?`,
		`DELETE FROM usage WHERE session_id = ?`,
		`DELETE FROM events WHERE session_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, pk); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET generation = generation + 1, content_revision = content_revision + 1 WHERE session_id = ?`, pk)
	return err
}

// applyTool merges one call and its result parts. A call and its result can
// arrive in different batches; neither overwrites the other with nothing.
func (w *Writer) applyTool(ctx context.Context, tx store.Tx, j *journal, pk string, c domain.TranscriptToolCall) (int, error) {
	callID, _ := pgText(transcript.Redact(c.ToolUseID))
	if strings.TrimSpace(callID) == "" {
		return 0, errors.New("transcript: tool call without an id")
	}
	status := "none"
	if c.HasResult {
		status = "ok"
		if c.IsError {
			status = "error"
		}
	}
	input, _ := pgText(transcript.RedactedInputFull(c.InputJSON))
	state := "unknown"
	var js any
	if input != "" {
		state = "complete" // the source delivers the whole input; nothing is cut
		if json.Valid([]byte(input)) && !strings.Contains(input, `\u0000`) {
			js = input
		}
	}
	eventID := transcript.Redact(c.TurnUUID)
	changed := 0

	// The event must exist; a call whose turn has not arrived is held, not invented.
	var one int
	haveEvent := eventID != "" && tx.QueryRowContext(ctx, `SELECT 1 FROM events WHERE session_id = ? AND event_id = ?`, pk, eventID).Scan(&one) == nil
	if !haveEvent {
		var existingEvent string
		if err := tx.QueryRowContext(ctx, `SELECT event_id FROM tool_calls WHERE session_id = ? AND call_id = ?`, pk, callID).Scan(&existingEvent); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
			return 0, w.holdCall(ctx, tx, pk, c, callID, eventID)
		}
		eventID = existingEvent // result for a call already stored
	}

	// Only a real difference is a change: a replayed or unchanged call is a no-op,
	// so it neither bumps the journal nor makes the session look updated.
	var oldName, oldInput, oldStatus string
	var oldBytes int64
	var oldStarted, oldEnded sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT tool_name, input_text_redacted, status, reported_result_bytes, started_at, ended_at FROM tool_calls WHERE session_id = ? AND call_id = ?`,
		pk, callID).Scan(&oldName, &oldInput, &oldStatus, &oldBytes, &oldStarted, &oldEnded)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	name := transcript.Redact(c.Name)
	started, ended := tsPtr(c.StartedAt), tsPtr(c.EndedAt)
	differs := !exists ||
		(name != "" && name != oldName) ||
		(input != "" && input != oldInput) ||
		(status != "none" && (status != oldStatus || int64(c.ResultSize) != oldBytes)) ||
		(started != nil && (!oldStarted.Valid || !oldStarted.Time.Equal(started.(time.Time)))) ||
		(ended != nil && (!oldEnded.Valid || !oldEnded.Time.Equal(ended.(time.Time))))
	if differs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_calls (session_id, call_id, event_id, tool_name, target_redacted, input_text_redacted, input_json_redacted, input_complete, input_state,
 reported_result_bytes, started_at, ended_at, status)
VALUES (?,?,?,?,?,?,?::jsonb,?,?,?,?,?,?)
ON CONFLICT (session_id, call_id) DO UPDATE SET
 tool_name = CASE WHEN excluded.tool_name <> '' THEN excluded.tool_name ELSE tool_calls.tool_name END,
 input_text_redacted = CASE WHEN excluded.input_text_redacted <> '' THEN excluded.input_text_redacted ELSE tool_calls.input_text_redacted END,
 input_json_redacted = COALESCE(excluded.input_json_redacted, tool_calls.input_json_redacted),
 input_complete = tool_calls.input_complete OR excluded.input_complete,
 input_state = CASE WHEN excluded.input_state <> 'unknown' THEN excluded.input_state ELSE tool_calls.input_state END,
 target_redacted = COALESCE(excluded.target_redacted, tool_calls.target_redacted),
 started_at = COALESCE(excluded.started_at, tool_calls.started_at),
 status = CASE WHEN excluded.status <> 'none' THEN excluded.status ELSE tool_calls.status END,
 reported_result_bytes = CASE WHEN excluded.status <> 'none' THEN excluded.reported_result_bytes ELSE tool_calls.reported_result_bytes END,
 ended_at = COALESCE(excluded.ended_at, tool_calls.ended_at)`,
			pk, callID, eventID, name, nullStr(transcript.Redact(transcript.ToolTarget(c.Name, input))), input, js, state == "complete", state,
			c.ResultSize, started, ended, status); err != nil {
			return 0, err
		}
		changed++
		if err := j.note(ctx, "tool_call_upsert", callID); err != nil {
			return 0, err
		}
		if input != "" {
			if err := upsertDoc(ctx, tx, searchDoc{session: pk, kind: "tool_input", entity: callID, field: "input", digest: sha(input), generation: j.gen, text: input}); err != nil {
				return 0, err
			}
		}
	}

	for _, part := range c.Results {
		n, err := w.applyResultPart(ctx, tx, j, pk, callID, part)
		if err != nil {
			return 0, err
		}
		changed += n
	}
	return changed, nil
}

func (w *Writer) holdCall(ctx context.Context, tx store.Tx, pk string, c domain.TranscriptToolCall, callID, eventID string) error {
	b, _ := json.Marshal(c)
	refs, _ := json.Marshal(map[string]string{"session": pk, "event": eventID})
	_, err := tx.ExecContext(ctx, `INSERT INTO pending_records (pending_id, session_hint, record_kind, native_key, required_refs, payload_redacted, encoding, payload_digest, parser_version, reason,
 first_seen_at, last_seen_at, attempts, state) VALUES (?,?, 'tool_call', ?, ?::jsonb, ?, 'json/utf8', ?, ?, 'missing_event', ?, ?, 0, 'pending')
ON CONFLICT (pending_id) DO UPDATE SET last_seen_at = excluded.last_seen_at, attempts = pending_records.attempts + 1`,
		"live/"+pk+"/"+callID, pk, callID, string(refs), b, sha(string(b)), MappingVersion, w.now().UTC(), w.now().UTC())
	return err
}

func (w *Writer) applyResultPart(ctx context.Context, tx store.Tx, j *journal, pk, callID string, p domain.TranscriptResultPart) (int, error) {
	partID, _ := pgText(transcript.Redact(p.PartID))
	if partID == "" {
		partID = fmt.Sprintf("p%d", p.Ordinal)
	}
	var text, jsn any
	var data []byte
	var digestSrc, searchText string
	switch {
	case p.Bytes != nil:
		data = p.Bytes
		digestSrc = sha(string(p.Bytes))
	case p.JSON != "":
		clean, ok := transcript.RedactJSONValue(p.JSON)
		if !ok {
			t, _ := pgText(transcript.Redact(p.JSON))
			text, digestSrc, searchText = t, sha(t), t
			break
		}
		clean, _ = pgText(clean)
		jsn, digestSrc, searchText = clean, sha(clean), clean
	default:
		t, _ := pgText(transcript.Redact(p.Text))
		text, digestSrc, searchText = t, sha(t), t
	}
	media := p.MediaType
	if media == "" {
		media = "text/plain"
	}
	var cur sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT content_digest FROM tool_results WHERE session_id = ? AND call_id = ? AND part_id = ?`, pk, callID, partID).Scan(&cur)
	if err == nil && cur.String == digestSrc {
		return 0, nil // identical part already stored
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tool_results (session_id, call_id, part_id, ordinal, media_type, text_redacted, json_redacted, bytes_redacted, result_complete, content_digest)
VALUES (?,?,?,?,?,?,?::jsonb,?,?,?)
ON CONFLICT (session_id, call_id, part_id) DO UPDATE SET ordinal = excluded.ordinal, media_type = excluded.media_type, text_redacted = excluded.text_redacted,
 json_redacted = excluded.json_redacted, bytes_redacted = excluded.bytes_redacted, result_complete = excluded.result_complete, content_digest = excluded.content_digest`,
		pk, callID, partID, p.Ordinal, media, text, jsn, data, p.Complete, digestSrc); err != nil {
		return 0, err
	}
	if err := j.note(ctx, "tool_result_upsert", callID+"/"+partID); err != nil {
		return 0, err
	}
	err = upsertDoc(ctx, tx, searchDoc{session: pk, kind: "tool_result", entity: callID + "/" + partID, field: "text", digest: digestSrc, generation: j.gen, text: searchText})
	return 1, err
}

// upsertDoc keeps one search document per (session, kind, entity, field) in step
// with its canonical text: unchanged text is a no-op, changed text is replaced,
// and text that became empty removes the document.
func upsertDoc(ctx context.Context, tx store.Tx, d searchDoc) error {
	id := docID(d)
	var old string
	err := tx.QueryRowContext(ctx, `SELECT content_digest FROM search_documents WHERE document_id = ?`, id).Scan(&old)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if exists && old == d.digest {
		return nil
	}
	if exists {
		if _, err := tx.ExecContext(ctx, `DELETE FROM search_documents WHERE document_id = ?`, id); err != nil {
			return err
		}
	}
	_, err = writeDocs(ctx, tx, []searchDoc{d})
	return err
}
