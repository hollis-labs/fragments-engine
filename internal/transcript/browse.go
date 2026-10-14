package transcript

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file is the owner-only read surface over the private store. Everything
// it returns is already redacted text or metadata; it never returns source
// paths, native session ids, raw archives, attachment bytes or thinking text
// (only that thinking was present, and its length). Enrichment columns do not
// exist yet; they join in here once the enrichment schema lands.

const (
	maxBrowseLimit  = 200
	maxSnippetBytes = 400
	maxTurnTextRead = 20 << 10
	maxTurnPage     = 500
	maxTreeNodes    = 500
)

var ErrSessionNotFound = errors.New("transcript: session not found")

// SessionFilter selects sessions for the browse list. Zero fields match all.
type SessionFilter struct {
	Tool     string
	Project  string // substring of the working directory
	Model    string
	Kind     string // "main" or "subagent"
	Since    time.Time
	Until    time.Time
	HasError *bool
	// HasEnrichment and Tag use the newest ok Tier 1 enrichment, when any.
	HasEnrichment *bool
	Tag           string
	Sort          string // started, turns, tokens, tool_calls, errors
	Desc          bool
	Limit         int
	Cursor        string
}

// SessionSummary is one row of the browse list.
type SessionSummary struct {
	ID        string `json:"id"`
	Tool      string `json:"tool"`
	Kind      string `json:"kind"`
	ParentID  string `json:"parent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	Model     string `json:"model,omitempty"`
	Project   string `json:"project,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
	Title     string `json:"title"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	Turns     int    `json:"turns"`
	Tokens    int64  `json:"tokens"`
	ToolCalls int    `json:"tool_calls"`
	Errors    int    `json:"errors"`
	Snippet   string `json:"snippet,omitempty"`
	// Enrichment-derived, present only once the enrichment pipeline has run.
	Enriched bool     `json:"enriched,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

type SessionPage struct {
	Items      []SessionSummary `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

const summaryCols = `s.session_pk, s.tool, s.kind, s.parent_session_pk, s.agent_type, s.model, s.cwd, s.git_branch,
 COALESCE(t.title, ''), s.started_at, s.ended_at, s.turn_count,
 COALESCE(u.tokens, 0), COALESCE(c.calls, 0), COALESCE(c.errs, 0), COALESCE(en.result_json, '')`

const summaryJoins = `
 JOIN tx_sessions s ON s.session_pk = %s
 LEFT JOIN transcripts t ON t.id = s.session_pk
 LEFT JOIN (SELECT session_pk, SUM(input_tokens + output_tokens + cache_read_tokens + cache_creation_tokens) AS tokens FROM tx_usage GROUP BY session_pk) u ON u.session_pk = s.session_pk
 LEFT JOIN (SELECT session_pk, COUNT(*) AS calls, SUM(CASE WHEN result_status = 'error' THEN 1 ELSE 0 END) AS errs FROM tx_tool_calls GROUP BY session_pk) c ON c.session_pk = s.session_pk
 LEFT JOIN tx_enrichment en ON en.id = (SELECT e.id FROM tx_enrichment e WHERE e.session_pk = s.session_pk AND e.tier = 1 AND e.status = 'ok' ORDER BY e.created_at DESC, e.id DESC LIMIT 1)`

var listSelect = `SELECT ` + summaryCols + ` FROM (SELECT session_pk FROM tx_sessions) k` + fmt.Sprintf(summaryJoins, "k.session_pk")

var sortColumns = map[string]string{
	"started":    "s.started_at",
	"turns":      "s.turn_count",
	"tokens":     "COALESCE(u.tokens, 0)",
	"tool_calls": "COALESCE(c.calls, 0)",
	"errors":     "COALESCE(c.errs, 0)",
}

func decodeCursor(cursor string) int {
	if cursor == "" {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > maxBrowseLimit {
		return maxBrowseLimit
	}
	return limit
}

func (f SessionFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Tool != "" {
		conds = append(conds, `s.tool = ?`)
		args = append(args, f.Tool)
	}
	if f.Project != "" {
		conds = append(conds, `instr(lower(s.cwd), lower(?)) > 0`)
		args = append(args, f.Project)
	}
	if f.Model != "" {
		conds = append(conds, `s.model = ?`)
		args = append(args, f.Model)
	}
	if f.Kind != "" {
		conds = append(conds, `s.kind = ?`)
		args = append(args, f.Kind)
	}
	if !f.Since.IsZero() {
		conds = append(conds, `s.started_at >= ?`)
		args = append(args, stamp(f.Since))
	}
	if !f.Until.IsZero() {
		conds = append(conds, `s.started_at < ?`)
		args = append(args, stamp(f.Until))
	}
	if f.HasError != nil {
		if *f.HasError {
			conds = append(conds, `COALESCE(c.errs, 0) > 0`)
		} else {
			conds = append(conds, `COALESCE(c.errs, 0) = 0`)
		}
	}
	if f.HasEnrichment != nil {
		if *f.HasEnrichment {
			conds = append(conds, `en.id IS NOT NULL`)
		} else {
			conds = append(conds, `en.id IS NULL`)
		}
	}
	if f.Tag != "" {
		conds = append(conds, `en.id IS NOT NULL AND EXISTS (SELECT 1 FROM json_each(COALESCE(NULLIF(en.result_json, ''), '{}'), '$.topics') j WHERE j.value = ?)`)
		args = append(args, f.Tag)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func scanSummary(rows *sql.Rows, withSnippet bool) (SessionSummary, error) {
	var s SessionSummary
	var title, result string
	dest := []any{&s.ID, &s.Tool, &s.Kind, &s.ParentID, &s.AgentType, &s.Model, &s.Project, &s.GitBranch,
		&title, &s.StartedAt, &s.EndedAt, &s.Turns, &s.Tokens, &s.ToolCalls, &s.Errors, &result}
	if withSnippet {
		dest = append(dest, &s.Snippet)
	}
	if err := rows.Scan(dest...); err != nil {
		return s, err
	}
	s.Title = title
	if result != "" {
		if e, ok := parseEnrichment(result); ok {
			s.Enriched = true
			s.Tags = e.Tags
			if e.Title != "" {
				s.Title = e.Title
			}
		}
	}
	return s, nil
}

// EnrichmentView is the stored Tier 1 result as shown to the owner. Citations
// are tx_turns.seq values.
type EnrichmentView struct {
	Title       string   `json:"title,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	SessionType string   `json:"session_type,omitempty"`
	Outcome     string   `json:"outcome,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Decisions   []Claim  `json:"decisions,omitempty"`
	FollowUps   []Claim  `json:"follow_ups,omitempty"`
}

// parseEnrichment reads a stored Tier 1 result, whose topics become tags.
func parseEnrichment(resultJSON string) (EnrichmentView, bool) {
	var raw struct {
		Title       string   `json:"title"`
		Summary     string   `json:"summary"`
		SessionType string   `json:"session_type"`
		Outcome     string   `json:"outcome"`
		Topics      []string `json:"topics"`
		Decisions   []Claim  `json:"decisions"`
		FollowUps   []Claim  `json:"follow_ups"`
	}
	if json.Unmarshal([]byte(resultJSON), &raw) != nil {
		return EnrichmentView{}, false
	}
	return EnrichmentView{Title: raw.Title, Summary: raw.Summary, SessionType: raw.SessionType, Outcome: raw.Outcome,
		Tags: raw.Topics, Decisions: raw.Decisions, FollowUps: raw.FollowUps}, true
}

type Claim struct {
	Text  string `json:"text"`
	Turns []int  `json:"turns,omitempty"`
}

// fillTitles gives untitled sessions the first line of their first user turn.
func (s *Store) fillTitles(ctx context.Context, items []SessionSummary) {
	for i := range items {
		if items[i].Title != "" && items[i].Title != items[i].ID {
			continue
		}
		var text string
		_ = s.db.QueryRowContext(ctx, `SELECT text_redacted FROM tx_turns WHERE session_pk = ? AND role = 'user' AND text_redacted <> '' ORDER BY seq LIMIT 1`, items[i].ID).Scan(&text)
		items[i].Title = firstLine(text, 140)
	}
}

func firstLine(text string, max int) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	return clip(text, max)
}

// ListSessions returns a filtered, sorted page of sessions.
func (s *Store) ListSessions(ctx context.Context, f SessionFilter) (SessionPage, error) {
	if s == nil {
		return SessionPage{}, ErrPrivateStoreRequired
	}
	limit := clampLimit(f.Limit)
	offset := decodeCursor(f.Cursor)
	col, ok := sortColumns[f.Sort]
	if !ok {
		col = sortColumns["started"]
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	where, args := f.where()
	q := listSelect + where + ` ORDER BY ` + col + ` ` + dir + `, s.session_pk LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return SessionPage{}, errors.New("transcript: cannot list sessions")
	}
	defer rows.Close()
	var page SessionPage
	for rows.Next() {
		item, err := scanSummary(rows, false)
		if err != nil {
			return SessionPage{}, errors.New("transcript: cannot read sessions")
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return SessionPage{}, errors.New("transcript: cannot read sessions")
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = encodeCursor(offset + limit)
	}
	s.fillTitles(ctx, page.Items)
	if page.Items == nil {
		page.Items = []SessionSummary{}
	}
	return page, nil
}

// ftsQuery turns free text into a safe FTS5 query: each word is quoted so
// operators and punctuation in the input cannot change the query's meaning.
func ftsQuery(text string) string {
	var terms []string
	for _, word := range strings.Fields(text) {
		word = strings.ReplaceAll(word, `"`, ``)
		if word != "" {
			terms = append(terms, `"`+word+`"`)
		}
	}
	return strings.Join(terms, " ")
}

// SearchSessions runs full-text search over the redacted session text. The
// filter narrows the matches; Sort is ignored in favour of relevance.
func (s *Store) SearchSessions(ctx context.Context, text string, f SessionFilter) (SessionPage, error) {
	if s == nil {
		return SessionPage{}, ErrPrivateStoreRequired
	}
	match := ftsQuery(text)
	if match == "" {
		return SessionPage{Items: []SessionSummary{}}, nil
	}
	limit := clampLimit(f.Limit)
	offset := decodeCursor(f.Cursor)
	where, args := f.where()
	query := `SELECT ` + summaryCols + `,
 snippet(transcript_search, 2, '[', ']', '…', 24)
 FROM transcript_search` + fmt.Sprintf(summaryJoins, "transcript_search.id") + `
 WHERE transcript_search MATCH ?`
	margs := []any{match}
	if where != "" {
		query += ` AND ` + strings.TrimPrefix(where, " WHERE ")
		margs = append(margs, args...)
	}
	query += ` ORDER BY rank, s.session_pk LIMIT ? OFFSET ?`
	margs = append(margs, limit+1, offset)
	rows, err := s.db.QueryContext(ctx, query, margs...)
	if err != nil {
		return SessionPage{}, errors.New("transcript: cannot search sessions")
	}
	defer rows.Close()
	var page SessionPage
	for rows.Next() {
		item, err := scanSummary(rows, true)
		if err != nil {
			return SessionPage{}, errors.New("transcript: cannot read search results")
		}
		item.Snippet = clip(item.Snippet, maxSnippetBytes)
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return SessionPage{}, errors.New("transcript: cannot read search results")
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = encodeCursor(offset + limit)
	}
	s.fillTitles(ctx, page.Items)
	if page.Items == nil {
		page.Items = []SessionSummary{}
	}
	return page, nil
}

// Facets lists the distinct values the filter bar offers.
type Facets struct {
	Tools    []string `json:"tools"`
	Models   []string `json:"models"`
	Projects []string `json:"projects"`
	Tags     []string `json:"tags"`
	Total    int      `json:"total"`
}

func (s *Store) distinct(ctx context.Context, column string) []string {
	out := []string{}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT `+column+` FROM tx_sessions WHERE `+column+` <> '' ORDER BY `+column+` LIMIT 500`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// enrichmentTags lists the most used topics across current enrichments.
func (s *Store) enrichmentTags(ctx context.Context) []string {
	out := []string{}
	rows, err := s.db.QueryContext(ctx, `SELECT j.value FROM tx_enrichment en, json_each(COALESCE(NULLIF(en.result_json, ''), '{}'), '$.topics') j
 WHERE en.tier = 1 AND en.status = 'ok' AND en.id = (SELECT e.id FROM tx_enrichment e WHERE e.session_pk = en.session_pk AND e.tier = 1 AND e.status = 'ok' ORDER BY e.created_at DESC, e.id DESC LIMIT 1)
 GROUP BY j.value ORDER BY COUNT(*) DESC, j.value LIMIT 200`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (s *Store) SessionFacets(ctx context.Context) (Facets, error) {
	if s == nil {
		return Facets{}, ErrPrivateStoreRequired
	}
	f := Facets{Tools: s.distinct(ctx, "tool"), Models: s.distinct(ctx, "model"), Projects: s.distinct(ctx, "cwd")}
	f.Tags = s.enrichmentTags(ctx)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_sessions`).Scan(&f.Total); err != nil {
		return Facets{}, errors.New("transcript: cannot count sessions")
	}
	return f, nil
}

