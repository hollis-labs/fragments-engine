package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
	"github.com/hollis-labs/libs/core/store"
)

// Postgres implementation of transcript.BrowseBackend: the read side behind FE's
// transcript API and CLI. Every method is read-only and bounded by a deadline.
// Differences from the SQLite store, by design:
//   - search covers turn text, thinking, tool input and tool results, not just turns;
//   - there is no owner-only restriction: all agents may read all transcripts;
//   - a session's turn offset counts kind='turn' events only (raw 'step' events are skipped).

var _ transcript.BrowseBackend = (*Service)(nil)

const (
	tokensExpr = `(SELECT COALESCE(SUM(x.input_tokens + x.output_tokens + x.cache_read_tokens + x.cache_creation_tokens), 0)::bigint FROM usage x WHERE x.session_id = s.session_id)`
	callsExpr  = `(SELECT count(*) FROM tool_calls x WHERE x.session_id = s.session_id)`
	errsExpr   = `(SELECT count(*) FROM tool_calls x WHERE x.session_id = s.session_id AND x.status = 'error')`

	// enJoin attaches the newest ok Tier 1 enrichment (en.result is jsonb).
	enJoin = ` LEFT JOIN LATERAL (SELECT e.enrichment_id, e.result_redacted AS result FROM enrichments e
 WHERE e.session_id = s.session_id AND e.tier = 1 AND e.status = 'ok' ORDER BY e.created_at DESC, e.enrichment_id DESC LIMIT 1) en ON true`

	summaryCols = `s.session_id, s.harness, s.kind, COALESCE(s.parent_session_id, ''), s.agent_type, COALESCE(s.model, ''), COALESCE(s.cwd_redacted, ''),
 COALESCE(s.git_branch_redacted, ''), COALESCE((SELECT lm.title FROM legacy_material lm WHERE lm.session_id = s.session_id ORDER BY lm.legacy_id LIMIT 1), ''),
 s.started_at, s.ended_at, s.legacy_turn_count, ` + tokensExpr + `, ` + callsExpr + `, ` + errsExpr + `, COALESCE(en.result::text, '')`

	// A text search reads whole sessions' worth of chunks; keep headlines short.
	snippetWords = 24
)

var sortExprs = map[string]string{
	"started":    "s.started_at",
	"turns":      "s.legacy_turn_count",
	"tokens":     tokensExpr,
	"tool_calls": callsExpr,
	"errors":     errsExpr,
}

func ts(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return transcript.Stamp(t.Time)
}

// filterSQL renders the filter as conditions over s (sessions) and en (enrichJoin).
func filterSQL(f transcript.SessionFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Tool != "" {
		conds = append(conds, `s.harness = ?`)
		args = append(args, f.Tool)
	}
	if f.Project != "" {
		conds = append(conds, `position(lower(?) in lower(COALESCE(s.cwd_redacted, ''))) > 0`)
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
		args = append(args, f.Since.UTC())
	}
	if !f.Until.IsZero() {
		conds = append(conds, `s.started_at < ?`)
		args = append(args, f.Until.UTC())
	}
	if f.HasError != nil {
		if *f.HasError {
			conds = append(conds, errsExpr+` > 0`)
		} else {
			conds = append(conds, errsExpr+` = 0`)
		}
	}
	if f.HasEnrichment != nil {
		if *f.HasEnrichment {
			conds = append(conds, `en.enrichment_id IS NOT NULL`)
		} else {
			conds = append(conds, `en.enrichment_id IS NULL`)
		}
	}
	if f.Tag != "" {
		conds = append(conds, `en.enrichment_id IS NOT NULL AND jsonb_typeof(en.result->'topics') = 'array' AND jsonb_exists(en.result->'topics', ?)`)
		args = append(args, f.Tag)
	}
	return strings.Join(conds, " AND "), args
}

func and(where string) string {
	if where == "" {
		return ""
	}
	return " AND " + where
}

func scanSummary(rows *sql.Rows, extra ...any) (transcript.SessionSummary, string, error) {
	var s transcript.SessionSummary
	var title, result string
	var started, ended sql.NullTime
	dest := []any{&s.ID, &s.Tool, &s.Kind, &s.ParentID, &s.AgentType, &s.Model, &s.Project, &s.GitBranch,
		&title, &started, &ended, &s.Turns, &s.Tokens, &s.ToolCalls, &s.Errors, &result}
	dest = append(dest, extra...)
	if err := rows.Scan(dest...); err != nil {
		return s, "", err
	}
	s.StartedAt, s.EndedAt, s.Title = ts(started), ts(ended), title
	if result != "" {
		if e, ok := transcript.ParseEnrichment(result); ok {
			s.Enriched = true
			s.Tags = e.Tags
			if e.Title != "" {
				s.Title = e.Title
			}
		}
	}
	return s, result, nil
}

