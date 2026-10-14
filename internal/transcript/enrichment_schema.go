package transcript

// enrichmentSchema is the v2 addition: derived, redacted session summaries and
// their embeddings. Like every tx_* table it lives only in the owner-private
// transcripts.db.
const enrichmentSchema = `
CREATE TABLE IF NOT EXISTS tx_enrichment (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_pk TEXT NOT NULL, tier INTEGER NOT NULL,
 prompt_version TEXT NOT NULL, model TEXT NOT NULL, provider TEXT NOT NULL DEFAULT '',
 content_digest TEXT NOT NULL, status TEXT NOT NULL, error_class TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT '', quality_json TEXT NOT NULL DEFAULT '',
 tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
 calls INTEGER NOT NULL DEFAULT 0, chunks INTEGER NOT NULL DEFAULT 0,
 run_id TEXT NOT NULL DEFAULT '', request_prefix TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 UNIQUE (session_pk, tier, prompt_version, model, content_digest)
);
CREATE TABLE IF NOT EXISTS tx_enrichment_spend (
 id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL, session_pk TEXT NOT NULL, request_id TEXT NOT NULL,
 provider TEXT NOT NULL, model TEXT NOT NULL, tokens_in INTEGER NOT NULL, tokens_out INTEGER NOT NULL,
 cost_usd REAL NOT NULL DEFAULT 0, at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tx_enrichment_spend_run ON tx_enrichment_spend (run_id);
CREATE INDEX IF NOT EXISTS tx_enrichment_session ON tx_enrichment (session_pk, tier, created_at);
CREATE INDEX IF NOT EXISTS tx_enrichment_status ON tx_enrichment (status, tier);
`
