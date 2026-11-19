-- +goose Up
-- The transcript corpus lives only in Postgres (pgvector). This file exists so
-- the per-dialect migration sets keep identical version numbers
-- (migrate.CheckParity). There is no SQLite transcript store, fallback or cache.
SELECT 1;

-- +goose Down
SELECT 1;
