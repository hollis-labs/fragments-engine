package pgstore

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
	"github.com/hollis-labs/libs/core/store"
)

// SearchSummaries searches the enrichment text of every enriched session. The
// newest ok Tier 1 results are read in one short query and matched in Go, which
// covers decisions and follow-ups that no index holds.
func (s *Service) SearchSummaries(ctx context.Context, text string, f transcript.SessionFilter) (transcript.SessionPage, error) {
	terms := transcript.SearchTerms(text)
	if len(terms) == 0 {
		return transcript.SessionPage{Items: []transcript.SessionSummary{}, Mode: transcript.ModeSummary}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, transcript.SummaryDeadline)
	defer cancel()
	page := transcript.SessionPage{Mode: transcript.ModeSummary, Items: []transcript.SessionSummary{}}
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		ranked, err := rankSummaries(ctx, tx, terms, f)
		if err != nil {
			return err
		}
		limit := transcript.ClampLimit(f.Limit)
		offset := transcript.DecodeCursor(f.Cursor)
		if offset < len(ranked) {
			end := offset + limit
			if end >= len(ranked) {
				end = len(ranked)
			} else {
				page.NextCursor = transcript.EncodeCursor(end)
			}
			page.Items = ranked[offset:end]
		}
		fillTitles(ctx, tx, page.Items)
		return nil
	})
	if err != nil {
		if p, ok := deadlineHint(ctx, transcript.ModeSummary); ok {
			return p, nil
		}
		return transcript.SessionPage{}, err
	}
	return page, nil
}

func rankSummaries(ctx context.Context, tx store.Querier, terms []string, f transcript.SessionFilter) ([]transcript.SessionSummary, error) {
	where, args := filterSQL(f)
	q := `SELECT ` + summaryCols + ` FROM sessions s` + enJoin + ` WHERE en.enrichment_id IS NOT NULL` + and(where)
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, readErr("search summaries")
	}
	defer rows.Close()
	type scored struct {
		item  transcript.SessionSummary
		score int
	}
	var hits []scored
	for rows.Next() {
		item, raw, err := scanSummary(rows)
		if err != nil {
			return nil, readErr("read summaries")
		}
		score, m, ok := transcript.MatchSummaryJSON(raw, terms)
		if !ok {
			continue
		}
		item.Match = &m
		item.Score = float64(score)
		hits = append(hits, scored{item, score})
	}
	if err := rows.Err(); err != nil {
		return nil, readErr("read summaries")
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
	out := make([]transcript.SessionSummary, len(hits))
	for i, h := range hits {
		out[i] = h.item
	}
	return out, nil
}

// EmbeddingGroups lists what summary vectors exist, so a caller can tell "none
// yet" from "made by a different model".
func (s *Service) EmbeddingGroups(ctx context.Context) ([]transcript.EmbeddingGroup, error) {
	var out []transcript.EmbeddingGroup
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		rows, err := tx.QueryContext(ctx, `SELECT g.model, g.format, g.dimension, count(*) FROM embeddings e JOIN embedding_groups g ON g.group_id = e.group_id
 WHERE e.kind = 'summary' GROUP BY g.model, g.format, g.dimension ORDER BY g.model, g.format, g.dimension`)
		if err != nil {
			return readErr("read embeddings")
		}
		defer rows.Close()
		for rows.Next() {
			var g transcript.EmbeddingGroup
			if err := rows.Scan(&g.Model, &g.Format, &g.Dim, &g.Count); err != nil {
				return readErr("read embeddings")
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// vectorLiteral renders vec in pgvector's text form.
func vectorLiteral(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// SemanticSessions returns the k sessions whose summary vectors are nearest to
// vec by cosine similarity (exact scan; no approximate index yet). Only vectors
// of the same model, prompt format and dimension are compared.
func (s *Service) SemanticSessions(ctx context.Context, model, format string, vec []float32, f transcript.SessionFilter) (transcript.SessionPage, error) {
	ctx, cancel := context.WithTimeout(ctx, transcript.SemanticDeadline)
	defer cancel()
	k := transcript.ClampLimit(f.Limit)
	if k > transcript.MaxRankedLimit {
		k = transcript.MaxRankedLimit
	}
	if len(vec) == 0 {
		return transcript.SessionPage{Items: []transcript.SessionSummary{}, Mode: transcript.ModeSemantic}, nil
	}
	lit := vectorLiteral(vec)
	where, fargs := filterSQL(f)
	q := `SELECT ` + summaryCols + `, 1 - (e.embedding <=> ?::vector) FROM embeddings e
 JOIN embedding_groups g ON g.group_id = e.group_id AND g.model = ? AND g.format = ? AND g.dimension = ?
 JOIN sessions s ON s.session_id = e.session_id` + enJoin + `
 WHERE e.kind = 'summary' AND e.dimension = ?` + and(where) + `
 ORDER BY e.embedding <=> ?::vector, s.session_id LIMIT ?`
	args := []any{lit, model, format, len(vec), len(vec)}
	args = append(args, fargs...)
	args = append(args, lit, k*3)
	page := transcript.SessionPage{Mode: transcript.ModeSemantic, Items: []transcript.SessionSummary{}}
	err := s.st.ReadTx(ctx, func(ctx context.Context, tx store.Querier) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return readErr("search semantically")
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() && len(page.Items) < k {
			var score float64
			item, raw, err := scanSummary(rows, &score)
			if err != nil {
				return readErr("search semantically")
			}
			if seen[item.ID] { // a session has one summary vector per chunk; keep its nearest
				continue
			}
			seen[item.ID] = true
			item.Score = score
			item.Match = &transcript.Match{Field: "semantic", Excerpt: transcript.SummaryLeadOf(raw)}
			page.Items = append(page.Items, item)
		}
		if err := rows.Err(); err != nil {
			return readErr("search semantically")
		}
		rows.Close()
		fillTitles(ctx, tx, page.Items)
		return nil
	})
	if err != nil {
		if p, ok := deadlineHint(ctx, transcript.ModeSemantic); ok {
			return p, nil
		}
		return transcript.SessionPage{}, err
	}
	return page, nil
}
