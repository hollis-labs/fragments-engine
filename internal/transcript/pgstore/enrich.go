package pgstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
	"github.com/hollis-labs/libs/core/store"
)

var _ transcript.EnrichBackend = (*Service)(nil)

func stampTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339Nano)
}

// MainSessions lists main sessions with at least one text turn whose last
// activity is older than minIdle.
func (s *Service) MainSessions(ctx context.Context, minIdle time.Duration, now time.Time, nativeID string) ([]transcript.SessionRef, error) {
	q := `SELECT s.session_id, s.native_session_id, s.ended_at, s.harness FROM sessions s
 WHERE s.kind = 'main' AND EXISTS (SELECT 1 FROM events t WHERE t.session_id = s.session_id AND t.kind = 'turn' AND t.text_redacted <> '')`
	var args []any
	if nativeID != "" {
		q += ` AND s.native_session_id = ?`
		args = append(args, nativeID)
	}
	if minIdle > 0 {
		q += ` AND (s.ended_at IS NULL OR s.ended_at <= ?)`
		args = append(args, now.Add(-minIdle).UTC())
	}
	q += ` ORDER BY s.started_at NULLS FIRST, s.session_id`
	rows, err := s.st.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errors.New("transcript: cannot list sessions")
	}
	defer rows.Close()
	var out []transcript.SessionRef
	for rows.Next() {
		var r transcript.SessionRef
		var ended sql.NullTime
		if err := rows.Scan(&r.SessionPK, &r.NativeSessionID, &ended, &r.Tool); err != nil {
			return nil, errors.New("transcript: cannot read sessions")
		}
		r.EndedAt = stampTime(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LegacySessions is always empty: every session in the Postgres corpus is structured.
func (s *Service) LegacySessions(ctx context.Context, nativeID string) ([]transcript.SessionRef, error) {
	return nil, nil
}

func (s *Service) scanCount(ctx context.Context, q string, args ...any) ([]transcript.NameCount, error) {
	rows, err := s.st.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []transcript.NameCount
	for rows.Next() {
		var n transcript.NameCount
		if err := rows.Scan(&n.Name, &n.Count); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SessionFacts computes Tier 0 for a session, subagent totals included.
func (s *Service) SessionFacts(ctx context.Context, pk string) (transcript.SessionFacts, error) {
	var f transcript.SessionFacts
	var model, cwd, branch sql.NullString
	var started, ended sql.NullTime
	err := s.st.QueryRowContext(ctx, `SELECT harness, native_session_id, kind, model, cwd_redacted, git_branch_redacted, started_at, ended_at
 FROM sessions WHERE session_id = ?`, pk).Scan(&f.Tool, &f.NativeSessionID, &f.Kind, &model, &cwd, &branch, &started, &ended)
	if err != nil {
		return f, errors.New("transcript: unknown session")
	}
	f.Model, f.CWD, f.GitBranch = model.String, cwd.String, branch.String
	f.StartedAt, f.EndedAt = stampTime(started), stampTime(ended)
	if started.Valid && ended.Valid && ended.Time.After(started.Time) {
		f.DurationSec = int64(ended.Time.Sub(started.Time).Seconds())
	}
	roles, err := s.scanCount(ctx, `SELECT coalesce(role, ''), COUNT(*) FROM events WHERE session_id = ? AND kind = 'turn' GROUP BY role`, pk)
	if err != nil {
		return f, errors.New("transcript: cannot count turns")
	}
	for _, r := range roles {
		switch r.Name {
		case "user":
			f.UserTurns = r.Count
		case "assistant":
			f.AssistantTurns = r.Count
		}
	}
	if f.TopTools, err = s.scanCount(ctx, `SELECT tool_name, COUNT(*) AS n FROM tool_calls WHERE session_id = ? GROUP BY tool_name ORDER BY n DESC, tool_name LIMIT 10`, pk); err != nil {
		return f, errors.New("transcript: cannot count tools")
	}
	if err := s.st.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM((status = 'error')::int), 0) FROM tool_calls WHERE session_id = ?`, pk).Scan(&f.ToolCalls, &f.ToolErrors); err != nil {
		return f, errors.New("transcript: cannot count tools")
	}
	if f.SubagentTypes, err = s.scanCount(ctx, `SELECT CASE WHEN agent_type = '' THEN 'unknown' ELSE agent_type END, COUNT(*) AS n FROM sessions WHERE parent_session_id = ? GROUP BY 1 ORDER BY n DESC, 1`, pk); err != nil {
		return f, errors.New("transcript: cannot count subagents")
	}
	for _, t := range f.SubagentTypes {
		f.Subagents += t.Count
	}
	if err := s.st.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(u.input_tokens),0), COALESCE(SUM(u.output_tokens),0), COALESCE(SUM(u.cache_read_tokens),0), COALESCE(SUM(u.cache_creation_tokens),0)
 FROM usage u WHERE u.session_id = ? OR u.session_id IN (SELECT session_id FROM sessions WHERE parent_session_id = ?)`, pk, pk).
		Scan(&f.Requests, &f.TokensIn, &f.TokensOut, &f.TokensCacheRead, &f.TokensCache); err != nil {
		return f, errors.New("transcript: cannot sum usage")
	}
	// Files touched: path-like keys in the (already redacted) tool inputs.
	rows, err := s.st.QueryContext(ctx, `SELECT input_text_redacted FROM tool_calls WHERE session_id = ? AND input_text_redacted <> ''`, pk)
	if err != nil {
		return f, errors.New("transcript: cannot read tool inputs")
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return f, errors.New("transcript: cannot read tool inputs")
		}
		var in map[string]any
		if json.Unmarshal([]byte(raw), &in) != nil {
			continue
		}
		for _, key := range []string{"file_path", "path", "notebook_path"} {
			if v, ok := in[key].(string); ok && v != "" && len(v) < 400 {
				seen[v]++
			}
		}
		if list, ok := in["paths"].([]any); ok {
			for _, item := range list {
				if v, ok := item.(string); ok && v != "" && len(v) < 400 {
					seen[v]++
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return f, errors.New("transcript: cannot read tool inputs")
	}
	for p, n := range seen {
		f.FilesTouched = append(f.FilesTouched, transcript.NameCount{Name: p, Count: n})
	}
	sort.Slice(f.FilesTouched, func(i, j int) bool {
		if f.FilesTouched[i].Count != f.FilesTouched[j].Count {
			return f.FilesTouched[i].Count > f.FilesTouched[j].Count
		}
		return f.FilesTouched[i].Name < f.FilesTouched[j].Name
	})
	if len(f.FilesTouched) > 20 {
		f.FilesTouched = f.FilesTouched[:20]
	}
	return f, nil
}

// EnrichmentInput gathers a session's redacted text turns, Tier 0 facts and its
// subagents' purpose lines. The digest covers exactly the text a model sees.
func (s *Service) EnrichmentInput(ctx context.Context, ref transcript.SessionRef) (transcript.EnrichmentInput, error) {
	in := transcript.EnrichmentInput{Ref: ref}
	facts, err := s.SessionFacts(ctx, ref.SessionPK)
	if err != nil {
		return in, err
	}
	in.Facts = facts
	rows, err := s.st.QueryContext(ctx, `SELECT ordinal, coalesce(role, ''), text_redacted FROM events WHERE session_id = ? AND kind = 'turn' AND text_redacted <> '' ORDER BY ordinal, event_id`, ref.SessionPK)
	if err != nil {
		return in, errors.New("transcript: cannot read turns")
	}
	var all strings.Builder
	for rows.Next() {
		var t transcript.EnrichTurn
		if err := rows.Scan(&t.Seq, &t.Role, &t.Text); err != nil {
			rows.Close()
			return in, errors.New("transcript: cannot read turns")
		}
		in.Turns = append(in.Turns, t)
		in.TextBytes += len(t.Text)
		all.WriteString(t.Role)
		all.WriteByte(0)
		all.WriteString(t.Text)
		all.WriteByte(0)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return in, errors.New("transcript: cannot read turns")
	}
	rows.Close()
	in.ContentDigest = transcript.Digest([]byte(all.String()))
	prow, err := s.st.QueryContext(ctx, `SELECT description_redacted FROM sessions WHERE parent_session_id = ? AND coalesce(description_redacted, '') <> '' ORDER BY started_at NULLS FIRST, session_id LIMIT 10`, ref.SessionPK)
	if err != nil {
		return in, errors.New("transcript: cannot read subagents")
	}
	defer prow.Close()
	for prow.Next() {
		var d string
		if err := prow.Scan(&d); err != nil {
			return in, errors.New("transcript: cannot read subagents")
		}
		in.Purposes = append(in.Purposes, transcript.Clip(d, 200))
	}
	return in, prow.Err()
}

// EnrichmentDone reports whether an ok row already exists for the exact key.
func (s *Service) EnrichmentDone(ctx context.Context, pk string, tier int, promptVersion, model, contentDigest string) (bool, error) {
	var n int
	err := s.st.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrichments WHERE session_id = ? AND tier = ? AND prompt_version = ? AND model = ? AND content_digest = ? AND status = 'ok'`,
		pk, tier, promptVersion, model, contentDigest).Scan(&n)
	return n > 0, err
}

// jsonForPG redacts every string inside raw and returns it ready for a jsonb
// column. A NUL escape is replaced because jsonb cannot hold it.
func jsonForPG(raw string) (any, error) {
	if raw == "" {
		return nil, nil
	}
	clean, ok := transcript.RedactJSONValue(raw)
	if !ok {
		return nil, errors.New("transcript: enrichment result is not JSON")
	}
	return strings.ReplaceAll(clean, `\u0000`, `�`), nil
}

func enrichmentID(r transcript.EnrichmentRecord) string {
	return "live:" + sha(strings.Join([]string{r.SessionPK, strconv.Itoa(r.Tier), r.PromptVersion, r.Provider, r.Model, r.ContentDigest}, "\x00"))
}

// SaveEnrichment upserts one result, redacting it structurally.
func (s *Service) SaveEnrichment(ctx context.Context, r transcript.EnrichmentRecord) error {
	return s.SaveEnrichmentWithin(ctx, r, time.Minute)
}

// SaveEnrichmentWithin is SaveEnrichment with an explicit deadline, for
// best-effort writes that must not stall a halt.
func (s *Service) SaveEnrichmentWithin(ctx context.Context, r transcript.EnrichmentRecord, deadline time.Duration) error {
	if s == nil || s.st == nil {
		return transcript.ErrPrivateStoreRequired
	}
	result, err := jsonForPG(r.ResultJSON)
	if err != nil {
		return err
	}
	quality, err := jsonForPG(r.QualityJSON)
	if err != nil {
		return err
	}
	created := time.Now().UTC()
	if r.CreatedAt != "" {
		if t, e := time.Parse(time.RFC3339Nano, r.CreatedAt); e == nil {
			created = t.UTC()
		}
	}
	created = created.Truncate(time.Microsecond)
	if deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	r.PromptVersion, r.Model, r.Provider = transcript.Redact(r.PromptVersion), transcript.Redact(r.Model), transcript.Redact(r.Provider)
	err = s.st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var gen, rev int64
		if err := tx.QueryRowContext(ctx, `SELECT generation, content_revision FROM sessions WHERE session_id = ?`, r.SessionPK).Scan(&gen, &rev); err != nil {
			return fmt.Errorf("transcript: unknown session: %w", err)
		}
		var head int64
		if err := tx.QueryRowContext(ctx, `SELECT head FROM feed_state WHERE singleton FOR UPDATE`).Scan(&head); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoFeed
			}
			return err
		}
		id := enrichmentID(r)
		if _, err := tx.ExecContext(ctx, `INSERT INTO enrichments (enrichment_id, session_id, content_revision, tier, prompt_version, provider, model, content_digest, status,
 result_redacted, quality, error_class, tokens_in, tokens_out, calls, chunks, run_id, request_prefix, freshness, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?::jsonb,?::jsonb,?,?,?,?,?,?,?,'current',?)
ON CONFLICT (session_id, tier, prompt_version, provider, model, content_digest) DO UPDATE SET
 status = excluded.status, error_class = excluded.error_class, result_redacted = excluded.result_redacted, quality = excluded.quality,
 tokens_in = excluded.tokens_in, tokens_out = excluded.tokens_out, calls = excluded.calls, chunks = excluded.chunks, run_id = excluded.run_id,
 request_prefix = excluded.request_prefix, content_revision = excluded.content_revision, freshness = 'current', created_at = excluded.created_at`,
			id, r.SessionPK, rev, r.Tier, r.PromptVersion, r.Provider, r.Model, r.ContentDigest, r.Status, result, quality, nullStr(transcript.Redact(r.ErrorClass)),
			r.TokensIn, r.TokensOut, r.Calls, r.Chunks, nullStr(transcript.Redact(r.RunID)), transcript.Redact(r.RequestPrefix), created); err != nil {
			return err
		}
		if r.Tier == 1 && r.Status == "ok" {
			text := ""
			if rs, ok := result.(string); ok {
				text = enrichmentSearchText(rs)
			}
			if err := upsertDoc(ctx, tx, searchDoc{session: r.SessionPK, kind: "enrichment", entity: "tier1", field: "summary", digest: sha(text), generation: gen, text: text}); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO changes (seq, session_id, generation, content_revision, kind, entity_key) VALUES (?,?,?,?, 'enrichment_update', ?)`,
			head+1, r.SessionPK, gen, rev, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE feed_state SET head = ? WHERE singleton`, head+1)
		return err
	})
	if err != nil {
		return transcript.NewWriteError("save enrichment")
	}
	return nil
}

// enrichmentSearchText is the text indexed for a Tier 1 result: title, summary,
// topics and entities (already redacted).
func enrichmentSearchText(resultJSON string) string {
	var r struct {
		Title, Summary string
		Topics         []string
		Entities       struct{ Projects, Repos, Tools, Technologies []string }
	}
	var raw map[string]any
	if json.Unmarshal([]byte(resultJSON), &raw) != nil {
		return ""
	}
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &r)
	ents := append(append(append(append([]string{}, r.Entities.Projects...), r.Entities.Repos...), r.Entities.Tools...), r.Entities.Technologies...)
	parts := []string{r.Title, r.Summary, strings.Join(r.Topics, " "), strings.Join(ents, " ")}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// LatestEnrichment returns the newest ok row for a session and tier.
func (s *Service) LatestEnrichment(ctx context.Context, pk string, tier int) (transcript.EnrichmentRecord, bool, error) {
	var r transcript.EnrichmentRecord
	var created time.Time
	var errClass, runID sql.NullString
	err := s.st.QueryRowContext(ctx, `SELECT session_id, tier, prompt_version, model, provider, content_digest, status, error_class, coalesce(result_redacted::text, ''), coalesce(quality::text, ''),
 tokens_in, tokens_out, calls, chunks, run_id, request_prefix, created_at FROM enrichments
 WHERE session_id = ? AND tier = ? AND status = 'ok' ORDER BY created_at DESC, enrichment_id DESC LIMIT 1`, pk, tier).
		Scan(&r.SessionPK, &r.Tier, &r.PromptVersion, &r.Model, &r.Provider, &r.ContentDigest, &r.Status, &errClass, &r.ResultJSON, &r.QualityJSON,
			&r.TokensIn, &r.TokensOut, &r.Calls, &r.Chunks, &runID, &r.RequestPrefix, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.ErrorClass, r.RunID = errClass.String, runID.String
	r.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	return r, true, nil
}

// AppendSpend records one gateway call in the append-only ledger.
func (s *Service) AppendSpend(ctx context.Context, r transcript.SpendRecord) error {
	if s == nil || s.st == nil {
		return transcript.ErrPrivateStoreRequired
	}
	var rnd [12]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	if math.IsNaN(r.CostUSD) || math.IsInf(r.CostUSD, 0) || r.CostUSD < 0 {
		return errors.New("transcript: invalid spend cost")
	}
	err := s.st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO enrichment_spend (spend_id, enrichment_id, session_id, run_id, request_id, provider, model, tokens_in, tokens_out,
 tokens_cache_read, tokens_cache_write, cost_usd, occurred_at) VALUES (?,NULL,?,?,?,?,?,?,?,?,?,?::numeric,?)`,
			"live:"+hex.EncodeToString(rnd[:]), r.SessionPK, transcript.Redact(r.RunID), transcript.Redact(r.RequestID), transcript.Redact(r.Provider), transcript.Redact(r.Model),
			r.TokensIn, r.TokensOut, r.CacheRead, r.CacheWrite, strconv.FormatFloat(r.CostUSD, 'g', -1, 64), time.Now().UTC().Truncate(time.Microsecond))
		return err
	})
	if err != nil {
		return transcript.NewWriteError("record spend")
	}
	return nil
}

// Spend totals the ledger, for one run when runID is set and across all runs otherwise.
func (s *Service) Spend(ctx context.Context, runID string) (transcript.SpendTotals, error) {
	var t transcript.SpendTotals
	q := `SELECT COUNT(*), COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0), COALESCE(SUM(cost_usd),0)::float8 FROM enrichment_spend`
	var args []any
	if runID != "" {
		q += ` WHERE run_id = ?`
		args = append(args, runID)
	}
	if err := s.st.QueryRowContext(ctx, q, args...).Scan(&t.Calls, &t.TokensIn, &t.TokensOut, &t.CostUSD); err != nil {
		return t, errors.New("transcript: cannot read spend")
	}
	return t, nil
}

// ---- embeddings ----

func embGroupID(model, format string, dim int) string {
	return fmt.Sprintf("%s|%s|%d|cosine", model, format, dim)
}

// vecText renders a vector as pgvector text, rejecting non-finite values and the zero vector.
func vecText(v []float32) (string, error) {
	if len(v) == 0 {
		return "", errors.New("transcript: empty embedding")
	}
	var b strings.Builder
	b.WriteByte('[')
	var norm float64
	for i, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return "", errors.New("transcript: non-finite embedding value")
		}
		norm += float64(f) * float64(f)
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	if norm == 0 {
		return "", errors.New("transcript: zero embedding")
	}
	return b.String(), nil
}

