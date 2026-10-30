package transcript

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const ingestKey = "last_ingest_at"

// stampIngest records that a segment was accepted: overall and for the session's tool.
func stampIngest(ctx context.Context, tx *sql.Tx, pk string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var tool string
	_ = tx.QueryRowContext(ctx, `SELECT tool FROM tx_sessions WHERE session_pk = ?`, pk).Scan(&tool)
	keys := []string{ingestKey}
	if tool != "" {
		keys = append(keys, ingestKey+":"+tool)
	}
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tx_meta VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, now); err != nil {
			return err
		}
	}
	return nil
}

// SourceIngest is when one tool's transcripts were last ingested.
type SourceIngest struct {
	Tool         string `json:"tool"`
	LastIngestAt string `json:"last_ingest_at"`
}

// IngestStatus says how fresh the stored transcripts are. State is "never" until a segment has been accepted.
type IngestStatus struct {
	State        string         `json:"state"`
	LastIngestAt string         `json:"last_ingest_at,omitempty"`
	Sources      []SourceIngest `json:"sources"`
}

// Ingest reads the freshness stamps.
func (s *Store) Ingest(ctx context.Context) (IngestStatus, error) {
	if s == nil {
		return IngestStatus{}, ErrPrivateStoreRequired
	}
	out := IngestStatus{State: "never", Sources: []SourceIngest{}}
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM tx_meta WHERE key = ? OR key LIKE ? ORDER BY key`, ingestKey, ingestKey+`:%`)
	if err != nil {
		return IngestStatus{}, errors.New("transcript: cannot read ingest status")
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return IngestStatus{}, errors.New("transcript: cannot read ingest status")
		}
		if k == ingestKey {
			out.State, out.LastIngestAt = "ok", v
		} else {
			out.Sources = append(out.Sources, SourceIngest{Tool: strings.TrimPrefix(k, ingestKey+":"), LastIngestAt: v})
		}
	}
	return out, rows.Err()
}

// Bucket is one group of the aggregate: sessions and what they hold.
type Bucket struct {
	Key       string `json:"key"`
	Sessions  int    `json:"sessions"`
	Turns     int    `json:"turns"`
	Tokens    int64  `json:"tokens"`
	ToolCalls int    `json:"tool_calls"`
	Errors    int    `json:"errors"`
}

// Stats aggregates the filtered sessions over the whole database.
type Stats struct {
	Totals    Bucket   `json:"totals"`
	ByDay     []Bucket `json:"by_day"`
	ByTool    []Bucket `json:"by_tool"`
	ByModel   []Bucket `json:"by_model"`
	ByProject []Bucket `json:"by_project"`
}

const (
	maxStatDays     = 400
	maxStatProjects = 20
	statsDeadline   = 5 * time.Second
)

// Stats computes totals and groupings for the sessions that match the filter (the search and paging fields are
// ignored). Groups with no value are reported under the key "".
func (s *Store) Stats(ctx context.Context, f SessionFilter) (Stats, error) {
	if s == nil {
		return Stats{}, ErrPrivateStoreRequired
	}
	ctx, cancel := context.WithTimeout(ctx, statsDeadline)
	defer cancel()
	where, args := f.where()
	cte := `WITH f AS (SELECT s.tool AS tool, s.model AS model, s.cwd AS cwd, substr(s.started_at, 1, 10) AS day, s.turn_count AS turns,
 COALESCE(u.tokens, 0) AS tokens, COALESCE(c.calls, 0) AS calls, COALESCE(c.errs, 0) AS errs
 FROM (SELECT session_pk FROM tx_sessions) k` + strings.ReplaceAll(summaryJoins, "%s", "k.session_pk") + where + `) `
	agg := `COUNT(*), COALESCE(SUM(turns), 0), COALESCE(SUM(tokens), 0), COALESCE(SUM(calls), 0), COALESCE(SUM(errs), 0)`
	var out Stats
	scan := func(r interface{ Scan(...any) error }, b *Bucket, withKey bool) error {
		if withKey {
			return r.Scan(&b.Key, &b.Sessions, &b.Turns, &b.Tokens, &b.ToolCalls, &b.Errors)
		}
		return r.Scan(&b.Sessions, &b.Turns, &b.Tokens, &b.ToolCalls, &b.Errors)
	}
	if err := scan(s.db.QueryRowContext(ctx, cte+`SELECT `+agg+` FROM f`, args...), &out.Totals, false); err != nil {
		return Stats{}, errors.New("transcript: cannot compute stats")
	}
	group := func(col, extra, order string, limit int) ([]Bucket, error) {
		rows, err := s.db.QueryContext(ctx, cte+`SELECT `+col+`, `+agg+` FROM f `+extra+` GROUP BY `+col+` ORDER BY `+order+` LIMIT ?`, append(append([]any{}, args...), limit)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Bucket{}
		for rows.Next() {
			var b Bucket
			if err := scan(rows, &b, true); err != nil {
				return nil, err
			}
			out = append(out, b)
		}
		return out, rows.Err()
	}
	var err error
	if out.ByDay, err = group("day", `WHERE day <> ''`, "day DESC", maxStatDays); err != nil {
		return Stats{}, errors.New("transcript: cannot compute stats")
	}
	for i, j := 0, len(out.ByDay)-1; i < j; i, j = i+1, j-1 {
		out.ByDay[i], out.ByDay[j] = out.ByDay[j], out.ByDay[i]
	}
	if out.ByTool, err = group("tool", "", "COUNT(*) DESC, tool", 50); err != nil {
		return Stats{}, errors.New("transcript: cannot compute stats")
	}
	if out.ByModel, err = group("model", "", "COUNT(*) DESC, model", 50); err != nil {
		return Stats{}, errors.New("transcript: cannot compute stats")
	}
	if out.ByProject, err = group("cwd", "", "COUNT(*) DESC, cwd", maxStatProjects); err != nil {
		return Stats{}, errors.New("transcript: cannot compute stats")
	}
	return out, nil
}