// fillTitles gives untitled sessions the first line of their first user turn.
func fillTitles(ctx context.Context, q store.Querier, items []transcript.SessionSummary) {
	for i := range items {
		if items[i].Title != "" && items[i].Title != items[i].ID {
			continue
		}
		var text string
		_ = q.QueryRowContext(ctx, `SELECT text_redacted FROM events WHERE session_id = ? AND kind = 'turn' AND role = 'user' AND text_redacted <> '' ORDER BY ordinal LIMIT 1`, items[i].ID).Scan(&text)
		items[i].Title = transcript.FirstLine(text, 140)
		if items[i].Title == "" {
			items[i].Title = transcript.Untitled(items[i])
		}
	}
}

func readErr(what string) error { return errors.New("transcript: cannot " + what) }

// deadlineHint turns an expired deadline into the too-broad hint.
func deadlineHint(ctx context.Context, mode string) (transcript.SessionPage, bool) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return transcript.SessionPage{Items: []transcript.SessionSummary{}, Hint: transcript.HintTooBroad, Mode: mode}, true
	}
	return transcript.SessionPage{}, false
}

// ListSessions returns a filtered, sorted page of sessions.
func (s *Service) ListSessions(ctx context.Context, f transcript.SessionFilter) (transcript.SessionPage, error) {
	limit := transcript.ClampLimit(f.Limit)
	offset := transcript.DecodeCursor(f.Cursor)
	col, ok := sortExprs[f.Sort]
	if !ok {
		col = sortExprs["started"]
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	where, args := filterSQL(f)
	q := `SELECT ` + summaryCols + ` FROM sessions s` + enJoin
	if where != "" {
		q += ` WHERE ` + where
	}
	q += ` ORDER BY ` + col + ` ` + dir + `, s.session_id LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	var page transcript.SessionPage
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return readErr("list sessions")
		}
		defer rows.Close()
		for rows.Next() {
			item, _, err := scanSummary(rows)
			if err != nil {
				return readErr("read sessions")
			}
			page.Items = append(page.Items, item)
		}
		if err := rows.Err(); err != nil {
			return readErr("read sessions")
		}
		rows.Close()
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextCursor = transcript.EncodeCursor(offset + limit)
		}
		fillTitles(ctx, tx, page.Items)
		return nil
	})
	if err != nil {
		return transcript.SessionPage{}, err
	}
	if page.Items == nil {
		page.Items = []transcript.SessionSummary{}
	}
	return page, nil
}

// ---- text search ----------------------------------------------------------

// tsQuery is the query predicate: every word must occur in one chunk (the SQLite
// store required every word in one document). The words go through the same
// unaccent step as the indexed text.
const tsQuery = `plainto_tsquery('simple', gostore_unaccent(?))`

// chunkHit is the best matching chunk of one session.
type chunkHit struct {
	kind, entity, field, text string
}

// SearchSessions runs full-text search over everything stored for a session:
// turn text, thinking, tool inputs and tool results. Results are one row per
// session, ranked by the best chunk; the filter narrows them, Sort is ignored.
func (s *Service) SearchSessions(ctx context.Context, text string, f transcript.SessionFilter) (transcript.SessionPage, error) {
	terms := transcript.SearchTerms(text)
	if len(terms) == 0 {
		return transcript.SessionPage{Items: []transcript.SessionSummary{}, Mode: transcript.ModeText}, nil
	}
	query := strings.Join(terms, " ")
	ctx, cancel := context.WithTimeout(ctx, transcript.SearchDeadline)
	defer cancel()
	limit := transcript.ClampLimit(f.Limit)
	offset := transcript.DecodeCursor(f.Cursor)
	where, fargs := filterSQL(f)
	q := `WITH q AS (SELECT ` + tsQuery + ` AS tsq),
 hits AS (SELECT d.session_id, d.source_kind, d.entity_key, d.field, c.chunk_idx, c.text_redacted, ts_rank(c.chunk_fts_vec, q.tsq) AS rank
  FROM search_chunks c JOIN search_documents d ON d.document_id = c.document_id, q WHERE c.chunk_fts_vec @@ q.tsq),
 best AS (SELECT DISTINCT ON (session_id) * FROM hits ORDER BY session_id, rank DESC, source_kind, entity_key, chunk_idx)
 SELECT ` + summaryCols + `, b.source_kind, b.entity_key, b.field, b.text_redacted, b.rank
 FROM best b JOIN sessions s ON s.session_id = b.session_id` + enJoin
	args := []any{query}
	if where != "" {
		q += ` WHERE ` + where
		args = append(args, fargs...)
	}
	q += ` ORDER BY b.rank DESC, s.session_id LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)

	var page transcript.SessionPage
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return readErr("search sessions")
		}
		defer rows.Close()
		var hits []chunkHit
		for rows.Next() {
			var h chunkHit
			var rank float64
			item, _, err := scanSummary(rows, &h.kind, &h.entity, &h.field, &h.text, &rank)
			if err != nil {
				return readErr("search sessions")
			}
			item.Score = 0 // relevance orders the list; Score is reserved for semantic/hybrid
			page.Items = append(page.Items, item)
			hits = append(hits, h)
		}
		if err := rows.Err(); err != nil {
			return readErr("search sessions")
		}
		rows.Close()
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			hits = hits[:limit]
			page.NextCursor = transcript.EncodeCursor(offset + limit)
		}
		for i, h := range hits {
			hl := highlightOf(h.text, terms)
			bracketed := bracket(hl)
			page.Items[i].Snippet = transcript.Clip(bracketed, transcript.MaxSnippetBytes)
			page.Items[i].Match = &transcript.Match{Field: matchField(h), Excerpt: page.Items[i].Snippet}
			if owner, ok := ownerTurn(ctx, tx, page.Items[i].ID, h); ok {
				seq, off := owner.seq, owner.offset
				page.Items[i].TurnSeq, page.Items[i].TurnOffset = &seq, &off
				hlc := hl
				page.Items[i].Highlight = &hlc
			}
		}
		fillTitles(ctx, tx, page.Items)
		return nil
	})
	if err != nil {
		if p, ok := deadlineHint(ctx, transcript.ModeText); ok {
			return p, nil
		}
		return transcript.SessionPage{}, err
	}
	page.Mode = transcript.ModeText
	if page.Items == nil {
		page.Items = []transcript.SessionSummary{}
	}
	return page, nil
}

