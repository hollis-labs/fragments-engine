package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const kind = "claude_code"

type Source struct{}

func (Source) Kind() string {
	return kind
}

func (Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	root := config.ExpandHome(cfg.Source.Root)
	paths, err := sessionFiles(ctx, root)
	if err != nil {
		return nil, err
	}

	maxSizeBytes := int64(50 * 1024 * 1024)
	rules, err := config.DecodeRules[config.ClaudeCodeRules](cfg)
	if err != nil {
		return nil, err
	}
	if rules.MaxFileSizeMB > 0 {
		maxSizeBytes = int64(rules.MaxFileSizeMB) * 1024 * 1024
	}

	out := make([]domain.PipelineFragment, 0, len(paths))
	for _, path := range paths {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSizeBytes {
			continue
		}
		fragment, err := parseSessionFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse claude session %s: %w", path, err)
		}
		if strings.TrimSpace(fragment.Content) == "" {
			continue
		}
		if segment := subagentSegment(root, path); segment != "" {
			fragment.SourceIdentity.SegmentKey = segment
			fragment.Metadata["subagent_segment"] = segment
			fragment.CanonicalPath += "/" + segment
		}
		out = append(out, fragment)
	}
	return out, nil
}

type event struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	SessionID string         `json:"sessionId"`
	Slug      string         `json:"slug"`
	CWD       string         `json:"cwd"`
	Message   map[string]any `json:"message"`
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
				return walkErr
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

func parseSessionFile(path string) (domain.PipelineFragment, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	var (
		sessionID  string
		slug       string
		cwd        string
		createdAt  time.Time
		messages   []string
		sourceFile = path
	)

	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 0, 1024*1024)
	scanner.Buffer(buffer, 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if sessionID == "" {
			sessionID = ev.SessionID
		}
		if slug == "" {
			slug = ev.Slug
		}
		if cwd == "" {
			cwd = ev.CWD
		}
		if ts, ok := parseEventTime(ev.Timestamp); ok {
			if createdAt.IsZero() || ts.Before(createdAt) {
				createdAt = ts
			}
		}
		switch ev.Type {
		case "user":
			text := extractContent(ev.Message)
			if text != "" {
				messages = append(messages, "## user\n\n"+text)
			}
		case "assistant":
			text := extractContent(ev.Message)
			if text != "" {
				messages = append(messages, "## assistant\n\n"+text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("scan session file: %w", err)
	}

	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if slug == "" {
		slug = sessionID
	}

	title := "Claude session: " + slug
	meta := map[string]any{
		"session_id":  sessionID,
		"slug":        slug,
		"cwd":         cwd,
		"source_file": sourceFile,
	}
	if createdAt.IsZero() {
		info, err := os.Stat(path)
		if err == nil {
			createdAt = info.ModTime().UTC()
		} else {
			createdAt = time.Now().UTC()
		}
	}

	content := strings.Join(messages, "\n\n")
	if content == "" {
		return domain.PipelineFragment{}, nil
	}

	canonicalPath := filepath.ToSlash(filepath.Join("fragments", "chats", "claude", createdAt.Format("2006-01-02"), sessionID))
	return domain.PipelineFragment{
		Source:        "claude",
		SourceType:    "chat",
		SourceID:      sessionID,
		Title:         title,
		Content:       content,
		CreatedAt:     createdAt.UTC(),
		Metadata:      meta,
		CanonicalPath: canonicalPath,
	}, nil
}

func extractContent(message map[string]any) string {
	if message == nil {
		return ""
	}
	raw, ok := message["content"]
	if !ok {
		return ""
	}
	switch content := raw.(type) {
	case string:
		return strings.TrimSpace(content)
	case []any:
		parts := make([]string, 0, len(content))
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockMap["type"] == "text" {
				if text, ok := blockMap["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
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
