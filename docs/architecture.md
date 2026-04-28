# Fragments Engine — Architecture

## Architecture Boundary

Fragments Engine is a peer application in the portfolio, not a privileged coordinator inside a larger runtime.

That means:

- FE owns its own ingest pipeline, inbox, routing state, provenance, and recall/index layer.
- FE is itself the knowledge/search engine for fragments in this domain.
- FE may publish content to external systems, but those systems are always peers reached through explicit interfaces.
- Destination kinds are transport-oriented, not app-oriented.

Two concepts must stay separate:

1. **FE internal index / recall engine**
   This is FE's own search and relationship layer. It can start with SQLite FTS and later use an embedded recall engine such as Vanta behind an internal interface.
2. **FE external destinations**
   These are outputs reached through adapters such as `file`, `api`, `mcp`, or `cli`. Nil, Nanite, or any other app would be configured through one of those transport kinds rather than appearing as hard-coded destination types.

## Pipeline Overview

```
┌─────────────────────────────────────────────────────────────┐
│                        SOURCES                              │
│  Claude logs · ChatGPT exports · URL manifests · Git ·     │
│  Web clips · Manual                                        │
│  RSS/Atom · YouTube · Any Carrier connector                │
└──────────────────────────┬──────────────────────────────────┘
                           │ normalize → Fragment
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                    CLASSIFY & ENRICH                        │
│                                                             │
│  Stage 1 — Deterministic                                    │
│    · Source-type rules                                      │
│    · Regex / keyword classifiers                            │
│    · Dedup (content hash, later richer methods)             │
│                                                             │
│  Stage 2 — AI enrichment                                    │
│    · Summary / title generation                             │
│    · Tag refinement / zero-shot classification              │
│    · Entity extraction                                      │
│    · Relationship hints for recall                          │
└──────────────────────────┬──────────────────────────────────┘
                           │ Fragment + tags + confidence
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                     ROUTE DECISION                          │
│                                                             │
│  confidence ≥ threshold  →  auto-route to destination       │
│  confidence < threshold  →  hold in Inbox for review        │
│  explicit rule match     →  route (overrides AI)            │
└──────────────┬───────────────────────────┬──────────────────┘
               │ auto-route                │ low confidence
               ▼                           ▼
┌─────────────────────┐       ┌────────────────────────────┐
│ EXTERNAL DESTINATIONS│      │           INBOX            │
│                     │       │                            │
│  · file             │       │  · Review UI               │
│  · api              │       │  · Manual tag / route      │
│  · mcp              │       │  · Approve / reject        │
│  · cli              │       │  · Teach classifier        │
└─────────────────────┘       └────────────────────────────┘
```

## Internal Knowledge Role

FE does not disappear after routing.

FE keeps its own canonical fragment records and indexing metadata so it can answer:

- where a fragment came from
- how it was normalized
- what routes matched
- whether it was staged or auto-routed
- where it was sent
- how it relates to other fragments
- how to find it later through search and recall
- which normalized entities it has extracted and linked across fragments

FE should expose two different relationship views:

- durable FE-owned relations stored in SQLite
- retrieval-time related results returned by the active recall backend

Those are complementary but not interchangeable. A stored edge means FE has committed to that relationship as local knowledge. A recall hit means the current backend ranked something as relevant at query time.

External destinations receive exported copies or rendered artifacts. FE remains the canonical system for fragment provenance and recall within its own domain.

## Core Entities

### Fragment

The unit of information. Everything becomes a Fragment on ingestion.

```go
Fragment {
  id            string
  source        string
  source_id     string
  content       string
  title         string
  created_at    time.Time
  ingested_at   time.Time
  summary       string
  confidence    float64
  status        enum
  route         *RouteTarget
  metadata      map[string]any
}
```

### Route

An external-output rule made of a destination + condition + priority.

```go
Route {
  id             string
  name           string
  condition      Expr
  destination    Destination   // file | api | mcp | cli
  priority       int
  confidence_min float64
}
```

### Destination kinds

- `file` — write a filesystem bundle at `{root}/{canonical_path}/fragment.md` with sibling `attachments/`
- `api` — POST or PUT to an external HTTP API
- `mcp` — call an external MCP tool/server
- `cli` — invoke a local CLI adapter or script

