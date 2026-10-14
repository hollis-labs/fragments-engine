package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Search modes offered by the Chats page. Every mode is read-only, bounded by a
// deadline, and never holds a cursor open: the store uses journal mode DELETE,
// so a long read blocks the enrichment writer.
const (
	ModeText     = "text"     // redacted transcript text (FTS5)
	ModeSummary  = "summary"  // enrichment text: title, summary, decisions, follow-ups, topics, entities
	ModeSemantic = "semantic" // cosine similarity over summary vectors
	ModeHybrid   = "hybrid"   // semantic + summary + transcript text, reciprocal rank fusion
)

// Stable notice codes reported with a search page.
const (
	NoticeSemanticUnavailable = "semantic_unavailable" // embedding server unreachable or not configured
	NoticeNoVectors           = "no_vectors"           // no summary has been embedded yet
	NoticeModelMismatch       = "model_mismatch"       // vectors exist, but not for the configured model or format
	NoticeDimMismatch         = "dim_mismatch"         // the server answers with a different dimension than stored
	NoticeTranscriptTooBroad  = "transcript_too_broad" // hybrid dropped the transcript-text list for time
)

const (
	summarySearchDeadline = 3 * time.Second
	semanticScanDeadline  = 3 * time.Second
	hybridTextDeadline    = 2 * time.Second
	maxRankedResults      = 100 // semantic and hybrid return a top-k, not a pageable list
	rrfK                  = 60
	maxExcerptBytes       = 220
	maxSearchTerms        = 8
)

// ValidMode reports whether m is a search mode.
func ValidMode(m string) bool {
	switch m {
	case ModeText, ModeSummary, ModeSemantic, ModeHybrid:
		return true
	}
	return false
}

// Match says why a session was returned by a summary, semantic or hybrid
// search. Field is the primary field: title, topics, entities, summary,
// decisions, follow_ups, transcript or semantic. Sources lists every list that
// contributed in hybrid mode.
type Match struct {
	Field   string   `json:"field"`
	Sources []string `json:"sources,omitempty"`
	// Excerpt is short; matched words are wrapped in [ ].
	Excerpt string `json:"excerpt,omitempty"`
}

// summaryEntry is one searchable piece of a stored Tier 1 result.
type summaryEntry struct {
	field  string
	text   string
	weight int
}

var summaryWeights = map[string]int{"title": 5, "topics": 4, "entities": 3, "summary": 3, "decisions": 2, "follow_ups": 2}

func summaryEntries(resultJSON string) []summaryEntry {
	var r struct {
		Title    string   `json:"title"`
		Summary  string   `json:"summary"`
		Topics   []string `json:"topics"`
		Entities struct {
			Projects, Repos, Tools, Technologies []string
		} `json:"entities"`
		Decisions []Claim `json:"decisions"`
		FollowUps []Claim `json:"follow_ups"`
	}
	if json.Unmarshal([]byte(resultJSON), &r) != nil {
		return nil
	}
	var out []summaryEntry
	add := func(field, text string) {
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, summaryEntry{field, text, summaryWeights[field]})
		}
	}
	add("title", r.Title)
	add("topics", strings.Join(r.Topics, " · "))
	ents := append(append(append(append([]string{}, r.Entities.Projects...), r.Entities.Repos...), r.Entities.Tools...), r.Entities.Technologies...)
	add("entities", strings.Join(ents, " · "))
	add("summary", r.Summary)
	for _, d := range r.Decisions {
		add("decisions", d.Text)
	}
	for _, d := range r.FollowUps {
		add("follow_ups", d.Text)
	}
	return out
}

// searchTerms lowercases and de-duplicates the words of a query.
func searchTerms(text string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(strings.ReplaceAll(w, `"`, ""), " ")
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
		if len(terms) == maxSearchTerms {
			break
		}
	}
	return terms
}

// matchSummary scores a stored result against the terms. Every term must occur
// somewhere in the result; the score favours titles, topics and entities over
// summary prose and claims. It returns the best field with a highlighted
// excerpt.
func matchSummary(entries []summaryEntry, terms []string) (score int, m Match, ok bool) {
	covered := make([]bool, len(terms))
	bestScore := 0
	var best summaryEntry
	for _, e := range entries {
		lower := strings.ToLower(e.text)
		hit := 0
		for i, t := range terms {
			if strings.Contains(lower, t) {
				covered[i] = true
				hit++
			}
		}
		if hit == 0 {
			continue
		}
		s := hit * e.weight
		score += s
		if s > bestScore {
			bestScore, best = s, e
		}
	}
	for _, c := range covered {
		if !c {
			return 0, Match{}, false
		}
	}
	return score, Match{Field: best.field, Excerpt: excerpt(best.text, terms)}, true
}

