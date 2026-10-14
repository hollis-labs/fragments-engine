package antigravity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// Structured capture of one Antigravity conversation database.
//
// A database is a CortexStep log: steps(idx, step_type, status, metadata, ...,
// step_payload). Step type 14 is user input, 15 is a planner response (text,
// thinking, tool calls), 132 is a tool execution step. Everything else is
// skipped and counted per type. Model names live in gen_metadata and join to a
// planner step by response id, never by idx.
//
// Cursor. The producer replaces a whole database with a fresh sqlite snapshot
// each fire, so a byte offset into the file means nothing. TranscriptCursor.
// ByteOffset is instead the idx of the first step that has NOT been consumed:
// steps with a smaller idx are stored. TailDigest identifies the last consumed
// step, so a snapshot that rewrote earlier steps is detected and the segment is
// re-parsed whole (Reset) instead of trusted. Steps in a trailing run with
// status 2, 6 or 7 (running or aborted) are not consumed, so they are read
// again once the producer has synced their final state. The store keys every
// row by event or call id, so re-reading a step never duplicates it.
const (
	stepUser     = 14
	stepPlanner  = 15
	stepToolRun  = 132
	maxStepBytes = 10 << 20
	maxExcerpt   = 2048
	statusOK     = 3
)

// A pass reads at most this many steps or this much step text, whichever comes
// first; a larger conversation continues on the next fire. Variables so tests can
// force a small batch.
var (
	maxBatchSteps = 20000
	maxBatchBytes = 64 << 20
)

// nonTerminal reports step statuses that may still change.
func nonTerminal(status int) bool { return status == 2 || status == 6 || status == 7 }

type stepRow struct {
	idx       int64
	typ       int
	format    int
	status    int
	payload   []byte // only for user and planner steps
	size      int
	meta      pbMsg
	hasMeta   bool
	hasErrors bool
}

func (r stepRow) created() time.Time {
	if !r.hasMeta {
		return time.Time{}
	}
	return r.meta.stamp(1)
}

// latest is the last lifecycle timestamp recorded on the step.
func (r stepRow) latest() time.Time {
	t := r.created()
	if !r.hasMeta {
		return t
	}
	for _, num := range []int{6, 7, 8, 32} {
		if v := r.meta.stamp(num); v.After(t) {
			t = v
		}
	}
	return t
}

// eventID is the turn identity. The metadata uuid (field 12) is shared by the
// steps of one model turn, so it cannot key a step: idx is the append-only
// primary key and is stable across snapshots.
func (r stepRow) eventID() string { return "step-" + strconv.FormatInt(r.idx, 10) }

// uuid is the metadata uuid, used only as one input to the step digest.
func (r stepRow) uuid() string {
	if r.hasMeta {
		if s, ok := r.meta.text(12); ok && s != "" {
			return s
		}
	}
	return "step-" + strconv.FormatInt(r.idx, 10)
}

// digest identifies a step by immutable facts only, never by status or the
// lifecycle timestamps that change while it runs.
func (r stepRow) digest() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s|%d", r.idx, r.typ, r.uuid(), r.created().Unix())))
	return hex.EncodeToString(sum[:])
}

