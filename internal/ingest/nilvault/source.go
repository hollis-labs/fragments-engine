// Package nilvault ingests note and scratch items out of Nil's per-vault
// SQLite databases (todo.db). It reads directly against Nil's own storage --
// there is no Nil API/CLI/MCP surface for bulk reads yet (see the separate
// Nil-project epic scoping that, EP-20260816-0003) -- so this source opens
// each vault's SQLite file read-only and queries it directly. See source.go
// for the read-only DSN mechanics; see pmjson.go for the notes_doc (PM-JSON)
// -> plain-text conversion.
package nilvault

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest/sourceutil"
)

const kind = "nil_vault"

// todo/note/scratch is Nil's full kind set on the todos table; only note and
// scratch are ingested here (todo is excluded by design decision -- see
// CW-20260816-0053).
const todosQuery = `
SELECT id, title, notes_doc, created_at, updated_at, priority, section, pinned, completed, archived, due_at, kind, api_source
FROM todos
WHERE kind IN ('note', 'scratch')
ORDER BY id`

// allProjectsQuery/allContextsQuery/allTagsQuery/allRefsQuery are fetched
// once per vault (not once per item) and grouped in memory by todo_id --
// avoids an N+1 query pattern against a foreign, potentially
// concurrently-written database, which would otherwise multiply lock
// contention with Nil's own writer connections.
const allProjectsQuery = `
SELECT tp.todo_id, p.name FROM todo_projects tp
JOIN projects p ON p.id = tp.project_id
ORDER BY tp.todo_id, p.name`

const allContextsQuery = `
SELECT tc.todo_id, c.name FROM todo_contexts tc
JOIN contexts c ON c.id = tc.context_id
ORDER BY tc.todo_id, c.name`

const allTagsQuery = `
SELECT tt.todo_id, t.name FROM todo_tags tt
JOIN tags t ON t.id = tt.tag_id
ORDER BY tt.todo_id, t.name`

const allRefsQuery = `SELECT source_id, target_id FROM refs ORDER BY source_id, target_id`

type Source struct{}

func (Source) Kind() string {
	return kind
}

// nilConfig mirrors the shape of Nil's ~/.config/nil/config.json (see Nil's
// config.Config / config.Vault, config/config.go). Only the vault registry
// fields matter here; the shared global inbox path (InboxPath) is
// deliberately not read -- only named vaults are ingested (design decision,
// CW-20260816-0053).
type nilConfig struct {
	ActiveVaultID string     `json:"activeVaultId"`
	InboxPath     string     `json:"inboxPath"`
	Vaults        []nilVault `json:"vaults"`
}

type nilVault struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}

// todoRow is the raw scan target for one todos row. Nullable/optional
// columns use sql.NullString since Nil's schema doesn't mark them NOT NULL.
type todoRow struct {
	ID        int64
	Title     string
	NotesDoc  sql.NullString
	CreatedAt string
	UpdatedAt string
	Priority  sql.NullString
	Section   sql.NullString
	Pinned    int64
	Completed int64
	Archived  int64
	DueAt     sql.NullString
	Kind      string
	APISource sql.NullString
}

func (Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	root := config.ExpandHome(cfg.Source.Root)
	rules, err := config.DecodeRules[config.NilVaultRules](cfg)
	if err != nil {
		return nil, err
	}

	vaults, err := loadVaults(root)
	if err != nil {
		return nil, err
	}
	vaults = filterVaults(vaults, rules)

	out := make([]domain.PipelineFragment, 0)
	for _, vault := range vaults {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		info, statErr := os.Stat(vault.Path)
		if statErr != nil {
			log.Printf("nilvault: skipping vault %q: directory %s not accessible: %v", vault.Name, vault.Path, statErr)
			continue
		}
		if !info.IsDir() {
			log.Printf("nilvault: skipping vault %q: %s is not a directory", vault.Name, vault.Path)
			continue
		}

		items, err := collectVaultItems(ctx, cfg, vault)
		if err != nil {
			log.Printf("nilvault: skipping vault %q (%s): %v", vault.Name, vault.Path, err)
			continue
		}
		out = append(out, items...)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func loadVaults(root string) ([]nilVault, error) {
	configPath := filepath.Join(root, "config.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read nil config %s: %w", configPath, err)
	}
	var parsed nilConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode nil config %s: %w", configPath, err)
	}
	return parsed.Vaults, nil
}

