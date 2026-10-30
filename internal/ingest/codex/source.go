// Package codex reads Codex rollout JSONL files without changing the archive.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
		fragment, class, tail, err := collectSegment(ctx, path, rel, info, maxBytes, domain.ExcerptCapture(max(0, rules.ToolResultExcerptBytes), max(0, rules.ToolResultExcerptOnErrorBytes)))
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
		if class != "" {
			// Nothing new since the stored cursor, or nothing to import yet.
			continue
		}
		// A thread can have multiple physical rollout files (fork/revert histories).
		// Keep each file distinct without treating its metadata as authority.
		fragment.SourceIdentity.SegmentKey = "rollout/" + filepath.ToSlash(rel)
		fragment.Detail.ApplyExcerptPolicy(max(0, rules.ToolResultExcerptBytes), max(0, rules.ToolResultExcerptOnErrorBytes))
		out = append(out, fragment)
	}
	return out, nil
}

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
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
