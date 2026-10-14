package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// Tier 0 facts are deterministic: counts and names computed from the tx_*
// tables, no model involved. They are plain data, so they are safe to hand to a
// prompt header and to store as a tier-0 enrichment row.
type SessionFacts struct {
	Tool, NativeSessionID, Kind, Model, CWD, GitBranch string
	StartedAt, EndedAt                                 string
	DurationSec                                        int64
	UserTurns, AssistantTurns                          int
	ToolCalls, ToolErrors                              int
	TopTools                                           []NameCount
	Subagents                                          int
	SubagentTypes                                      []NameCount
	Requests                                           int
	TokensIn, TokensOut, TokensCacheRead, TokensCache  int64
	FilesTouched                                       []NameCount
}

type NameCount struct {
	Name  string
	Count int
}

// SessionRef identifies a main session that has text to enrich.
type SessionRef struct {
	SessionPK, NativeSessionID, EndedAt, Tool string
	// Legacy marks a record with text only (Codex today): no tx_* rows, so its
	// turns are recovered from the stored "## role" text.
	Legacy bool
}

// EnrichTurn is one text turn as the model will see it; Seq is tx_turns.seq.
type EnrichTurn struct {
	Seq        int
	Role, Text string
}

// EnrichmentInput is everything Tier 1 needs for one session, all redacted.
type EnrichmentInput struct {
	Ref           SessionRef
	Facts         SessionFacts
	Turns         []EnrichTurn
	Purposes      []string // subagent purpose lines, redacted
	ContentDigest string   // digest of the turn text actually sent
	TextBytes     int
}

type EnrichmentRecord struct {
	SessionPK, PromptVersion, Model, Provider, ContentDigest string
	Tier                                                     int
	Status, ErrorClass, ResultJSON, QualityJSON              string
	TokensIn, TokensOut, Calls, Chunks                       int
	RunID, RequestPrefix, CreatedAt                          string
}

