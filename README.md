# Fragments Engine

Fragments Engine (FE) is a local-first ingest → classify → route → recall
engine for information fragments — chat history, notes, links, articles,
media, and code changes. It's the inbox and the knowledge/search engine at
once: what it can't route confidently stays in the inbox for a human
decision instead of being filed silently.

> **Pre-release.** FE is unreleased (MIT-licensed; see [LICENSE](./LICENSE)) — no outside
> consumers, no compatibility guarantees. It is, however, in active
> daily use as Chrispian's own inbox and recall layer, and runs as a live
> local service. This is a working system, not a prototype; it just isn't
> released. Built in the open: this README and [`docs/`](./docs) describe
> what exists today, not a pitch for what's planned.

## What it is today

- **Ingests** Claude Code, Codex and Antigravity archives and ChatGPT exports
  into an explicit private redacted store; filesystem docs,
  git changes, saved URLs (single-link or manifest/batch), pre-fetched
  browser captures, and notes read directly out of Nil's per-vault SQLite
  databases.
- **Classifies and enriches** deterministically first — markdown/code
  attachment parsing, OCR (Apple Vision, with `tesseract` fallback), image
  metadata — with optional provider-backed image analysis (OpenAI, Ollama)
  layered on top, never replacing the deterministic record.
- **Routes** to external destinations over transport-typed adapters —
  `file`, `mcp`, `api`, `cli`, `callback` — never by app name. FE's own FFS
  filesystem output (`~/Documents/ffs`) is one such destination, not a
  special case.
- **Recalls** through FE-owned search (SQLite or embedded Vanta) over
  fragment content and provenance, with entities and durable relations.
- **Exposes one service layer** three ways — CLI, HTTP API, and MCP — so a
  human, a script, or an agent hit the same behavior.

## Private transcript storage

Claude Code, Codex, Antigravity and ChatGPT transcript ingests require an explicit
`transcripts.private_root`. An empty value refuses transcript ingestion before
collecting source files. All registered transcript adapters enforce this boundary.
This source change does not activate any importer, schedule, or synced Mac archive.

The destination is a **local owner-only store**, owned by the process's effective
OS UID. Use a dedicated root with an existing trusted parent. FE creates the
root with mode `0700` and `transcripts.db` with mode `0600`; existing unsafe modes,
foreign ownership, symlinks, hard-linked database files, writable exposed
ancestors, and a foreign database schema are refused rather than repaired.
Ownership and database identity are checked again on acceptance. This isolates
other OS users; it does not protect against the same UID, root, disk access, or
an already compromised process. Protect backups with the same permissions.

The transcript store redacts recognizable private-key blocks, common token/key
formats, authorization/cookie headers, labelled secrets, and URL credentials
**before any transcript canonical or index write**. Titles, source locators,
IDs, and nested metadata are included. Only redacted text reaches the local
SQLite text index. Pattern redaction cannot detect every unlabeled or encoded
secret, so redacted transcripts remain owner-private and must not be treated as
safe to publish.

Transcript acceptance bypasses attachment/LLM analysis, shared captures, routing,
inbox, and SQLite/Vanta/Tesseract recall. The shared canonical write boundaries
and manual intake reject transcript producers. There is no public transcript
search/export API in this slice (the owner-only Chats routes below are loopback-only and off by default). Public ingest preview refuses transcript sources
before collection because raw titles, IDs and locators can contain secrets. Raw attachments and ChatGPT raw-copy/delete
options are refused; the source archive remains read-only. Existing records in
the shared database are neither retrospectively redacted nor removed by this
change. Pending identity/media backfills containing transcript material refuse
before copying it into new canonical records. Any historical-data disposition requires separate
explicit authority before activation.

### Claude archive layout

The `claude_code` source accepts both native `root/projects/<project>/*.jsonl`
and synced `root/<project>/*.jsonl` layouts. It also reads
`<project>/<session>/subagents/**/*.jsonl`. Subagent files have distinct segment
identities even when they carry the parent's session ID; unchanged imports are
skipped. Symlinked projects/files and unrelated nested JSONL files are ignored.
The source is read-only and uses the same required private transcript store and
pre-persistence redaction path. Configuring a layout does not activate a schedule.

