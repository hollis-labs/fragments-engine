# Fragments Engine

Fragments Engine (FE) is a local-first ingest → classify → route → recall
engine for information fragments — chat history, notes, links, articles,
media, and code changes. It's the inbox and the knowledge/search engine at
once: what it can't route confidently stays in the inbox for a human
decision instead of being filed silently.

> **Pre-release.** FE is unreleased and unlicensed — no public repo, no
> outside consumers, no compatibility guarantees. It is, however, in active
> daily use as Chrispian's own inbox and recall layer, and runs as a live
> local service. This is a working system, not a prototype; it just isn't
> released. Built in the open: this README and [`docs/`](./docs) describe
> what exists today, not a pitch for what's planned.

## What it is today

- **Ingests** Claude Code chat history, ChatGPT exports, filesystem docs,
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
long-running dev service (Cerberus resource `fragments-engine-dev`,
launchd-managed) does not — add the variable to that resource's `env:` block
and reload it, or the key never reaches the process.

Full operator/agent usage guide: [`docs/usage.md`](docs/usage.md). Browser
capture and the Sysop Reader: [`docs/browser-capture-reader.md`](docs/browser-capture-reader.md).
Architecture: [`docs/architecture.md`](docs/architecture.md).
