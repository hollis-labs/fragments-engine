package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CoverageFile is one session file found on disk and the session id it claims.
type CoverageFile struct {
	Tool string
	Host string
	Path string        // relative to the scanned root
	ID   string        // native session id; "" when the file has none yet
	Age  time.Duration // time since last write; 0 when unknown
}

// CoverageRow is the per tool and host tally. Missing lists the on-disk ids the
// store has no tx_sessions row for; Unidentified counts files with no id at all
// (empty or still being written), which cannot be compared.
type CoverageRow struct {
	Tool         string   `json:"tool"`
	Host         string   `json:"host"`
	Files        int      `json:"files"`
	InStore      int      `json:"in_store"`
	Unidentified int      `json:"unidentified"`
	Pending      int      `json:"pending"`
	Missing      []string `json:"missing"`
}

// StoredSessionIDs returns the native ids of main sessions in tx_sessions, by tool.
func (s *Store) StoredSessionIDs(ctx context.Context) (map[string]map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool, native_session_id FROM tx_sessions WHERE kind = 'main'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var tool, id string
		if err := rows.Scan(&tool, &id); err != nil {
			return nil, err
		}
		if out[tool] == nil {
			out[tool] = map[string]bool{}
		}
		out[tool][id] = true
	}
	return out, rows.Err()
}

// Coverage compares files on disk with the stored ids. Rows are sorted by tool
// then host; ids within a row are sorted and de-duplicated.
//
// A file written to within grace is Pending, not Missing: ingest runs on a
// schedule, so a live session is expected to trail the disk.
func Coverage(files []CoverageFile, stored map[string]map[string]bool, grace time.Duration) []CoverageRow {
	type key struct{ tool, host string }
	rows := map[key]*CoverageRow{}
	seen := map[key]map[string]bool{}
	for _, f := range files {
		k := key{f.Tool, f.Host}
		row := rows[k]
		if row == nil {
			row = &CoverageRow{Tool: f.Tool, Host: f.Host, Missing: []string{}}
			rows[k], seen[k] = row, map[string]bool{}
		}
		row.Files++
		switch {
		case f.ID == "":
			row.Unidentified++
		case stored[f.Tool][f.ID]:
			row.InStore++
		case f.Age > 0 && f.Age < grace:
			row.Pending++
		case !seen[k][f.ID]:
			seen[k][f.ID] = true
			row.Missing = append(row.Missing, f.ID)
		}
	}
	out := make([]CoverageRow, 0, len(rows))
	for _, r := range rows {
		sort.Strings(r.Missing)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// Drifted reports whether any row has a session on disk that the store lacks.
func Drifted(rows []CoverageRow) bool {
	for _, r := range rows {
		if len(r.Missing) > 0 {
			return true
		}
	}
	return false
}

// ScanClaudeFiles lists main session files under a Claude root (native
// projects/<p>/<id>.jsonl or synced <p>/<id>.jsonl; subagent files excluded).
func ScanClaudeFiles(root, host string) ([]CoverageFile, error) {
	return scanJSONL(root, "claude", host, func(parts []string) bool {
		if len(parts) > 0 && parts[0] == "projects" {
			parts = parts[1:]
		}
		return len(parts) == 2
	}, func(raw []byte) string {
		var ev struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(raw, &ev) == nil {
			return ev.SessionID
		}
		return ""
	})
}

// ScanCodexFiles lists rollout files under a Codex root and reads each file's
// own session_meta id.
func ScanCodexFiles(root, host string) ([]CoverageFile, error) {
	return scanJSONL(root, "codex", host, func([]string) bool { return true }, func(raw []byte) string {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &rec) == nil && rec.Type == "session_meta" {
			return rec.Payload.ID
		}
		return ""
	})
}

func scanJSONL(root, tool, host string, wanted func(parts []string) bool, idOf func([]byte) string) ([]CoverageFile, error) {
	var out []CoverageFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			return nil
		}
		if entry.IsDir() && entry.Name() == ".rsync-partial" {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !wanted(strings.Split(rel, "/")) {
			return nil
		}
		var age time.Duration
		if info, err := entry.Info(); err == nil {
			age = max(time.Since(info.ModTime()), time.Nanosecond)
		}
		out = append(out, CoverageFile{Tool: tool, Host: host, Path: rel, ID: firstID(path, idOf), Age: age})
		return nil
	})
	return out, err
}

// firstID returns the first non-empty id in the head of a file. Reading stops
// at an unterminated final line, so a file still being written yields no id
// rather than a wrong one.
func firstID(path string, idOf func([]byte) string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	for i := 0; i < 200; i++ {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return ""
		}
		if err != nil {
			return ""
		}
		if id := idOf(line); id != "" {
			return id
		}
	}
	return ""
}