func matchField(h chunkHit) string {
	switch {
	case h.kind == "tool_result":
		return "tool_result"
	case h.kind == "tool_input":
		return "tool_input"
	case h.kind == "event" && h.field == "thinking":
		return "thinking"
	}
	return "transcript"
}

type turnRef struct{ seq, offset int }

// ownerTurn finds the turn a hit belongs to: the event itself, or the event of
// the tool call whose input or result matched.
func ownerTurn(ctx context.Context, q store.Querier, session string, h chunkHit) (turnRef, bool) {
	var ref turnRef
	var err error
	switch h.kind {
	case "event":
		err = q.QueryRowContext(ctx, `SELECT e.ordinal, (SELECT count(*) FROM events p WHERE p.session_id = e.session_id AND p.kind = 'turn' AND p.ordinal < e.ordinal)
 FROM events e WHERE e.session_id = ? AND e.event_id = ? AND e.kind = 'turn'`, session, h.entity).Scan(&ref.seq, &ref.offset)
	case "tool_input", "tool_result":
		err = q.QueryRowContext(ctx, `SELECT e.ordinal, (SELECT count(*) FROM events p WHERE p.session_id = e.session_id AND p.kind = 'turn' AND p.ordinal < e.ordinal)
 FROM tool_calls c JOIN events e ON e.session_id = c.session_id AND e.event_id = c.event_id AND e.kind = 'turn'
 WHERE c.session_id = ? AND (c.call_id = ? OR left(?, length(c.call_id) + 1) = c.call_id || '/') ORDER BY length(c.call_id) DESC LIMIT 1`,
			session, h.entity, h.entity).Scan(&ref.seq, &ref.offset)
	default:
		return ref, false
	}
	return ref, err == nil
}

// highlightOf builds a short window of text around the first matched word, with
// each matched word as a rune range. Matching is case-insensitive on whole
// substrings, which is close to the index's token match.
func highlightOf(text string, terms []string) transcript.Highlight {
	text = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text)
	lower := strings.ToLower(text)
	if len(lower) != len(text) {
		// Case folding moved byte offsets (rare scripts): show the start, unmarked.
		return transcript.Highlight{Text: transcript.Clip(text, transcript.MaxSnippetBytes), Ranges: []transcript.Range{}}
	}
	first := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (first < 0 || i < first) {
			first = i
		}
	}
	start, end := 0, len(text)
	if first > 80 {
		start = first - 80
	}
	if end-start > 2*transcript.MaxSnippetBytes {
		end = start + 2*transcript.MaxSnippetBytes
	}
	for start > 0 && start < len(text) && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	window, wl := text[start:end], lower[start:end]
	h := transcript.Highlight{Ranges: []transcript.Range{}}
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	suffix := ""
	if end < len(text) {
		suffix = "…"
	}
	h.Text = prefix + window + suffix
	marked := make([]bool, len(window))
	for _, t := range terms {
		for from := 0; ; {
			i := strings.Index(wl[from:], t)
			if i < 0 {
				break
			}
			for j := from + i; j < from+i+len(t); j++ {
				marked[j] = true
			}
			from += i + len(t)
		}
	}
	runes := utf8.RuneCountInString(prefix)
	in, rs := false, 0
	for i := 0; i < len(window); {
		_, size := utf8.DecodeRuneInString(window[i:])
		m := marked[i]
		if m && !in {
			in, rs = true, runes
		} else if !m && in {
			h.Ranges = append(h.Ranges, transcript.Range{Start: rs, End: runes})
			in = false
		}
		runes++
		i += size
	}
	if in {
		h.Ranges = append(h.Ranges, transcript.Range{Start: rs, End: runes})
	}
	return h
}