### Claude transcript structure and incremental capture

Besides the searchable text, a Claude ingest keeps the structure of each session
in the same owner-private `transcripts.db`: `tx_sessions` (main and subagent
sessions, keyed so `native_session_id` joins to Tether's), `tx_turns` (uuid,
parent, role, timestamp, request id), `tx_tool_calls`, `tx_usage` and
`tx_cursors`. Every string goes through the same redaction before it is stored.
The raw JSONL stays the immutable source of truth; nothing here is a copy of it,
and none of it reaches shared recall, routing or destinations.

- **Thinking** is kept as presence and byte length only, never text.
- **Tool calls** keep name, redacted input (capped at 4 KiB; over the cap only
  the top-level keys and size), start/end time, and the result's status and byte
  size. A redacted result excerpt is off by default; set
  `rules.tool_result_excerpt_bytes` (at most 2048) to keep one.
- **Usage** is tokens only (input, output, cache read, cache creation), one row
  per request id. A response split over several lines repeats its usage, so rows
  merge by maximum rather than summing lines. Prices and task or workstream
  dimensions belong to whoever joins on the session id.
- **Subagents** (`<session>/subagents/**`) are sessions of their own, linked to
  the parent session and, through `spawn_tool_use_id` from the sibling
  `.meta.json`, to the tool call that spawned them.
- **Incremental capture:** each file has a cursor (byte offset after the last
  complete line, plus a digest of the bytes before it). An unchanged file costs
  a stat; an appended file is read from its offset; a truncated or rewritten
  file fails the digest check and is re-parsed whole, replacing its rows. A
  half-written final line waits for the next run.
- A file over `max_file_size_mb` (default 1 GiB), a line over 10 MiB
  (`unreadable_line`) or a malformed line (`malformed_line`) is skipped and
  counted, never silent; unchanged files count as `unchanged`.
- The search text is rebuilt from the stored turns and capped at 12 MiB per
  session segment; `tx_turns` keeps every turn.

`fragments-engine transcript tool-calls --config PATH [--name N] [--session ID]
[--since T] [--until T] [--status ok|error|none]` and `transcript usage
--session ID` print metadata only (no text, input or output). Opening a
`transcripts.db` that has these tables with a build that predates them is
refused, so keep a backup before the first run of a new build on a live store.

### Transcript enrichment

`fragments-engine transcript enrich` describes private sessions for search:
Tier 0 facts (no model) from the stored turns and tool calls, and a Tier 1 JSON
description (title, summary, session type, outcome, topics, decisions and
follow-ups citing turn numbers, entities) from a model. Long sessions are split
into parts and combined; Claude sessions and Codex text records are covered.
Results live only in the owner-private `transcripts.db`.

Nothing is sent to a model unless the provider is listed in
`transcripts.enrichment.allowed_providers` (empty by default; a run fails closed
before any call). `exclude_sessions` and `exclude_cwd_prefixes` are a never-send
switch. Input is the redacted text only. `--dry-run` makes no call and prints
counts and a characters/4 token estimate (and a dollar upper bound when prices
are configured); `--limit N`, `--sessions a,b`, `--sample` (a reproducible
20-session verification set) and `--force` narrow or repeat a run.

