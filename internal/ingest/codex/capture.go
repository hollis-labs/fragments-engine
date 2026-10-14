package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const (
	tailWindow      = 4096
	maxExcerptBytes = 2048
	rawExcerptBytes = 16 << 10
)

// session_meta carries the thread's identity. A subagent names its parent under
// source.subagent.thread_spawn; a fork names it as forked_from_id.
type sessionMeta struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	Timestamp      string          `json:"timestamp"`
	CWD            string          `json:"cwd"`
	CLIVersion     string          `json:"cli_version"`
	ParentThreadID string          `json:"parent_thread_id"`
	ForkedFromID   string          `json:"forked_from_id"`
	Source         json.RawMessage `json:"source"`
}

type spawnInfo struct {
	Subagent struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
			Depth          int    `json:"depth"`
			AgentRole      string `json:"agent_role"`
		} `json:"thread_spawn"`
	} `json:"subagent"`
}

type tokenUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Total      int64 `json:"total_tokens"`
}

type textMessage struct {
	role, text, id string
	ts             time.Time
	thinking       bool
	thinkingBytes  int
}

// rolloutResult is one parse of a rollout file, whole or from a stored cursor.
type rolloutResult struct {
	meta         sessionMeta
	created      time.Time
	responses    []textMessage
	events       []textMessage
	detail       domain.TranscriptDetail
	offset       int64
	lineNo       int
	tail         []byte
	incomplete   *tailInfo // a final line still being written
	limitHit     bool
	eventsAppend bool // an incremental parse saw only legacy event messages
}

func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readWindow returns the (up to) tailWindow bytes that end at offset.
func readWindow(path string, offset int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	n := int64(tailWindow)
	if offset < n {
		n = offset
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, offset-n); err != nil {
		return nil, false
	}
	return buf, true
}

var patchPath = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