// EmbeddingCurrent reports whether a vector made from exactly this text, with
// this model and prompt format, already exists.
func (s *Service) EmbeddingCurrent(ctx context.Context, pk, kind string, idx int, model, format, textDigest string) (bool, error) {
	var n int
	err := s.st.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings e JOIN embedding_groups g USING (group_id)
 WHERE e.session_id = ? AND e.kind = ? AND e.chunk_idx = ? AND g.model = ? AND g.format = ? AND e.text_digest = ?`,
		pk, kind, idx, model, format, textDigest).Scan(&n)
	return n > 0, err
}

// SaveEmbedding stores a vector under its (model, format, dimension) group.
func (s *Service) SaveEmbedding(ctx context.Context, pk, kind string, idx int, model, format, textDigest string, vec []float32) error {
	if s == nil || s.st == nil {
		return transcript.ErrPrivateStoreRequired
	}
	vt, err := vecText(vec)
	if err != nil {
		return err
	}
	model, format = transcript.Redact(model), transcript.Redact(format)
	gid := embGroupID(model, format, len(vec))
	err = s.st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var rev int64
		if err := tx.QueryRowContext(ctx, `SELECT content_revision FROM sessions WHERE session_id = ?`, pk).Scan(&rev); err != nil {
			return fmt.Errorf("unknown session: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_groups (group_id, model, format, dimension, metric) VALUES (?,?,?,?,'cosine') ON CONFLICT DO NOTHING`,
			gid, model, format, len(vec)); err != nil {
			return err
		}
		var enr any
		if kind == "summary" {
			var id string
			if tx.QueryRowContext(ctx, `SELECT enrichment_id FROM enrichments WHERE session_id = ? AND tier = 1 AND status = 'ok' ORDER BY created_at DESC, enrichment_id DESC LIMIT 1`, pk).Scan(&id) == nil {
				enr = id
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO embeddings (session_id, kind, chunk_idx, group_id, dimension, enrichment_id, content_revision, text_digest, embedding, freshness, created_at)
VALUES (?,?,?,?,?,?,?,?,?::vector,'current',?)
ON CONFLICT (session_id, kind, chunk_idx, group_id) DO UPDATE SET enrichment_id = excluded.enrichment_id, content_revision = excluded.content_revision,
 text_digest = excluded.text_digest, embedding = excluded.embedding, freshness = 'current', created_at = excluded.created_at`,
			pk, kind, idx, gid, len(vec), enr, rev, textDigest, vt, time.Now().UTC().Truncate(time.Microsecond))
		return err
	})
	if err != nil {
		return fmt.Errorf("transcript: save embedding: %w", err)
	}
	return nil
}

// SummaryTexts lists the newest ok Tier 1 result per session as embeddable text.
func (s *Service) SummaryTexts(ctx context.Context) ([]transcript.SummaryText, error) {
	rows, err := s.st.QueryContext(ctx, `SELECT DISTINCT ON (e.session_id) e.session_id, coalesce(x.native_session_id, ''), coalesce(e.result_redacted::text, '')
 FROM enrichments e LEFT JOIN sessions x ON x.session_id = e.session_id
 WHERE e.tier = 1 AND e.status = 'ok' ORDER BY e.session_id, e.created_at DESC, e.enrichment_id DESC`)
	if err != nil {
		return nil, errors.New("transcript: cannot list summaries")
	}
	defer rows.Close()
	var out []transcript.SummaryText
	for rows.Next() {
		var t transcript.SummaryText
		var raw string
		if err := rows.Scan(&t.SessionPK, &t.NativeSessionID, &raw); err != nil {
			return nil, errors.New("transcript: cannot read summaries")
		}
		t.Text = transcript.SummaryTextOf(raw)
		t.Digest = transcript.Digest([]byte(t.Text))
		out = append(out, t)
	}
	return out, rows.Err()
}

// SearchEnriched runs a full-text query over titles, summaries, topics and
// entities. Every term must match. Rank is negated ts_rank_cd so that, as with
// the SQLite store's bm25, a lower value is a better match.
func (s *Service) SearchEnriched(ctx context.Context, query string, limit int) ([]transcript.SearchHit, error) {
	q := strings.TrimSpace(query)
	if len(strings.Fields(q)) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.st.QueryContext(ctx, `WITH q AS (SELECT plainto_tsquery('simple', gostore_unaccent(translate(?::text, '<>', '  '))) AS tq)
SELECT d.session_id, coalesce(x.native_session_id, ''),
 coalesce((SELECT e.result_redacted->>'title' FROM enrichments e WHERE e.session_id = d.session_id AND e.tier = 1 AND e.status = 'ok' ORDER BY e.created_at DESC, e.enrichment_id DESC LIMIT 1), ''),
 -max(ts_rank_cd(c.chunk_fts_vec, q.tq))::float8 AS rank
FROM search_chunks c JOIN search_documents d USING (document_id) CROSS JOIN q LEFT JOIN sessions x ON x.session_id = d.session_id
WHERE d.source_kind = 'enrichment' AND c.chunk_fts_vec @@ q.tq
GROUP BY d.session_id, x.native_session_id ORDER BY rank, d.session_id LIMIT ?`, q, limit)
	if err != nil {
		return nil, errors.New("transcript: cannot search")
	}
	defer rows.Close()
	var out []transcript.SearchHit
	for rows.Next() {
		var h transcript.SearchHit
		if err := rows.Scan(&h.SessionPK, &h.NativeSessionID, &h.Title, &h.Rank); err != nil {
			return nil, errors.New("transcript: cannot read search results")
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// NearestSummaries returns the k summaries closest to vec by exact cosine
// similarity, among vectors of the same model, format and dimension.
func (s *Service) NearestSummaries(ctx context.Context, model, format string, vec []float32, k int) ([]transcript.SearchHit, error) {
	if k <= 0 || k > 200 {
		k = 10
	}
	vt, err := vecText(vec)
	if err != nil {
		return nil, nil
	}
	rows, err := s.st.QueryContext(ctx, `SELECT e.session_id, coalesce(x.native_session_id, ''),
 coalesce((SELECT en.result_redacted->>'title' FROM enrichments en WHERE en.session_id = e.session_id AND en.tier = 1 AND en.status = 'ok' ORDER BY en.created_at DESC, en.enrichment_id DESC LIMIT 1), ''),
 (1 - (e.embedding <=> ?::vector))::float8 AS sim
FROM embeddings e JOIN embedding_groups g USING (group_id) LEFT JOIN sessions x ON x.session_id = e.session_id
WHERE e.kind = 'summary' AND g.model = ? AND g.format = ? AND g.dimension = ? ORDER BY e.embedding <=> ?::vector, e.session_id LIMIT ?`,
		vt, model, format, len(vec), vt, k)
	if err != nil {
		return nil, errors.New("transcript: cannot read embeddings")
	}
	defer rows.Close()
	var out []transcript.SearchHit
	for rows.Next() {
		var h transcript.SearchHit
		if err := rows.Scan(&h.SessionPK, &h.NativeSessionID, &h.Title, &h.Rank); err != nil {
			return nil, errors.New("transcript: cannot read embeddings")
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