```yaml
transcripts:
  private_root: /path/to/private
  enrichment:
    provider: anthropic-direct          # the allow-list id; the Tether gateway uses its own provider ids
    model: claude-haiku-5-5
    allowed_providers: [anthropic-direct]
    max_cost_usd: 2                     # per run, a hard stop
    max_total_cost_usd: 10              # across all runs, from the spend ledger; a hard stop
    anthropic:
      api_key_env: FE_ANTHROPIC_API_KEY # NAME of the environment variable; the key is never in this file
      thinking: disabled                # Haiku 5.5 thinks by default and thinking counts toward max_tokens
      pricing:                          # USD per million tokens. No built-in defaults: a run refuses to
        input_per_mtok: 0.10            # start while input and output are unset. These are claude-haiku-5-5's
        output_per_mtok: 0.50           # published prices as read from Anthropic's pricing page on
        cache_read_per_mtok: 0.01       # 2026-10-14 (platform.claude.com/docs/en/about-claude/pricing and
        cache_write_per_mtok: 0.125     # .../models/haiku-5-5/overview): 5-minute cache writes. Re-confirm
        long_prompt_threshold_tokens: 100000   # before relying on them: a request whose prompt (input plus
        long:                                  # cache reads and writes) is over 100,000 tokens pays the
          input_per_mtok: 0.50                 # higher tier, priced per request.
          output_per_mtok: 2.50
          cache_read_per_mtok: 0.05
          cache_write_per_mtok: 0.625
    stages:                             # named, reusable selections for staged runs
      s1:                               # `transcript enrich --stage s1`
        allocation:                     # "tool/bucket": how many PENDING sessions to take
          "claude/<1k": 20              # buckets are estimated prompt tokens:
          "claude/1-8k": 45             # <1k, 1-8k, 8-30k, 30-60k, >60k (map-reduce)
          "claude/8-30k": 45
          "claude/30-60k": 22
          "claude/>60k": 8
          "codex/<1k": 8
          "codex/1-8k": 72
          "codex/8-30k": 45
          "codex/30-60k": 25
          "codex/>60k": 10
      claude-rest:                      # everything left of one tool, newest first
        tool: claude
        order: newest
        allocation: {"claude/<1k": 1000, "claude/1-8k": 1000, "claude/8-30k": 1000, "claude/30-60k": 1000, "claude/>60k": 1000}
    embedding:
      provider: openai_compat_local     # or ollama; both refuse any host that is not on this machine
      base_url: http://127.0.0.1:8183
      model: qwen3-embedding-0.6b
      dim: 1024
      query_instruction: "Given a search query about a past coding conversation, retrieve the conversation summaries that answer it"
```

Staged runs: `--stage NAME` (a named allocation above), `--tool claude|codex`,
`--order newest` and `--limit N` select what to enrich. Sessions already
enriched are excluded before selection, so each stage takes the next unprocessed
sessions; within a stratum the order is a reproducible hash (or newest first).
`--dry-run` prints, per stratum, how many sessions are pending and how many the
stage selects, the expected cost (from the measured ratios: real input about 1.6x
the estimate, about 850 output tokens per call) and an upper bound. After a real
run it prints the gates for the stage: first-try JSON-valid rate (>= 90%),
failure rate (<= 5%), citation grounding (>= 95%), project/repo entity grounding
(>= 90%) and spend against the expected cost (<= 1.2x), as `stage_gates=PASS|FAIL`.
A run halted by a stop rule never passes.

Store writes and contention: the private store is a rollback-journal SQLite
file shared with the running service, so a write can meet a lock. Enrichment
writes (results, the spend ledger, embeddings, Tier-0 facts) retry on lock
contention with backoff, up to 60 seconds, and a failure names its cause as
`(busy)` or `(error)` instead of a fixed message. After a paid call the ledger
row is written first. If that write ultimately fails the run halts at once
(stops paying), drops the result, logs only the session id and reports the
unrecorded spend as `ledger_gap_usd`. If the ledger row exists but the result
cannot be saved, the session is marked failed-with-cost (best effort) and the
run halts. Both are the hard stop `stopped_by=store_write`, never counted as
provider failures. Tier-0 facts are refreshed only for the sessions a run will
enrich.

