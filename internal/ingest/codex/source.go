// Package codex reads Codex rollout JSONL files without changing the archive.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const kind = "codex_sessions"

// maxRetainedText bounds the message text kept from one rollout. It matches the
// private store's per-record cap, which is the real limit: rollouts are mostly
// tool output and images, so raw file size says little about what is kept.
var maxRetainedText = 16 << 20 // var so tests can lower it

// defaultMaxFileBytes is a hard ceiling on a rollout read, not a content rule.
const defaultMaxFileBytes = 1 << 30

type Source struct{}

func (Source) Kind() string { return kind }

func (Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	rules, err := config.DecodeRules[config.CodexSessionRules](cfg)
	if err != nil {
		return nil, err
	}
	maxBytes := int64(defaultMaxFileBytes)
	if rules.MaxFileSizeMB > 0 {
		maxBytes = int64(rules.MaxFileSizeMB) * 1024 * 1024
	}
	root := config.ExpandHome(cfg.Source.Root)
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("codex source root is required")
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			// One unreadable or just-vanished directory (rsync is writing here)
			// must not abort the whole source.
			if rel, err := filepath.Rel(root, path); err == nil {
				domain.NoteSkip(ctx, "unreadable_dir", filepath.ToSlash(rel))
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && entry.Name() == ".rsync-partial" {
			return filepath.SkipDir
		}
		if entry.Type().IsRegular() && filepath.Ext(path) == ".jsonl" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerate codex sessions: %w", err)
	}
	sort.Strings(paths)
	var out []domain.PipelineFragment
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("codex session locator: %w", err)
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(path)
		if err != nil {
			// Gone between enumeration and now, or unreadable: this file only.
			class := "unreadable_file"
			if os.IsNotExist(err) {
				class = "vanished"
			}
			domain.NoteSkip(ctx, class, rel)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > maxBytes {
			domain.NoteSkip(ctx, "size_limit", rel)
			continue
		}
		fragment, tail, err := parseSession(ctx, path, info.ModTime(), maxBytes)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			// A bad record, an unreadable line or an I/O problem costs this file
			// its place in this run, never the other files: it is counted with
			// its class, path hash, line and byte offset.
			var diag *domain.CollectionError
			if errors.As(err, &diag) {
				domain.NoteSkipAt(ctx, diag.Class, rel, diag.Line, diag.Offset)
			} else {
				domain.NoteSkip(ctx, "unreadable_file", rel)
			}
			continue
		}
		if tail != nil {
			// The final line was still being written when the file was copied.
			// Everything before it is imported; the file is picked up again when
			// its bytes change.
			domain.NoteSkipAt(ctx, "incomplete_tail", rel, tail.line, tail.offset)
		}
		if fragment.Content == "" {
			continue
		}
		// A thread can have multiple physical rollout files (fork/revert histories).
		// Keep each file distinct without treating its metadata as authority.
		fragment.SourceIdentity.SegmentKey = "rollout/" + filepath.ToSlash(rel)
		out = append(out, fragment)
	}
	return out, nil
}

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}
type sessionMeta struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	Timestamp      string `json:"timestamp"`
	CWD            string `json:"cwd"`
	CLIVersion     string `json:"cli_version"`
	ParentThreadID string `json:"parent_thread_id"`
	ForkedFromID   string `json:"forked_from_id"`
}
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type message struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
	Message string         `json:"message"`
}
type textMessage struct{ role, text string }

// tailInfo marks a final line that was cut off mid-write.
type tailInfo struct {
	line   int
	offset int64
}

// readLine returns one line including its newline, if it has one. A line over
// the cap is an unreadable_line failure that carries only its position.
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

const maxLineBytes = 10 << 20

