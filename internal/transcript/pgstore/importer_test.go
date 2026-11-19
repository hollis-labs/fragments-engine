package pgstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	_ "modernc.org/sqlite"
)

// fixtureDB builds a tiny schema-v4 SQLite database with the edge cases found
// in the live corpus.
func fixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE tx_sessions (session_pk TEXT PRIMARY KEY, tool TEXT NOT NULL, native_session_id TEXT NOT NULL, segment_key TEXT NOT NULL, kind TEXT NOT NULL,
 parent_session_pk TEXT NOT NULL DEFAULT '', parent_native_id TEXT NOT NULL DEFAULT '', agent_type TEXT NOT NULL DEFAULT '', spawn_depth INTEGER NOT NULL DEFAULT 0,
 spawn_tool_use_id TEXT NOT NULL DEFAULT '', is_fork INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '', cwd TEXT NOT NULL DEFAULT '', git_branch TEXT NOT NULL DEFAULT '',
 entrypoint TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '', turn_count INTEGER NOT NULL DEFAULT 0, description_redacted TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE tx_turns (session_pk TEXT NOT NULL, seq INTEGER NOT NULL, event_uuid TEXT NOT NULL, parent_uuid TEXT NOT NULL DEFAULT '', role TEXT NOT NULL, ts TEXT NOT NULL DEFAULT '',
 request_id TEXT NOT NULL DEFAULT '', is_sidechain INTEGER NOT NULL DEFAULT 0, stop_reason TEXT NOT NULL DEFAULT '', text_redacted TEXT NOT NULL DEFAULT '',
 has_thinking INTEGER NOT NULL DEFAULT 0, thinking_bytes INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (session_pk, event_uuid))`,
		`CREATE TABLE tx_tool_calls (tool_use_id TEXT NOT NULL, session_pk TEXT NOT NULL, turn_uuid TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, input_redacted TEXT NOT NULL DEFAULT '',
 result_status TEXT NOT NULL DEFAULT 'none', result_bytes INTEGER NOT NULL DEFAULT 0, result_excerpt_redacted TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL DEFAULT '',
 ended_at TEXT NOT NULL DEFAULT '', target_redacted TEXT NOT NULL DEFAULT '', PRIMARY KEY (session_pk, tool_use_id))`,
		`CREATE TABLE tx_usage (session_pk TEXT NOT NULL, request_id TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', ts TEXT NOT NULL DEFAULT '', input_tokens INTEGER NOT NULL DEFAULT 0,
 output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_creation_tokens INTEGER NOT NULL DEFAULT 0, effort TEXT NOT NULL DEFAULT '', PRIMARY KEY (session_pk, request_id))`,
		`CREATE TABLE tx_enrichment (id INTEGER PRIMARY KEY AUTOINCREMENT, session_pk TEXT NOT NULL, tier INTEGER NOT NULL, prompt_version TEXT NOT NULL, model TEXT NOT NULL,
 provider TEXT NOT NULL DEFAULT '', content_digest TEXT NOT NULL, status TEXT NOT NULL, error_class TEXT NOT NULL DEFAULT '', result_json TEXT NOT NULL DEFAULT '',
 quality_json TEXT NOT NULL DEFAULT '', tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0, calls INTEGER NOT NULL DEFAULT 0, chunks INTEGER NOT NULL DEFAULT 0,
 run_id TEXT NOT NULL DEFAULT '', request_prefix TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL)`,
		`CREATE TABLE tx_embeddings (session_pk TEXT NOT NULL, kind TEXT NOT NULL, chunk_idx INTEGER NOT NULL, model TEXT NOT NULL, dim INTEGER NOT NULL, format TEXT NOT NULL DEFAULT '',
 text_digest TEXT NOT NULL, vector BLOB NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (session_pk, kind, chunk_idx, model))`,
		`CREATE TABLE tx_enrichment_spend (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL, session_pk TEXT NOT NULL, request_id TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
 tokens_in INTEGER NOT NULL, tokens_out INTEGER NOT NULL, tokens_cache_read INTEGER NOT NULL DEFAULT 0, tokens_cache_write INTEGER NOT NULL DEFAULT 0, cost_usd REAL NOT NULL DEFAULT 0, at TEXT NOT NULL)`,
		`CREATE TABLE tx_cursors (cursor_id TEXT PRIMARY KEY, session_pk TEXT NOT NULL, file_size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, byte_offset INTEGER NOT NULL, line_no INTEGER NOT NULL,
 tail_digest TEXT NOT NULL, source_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE transcripts (id TEXT PRIMARY KEY, content_digest TEXT NOT NULL, redaction_version INTEGER NOT NULL, material_json TEXT NOT NULL, title TEXT NOT NULL, content TEXT NOT NULL)`,
		`CREATE TABLE tx_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO tx_meta VALUES ('schema_version','4')`,
		`INSERT INTO tx_enrichment (session_pk,tier,prompt_version,model,provider,content_digest,status,result_json,created_at,tokens_in) VALUES
 ('p',1,'v1','m','prov','dg','ok','{"title":"Plan the import","decisions":["use goose","exact cosine"],"summary":"s"}','2026-11-19T11:00:00Z',7)`,
		`INSERT INTO tx_embeddings VALUES ('p','summary',0,'qwen',3,'doc-plain/v1','td',x'0000803f0000000000000000','2026-11-19T11:00:01Z')`,
		`INSERT INTO tx_embeddings VALUES ('p','summary',1,'qwen',3,'doc-plain/v1','td2',x'000000000000000000000000','2026-11-19T11:00:01Z')`,
		`INSERT INTO tx_enrichment_spend (run_id,session_pk,request_id,provider,model,tokens_in,tokens_out,cost_usd,at) VALUES ('r','p','q','prov','m',1,2,0.0001622,'2026-11-19T11:00:02Z')`,
		`INSERT INTO tx_cursors VALUES ('cur1','p',10,20,30,40,'tail','src')`,
		`INSERT INTO transcripts VALUES ('p','cd',1,'{}','Linked','linked content')`,
		`INSERT INTO transcripts VALUES ('zzz','cd2',1,'{}','Unlinked','unlinked content')`,
		// parent first, child second; the child has an empty start time like many live Claude sessions
		`INSERT INTO tx_sessions (session_pk,tool,native_session_id,segment_key,kind,started_at,ended_at,turn_count) VALUES ('p','claude','np','','main','2026-11-19T10:00:00.123456789Z','2026-11-19T10:05:00Z',3)`,
		`INSERT INTO tx_sessions (session_pk,tool,native_session_id,segment_key,kind,parent_session_pk,is_fork) VALUES ('c','claude','nc','','sub','p',1)`,
		`INSERT INTO tx_turns (session_pk,seq,event_uuid,role,ts,text_redacted) VALUES ('p',1,'e1','user','2026-11-19T10:00:00.123456789Z','héllo wörld')`,
		`INSERT INTO tx_turns (session_pk,seq,event_uuid,parent_uuid,role,ts,text_redacted) VALUES ('p',2,'e2','e1','assistant','','plain')`,
		"INSERT INTO tx_turns (session_pk,seq,event_uuid,role,ts,text_redacted) VALUES ('p',3,'e3','user','2026-11-19T10:00:02Z','bad' || char(0) || 'nul')",
		`INSERT INTO tx_tool_calls (tool_use_id,session_pk,turn_uuid,name,input_redacted,result_status,result_bytes,result_excerpt_redacted,started_at) VALUES ('t1','p','e2','Bash','{"cmd":"ls"}','ok',12,'file.txt','2026-11-19T10:00:03Z')`,
		`INSERT INTO tx_tool_calls (tool_use_id,session_pk,turn_uuid,name,input_redacted) VALUES ('t2','p','e2','Read','{"truncated":true,"bytes":9000,"keys":["a"]}')`,
		`INSERT INTO tx_tool_calls (tool_use_id,session_pk,turn_uuid,name) VALUES ('t3','p','','Grep')`,
		`INSERT INTO tx_tool_calls (tool_use_id,session_pk,turn_uuid,name) VALUES ('t4','p','e3','Edit')`,
		`INSERT INTO tx_usage (session_pk,request_id,model,ts,input_tokens,output_tokens) VALUES ('p','r1','m','2026-11-19T10:00:04Z',5,6)`,
		`INSERT INTO tx_usage (session_pk,request_id,input_tokens) VALUES ('p','r2',-1)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return db
}

func TestImportEdgeCasesIdempotentAndReconciled(t *testing.T) {
	cfg, _, cleanup := openScratch(t)
	defer cleanup()
	ctx := context.Background()
	if err := migrate.Up(ctx, cfg, Migrations()); err != nil {
		t.Fatal(err)
	}
	dst, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	src := fixtureDB(t)
	defer src.Close()

	run := func() *Importer {
		im := &Importer{Src: src, Dst: dst, RunID: "run1", SourceIdentity: "fixture", Batch: 2}
		if err := im.Run(ctx); err != nil {
			t.Fatal(err)
		}
		return im
	}
	run()
	im := run() // second run must change nothing

	want := map[string][4]int64{ // imported, pending, rejected, source
		"tx_sessions":         {2, 0, 0, 2},
		"tx_turns":            {2, 0, 1, 3}, // NUL turn rejected
		"tx_tool_calls":       {2, 2, 0, 4}, // t3 empty turn_uuid, t4 points at the rejected turn
		"tx_usage":            {1, 0, 1, 2}, // negative counter rejected
		"tx_enrichment":       {1, 0, 0, 1},
		"tx_embeddings":       {1, 0, 1, 2}, // zero vector rejected
		"tx_enrichment_spend": {1, 0, 0, 1},
		"tx_cursors":          {0, 1, 0, 1}, // held in legacy_rows, not mapped
		"transcripts":         {2, 0, 0, 2},
	}
	for table, w := range want {
		st := im.ev.Tables[table]
		got := [4]int64{st.Imported, st.Pending, st.Rejected, st.SourceRows}
		if got != w {
			t.Errorf("%s: imported/pending/rejected/source = %v, want %v", table, got, w)
		}
		if st.Verified == nil || !*st.Verified {
			t.Errorf("%s: reconciliation failed: %+v", table, st)
		}
	}
	q := func(query string) (s string) {
		if err := dst.QueryRowContext(ctx, query).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return
	}
	if got := q(`SELECT parent_session_id FROM sessions WHERE session_id = 'c'`); got != "p" {
		t.Errorf("parent link = %q", got)
	}
	if got := q(`SELECT input_state FROM tool_calls WHERE call_id = 't2'`); got != "partial" {
		t.Errorf("truncated input_state = %q", got)
	}
	if got := q(`SELECT payload_redacted->>'legacy_ts' FROM events WHERE event_id = 'e1'`); got != "2026-11-19T10:00:00.123456789Z" {
		t.Errorf("original nanosecond timestamp not preserved: %q", got)
	}
	if got := q(`SELECT count(*)::text FROM legacy_rows WHERE reason = 'nul_in_text' AND source_table = 'tx_turns' AND convert_from(payload_redacted, 'UTF8') LIKE '%\\u0000%'`); got != "1" {
		t.Errorf("NUL row not preserved exactly in legacy_rows: %s", got)
	}
	if got := q(`SELECT count(*)::text FROM search_documents WHERE source_kind = 'enrichment' AND field IN ('title','decisions','summary')`); got != "3" {
		t.Errorf("enrichment search documents: %s", got)
	}
	if got := q(`SELECT count(*)::text FROM search_chunks WHERE text_redacted LIKE '%exact cosine%'`); got != "1" {
		t.Errorf("enrichment decisions not searchable: %s", got)
	}
	if got := q(`SELECT count(*)::text FROM search_documents WHERE source_kind = 'legacy_material'`); got != "0" {
		t.Errorf("legacy material projected although the session has events: %s", got)
	}
	if got := q(`SELECT count(*)::text FROM legacy_material WHERE session_id IS NULL`); got != "1" {
		t.Errorf("unlinked legacy transcript: %s", got)
	}
	if st := im.ev.Tables["search"]; st.Verified == nil || !*st.Verified {
		t.Errorf("search projection did not verify: %+v", st)
	}
	if got := q(`SELECT count(*)::text FROM sessions WHERE content_digest = 'unset'`); got != "0" {
		t.Errorf("sessions without digest: %s", got)
	}
	if got := q(`SELECT count(*)::text FROM tool_results WHERE part_id = 'legacy-excerpt' AND result_complete = false`); got != "1" {
		t.Errorf("legacy excerpt results: %s", got)
	}
}

// TestImportRealBackup rehearses the import on a backup copy of the live
// database. Set FE_TRANSCRIPTS_IMPORT_SRC to the backup path.
func TestImportRealBackup(t *testing.T) {
	path := os.Getenv("FE_TRANSCRIPTS_IMPORT_SRC")
	if path == "" {
		t.Skip("FE_TRANSCRIPTS_IMPORT_SRC not set")
	}
	cfg, _, cleanup := openScratch(t)
	if os.Getenv("FE_TRANSCRIPTS_KEEP") == "" {
		defer cleanup()
	}
	ctx := context.Background()
	if err := migrate.Up(ctx, cfg, Migrations()); err != nil {
		t.Fatal(err)
	}
	dst, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	src, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	im := &Importer{Src: src, Dst: dst, RunID: "rehearsal", SourceIdentity: os.Getenv("FE_TRANSCRIPTS_IMPORT_ID"), Batch: 5000, Log: t.Logf}
	if err := im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	for table, st := range im.ev.Tables {
		t.Logf("%-14s source=%d imported=%d pending=%d rejected=%d ns_truncated=%d reasons=%v verified=%v",
			table, st.SourceRows, st.Imported, st.Pending, st.Rejected, st.TruncatedNS, st.RejectReason, st.Verified != nil && *st.Verified)
		if st.Verified == nil || !*st.Verified {
			t.Errorf("%s did not reconcile", table)
		}
	}
}