// bracket renders a highlight in the bracketed display form; literal brackets in
// the text become parentheses so the markers stay unambiguous.
func bracket(h transcript.Highlight) string {
	var b strings.Builder
	runes := []rune(strings.NewReplacer("[", "(", "]", ")").Replace(h.Text))
	idx := 0
	for i, r := range runes {
		for idx < len(h.Ranges) && h.Ranges[idx].End == i {
			b.WriteByte(']')
			idx++
		}
		for _, rg := range h.Ranges {
			if rg.Start == i {
				b.WriteByte('[')
			}
		}
		b.WriteRune(r)
	}
	for ; idx < len(h.Ranges); idx++ {
		b.WriteByte(']')
	}
	return b.String()
}

// ---- projects, facets ------------------------------------------------------

// SuggestProjects returns working directories containing q, most used first.
func (s *Service) SuggestProjects(ctx context.Context, q string, limit int) ([]transcript.ProjectCount, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > transcript.MaxSuggestLimit {
		limit = transcript.MaxSuggestLimit
	}
	ctx, cancel := context.WithTimeout(ctx, transcript.SearchDeadline)
	defer cancel()
	out := []transcript.ProjectCount{}
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		rows, err := tx.QueryContext(ctx, `SELECT cwd_redacted, count(*) FROM sessions WHERE COALESCE(cwd_redacted, '') <> '' AND position(lower(?) in lower(cwd_redacted)) > 0
 GROUP BY cwd_redacted ORDER BY count(*) DESC, cwd_redacted LIMIT ?`, q, limit)
		if err != nil {
			return readErr("suggest projects")
		}
		defer rows.Close()
		for rows.Next() {
			var p transcript.ProjectCount
			if err := rows.Scan(&p.Project, &p.Sessions); err != nil {
				return readErr("suggest projects")
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func distinct(ctx context.Context, q store.Querier, expr string) []string {
	out := []string{}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT `+expr+` AS v FROM sessions WHERE COALESCE(`+expr+`, '') <> '' ORDER BY v LIMIT 500`)
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

// latestOK selects the newest ok Tier 1 enrichment per session.
const latestOK = `SELECT DISTINCT ON (e.session_id) e.session_id, e.result_redacted AS result FROM enrichments e
 WHERE e.tier = 1 AND e.status = 'ok' ORDER BY e.session_id, e.created_at DESC, e.enrichment_id DESC`

// SessionFacets lists the distinct values the filter bar offers.
func (s *Service) SessionFacets(ctx context.Context) (transcript.Facets, error) {
	f := transcript.Facets{TagCounts: []transcript.TagCount{}}
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		f.Tools, f.Models = distinct(ctx, tx, "harness"), distinct(ctx, tx, "model")
		_ = tx.QueryRowContext(ctx, `SELECT count(DISTINCT cwd_redacted) FROM sessions WHERE COALESCE(cwd_redacted, '') <> ''`).Scan(&f.ProjectCount)
		if rows, err := tx.QueryContext(ctx, `SELECT t, count(*) FROM (`+latestOK+`) l,
 jsonb_array_elements_text(CASE WHEN jsonb_typeof(l.result->'topics') = 'array' THEN l.result->'topics' ELSE '[]'::jsonb END) AS t
 GROUP BY t ORDER BY count(*) DESC, t LIMIT 200`); err == nil {
			for rows.Next() {
				var c transcript.TagCount
				if rows.Scan(&c.Tag, &c.Count) == nil {
					f.TagCounts = append(f.TagCounts, c)
				}
			}
			rows.Close()
		}
		f.Tags = make([]string, len(f.TagCounts))
		for i, c := range f.TagCounts {
			f.Tags[i] = c.Tag
		}
		_ = tx.QueryRowContext(ctx, `SELECT count(DISTINCT session_id) FROM enrichments WHERE tier = 1 AND status = 'ok'`).Scan(&f.Enriched)
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&f.Total); err != nil {
			return readErr("count sessions")
		}
		return nil
	})
	return f, err
}

// ---- session detail --------------------------------------------------------

// SessionDetail returns the session, a page of its turns and its subagent tree.
func (s *Service) SessionDetail(ctx context.Context, id string, turnOffset, turnLimit int) (transcript.SessionDetail, error) {
	if turnOffset < 0 {
		turnOffset = 0
	}
	if turnLimit <= 0 || turnLimit > transcript.MaxTurnPage {
		turnLimit = 200
	}
	var d transcript.SessionDetail
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+summaryCols+` FROM sessions s`+enJoin+` WHERE s.session_id = ?`, id)
		if err != nil {
			return readErr("read session")
		}
		found := false
		if rows.Next() {
			d.Session, _, err = scanSummary(rows)
			found = err == nil
		}
		rows.Close()
		if !found {
			return transcript.ErrSessionNotFound
		}
		one := []transcript.SessionSummary{d.Session}
		fillTitles(ctx, tx, one)
		d.Session = one[0]
		if err := tx.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(input_tokens), 0)::bigint, COALESCE(SUM(output_tokens), 0)::bigint,
 COALESCE(SUM(cache_read_tokens), 0)::bigint, COALESCE(SUM(cache_creation_tokens), 0)::bigint FROM usage WHERE session_id = ?`, id).
			Scan(&d.Usage.Requests, &d.Usage.Input, &d.Usage.Output, &d.Usage.CacheRead, &d.Usage.CacheCreation); err != nil {
			return readErr("read usage")
		}
		_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE session_id = ? AND kind = 'turn'`, id).Scan(&d.TurnTotal)
		calls, err := callsByTurn(ctx, tx, id)
		if err != nil {
			return err
		}
		trows, err := tx.QueryContext(ctx, `SELECT ordinal, event_id, role, event_at, is_sidechain, stop_reason, text_redacted, has_thinking, thinking_bytes
 FROM events WHERE session_id = ? AND kind = 'turn' ORDER BY ordinal LIMIT ? OFFSET ?`, id, turnLimit, turnOffset)
		if err != nil {
			return readErr("read turns")
		}
		defer trows.Close()
		d.Turns = []transcript.Turn{}
		for trows.Next() {
			var t transcript.Turn
			var at sql.NullTime
			var role sql.NullString
			if err := trows.Scan(&t.Seq, &t.EventID, &role, &at, &t.IsSidechain, &t.StopReason, &t.Text, &t.HasThinking, &t.ThinkingBytes); err != nil {
				return readErr("read turns")
			}
			t.Role, t.TS = role.String, ts(at)
			if len(t.Text) > transcript.MaxTurnTextRead {
				t.Text = transcript.Clip(t.Text, transcript.MaxTurnTextRead)
				t.TextTruncated = true
			}
			t.ToolCalls = calls[t.EventID]
			d.Turns = append(d.Turns, t)
		}
		if err := trows.Err(); err != nil {
			return readErr("read turns")
		}
		trows.Close()
		if turnOffset+len(d.Turns) < d.TurnTotal {
			d.NextTurn = turnOffset + len(d.Turns)
		}
		d.Subagents = subagentTree(ctx, tx, id)
		d.UnattachedCalls = unattachedCalls(ctx, tx, id)
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT result_redacted::text FROM enrichments WHERE session_id = ? AND tier = 1 AND status = 'ok' AND result_redacted IS NOT NULL
 ORDER BY created_at DESC, enrichment_id DESC LIMIT 1`, id).Scan(&raw); err == nil {
			if e, ok := transcript.ParseEnrichment(raw); ok {
				d.Enrichment = &e
			}
		}
		return nil
	})
	return d, err
}