func parseSession(ctx context.Context, path string, modTime time.Time, maxBytes int64) (domain.PipelineFragment, *tailInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		class := "unreadable_file"
		if os.IsNotExist(err) {
			class = "vanished"
		}
		return domain.PipelineFragment{}, nil, &domain.CollectionError{Class: class}
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxBytes + 1}
	reader := bufio.NewReaderSize(limited, 1<<20)
	var meta sessionMeta
	var responses, events []textMessage
	var created time.Time
	lineNo := 0
	var retained int
	var tail *tailInfo
	var lineStart, next int64 // byte offsets: current line start, following line start
	fail := func(class string) error {
		return &domain.CollectionError{Class: class, Line: lineNo, Offset: lineStart}
	}
	for {
		if err := ctx.Err(); err != nil {
			return domain.PipelineFragment{}, nil, err
		}
		rawLine, readErr := readLine(reader, lineNo+1, next)
		if readErr != nil && readErr != io.EOF {
			var ce *domain.CollectionError
			if errors.As(readErr, &ce) {
				return domain.PipelineFragment{}, nil, readErr
			}
			return domain.PipelineFragment{}, nil, &domain.CollectionError{Class: "read_error", Line: lineNo + 1, Offset: next}
		}
		if len(rawLine) == 0 {
			break
		}
		lineNo++
		lineStart = next
		next += int64(len(rawLine))
		terminated := bytes.HasSuffix(rawLine, []byte("\n"))
		raw := bytes.TrimSpace(rawLine)
		if len(raw) == 0 {
			if readErr == io.EOF {
				break
			}
			continue
		}
		if !terminated && !json.Valid(raw) {
			// No trailing newline and not valid JSON: the writer was still
			// appending when this copy was made. This is the only line that can
			// be in that state; stop here and keep what is complete.
			tail = &tailInfo{line: lineNo, offset: lineStart}
			break
		}
		var record line
		// Never include a decoder error or raw record in diagnostics.
		if json.Unmarshal(raw, &record) != nil || record.Type == "" {
			return domain.PipelineFragment{}, nil, fail("invalid_record")
		}
		if ts, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil && (created.IsZero() || ts.Before(created)) {
			created = ts
		}
		switch record.Type {
		case "session_meta":
			var next sessionMeta
			if json.Unmarshal(record.Payload, &next) != nil || next.ID == "" {
				return domain.PipelineFragment{}, nil, fail("invalid_session_meta")
			}
			if meta.ID != "" && meta.ID != next.ID {
				// A forked rollout replays its parent's session_meta after its own.
				// The first record names this file's thread; a later record for
				// another thread is ancestry, never a competing identity.
				if meta.ParentThreadID == "" {
					meta.ParentThreadID = next.ID
				}
				continue
			}
			if next.ParentThreadID == "" {
				next.ParentThreadID = next.ForkedFromID
			}
			meta = next
			if ts, err := time.Parse(time.RFC3339Nano, meta.Timestamp); err == nil {
				created = ts
			}
		case "response_item":
			var msg message
			if json.Unmarshal(record.Payload, &msg) != nil {
				return domain.PipelineFragment{}, nil, fail("invalid_response_item")
			}
			if msg.Type != "message" || (msg.Role != "user" && msg.Role != "assistant") {
				continue
			}
			var parts []string
			for _, block := range msg.Content {
				if (block.Type == "input_text" || block.Type == "output_text") && strings.TrimSpace(block.Text) != "" {
					parts = append(parts, block.Text)
				}
			}
			if len(parts) > 0 {
				text := strings.Join(parts, "\n")
				if retained += len(text); retained > maxRetainedText {
					return domain.PipelineFragment{}, nil, fail("size_limit")
				}
				responses = append(responses, textMessage{msg.Role, text})
			}
		case "event_msg":
			var msg message
			if json.Unmarshal(record.Payload, &msg) != nil {
				return domain.PipelineFragment{}, nil, fail("invalid_event")
			}
			role := ""
			switch msg.Type {
			case "user_message":
				role = "user"
			case "agent_message":
				role = "assistant"
			}
			if role != "" && strings.TrimSpace(msg.Message) != "" {
				if retained += len(msg.Message); retained > maxRetainedText {
					return domain.PipelineFragment{}, nil, fail("size_limit")
				}
				events = append(events, textMessage{role, msg.Message})
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if limited.N == 0 {
		return domain.PipelineFragment{}, nil, &domain.CollectionError{Class: "size_limit", Offset: maxBytes}
	}
	if meta.ID == "" {
		if tail != nil {
			// A brand-new file whose first line is still being written has nothing
			// to import yet.
			return domain.PipelineFragment{}, tail, nil
		}
		return domain.PipelineFragment{}, nil, &domain.CollectionError{Class: "missing_session_meta"}
	}
	// Legacy event messages duplicate response items; prefer response material.
	messages := responses
	if len(messages) == 0 {
		messages = events
	}
	if len(messages) == 0 {
		return domain.PipelineFragment{}, tail, nil
	}
	var parts []string
	for _, msg := range messages {
		parts = append(parts, "## "+msg.role+"\n\n"+msg.text)
	}
	if created.IsZero() {
		created = modTime
	}
	created = created.UTC()
	return domain.PipelineFragment{
		Source: "codex", SourceType: "transcript", SourceID: meta.ID, Title: "Codex session: " + meta.ID,
		Content: strings.Join(parts, "\n\n"), CreatedAt: created.UTC(),
		Metadata:      map[string]any{"thread_id": meta.ID, "session_id": meta.SessionID, "cwd": meta.CWD, "cli_version": meta.CLIVersion, "parent_thread_id": meta.ParentThreadID, "source_file": path},
		CanonicalPath: filepath.ToSlash(filepath.Join("fragments", "chats", "codex", created.Format("2006-01-02"), meta.ID)),
	}, tail, nil
}