The Anthropic key is read from the named environment variable at startup, like
other service keys (FE never loads a `.env` file); it is not logged, stored or
included in any error, and provider errors are reduced to a status and
Anthropic's fixed error type (never a header, request body or response body).
To keep the key out of a shell, run enrichment under a transient user unit that
loads an owner-only env file: `systemd-run --user --pipe --wait --collect -p
EnvironmentFile=/path/to/enrich.env -- /path/to/fragments-engine transcript
enrich --config ...`. Every call, including a rejected reply, is written to the
append-only `tx_enrichment_spend` ledger with its tokens (cache reads and writes
separately) and the cost computed from the configured prices, and both caps are
enforced from that ledger. Embeddings use a local OpenAI-style `/v1/embeddings`
server (llama.cpp `--embeddings`) or Ollama; each vector records its model,
dimension and prompt format, so changing any of them re-indexes cleanly.
`transcript embed` and `transcript search [--semantic]` use them. The schema
adds tables to `transcripts.db`; a build that predates them refuses such a
database, so back it up before the first run of a new build.

### Codex session archives

The disabled `codex_sessions` example reads JSONL files recursively under its
configured archive root, including `sessions/YYYY/MM/DD` and archived sessions.
Symlinks and `.rsync-partial` transfer directories are ignored.
The parser requires `session_meta` with a thread ID and reads user/assistant
`response_item` text blocks. Event-only rollouts fall back to `user_message` and
`agent_message` events; duplicated legacy events are ignored when response
messages exist. Tool traces, reasoning and media are not imported. Physical
rollout files have distinct segment identities. A forked rollout starts with its
own `session_meta` and then replays its parent's: the first record is the file's
identity and a later one for another thread is recorded as `parent_thread_id`.

