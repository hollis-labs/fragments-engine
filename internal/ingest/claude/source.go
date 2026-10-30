package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const kind = "claude_code"

const (
	defaultMaxFileBytes = 1 << 30
	maxLineBytes        = 10 << 20
	tailWindow          = 4096
	maxExcerptBytes     = 2048
	rawExcerptBytes     = 16 << 10
)

type Source struct{}

func (Source) Kind() string {
	return kind
}

// Collect reads Claude session files into searchable fragments plus the
// structured turns, tool calls and usage beside them. A file with a stored
// cursor is read only from that cursor, and only if the bytes before it are
// unchanged; otherwise it is parsed whole and its derived rows are replaced.
func (Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	root := config.ExpandHome(cfg.Source.Root)
	paths, err := sessionFiles(ctx, root)
	if err != nil {
		return nil, err
	}

	maxSizeBytes := int64(defaultMaxFileBytes)
	rules, err := config.DecodeRules[config.ClaudeCodeRules](cfg)
	if err != nil {
		return nil, err
	}
	if rules.MaxFileSizeMB > 0 {
		maxSizeBytes = int64(rules.MaxFileSizeMB) * 1024 * 1024
	}
	excerpt := rules.ToolResultExcerptBytes
	if excerpt < 0 {
		excerpt = 0
	}
	if excerpt > maxExcerptBytes {
		excerpt = maxExcerptBytes
	}
	onError := rules.ToolResultExcerptOnErrorBytes
	if onError < 0 {
		onError = 0
	}
	if onError > maxExcerptBytes {
		onError = maxExcerptBytes
	}

	out := make([]domain.PipelineFragment, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		relPath = filepath.ToSlash(relPath)
		if info.Size() > maxSizeBytes {
			domain.NoteSkip(ctx, "size_limit", relPath)
			continue
		}
		fragment, skip, err := collectSegment(ctx, root, path, relPath, info, domain.ExcerptCapture(excerpt, onError))
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			// One bad or unreadable file costs only itself: counted with its class
			// and position, never the other files.
			var diag *domain.CollectionError
			if errors.As(err, &diag) {
				domain.NoteSkipAt(ctx, diag.Class, relPath, diag.Line, diag.Offset)
			} else {
				domain.NoteSkip(ctx, "unreadable_file", relPath)
			}
			continue
		}
		if skip != "" {
			domain.NoteSkip(ctx, skip, relPath)
			continue
		}
		fragment.Detail.ApplyExcerptPolicy(excerpt, onError)
		out = append(out, fragment)
	}
	return out, nil
}

// Native roots contain projects/<project>; synced archives contain <project>.
// Only session JSONL files and <session>/subagents/**/*.jsonl are collected.
// Directory and file symlinks are not followed.
func sessionFiles(ctx context.Context, root string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read claude root: %w", err)
	}
	var projects []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if entry.Name() == "projects" {
			native, err := os.ReadDir(path)
			if err != nil {
				return nil, fmt.Errorf("read claude projects: %w", err)
			}
			for _, project := range native {
				if project.IsDir() {
					projects = append(projects, filepath.Join(path, project.Name()))
				}
			}
		} else {
			projects = append(projects, path)
		}
	}
	var paths []string
	for _, project := range projects {
		if err := filepath.WalkDir(project, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				if path == project {
					return walkErr
				}
				// One unreadable or just-vanished directory (rsync is writing
				// here) must not abort the whole source.
				if r, e := filepath.Rel(root, path); e == nil {
					domain.NoteSkip(ctx, "unreadable_dir", filepath.ToSlash(r))
				}
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(project, path)
			if err != nil {
				return err
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if entry.IsDir() {
				if len(parts) == 2 && parts[1] != "subagents" {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type().IsRegular() && filepath.Ext(path) == ".jsonl" && (len(parts) == 1 || len(parts) >= 3 && parts[1] == "subagents") {
				paths = append(paths, path)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("enumerate claude sessions: %w", err)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func subagentSegment(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 0 && parts[0] == "projects" {
		parts = parts[1:]
	}
	if len(parts) < 4 || parts[2] != "subagents" {
		return ""
	}
	return "subagent/" + strings.Join(parts[1:], "/")
}
