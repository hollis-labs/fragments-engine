package pgstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/hollis-labs/libs/core/store"
)

// idPrefix namespaces ids that the source generated with AUTOINCREMENT by the
// immutable source identity, so a re-import of the same source maps to the same
// ids and a different source can never collide.
func (im *Importer) idPrefix() string {
	id := im.SourceIdentity
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		id = "legacy"
	}
	return id
}

// canonJSON renders JSON in one canonical form (sorted keys, numbers as
// written) so source text and Postgres jsonb compare equal.
func canonJSON(s string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func validJSONArg(s string) (arg any, canonical string, ok bool) {
	if s == "" {
		return nil, "", true
	}
	if strings.Contains(s, `\u0000`) {
		return nil, "", false
	}
	c, err := canonJSON(s)
	if err != nil {
		return nil, "", false
	}
	return s, c, true
}

// ---- enrichment ----

type enrichRow struct {
	id                                                                                             int64
	pk, prompt, model, provider, digest, status, errClass, result, quality, runID, pref, createdAt string
	tier, tin, tout, calls, chunks                                                                 int64
}

func (im *Importer) importEnrichment(ctx context.Context) error {
	return copyTable(ctx, im, spec[enrichRow]{
		table: "tx_enrichment",
		cols: `id, session_pk, tier, prompt_version, model, provider, content_digest, status, error_class, result_json, quality_json,
 tokens_in, tokens_out, calls, chunks, run_id, request_prefix, created_at`,
		scan: func(r *sql.Rows) (int64, enrichRow, error) {
			var rid int64
			var e enrichRow
			err := r.Scan(&rid, &e.id, &e.pk, &e.tier, &e.prompt, &e.model, &e.provider, &e.digest, &e.status, &e.errClass, &e.result, &e.quality,
				&e.tin, &e.tout, &e.calls, &e.chunks, &e.runID, &e.pref, &e.createdAt)
			return rid, e, err
		},
		describe: func(e enrichRow) (string, map[string]any) {
			return i2s(e.id), map[string]any{"id": e.id, "session_pk": e.pk, "tier": e.tier, "prompt_version": e.prompt, "model": e.model, "provider": e.provider,
				"content_digest": e.digest, "status": e.status, "error_class": e.errClass, "result_json": e.result, "quality_json": e.quality,
				"tokens_in": e.tin, "tokens_out": e.tout, "calls": e.calls, "chunks": e.chunks, "run_id": e.runID, "request_prefix": e.pref, "created_at": e.createdAt}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, e enrichRow) (result, error) {
			if hasNUL(e.pk, e.prompt, e.model, e.provider, e.digest, e.status, e.errClass, e.runID, e.pref) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			created, createdC, err := tsArg(e.createdAt, st)
			if err != nil || created == nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			res, resC, ok1 := validJSONArg(e.result)
			qual, qualC, ok2 := validJSONArg(e.quality)
			if !ok1 || !ok2 {
				return result{out: oRejected, reason: "invalid_json"}, nil
			}
			if e.tier < math.MinInt32 || e.tin < 0 || e.tout < 0 || e.calls < 0 || e.chunks < 0 {
				return result{out: oRejected, reason: "negative_counter"}, nil
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO enrichments (enrichment_id, session_id, content_revision, tier, prompt_version, provider, model, content_digest, status,
 result_redacted, quality, error_class, tokens_in, tokens_out, calls, chunks, run_id, request_prefix, freshness, created_at)
VALUES (?,?,1,?,?,?,?,?,?,?::jsonb,?::jsonb,?,?,?,?,?,?,?,'unverified',?) ON CONFLICT DO NOTHING`,
				im.idPrefix()+":"+i2s(e.id), e.pk, e.tier, e.prompt, e.provider, e.model, e.digest, e.status, res, qual, nullStr(e.errClass),
				e.tin, e.tout, e.calls, e.chunks, nullStr(e.runID), e.pref, created); err != nil {
				return result{}, err
			}
			return result{out: oImported, hash: canon(i2s(e.id), e.pk, i2s(e.tier), e.prompt, e.model, e.provider, e.digest, e.status, e.errClass, resC, qualC,
				i2s(e.tin), i2s(e.tout), i2s(e.calls), i2s(e.chunks), e.runID, e.pref, createdC)}, nil
		},
	})
}

// ---- embeddings ----

type embedRow struct {
	pk, kind, model, format, digest, createdAt string
	chunk, dim                                 int64
	vec                                        []byte
}

func groupID(model, format string, dim int64) string {
	return fmt.Sprintf("%s|%s|%d|cosine", model, format, dim)
}

// vectorText renders little-endian float32 bytes as pgvector text. It returns
// false for a wrong length, a non-finite value or a zero vector.
func vectorText(b []byte, dim int64) (string, bool) {
	if int64(len(b)) != dim*4 || dim == 0 {
		return "", false
	}
	var sb strings.Builder
	sb.WriteByte('[')
	var norm float64
	for i := int64(0); i < dim; i++ {
		f := math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return "", false
		}
		norm += float64(f) * float64(f)
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String(), norm > 0
}

// vectorBytes is the inverse of vectorText, used to compare what Postgres holds.
func vectorBytes(txt string) ([]byte, error) {
	txt = strings.Trim(txt, "[]")
	parts := strings.Split(txt, ",")
	out := make([]byte, 0, len(parts)*4)
	var tmp [4]byte
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint32(tmp[:], math.Float32bits(float32(f)))
		out = append(out, tmp[:]...)
	}
	return out, nil
}

func (im *Importer) importEmbeddings(ctx context.Context) error {
	return copyTable(ctx, im, spec[embedRow]{
		table: "tx_embeddings",
		cols:  `session_pk, kind, chunk_idx, model, dim, format, text_digest, vector, created_at`,
		scan: func(r *sql.Rows) (int64, embedRow, error) {
			var rid int64
			var e embedRow
			err := r.Scan(&rid, &e.pk, &e.kind, &e.chunk, &e.model, &e.dim, &e.format, &e.digest, &e.vec, &e.createdAt)
			return rid, e, err
		},
		describe: func(e embedRow) (string, map[string]any) {
			return fmt.Sprintf("%s|%s|%d|%s", e.pk, e.kind, e.chunk, e.model), map[string]any{"session_pk": e.pk, "kind": e.kind, "chunk_idx": e.chunk, "model": e.model,
				"dim": e.dim, "format": e.format, "text_digest": e.digest, "vector_hex": fmt.Sprintf("%x", e.vec), "created_at": e.createdAt}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, e embedRow) (result, error) {
			if hasNUL(e.pk, e.kind, e.model, e.format, e.digest) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			created, createdC, err := tsArg(e.createdAt, st)
			if err != nil || created == nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			vt, ok := vectorText(e.vec, e.dim)
			if !ok {
				return result{out: oRejected, reason: "invalid_vector"}, nil
			}
			gid := groupID(e.model, e.format, e.dim)
			if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_groups (group_id, model, format, dimension, metric) VALUES (?,?,?,?,'cosine') ON CONFLICT DO NOTHING`,
				gid, e.model, e.format, e.dim); err != nil {
				return result{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO embeddings (session_id, kind, chunk_idx, group_id, dimension, enrichment_id, content_revision, text_digest, embedding, freshness, created_at)
VALUES (?,?,?,?,?,NULL,1,?,?::vector,'unverified',?) ON CONFLICT DO NOTHING`, e.pk, e.kind, e.chunk, gid, e.dim, e.digest, vt, created); err != nil {
				return result{}, err
			}
			return result{out: oImported, hash: canon(e.pk, e.kind, i2s(e.chunk), e.model, i2s(e.dim), e.format, e.digest, sha(string(e.vec)), createdC)}, nil
		},
	})
}

// ---- spend ----

type spendRow struct {
	id                                int64
	run, pk, req, provider, model, at string
	tin, tout, cacheRead, cacheWrite  int64
	cost                              float64
}

func fmtCost(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func (im *Importer) importSpend(ctx context.Context) error {
	return copyTable(ctx, im, spec[spendRow]{
		table: "tx_enrichment_spend",
		cols:  `id, run_id, session_pk, request_id, provider, model, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, at`,
		scan: func(r *sql.Rows) (int64, spendRow, error) {
			var rid int64
			var s spendRow
			err := r.Scan(&rid, &s.id, &s.run, &s.pk, &s.req, &s.provider, &s.model, &s.tin, &s.tout, &s.cacheRead, &s.cacheWrite, &s.cost, &s.at)
			return rid, s, err
		},
		describe: func(s spendRow) (string, map[string]any) {
			return i2s(s.id), map[string]any{"id": s.id, "run_id": s.run, "session_pk": s.pk, "request_id": s.req, "provider": s.provider, "model": s.model,
				"tokens_in": s.tin, "tokens_out": s.tout, "tokens_cache_read": s.cacheRead, "tokens_cache_write": s.cacheWrite, "cost_usd": s.cost, "at": s.at}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, s spendRow) (result, error) {
			if hasNUL(s.run, s.pk, s.req, s.provider, s.model) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			at, atC, err := tsArg(s.at, st)
			if err != nil || at == nil {
				return result{out: oRejected, reason: "invalid_timestamp"}, nil
			}
			if s.tin < 0 || s.tout < 0 || s.cacheRead < 0 || s.cacheWrite < 0 || s.cost < 0 || math.IsNaN(s.cost) || math.IsInf(s.cost, 0) {
				return result{out: oRejected, reason: "invalid_counter_or_cost"}, nil
			}
			// enrichment_id stays NULL: run_id alone does not uniquely identify an enrichment.
			if _, err := tx.ExecContext(ctx, `INSERT INTO enrichment_spend (spend_id, enrichment_id, session_id, run_id, request_id, provider, model, tokens_in, tokens_out,
 tokens_cache_read, tokens_cache_write, cost_usd, occurred_at) VALUES (?,NULL,?,?,?,?,?,?,?,?,?,?::numeric,?) ON CONFLICT DO NOTHING`,
				im.idPrefix()+":"+i2s(s.id), s.pk, s.run, s.req, s.provider, s.model, s.tin, s.tout, s.cacheRead, s.cacheWrite, fmtCost(s.cost), at); err != nil {
				return result{}, err
			}
			return result{out: oImported, hash: canon(i2s(s.id), s.run, s.pk, s.req, s.provider, s.model, i2s(s.tin), i2s(s.tout), i2s(s.cacheRead), i2s(s.cacheWrite), fmtCost(s.cost), atC)}, nil
		},
	})
}

// ---- cursors ----

type cursorRow struct {
	id, pk, tail, sourceID    string
	size, mtime, offset, line int64
}

// importCursors preserves every legacy cursor in legacy_rows as pending. A
// digest cursor id is not reversible to a source file key, so none is used as
// an active resume offset; sources re-ingest from the start idempotently.
func (im *Importer) importCursors(ctx context.Context) error {
	return copyTable(ctx, im, spec[cursorRow]{
		table: "tx_cursors",
		cols:  `cursor_id, session_pk, file_size, mtime_ns, byte_offset, line_no, tail_digest, source_id`,
		scan: func(r *sql.Rows) (int64, cursorRow, error) {
			var rid int64
			var c cursorRow
			err := r.Scan(&rid, &c.id, &c.pk, &c.size, &c.mtime, &c.offset, &c.line, &c.tail, &c.sourceID)
			return rid, c, err
		},
		describe: func(c cursorRow) (string, map[string]any) {
			return c.id, map[string]any{"cursor_id": c.id, "session_pk": c.pk, "file_size": c.size, "mtime_ns": c.mtime, "byte_offset": c.offset,
				"line_no": c.line, "tail_digest": c.tail, "source_id": c.sourceID}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, c cursorRow) (result, error) {
			pk, row := (&Importer{}).cursorRow(c)
			if err := im.keep(ctx, tx, "tx_cursors", pk, "source_manifest_unmapped", "pending", row); err != nil {
				return result{}, err
			}
			return result{out: oPending}, nil
		},
	})
}

func (*Importer) cursorRow(c cursorRow) (string, map[string]any) {
	return c.id, map[string]any{"cursor_id": c.id, "session_pk": c.pk, "file_size": c.size, "mtime_ns": c.mtime, "byte_offset": c.offset,
		"line_no": c.line, "tail_digest": c.tail, "source_id": c.sourceID}
}

// ---- legacy transcripts table ----

type legacyRow struct {
	id, digest, material, title, content string
	redaction                            int64
}

func (im *Importer) importLegacyMaterial(ctx context.Context) error {
	return copyTable(ctx, im, spec[legacyRow]{
		table: "transcripts",
		cols:  `id, content_digest, redaction_version, material_json, title, content`,
		scan: func(r *sql.Rows) (int64, legacyRow, error) {
			var rid int64
			var l legacyRow
			err := r.Scan(&rid, &l.id, &l.digest, &l.redaction, &l.material, &l.title, &l.content)
			return rid, l, err
		},
		describe: func(l legacyRow) (string, map[string]any) {
			return l.id, map[string]any{"id": l.id, "content_digest": l.digest, "redaction_version": l.redaction, "material_json": l.material, "title": l.title, "content": l.content}
		},
		apply: func(ctx context.Context, tx store.Tx, st *TableStat, l legacyRow) (result, error) {
			if hasNUL(l.id, l.digest, l.material, l.title, l.content) {
				return result{out: oRejected, reason: "nul_in_text"}, nil
			}
			// session_id links only when a session with exactly this key exists.
			if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_material (legacy_id, session_id, content_digest, redaction_version, material_json_text, title, content)
VALUES (?, (SELECT session_id FROM sessions WHERE session_id = ?), ?,?,?,?,?) ON CONFLICT DO NOTHING`, l.id, l.id, l.digest, l.redaction, l.material, l.title, l.content); err != nil {
				return result{}, err
			}
			return result{out: oImported, hash: canon(l.id, l.digest, i2s(l.redaction), l.material, l.title, l.content)}, nil
		},
	})
}

// ---- tx_meta ----

func (im *Importer) importMeta(ctx context.Context) error {
	rows, err := im.Src.QueryContext(ctx, `SELECT key, value FROM tx_meta`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		m[k] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}
	im.ev.SourceMeta = m
	return im.saveEvidence(ctx, "running")
}

// ---- search projection ----

const (
	chunkMax          = 4096
	projectionVersion = "chunk/v1"
)

type textChunk struct {
	start, end int
	text       string
}

// chunkText splits s into contiguous, non-overlapping UTF-8 slices of at most
// chunkMax bytes, preferring a whitespace boundary. Empty text has no chunks.
func chunkText(s string) []textChunk {
	var out []textChunk
	for pos := 0; pos < len(s); {
		end := pos + chunkMax
		if end >= len(s) {
			end = len(s)
		} else {
			// back off to a rune start, then prefer the last whitespace in the back half
			for end > pos && !isRuneStart(s[end]) {
				end--
			}
			for i := end; i > pos+chunkMax/2; i-- {
				if s[i-1] == ' ' || s[i-1] == '\n' || s[i-1] == '\t' {
					end = i
					break
				}
			}
		}
		out = append(out, textChunk{pos, end, s[pos:end]})
		pos = end
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

type searchDoc struct {
	session, kind, entity, field, digest string
	generation                           int64
	text                                 string
}

func docID(d searchDoc) string {
	return sha(d.session + "\x00" + d.kind + "\x00" + d.entity + "\x00" + d.field)
}

func writeDocs(ctx context.Context, tx store.Tx, docs []searchDoc) (int, error) {
	n := 0
	for _, d := range docs {
		if d.text == "" || strings.IndexByte(d.text, 0) >= 0 {
			continue
		}
		id := docID(d)
		cs := chunkText(d.text)
		res, err := tx.ExecContext(ctx, `INSERT INTO search_documents (document_id, session_id, generation, source_revision, source_kind, entity_key, field, content_digest,
 projection_version, text_config, source_bytes, chunk_count) VALUES (?,?,?,1,?,?,?,?,?,'simple',?,?) ON CONFLICT DO NOTHING`,
			id, d.session, d.generation, d.kind, d.entity, d.field, d.digest, projectionVersion, len(d.text), len(cs))
		if err != nil {
			return n, err
		}
		if a, _ := res.RowsAffected(); a == 0 {
			continue // already projected by an earlier run
		}
		for i, c := range cs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO search_chunks (chunk_id, document_id, chunk_idx, byte_start, byte_end, text_redacted) VALUES (?,?,?,?,?,?)`,
				id+"/"+strconv.Itoa(i), id, i, c.start, c.end, c.text); err != nil {
				return n, err
			}
		}
		n++
	}
	return n, nil
}

// projStep is one canonical-text source for the search projection, paged by a
// keyset cursor so memory stays flat on a multi-million-row corpus.
type projStep struct {
	name  string
	query string // takes the cursor as (a, b) then a limit
	mk    func(r *sql.Rows) (docs []searchDoc, cursor string, err error)
}

func splitCursor(c string) (string, string) {
	a, b, _ := strings.Cut(c, "\x00")
	return a, b
}

// rebuildSearch projects canonical text into search documents and chunks. It
// is derived data: FTS5 index pages are never imported. It is idempotent: an
// existing document is skipped, so a re-run only fills what is missing.
func (im *Importer) rebuildSearch(ctx context.Context) error {
	st := im.stat("search")
	if st.Done {
		return nil
	}
	steps := []projStep{
		{"events", `SELECT e.session_id, e.event_id, e.content_digest, e.text_redacted, s.generation FROM events e JOIN sessions s USING (session_id)
 WHERE (e.session_id, e.event_id) > (?, ?) AND e.text_redacted <> '' ORDER BY e.session_id, e.event_id LIMIT ?`,
			func(r *sql.Rows) ([]searchDoc, string, error) {
				var sid, eid, dg, tx string
				var gen int64
				if err := r.Scan(&sid, &eid, &dg, &tx, &gen); err != nil {
					return nil, "", err
				}
				return []searchDoc{{sid, "event", eid, "text", dg, gen, tx}}, sid + "\x00" + eid, nil
			}},
		{"tool_inputs", `SELECT c.session_id, c.call_id, c.input_text_redacted, s.generation FROM tool_calls c JOIN sessions s USING (session_id)
 WHERE (c.session_id, c.call_id) > (?, ?) AND c.input_text_redacted <> '' ORDER BY c.session_id, c.call_id LIMIT ?`,
			func(r *sql.Rows) ([]searchDoc, string, error) {
				var sid, cid, tx string
				var gen int64
				if err := r.Scan(&sid, &cid, &tx, &gen); err != nil {
					return nil, "", err
				}
				return []searchDoc{{sid, "tool_input", cid, "input", sha(tx), gen, tx}}, sid + "\x00" + cid, nil
			}},
		{"tool_results", `SELECT r.session_id, r.call_id || '/' || r.part_id, r.content_digest, r.text_redacted, s.generation FROM tool_results r JOIN sessions s USING (session_id)
 WHERE (r.session_id, r.call_id || '/' || r.part_id) > (?, ?) AND r.text_redacted IS NOT NULL AND r.text_redacted <> '' ORDER BY r.session_id, r.call_id || '/' || r.part_id LIMIT ?`,
			func(r *sql.Rows) ([]searchDoc, string, error) {
				var sid, key, dg, tx string
				var gen int64
				if err := r.Scan(&sid, &key, &dg, &tx, &gen); err != nil {
					return nil, "", err
				}
				return []searchDoc{{sid, "tool_result", key, "text", dg, gen, tx}}, sid + "\x00" + key, nil
			}},
		{"enrichment", `SELECT e.session_id, e.enrichment_id, e.content_digest, e.result_redacted::text, s.generation FROM enrichments e JOIN sessions s USING (session_id)
 WHERE (e.session_id, e.enrichment_id) > (?, ?) AND e.status = 'ok' AND e.result_redacted IS NOT NULL ORDER BY e.session_id, e.enrichment_id LIMIT ?`,
			func(r *sql.Rows) ([]searchDoc, string, error) {
				var sid, eid, dg, js string
				var gen int64
				if err := r.Scan(&sid, &eid, &dg, &js, &gen); err != nil {
					return nil, "", err
				}
				var m map[string]json.RawMessage
				_ = json.Unmarshal([]byte(js), &m)
				var docs []searchDoc
				for _, f := range []string{"title", "summary", "topics", "entities", "decisions", "follow_ups"} {
					if raw, ok := m[f]; ok {
						docs = append(docs, searchDoc{sid, "enrichment", eid, f, dg, gen, renderJSONText(raw)})
					}
				}
				return docs, sid + "\x00" + eid, nil
			}},
	}
	for _, step := range steps {
		cur := ""
		for {
			a, b := splitCursor(cur)
			rows, err := im.Dst.QueryContext(ctx, step.query, a, b, 2000)
			if err != nil {
				return fmt.Errorf("search %s: %w", step.name, err)
			}
			var docs []searchDoc
			n := 0
			for rows.Next() {
				d, c, err := step.mk(rows)
				if err != nil {
					rows.Close()
					return err
				}
				docs = append(docs, d...)
				cur = c
				n++
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			var wrote int
			if err := im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
				var err error
				wrote, err = writeDocs(ctx, tx, docs)
				return err
			}); err != nil {
				return fmt.Errorf("search %s: %w", step.name, err)
			}
			st.Imported += int64(wrote)
		}
		im.logf("search %s: done (%d documents written so far)", step.name, st.Imported)
	}
	if err := im.projectLegacyMaterial(ctx, st); err != nil {
		return err
	}
	st.Done = true
	return im.saveEvidence(ctx, "running")
}

func (im *Importer) projectLegacyMaterial(ctx context.Context, st *TableStat) error {
	cur := ""
	for {
		rows, err := im.Dst.QueryContext(ctx, `SELECT m.legacy_id, m.content_digest, m.content FROM legacy_material m
 WHERE m.legacy_id > ? AND m.content <> '' AND NOT EXISTS (SELECT 1 FROM events e WHERE e.session_id = m.legacy_id) ORDER BY m.legacy_id LIMIT 200`, cur)
		if err != nil {
			return err
		}
		var docs []searchDoc
		for rows.Next() {
			var id, dg, content string
			if err := rows.Scan(&id, &dg, &content); err != nil {
				rows.Close()
				return err
			}
			cur = id
			// A document needs a session to hang on; unlinked legacy material is held but not projected.
			docs = append(docs, searchDoc{session: id, kind: "legacy_material", entity: id, field: "content", digest: dg, generation: 1, text: content})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(docs) == 0 {
			return nil
		}
		if err := im.Dst.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			// only sessions that exist can own a document
			var keep []searchDoc
			for _, d := range docs {
				var one int
				switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE session_id = ?`, d.session).Scan(&one); err {
				case nil:
					keep = append(keep, d)
				case sql.ErrNoRows:
				default:
					return err
				}
			}
			n, err := writeDocs(ctx, tx, keep)
			st.Imported += int64(n)
			return err
		}); err != nil {
			return err
		}
	}
}