App-level integrations are configured on top of those transports:

- Nil would be an `mcp` or `api` destination
- another Vanta runtime would be an external `api` or `mcp` destination if desired
- any portfolio app remains a peer, not a special destination kind in FE

Current first provider adapters:

- `mcp.nil_inbox` — calls Nil's `nil_create_inbox` tool over stdio MCP
- `api.nanite_messaging` — posts FE fragment exports into Nanite's messaging API
- `api.nanite_user_mailbox` — specialized Nanite messaging route for `to_agent_id=user` in a concrete session inbox
- `cli.corpus_cli` — runs a local command with FE fragment JSON on stdin

Operational delivery behavior:

- FE applies retry/backoff at the transport layer, with defaults scoped by destination kind
- FE transport retry defaults are now config-driven globally and can still be overridden per destination
- FE destination status now exposes the resolved retry and queue policy in effect for that peer
- FE now exposes first-class destination policy mutation surfaces instead of requiring raw JSON edits for common retry and queue tuning
- FE destination lifecycle now includes first-class rename, dry-run validation, and guarded delete operations
- FE route lifecycle now includes first-class rename, dry-run preview against staged inbox items, and guarded delete operations
- FE ingest lifecycle now includes first-class list, dry-run validation, side-effect-free preview, and archive-policy mutation operations
- FE now enforces ChatGPT text-export archive roots under `~/Documents/corpus/ai-chat-logs/chatgpt/logs`
- FE records delivery attempts in `route_log` as structured FE-owned audit events
- FE computes destination status and delivery metrics from its own logs plus live transport probes
- FE now persists queued destination retries in SQLite through `go-queue`, but FE itself still owns routing and delivery semantics
- FE long-running wrappers can auto-drain queued delivery retries in the background through a config-driven polling loop
- FE queue inspection can now be scoped by destination id so operators can isolate one external peer at a time
- FE failed-job replay now performs a destination-status preflight and requires an explicit override when the target is still unhealthy
- FE now keeps a separate queue audit stream for replay/purge/dead-letter lifecycle events instead of overloading `route_log`
- FE can now report destination-level queue alert summaries derived from queued state, dead-letter history, and current destination reachability
- FE queue policy is now config-driven for replay cooldowns, replay-rate limits, and alert thresholds
- FE destination configs can now override those queue defaults per peer integration through a `queue_policy` block

Current queue note:

- `go-queue` in `~/Projects-apps/framework/libs/go-queue` is now the backing store for FE’s delivery retry queue.
- FE is using the SQLite driver directly and draining jobs through FE-owned logic so route decisions, inbox state, and `route_log` remain canonical inside FE.

## Internal Recall Layer

If FE adopts Vanta, it should be embedded as FE's internal recall/index implementation, not modeled as an external destination.

That embedded role would look like this:

```text
fragments/
  chats/claude/{date}/{session_id}
  chats/chatgpt/{date}/{conv_id}
  kb/{topic}/{fragment_id}
  inbox/{fragment_id}
```

Long-term external chat-log archive convention:

```text
~/Documents/corpus/ai-chat-logs/
  claude/logs/
  chatgpt/logs/
```

For ChatGPT exports, FE currently ingests from `conversations-*.json` and can mirror those text export files into `~/Documents/corpus/ai-chat-logs/chatgpt/logs/<export-folder>/` before parsing. FE now rejects archive roots outside that canonical vendor tree. FE also persists attachment and URL-reference records from the export metadata. Local text-like attachments now pass through a deterministic FE enrichment step before fragment upsert, so markdown frontmatter and extracted attachment text become part of FE-owned recall state instead of remaining destination-only metadata. FE also has an embedded `.docx` extractor and a first multimodal image path: deterministic image dimensions/format, optional `tesseract` OCR, FE-owned image analysis summary/tags/signals, optional provider-backed `vision_analysis` through the attachment analyzer adapter layer, decoded metadata in fragment detail, and preview generation in file-bundle publishing. When a fragment is routed to a `file` destination, FE copies local attachments into the fragment bundle and records per-fragment `storage_path`; remote URLs remain references.

