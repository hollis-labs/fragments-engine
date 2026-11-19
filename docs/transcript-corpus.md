# Transcript corpus

The transcript corpus holds agent session transcripts (Claude Code, Codex,
Antigravity) in Postgres with pgvector. Fragments Engine is its only writer.
Readers hold `SELECT` only. Code: `internal/transcript/pgstore`.

## What is stored

The corpus keeps everything a session produced. Secrets are redacted before
storage; nothing is cut for length.

| Content | Table | Notes |
|---|---|---|
| Sessions | `sessions` | one row per source segment; subagents link to their parent |
| Turns | `events` | visible text in `text_redacted`, thinking text in `thinking_redacted` |
| Tool calls | `tool_calls` | the full input, whole; `input_state` is `complete`, `partial` or `unknown` |
| Tool results | `tool_results` | the complete result as ordered parts (see below) |
| Usage | `usage` | token counters, merged per request by maximum |
| Enrichment, embeddings | `enrichments`, `embeddings`, `embedding_groups` | summaries and cosine vectors, kept per model/format/dimension |
| Search | `search_documents`, `search_chunks` | a derived projection, rebuilt from the rows above |
| Change journal | `feed_state`, `changes` | commit-ordered feed for resumable tails |
| Held records | `pending_records`, `legacy_rows`, `legacy_material` | rows that could not be mapped, kept rather than dropped |

### Tool-result parts

A result is one row per content part, in order (`ordinal`). Exactly one of
`text_redacted`, `json_redacted` or `bytes_redacted` is set:

- text blocks are stored as text;
- base64 images are decoded and stored as bytes, with their `media_type`;
- any other block type is stored as JSON, so an unrecognised block is preserved.

Text and JSON parts are searchable. Large text is chunked for search only; the
stored part is always whole.

## Limits

There are no content limits. The ingest does not truncate input, results or
thinking, and does not skip a session for being large:

- a JSONL line is rejected only above 1 GiB (a safety ceiling, not a content rule);
- there is no file-size limit. A file is read in bounded passes of 64 MiB that
  resume at the stored cursor. A pass that stops early reports
  `batch_continues`, and the run repeats until none remain, so memory stays flat
  however large a session is.

A value that cannot be stored is kept and flagged, never dropped silently. For
example, a NUL byte cannot be stored in Postgres text; it is replaced with
U+FFFD and the event is marked `{"nul_replaced": true}`.

## Redaction and the policy flag

Strings are redacted (`transcript.Redact`) before they are stored. **Binary
parts cannot be redacted**: an image may contain something a text rule would
have removed. Each part therefore carries two flags:

| Column | Values | Meaning |
|---|---|---|
| `redaction_state` | `redacted`, `not_redactable` | whether the redactor could process the part. Every binary part is `not_redactable` (enforced by a CHECK). |
| `read_policy` | `open`, `withheld` | what a reader must enforce. |

**Today every part is `open`: all agents may read all transcripts, including
images.** The flag is in place so that withholding a class of parts later is an
update to `read_policy`, not a re-ingest or a schema change. A re-parse of the
same part does not reset a `read_policy` that was set after the fact. Readers
must treat `withheld` as "do not return the content" and may still report that
the part exists, its media type and its size.

## Retention

None. The corpus keeps everything; no purge or expiry runs. A retention policy
is a future decision. The schema keeps source provenance and the dependency
between raw, canonical and derived data so one can be added without rebuilding.

## Change journal

Each write transaction locks `feed_state`, takes the next sequence numbers and
appends to `changes` (`event_upsert`, `tool_call_upsert`, `tool_result_upsert`,
`session_update`, `enrichment_update`, `session_reset`). The sequence is the
commit order. A reader's cursor binds the feed `epoch`; an epoch change means
cached cursors are invalid. An unchanged replay of a segment appends nothing. A
re-parse (`Reset`) starts a new session `generation` and journals
`session_reset`.

## Roles and provisioning

| Role | Used for | Rights |
|---|---|---|
| schema owner | `MigrateDSN`, only for `migrate.Up` | DDL |
| writer | Fragments Engine runtime `DSN` | `SELECT, INSERT, UPDATE, DELETE` |
| reader | the transcript service | `SELECT` only |

pgvector is not a trusted extension: a superuser must run `CREATE EXTENSION
vector` once, before the schema owner runs the migrations. The migrations use
`IF NOT EXISTS`. Grant the writer and reader through `ALTER DEFAULT PRIVILEGES
FOR ROLE <owner>` so later tables inherit them. No sequences are used.

## Import from the previous store

`pgstore.Importer` copies a SQLite `transcripts.db` (schema v4) into Postgres.
It reads only a backup copy, is idempotent and resumable, keeps unmappable rows
in `legacy_rows` or `pending_records`, and reconciles every table by row count
and a canonical hash recomputed from Postgres. Rehearse it with:

```
FE_TRANSCRIPTS_IMPORT_SRC=<backup copy> FE_TRANSCRIPTS_TEST_PG_DSN=<admin dsn> \
FE_TRANSCRIPTS_TEST_PG_HOSTPORT=<host port=N> \
go test -timeout 0 -run TestImportRealBackup ./internal/transcript/pgstore
```

History imported from SQLite predates full capture: its tool results are absent
and its large sessions are truncated. Re-ingesting the raw session files into
Postgres repairs both.