// verifySearch checks the projection: every document's chunks are contiguous
// from byte 0 to source_bytes and match chunk_count, and each source kind has
// one document per non-empty canonical text.
func (im *Importer) verifySearch(ctx context.Context) error {
	st := im.stat("search")
	var bad int64
	if err := im.Dst.QueryRowContext(ctx, `SELECT count(*) FROM search_documents d LEFT JOIN
 (SELECT document_id, count(*) n, coalesce(sum(byte_end - byte_start),0) b, min(byte_start) lo, max(byte_end) hi FROM search_chunks GROUP BY document_id) c USING (document_id)
 WHERE coalesce(c.n,0) <> d.chunk_count OR coalesce(c.b,0) <> d.source_bytes OR (d.chunk_count > 0 AND (c.lo <> 0 OR c.hi <> d.source_bytes))`).Scan(&bad); err != nil {
		return err
	}
	// chunk-gap check: a chunk must start where the previous one ended
	var gaps int64
	if err := im.Dst.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT byte_start - lag(byte_end, 1, 0) OVER (PARTITION BY document_id ORDER BY chunk_idx) AS gap FROM search_chunks) g WHERE gap <> 0`).Scan(&gaps); err != nil {
		return err
	}
	expect := map[string]string{
		"event":       `SELECT count(*) FROM events WHERE text_redacted <> ''`,
		"tool_input":  `SELECT count(*) FROM tool_calls WHERE input_text_redacted <> ''`,
		"tool_result": `SELECT count(*) FROM tool_results WHERE text_redacted IS NOT NULL AND text_redacted <> ''`,
	}
	ok := bad == 0 && gaps == 0
	for kind, q := range expect {
		var want, got int64
		if err := im.Dst.QueryRowContext(ctx, q).Scan(&want); err != nil {
			return err
		}
		if err := im.Dst.QueryRowContext(ctx, `SELECT count(*) FROM search_documents WHERE source_kind = ?`, kind).Scan(&got); err != nil {
			return err
		}
		im.logf("search %s: %d documents for %d non-empty texts", kind, got, want)
		if got != want {
			ok = false
		}
	}
	im.logf("search: %d documents with inconsistent chunks, %d chunk gaps", bad, gaps)
	st.Verified = &ok
	return nil
}

// renderJSONText turns a JSON string or list of strings into plain text.
func renderJSONText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var l []any
	if json.Unmarshal(raw, &l) == nil {
		var b bytes.Buffer
		for i, v := range l {
			if i > 0 {
				b.WriteString("\n")
			}
			switch x := v.(type) {
			case string:
				b.WriteString(x)
			default:
				j, _ := json.Marshal(x)
				b.Write(j)
			}
		}
		return b.String()
	}
	return string(raw)
}
