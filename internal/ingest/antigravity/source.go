// Package antigravity reads checkpointed conversation databases and brain text
// artifacts. It never creates source sidecars or copies raw material to disk.
package antigravity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
			if !before.Mode().IsRegular() || before.Size() > maxBytes {
				continue
			}
			var content string
			if section == "conversations" {
				content, err = conversation(ctx, path, maxBytes)
			} else {
				content, err = brain(ctx, path, maxBytes)
			}
			if err != nil {
				return nil, err
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				return nil, errors.New("Antigravity input changed during collection")
			}
			if strings.TrimSpace(content) == "" {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil, errors.New("invalid Antigravity input locator")
			}
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

func brain(ctx context.Context, path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open Antigravity brain artifact")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return "", errors.New("cannot read Antigravity brain artifact within size limit")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validText(raw) {
		return "", errors.New("Antigravity brain artifact is not supported UTF-8 text")
	}
	// Structured logs are kept as separate text artifacts, not projected into
	// conversation turns. No binary attachments are materialized.
	return string(raw), nil
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

func conversation(ctx context.Context, path string, maxBytes int64) (string, error) {
	if err := snapshotReady(path); err != nil {
		return "", err
	}
	// immutable avoids source locks, journals and SHM writes. Pending WAL must
	// therefore be rejected explicitly, rather than silently ignored.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", errors.New("cannot open Antigravity database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// ORDER BY may need temporary storage; raw text must stay in memory.
	if _, err := db.ExecContext(ctx, `PRAGMA temp_store=MEMORY`); err != nil {
		return "", errors.New("cannot configure Antigravity read-only database")
	}
	rows, err := db.QueryContext(ctx, `SELECT step_type, step_format, length(step_payload), CASE WHEN length(step_payload) <= 10485760 THEN step_payload END FROM steps WHERE step_type IN (14,15) ORDER BY idx`)
	if err != nil {
		return "", errors.New("unsupported Antigravity database schema")
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var stepType, format, size int
		var raw []byte
		if rows.Scan(&stepType, &format, &size, &raw) != nil || format != 0 || size > 10*1024*1024 {
			return "", errPayload
		}
		text, err := stepText(stepType, raw)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		role := "user"
		if stepType == 15 {
			role = "assistant"
		}
		if int64(out.Len()+len(text)+20) > maxBytes {
			return "", errors.New("Antigravity conversation exceeds size limit")
		}
		fmt.Fprintf(&out, "## %s\n\n%s\n\n", role, text)
	}
	if rows.Err() != nil {
		return "", errors.New("cannot read Antigravity conversation")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := snapshotReady(path); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