// excerpt returns a window of text around the first matched term, with every
// matched word wrapped in [ ]. Brackets in the source become parentheses so the
// markers stay unambiguous.
func excerpt(text string, terms []string) string {
	text = strings.NewReplacer("[", "(", "]", ")", "\r", " ", "\n", " ").Replace(text)
	lower := strings.ToLower(text)
	if len(lower) != len(text) {
		// Case folding changed byte offsets (rare scripts); show the start, unmarked.
		return clip(text, maxExcerptBytes)
	}
	first := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (first < 0 || i < first) {
			first = i
		}
	}
	start, end := 0, len(text)
	if first > 60 {
		start = first - 60
	}
	if end-start > maxExcerptBytes {
		end = start + maxExcerptBytes
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	window, wlower := text[start:end], lower[start:end]
	marked := make([]bool, len(window))
	for _, t := range terms {
		for from := 0; ; {
			i := strings.Index(wlower[from:], t)
			if i < 0 {
				break
			}
			for j := from + i; j < from+i+len(t); j++ {
				marked[j] = true
			}
			from += i + len(t)
		}
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	in := false
	for i := 0; i < len(window); i++ {
		if marked[i] && !in {
			b.WriteByte('[')
			in = true
		} else if !marked[i] && in {
			b.WriteByte(']')
			in = false
		}
		b.WriteByte(window[i])
	}
	if in {
		b.WriteByte(']')
	}
	if end < len(text) {
		b.WriteString("…")
	}
	return b.String()
}

// SearchSummaries searches the enrichment text of every enriched session. There
// is no schema dependency: the newest ok Tier 1 results are read in one short
// query and matched in Go, which covers decisions and follow-ups that the
// full-text index does not hold. The corpus is a few thousand small documents.
func (s *Store) SearchSummaries(ctx context.Context, text string, f SessionFilter) (SessionPage, error) {
	if s == nil {
		return SessionPage{}, ErrPrivateStoreRequired
	}
	terms := searchTerms(text)
	if len(terms) == 0 {
		return SessionPage{Items: []SessionSummary{}, Mode: ModeSummary}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, summarySearchDeadline)
	defer cancel()
	ranked, err := s.rankSummaries(ctx, terms, f)
	if err != nil {
		return searchFailureMode(ctx, ModeSummary)
	}
	limit := clampLimit(f.Limit)
	offset := decodeCursor(f.Cursor)
	page := SessionPage{Mode: ModeSummary, Items: []SessionSummary{}}
	if offset < len(ranked) {
		end := offset + limit
		if end >= len(ranked) {
			end = len(ranked)
		} else {
			page.NextCursor = encodeCursor(end)
		}
		page.Items = ranked[offset:end]
	}
	s.fillTitles(ctx, page.Items)
	return page, nil
}

// rankSummaries returns every matching session, best first.
func (s *Store) rankSummaries(ctx context.Context, terms []string, f SessionFilter) ([]SessionSummary, error) {
	where, args := f.where()
	q := listSelect + ` WHERE en.id IS NOT NULL AND en.result_json <> ''`
	if where != "" {
		q += ` AND ` + strings.TrimPrefix(where, " WHERE ")
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errors.New("transcript: cannot search summaries")
	}
	defer rows.Close()
	type scored struct {
		item  SessionSummary
		score int
	}
	var hits []scored
	for rows.Next() {
		item, raw, err := scanSummaryRaw(rows)
		if err != nil {
			return nil, errors.New("transcript: cannot read summaries")
		}
		score, m, ok := matchSummary(summaryEntries(raw), terms)
		if !ok {
			continue
		}
		item.Match = &m
		item.Score = float64(score)
		hits = append(hits, scored{item, score})
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("transcript: cannot read summaries")
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].item.StartedAt != hits[j].item.StartedAt {
			return hits[i].item.StartedAt > hits[j].item.StartedAt
		}
		return hits[i].item.ID < hits[j].item.ID
	})
	out := make([]SessionSummary, len(hits))
	for i, h := range hits {
		out[i] = h.item
	}
	return out, nil
}

// EmbeddingGroup counts the stored summary vectors made with one model, prompt
// format and dimension.
type EmbeddingGroup struct {
	Model, Format string
	Dim, Count    int
}

