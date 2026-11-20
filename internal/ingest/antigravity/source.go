// Package antigravity reads checkpointed conversation databases and brain text
// artifacts. It never creates source sidecars or copies raw material to disk.
package antigravity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	_ "modernc.org/sqlite"
)

type Source struct{}

func (Source) Kind() string { return "antigravity" }

var ErrSnapshotRequired = errors.New("Antigravity database requires a checkpoint-consistent snapshot without pending WAL/journal data")

func (Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	rules, err := config.DecodeRules[config.AntigravityRules](cfg)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Source.Root) == "" {
		return nil, errors.New("Antigravity source root is required")
	}
	root, err := filepath.Abs(config.ExpandHome(cfg.Source.Root))
	if err != nil {
		return nil, errors.New("invalid Antigravity root")
	}
	var out []domain.PipelineFragment
	for _, section := range []string{"conversations", "brain"} {
		paths, err := sourceFiles(ctx, filepath.Join(root, section), section == "conversations")
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			before, err := os.Lstat(path)
			if err != nil {
				return nil, errors.New("cannot inspect Antigravity input")
			}
			if !before.Mode().IsRegular() {
				continue
			}
			relPath, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil, errors.New("invalid Antigravity input locator")
			}
			relPath = filepath.ToSlash(relPath)
			if section == "brain" && !rules.IncludeLogChunks && isLogChunk(relPath) {
				// Pure duplicates of the parent transcript logs; counted so the
				// exclusion is visible. Already-stored records are not touched.
				domain.NoteSkip(ctx, "log_chunks_excluded", relPath)
				continue
			}
			var (
				captured domain.PipelineFragment
				skip     string
			)
			if section == "conversations" {
				captured, skip, err = captureConversation(ctx, filepath.Join(root, "brain"), path, relPath, before)
			} else if c, ok := domain.LookupCursor(ctx, relPath); ok && c.FileSize == before.Size() && c.ModTime.Equal(before.ModTime().UTC()) {
				// Stored and unchanged: not read again.
				domain.NoteSkip(ctx, "unchanged", relPath)
				continue
			}
			if errors.Is(err, ErrSnapshotRequired) {
				// Fail closed for this database only. It is retried on the next
				// fire, once the producer has synced a checkpointed snapshot.
				domain.NoteSkip(ctx, "pending_wal", relPath)
				continue
			}
			if err != nil {
				return nil, err
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				return nil, errors.New("Antigravity input changed during collection")
			}
			if section == "conversations" {
				if skip != "" {
					domain.NoteSkip(ctx, skip, relPath)
					continue
				}
				out = append(out, captured)
				continue
			}
			created := before.ModTime().UTC()
			id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			filePath := path
			out = append(out, domain.PipelineFragment{
				Source:     "antigravity_brain",
				SourceType: "transcript", SourceID: id, Title: "Antigravity brain: " + id,
				CreatedAt:      created,
				SourceIdentity: domain.SourceIdentity{SegmentKey: relPath},
				File: &domain.TranscriptFile{
					ConversationID: brainConversation(relPath), RelPath: relPath, Area: brainArea(root, path),
					Size: before.Size(), ModTime: created,
					Load: func() ([]byte, error) { return os.ReadFile(filePath) },
				},
			})
		}
	}
	return out, nil
}

func sourceFiles(ctx context.Context, root string, databases bool) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root && os.IsNotExist(walkErr) {
				return nil
			}
			return errors.New("cannot enumerate Antigravity inputs")
		}
		if entry.IsDir() && entry.Name() == ".rsync-partial" {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch {
		case databases:
			if ext == ".db" {
				paths = append(paths, path)
			}
		case brainArea(root, path) != "scratch":
			// Artifacts, uploads and system files are kept whatever their type.
			paths = append(paths, path)
		case ext == ".txt" || ext == ".md" || ext == ".json" || ext == ".jsonl" || ext == ".log":
			// Scratch work trees hold code and dependencies; their text files are kept,
			// the rest (sources, maps, binaries) is not.
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

// brainConversation is the conversation id in brain/<conversation>/... .
func brainConversation(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) >= 3 && parts[0] == "brain" {
		return parts[1]
	}
	return ""
}

// brainArea says what kind of file a brain path is: scratch work, an upload, a system
// file, or (everything else) an artifact the agent wrote.
func brainArea(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "artifact"
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 0 && parts[0] == "brain" {
		parts = parts[1:]
	}
	if len(parts) < 2 { // brain/<conversation>/<file>
		return "artifact"
	}
	switch parts[1] {
	case "scratch":
		return "scratch"
	case ".user_uploaded":
		return "upload"
	case ".system_generated":
		return "system"
	}
	return "artifact"
}

func snapshotReady(path string) error {
	for _, suffix := range []string{"-wal", "-journal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || (suffix != "-shm" && info.Size() > 0) {
			return ErrSnapshotRequired
		}
	}
	return nil
}

// isLogChunk reports brain/<conversation>/.system_generated/logs/chunks/**.
func isLogChunk(rel string) bool {
	parts := strings.Split(rel, "/")
	return len(parts) >= 6 && parts[0] == "brain" && parts[2] == ".system_generated" && parts[3] == "logs" && parts[4] == "chunks"
}
