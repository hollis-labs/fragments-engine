-- Search index for search_chunks, after libs core/store/search (search.Index{Name: chunk_fts, Table: search_chunks, IDColumn: chunk_id}.GooseSQL).
-- Edited from the generated text: the library fixes the extension schema at generation time, but
-- this database lives in whatever schema the migration login creates tables in (`app` under
-- Substrate/Cerberus, `public` elsewhere). The unaccent extension and its wrapper are created
-- there, and the wrapper is qualified with it, so roles that cannot use `public` can still run it.
-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE s text := (SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass('search_chunks'));
BEGIN
  EXECUTE format('CREATE EXTENSION IF NOT EXISTS unaccent SCHEMA %I', s);
  EXECUTE format('CREATE OR REPLACE FUNCTION %I.gostore_unaccent(text) RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $f$ SELECT %I.unaccent(%L::regdictionary, $1) $f$', s, s, s || '.unaccent');
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE search_chunks ADD COLUMN chunk_fts_vec tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', gostore_unaccent(translate(coalesce(text_redacted, ''), '<>', '  '))), 'A')
) STORED;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX chunk_fts_gin ON search_chunks USING GIN (chunk_fts_vec);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS chunk_fts_gin;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS chunk_fts_trgm_text_redacted;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE search_chunks DROP COLUMN IF EXISTS chunk_fts_vec;
-- +goose StatementEnd
