package pgstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/search"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationParity(t *testing.T) {
	if err := migrate.CheckParity(Migrations()); err != nil {
		t.Fatal(err)
	}
}

// pgDSN returns an admin DSN for a scratch Postgres with pgvector, or skips.
// CI fails instead of skipping.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("FE_TRANSCRIPTS_TEST_PG_DSN")
	if dsn == "" {
		if os.Getenv("CI") != "" && os.Getenv("CI") != "0" && os.Getenv("CI") != "false" {
			t.Fatal("FE_TRANSCRIPTS_TEST_PG_DSN is required in CI")
		}
		t.Skip("FE_TRANSCRIPTS_TEST_PG_DSN not set")
	}
	return dsn
}

// openScratch gives a private schema-owning role/database pair so the migration
// runs as a non-superuser owner, like production, and returns the runtime and
// migrate configs.
func openScratch(t *testing.T) (store.Config, string, func()) {
	t.Helper()
	admin, err := sql.Open("pgx", pgDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	if len(name) > 40 {
		name = name[:40]
	}
	db, owner, rt, ro := "t_"+name, "own_"+name, "rt_"+name, "ro_"+name
	ctx := context.Background()
	for _, q := range []string{
		fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, db),
		fmt.Sprintf(`DROP ROLE IF EXISTS %s`, owner), fmt.Sprintf(`DROP ROLE IF EXISTS %s`, rt), fmt.Sprintf(`DROP ROLE IF EXISTS %s`, ro),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'p'`, ro),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'o'`, owner),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'r'`, rt),
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, db, owner),
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// pgvector is not a trusted extension: a database owner cannot create it, so
	// provisioning must (this mirrors what Cerberus has to do for fe_transcripts).
	dbAdmin, err := sql.Open("pgx", strings.Replace(pgDSN(t), "dbname=postgres", "dbname="+db, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE EXTENSION vector`,
		// Provisioning grants: tables the owner creates later are DML for the
		// writer and SELECT-only for the reader. No sequences are used.
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s`, owner, rt),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT ON TABLES TO %s`, owner, ro),
	} {
		if _, err := dbAdmin.ExecContext(ctx, q); err != nil {
			t.Fatalf("provision %s: %v", q, err)
		}
	}
	dbAdmin.Close()
	host := os.Getenv("FE_TRANSCRIPTS_TEST_PG_HOSTPORT") // e.g. "127.0.0.1 port=55441"
	cfg := store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{
		DSN:           fmt.Sprintf("host=%s user=%s password=r dbname=%s sslmode=disable", host, rt, db),
		MigrateDSN:    fmt.Sprintf("host=%s user=%s password=o dbname=%s sslmode=disable", host, owner, db),
		AllowInsecure: true,
	}}
	readerDSN := fmt.Sprintf("host=%s user=%s password=p dbname=%s sslmode=disable", host, ro, db)
	return cfg, readerDSN, func() {
		for _, q := range []string{
			fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, db),
			fmt.Sprintf(`DROP ROLE IF EXISTS %s`, owner), fmt.Sprintf(`DROP ROLE IF EXISTS %s`, rt), fmt.Sprintf(`DROP ROLE IF EXISTS %s`, ro),
		} {
			if _, err := admin.ExecContext(ctx, q); err != nil {
				t.Errorf("cleanup %s: %v", q, err)
			}
		}
		admin.Close()
	}
}

