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

	// Reads share the database file with the ingest writers, and in DELETE
	// journal mode a reader blocks their commits, so every read is short.
	searchDeadline = 4 * time.Second
	snippetBudget  = 700 * time.Millisecond
	// snippet() reads the whole document and cannot be interrupted partway, so
	// documents past this size are listed without a snippet.
	maxSnippetDocBytes = 256 << 10
	maxSuggestLimit    = 50
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
	// TurnOffset and TurnSeq locate the best-matching turn of a text search; Highlight is that turn's snippet
	// with the matched spans as rune ranges, so a reader never has to parse the bracketed snippet.
	TurnOffset *int       `json:"turn_offset,omitempty"`
	TurnSeq    *int       `json:"turn_seq,omitempty"`
	Highlight  *Highlight `json:"highlight,omitempty"`
	// Score is cosine similarity (semantic) or the fused rank score (hybrid).
	Score float64 `json:"score,omitempty"`
	// Match says which field matched a summary, semantic or hybrid search.
	Match *Match `json:"match,omitempty"`
	// Enrichment-derived, present only once the enrichment pipeline has run.
	Enriched bool     `json:"enriched,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

type SessionPage struct {
	Items      []SessionSummary `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
	// Hint is set when a search was too broad to answer within its time limit.
	Hint string `json:"hint,omitempty"`
	// SnippetsSkipped counts results returned without a snippet because the
	// snippet time budget ran out.
	SnippetsSkipped int `json:"snippets_skipped,omitempty"`
	// Mode is the search mode that produced the items, which differs from the
	// one requested when a mode was unavailable.
	Mode string `json:"mode,omitempty"`
	// Notice is a stable code explaining a fallback or an empty semantic result.
	Notice string `json:"notice,omitempty"`
}

// HintTooBroad asks the owner to narrow a search that could not finish in time.
const HintTooBroad = "too_broad"

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

func scanSummary(rows *sql.Rows, extra ...any) (SessionSummary, error) {
	s, _, err := scanSummaryRaw(rows, extra...)
	return s, err
}

// scanSummaryRaw also returns the stored Tier 1 result, for callers that match
// against its fields.
func scanSummaryRaw(rows *sql.Rows, extra ...any) (SessionSummary, string, error) {
	var s SessionSummary
	var title, result string
	dest := []any{&s.ID, &s.Tool, &s.Kind, &s.ParentID, &s.AgentType, &s.Model, &s.Project, &s.GitBranch,
		&title, &s.StartedAt, &s.EndedAt, &s.Turns, &s.Tokens, &s.ToolCalls, &s.Errors, &result}
	dest = append(dest, extra...)
	if err := rows.Scan(dest...); err != nil {
		return s, "", err
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
	return s, result, nil
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
		if items[i].Title == "" {
			items[i].Title = untitled(items[i])
		}
	}
}

// untitled names a session with no title and no user text by what is known
// about it: its tool and start date.
func untitled(item SessionSummary) string {
	parts := []string{"Untitled session"}
	if item.Tool != "" {
		parts = append(parts, item.Tool)
	}
	if len(item.StartedAt) >= 10 {
		parts = append(parts, item.StartedAt[:10])
	}
	return strings.Join(parts, " · ")
}