For standalone saved URLs, FE now supports `url_source` as a deterministic ingest. The current shape is manifest-driven and local-first:

- operators drop `.txt`, `.json`, or `.jsonl` URL manifests into a source directory
- FE fetches the URL directly
- FE extracts readable text for HTML and plain text bodies
- FE extracts text PDFs with embedded-first and optional local-tool fallback adapters
- FE extracts YouTube transcripts when captions are available, with optional `yt-dlp` fallback
- FE can also use `yt-dlp` for known non-YouTube video-page URLs when subtitles are available
- FE stages other images/videos as reference fragments until richer extraction is added
- FE always preserves the canonical URL as a fragment-linked reference attachment
FE ingest preview for `chatgpt_export` is intentionally side-effect-free and does not perform archive copy/delete operations.

The important boundary is:

- FE service layer stays recall-engine-agnostic
- any embedded recall engine sits behind an internal FE recall/index interface
- FE transport wrappers still call FE services, not the embedded engine directly

Current FE recall implementations:

- `sqlite` for deterministic local FTS
- embedded `vanta` for FE-owned BM25 or hybrid recall

Current FE embedding-provider support for embedded `vanta`:

- `ollama`
- `llama` as an alias for `ollama`
- `openai`

If the configured embedding provider is unavailable at runtime, FE should remain searchable and fall back to non-embedding recall instead of coupling the product to a remote dependency.

Current deterministic durable relation examples:

- `shared_repo`
- `shared_workspace`
- `shared_model`
- `shared_tool`
- `shared_source_type`

## Attachments

FE now has a first-class attachment/reference record model:

- fragments remain the canonical textual unit
- attachments and URL references are linked FE-owned records
- FE can persist local file references from source exports plus external URLs cited in the source metadata
- FE does not need to download or transform remote media to preserve provenance

Current first-pass behavior:

- ChatGPT export ingest records attachment references when the export metadata provides them
- ChatGPT export ingest also records URL references surfaced in message metadata
- these records are visible through fragment detail in CLI, API, and MCP
- `file` destination publish now co-locates copied local attachments under the fragment bundle

Near-term rule:

- attachment storage and attachment-aware ingest come before multimodal analysis
- planned `url` ingest should reuse the same FE-owned attachment/reference model instead of inventing a separate storage path
- `shared_terms`
- `shared_topic_terms`

FE should also persist normalized entities as first-class local records so it can support direct lookups such as:

- all fragments mentioning model `llama3.1`
- all fragments tied to repo `sample-project`
- all fragments associated with workspace `foo`

Those persisted entities should also be usable in other deterministic FE systems:

- route conditions
- inbox triage
- bulk manual routing for staged inbox clusters
- constrained search/recall
- future classifier rules

Current entity-aware inbox review surfaces:

- list entity groups across staged inbox items
- inspect inbox items for a specific persisted entity
- manually apply a route to all staged items matching an entity, while preserving per-fragment route-log audit

## Classification Strategy

### Deterministic first, AI second

Run classifiers in priority order:

1. Source-type rules
2. Regex / keyword
3. Bayesian
4. AI enrichment

Combined confidence = weighted average of firing classifiers. Threshold is configurable per route.

### Learning loop

When a user manually routes an inbox item:

- the classification decision is recorded as a training sample
- Bayesian classifiers retrain on next run
- the inbox teaches the system over time

## Storage

Single SQLite database:

- `fragments` — core entity
- `entities` — normalized FE-owned entity records
- `fragment_entities` — fragment-to-entity joins with source and confidence
- `inbox` — staged fragments pending review
- `routes` — routing rules
- `destinations` — external destination configs
- `route_log` — audit trail of every routing decision
- `classifiers` — classifier definitions + trained state
- `relationships / summaries / index metadata` — FE-owned knowledge metadata

## Carrier Transition Path

1. Carrier's ingest sources become FE connectors
2. Carrier's corpus becomes a FE destination through `file`
3. Carrier's blueprint/generate pipeline continues operating against whatever corpus FE produces
4. FE's Go runtime gradually replaces the current Python ingest/runtime path