func hasColumn(cols map[string]bool, name string) bool { return cols[name] }

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, errors.New("unsupported Antigravity database schema")
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			return nil, errors.New("unsupported Antigravity database schema")
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// captureConversation turns one conversation database into a fragment carrying
// the searchable text and the structured detail. It returns a skip class instead
// of a fragment when there is nothing new to store.
func captureConversation(ctx context.Context, brainRoot, path, relPath string, info os.FileInfo, maxBytes int64, excerpt int) (domain.PipelineFragment, string, error) {
	if err := snapshotReady(path); err != nil {
		return domain.PipelineFragment{}, "", err
	}
	cur, hasCur := domain.LookupCursor(ctx, relPath)
	if hasCur && cur.FileSize == info.Size() && cur.ModTime.UnixNano() == info.ModTime().UnixNano() {
		return domain.PipelineFragment{}, "unchanged", nil
	}

	// immutable avoids source locks, journals and SHM writes. Pending WAL must
	// therefore be rejected explicitly (above), rather than silently ignored.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return domain.PipelineFragment{}, "", errors.New("cannot open Antigravity database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// ORDER BY may need temporary storage; raw text must stay in memory.
	if _, err := db.ExecContext(ctx, `PRAGMA temp_store=MEMORY`); err != nil {
		return domain.PipelineFragment{}, "", errors.New("cannot configure Antigravity read-only database")
	}
	cols, err := tableColumns(ctx, db, "steps")
	if err != nil {
		return domain.PipelineFragment{}, "", err
	}
	if !hasColumn(cols, "idx") || !hasColumn(cols, "step_type") || !hasColumn(cols, "step_format") || !hasColumn(cols, "step_payload") {
		return domain.PipelineFragment{}, "", errors.New("unsupported Antigravity database schema")
	}

	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	start := int64(0)
	incremental := false
	if hasCur && cur.ByteOffset > 0 {
		last, ok, err := loadStep(ctx, db, cols, cur.ByteOffset-1)
		if err != nil {
			return domain.PipelineFragment{}, "", err
		}
		if ok && last.digest() == cur.TailDigest {
			incremental, start = true, cur.ByteOffset
		}
	}

	steps, partial, err := loadSteps(ctx, db, cols, start)
	if err != nil {
		return domain.PipelineFragment{}, "", err
	}
	if len(steps) == 0 {
		if incremental {
			return domain.PipelineFragment{}, "unchanged", nil
		}
		return domain.PipelineFragment{}, "empty", nil
	}

	models := map[string]string{}
	for _, s := range steps {
		if s.typ == stepPlanner {
			models, err = responseModels(ctx, db)
			if err != nil {
				return domain.PipelineFragment{}, "", err
			}
			break
		}
	}

	detail := domain.TranscriptDetail{
		Session: domain.TranscriptSession{Tool: "antigravity", NativeSessionID: id, Kind: "main"},
	}
	var text strings.Builder
	calls := map[string]*domain.TranscriptToolCall{}
	var order []string
	var started, ended time.Time
	for _, s := range steps {
		if t := s.created(); !t.IsZero() && (started.IsZero() || t.Before(started)) {
			started = t
		}
		if t := s.latest(); t.After(ended) {
			ended = t
		}
		switch s.typ {
		case stepUser, stepPlanner:
			if s.format != 0 || s.size > maxStepBytes {
				return domain.PipelineFragment{}, "", errPayload
			}
			body, err := stepText(s.typ, s.payload)
			if err != nil {
				return domain.PipelineFragment{}, "", err
			}
			role := "user"
			if s.typ == stepPlanner {
				role = "assistant"
			}
			turn := domain.TranscriptTurn{EventUUID: s.eventID(), Role: role, TS: s.created(), Text: strings.TrimSpace(body)}
			if s.typ == stepPlanner {
				planner(&s, &turn, &detail, models, calls, &order)
			}
			if turn.Text != "" {
				if int64(text.Len()+len(turn.Text)+20) > maxBytes {
					return domain.PipelineFragment{}, "", errors.New("Antigravity conversation exceeds size limit")
				}
				fmt.Fprintf(&text, "## %s\n\n%s\n\n", role, turn.Text)
			}
			detail.Turns = append(detail.Turns, turn)
		case stepToolRun:
			toolResult(brainRoot, id, excerpt, &s, calls, &order)
		default:
			domain.NoteSkip(ctx, "unsupported_step_"+strconv.Itoa(s.typ), relPath)
		}
	}
	for _, key := range order {
		detail.Tools = append(detail.Tools, *calls[key])
	}
	if err := ctx.Err(); err != nil {
		return domain.PipelineFragment{}, "", err
	}
	if err := snapshotReady(path); err != nil {
		return domain.PipelineFragment{}, "", err
	}

	detail.Session.StartedAt, detail.Session.EndedAt = started, ended
	detail.Session.CWD = workspacePath(ctx, db)
	if detail.Session.Model == "" {
		for _, u := range detail.Usage {
			if u.Model != "" {
				detail.Session.Model = u.Model
				break
			}
		}
	}
	detail.Reset = hasCur && !incremental
	detail.Incremental = incremental
	detail.CursorPath = relPath
	prevDigest := ""
	if incremental {
		prevDigest = cur.TailDigest
	}
	next, digestOf := nextCursor(steps, start, prevDigest, partial)
	cursorSize := info.Size()
	if partial {
		// More steps remain in this snapshot. A size of 0 never matches the file,
		// so the next fire continues from the cursor instead of calling it unchanged.
		cursorSize = 0
		domain.NoteSkip(ctx, "batch_continues", relPath)
	}
	detail.Cursor = domain.TranscriptCursor{
		FileSize: cursorSize, ModTime: info.ModTime().UTC(), ByteOffset: next,
		LineNo: len(steps), TailDigest: digestOf, SourceID: id,
	}

	created := info.ModTime().UTC()
	frag := domain.PipelineFragment{
		Source: "antigravity_db", SourceType: "transcript", SourceID: id, Title: "Antigravity conversations: " + id,
		Content: strings.TrimSpace(text.String()), CreatedAt: created,
		SourceIdentity: domain.SourceIdentity{SegmentKey: filepath.ToSlash(relPath)},
		Metadata:       map[string]any{"source_file": path, "observed_at": created.Format(time.RFC3339Nano), "format": "conversations"},
		CanonicalPath:  filepath.ToSlash(filepath.Join("fragments", "chats", "antigravity", "conversations", created.Format("2006-01-02"), id)),
		Detail:         &detail,
	}
	return frag, "", nil
}