// Turn is one stored turn. Thinking text is never stored, so only its presence
// and length are reported.
type Turn struct {
	Seq           int            `json:"seq"`
	Role          string         `json:"role"`
	TS            string         `json:"ts,omitempty"`
	IsSidechain   bool           `json:"is_sidechain,omitempty"`
	StopReason    string         `json:"stop_reason,omitempty"`
	Text          string         `json:"text"`
	TextTruncated bool           `json:"text_truncated,omitempty"`
	HasThinking   bool           `json:"has_thinking,omitempty"`
	ThinkingBytes int            `json:"thinking_bytes,omitempty"`
	EventID       string         `json:"event_id"`
	ToolCalls     []ToolCallMeta `json:"tool_calls,omitempty"`
}

// ToolCallMeta is the metadata view of a call: name, status and result size.
type ToolCallMeta struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	ResultBytes int    `json:"result_bytes"`
	StartedAt   string `json:"started_at,omitempty"`
	EndedAt     string `json:"ended_at,omitempty"`
}

type UsageView struct {
	Requests      int   `json:"requests"`
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_tokens"`
	CacheCreation int64 `json:"cache_creation_tokens"`
}

// TreeNode is a subagent in the session's spawn tree.
type TreeNode struct {
	SessionSummary
	Children []*TreeNode `json:"children,omitempty"`
}