// errExcerpt is the redacted start of a failed call's first open text result.
const errExcerpt = `CASE WHEN c.status = 'error' THEN COALESCE((SELECT left(r.text_redacted, 400) FROM tool_results r
 WHERE r.session_id = c.session_id AND r.call_id = c.call_id AND r.text_redacted IS NOT NULL AND r.read_policy = 'open' ORDER BY r.ordinal, r.part_id LIMIT 1), '') ELSE '' END`

func scanCallMeta(rows *sql.Rows, extra ...any) (transcript.ToolCallMeta, error) {
	var c transcript.ToolCallMeta
	var started, ended sql.NullTime
	var target sql.NullString
	dest := append([]any{}, extra...)
	dest = append(dest, &c.Name, &c.Status, &c.ResultBytes, &started, &ended, &target, &c.ErrorExcerpt)
	err := rows.Scan(dest...)
	c.StartedAt, c.EndedAt, c.Target = ts(started), ts(ended), transcript.Clip(target.String, 200)
	return c, err
}

func callsByTurn(ctx context.Context, q store.Querier, id string) (map[string][]transcript.ToolCallMeta, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.event_id, c.tool_name, c.status, c.reported_result_bytes, c.started_at, c.ended_at, c.target_redacted, `+errExcerpt+`
 FROM tool_calls c WHERE c.session_id = ? ORDER BY c.started_at, c.call_id`, id)
	if err != nil {
		return nil, readErr("read tool calls")
	}
	defer rows.Close()
	out := map[string][]transcript.ToolCallMeta{}
	for rows.Next() {
		var turn string
		c, err := scanCallMeta(rows, &turn)
		if err != nil {
			return nil, readErr("read tool calls")
		}
		out[turn] = append(out[turn], c)
	}
	return out, rows.Err()
}

// unattachedCalls lists tool rows whose event is not one of the session's turns.
func unattachedCalls(ctx context.Context, q store.Querier, id string) []transcript.ToolCallMeta {
	rows, err := q.QueryContext(ctx, `SELECT c.tool_name, c.status, c.reported_result_bytes, c.started_at, c.ended_at, c.target_redacted, `+errExcerpt+`
 FROM tool_calls c WHERE c.session_id = ? AND NOT EXISTS (SELECT 1 FROM events e WHERE e.session_id = c.session_id AND e.event_id = c.event_id AND e.kind = 'turn')
 ORDER BY c.started_at, c.call_id LIMIT ?`, id, transcript.MaxTreeNodes)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []transcript.ToolCallMeta
	for rows.Next() {
		if c, err := scanCallMeta(rows); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// subagentTree walks parent_session_id breadth-first below id, bounded so a
// malformed cycle or huge fan-out cannot run away.
func subagentTree(ctx context.Context, q store.Querier, id string) []*transcript.TreeNode {
	roots := []*transcript.TreeNode{}
	nodes := map[string]*transcript.TreeNode{id: {}}
	seen := 0
	queue := []string{id}
	for len(queue) > 0 && seen < transcript.MaxTreeNodes {
		parent := queue[0]
		queue = queue[1:]
		rows, err := q.QueryContext(ctx, `SELECT `+summaryCols+` FROM sessions s`+enJoin+` WHERE s.parent_session_id = ? ORDER BY s.started_at, s.session_id`, parent)
		if err != nil {
			break
		}
		var kids []transcript.SessionSummary
		for rows.Next() {
			if item, _, err := scanSummary(rows); err == nil {
				kids = append(kids, item)
			}
		}
		rows.Close()
		fillTitles(ctx, q, kids)
		for _, k := range kids {
			if _, dup := nodes[k.ID]; dup || seen >= transcript.MaxTreeNodes {
				continue
			}
			seen++
			n := &transcript.TreeNode{SessionSummary: k}
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

// ---- find in session, errors ----------------------------------------------

func sessionExists(ctx context.Context, q store.Querier, id string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE session_id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return transcript.ErrSessionNotFound
	}
	if err != nil {
		return readErr("read session")
	}
	return nil
}

// FindInSession lists the turns of one session that contain the text, in turn
// order, starting after afterOffset (negative: from the start). A turn matches
// when its own text or thinking, or the input or result of one of its tool
// calls, contains the words.
func (s *Service) FindInSession(ctx context.Context, id, text string, afterOffset, limit int) (transcript.FindResult, error) {
	out := transcript.FindResult{Matches: []transcript.TurnMatch{}}
	terms := transcript.SearchTerms(text)
	if limit <= 0 {
		limit = 20
	}
	if limit > transcript.MaxFindLimit {
		limit = transcript.MaxFindLimit
	}
	ctx, cancel := context.WithTimeout(ctx, transcript.FindDeadline)
	defer cancel()
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		if err := sessionExists(ctx, tx, id); err != nil {
			return err
		}
		if len(terms) == 0 {
			return nil
		}
		type turn struct {
			offset, seq int
			role        string
		}
		turns := map[string]turn{}
		trows, err := tx.QueryContext(ctx, `SELECT event_id, ordinal, COALESCE(role, '') FROM events WHERE session_id = ? AND kind = 'turn' ORDER BY ordinal`, id)
		if err != nil {
			return readErr("search session")
		}
		for n := 0; trows.Next(); n++ {
			var eid string
			var t turn
			if trows.Scan(&eid, &t.seq, &t.role) != nil {
				trows.Close()
				return readErr("search session")
			}
			t.offset = n
			turns[eid] = t
		}
		trows.Close()
		callTurn := map[string]string{}
		crows, err := tx.QueryContext(ctx, `SELECT call_id, event_id FROM tool_calls WHERE session_id = ?`, id)
		if err != nil {
			return readErr("search session")
		}
		for crows.Next() {
			var c, e string
			if crows.Scan(&c, &e) == nil {
				callTurn[c] = e
			}
		}
		crows.Close()
		rows, err := tx.QueryContext(ctx, `WITH q AS (SELECT `+tsQuery+` AS tsq)
 SELECT d.source_kind, d.entity_key, c.text_redacted FROM search_chunks c JOIN search_documents d ON d.document_id = c.document_id, q
 WHERE d.session_id = ? AND c.chunk_fts_vec @@ q.tsq ORDER BY d.source_kind, d.entity_key, d.field, c.chunk_idx`, strings.Join(terms, " "), id)
		if err != nil {
			return readErr("search session")
		}
		defer rows.Close()
		best := map[string]transcript.TurnMatch{}
		for rows.Next() {
			var kind, entity, chunk string
			if err := rows.Scan(&kind, &entity, &chunk); err != nil {
				return readErr("search session")
			}
			eid := ""
			switch kind {
			case "event":
				eid = entity
			case "tool_input", "tool_result":
				call := entity
				if i := strings.LastIndex(entity, "/"); kind == "tool_result" && i >= 0 {
					if _, ok := callTurn[entity]; !ok {
						call = entity[:i]
					}
				}
				eid = callTurn[call]
			}
			t, ok := turns[eid]
			if !ok {
				continue
			}
			if _, have := best[eid]; have {
				continue
			}
			hl := highlightOf(chunk, terms)
			best[eid] = transcript.TurnMatch{TurnOffset: t.offset, Seq: t.seq, Role: t.role, Snippet: bracket(hl), Highlight: hl}
		}
		if err := rows.Err(); err != nil {
			return readErr("search session")
		}
		all := make([]transcript.TurnMatch, 0, len(best))
		for _, m := range best {
			all = append(all, m)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].TurnOffset < all[j].TurnOffset })
		out.Total = len(all)
		for _, m := range all {
			if m.TurnOffset <= afterOffset {
				continue
			}
			if len(out.Matches) == limit {
				out.Truncated = true
				break
			}
			out.Matches = append(out.Matches, m)
		}
		return nil
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return transcript.FindResult{}, transcript.ErrFindTooBroad
		}
		return transcript.FindResult{}, err
	}
	return out, nil
}

// SessionErrors lists the failed tool calls of one session in time order.
func (s *Service) SessionErrors(ctx context.Context, id string) (transcript.ErrorList, error) {
	out := transcript.ErrorList{Items: []transcript.ErrorItem{}}
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		if err := sessionExists(ctx, tx, id); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tool_calls WHERE session_id = ? AND status = 'error'`, id).Scan(&out.Total); err != nil {
			return readErr("read errors")
		}
		rows, err := tx.QueryContext(ctx, `SELECT e.ordinal, c.tool_name, c.target_redacted, `+errExcerpt+`,
 (SELECT count(*) FROM events p WHERE p.session_id = c.session_id AND p.kind = 'turn' AND p.ordinal < e.ordinal)
 FROM tool_calls c LEFT JOIN events e ON e.session_id = c.session_id AND e.event_id = c.event_id AND e.kind = 'turn'
 WHERE c.session_id = ? AND c.status = 'error' ORDER BY c.started_at, c.call_id LIMIT ?`, id, transcript.MaxErrorItems)
		if err != nil {
			return readErr("read errors")
		}
		defer rows.Close()
		for rows.Next() {
			var it transcript.ErrorItem
			var seq, offset sql.NullInt64
			var target sql.NullString
			if err := rows.Scan(&seq, &it.Tool, &target, &it.ErrorExcerpt, &offset); err != nil {
				return readErr("read errors")
			}
			it.Target = transcript.Clip(target.String, 200)
			if seq.Valid {
				sv, ov := int(seq.Int64), int(offset.Int64)
				it.Seq, it.TurnOffset = &sv, &ov
			}
			out.Items = append(out.Items, it)
		}
		return rows.Err()
	})
	return out, err
}