func firstLine(text string, max int) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(clip(text, max))
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
		item, err := scanSummary(rows)
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
//
// Ranking is cheap, but snippet() re-reads each matched document and a few
// documents are megabytes, so results are ranked first and snippets are filled
// in rank order inside a time budget; the rest come back without one. The whole
// search is bounded so a too-broad query returns a hint instead of holding the
// database's read lock.
func (s *Store) SearchSessions(ctx context.Context, text string, f SessionFilter) (SessionPage, error) {
	if s == nil {
		return SessionPage{}, ErrPrivateStoreRequired
	}
	match := ftsQuery(text)
	if match == "" {
		return SessionPage{Items: []SessionSummary{}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, searchDeadline)
	defer cancel()
	limit := clampLimit(f.Limit)
	offset := decodeCursor(f.Cursor)
	where, args := f.where()
	query := `SELECT ` + summaryCols + `, transcript_search.rowid, COALESCE(octet_length(t.content), 0)
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
		return searchFailure(ctx)
	}
	defer rows.Close()
	var page SessionPage
	var rowids, sizes []int64
	for rows.Next() {
		var rowid, size int64
		item, err := scanSummary(rows, &rowid, &size)
		if err != nil {
			return searchFailure(ctx)
		}
		page.Items = append(page.Items, item)
		rowids = append(rowids, rowid)
		sizes = append(sizes, size)
	}
	if err := rows.Err(); err != nil {
		return searchFailure(ctx)
	}
	rows.Close()
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		rowids = rowids[:limit]
		sizes = sizes[:limit]
		page.NextCursor = encodeCursor(offset + limit)
	}
	budget, stop := context.WithTimeout(ctx, snippetBudget)
	defer stop()
	for i, rowid := range rowids {
		if sizes[i] > maxSnippetDocBytes {
			page.SnippetsSkipped++
			continue
		}
		if budget.Err() != nil {
			page.SnippetsSkipped++
			continue
		}
		var snip string
		err := s.db.QueryRowContext(budget, `SELECT snippet(transcript_search, 2, '[', ']', '…', 24) FROM transcript_search
 WHERE rowid = ? AND transcript_search MATCH ?`, rowid, match).Scan(&snip)
		if err != nil {
			page.SnippetsSkipped++
			continue
		}
		page.Items[i].Snippet = clip(snip, maxSnippetBytes)
		page.Items[i].Match = &Match{Field: "transcript", Excerpt: page.Items[i].Snippet}
	}
	ids := make([]string, len(page.Items))
	for i := range page.Items {
		ids[i] = page.Items[i].ID
	}
	if hits := s.bestTurns(budget, match, ids); len(hits) > 0 {
		for i := range page.Items {
			if h, ok := hits[page.Items[i].ID]; ok {
				seq, off, hl := h.seq, h.offset, h.highlight
				page.Items[i].TurnSeq, page.Items[i].TurnOffset, page.Items[i].Highlight = &seq, &off, &hl
			}
		}
	}
	page.Mode = ModeText
	s.fillTitles(ctx, page.Items)
	if page.Items == nil {
		page.Items = []SessionSummary{}
	}
	return page, nil
}

// searchFailure turns a deadline into the too-broad hint and anything else into
// a generic error.
func searchFailure(ctx context.Context) (SessionPage, error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return SessionPage{Items: []SessionSummary{}, Hint: HintTooBroad}, nil
	}
	return SessionPage{}, errors.New("transcript: cannot search sessions")
}

// ProjectCount is a working directory and how many sessions ran in it.
type ProjectCount struct {
	Project  string `json:"project"`
	Sessions int    `json:"sessions"`
}

// SuggestProjects returns working directories containing q (case-insensitive),
// most used first. It backs search-as-you-type, so every project is reachable
// without sending the whole list.
func (s *Store) SuggestProjects(ctx context.Context, q string, limit int) ([]ProjectCount, error) {
	if s == nil {
		return nil, ErrPrivateStoreRequired
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxSuggestLimit {
		limit = maxSuggestLimit
	}
	ctx, cancel := context.WithTimeout(ctx, searchDeadline)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT cwd, COUNT(*) FROM tx_sessions WHERE cwd <> '' AND instr(lower(cwd), lower(?)) > 0
 GROUP BY cwd ORDER BY COUNT(*) DESC, cwd LIMIT ?`, q, limit)
	if err != nil {
		return nil, errors.New("transcript: cannot suggest projects")
	}
	defer rows.Close()
	out := []ProjectCount{}
	for rows.Next() {
		var p ProjectCount
		if err := rows.Scan(&p.Project, &p.Sessions); err != nil {
			return nil, errors.New("transcript: cannot suggest projects")
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Facets lists the distinct values the filter bar offers.
type Facets struct {
	Tools  []string `json:"tools"`
	Models []string `json:"models"`
	// ProjectCount is how many distinct working directories exist; use the
	// project suggestions route to find them.
	ProjectCount int      `json:"project_count"`
	Tags         []string `json:"tags"`
	// TagCounts is Tags with how many enriched sessions carry each topic.
	TagCounts []TagCount `json:"tag_counts"`
	// Enriched is how many sessions have a current ok Tier 1 result.
	Enriched int `json:"enriched"`
	Total    int `json:"total"`
}

// TagCount is an enrichment topic and the sessions that carry it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
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
func (s *Store) enrichmentTags(ctx context.Context) []TagCount {
	out := []TagCount{}
	rows, err := s.db.QueryContext(ctx, `SELECT j.value, COUNT(*) FROM tx_enrichment en JOIN tx_sessions xs ON xs.session_pk = en.session_pk, json_each(COALESCE(NULLIF(en.result_json, ''), '{}'), '$.topics') j
 WHERE en.tier = 1 AND en.status = 'ok' AND en.id = (SELECT e.id FROM tx_enrichment e WHERE e.session_pk = en.session_pk AND e.tier = 1 AND e.status = 'ok' ORDER BY e.created_at DESC, e.id DESC LIMIT 1)
 GROUP BY j.value ORDER BY COUNT(*) DESC, j.value LIMIT 200`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var t TagCount
		if rows.Scan(&t.Tag, &t.Count) == nil {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) SessionFacets(ctx context.Context) (Facets, error) {
	if s == nil {
		return Facets{}, ErrPrivateStoreRequired
	}
	f := Facets{Tools: s.distinct(ctx, "tool"), Models: s.distinct(ctx, "model")}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT cwd) FROM tx_sessions WHERE cwd <> ''`).Scan(&f.ProjectCount)
	f.TagCounts = s.enrichmentTags(ctx)
	f.Tags = make([]string, len(f.TagCounts))
	for i, t := range f.TagCounts {
		f.Tags[i] = t.Tag
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT e.session_pk) FROM tx_enrichment e JOIN tx_sessions xs ON xs.session_pk = e.session_pk WHERE e.tier = 1 AND e.status = 'ok'`).Scan(&f.Enriched)
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
	// Target is what the call acted on (path, command start, URL host, pattern), at most 200 bytes.
	Target string `json:"target,omitempty"`
	// ErrorExcerpt is the redacted start of a failed call's result, present only for status "error" and only when
	// the source kept one (tool_result_excerpt_on_error_bytes).
	ErrorExcerpt string `json:"error_excerpt,omitempty"`
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
	// UnattachedCalls are tool rows whose turn is not in the capture (a result
	// whose call was never seen), so the per-turn lists plus this one add up to
	// the list's tool_calls count.
	UnattachedCalls []ToolCallMeta `json:"unattached_calls,omitempty"`
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
		d.Session, err = scanSummary(rows)
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
	d.UnattachedCalls = s.unattachedCalls(ctx, id)
	if rec, ok, err := s.LatestEnrichment(ctx, id, 1); err == nil && ok {
		if e, ok := parseEnrichment(rec.ResultJSON); ok {
			d.Enrichment = &e
		}
	}
	return d, nil
}

func (s *Store) callsByTurn(ctx context.Context, id string) (map[string][]ToolCallMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT turn_uuid, name, result_status, result_bytes, started_at, ended_at, target_redacted, CASE WHEN result_status = 'error' THEN result_excerpt_redacted ELSE '' END FROM tx_tool_calls WHERE session_pk = ? ORDER BY started_at, tool_use_id`, id)
	if err != nil {
		return nil, errors.New("transcript: cannot read tool calls")
	}
	defer rows.Close()
	out := map[string][]ToolCallMeta{}
	for rows.Next() {
		var turn string
		var c ToolCallMeta
		if err := rows.Scan(&turn, &c.Name, &c.Status, &c.ResultBytes, &c.StartedAt, &c.EndedAt, &c.Target, &c.ErrorExcerpt); err != nil {
			return nil, errors.New("transcript: cannot read tool calls")
		}
		out[turn] = append(out[turn], c)
	}
	return out, rows.Err()
}

// unattachedCalls lists tool rows that match none of the session's turns.
func (s *Store) unattachedCalls(ctx context.Context, id string) []ToolCallMeta {
	rows, err := s.db.QueryContext(ctx, `SELECT name, result_status, result_bytes, started_at, ended_at, target_redacted, CASE WHEN result_status = 'error' THEN result_excerpt_redacted ELSE '' END FROM tx_tool_calls
 WHERE session_pk = ? AND turn_uuid NOT IN (SELECT event_uuid FROM tx_turns WHERE session_pk = ?) ORDER BY started_at, tool_use_id LIMIT ?`, id, id, maxTreeNodes)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ToolCallMeta
	for rows.Next() {
		var c ToolCallMeta
		if rows.Scan(&c.Name, &c.Status, &c.ResultBytes, &c.StartedAt, &c.EndedAt, &c.Target, &c.ErrorExcerpt) == nil {
			out = append(out, c)
		}
	}
	return out
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
			if item, err := scanSummary(rows); err == nil {
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
