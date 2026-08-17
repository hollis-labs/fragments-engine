# Roadmap

This is the high-level Fragments Engine roadmap after the current deterministic core.

## Current State

FE already has:

- manual intake through the shared fragment service
- Claude Code ingest
- ChatGPT text-export ingest
- deterministic filesystem docs ingest
- deterministic git-changes ingest
- canonical fragment persistence and provenance
- FE-owned recall with SQLite and embedded Vanta options
- entities and deterministic durable relations
- inbox, routes, external destinations, and queue operations
- thin CLI, API, and MCP wrappers over the same service layer
- a vendored Sysop shell for operator workflows
- multimodal attachment enrichment with deterministic and provider-backed image analysis
- explicit attachment reanalysis flows through CLI, API, and MCP
- domain-aware inbox review for GitHub repos and Pinterest pins
- Stack Explorer sync/scan integration for reviewed GitHub repo fragments
- FFS file destinations and inbox-preserving materialization
- direct sysop `Save to FFS` for notes, quotes, reports, pins, and references
- a sysop Library page for folder-like browsing over processed fragments
- deterministic link enrichment for manual single-URL saves (local extraction primary, async Firecrawl fallback for bot-blocked sources), triggered by a bare URL or a `#link` hashtag alongside a URL embedded in other text
- pre-fetched-content intake for clients that already extracted a page themselves (`source_url`/`description`/`selection` on `/v1/intake`), first used by the `apps/fe-clipper` Chrome web clipper extension
- `nil_vault` ingest reading notes directly out of Nil's per-vault SQLite databases
- a fifth destination kind, `callback` (fire-and-forget, async-delivery-queue-only), plus `go-directives` ingest wiring, for the Loom pilot's Nanite Curator integration

## Reality Check

The written phase names lag the repo a bit.

FE is no longer just "the deterministic core" plus future ideas. In practice it
already acts as:

- a working local-first inbox for manual saves and imported material
- a provenance/search layer for chats, URLs, docs, git changes, and attachments
- a transport-based router with queue/retry controls

That means the next meaningful work is less about proving FE can ingest/store
fragments and more about making inbox review and downstream automation match
real operator usage.

As of the latest FFS work, FE is also becoming the user's virtual filesystem for
high-value saved information. The filesystem output under `~/Documents/ffs` is
the durable human-readable layer; FE remains the canonical metadata, recall,
provenance, and routing layer.

## Phase 1: Attachment Planning

Goal:

- decide how FE should treat binary and media artifacts before enabling them

Scope:

- define where attachments live in the corpus tree
- define whether FE copies attachments, references them in place, or supports both
- define dedupe and content-addressing rules
- define provenance fields for attachment origin, export bundle, and parent fragment
- decide what becomes searchable versus only inspectable

Non-goal:

- full OCR, vision, or multimodal reasoning in the first pass

Current delivered foundation:

- FE now stores attachment and URL-reference records as first-class fragment-linked data
- ChatGPT export ingest can already persist local file references and cited URLs when the export metadata provides them
- FE now enriches local markdown/text/code-like attachments before upsert
- markdown frontmatter is parsed and persisted on attachment metadata
- FE now has embedded `.docx` extraction and a local OCR path that prefers Apple Vision on macOS with `tesseract` fallback
- FE now stores deterministic image metadata and preview paths as the first multimodal attachment baseline
- FE now stores FE-owned image analysis summaries/tags/signals as part of that baseline
- FE now supports provider-backed image analysis through a provider adapter layer with OpenAI and Ollama implementations
- FE now supports attachment reanalysis without requiring full fragment reingest
- FE now has the deterministic building blocks needed for image-centric inbox workflows
- Pinterest pins now have the first operator-facing fetch/index/preview/export flow through reviewer automation, corpus bundles, and the sysop Library

## Phase 2: Attachment Ingest

Goal:

- support attachment-aware chat imports without breaking deterministic behavior

Likely first capabilities:

- discover images and files from ChatGPT exports
- attach file metadata to parent chat fragments
- persist FE-owned attachment records and links
- expose attachment inspection in fragment detail

Follow-on:

- image thumbnails or previews
- stronger provider policy/fallback rules for model-backed vision enrichment, plus broader OCR coverage and richer document extraction beyond the current markdown/text/code/docx baseline
- future office formats to add when needed: `.pptx`, `.xlsx`

## Phase 3: URL Source Ingest

Goal:

- make standalone URLs a first-class FE ingest source for articles, PDFs, videos, and related media

Likely first capabilities:

- ingest a URL as a fragment plus attachment/reference record
- classify destination type from headers, MIME, and URL shape
- persist canonical source URL, fetch metadata, and derived title
- stage the result into inbox for review rather than forcing an external route

Current delivered foundation:

- FE now supports `url_source` as a manifest-driven deterministic ingest
- FE can fetch and index HTML/article and plain text URLs today
- FE can now extract text from text-based PDFs through an embedded Go library, with optional `pdftotext` and `ocrmypdf` fallback adapters
- FE now records page-level PDF snippet metadata to support later page-image and page-vision work
- FE can now extract YouTube transcripts when captions are available, with optional `yt-dlp` fallback
- FE can now try `yt-dlp` for known non-YouTube video-page URLs when subtitles are available
- FE still stages other images and videos as reference fragments with placeholder content
- FE preserves canonical URL references as fragment-linked attachment records
- manual single-URL saves now get the same deterministic extraction treatment as batch `url_source` ingest (previously a gap; resolved) — local extraction primary, async Firecrawl fallback via the inbox reviewer for bot-blocked sources, bounded retry with a documented failure reason on the fragment
- clients that already extracted a page themselves (e.g. a browser extension) can submit pre-fetched content directly, skipping FE's own fetch entirely

Observed current gap from operator usage:

- output bundle paths are now written for FFS materialization, but browse rows do not yet expose those output refs directly
- the inbox reviewer's oldest-first, fixed-batch-size, no-rotation selection means a freshly-staged item can be starved indefinitely behind an existing backlog — confirmed live, filed as `CW-20260816-0063`

Follow-on:

- page-image rendering for PDFs
- page-level PDF vision and scanned-PDF handling
- audio ingest/transcription
- video frame and timeline-aware enrichment
- remote media fetch/cache policy

## Phase 4: Inbox and Triage UX

Goal:

- make FE easier to operate at scale through agent and operator workflows

Likely additions:

- dismiss/archive actions for inbox clusters
- saved triage views by entity/source/status
- route suggestions based on FE-owned history
- queue and destination health summaries tuned for operators

Near-term usage-driven additions now suggested by the live inbox:

- continue chronological/domain-aware inbox review now that GitHub repos and Pinterest pins have first working paths
- expand domain-aware review to Reddit discussions, docs/blog links, PDFs, videos, and references
- make notes, quotes, and reports first-class creation flows instead of relying on manual source-type selection
- add bulk materialization and repair/backfill controls for FFS bundles
- scheduled inbox-review agents that propose actions rather than only waiting for manual triage

## Phase 5: FFS Library and Virtual Filesystem

Goal:

- make FE the finder and virtual filesystem for the material the user cares
  about most, while keeping the on-disk FFS layout understandable without FE

Current delivered foundation:

- chosen root: `~/Documents/ffs`
- namespace: `docs`, `media`, `artifacts`, `views`, and `inbox`
- Pinterest pins materialize under `media/pins`
- references materialize under `docs/references`
- notes, quotes, and reports materialize under `docs/notes`, `docs/quotes`, and
  `docs/reports`
- sysop Library browses processed fragments with list/card modes, sorting,
  search, filters, virtual folders, and fragment previews

Next capabilities:

- expose materialized output refs directly in `/v1/fragments/browse`
- derive Library folder paths from backend/output state instead of frontend-only
  source-type inference
- add saved Library views for common roots such as `docs/notes`,
  `docs/references`, and `media/pins`
- add bulk materialization from Library folders and filtered sets
- add FFS repair/backfill commands that can rewrite bundles without touching
  user metadata

## Phase 6: Recall Expansion

Goal:

- deepen FE’s search and relationship model without losing explainability

Likely additions:

- more deterministic entity kinds
- explicit project/workspace/model/tool taxonomies
- entity-centric browse flows
- better related-fragment reasoning that shows durable edges and retrieval-time signals together
- richer embedded Vanta indexing once new fragment types arrive

## Phase 7: External Integration Depth

Goal:

- keep FE as the canonical system while broadening export surfaces

Likely additions:

- more MCP providers
- richer API providers
- stronger `cli` contract if needed
- destination-specific delivery/reporting policy presets

## Guiding Rules

- FE stays the canonical memory for fragment provenance, routing, and recall
- portfolio apps remain peers, never hidden internal dependencies
- new ingestion features should land behind deterministic metadata first
- multimodal/AI enrichment should build on FE-owned records, not replace them
- live operator usage should drive prioritization once the deterministic substrate exists