// ---- ingest freshness, stats ----------------------------------------------

// Ingest reports how fresh the corpus is, from the writer's checkpoints.
func (s *Service) Ingest(ctx context.Context) (transcript.IngestStatus, error) {
	out := transcript.IngestStatus{State: "never", Sources: []transcript.SourceIngest{}}
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		var last sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT max(checkpoint_at) FROM ingest_checkpoints`).Scan(&last); err != nil {
			return readErr("read ingest status")
		}
		if last.Valid {
			out.State, out.LastIngestAt = "ok", last.Time.UTC().Format(time.RFC3339)
		}
		rows, err := tx.QueryContext(ctx, `SELECT so.harness, max(c.checkpoint_at) FROM ingest_checkpoints c JOIN sources so ON so.source_id = c.source_id GROUP BY so.harness ORDER BY so.harness`)
		if err != nil {
			return readErr("read ingest status")
		}
		defer rows.Close()
		for rows.Next() {
			var tool string
			var at time.Time
			if err := rows.Scan(&tool, &at); err != nil {
				return readErr("read ingest status")
			}
			out.Sources = append(out.Sources, transcript.SourceIngest{Tool: tool, LastIngestAt: at.UTC().Format(time.RFC3339)})
		}
		return rows.Err()
	})
	return out, err
}

// Stats aggregates the filtered sessions over the whole database.
func (s *Service) Stats(ctx context.Context, f transcript.SessionFilter) (transcript.Stats, error) {
	ctx, cancel := context.WithTimeout(ctx, transcript.StatsDeadline)
	defer cancel()
	where, args := filterSQL(f)
	cte := `WITH f AS (SELECT s.harness AS tool, COALESCE(s.model, '') AS model, COALESCE(s.cwd_redacted, '') AS cwd,
 COALESCE(to_char(s.started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), '') AS day, s.legacy_turn_count AS turns,
 ` + tokensExpr + ` AS tokens, ` + callsExpr + ` AS calls, ` + errsExpr + ` AS errs FROM sessions s` + enJoin
	if where != "" {
		cte += ` WHERE ` + where
	}
	cte += `) `
	agg := `count(*), COALESCE(SUM(turns), 0)::bigint, COALESCE(SUM(tokens), 0)::bigint, COALESCE(SUM(calls), 0)::bigint, COALESCE(SUM(errs), 0)::bigint`
	var out transcript.Stats
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		if err := tx.QueryRowContext(ctx, cte+`SELECT `+agg+` FROM f`, args...).
			Scan(&out.Totals.Sessions, &out.Totals.Turns, &out.Totals.Tokens, &out.Totals.ToolCalls, &out.Totals.Errors); err != nil {
			return readErr("compute stats")
		}
		group := func(col, extra, order string, limit int) ([]transcript.Bucket, error) {
			rows, err := tx.QueryContext(ctx, cte+`SELECT `+col+`, `+agg+` FROM f `+extra+` GROUP BY `+col+` ORDER BY `+order+` LIMIT ?`, append(append([]any{}, args...), limit)...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			res := []transcript.Bucket{}
			for rows.Next() {
				var b transcript.Bucket
				if err := rows.Scan(&b.Key, &b.Sessions, &b.Turns, &b.Tokens, &b.ToolCalls, &b.Errors); err != nil {
					return nil, err
				}
				res = append(res, b)
			}
			return res, rows.Err()
		}
		var err error
		if out.ByDay, err = group("day", `WHERE day <> ''`, "day DESC", transcript.MaxStatDays); err != nil {
			return readErr("compute stats")
		}
		for i, j := 0, len(out.ByDay)-1; i < j; i, j = i+1, j-1 {
			out.ByDay[i], out.ByDay[j] = out.ByDay[j], out.ByDay[i]
		}
		if out.ByTool, err = group("tool", "", "count(*) DESC, tool", 50); err != nil {
			return readErr("compute stats")
		}
		if out.ByModel, err = group("model", "", "count(*) DESC, model", 50); err != nil {
			return readErr("compute stats")
		}
		if out.ByProject, err = group("cwd", "", "count(*) DESC, cwd", transcript.MaxStatProjects); err != nil {
			return readErr("compute stats")
		}
		return nil
	})
	return out, err
}

// ---- tool calls, usage -----------------------------------------------------

// QueryToolCalls lists tool-call metadata by name, session, time and status.
func (s *Service) QueryToolCalls(ctx context.Context, f transcript.ToolCallFilter) ([]transcript.ToolCall, error) {
	q := `SELECT c.tool_name, s.native_session_id, s.kind, c.status, c.call_id, c.started_at, c.ended_at, c.reported_result_bytes
 FROM tool_calls c JOIN sessions s ON s.session_id = c.session_id WHERE true`
	var args []any
	if f.Name != "" {
		q += ` AND c.tool_name = ?`
		args = append(args, f.Name)
	}
	if f.NativeSessionID != "" {
		q += ` AND s.native_session_id = ?`
		args = append(args, f.NativeSessionID)
	}
	if !f.Since.IsZero() {
		q += ` AND c.started_at >= ?`
		args = append(args, f.Since.UTC())
	}
	if !f.Until.IsZero() {
		q += ` AND c.started_at < ?`
		args = append(args, f.Until.UTC())
	}
	if f.Status != "" {
		q += ` AND c.status = ?`
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q += ` ORDER BY c.started_at, c.call_id LIMIT ?`
	args = append(args, limit)
	var out []transcript.ToolCall
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return readErr("query tool calls")
		}
		defer rows.Close()
		for rows.Next() {
			var c transcript.ToolCall
			var started, ended sql.NullTime
			if err := rows.Scan(&c.Name, &c.NativeSessionID, &c.SessionKind, &c.Status, &c.ToolUseID, &started, &ended, &c.ResultBytes); err != nil {
				return readErr("read tool calls")
			}
			c.StartedAt, c.EndedAt = ts(started), ts(ended)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// SessionUsage sums stored token usage for a native session id.
func (s *Service) SessionUsage(ctx context.Context, nativeSessionID string) (transcript.UsageTotals, error) {
	var t transcript.UsageTotals
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(u.input_tokens), 0)::bigint, COALESCE(SUM(u.output_tokens), 0)::bigint,
 COALESCE(SUM(u.cache_read_tokens), 0)::bigint, COALESCE(SUM(u.cache_creation_tokens), 0)::bigint
 FROM usage u JOIN sessions s ON s.session_id = u.session_id WHERE s.native_session_id = ?`, nativeSessionID).
			Scan(&t.Requests, &t.Input, &t.Output, &t.CacheRead, &t.CacheCreation); err != nil {
			return readErr("read usage")
		}
		return nil
	})
	return t, err
}

// StoredSessionIDs returns the native ids of sessions of every kind, by tool, for
// the coverage check. Codex stores a spawned agent's rollout as a subagent session
// under its own id, so a main-only list would report those files as missing.
func (s *Service) StoredSessionIDs(ctx context.Context) (map[string]map[string]bool, error) {
	rows, err := s.st.QueryContext(ctx, `SELECT harness, native_session_id FROM sessions`)
	if err != nil {
		return nil, errors.New("transcript: cannot list sessions")
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var tool, id string
		if err := rows.Scan(&tool, &id); err != nil {
			return nil, errors.New("transcript: cannot read sessions")
		}
		if out[tool] == nil {
			out[tool] = map[string]bool{}
		}
		out[tool][id] = true
	}
	return out, rows.Err()
}

var _ transcript.CoverageSource = (*Service)(nil)