type SessionDetail struct {
	Session   SessionSummary `json:"session"`
	Usage     UsageView      `json:"usage"`
	Turns     []Turn         `json:"turns"`
	TurnTotal int            `json:"turn_total"`
	NextTurn  int            `json:"next_turn,omitempty"`
	Subagents []*TreeNode    `json:"subagents"`
	// Enrichment is present only once the enrichment pipeline has run.
	Enrichment *EnrichmentView `json:"enrichment,omitempty"`
}

// SessionDetail returns the session, a page of its turns and its subagent tree.
func (s *Store) SessionDetail(ctx context.Context, id string, turnOffset, turnLimit int) (SessionDetail, error) {
	if s == nil {
		return SessionDetail{}, ErrPrivateStoreRequired
	}
	if turnOffset < 0 {
		turnOffset = 0
	}
	if turnLimit <= 0 || turnLimit > maxTurnPage {
		turnLimit = 200
	}
	rows, err := s.db.QueryContext(ctx, listSelect+` WHERE s.session_pk = ?`, id)
	if err != nil {
		return SessionDetail{}, errors.New("transcript: cannot read session")
	}
	var d SessionDetail
	found := false
	if rows.Next() {
		d.Session, err = scanSummary(rows, false)
		found = err == nil
	}
	rows.Close()
	if !found {
		return SessionDetail{}, ErrSessionNotFound
	}
	one := []SessionSummary{d.Session}
	s.fillTitles(ctx, one)
	d.Session = one[0]

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
 COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0) FROM tx_usage WHERE session_pk = ?`, id).
		Scan(&d.Usage.Requests, &d.Usage.Input, &d.Usage.Output, &d.Usage.CacheRead, &d.Usage.CacheCreation); err != nil {
		return SessionDetail{}, errors.New("transcript: cannot read usage")
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, id).Scan(&d.TurnTotal)

	calls, err := s.callsByTurn(ctx, id)
	if err != nil {
		return SessionDetail{}, err
	}
	trows, err := s.db.QueryContext(ctx, `SELECT seq, event_uuid, role, ts, is_sidechain, stop_reason, text_redacted, has_thinking, thinking_bytes
 FROM tx_turns WHERE session_pk = ? ORDER BY seq LIMIT ? OFFSET ?`, id, turnLimit, turnOffset)
	if err != nil {
		return SessionDetail{}, errors.New("transcript: cannot read turns")
	}
	defer trows.Close()
	d.Turns = []Turn{}
	for trows.Next() {
		var t Turn
		var side, think int
		if err := trows.Scan(&t.Seq, &t.EventID, &t.Role, &t.TS, &side, &t.StopReason, &t.Text, &think, &t.ThinkingBytes); err != nil {
			return SessionDetail{}, errors.New("transcript: cannot read turns")
		}
		t.IsSidechain = side != 0
		t.HasThinking = think != 0
		if len(t.Text) > maxTurnTextRead {
			t.Text = clip(t.Text, maxTurnTextRead)
			t.TextTruncated = true
		}
		t.ToolCalls = calls[t.EventID]
		d.Turns = append(d.Turns, t)
	}
	if err := trows.Err(); err != nil {
		return SessionDetail{}, errors.New("transcript: cannot read turns")
	}
	if turnOffset+len(d.Turns) < d.TurnTotal {
		d.NextTurn = turnOffset + len(d.Turns)
	}
	d.Subagents = s.subagentTree(ctx, id)
	if rec, ok, err := s.LatestEnrichment(ctx, id, 1); err == nil && ok {
		if e, ok := parseEnrichment(rec.ResultJSON); ok {
			d.Enrichment = &e
		}
	}
	return d, nil
}

func (s *Store) callsByTurn(ctx context.Context, id string) (map[string][]ToolCallMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT turn_uuid, name, result_status, result_bytes, started_at, ended_at FROM tx_tool_calls WHERE session_pk = ? ORDER BY started_at, tool_use_id`, id)
	if err != nil {
		return nil, errors.New("transcript: cannot read tool calls")
	}
	defer rows.Close()
	out := map[string][]ToolCallMeta{}
	for rows.Next() {
		var turn string
		var c ToolCallMeta
		if err := rows.Scan(&turn, &c.Name, &c.Status, &c.ResultBytes, &c.StartedAt, &c.EndedAt); err != nil {
			return nil, errors.New("transcript: cannot read tool calls")
		}
		out[turn] = append(out[turn], c)
	}
	return out, rows.Err()
}