func filterVaults(vaults []nilVault, rules config.NilVaultRules) []nilVault {
	include := toStringSet(rules.IncludeVaults)
	exclude := toStringSet(rules.ExcludeVaults)
	if len(include) == 0 && len(exclude) == 0 {
		return vaults
	}
	out := make([]nilVault, 0, len(vaults))
	for _, vault := range vaults {
		if len(include) > 0 {
			_, byID := include[vault.ID]
			_, byName := include[vault.Name]
			if !byID && !byName {
				continue
			}
		}
		if _, byID := exclude[vault.ID]; byID {
			continue
		}
		if _, byName := exclude[vault.Name]; byName {
			continue
		}
		out = append(out, vault)
	}
	return out
}

func toStringSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out[item] = struct{}{}
	}
	return out
}

// collectVaultItems opens vault's todo.db read-only and returns one
// PipelineFragment per note/scratch item with non-empty converted content.
//
// Read-only DSN: modernc.org/sqlite (FE's SQLite driver, see go.mod) always
// opens with SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE at the Go-driver
// level (see conn.go newConn) -- it does NOT expose a "mode=ro" Go-level DSN
// param the way mattn/go-sqlite3 does. However, when the DSN is prefixed
// with "file:" AND the driver's SQLITE_OPEN_URI flag is set (which it always
// is), the *SQLite C library itself* parses the URI and its "mode=ro" query
// parameter, which restricts (but per SQLite's URI docs, can never expand)
// the actual open mode -- verified empirically against this exact driver
// version: a write against a "file:...?mode=ro" connection fails with
// "attempt to write a readonly database", and opening a nonexistent path
// this way fails outright rather than creating it. Omitting the "file:"
// prefix is NOT safe: the Go-level DSN parser strips everything after '?'
// before it ever reaches SQLite's URI parser unless the DSN starts with
// "file:", silently discarding "mode=ro" and opening read-write. So the
// "file:" prefix here is load-bearing, not decorative. The path itself is
// built via sourceutil.FileURI, which percent-encodes it through Go's
// net/url -- a raw fmt.Sprintf-ed path could otherwise be misparsed by
// SQLite's URI parser if it contains '#', '%', '?', or spaces.
func collectVaultItems(ctx context.Context, cfg config.IngestConfig, vault nilVault) ([]domain.PipelineFragment, error) {
	dbPath := filepath.Join(vault.Path, "todo.db")
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve todo.db path: %w", err)
	}
	dsn := sourceutil.FileURI(absPath) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open todo.db: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("open todo.db: %w", err)
	}

	rows, err := db.QueryContext(ctx, todosQuery)
	if err != nil {
		return nil, fmt.Errorf("query todos: %w", err)
	}
	var items []todoRow
	for rows.Next() {
		var item todoRow
		if err := rows.Scan(
			&item.ID, &item.Title, &item.NotesDoc, &item.CreatedAt, &item.UpdatedAt,
			&item.Priority, &item.Section, &item.Pinned, &item.Completed, &item.Archived,
			&item.DueAt, &item.Kind, &item.APISource,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan todos row: %w", err)
		}
		items = append(items, item)
	}
	scanErr := rows.Err()
	rows.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("iterate todos rows: %w", scanErr)
	}

	// Taxonomy/refs are fetched once per vault, not once per item -- avoids
	// an N+1 query pattern against a foreign, potentially
	// concurrently-written database.
	projectsByID, err := queryGroupedNames(ctx, db, allProjectsQuery)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	contextsByID, err := queryGroupedNames(ctx, db, allContextsQuery)
	if err != nil {
		return nil, fmt.Errorf("query contexts: %w", err)
	}
	tagsByID, err := queryGroupedNames(ctx, db, allTagsQuery)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	refsByID, err := queryGroupedRefs(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("query refs: %w", err)
	}

	var out []domain.PipelineFragment
	for _, item := range items {
		content, docLinkedIDs := convertNotesDoc(item.NotesDoc.String)
		if strings.TrimSpace(content) == "" {
			continue
		}

		createdAt, err := parseNilTime(item.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at for item %d: %w", item.ID, err)
		}

		linkedIDs := dedupStrings(append(append([]string{}, refsByID[item.ID]...), docLinkedIDs...))
		sort.Strings(linkedIDs)

		out = append(out, buildFragment(cfg, vault, item, content, createdAt, projectsByID[item.ID], contextsByID[item.ID], tagsByID[item.ID], linkedIDs))
	}
	return out, nil
}

