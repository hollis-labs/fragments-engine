// Package pgstore is the Postgres (pgvector) transcript corpus. Fragments
// Engine is its only writer; the shared-services reader gets SELECT only.
// Schema changes are goose migrations applied through libs core/store with a
// separate schema-owner credential; the runtime role is DML-only.
package pgstore

import (
	"embed"

	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/search"
)

//go:embed migrations
var migrationsFS embed.FS

// VersionTable is the goose version table for the transcript schema. It is
// separate from any other schema in the same database.
const VersionTable = "transcript_goose_version"

// Migrations is the transcript migration source. The sqlite directory holds
// parity stubs only: there is no SQLite transcript store.
func Migrations() migrate.Source {
	return migrate.Source{FS: migrationsFS, Root: "migrations", Table: VersionTable}
}

// ChunkIndex is the full-text index over search_chunks. Migration 0002 is its
// generated DDL; keep the two in step.
func ChunkIndex() search.Index {
	return search.Index{
		Name: "chunk_fts", Table: "search_chunks", IDColumn: "chunk_id",
		Fields:           []search.Field{{Column: "text_redacted", Weight: 1}},
		ImmutableContent: true,
	}
}