// subagentTree walks parent_session_pk breadth-first below id, bounded so a
// malformed cycle or huge fan-out cannot run away.
func (s *Store) subagentTree(ctx context.Context, id string) []*TreeNode {
	roots := []*TreeNode{}
	nodes := map[string]*TreeNode{id: {}}
	seen := 0
	queue := []string{id}
	for len(queue) > 0 && seen < maxTreeNodes {
		parent := queue[0]
		queue = queue[1:]
		rows, err := s.db.QueryContext(ctx, listSelect+` WHERE s.parent_session_pk = ? ORDER BY s.started_at, s.session_pk`, parent)
		if err != nil {
			break
		}
		var kids []SessionSummary
		for rows.Next() {
			if item, err := scanSummary(rows, false); err == nil {
				kids = append(kids, item)
			}
		}
		rows.Close()
		s.fillTitles(ctx, kids)
		for _, k := range kids {
			if _, dup := nodes[k.ID]; dup || seen >= maxTreeNodes {
				continue
			}
			seen++
			n := &TreeNode{SessionSummary: k}
			nodes[k.ID] = n
			if parent == id {
				roots = append(roots, n)
			} else {
				nodes[parent].Children = append(nodes[parent].Children, n)
			}
			queue = append(queue, k.ID)
		}
	}
	return roots
}
