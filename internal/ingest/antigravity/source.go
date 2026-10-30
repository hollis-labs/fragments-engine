// Package antigravity reads checkpointed conversation databases and brain text
// artifacts. It never creates source sidecars or copies raw material to disk.
package antigravity

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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
	maxBytes := int64(50 * 1024 * 1024)
	if rules.MaxFileSizeMB > 0 {
		if rules.MaxFileSizeMB > 1024 {
			return nil, errors.New("Antigravity file limit exceeds 1024 MiB")
		}
		maxBytes = int64(rules.MaxFileSizeMB) * 1024 * 1024
	}
	excerpt := rules.ToolResultExcerptBytes
	if excerpt < 0 {
		excerpt = 0
	}
	if excerpt > maxExcerpt {
		excerpt = maxExcerpt
	}
	onError := rules.ToolResultExcerptOnErrorBytes
	if onError < 0 {
		onError = 0
	}
	if onError > maxExcerpt {
		onError = maxExcerpt
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
			if before.Size() > maxBytes {
				domain.NoteSkip(ctx, "size_limit", relPath)
				continue
			}
			var (
				content  string
				trimmed  bool
				captured domain.PipelineFragment
				skip     string
			)
			if section == "conversations" {
				captured, skip, err = captureConversation(ctx, filepath.Join(root, "brain"), path, relPath, before, maxBytes, domain.ExcerptCapture(excerpt, onError))
			} else {
				content, trimmed, err = brain(ctx, path, maxBytes)
			}
			if errors.Is(err, errBrainEncoding) {
				// One undecodable artifact must not abort the run. It is counted
				// per file and picked up again only if its bytes change.
				domain.NoteSkip(ctx, "not_utf8", relPath)
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
				captured.Detail.ApplyExcerptPolicy(excerpt, onError)
				out = append(out, captured)
				continue
			}
			if strings.TrimSpace(content) == "" {
				continue
			}
			if trimmed {
				// Imported, not skipped: counted so truncated artifacts stay visible.
				domain.NoteSkip(ctx, "trimmed_tail", relPath)
			}
			rel := relPath
			id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			created := before.ModTime().UTC()
			out = append(out, domain.PipelineFragment{
				Source:     "antigravity_" + map[string]string{"conversations": "db", "brain": "brain"}[section],
				SourceType: "transcript", SourceID: id, Title: "Antigravity " + section + ": " + id,
				Content: content, CreatedAt: created,
				SourceIdentity: domain.SourceIdentity{SegmentKey: filepath.ToSlash(rel)},
				Metadata:       map[string]any{"source_file": path, "observed_at": created.Format(time.RFC3339Nano), "format": section},
				CanonicalPath:  filepath.ToSlash(filepath.Join("fragments", "chats", "antigravity", section, created.Format("2006-01-02"), id)),
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
		if databases && ext == ".db" || !databases && (ext == ".txt" || ext == ".md" || ext == ".json" || ext == ".jsonl" || ext == ".log") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

var errBrainEncoding = errors.New("Antigravity brain artifact is not supported UTF-8 text")

// brain reads one brain artifact. An artifact whose only defect is an
// incomplete UTF-8 sequence at its very end (a size-capped chunk cut mid-rune)
// is decoded after dropping that partial rune and reported as trimmed; any other
// invalid text returns errBrainEncoding.
func brain(ctx context.Context, path string, maxBytes int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, errors.New("cannot open Antigravity brain artifact")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return "", false, errors.New("cannot read Antigravity brain artifact within size limit")
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	trimmed := false
	if !utf8.Valid(raw) {
		head, ok := trimPartialRune(raw)
		if !ok {
			return "", false, errBrainEncoding
		}
		raw, trimmed = head, true
	}
	if !validText(raw) {
		return "", false, errBrainEncoding
	}
	// Structured logs are kept as separate text artifacts, not projected into
	// conversation turns. No binary attachments are materialized.
	return string(raw), trimmed, nil
}

// trimPartialRune drops an incomplete UTF-8 sequence (1-3 bytes) from the end of
// raw. It reports false unless that is the only defect.
func trimPartialRune(raw []byte) ([]byte, bool) {
	for n := 1; n <= utf8.UTFMax-1 && n <= len(raw); n++ {
		tail := raw[len(raw)-n:]
		if utf8.RuneStart(tail[0]) && tail[0] >= utf8.RuneSelf && !utf8.FullRune(tail) {
			if head := raw[:len(raw)-n]; utf8.Valid(head) {
				return head, true
			}
			return nil, false
		}
	}
	return nil, false
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