func TestMigrateAsOwnerRuntimeIsDMLOnly(t *testing.T) {
	cfg, _, cleanup := openScratch(t)
	defer cleanup()
	ctx := context.Background()
	src := Migrations()
	if err := migrate.Up(ctx, cfg, src); err != nil {
		t.Fatalf("migrate as owner: %v", err)
	}
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := migrate.Verify(ctx, s, src); err != nil {
		t.Fatalf("verify with runtime role: %v", err)
	}
	// The runtime role is DML-only: it cannot create tables.
	if _, err := s.ExecContext(ctx, `CREATE TABLE rogue (x int)`); err == nil {
		t.Fatal("runtime role created a table")
	}
	now := store.Canonical(timeNow())
	must := func(q string, a ...any) error {
		return s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			_, err := tx.ExecContext(ctx, q, a...)
			return err
		})
	}
	if err := must(`INSERT INTO sessions (session_id, harness, native_session_id, segment_key, ingest_state, content_digest) VALUES ('s1','claude','n1','','current','d')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if err := must(`INSERT INTO embedding_groups VALUES ('g1','m','doc-plain/v1',3,'cosine')`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO embeddings (session_id,kind,chunk_idx,group_id,dimension,content_revision,text_digest,embedding,created_at) VALUES ('s1','summary',?,'g1',?,1,'x',?::vector,?)`
	if err := must(ins, 0, 3, "[1,0,0]", now); err != nil {
		t.Fatalf("valid vector: %v", err)
	}
	for name, c := range map[string][]any{
		"zero vector":        {1, 3, "[0,0,0]", now},
		"dimension mismatch": {2, 3, "[1,0]", now},
		"NaN":                {3, 3, "[NaN,0,0]", now},
	} {
		if err := must(ins, c...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A tool result must hold exactly one representation.
	if err := must(`INSERT INTO tool_results VALUES ('s1','c','p',0,'text/plain',NULL,NULL,NULL,true,'d')`); err == nil {
		t.Error("tool result with no payload was accepted")
	}
	// Cosine ordering works with the untyped vector column.
	var id int
	if err := s.QueryRowContext(ctx, `SELECT chunk_idx FROM embeddings ORDER BY embedding <=> ?::vector LIMIT 1`, "[1,0,0]").Scan(&id); err != nil || id != 0 {
		t.Errorf("cosine query: %v (%d)", err, id)
	}
}

func timeNow() time.Time { return time.Now() }

func TestReaderRoleIsSelectOnly(t *testing.T) {
	cfg, readerDSN, cleanup := openScratch(t)
	defer cleanup()
	ctx := context.Background()
	if err := migrate.Up(ctx, cfg, Migrations()); err != nil {
		t.Fatal(err)
	}
	rcfg := store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: readerDSN, AllowInsecure: true}, SchemaMode: store.SchemaVerify}
	r, err := store.Open(ctx, rcfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := migrate.Verify(ctx, r, Migrations()); err != nil {
		t.Fatalf("reader verify: %v", err)
	}
	var n int
	if err := r.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("reader select: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO sources VALUES ('a','h','h','k','r','p')`,
		`UPDATE feed_state SET head = head + 1`,
		`DELETE FROM sessions`,
		`CREATE TABLE rogue (x int)`,
		`TRUNCATE sessions`,
	} {
		err := r.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error { _, err := tx.ExecContext(ctx, q); return err })
		if err == nil {
			t.Errorf("reader ran %q", q)
		}
	}
}

func TestChunkSearchThroughReaderRole(t *testing.T) {
	cfg, readerDSN, cleanup := openScratch(t)
	defer cleanup()
	ctx := context.Background()
	if err := migrate.Up(ctx, cfg, Migrations()); err != nil {
		t.Fatal(err)
	}
	w, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	err = w.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, q := range []string{
			`INSERT INTO sessions (session_id, harness, native_session_id, segment_key, ingest_state, content_digest) VALUES ('s1','claude','n1','','current','d')`,
			`INSERT INTO search_documents VALUES ('d1','s1',1,1,'event','e1','text','dg','v1','simple',18,1)`,
			`INSERT INTO search_chunks (chunk_id,document_id,chunk_idx,byte_start,byte_end,text_redacted) VALUES ('d1/0','d1',0,0,18,'Straße café plan')`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Open(ctx, store.Config{Driver: store.DriverPostgres, SchemaMode: store.SchemaVerify,
		Postgres: store.PostgresConfig{DSN: readerDSN, AllowInsecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	sr, err := search.New(r.Dialect(), ChunkIndex())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"cafe", "strasse", `"café plan"`} {
		hits, err := sr.Search(ctx, r.Reader(), search.Query{Text: q, Limit: 5})
		if err != nil || len(hits) != 1 || hits[0].ID != "d1/0" {
			t.Errorf("search %q: %v %+v", q, err, hits)
		}
	}
}
