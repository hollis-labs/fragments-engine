// Package codex reads Codex rollout JSONL files without changing the archive.
package codex

import (
	"bufio"
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
			if path == root && os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
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
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect codex session: %w", err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("codex session locator: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if info.Size() > maxBytes {
			domain.NoteSkip(ctx, "size_limit", rel)
			continue
		}
		fragment, err := parseSession(ctx, path, info.ModTime(), maxBytes)
		if err != nil {
			var diag *domain.CollectionError
			if errors.As(err, &diag) && diag.Class == "size_limit" {
				domain.NoteSkip(ctx, diag.Class, rel)
				continue
			}
			return nil, domain.AttributeFile(err, rel)
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

func parseSession(ctx context.Context, path string, modTime time.Time, maxBytes int64) (domain.PipelineFragment, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("open codex session: %w", err)
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var meta sessionMeta
	var responses, events []textMessage
	var created time.Time
	lineNo := 0
	var retained int
	var lineStart, next int64 // byte offsets: current line start, following line start
	fail := func(class string) error {
		return &domain.CollectionError{Class: class, Line: lineNo, Offset: lineStart}
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return domain.PipelineFragment{}, err
		}
		lineNo++
		raw := scanner.Bytes()
		lineStart = next
		next += int64(len(raw)) + 1
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var record line
		// Never include a decoder error or raw record in diagnostics.
		if json.Unmarshal(raw, &record) != nil || record.Type == "" {
			return domain.PipelineFragment{}, fail("invalid_record")
		}
		if ts, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil && (created.IsZero() || ts.Before(created)) {
			created = ts
		}
		switch record.Type {
		case "session_meta":
			var next sessionMeta
			if json.Unmarshal(record.Payload, &next) != nil || next.ID == "" {
				return domain.PipelineFragment{}, fail("invalid_session_meta")
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
				return domain.PipelineFragment{}, fail("invalid_response_item")
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
					return domain.PipelineFragment{}, fail("size_limit")
				}
				responses = append(responses, textMessage{msg.Role, text})
			}
		case "event_msg":
			var msg message
			if json.Unmarshal(record.Payload, &msg) != nil {
				return domain.PipelineFragment{}, fail("invalid_event")
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
					return domain.PipelineFragment{}, fail("size_limit")
				}
				events = append(events, textMessage{role, msg.Message})
			}
		}
	}
	if scanner.Err() != nil {
		// The line that overflowed the scanner starts at the running offset.
		lineNo++
		lineStart = next
		return domain.PipelineFragment{}, fail("unreadable_line")
	}
	if limited.N == 0 {
		return domain.PipelineFragment{}, &domain.CollectionError{Class: "size_limit", Offset: maxBytes}
	}
	if meta.ID == "" {
		return domain.PipelineFragment{}, &domain.CollectionError{Class: "missing_session_meta"}
	}
	// Legacy event messages duplicate response items; prefer response material.
	messages := responses
	if len(messages) == 0 {
		messages = events
	}
	if len(messages) == 0 {
		return domain.PipelineFragment{}, nil
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
	}, nil
}
