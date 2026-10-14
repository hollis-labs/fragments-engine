package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type event struct {
	Type          string          `json:"type"`
	Timestamp     string          `json:"timestamp"`
	SessionID     string          `json:"sessionId"`
	Slug          string          `json:"slug"`
	CWD           string          `json:"cwd"`
	UUID          string          `json:"uuid"`
	ParentUUID    string          `json:"parentUuid"`
	GitBranch     string          `json:"gitBranch"`
	Entrypoint    string          `json:"entrypoint"`
	RequestID     string          `json:"requestId"`
	IsSidechain   bool            `json:"isSidechain"`
	Effort        json.RawMessage `json:"effort"`
	PerTurnEffort json.RawMessage `json:"perTurnEffort"`
	Message       json.RawMessage `json:"message"`
}

type messageBody struct {
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	Usage      *struct {
		Input         int64 `json:"input_tokens"`
		Output        int64 `json:"output_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type segmentResult struct {
	sessionID, slug, cwd string
	createdAt            time.Time
	messages             []string
	detail               domain.TranscriptDetail
	offset               int64
	lineNo               int
	tail                 []byte
	malformed            int
}

// collectSegment parses one session file, from its stored cursor when that is
// still valid. It returns a skip class instead of a fragment when there is
// nothing new to store.
func collectSegment(ctx context.Context, root, path, relPath string, info os.FileInfo, excerpt int) (domain.PipelineFragment, string, error) {
	cur, hasCur := domain.LookupCursor(ctx, relPath)
	var start int64
	var startLine int
	var tail []byte
	incremental := false
	if hasCur && cur.ByteOffset > 0 && cur.ByteOffset <= info.Size() {
		if window, ok := readWindow(path, cur.ByteOffset); ok && digestHex(window) == cur.TailDigest {
			incremental, start, startLine, tail = true, cur.ByteOffset, cur.LineNo, window
		}
	}
	if incremental && info.Size() == cur.ByteOffset {
		return domain.PipelineFragment{}, "unchanged", nil
	}
	res, err := parseSegment(ctx, path, start, startLine, tail, excerpt)
	if err != nil {
		return domain.PipelineFragment{}, "", err
	}
	if res.malformed > 0 {
		domain.NoteSkip(ctx, "malformed_line", relPath)
	}
	if res.offset == start {
		if incremental {
			return domain.PipelineFragment{}, "unchanged", nil
		}
		return domain.PipelineFragment{}, "empty", nil
	}

	sessionID := res.sessionID
	if incremental && cur.SourceID != "" {
		sessionID = cur.SourceID
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	slug := res.slug
	if slug == "" {
		slug = sessionID
	}
	createdAt := res.createdAt
	if createdAt.IsZero() {
		createdAt = info.ModTime().UTC()
	}
	meta := map[string]any{
		"session_id":  sessionID,
		"slug":        slug,
		"cwd":         res.cwd,
		"source_file": path,
	}
	fragment := domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      sessionID,
		Title:         "Claude session: " + slug,
		Content:       strings.Join(res.messages, "\n\n"),
		CreatedAt:     createdAt.UTC(),
		Metadata:      meta,
		CanonicalPath: filepath.ToSlash(filepath.Join("fragments", "chats", "claude", createdAt.Format("2006-01-02"), sessionID)),
	}
	session := res.detail.Session
	session.Tool, session.NativeSessionID, session.Kind = "claude", sessionID, "main"
	if segment := subagentSegment(root, path); segment != "" {
		fragment.SourceIdentity.SegmentKey = segment
		meta["subagent_segment"] = segment
		fragment.CanonicalPath += "/" + segment
		session.Kind = "subagent"
		session.ParentNativeID = parentSession(root, path)
		applySubagentMeta(path, &session)
	}
	res.detail.Session = session
	res.detail.Reset = hasCur && !incremental
	res.detail.Incremental = incremental
	res.detail.CursorPath = relPath
	res.detail.Cursor = domain.TranscriptCursor{
		FileSize: info.Size(), ModTime: info.ModTime().UTC(), ByteOffset: res.offset,
		LineNo: res.lineNo, TailDigest: digestHex(res.tail), SourceID: sessionID,
	}
	fragment.Detail = &res.detail
	return fragment, "", nil
}

func parentSession(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 0 && parts[0] == "projects" {
		parts = parts[1:]
	}
	if len(parts) < 4 {
		return ""
	}
	return parts[1]
}

// applySubagentMeta reads the small <agent>.meta.json beside a subagent file.
func applySubagentMeta(path string, session *domain.TranscriptSession) {
	metaPath := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
	info, err := os.Lstat(metaPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return
	}
	var meta struct {
		AgentType  string `json:"agentType"`
		ToolUseID  string `json:"toolUseId"`
		SpawnDepth int    `json:"spawnDepth"`
		IsFork     bool   `json:"isFork"`
		Model      string `json:"model"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return
	}
	session.AgentType, session.SpawnToolUseID = meta.AgentType, meta.ToolUseID
	session.SpawnDepth, session.IsFork = meta.SpawnDepth, meta.IsFork
	if meta.Model != "" {
		session.Model = meta.Model
	}
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

// readLine returns one line including its newline. A line over maxLineBytes is
// an unreadable_line failure carrying only its position.
func readLine(r *bufio.Reader, lineNo int, offset int64) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLineBytes {
			return nil, &domain.CollectionError{Class: "unreadable_line", Line: lineNo, Offset: offset}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, err
	}
}

