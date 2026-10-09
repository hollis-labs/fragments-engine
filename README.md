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

- **Ingests** Claude Code chat history and ChatGPT exports into an explicit private
  redacted store; filesystem docs,
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

Claude Code and ChatGPT transcript ingests require an explicit
`transcripts.private_root`. An empty value refuses transcript ingestion before
collecting source files. This also applies to the reserved Codex and Antigravity
transcript source names when those adapters are added. This source change does
not activate any importer, schedule, or synced Mac archive.

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
search/export API in this slice. Raw attachments and ChatGPT raw-copy/delete
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

### Codex session archives

The disabled `codex_sessions` example reads JSONL files recursively under its
configured archive root, including `sessions/YYYY/MM/DD` and archived sessions.
Symlinks and files above `max_file_size_mb` (default 50 MiB) are ignored.
The parser requires `session_meta` with a thread ID and reads user/assistant
`response_item` text blocks. Event-only rollouts fall back to `user_message` and
`agent_message` events; duplicated legacy events are ignored when response
messages exist. Tool traces, reasoning and media are not imported. Malformed
records or conflicting/missing identity metadata fail without exposing payloads
in diagnostics. Physical rollout files have distinct segment identities.

The recorded format is based on [Codex's public rollout definitions](https://github.com/openai/codex/blob/5fe4fc8f7cd16688b5c661d8cdb76e2a340b046d/codex-rs/history/src/rollout_payload.rs)
and [message types](https://github.com/openai/codex/blob/5fe4fc8f7cd16688b5c661d8cdb76e2a340b046d/codex-rs/protocol/src/models.rs).
Acceptance requires the explicit private transcript root and redacts all
persisted text/metadata before canonical or local index writes. Shared recall,
routing, inbox and destinations are bypassed. Archives remain read-only, and
adding this source does not activate it or install a schedule.

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