// parseRollout reads a rollout from start (a byte offset just after a complete
// line, with startLine lines before it and tail the bytes that precede it) up
// to the last complete line. A half-written final line is reported, never
// consumed, so the cursor never moves past bytes that can still change.
func parseRollout(ctx context.Context, path string, start int64, startLine int, tail []byte, maxBytes int64, excerpt int) (rolloutResult, error) {
	file, err := os.Open(path)
	if err != nil {
		class := "unreadable_file"
		if os.IsNotExist(err) {
			class = "vanished"
		}
		return rolloutResult{}, &domain.CollectionError{Class: class}
	}
	defer file.Close()
	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return rolloutResult{}, &domain.CollectionError{Class: "read_error", Offset: start}
		}
	}
	limited := &io.LimitedReader{R: file, N: maxBytes + 1}
	reader := bufio.NewReaderSize(limited, 1<<20)
	res := rolloutResult{offset: start, lineNo: startLine, tail: append([]byte(nil), tail...)}
	var retained int
	var lineStart int64
	fail := func(class string) error {
		return &domain.CollectionError{Class: class, Line: res.lineNo, Offset: lineStart}
	}

	tools := map[string]*domain.TranscriptToolCall{}
	var toolOrder []string
	toolFor := func(id string) *domain.TranscriptToolCall {
		if c, ok := tools[id]; ok {
			return c
		}
		c := &domain.TranscriptToolCall{ToolUseID: id}
		tools[id] = c
		toolOrder = append(toolOrder, id)
		return c
	}
	var records, counts []domain.TranscriptUsage
	var firstTS, lastTS time.Time
	var curModel, curEffort, lastTurn string
	var pendingThinking bool
	var pendingBytes int

	for {
		if err := ctx.Err(); err != nil {
			return rolloutResult{}, err
		}
		rawLine, readErr := readLine(reader, res.lineNo+1, res.offset)
		if readErr != nil && readErr != io.EOF {
			var ce *domain.CollectionError
			if errors.As(readErr, &ce) {
				return rolloutResult{}, readErr
			}
			return rolloutResult{}, &domain.CollectionError{Class: "read_error", Line: res.lineNo + 1, Offset: res.offset}
		}
		if len(rawLine) == 0 {
			break
		}
		terminated := bytes.HasSuffix(rawLine, []byte("\n"))
		raw := bytes.TrimSpace(rawLine)
		if !terminated && len(raw) > 0 && !json.Valid(raw) {
			// No trailing newline and not valid JSON: the writer was still
			// appending when this copy was made. Stop before it.
			res.incomplete = &tailInfo{line: res.lineNo + 1, offset: res.offset}
			break
		}
		res.lineNo++
		lineStart = res.offset
		res.offset += int64(len(rawLine))
		res.tail = append(res.tail, rawLine...)
		if len(res.tail) > tailWindow {
			res.tail = append([]byte(nil), res.tail[len(res.tail)-tailWindow:]...)
		}
		if len(raw) == 0 {
			if readErr == io.EOF {
				break
			}
			continue
		}
		var record line
		// Never include a decoder error or raw record in diagnostics.
		if json.Unmarshal(raw, &record) != nil || record.Type == "" {
			return rolloutResult{}, fail("invalid_record")
		}
		ts, haveTS := time.Time{}, false
		if t, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
			ts, haveTS = t.UTC(), true
			if res.created.IsZero() || ts.Before(res.created) {
				res.created = ts
			}
			if firstTS.IsZero() || ts.Before(firstTS) {
				firstTS = ts
			}
			if ts.After(lastTS) {
				lastTS = ts
			}
		}
		lineID := fmt.Sprintf("L%d", res.lineNo)
		switch record.Type {
		case "session_meta":
			var next sessionMeta
			if json.Unmarshal(record.Payload, &next) != nil || next.ID == "" {
				return rolloutResult{}, fail("invalid_session_meta")
			}
			if res.meta.ID != "" && res.meta.ID != next.ID {
				// A forked rollout replays its parent's session_meta after its own.
				// The first record names this file's thread; a later record for
				// another thread is ancestry, never a competing identity.
				if res.meta.ParentThreadID == "" {
					res.meta.ParentThreadID = next.ID
				}
				continue
			}
			if next.ParentThreadID == "" {
				next.ParentThreadID = next.ForkedFromID
			}
			res.meta = next
			if t, err := time.Parse(time.RFC3339Nano, next.Timestamp); err == nil {
				res.created = t
			}
		case "turn_context":
			var tc struct {
				Model  string `json:"model"`
				Effort string `json:"effort"`
				CWD    string `json:"cwd"`
			}
			if json.Unmarshal(record.Payload, &tc) == nil {
				if tc.Model != "" {
					curModel = tc.Model
					if res.detail.Session.Model == "" {
						res.detail.Session.Model = tc.Model
					}
				}
				if tc.Effort != "" {
					curEffort = tc.Effort
				}
				if res.detail.Session.CWD == "" {
					res.detail.Session.CWD = tc.CWD
				}
			}
		case "token_usage_record":
			// One record per model response: exact, so preferred over token_count.
			var rec struct {
				ResponseID string     `json:"response_id"`
				Usage      tokenUsage `json:"usage"`
			}
			if json.Unmarshal(record.Payload, &rec) == nil && rec.Usage.Total > 0 {
				id := rec.ResponseID
				if id == "" {
					id = lineID
				}
				records = append(records, usageRow(id, curModel, curEffort, ts, rec.Usage))
			}
		case "response_item":
			var msg message
			if json.Unmarshal(record.Payload, &msg) != nil {
				return rolloutResult{}, fail("invalid_response_item")
			}
			// The tool fields are best effort: a shape this reader does not know
			// costs that call its detail, never the file its place in the run.
			var x struct {
				CallID    string          `json:"call_id"`
				Name      string          `json:"name"`
				Namespace string          `json:"namespace"`
				Arguments json.RawMessage `json:"arguments"`
				Input     json.RawMessage `json:"input"`
				Output    json.RawMessage `json:"output"`
				Summary   json.RawMessage `json:"summary"`
				Action    json.RawMessage `json:"action"`
			}
			_ = json.Unmarshal(record.Payload, &x)
			switch msg.Type {
			case "message":
				if msg.Role != "user" && msg.Role != "assistant" {
					continue
				}
				var parts []string
				for _, block := range msg.Content {
					if (block.Type == "input_text" || block.Type == "output_text") && strings.TrimSpace(block.Text) != "" {
						parts = append(parts, block.Text)
					}
				}
				if len(parts) == 0 {
					continue
				}
				// Edge whitespace is dropped, as the searchable record's turn
				// splitter drops it: enrichment digests are over this text.
				text := strings.TrimSpace(strings.Join(parts, "\n"))
				if retained += len(text); retained > maxRetainedText {
					return rolloutResult{}, fail("size_limit")
				}
				m := textMessage{role: msg.Role, text: text, id: lineID, ts: ts}
				if msg.Role == "assistant" && pendingThinking {
					m.thinking, m.thinkingBytes = true, pendingBytes
					pendingThinking, pendingBytes = false, 0
				}
				res.responses = append(res.responses, m)
				lastTurn = lineID
			case "reasoning":
				// The reasoning text is never kept: only that it happened, and how
				// much readable summary there was.
				pendingThinking = true
				pendingBytes += summaryBytes(x.Summary)
			case "function_call", "custom_tool_call", "tool_search_call", "web_search_call":
				id := x.CallID
				if id == "" {
					id = lineID
				}
				call := toolFor(id)
				call.TurnUUID, call.StartedAt = lastTurn, ts
				call.Name = toolName(msg.Type, x.Namespace, x.Name)
				call.InputJSON = toolInput(msg.Type, x.Name, rawText(x.Arguments), rawText(x.Input), x.Action)
			case "function_call_output", "custom_tool_call_output", "tool_search_output":
				if x.CallID == "" {
					continue
				}
				call := toolFor(x.CallID)
				text := outputText(x.Output)
				size := len(text)
				if size > call.ResultSize {
					call.ResultSize = size
				}
				call.HasResult = true
				if outputFailed(text) {
					call.IsError = true
				}
				if call.EndedAt.IsZero() && haveTS {
					call.EndedAt = ts
				}
				setExcerpt(call, text, excerpt)
			}
		case "event_msg":
			var msg message
			if json.Unmarshal(record.Payload, &msg) != nil {
				return rolloutResult{}, fail("invalid_event")
			}
			var ev struct {
				CallID   string          `json:"call_id"`
				ExitCode *int            `json:"exit_code"`
				Status   string          `json:"status"`
				Success  *bool           `json:"success"`
				Output   string          `json:"aggregated_output"`
				Stdout   string          `json:"stdout"`
				Result   json.RawMessage `json:"result"`
				Info     *struct {
					Total tokenUsage `json:"total_token_usage"`
					Last  tokenUsage `json:"last_token_usage"`
				} `json:"info"`
			}
			_ = json.Unmarshal(record.Payload, &ev)
			switch msg.Type {
			case "user_message", "agent_message":
				role := "user"
				if msg.Type == "agent_message" {
					role = "assistant"
				}
				if strings.TrimSpace(msg.Message) == "" {
					continue
				}
				msg.Message = strings.TrimSpace(msg.Message)
				if retained += len(msg.Message); retained > maxRetainedText {
					return rolloutResult{}, fail("size_limit")
				}
				res.events = append(res.events, textMessage{role: role, text: msg.Message, id: lineID, ts: ts})
			case "token_count":
				// The same count is emitted again whenever the session is idle, so a
				// line is a usage row only when the running total moved. The total is
				// the row's id: a repeat, in this parse or a later one, merges.
				if ev.Info != nil && ev.Info.Total.Total > 0 && ev.Info.Last.Total > 0 {
					counts = append(counts, usageRow(fmt.Sprintf("tc-%d", ev.Info.Total.Total), curModel, curEffort, ts, ev.Info.Last))
				}
			case "exec_command_end", "patch_apply_end", "mcp_tool_call_end":
				if ev.CallID == "" {
					continue
				}
				call := toolFor(ev.CallID)
				call.HasResult = true
				if haveTS {
					call.EndedAt = ts
				}
				out := ev.Output
				if out == "" {
					out = ev.Stdout
				}
				if len(out) > call.ResultSize {
					call.ResultSize = len(out)
				}
				if hasErrKey(ev.Result) {
					call.IsError = true
				}
				if (ev.ExitCode != nil && *ev.ExitCode != 0) || (ev.Success != nil && !*ev.Success) || ev.Status == "failed" || ev.Status == "error" {
					call.IsError = true
				}
				setExcerpt(call, out, excerpt)
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if limited.N == 0 {
		return rolloutResult{}, &domain.CollectionError{Class: "size_limit", Offset: maxBytes}
	}
	// Per-response records beat the repeated running counts; never both.
	usage := records
	if len(usage) == 0 {
		usage = counts
	}
	res.detail.Usage = usage
	for _, id := range toolOrder {
		res.detail.Tools = append(res.detail.Tools, *tools[id])
	}
	res.detail.Session.StartedAt, res.detail.Session.EndedAt = firstTS, lastTS
	return res, nil
}

func usageRow(id, model, effort string, ts time.Time, u tokenUsage) domain.TranscriptUsage {
	// Codex counts cached tokens inside input_tokens; Claude reports them apart.
	// Splitting them here keeps Input + CacheRead + CacheCreation the prompt size
	// for both tools. Output includes reasoning tokens.
	in := u.Input - u.Cached - u.CacheWrite
	if in < 0 {
		in = 0
	}
	return domain.TranscriptUsage{RequestID: id, Model: model, TS: ts, Input: in, Output: u.Output,
		CacheRead: u.Cached, CacheCreation: u.CacheWrite, Effort: effort}
}

func summaryBytes(raw json.RawMessage) int {
	var items []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return 0
	}
	n := 0
	for _, it := range items {
		n += len(it.Text)
	}
	return n
}