func parseSegment(ctx context.Context, path string, start int64, startLine int, tail []byte, excerpt int) (segmentResult, error) {
	file, err := os.Open(path)
	if err != nil {
		class := "unreadable_file"
		if os.IsNotExist(err) {
			class = "vanished"
		}
		return segmentResult{}, &domain.CollectionError{Class: class}
	}
	defer file.Close()
	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return segmentResult{}, &domain.CollectionError{Class: "read_error", Offset: start}
		}
	}
	res := segmentResult{offset: start, lineNo: startLine, tail: append([]byte(nil), tail...)}
	reader := bufio.NewReaderSize(file, 1<<20)

	tools := map[string]*domain.TranscriptToolCall{}
	var toolOrder []string
	usage := map[string]*domain.TranscriptUsage{}
	var usageOrder []string
	var firstTS, lastTS time.Time

	for {
		if err := ctx.Err(); err != nil {
			return segmentResult{}, err
		}
		line, readErr := readLine(reader, res.lineNo+1, res.offset)
		if readErr != nil && readErr != io.EOF {
			if _, ok := readErr.(*domain.CollectionError); ok {
				return segmentResult{}, readErr
			}
			return segmentResult{}, &domain.CollectionError{Class: "read_error", Line: res.lineNo + 1, Offset: res.offset}
		}
		if len(line) == 0 {
			break
		}
		if !bytes.HasSuffix(line, []byte("\n")) {
			// An unterminated final line is consumed only if it is already whole;
			// a half-synced line waits for the next fire.
			var probe map[string]json.RawMessage
			if json.Unmarshal(line, &probe) != nil {
				break
			}
		}
		res.lineNo++
		res.offset += int64(len(line))
		res.tail = append(res.tail, line...)
		if len(res.tail) > tailWindow {
			res.tail = append([]byte(nil), res.tail[len(res.tail)-tailWindow:]...)
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if readErr == io.EOF {
				break
			}
			continue
		}
		var ev event
		if json.Unmarshal(trimmed, &ev) != nil {
			res.malformed++
			if readErr == io.EOF {
				break
			}
			continue
		}
		if res.sessionID == "" {
			res.sessionID = ev.SessionID
		}
		if res.slug == "" {
			res.slug = ev.Slug
		}
		if res.cwd == "" {
			res.cwd = ev.CWD
		}
		ts, haveTS := parseEventTime(ev.Timestamp)
		if haveTS {
			if res.createdAt.IsZero() || ts.Before(res.createdAt) {
				res.createdAt = ts
			}
			if firstTS.IsZero() || ts.Before(firstTS) {
				firstTS = ts
			}
			if ts.After(lastTS) {
				lastTS = ts
			}
		}
		s := &res.detail.Session
		if s.GitBranch == "" {
			s.GitBranch = ev.GitBranch
		}
		if s.Entrypoint == "" {
			s.Entrypoint = ev.Entrypoint
		}
		if s.CWD == "" {
			s.CWD = ev.CWD
		}

		if (ev.Type == "user" || ev.Type == "assistant") && len(ev.Message) > 0 {
			var msg messageBody
			if json.Unmarshal(ev.Message, &msg) != nil {
				msg = messageBody{}
			}
			uuid := ev.UUID
			if uuid == "" {
				uuid = fmt.Sprintf("line:%d", res.lineNo)
			}
			text, thinking, blocks := splitContent(msg.Content)
			turn := domain.TranscriptTurn{
				EventUUID: uuid, ParentUUID: ev.ParentUUID, Role: ev.Type, TS: ts, RequestID: ev.RequestID,
				IsSidechain: ev.IsSidechain, StopReason: msg.StopReason, Text: text,
				HasThinking: thinking > 0, ThinkingBytes: thinking,
			}
			res.detail.Turns = append(res.detail.Turns, turn)
			if text != "" {
				res.messages = append(res.messages, "## "+ev.Type+"\n\n"+text)
			}
			if ev.Type == "assistant" {
				if s.Model == "" {
					s.Model = msg.Model
				}
				if msg.Usage != nil {
					key := ev.RequestID
					if key == "" {
						key = "uuid:" + uuid
					}
					u := usage[key]
					if u == nil {
						u = &domain.TranscriptUsage{RequestID: key, TS: ts, Effort: effortOf(ev)}
						usage[key] = u
						usageOrder = append(usageOrder, key)
					}
					u.Model = msg.Model
					u.Input = max64(u.Input, msg.Usage.Input)
					u.Output = max64(u.Output, msg.Usage.Output)
					u.CacheRead = max64(u.CacheRead, msg.Usage.CacheRead)
					u.CacheCreation = max64(u.CacheCreation, msg.Usage.CacheCreation)
				}
			}
			for _, b := range blocks {
				switch b.Type {
				case "tool_use":
					if b.ID == "" {
						continue
					}
					call := toolFor(tools, &toolOrder, b.ID)
					call.TurnUUID, call.Name, call.InputJSON, call.StartedAt = uuid, b.Name, string(b.Input), ts
				case "tool_result":
					if b.ToolUseID == "" {
						continue
					}
					call := toolFor(tools, &toolOrder, b.ToolUseID)
					size, ex := resultText(b.Content, excerpt)
					call.HasResult, call.IsError, call.ResultSize, call.EndedAt = true, b.IsError, size, ts
					call.ResultExcerpt, call.MaxExcerpt = ex, excerpt
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	res.detail.Session.StartedAt, res.detail.Session.EndedAt = firstTS, lastTS
	for _, id := range toolOrder {
		res.detail.Tools = append(res.detail.Tools, *tools[id])
	}
	for _, id := range usageOrder {
		res.detail.Usage = append(res.detail.Usage, *usage[id])
	}
	return res, nil
}

func toolFor(m map[string]*domain.TranscriptToolCall, order *[]string, id string) *domain.TranscriptToolCall {
	if c, ok := m[id]; ok {
		return c
	}
	c := &domain.TranscriptToolCall{ToolUseID: id}
	m[id] = c
	*order = append(*order, id)
	return c
}

func max64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

// splitContent returns the searchable text, the byte length of any thinking
// (the thinking itself is dropped here), and the structured blocks.
func splitContent(raw json.RawMessage) (string, int, []block) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", 0, nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", 0, nil
		}
		return strings.TrimSpace(s), 0, nil
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return "", 0, nil
	}
	var parts []string
	thinking := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		case "thinking":
			thinking += len(b.Thinking)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), thinking, blocks
}

// resultText measures a tool result's text and, when excerpts are enabled,
// returns a bounded raw excerpt for the store to redact and truncate.
func resultText(raw json.RawMessage, excerpt int) (int, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, ""
	}
	var text string
	if raw[0] == '"' {
		_ = json.Unmarshal(raw, &text)
	} else {
		var blocks []block
		if json.Unmarshal(raw, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" {
					parts = append(parts, b.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
	}
	if excerpt <= 0 {
		return len(text), ""
	}
	if len(text) > rawExcerptBytes {
		return len(text), text[:rawExcerptBytes]
	}
	return len(text), text
}

func effortOf(ev event) string {
	for _, raw := range []json.RawMessage{ev.PerTurnEffort, ev.Effort} {
		var s string
		if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

func parseEventTime(raw string) (time.Time, bool) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err == nil {
		return ts.UTC(), true
	}
	return time.Time{}, false
}