// MainSessions lists main sessions with at least one text turn whose last
// activity is older than minIdle, so a session still being written is not
// enriched (and re-enriched) on every run.
func (s *Store) MainSessions(ctx context.Context, minIdle time.Duration, now time.Time, nativeID string) ([]SessionRef, error) {
	if s == nil {
		return nil, ErrPrivateStoreRequired
	}
	q := `SELECT s.session_pk, s.native_session_id, s.ended_at, s.tool FROM tx_sessions s
 WHERE s.kind = 'main' AND EXISTS (SELECT 1 FROM tx_turns t WHERE t.session_pk = s.session_pk AND t.text_redacted <> '')`
	var args []any
	if nativeID != "" {
		q += ` AND s.native_session_id = ?`
		args = append(args, nativeID)
	}
	if minIdle > 0 {
		q += ` AND (s.ended_at = '' OR s.ended_at <= ?)`
		args = append(args, stamp(now.Add(-minIdle)))
	}
	q += ` ORDER BY s.started_at, s.session_pk`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errors.New("transcript: cannot list sessions")
	}
	defer rows.Close()
	var out []SessionRef
	for rows.Next() {
		var r SessionRef
		if err := rows.Scan(&r.SessionPK, &r.NativeSessionID, &r.EndedAt, &r.Tool); err != nil {
			return nil, errors.New("transcript: cannot read sessions")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LegacySessions lists Codex records that have searchable text but no
// structured rows. Their last-activity time is unknown, so MinIdle does not
// apply to them.
func (s *Store) LegacySessions(ctx context.Context, nativeID string) ([]SessionRef, error) {
	if s == nil {
		return nil, ErrPrivateStoreRequired
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, material_json FROM transcripts WHERE id NOT IN (SELECT session_pk FROM tx_sessions) ORDER BY id`)
	if err != nil {
		return nil, errors.New("transcript: cannot list records")
	}
	defer rows.Close()
	var out []SessionRef
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, errors.New("transcript: cannot read records")
		}
		var m struct{ Source, SourceID string }
		if json.Unmarshal([]byte(raw), &m) != nil || m.Source != "codex" || (nativeID != "" && m.SourceID != nativeID) {
			continue
		}
		out = append(out, SessionRef{SessionPK: id, NativeSessionID: m.SourceID, Tool: "codex", Legacy: true})
	}
	return out, rows.Err()
}

// legacyInput rebuilds a text-only record's turns and a minimal fact set.
func (s *Store) legacyInput(ctx context.Context, ref SessionRef) (EnrichmentInput, error) {
	in := EnrichmentInput{Ref: ref}
	var content, raw string
	if err := s.db.QueryRowContext(ctx, `SELECT content, material_json FROM transcripts WHERE id = ?`, ref.SessionPK).Scan(&content, &raw); err != nil {
		return in, errors.New("transcript: unknown record")
	}
	var m struct {
		CreatedAt time.Time
		Metadata  map[string]any
	}
	_ = json.Unmarshal([]byte(raw), &m)
	cwd, _ := m.Metadata["cwd"].(string)
	in.Facts = SessionFacts{Tool: ref.Tool, NativeSessionID: ref.NativeSessionID, Kind: "main", CWD: cwd, StartedAt: stamp(m.CreatedAt)}
	var all strings.Builder
	for i, t := range splitTurns(content) {
		t.Seq = i
		in.Turns = append(in.Turns, t)
		in.TextBytes += len(t.Text)
		if t.Role == "user" {
			in.Facts.UserTurns++
		} else {
			in.Facts.AssistantTurns++
		}
		all.WriteString(t.Role)
		all.WriteByte(0)
		all.WriteString(t.Text)
		all.WriteByte(0)
	}
	in.ContentDigest = digest([]byte(all.String()))
	return in, nil
}

// splitTurns recovers turns from "## user" / "## assistant" blocks. A marker
// only counts at the start of a line between blank lines, so ordinary text
// containing the words does not split.
func splitTurns(content string) []EnrichTurn {
	lines := strings.Split(content, "\n")
	var out []EnrichTurn
	role := ""
	var buf []string
	flush := func() {
		if role != "" {
			text := strings.TrimSpace(strings.Join(buf, "\n"))
			if text != "" {
				out = append(out, EnrichTurn{Role: role, Text: text})
			}
		}
		buf = nil
	}
	for i, l := range lines {
		isMarker := (l == "## user" || l == "## assistant") && (i == 0 || lines[i-1] == "") && i+1 < len(lines) && lines[i+1] == ""
		if isMarker {
			flush()
			role = strings.TrimPrefix(l, "## ")
			continue
		}
		buf = append(buf, l)
	}
	flush()
	return out
}

// SessionFacts computes Tier 0 for a session, subagent totals included.
func (s *Store) SessionFacts(ctx context.Context, pk string) (SessionFacts, error) {
	var f SessionFacts
	err := s.db.QueryRowContext(ctx, `SELECT tool, native_session_id, kind, model, cwd, git_branch, started_at, ended_at
 FROM tx_sessions WHERE session_pk = ?`, pk).Scan(&f.Tool, &f.NativeSessionID, &f.Kind, &f.Model, &f.CWD, &f.GitBranch, &f.StartedAt, &f.EndedAt)
	if err != nil {
		return f, errors.New("transcript: unknown session")
	}
	if a, e1 := time.Parse(time.RFC3339Nano, f.StartedAt); e1 == nil {
		if b, e2 := time.Parse(time.RFC3339Nano, f.EndedAt); e2 == nil && b.After(a) {
			f.DurationSec = int64(b.Sub(a).Seconds())
		}
	}
	scanCount := func(q string, args ...any) ([]NameCount, error) {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []NameCount
		for rows.Next() {
			var n NameCount
			if err := rows.Scan(&n.Name, &n.Count); err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, rows.Err()
	}
	roles, err := scanCount(`SELECT role, COUNT(*) FROM tx_turns WHERE session_pk = ? GROUP BY role`, pk)
	if err != nil {
		return f, errors.New("transcript: cannot count turns")
	}
	for _, r := range roles {
		if r.Name == "user" {
			f.UserTurns = r.Count
		} else if r.Name == "assistant" {
			f.AssistantTurns = r.Count
		}
	}
	if f.TopTools, err = scanCount(`SELECT name, COUNT(*) AS n FROM tx_tool_calls WHERE session_pk = ? GROUP BY name ORDER BY n DESC, name LIMIT 10`, pk); err != nil {
		return f, errors.New("transcript: cannot count tools")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(result_status = 'error'), 0) FROM tx_tool_calls WHERE session_pk = ?`, pk).Scan(&f.ToolCalls, &f.ToolErrors); err != nil {
		return f, errors.New("transcript: cannot count tools")
	}
	if f.SubagentTypes, err = scanCount(`SELECT CASE WHEN agent_type = '' THEN 'unknown' ELSE agent_type END, COUNT(*) AS n FROM tx_sessions WHERE parent_session_pk = ? GROUP BY 1 ORDER BY n DESC, 1`, pk); err != nil {
		return f, errors.New("transcript: cannot count subagents")
	}
	for _, t := range f.SubagentTypes {
		f.Subagents += t.Count
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(u.input_tokens),0), COALESCE(SUM(u.output_tokens),0), COALESCE(SUM(u.cache_read_tokens),0), COALESCE(SUM(u.cache_creation_tokens),0)
 FROM tx_usage u WHERE u.session_pk = ? OR u.session_pk IN (SELECT session_pk FROM tx_sessions WHERE parent_session_pk = ?)`, pk, pk).
		Scan(&f.Requests, &f.TokensIn, &f.TokensOut, &f.TokensCacheRead, &f.TokensCache); err != nil {
		return f, errors.New("transcript: cannot sum usage")
	}
	// Files touched: path-like keys in the (already redacted) tool inputs.
	rows, err := s.db.QueryContext(ctx, `SELECT input_redacted FROM tx_tool_calls WHERE session_pk = ? AND input_redacted <> ''`, pk)
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
	}
	if err := rows.Err(); err != nil {
		return f, errors.New("transcript: cannot read tool inputs")
	}
	for p, n := range seen {
		f.FilesTouched = append(f.FilesTouched, NameCount{p, n})
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
func (s *Store) EnrichmentInput(ctx context.Context, ref SessionRef) (EnrichmentInput, error) {
	if ref.Legacy {
		return s.legacyInput(ctx, ref)
	}
	in := EnrichmentInput{Ref: ref}
	facts, err := s.SessionFacts(ctx, ref.SessionPK)
	if err != nil {
		return in, err
	}
	in.Facts = facts
	rows, err := s.db.QueryContext(ctx, `SELECT seq, role, text_redacted FROM tx_turns WHERE session_pk = ? AND text_redacted <> '' ORDER BY seq`, ref.SessionPK)
	if err != nil {
		return in, errors.New("transcript: cannot read turns")
	}
	var all strings.Builder
	for rows.Next() {
		var t EnrichTurn
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
	in.ContentDigest = digest([]byte(all.String()))
	prow, err := s.db.QueryContext(ctx, `SELECT description_redacted FROM tx_sessions WHERE parent_session_pk = ? AND description_redacted <> '' ORDER BY started_at, session_pk LIMIT 10`, ref.SessionPK)
	if err != nil {
		return in, errors.New("transcript: cannot read subagents")
	}
	defer prow.Close()
	for prow.Next() {
		var d string
		if err := prow.Scan(&d); err != nil {
			return in, errors.New("transcript: cannot read subagents")
		}
		in.Purposes = append(in.Purposes, clip(d, 200))
	}
	return in, prow.Err()
}

// EnrichmentDone reports whether an ok row already exists for the exact key.
func (s *Store) EnrichmentDone(ctx context.Context, pk string, tier int, promptVersion, model, contentDigest string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_enrichment WHERE session_pk = ? AND tier = ? AND prompt_version = ? AND model = ? AND content_digest = ? AND status = 'ok'`,
		pk, tier, promptVersion, model, contentDigest).Scan(&n)
	return n > 0, err
}

// SaveEnrichment upserts one result. The result JSON is redacted structurally
// (string values, never the JSON syntax) so a model echoing a secret cannot
// persist it.
func (s *Store) SaveEnrichment(ctx context.Context, r EnrichmentRecord) error {
	if s == nil {
		return ErrPrivateStoreRequired
	}
	if err := s.checkOwnership(); err != nil {
		return err
	}
	clean := func(raw string) (string, error) {
		if raw == "" {
			return "", nil
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return "", errors.New("transcript: enrichment result is not JSON")
		}
		out, err := json.Marshal(redactValue(v))
		return string(out), err
	}
	result, err := clean(r.ResultJSON)
	if err != nil {
		return err
	}
	quality, err := clean(r.QualityJSON)
	if err != nil {
		return err
	}
	if r.CreatedAt == "" {
		r.CreatedAt = stamp(time.Now())
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tx_enrichment (session_pk, tier, prompt_version, model, provider, content_digest, status, error_class,
 result_json, quality_json, tokens_in, tokens_out, calls, chunks, run_id, request_prefix, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(session_pk, tier, prompt_version, model, content_digest) DO UPDATE SET
  provider = excluded.provider, status = excluded.status, error_class = excluded.error_class, result_json = excluded.result_json,
  quality_json = excluded.quality_json, tokens_in = excluded.tokens_in, tokens_out = excluded.tokens_out, calls = excluded.calls,
  chunks = excluded.chunks, run_id = excluded.run_id, request_prefix = excluded.request_prefix, created_at = excluded.created_at`,
		r.SessionPK, r.Tier, Redact(r.PromptVersion), Redact(r.Model), Redact(r.Provider), r.ContentDigest, r.Status, Redact(r.ErrorClass),
		result, quality, r.TokensIn, r.TokensOut, r.Calls, r.Chunks, Redact(r.RunID), Redact(r.RequestPrefix), r.CreatedAt)
	if err != nil {
		return errors.New("transcript: cannot save enrichment")
	}
	if r.Tier == 1 && r.Status == "ok" {
		return s.indexEnrichment(ctx, r.SessionPK, result)
	}
	return nil
}

// indexEnrichment keeps the private full-text index in step with the newest ok
// Tier 1 result. The text is already redacted.
func (s *Store) indexEnrichment(ctx context.Context, pk, resultJSON string) error {
	var r struct {
		Title, Summary string
		Topics         []string
		Entities       struct{ Projects, Repos, Tools, Technologies []string }
	}
	var raw map[string]any
	if json.Unmarshal([]byte(resultJSON), &raw) != nil {
		return errors.New("transcript: cannot index enrichment")
	}
	// Field names in the stored contract are lower snake case.
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &r)
	ents := append(append(append(append([]string{}, r.Entities.Projects...), r.Entities.Repos...), r.Entities.Tools...), r.Entities.Technologies...)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("transcript: cannot index enrichment")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tx_enrichment_search WHERE session_pk = ?`, pk); err != nil {
		return errors.New("transcript: cannot index enrichment")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tx_enrichment_search (session_pk, title, summary, topics, entities) VALUES (?,?,?,?,?)`,
		pk, r.Title, r.Summary, strings.Join(r.Topics, " "), strings.Join(ents, " ")); err != nil {
		return errors.New("transcript: cannot index enrichment")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("transcript: cannot index enrichment")
	}
	return nil
}

// LatestEnrichment returns the newest ok row for a session and tier.
func (s *Store) LatestEnrichment(ctx context.Context, pk string, tier int) (EnrichmentRecord, bool, error) {
	var r EnrichmentRecord
	err := s.db.QueryRowContext(ctx, `SELECT session_pk, tier, prompt_version, model, provider, content_digest, status, error_class, result_json, quality_json,
 tokens_in, tokens_out, calls, chunks, run_id, request_prefix, created_at FROM tx_enrichment
 WHERE session_pk = ? AND tier = ? AND status = 'ok' ORDER BY created_at DESC, id DESC LIMIT 1`, pk, tier).
		Scan(&r.SessionPK, &r.Tier, &r.PromptVersion, &r.Model, &r.Provider, &r.ContentDigest, &r.Status, &r.ErrorClass, &r.ResultJSON, &r.QualityJSON,
			&r.TokensIn, &r.TokensOut, &r.Calls, &r.Chunks, &r.RunID, &r.RequestPrefix, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// SpendRecord is one gateway call: tokens as reported by the provider and the
// cost the gateway observed. The ledger is append-only so a cap holds across
// runs even when a result row is later replaced.
type SpendRecord struct {
	RunID, SessionPK, RequestID, Provider, Model string
	TokensIn, TokensOut                          int
	CostUSD                                      float64
}

type SpendTotals struct {
	Calls               int
	TokensIn, TokensOut int64
	CostUSD             float64
}

func (s *Store) AppendSpend(ctx context.Context, r SpendRecord) error {
	if s == nil {
		return ErrPrivateStoreRequired
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tx_enrichment_spend (run_id, session_pk, request_id, provider, model, tokens_in, tokens_out, cost_usd, at)
 VALUES (?,?,?,?,?,?,?,?,?)`, Redact(r.RunID), r.SessionPK, Redact(r.RequestID), Redact(r.Provider), Redact(r.Model), r.TokensIn, r.TokensOut, r.CostUSD, stamp(time.Now()))
	if err != nil {
		return errors.New("transcript: cannot record spend")
	}
	return nil
}

// Spend totals the ledger, for one run when runID is set and across all runs
// when it is empty.
func (s *Store) Spend(ctx context.Context, runID string) (SpendTotals, error) {
	var t SpendTotals
	q := `SELECT COUNT(*), COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0), COALESCE(SUM(cost_usd),0) FROM tx_enrichment_spend`
	var args []any
	if runID != "" {
		q += ` WHERE run_id = ?`
		args = append(args, runID)
	}
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&t.Calls, &t.TokensIn, &t.TokensOut, &t.CostUSD); err != nil {
		return t, errors.New("transcript: cannot read spend")
	}
	return t, nil
}
