package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
)

// Conn names where the credentials live. Connection strings are never in config.
type Conn struct {
	DSNEnv        string // runtime (DML-only) DSN
	MigrateDSNEnv string // schema-owner DSN, used only by Migrate
	// AllowInsecure permits a connection without TLS verification. It exists for
	// local development databases; the default requires sslmode=verify-full.
	AllowInsecure bool
}

// Open opens the corpus with the runtime (DML-only) credential read from the
// environment variable dsnEnv. The schema must already be migrated: the runtime
// role verifies it and never changes it.
func Open(ctx context.Context, c Conn) (*store.Store, error) {
	dsn, err := envDSN(c.DSNEnv)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: dsn, AllowInsecure: c.AllowInsecure}, SchemaMode: store.SchemaVerify})
	if err != nil {
		return nil, fmt.Errorf("transcript: open postgres corpus: %w", err)
	}
	if err := migrate.Verify(ctx, s, Migrations()); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("transcript: corpus schema is not current (run the migrate command as the schema owner): %w", err)
	}
	return s, nil
}

// Migrate applies the corpus migrations with the schema-owner credential
// (c.MigrateDSNEnv) and the runtime credential (c.DSNEnv). It is the only place the owner credential is used.
func Migrate(ctx context.Context, c Conn) error {
	dsn, err := envDSN(c.DSNEnv)
	if err != nil {
		return err
	}
	mdsn, err := envDSN(c.MigrateDSNEnv)
	if err != nil {
		return err
	}
	cfg := store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: dsn, MigrateDSN: mdsn, AllowInsecure: c.AllowInsecure}}
	return migrate.Up(ctx, cfg, Migrations())
}

func envDSN(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("transcript: no environment variable is configured for the Postgres credential")
	}
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", fmt.Errorf("transcript: environment variable %s is empty", name)
	}
	return v, nil
}

// ConnFor builds a Conn from the transcripts.postgres config section.
func ConnFor(c config.TranscriptPG) Conn {
	return Conn{DSNEnv: c.DSNEnv, MigrateDSNEnv: c.MigrateDSNEnv, AllowInsecure: c.AllowInsecure}
}
