package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
)

// Open opens the corpus with the runtime (DML-only) credential read from the
// environment variable dsnEnv. The schema must already be migrated: the runtime
// role verifies it and never changes it.
func Open(ctx context.Context, dsnEnv string) (*store.Store, error) {
	dsn, err := envDSN(dsnEnv)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: dsn}, SchemaMode: store.SchemaVerify})
	if err != nil {
		return nil, fmt.Errorf("transcript: open postgres corpus: %w", err)
	}
	if err := migrate.Verify(ctx, s, Migrations()); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("transcript: corpus schema is not current (run the migrate command as the schema owner): %w", err)
	}
	return s, nil
}

// Migrate applies the corpus migrations with the schema-owner credential read
// from migrateEnv, and the runtime credential from dsnEnv for the privilege
// check. It is the only place the owner credential is used.
func Migrate(ctx context.Context, dsnEnv, migrateEnv string) error {
	dsn, err := envDSN(dsnEnv)
	if err != nil {
		return err
	}
	mdsn, err := envDSN(migrateEnv)
	if err != nil {
		return err
	}
	cfg := store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: dsn, MigrateDSN: mdsn}}
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