func toolName(typ, namespace, name string) string {
	switch typ {
	case "web_search_call":
		return "web_search"
	case "tool_search_call":
		return "tool_search"
	}
	if namespace != "" && name != "" {
		return namespace + "." + name
	}
	return name
}

// toolInput returns the call's input as JSON. A patch is reduced to the files it
// names and its size: the patch text is source code the store has no use for.
func toolInput(typ, name, arguments, input string, action json.RawMessage) string {
	switch {
	case typ == "web_search_call":
		var a struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(action, &a) == nil && a.Query != "" {
			out, _ := json.Marshal(map[string]string{"query": a.Query})
			return string(out)
		}
		return ""
	case typ == "custom_tool_call" && name == "apply_patch":
		var paths []string
		seen := map[string]bool{}
		for _, m := range patchPath.FindAllStringSubmatch(input, -1) {
			if p := strings.TrimSpace(m[1]); p != "" && !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
		out, _ := json.Marshal(map[string]any{"paths": paths, "patch_bytes": len(input)})
		return string(out)
	case typ == "custom_tool_call":
		out, _ := json.Marshal(map[string]string{"input": input})
		return string(out)
	}
	if json.Valid([]byte(arguments)) && strings.HasPrefix(strings.TrimSpace(arguments), "{") {
		return arguments
	}
	if strings.TrimSpace(arguments) == "" {
		return ""
	}
	out, _ := json.Marshal(map[string]string{"arguments": arguments})
	return string(out)
}

// outputText returns a tool output as text. Older rollouts wrap it as
// {"output": "...", "metadata": {...}}; newer ones store the text directly.
func outputText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if raw[0] == '"' {
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var wrapped struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Output != "" {
		return wrapped.Output
	}
	return string(raw)
}

var exitLine = regexp.MustCompile(`(?m)^Process exited with code (-?\d+)`)

// outputFailed reads an exit status where the output states one. It is a hint:
// the structured exec/patch end events override it.
func outputFailed(text string) bool {
	if len(text) > 4096 {
		text = text[:4096]
	}
	m := exitLine.FindStringSubmatch(text)
	return m != nil && m[1] != "0"
}

func setExcerpt(call *domain.TranscriptToolCall, text string, excerpt int) {
	if excerpt <= 0 || text == "" {
		return
	}
	if len(text) > rawExcerptBytes {
		text = text[:rawExcerptBytes]
	}
	call.ResultExcerpt, call.MaxExcerpt = text, excerpt
}

// collectSegment parses one rollout, from its stored cursor when that is still
// valid. It returns a skip class instead of a fragment when nothing is new.
func collectSegment(ctx context.Context, path, rel string, info os.FileInfo, maxBytes int64, excerpt int) (domain.PipelineFragment, string, *tailInfo, error) {
	if excerpt > maxExcerptBytes {
		excerpt = maxExcerptBytes
	}
	cur, hasCur := domain.LookupCursor(ctx, rel)
	incremental := false
	var start int64
	var startLine int
	var window []byte
	if hasCur && cur.ByteOffset > 0 && cur.ByteOffset <= info.Size() {
		if w, ok := readWindow(path, cur.ByteOffset); ok && digestHex(w) == cur.TailDigest {
			incremental, start, startLine, window = true, cur.ByteOffset, cur.LineNo, w
		}
	}
	if incremental && info.Size() == cur.ByteOffset {
		return domain.PipelineFragment{}, "unchanged", nil, nil
	}
	res, err := parseRollout(ctx, path, start, startLine, window, maxBytes, excerpt)
	if err != nil {
		return domain.PipelineFragment{}, "", nil, err
	}
	// Legacy rollouts carry messages only as event_msg records. Which of the two
	// message streams a file uses is settled over the whole file, so an append
	// that has events but no response messages is re-read from the start.
	if incremental && len(res.responses) == 0 && len(res.events) > 0 {
		incremental, start, startLine, window = false, 0, 0, nil
		if res, err = parseRollout(ctx, path, 0, 0, nil, maxBytes, excerpt); err != nil {
			return domain.PipelineFragment{}, "", nil, err
		}
	}
	if res.offset == start && res.incomplete != nil && !incremental {
		// A brand-new file whose first line is still being written has nothing
		// to import yet.
		return domain.PipelineFragment{}, "empty", res.incomplete, nil
	}
	if res.offset == start {
		if incremental {
			return domain.PipelineFragment{}, "unchanged", res.incomplete, nil
		}
		return domain.PipelineFragment{}, "empty", res.incomplete, nil
	}
	sessionID := res.meta.ID
	if incremental && cur.SourceID != "" {
		sessionID = cur.SourceID
	}
	if sessionID == "" {
		if res.incomplete != nil {
			return domain.PipelineFragment{}, "empty", res.incomplete, nil
		}
		return domain.PipelineFragment{}, "", nil, &domain.CollectionError{Class: "missing_session_meta"}
	}
	// Legacy event messages duplicate response items; prefer response material.
	messages := res.responses
	if len(messages) == 0 {
		messages = res.events
	}
	var parts []string
	for _, msg := range messages {
		parts = append(parts, "## "+msg.role+"\n\n"+msg.text)
		res.detail.Turns = append(res.detail.Turns, domain.TranscriptTurn{
			EventUUID: msg.id, Role: msg.role, TS: msg.ts, Text: msg.text,
			HasThinking: msg.thinking, ThinkingBytes: msg.thinkingBytes,
		})
	}
	created := res.created
	if created.IsZero() {
		created = info.ModTime()
	}
	created = created.UTC()
	if len(parts) == 0 && !incremental {
		return domain.PipelineFragment{}, "empty", res.incomplete, nil
	}
	fragment := domain.PipelineFragment{
		Source: "codex", SourceType: "transcript", SourceID: sessionID, Title: "Codex session: " + sessionID,
		Content: strings.Join(parts, "\n\n"), CreatedAt: created,
		Metadata:      map[string]any{"thread_id": sessionID, "session_id": res.meta.SessionID, "cwd": res.meta.CWD, "cli_version": res.meta.CLIVersion, "parent_thread_id": res.meta.ParentThreadID, "source_file": path},
		CanonicalPath: filepath.ToSlash(filepath.Join("fragments", "chats", "codex", created.Format("2006-01-02"), sessionID)),
	}
	fragment.SourceIdentity.SegmentKey = "rollout/" + rel
	session := res.detail.Session
	session.Tool, session.NativeSessionID, session.Kind = "codex", sessionID, "main"
	meta := res.meta
	if incremental {
		// The header sits in the part stored earlier; the store keeps identity
		// fields as last written, so they are re-read rather than blanked.
		meta = readHeader(path)
	}
	applyMeta(&session, meta)
	res.detail.Session = session
	res.detail.Reset = hasCur && !incremental
	res.detail.Incremental = incremental
	res.detail.CursorPath = rel
	res.detail.Cursor = domain.TranscriptCursor{
		FileSize: info.Size(), ModTime: info.ModTime().UTC(), ByteOffset: res.offset,
		LineNo: res.lineNo, TailDigest: digestHex(res.tail), SourceID: sessionID,
	}
	fragment.Detail = &res.detail
	return fragment, "", res.incomplete, nil
}

// readHeader returns the thread's own session_meta from the top of the file.
func readHeader(path string) sessionMeta {
	f, err := os.Open(path)
	if err != nil {
		return sessionMeta{}
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 1<<20)
	var meta sessionMeta
	for i := 0; i < 8; i++ {
		raw, err := readLine(reader, i+1, 0)
		if len(raw) == 0 || (err != nil && err != io.EOF) {
			break
		}
		var rec line
		if json.Unmarshal(bytes.TrimSpace(raw), &rec) == nil && rec.Type == "session_meta" {
			var next sessionMeta
			if json.Unmarshal(rec.Payload, &next) == nil && next.ID != "" {
				if meta.ID == "" {
					meta = next
				} else if meta.ParentThreadID == "" {
					meta.ParentThreadID = next.ID
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	if meta.ParentThreadID == "" {
		meta.ParentThreadID = meta.ForkedFromID
	}
	return meta
}

// applyMeta fills the session's identity from its session_meta.
func applyMeta(s *domain.TranscriptSession, m sessionMeta) {
	s.CWD = m.CWD
	s.ParentNativeID = m.ParentThreadID
	s.IsFork = m.ForkedFromID != ""
	s.Entrypoint = "cli"
	var str string
	if json.Unmarshal(m.Source, &str) == nil && str != "" {
		s.Entrypoint = str
	}
	var spawn spawnInfo
	if len(m.Source) > 0 && json.Unmarshal(m.Source, &spawn) == nil && spawn.Subagent.ThreadSpawn.ParentThreadID != "" {
		sp := spawn.Subagent.ThreadSpawn
		s.Kind, s.Entrypoint = "subagent", "subagent"
		s.AgentType, s.SpawnDepth = sp.AgentRole, sp.Depth
		if s.ParentNativeID == "" {
			s.ParentNativeID = sp.ParentThreadID
		}
	}
}

// hasErrKey reports whether an MCP tool result is the error variant.
func hasErrKey(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, ok := m["Err"]
	return ok
}

// rawText returns a JSON string's text, or the raw JSON for any other shape.
func rawText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}
