package transcript

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// Embeddings live in the private store only, never in the shared recall index.
// Search is brute-force cosine in Go: the corpus is tens of thousands of
// vectors at most, so no vector extension is needed.

func encodeVec(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(f))
	}
	return out
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// EmbeddingCurrent reports whether a vector for exactly this text already exists.
func (s *Store) EmbeddingCurrent(ctx context.Context, pk, kind string, idx int, model, textDigest string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_embeddings WHERE session_pk = ? AND kind = ? AND chunk_idx = ? AND model = ? AND text_digest = ?`,
		pk, kind, idx, model, textDigest).Scan(&n)
	return n > 0, err
}

func (s *Store) SaveEmbedding(ctx context.Context, pk, kind string, idx int, model, textDigest string, vec []float32) error {
	if s == nil {
		return ErrPrivateStoreRequired
	}
	if len(vec) == 0 {
		return errors.New("transcript: empty embedding")
	}
	if err := s.checkOwnership(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tx_embeddings VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(session_pk, kind, chunk_idx, model) DO UPDATE SET
 dim = excluded.dim, text_digest = excluded.text_digest, vector = excluded.vector, created_at = excluded.created_at`,
		pk, kind, idx, Redact(model), len(vec), textDigest, encodeVec(vec), stamp(time.Now()))
	if err != nil {
		return errors.New("transcript: cannot save embedding")
	}
	return nil
}

// SummaryText is the text embedded and searched for a session's Tier 1 result.
type SummaryText struct {
	SessionPK, NativeSessionID, Text, Digest string
}

// SummaryTexts lists the newest ok Tier 1 result per session as embeddable text.
func (s *Store) SummaryTexts(ctx context.Context) ([]SummaryText, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.session_pk, COALESCE(x.native_session_id, ''), e.result_json FROM tx_enrichment e
 LEFT JOIN tx_sessions x ON x.session_pk = e.session_pk
 WHERE e.tier = 1 AND e.status = 'ok' AND e.id = (SELECT id FROM tx_enrichment e2 WHERE e2.session_pk = e.session_pk AND e2.tier = 1 AND e2.status = 'ok' ORDER BY created_at DESC, id DESC LIMIT 1)
 ORDER BY e.session_pk`)
	if err != nil {
		return nil, errors.New("transcript: cannot list summaries")
	}
	defer rows.Close()
	var out []SummaryText
	for rows.Next() {
		var t SummaryText
		var raw string
		if err := rows.Scan(&t.SessionPK, &t.NativeSessionID, &raw); err != nil {
			return nil, errors.New("transcript: cannot read summaries")
		}
		t.Text = summaryTextOf(raw)
		t.Digest = digest([]byte(t.Text))
		out = append(out, t)
	}
	return out, rows.Err()
}

// SearchHit is one result of summary search. Rank is bm25 (lower is better) for
// text search and cosine similarity (higher is better) for semantic search.
type SearchHit struct {
	SessionPK, NativeSessionID, Title string
	Rank                              float64
}

// SearchEnriched runs a full-text query over titles, summaries, topics and
// entities. Every term is quoted, so FTS syntax in the query cannot error or
// widen the match.
func (s *Store) SearchEnriched(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	var terms []string
	for _, f := range strings.Fields(query) {
		f = strings.Trim(strings.ReplaceAll(f, `"`, ""), " ")
		if f != "" {
			terms = append(terms, `"`+f+`"`)
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.session_pk, COALESCE(x.native_session_id, ''), f.title, bm25(tx_enrichment_search)
 FROM tx_enrichment_search f LEFT JOIN tx_sessions x ON x.session_pk = f.session_pk
 WHERE tx_enrichment_search MATCH ? ORDER BY bm25(tx_enrichment_search) LIMIT ?`, strings.Join(terms, " "), limit)
	if err != nil {
		return nil, errors.New("transcript: cannot search")
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.SessionPK, &h.NativeSessionID, &h.Title, &h.Rank); err != nil {
			return nil, errors.New("transcript: cannot read search results")
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// NearestSummaries returns the k summaries whose vectors are closest to vec by
// cosine similarity, among vectors made with the same model.
func (s *Store) NearestSummaries(ctx context.Context, model string, vec []float32, k int) ([]SearchHit, error) {
	if k <= 0 || k > 200 {
		k = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.session_pk, COALESCE(x.native_session_id, ''), v.vector,
  COALESCE((SELECT json_extract(e.result_json, '$.title') FROM tx_enrichment e WHERE e.session_pk = v.session_pk AND e.tier = 1 AND e.status = 'ok' ORDER BY e.created_at DESC, e.id DESC LIMIT 1), '')
 FROM tx_embeddings v LEFT JOIN tx_sessions x ON x.session_pk = v.session_pk WHERE v.kind = 'summary' AND v.model = ? AND v.dim = ?`, model, len(vec))
	if err != nil {
		return nil, errors.New("transcript: cannot read embeddings")
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var blob []byte
		if err := rows.Scan(&h.SessionPK, &h.NativeSessionID, &blob, &h.Title); err != nil {
			return nil, errors.New("transcript: cannot read embeddings")
		}
		h.Rank = cosine(vec, decodeVec(blob))
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rank > out[j].Rank })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// summaryTextOf renders the searchable form of a stored Tier 1 result.
func summaryTextOf(resultJSON string) string {
	var r struct {
		Title, Summary string
		Topics         []string
	}
	var raw map[string]any
	if json.Unmarshal([]byte(resultJSON), &raw) != nil {
		return ""
	}
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &r)
	return strings.TrimSpace(r.Title + "\n" + r.Summary + "\n" + strings.Join(r.Topics, " "))
}