// EmbeddingGroups lists what vectors exist, so a caller can tell "none yet"
// from "made by a different model" before scanning any.
func (s *Store) EmbeddingGroups(ctx context.Context) ([]EmbeddingGroup, error) {
	if s == nil {
		return nil, ErrPrivateStoreRequired
	}
	rows, err := s.db.QueryContext(ctx, `SELECT model, format, dim, COUNT(*) FROM tx_embeddings WHERE kind = 'summary' GROUP BY model, format, dim`)
	if err != nil {
		return nil, errors.New("transcript: cannot read embeddings")
	}
	defer rows.Close()
	var out []EmbeddingGroup
	for rows.Next() {
		var g EmbeddingGroup
		if err := rows.Scan(&g.Model, &g.Format, &g.Dim, &g.Count); err != nil {
			return nil, errors.New("transcript: cannot read embeddings")
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SemanticSessions returns the k sessions whose summary vectors are nearest to
// vec by cosine similarity. vec must already be embedded: no network call
// happens while the database is being read. Only vectors made with the same
// model, prompt format and dimension are compared, and the scan is bounded.
func (s *Store) SemanticSessions(ctx context.Context, model, format string, vec []float32, f SessionFilter) (SessionPage, error) {
	if s == nil {
		return SessionPage{}, ErrPrivateStoreRequired
	}
	ctx, cancel := context.WithTimeout(ctx, semanticScanDeadline)
	defer cancel()
	k := clampLimit(f.Limit)
	if k > maxRankedResults {
		k = maxRankedResults
	}
	where, args := f.where()
	q := `SELECT ` + summaryCols + `, v.vector FROM tx_embeddings v` + fmt.Sprintf(summaryJoins, "v.session_pk") +
		` WHERE v.kind = 'summary' AND v.model = ? AND v.format = ? AND v.dim = ?`
	qargs := []any{model, format, len(vec)}
	if where != "" {
		q += ` AND ` + strings.TrimPrefix(where, " WHERE ")
		qargs = append(qargs, args...)
	}
	rows, err := s.db.QueryContext(ctx, q, qargs...)
	if err != nil {
		return searchFailureMode(ctx, ModeSemantic)
	}
	defer rows.Close()
	var items []SessionSummary
	var raws []string
	for rows.Next() {
		var blob []byte
		item, raw, err := scanSummaryRaw(rows, &blob)
		if err != nil {
			return searchFailureMode(ctx, ModeSemantic)
		}
		item.Score = cosine(vec, decodeVec(blob))
		items = append(items, item)
		raws = append(raws, raw)
	}
	if err := rows.Err(); err != nil {
		return searchFailureMode(ctx, ModeSemantic)
	}
	rows.Close()
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		if items[order[a]].Score != items[order[b]].Score {
			return items[order[a]].Score > items[order[b]].Score
		}
		return items[order[a]].ID < items[order[b]].ID
	})
	if len(order) > k {
		order = order[:k]
	}
	page := SessionPage{Mode: ModeSemantic, Items: make([]SessionSummary, 0, len(order))}
	for _, i := range order {
		it := items[i]
		it.Match = &Match{Field: "semantic", Excerpt: summaryLead(raws[i])}
		page.Items = append(page.Items, it)
	}
	s.fillTitles(ctx, page.Items)
	return page, nil
}

// summaryLead is the start of a stored summary, shown beside a semantic hit.
func summaryLead(resultJSON string) string {
	for _, e := range summaryEntries(resultJSON) {
		if e.field == "summary" {
			return clip(strings.Join(strings.Fields(e.text), " "), maxExcerptBytes)
		}
	}
	return ""
}

func searchFailureMode(ctx context.Context, mode string) (SessionPage, error) {
	page, err := searchFailure(ctx)
	page.Mode = mode
	return page, err
}

// FuseRankings merges ranked lists by reciprocal rank fusion. Each list is best
// first and is tagged with its source name. A session keeps the most specific
// match found: a summary field with an excerpt, then transcript text, then
// semantic.
func FuseRankings(limit int, sources []string, lists ...[]SessionSummary) []SessionSummary {
	type fused struct {
		item    SessionSummary
		score   float64
		sources []string
	}
	byID := map[string]*fused{}
	var order []string
	for li, list := range lists {
		for rank, it := range list {
			f, ok := byID[it.ID]
			if !ok {
				f = &fused{item: it}
				byID[it.ID] = f
				order = append(order, it.ID)
			} else if matchRank(it) > matchRank(f.item) {
				keep := f.item
				f.item = it
				if f.item.Snippet == "" {
					f.item.Snippet = keep.Snippet
				}
			}
			f.score += 1 / float64(rrfK+rank+1)
			f.sources = append(f.sources, sources[li])
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byID[order[i]], byID[order[j]]
		if a.score != b.score {
			return a.score > b.score
		}
		return order[i] < order[j]
	})
	if limit > 0 && len(order) > limit {
		order = order[:limit]
	}
	out := make([]SessionSummary, 0, len(order))
	for _, id := range order {
		f := byID[id]
		it := f.item
		m := Match{Field: "semantic"}
		if it.Match != nil {
			m = *it.Match
		}
		if it.Match == nil && it.Snippet != "" {
			m = Match{Field: "transcript", Excerpt: it.Snippet}
		}
		m.Sources = f.sources
		it.Match = &m
		it.Score = f.score
		out = append(out, it)
	}
	return out
}

// matchRank prefers the match that explains the result best.
func matchRank(it SessionSummary) int {
	switch {
	case it.Match != nil && it.Match.Field != "semantic" && it.Match.Field != "transcript" && it.Match.Excerpt != "":
		return 3
	case it.Snippet != "" || (it.Match != nil && it.Match.Field == "transcript"):
		return 2
	case it.Match != nil:
		return 1
	}
	return 0
}