func queryGroupedNames(ctx context.Context, db *sql.DB, query string) (map[int64][]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var todoID int64
		var name string
		if err := rows.Scan(&todoID, &name); err != nil {
			return nil, err
		}
		out[todoID] = append(out[todoID], name)
	}
	return out, rows.Err()
}

func queryGroupedRefs(ctx context.Context, db *sql.DB) (map[int64][]string, error) {
	rows, err := db.QueryContext(ctx, allRefsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var sourceID, targetID int64
		if err := rows.Scan(&sourceID, &targetID); err != nil {
			return nil, err
		}
		out[sourceID] = append(out[sourceID], strconv.FormatInt(targetID, 10))
	}
	return out, rows.Err()
}

// parseNilTime parses Nil's created_at/updated_at strings. Nil writes these
// via SQLite's datetime('now'), which produces "YYYY-MM-DD HH:MM:SS" with no
// timezone suffix -- implicitly UTC. time.Parse returns UTC when the layout
// has no zone, so no explicit UTC conversion is needed for the primary
// layout; RFC3339 is tolerated as a defensive fallback.
func parseNilTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	layouts := []string{"2006-01-02 15:04:05", time.RFC3339}
	var lastErr error
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q: %w", raw, lastErr)
}

func buildFragment(cfg config.IngestConfig, vault nilVault, item todoRow, content string, createdAt time.Time, projects, contexts, tags, linkedIDs []string) domain.PipelineFragment {
	metadata := map[string]any{
		"vault_id":            vault.ID,
		"vault_name":          vault.Name,
		"kind":                item.Kind,
		"priority":            item.Priority.String,
		"section":             item.Section.String,
		"pinned":              item.Pinned != 0,
		"completed":           item.Completed != 0,
		"archived":            item.Archived != 0,
		"due_at":              item.DueAt.String,
		"projects":            projects,
		"contexts":            contexts,
		"tags":                tags,
		"nil_item_id":         item.ID,
		"nil_linked_item_ids": linkedIDs,
	}
	return domain.PipelineFragment{
		Source:        "nil",
		SourceType:    item.Kind,
		SourceID:      fmt.Sprintf("%s:%d", vault.ID, item.ID),
		Title:         item.Title,
		Content:       content,
		CreatedAt:     createdAt,
		Metadata:      metadata,
		CanonicalPath: canonicalNilPath(cfg, vault, item),
	}
}

func canonicalNilPath(cfg config.IngestConfig, vault nilVault, item todoRow) string {
	base := strings.Trim(sourceutil.NormalizePath(cfg.Routing.Namespace), "/")
	if base == "" {
		base = "fragments/nil"
	}
	vaultSegment := strings.Trim(sourceutil.NormalizePath(vault.Name), "/")
	if vaultSegment == "" {
		vaultSegment = strings.Trim(sourceutil.NormalizePath(vault.ID), "/")
	}
	return strings.Trim(base+"/"+vaultSegment+"/"+item.Kind+"/"+strconv.FormatInt(item.ID, 10), "/")
}