// nextCursor returns the idx of the first step that is not consumed and the
// digest of the last consumed step. A trailing run of non-terminal steps is
// left unconsumed so it is read again.
func nextCursor(steps []stepRow, start int64, prevDigest string, partial bool) (int64, string) {
	end := len(steps)
	// The trailing run is only "the tail" when the batch reached the end of the
	// database; a partial batch ends in the middle of the log.
	for !partial && end > 0 && nonTerminal(steps[end-1].status) {
		end--
	}
	if end == 0 {
		// Nothing new is consumable: the cursor stays where it was.
		return start, prevDigest
	}
	last := steps[end-1]
	return last.idx + 1, last.digest()
}

func stepColumns(cols map[string]bool) string {
	status := "3"
	if hasColumn(cols, "status") {
		status = "status"
	}
	meta, errs := "NULL", "0"
	if hasColumn(cols, "metadata") {
		meta = "metadata"
	}
	if hasColumn(cols, "error_details") {
		errs = "CASE WHEN error_details IS NOT NULL AND length(error_details) > 0 THEN 1 ELSE 0 END"
	}
	return "idx, step_type, step_format, " + status + ", length(step_payload), CASE WHEN step_type IN (14,15) AND length(step_payload) <= " +
		strconv.Itoa(maxStepBytes) + " THEN step_payload END, " + meta + ", " + errs
}

func scanStep(rows interface{ Scan(...any) error }) (stepRow, error) {
	var s stepRow
	var meta []byte
	var hasErr int
	if err := rows.Scan(&s.idx, &s.typ, &s.format, &s.status, &s.size, &s.payload, &meta, &hasErr); err != nil {
		return stepRow{}, errors.New("cannot read Antigravity conversation")
	}
	s.hasErrors = hasErr != 0
	if len(meta) > 0 {
		s.meta, s.hasMeta = decodeMsg(meta)
	}
	return s, nil
}

func loadStep(ctx context.Context, db *sql.DB, cols map[string]bool, idx int64) (stepRow, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+stepColumns(cols)+` FROM steps WHERE idx = ?`, idx)
	if err != nil {
		return stepRow{}, false, errors.New("cannot read Antigravity conversation")
	}
	defer rows.Close()
	if !rows.Next() {
		return stepRow{}, false, nil
	}
	s, err := scanStep(rows)
	if err != nil {
		return stepRow{}, false, err
	}
	return s, true, nil
}

// loadSteps reads steps from idx `from` in bounded batches: at most
// maxBatchSteps steps or about maxBatchBytes of text payload per pass, whichever
// comes first. partial reports that more steps remain; the cursor then lets the
// next fire continue where this one stopped, so a very large conversation never
// has to be held in memory whole.
func loadSteps(ctx context.Context, db *sql.DB, cols map[string]bool, from int64) ([]stepRow, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+stepColumns(cols)+` FROM steps WHERE idx >= ? ORDER BY idx`, from)
	if err != nil {
		return nil, false, errors.New("unsupported Antigravity database schema")
	}
	defer rows.Close()
	var out []stepRow
	var bytes int
	for rows.Next() {
		if len(out) >= maxBatchSteps || bytes >= maxBatchBytes {
			return out, true, nil
		}
		s, err := scanStep(rows)
		if err != nil {
			return nil, false, err
		}
		bytes += len(s.payload)
		out = append(out, s)
	}
	if rows.Err() != nil {
		return nil, false, errors.New("cannot read Antigravity conversation")
	}
	return out, false, nil
}

