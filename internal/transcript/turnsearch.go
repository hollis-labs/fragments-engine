package transcript

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Highlight markers: control characters that cannot occur in redacted transcript text from a user's point of view
// and are stripped from the output, so a literal bracket in the transcript never reads as a match.
const (
	hlOpen  = "\x02"
	hlClose = "\x03"
)

const (
	findDeadline  = 3 * time.Second
	maxFindLimit  = 100
	maxErrorItems = 200
	snippetTokens = 24
)

// Range is a highlighted span: rune offsets into Highlight.Text, end exclusive.
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Highlight is a snippet without markers plus where the matches sit in it.
type Highlight struct {
	Text   string  `json:"text"`
	Ranges []Range `json:"ranges"`
}

// splitHighlight turns an FTS snippet that uses the control-character markers into a bracketed snippet (the
// existing display form) and a structured highlight.
func splitHighlight(raw string) (string, Highlight) {
	raw = clip(raw, maxSnippetBytes*2)
	var plain, bracketed strings.Builder
	h := Highlight{Ranges: []Range{}}
	pos, start := 0, -1
	for _, r := range raw {
		switch string(r) {
		case hlOpen:
			start = pos
			bracketed.WriteByte('[')
		case hlClose:
			if start >= 0 {
				h.Ranges = append(h.Ranges, Range{start, pos})
				start = -1
			}
			bracketed.WriteByte(']')
		default:
			plain.WriteRune(r)
			bracketed.WriteRune(r)
			pos++
		}
	}
	h.Text = plain.String()
	return bracketed.String(), h
}

// TurnMatch is one turn that contains the query.
type TurnMatch struct {
	TurnOffset int       `json:"turn_offset"`
	Seq        int       `json:"seq"`
	Role       string    `json:"role"`
	Snippet    string    `json:"snippet"`
	Highlight  Highlight `json:"highlight"`
}

// FindResult is the answer to a find-in-session request.
type FindResult struct {
	Total     int         `json:"total"`
	Matches   []TurnMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
}

func (s *Store) sessionExists(ctx context.Context, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tx_sessions WHERE session_pk = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionNotFound
	}
	if err != nil {
		return errors.New("transcript: cannot read session")
	}
	return nil
}

// FindInSession lists the turns of one session that contain the text, in turn order, starting after afterOffset
// (a turn offset; negative means from the start). Total counts every matching turn; Truncated says more follow.
func (s *Store) FindInSession(ctx context.Context, id, text string, afterOffset, limit int) (FindResult, error) {
	if s == nil {
		return FindResult{}, ErrPrivateStoreRequired
	}
	if err := s.sessionExists(ctx, id); err != nil {
		return FindResult{}, err
	}
	out := FindResult{Matches: []TurnMatch{}}
	match := ftsQuery(text)
	if match == "" {
		return out, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxFindLimit {
		limit = maxFindLimit
	}
	ctx, cancel := context.WithTimeout(ctx, findDeadline)
	defer cancel()
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_turn_search WHERE tx_turn_search MATCH ? AND session_pk = ?`, match, id).Scan(&out.Total); err != nil {
		return FindResult{}, findFailure(ctx)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.seq, t.role, snippet(tx_turn_search, 0, ?, ?, '…', ?),
 (SELECT COUNT(*) FROM tx_turns p WHERE p.session_pk = m.session_pk AND p.seq < m.seq)
 FROM tx_turn_search m JOIN tx_turns t ON t.session_pk = m.session_pk AND t.seq = m.seq
 WHERE tx_turn_search MATCH ? AND m.session_pk = ? ORDER BY m.seq`, hlOpen, hlClose, snippetTokens, match, id)
	if err != nil {
		return FindResult{}, findFailure(ctx)
	}
	defer rows.Close()
	for rows.Next() {
		var m TurnMatch
		var raw string
		if err := rows.Scan(&m.Seq, &m.Role, &raw, &m.TurnOffset); err != nil {
			return FindResult{}, findFailure(ctx)
		}
		if m.TurnOffset <= afterOffset {
			continue
		}
		if len(out.Matches) == limit {
			out.Truncated = true
			break
		}
		m.Snippet, m.Highlight = splitHighlight(raw)
		out.Matches = append(out.Matches, m)
	}
	if err := rows.Err(); err != nil {
		return FindResult{}, findFailure(ctx)
	}
	return out, nil
}

