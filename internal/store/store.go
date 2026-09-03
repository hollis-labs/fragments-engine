package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	DB     *sql.DB
	dbPath string
}

func Open(dbPath string) (*Store, error) {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec %s: %w", pragma, err)
		}
	}

	s := &Store{DB: db, dbPath: absPath}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := s.backfillFragmentIdentity(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("backfill fragment identity: %w", err)
	}
	if err := s.backfillMedia(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("backfill media: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func (s *Store) DBPath() string {
	return s.dbPath
}

func (s *Store) migrate() error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	ctx := context.Background()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn: %w", err)
	}
	defer conn.Close()

	for _, pragma := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("exec %s on migration conn: %w", pragma, err)
		}
	}

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		applied, err := migrationApplied(ctx, conn, entry.Name())
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		for _, stmt := range splitSQL(string(data)) {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("exec migration %s statement: %w\nSQL: %s", entry.Name(), err, stmt)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations(name, applied_at)
			VALUES (?, CURRENT_TIMESTAMP)
		`, entry.Name()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func migrationApplied(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var applied string
	err := conn.QueryRowContext(ctx, `
		SELECT name
		FROM schema_migrations
		WHERE name = ?
	`, name).Scan(&applied)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, fmt.Errorf("lookup migration %s: %w", name, err)
}

func splitSQL(sqlText string) []string {
	var out []string
	var buf strings.Builder
	depth := 0
	for _, raw := range strings.Split(sqlText, ";") {
		upper := strings.ToUpper(strings.TrimSpace(raw))
		depth += strings.Count(upper, "BEGIN") - strings.Count(upper, "END")
		if buf.Len() > 0 {
			buf.WriteByte(';')
		}
		buf.WriteString(raw)
		if depth <= 0 {
			out = append(out, buf.String())
			buf.Reset()
			depth = 0
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}