// responseModels maps a planner response id to the model name recorded for it.
func responseModels(ctx context.Context, db *sql.DB) (map[string]string, error) {
	out := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT data FROM gen_metadata`)
	if err != nil {
		// A database without generation metadata still captures; models stay empty.
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			return out, nil
		}
		msg, ok := decodeMsg(raw)
		if !ok {
			continue
		}
		usage, ok := msg.sub(1, 4)
		if !ok {
			continue
		}
		respID, ok := usage.text(7)
		if !ok {
			continue
		}
		gen, _ := msg.sub(1)
		if name, ok := gen.text(19); ok && name != "" {
			out[respID] = name
		}
	}
	return out, nil
}

// planner adds the facts of a planner step: stop reason, thinking presence and
// size (never its text), tool calls and usage.
func planner(s *stepRow, turn *domain.TranscriptTurn, detail *domain.TranscriptDetail, models map[string]string,
	calls map[string]*domain.TranscriptToolCall, order *[]string) {
	var respID string
	if s.hasMeta {
		if u, ok := s.meta.sub(9); ok {
			respID, _ = u.text(7)
			usage := domain.TranscriptUsage{RequestID: respID, Model: models[respID], TS: s.created()}
			usage.Input = int64(varintOf(u, 2))
			usage.Output = int64(varintOf(u, 3))
			usage.CacheRead = int64(varintOf(u, 5))
			if respID != "" {
				detail.Usage = append(detail.Usage, usage)
			}
		}
	}
	turn.RequestID = respID
	if raw, ok := plannerBody(s); ok {
		if v, ok := raw.varint(12); ok {
			turn.StopReason = strconv.FormatUint(v, 10)
		}
		if f, ok := raw.first(3); ok && f.wire == 2 {
			turn.HasThinking, turn.ThinkingBytes = true, len(f.b)
		}
		for _, c := range raw.all(7) {
			if c.wire != 2 {
				continue
			}
			call, ok := decodeMsg(c.b)
			if !ok {
				continue
			}
			callID, _ := call.text(1)
			name, _ := call.text(2)
			if callID == "" {
				continue
			}
			input, _ := call.text(3)
			tc, seen := calls[callID]
			if !seen {
				tc = &domain.TranscriptToolCall{ToolUseID: callID}
				calls[callID] = tc
				*order = append(*order, callID)
			}
			tc.TurnUUID, tc.Name, tc.InputJSON, tc.StartedAt = turn.EventUUID, name, input, s.created()
		}
	}
}

func plannerBody(s *stepRow) (pbMsg, bool) {
	outer, ok := decodeMsg(s.payload)
	if !ok {
		return nil, false
	}
	return outer.sub(20)
}

func varintOf(m pbMsg, num int) uint64 {
	v, _ := m.varint(num)
	return v
}

// toolResult records the outcome of a tool execution step against its call.
// Status is read from the step: 3 is done, and a step with error details (status
// 4 or 7) is an error. Any other status (2, 5, 6) is not a known result and the
// call keeps status none.
func toolResult(brainRoot, id string, excerpt int, s *stepRow, calls map[string]*domain.TranscriptToolCall, order *[]string) {
	if !s.hasMeta {
		return
	}
	callID := ""
	if h, ok := s.meta.sub(4); ok {
		callID, _ = h.text(1)
	}
	if callID == "" {
		return
	}
	isErr := s.hasErrors
	known := isErr || s.status == statusOK
	tc, seen := calls[callID]
	if !seen {
		if !known {
			return
		}
		tc = &domain.TranscriptToolCall{ToolUseID: callID}
		calls[callID] = tc
		*order = append(*order, callID)
	}
	if !known {
		return
	}
	tc.HasResult, tc.IsError, tc.EndedAt = true, isErr, s.latest()
	size, text := outputFile(brainRoot, id, s.idx, excerpt)
	tc.ResultSize = size
	if excerpt > 0 {
		tc.ResultExcerpt, tc.MaxExcerpt = text, excerpt
	}
}

// outputFile returns the size, and optionally a bounded valid-UTF-8 excerpt, of
// the output a tool step wrote under brain/<conversation>/.system_generated.
func outputFile(brainRoot, id string, idx int64, excerpt int) (int, string) {
	path := filepath.Join(brainRoot, id, ".system_generated", "steps", strconv.FormatInt(idx, 10), "output.txt")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, ""
	}
	size := int(info.Size())
	if excerpt <= 0 {
		return size, ""
	}
	f, err := os.Open(path)
	if err != nil {
		return size, ""
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, int64(excerpt)))
	if err != nil {
		return size, ""
	}
	if !utf8.Valid(buf) {
		if head, ok := trimPartialRune(buf); ok {
			buf = head
		} else {
			return size, ""
		}
	}
	return size, string(buf)
}

// workspacePath returns the first workspace path recorded for the conversation.
func workspacePath(ctx context.Context, db *sql.DB) string {
	var raw []byte
	if db.QueryRowContext(ctx, `SELECT data FROM trajectory_metadata_blob LIMIT 1`).Scan(&raw) != nil {
		return ""
	}
	msg, ok := decodeMsg(raw)
	if !ok {
		return ""
	}
	for _, f := range msg.all(7) {
		if f.wire == 2 && validText(f.b) && len(f.b) > 0 && (f.b[0] == '/' || strings.HasPrefix(string(f.b), "file:")) {
			return string(f.b)
		}
	}
	return ""
}