func findFailure(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrFindTooBroad
	}
	return errors.New("transcript: cannot search session")
}

// ErrFindTooBroad is returned when a find could not finish in time.
var ErrFindTooBroad = errors.New("transcript: search too broad")

// ErrorItem is one failed tool call and the turn it belongs to.
type ErrorItem struct {
	TurnOffset   *int   `json:"turn_offset,omitempty"`
	Seq          *int   `json:"seq,omitempty"`
	Tool         string `json:"tool"`
	Target       string `json:"target,omitempty"`
	ErrorExcerpt string `json:"error_excerpt,omitempty"`
}

// ErrorList is a session's failed tool calls.
type ErrorList struct {
	Items []ErrorItem `json:"items"`
	Total int         `json:"total"`
}

// SessionErrors lists the failed tool calls of one session in time order, at most maxErrorItems.
func (s *Store) SessionErrors(ctx context.Context, id string) (ErrorList, error) {
	if s == nil {
		return ErrorList{}, ErrPrivateStoreRequired
	}
	if err := s.sessionExists(ctx, id); err != nil {
		return ErrorList{}, err
	}
	out := ErrorList{Items: []ErrorItem{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_tool_calls WHERE session_pk = ? AND result_status = 'error'`, id).Scan(&out.Total); err != nil {
		return ErrorList{}, errors.New("transcript: cannot read errors")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.seq, c.name, c.target_redacted, c.result_excerpt_redacted,
 (SELECT COUNT(*) FROM tx_turns p WHERE p.session_pk = c.session_pk AND p.seq < t.seq)
 FROM tx_tool_calls c LEFT JOIN tx_turns t ON t.session_pk = c.session_pk AND t.event_uuid = c.turn_uuid
 WHERE c.session_pk = ? AND c.result_status = 'error' ORDER BY c.started_at, c.tool_use_id LIMIT ?`, id, maxErrorItems)
	if err != nil {
		return ErrorList{}, errors.New("transcript: cannot read errors")
	}
	defer rows.Close()
	for rows.Next() {
		var it ErrorItem
		var seq, offset sql.NullInt64
		if err := rows.Scan(&seq, &it.Tool, &it.Target, &it.ErrorExcerpt, &offset); err != nil {
			return ErrorList{}, errors.New("transcript: cannot read errors")
		}
		if seq.Valid {
			sv, ov := int(seq.Int64), int(offset.Int64)
			it.Seq, it.TurnOffset = &sv, &ov
		}
		out.Items = append(out.Items, it)
	}
	return out, rows.Err()
}

// turnHit is the best-matching turn of one session for a query.
type turnHit struct {
	seq, offset int
	highlight   Highlight
}

// bestTurns finds, for each given session, the turn that matches the query best. One index scan answers the whole
// page; sessions without a per-turn hit are left out.
func (s *Store) bestTurns(ctx context.Context, match string, ids []string) map[string]turnHit {
	out := map[string]turnHit{}
	if len(ids) == 0 {
		return out
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{hlOpen, hlClose, snippetTokens, match}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.session_pk, m.seq, snippet(tx_turn_search, 0, ?, ?, '…', ?),
 (SELECT COUNT(*) FROM tx_turns p WHERE p.session_pk = m.session_pk AND p.seq < m.seq)
 FROM tx_turn_search m WHERE tx_turn_search MATCH ? AND m.session_pk IN (`+marks+`) ORDER BY rank`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var pk, raw string
		var h turnHit
		if rows.Scan(&pk, &h.seq, &raw, &h.offset) != nil {
			break
		}
		if _, seen := out[pk]; seen {
			continue
		}
		_, h.highlight = splitHighlight(raw)
		out[pk] = h
	}
	return out
}