Limits bound what is kept, not what is read. Rollouts are mostly tool output and
images, so a 300 MiB file can hold a few hundred KiB of messages. A line over
16 MiB of retained message text (the private store's record cap) skips that
file, and `max_file_size_mb` is an optional hard ceiling on a single file
(default 1 GiB). A single line over 10 MiB cannot be read and fails the run as
`unreadable_line`. Chunking is not done.

Malformed records or missing identity metadata fail the run without exposing
payloads. The error carries only a fixed class (`invalid_record`,
`invalid_session_meta`, `invalid_response_item`, `invalid_event`,
`unreadable_line`, `missing_session_meta`), a hash of the archive-relative path,
a line number and a byte offset, e.g.
`transcript: collection failed (class=invalid_record file=7e874bc89692 line=2 offset=45)`.
Inputs skipped per file (`size_limit`, `pending_wal`) are counted in the run's
`skipped` total, broken out by class in the CLI summary, and logged by class.

The recorded format is based on [Codex's public rollout definitions](https://github.com/openai/codex/blob/5fe4fc8f7cd16688b5c661d8cdb76e2a340b046d/codex-rs/history/src/rollout_payload.rs)
and [message types](https://github.com/openai/codex/blob/5fe4fc8f7cd16688b5c661d8cdb76e2a340b046d/codex-rs/protocol/src/models.rs).
Acceptance requires the explicit private transcript root and redacts all
persisted text/metadata before canonical or local index writes. Shared recall,
routing, inbox and destinations are bypassed. Archives remain read-only, and
adding this source does not activate it or install a schedule.

### Antigravity archives

The disabled `antigravity` example uses an archive root containing `conversations`
and `brain`. Conversation `.db` files must contain the observed SQLite `steps`
layout (`idx`, `step_type`, `step_format`, `step_payload`). Format 0 user input
(type 14, protobuf field 19: text 2, item text 3.1, fallback query 1) and planner
response (type 15, field 20.1) are decoded in order. This bounded field mapping is
based on [firsthand descriptor-derived parser source](https://github.com/skillsynchq/txcript/blob/8cd3b0e63f797b1531a14197f41a0e9eeedec8c6/src/harness/antigravity.rs),
not a vendor compatibility guarantee. Tools, reasoning, images and opaque
metadata are omitted; unsupported formats/malformed text fail without payload
output. There is no heuristic scan for message-like protobuf strings.

SQLite is opened read-only and immutable with temporary storage in memory.
A database with a nonempty WAL/journal sidecar is skipped (`pending_wal`), not
read: pending changes are never silently ignored, and one hot database does not
block the rest of the archive. It is counted and retried on the next fire. Other
unsafe sidecars still refuse the collection. The importer does not checkpoint a
source or make raw disk copies, so the producer owns consistency: it must
checkpoint before syncing (`sqlite3 SRC ".backup DEST"`, or
`PRAGMA wal_checkpoint(TRUNCATE)` on a quiescent database) and must not sync a
nonempty `-wal` or `-journal`. Absence of a sidecar alone does not prove a
complete sync.
Do not ingest a database that is being modified; detected replacement/size/time
changes refuse collection. Source timestamps are file observations, not invented
conversation creation times.

Brain `.txt`, `.md`, `.log`, `.json` and `.jsonl` files are imported as UTF-8 text
artifacts with separate physical-file segment identities, including compact/full
logs as distinct artifacts rather than inferred conversation turns. Binary files
and media are not copied. Invalid UTF-8/control bytes fail; symlinks, incomplete
`.rsync-partial` directories are ignored; files over `max_file_size_mb`
(default 50 MiB) are skipped and counted (`size_limit`). The entire collection is prepared in memory before acceptance.
All accepted text and locators go exclusively through the private redaction store;
unchanged material is skipped. No shared recall/routing/preview or schedule is
activated by registering the source. Pattern redaction still cannot recognize
all encoded secrets, so these artifacts remain owner-private.

### Browsing chats in Sysop (owner-only)

The Sysop **Chats** page and `/v1/transcripts/*` browse, search and sort the
private store for its owner: `GET /v1/transcripts/status`, `/sessions` (filters
`tool`, `model`, `project`, `kind`, `has_error`, `has_enrichment`, `tag`, `since`,
`until`; `sort` of `started|turns|tokens|tool_calls|errors` with `order`; `q`
for full-text search with snippets; `limit` and `cursor`), `/sessions/{id}`
(paged turns, tool-call metadata, usage, subagent tree, and enrichment with turn
citations once it has run), `/facets` (tools, models, tags, counts) and
`/projects?q=` (working directories containing `q`, most used first, for
search-as-you-type).

`/sessions?q=…&mode=` picks what `q` searches: `text` (redacted transcript
text, the default), `summary` (the enrichment text: title, summary, decisions,
follow-ups, topics, entities), `semantic` (cosine similarity over the summary
vectors that `transcript embed` stored) or `hybrid` (all three fused by
reciprocal rank). Summary, semantic and hybrid hits carry `match.field`, a short
`match.excerpt` with matched words in `[ ]`, and `score`; semantic and hybrid
return a top-k with no cursor. Semantic search embeds the query on the local
embedding server from `transcripts.enrichment.embedding` (same model, dimension
and `query_instruction` as the stored vectors, loopback only) before any
database read. When that is impossible the page says why in `notice`
(`semantic_unavailable`, `no_vectors`, `model_mismatch`, `dim_mismatch`) and
hybrid falls back to summary plus text; `/status` reports `semantic: true` only
when the stored vectors match the configured model and the server answers
(reachability is cached for 15 s). Query text is never written to the log.

Reads share the database file with the ingest writers and, in DELETE journal
mode, a reader blocks their commits, so every search is short. Results are ranked
first; snippets then fill in rank order inside a 700 ms budget and are skipped for
documents over 256 KiB (`snippets_skipped` counts them); a search that cannot
finish in 4 s returns `hint: "too_broad"` instead of holding the lock. A session
with no title and no user text is named `Untitled session · <tool> · <date>`.

This is the one deliberate exception to "the API never serves transcript
content", so it is fenced:

- **Off by default.** Set `ui.transcripts.enabled: true`. Disabled, every route
  is a 404 (as is an unreadable config), and Sysop hides the Chats page.
- **Loopback only.** `serve-api` refuses to start with the flag on and a
  non-loopback `-addr`; each request must also come from a loopback peer with a
  loopback `Host`, which blocks DNS rebinding from a browser.
- **Read-only, redacted, no raw material.** Redacted text and metadata only:
  session ids are the stored digests, never source paths or native ids; no raw
  archives or attachment bytes; thinking is shown only as present and its length;
  tool calls show name, status and result size.
- **Audited.** Every read logs route, id and count, never content.
- **No credential yet, by decision.** `authorizeTranscripts` is the seam for a
  bearer token (auth is tracked separately), and the UI keeps an in-memory slot
  for one.
- **Not agent-facing.** No MCP tool or agent CLI wraps these routes; gated agent
  access is a separate design, so the usual CLI/API/MCP parity rule is deferred.

```yaml
ui:
  transcripts:
    enabled: true
```

Sysop is embedded in the binary: run `make sysop-build` before `go build`.

## Where it sits in the stack

```
  sources          Claude Code / ChatGPT logs, saved URLs, browser clipper,
                    git changes, Nil vault notes
       │
  ┌─────────┐
  │   FE    │   ingest → classify → route → recall (canonical fragment store)
  └─────────┘
       │
  destinations     filesystem (FFS), Nil (mcp), Nanite (mcp/callback),
                    Stack Explorer (api sync), any file/api/mcp/cli peer
```

FE is a peer application, not an orchestrator: it never special-cases another
Hollis Labs tool by name, only by the transport it's reached through. That
means any of those peers can be swapped or dropped without touching FE's
core.

## Examples

**Daily driver.** Chrispian drops a link, a note, or a `#link`-tagged URL
into intake; the Chrome clipper extension (`apps/fe-clipper`) sends
pre-fetched captures straight in without a second fetch. Everything lands in
the Sysop Reader's inbox, gets classified, and is either routed automatically
or held for a triage decision.

**Composition.** Agents write finished documents into FE through the MCP
`write_doc` tool — the same call this session's `write-doc` skill uses — so
a document lands in FE's inbox for Chrispian to triage on his own schedule,
rather than being pasted into chat. Separately, FE fires a fire-and-forget
`callback` destination into Loom's Nanite Curator wake integration, and syncs
reviewed GitHub-repo fragments to Stack Explorer over its `api` destination.

**Virtual filesystem.** Saved notes, quotes, reports, references, and
Pinterest pins materialize under `~/Documents/ffs` in a human-readable
layout, while FE keeps the canonical metadata, recall index, and provenance
independent of that layout.

## Roadmap

Full detail in [`docs/roadmap.md`](docs/roadmap.md). Near-term:

- **Inbox and triage UX** — domain-aware review expanding beyond GitHub/
  Pinterest (Reddit, blogs, PDFs, video), first-class note/quote/report
  creation flows, and scheduled inbox-review agents that propose actions
  instead of waiting on manual triage.
- **Recall expansion** — more deterministic entity kinds, project/workspace/
  model/tool taxonomies, and better related-fragment reasoning that shows
  durable edges alongside retrieval-time signals.
- **External integration depth** — more MCP and API destination providers,
  and destination-specific delivery/reporting policy presets.

## Quick Start

```bash
make test
go run ./cmd/fragments-engine init
go run ./cmd/fragments-engine ingest run -config ./fragments.yaml
go run ./cmd/fragments-engine search -q "project roadmap"
```

Seed local config once with `make seed-config` (copies
`fragments.example.yaml` → the gitignored `fragments.yaml`), then edit
`fragments.yaml` — the server rewrites it in place as ingests are mutated
through the CRUD endpoints, so it must never be the template itself.

Config fields like `openai.api_key_env` and `firecrawl.api_key_env` name an
*environment variable*, not the secret. FE reads it once at process startup
with a plain `os.Getenv`. An interactive shell run inherits your exports; the
service run under launchd or another service manager does not — add the
variable to that service's environment and reload it, or the key never reaches
the process.

Full operator/agent usage guide: [`docs/usage.md`](docs/usage.md). Browser
capture and the Sysop Reader: [`docs/browser-capture-reader.md`](docs/browser-capture-reader.md).
Architecture: [`docs/architecture.md`](docs/architecture.md).

Dependency migration and source-schema compatibility are documented in [Published module adoption](docs/published-module-adoption.md).
