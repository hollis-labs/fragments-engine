# Fragments Engine — Next Session Handoff

## Where We Are

Identity and architecture are now clearer than the original handoff:

- FE is a peer application
- FE is itself the inbox and knowledge/search engine
- external destinations are transport-based and peer-oriented
- the current repo already has a working deterministic core

## Session Update 2026-05-24

This session shifted FE from "manual inbox staging only" to the first real
"review my saved items for me" loop.

Shipped in code:

- manual URL intake now deterministically enriches single-URL saves at ingest time
- GitHub repo URLs are normalized into `source_type=repo` with repo/platform entities and a `suggested_destination=stack_explorer` hint
- Pinterest pin URLs are normalized into `source_type=pin` with pin/platform entities and a `suggested_action=fetch_pin_image` hint
- FE now has a first `InboxReviewerService` that walks staged inbox items oldest-first
- the reviewer can run once from CLI via `fragments-engine inbox review`
- the API runtime now starts a background reviewer loop when `reviewer.enabled=true`
- reviewer config now supports `enabled`, `poll_interval_seconds`, `batch_size`, and `download_root`
- reviewer GitHub metadata fetches can now send an optional bearer token from `reviewer.github_token_env`
- reviewer config now also supports `stack_explorer_api_base` for GitHub repo publishing
- reviewer config now also supports `stack_explorer_scan` for automatic Stack Explorer scan queueing
- Pinterest review now fetches page metadata, downloads the main pin image locally, extracts image metadata, and writes preview JPEG paths
- Sysop fragment detail now renders image attachment previews inline through an FE-owned `/v1/fragments/attachment` media endpoint instead of relying on raw filesystem paths
- GitHub repo review can now upsert repo records into a live Stack Explorer API
- GitHub repo review can now sync GitHub/input tags into Stack Explorer repo tags
- GitHub repo review can now also enqueue Stack Explorer scans

Validated live against `data/fragments-engine.db`:

- full test suite passed with `go test ./...`
- the live inbox reviewer was run oldest-to-newest against real saved items
- GitHub saves were successfully specialized into repo fragments with reviewer metadata and Stack Explorer hints
- after enabling `reviewer.stack_explorer_api_base=http://localhost:8081`, 13 reviewed GitHub repo fragments were re-reviewed and synced into the live Stack Explorer catalog with FE-side sync metadata (`stack_explorer_repo_id`, `stack_explorer_sync_status`, `stack_explorer_synced_at`)
- after enabling `reviewer.stack_explorer_scan=se-repo-scan`, those same 13 GitHub repo fragments were re-reviewed again and now each have a unique pending Stack Explorer scan id (`106` through `118`)
- all 6 live Pinterest saves were re-reviewed successfully after a parser fix and now have:
  - richer titles and descriptions
  - `pin_image_url` metadata
  - a local downloaded image under `data/inbox-reviewer/pinterest/<pin-id>/`
  - a generated preview JPEG under `data/inbox-reviewer/pinterest/<pin-id>/previews/`
- the GUI now has the backend path it needs to render those local preview images safely

Important bug fixed during live validation:

- Pinterest pages place `content=` before `name/property=` in their meta tags
- the first parser assumed a stricter attribute order and missed `og:image`
- FE now parses meta tags independent of attribute order
- already-reviewed Pinterest pins are eligible for re-review when `pin_image_url` is still missing, so failed early passes can self-heal
- Stack Explorer scan IDs were initially stamped incorrectly because FE assumed the Stack Explorer scan list endpoint was honoring a `repo_id` filter
- FE no longer probes scan list state before creating a scan and now stamps `stack_explorer_scan_repo_id` so stale mismatched scan metadata self-heals on the next reviewer pass

Immediate next product gap after this:

- GitHub repo saves now sync into Stack Explorer, sync repo tags, and queue scans, but FE still does not trigger downstream audits automatically
- Pinterest/image assets are now downloadable, indexed, and previewable in fragment detail, but inbox/table-level visual browsing and search affordances are still missing

## Decisions Locked

- **Name**: Fragments Engine. Module: `github.com/hollis-labs/fragments-engine`.
- **Runtime**: Go.
- **Recall layer boundary**: FE is its own knowledge/search engine. Any embedded recall engine such as Vanta must sit behind FE-owned interfaces and remain an internal implementation detail.
- **Classification strategy**: deterministic first, Bayesian second, AI enrichment async and non-blocking.
- **Carrier's role**: becomes an ingest connector for FE.
- **Inbox model**: unrouted or low-confidence fragments stay staged; inbox review teaches the Bayesian classifiers over time.
- **Destination model**: external destinations only. Destination kinds are transport-based: `file`, `api`, `mcp`, `cli`.
- **Portfolio app boundary**: Nil, Nanite, and any other app are FE peers reached only through explicit integration surfaces.

## Current Implemented State

- deterministic Claude Code ingest works
- FE stores fragment metadata, provenance, inbox state, routes, destinations, and route logs
- deterministic full-text search works
- inbox staging works
- deterministic auto-route works
- `file` destinations execute
- route logs are inspectable through CLI/API/MCP
- fragment detail distinguishes durable FE relations from retrieval-time recall results
- fragment detail now exposes extracted deterministic entities
- deterministic durable relations now include entity-driven repo/workspace/model/tool edges plus source/type and term overlap
- FE now persists normalized entities and supports entity-to-fragment lookup
- FE search can now be constrained by persisted entity filters
- FE routes can now match on normalized entity kind/value
- FE inbox can now be reviewed by entity groups
- FE supports bulk manual routing of staged inbox items by persisted entity, with per-fragment audit in `route_log`
- FE now supports external `mcp` destinations via a Nil inbox adapter
- FE now supports external `api` destinations via a Nanite messaging adapter
- FE now supports a Nanite user-mailbox API alias for session-scoped delivery to `agent_id=user`
- FE now applies transport-aware delivery retry/backoff for external destinations
- FE transport retry defaults are now global-configurable and still override-capable per destination
- FE now exposes FE-owned destination status and delivery metrics across CLI, API, and MCP
- FE destination status now exposes the resolved retry and queue policy in effect for that peer
- FE now supports `chatgpt_export` ingest from `conversations-*.json`
- ChatGPT chat history should live long-term under `~/Documents/corpus/ai-chat-logs/<vendor>/logs`
- `chatgpt_export` can optionally mirror text export inputs into the corpus path before parsing
- `delete_copied_source` currently deletes only copied text input files, not attachments or binary assets
- FE now uses `~/Projects-apps/framework/libs/go-queue` as the persistence layer for external delivery retries
- queued delivery failures remain visible in `route_log`, and `queue status|drain` now expose the first operational controls
- FE now supports pending and failed queue inspection plus replay/purge controls
- FE queue inspection can now be scoped by destination id
- FE failed-job replay now does a destination-health preflight and needs an explicit force override for unhealthy targets
- FE now persists queue audit history for enqueue, replay, purge, dead-letter, and queued-delivery success
- FE now exposes destination-level queue summaries with alert state and dead-letter/replay counts
- FE queue policy is now config-driven for replay cooldowns, replay-rate limits, and alert thresholds
- FE destination configs can now override queue policy per peer integration
- FE now exposes first-class destination policy mutation through CLI/API/MCP
- FE destination lifecycle now includes first-class rename, dry-run validation, and guarded delete operations
- FE route lifecycle now includes first-class rename, staged-inbox preview, and guarded delete operations
- FE ingest lifecycle now includes first-class list, validation, side-effect-free preview, and archive-policy mutation surfaces
- FE now supports external `cli` destinations with JSON stdin delivery
- FE now enforces ChatGPT archive roots under `~/Documents/corpus/ai-chat-logs/chatgpt/logs`
- FE now persists fragment-linked attachment and URL-reference records
- ChatGPT export ingest now captures attachment and cited-URL metadata when present
- FE now enriches local markdown/text/code-like attachments before fragment upsert
- markdown frontmatter is now parsed and persisted on attachment metadata
- FE now has embedded `.docx` extraction and an optional `tesseract` OCR adapter for images
- FE now stores deterministic image metadata and preview paths as the first multimodal attachment baseline
- FE now supports optional provider-backed image `vision_analysis` metadata behind the attachment enrichment path, with `ollama` and `openai` adapters
- FE now has an explicit attachment reanalysis command path so provider-backed image analysis can be rerun without full reingest
- FE now supports attachment-analysis provider fallback policy (`fallback_backend`, `min_confidence`)
- FE now prefers Apple Vision OCR locally on macOS and uses `tesseract` as fallback
- FE now stores FE-owned image analysis summaries/tags/signals as part of that baseline
- FE now supports attachment reanalysis through CLI, API, and MCP without full fragment reingest
- FE now supports OpenAI-backed image vision analysis with working live local validation
- FE now exposes structured attachment analysis fields directly in CLI/API/MCP (`analysis_*`, `vision_*`)
- FE now supports `url_source` ingest from `.txt`, `.json`, and `.jsonl` URL manifests
- `url_source` now extracts embedded article text for HTML and embedded text for text-based PDFs
- PDF extraction now has optional external `pdftotext` and `ocrmypdf` fallback adapter paths
- PDF extraction now records `page_count`, `text_page_count`, and `page_snippets`
- `url_source` now extracts YouTube transcripts via embedded caption-track discovery with optional `yt-dlp` fallback
- `url_source` now tries `yt-dlp` for known non-YouTube video-page URLs before falling back to reference-only staging
- `url_source` still stages other images and videos as reference fragments
- `serve-api` and `serve-mcp` now auto-drain queued retries when `queue.auto_drain=true`
- FE now supports deterministic `filesystem_docs` ingest for markdown/text/source files with repo/path/frontmatter provenance
- FE now supports deterministic `git_changes` ingest for commit history and optional changed-doc fragments

## Live Inbox Review Snapshot

The live FE inbox currently has 54 staged fragments in `data/fragments-engine.db`.
The current mix is:

- 30 `url`
- 18 `text`
- 6 `article`

Observed operator patterns from oldest to newest:

- early items are bug notes, workflow/tooling ideas, and routing/product prompts
- a large middle slice is GitHub repos, docs, blog posts, Reddit threads, and standards/spec links saved for functionality research or implementation inspiration
- the newest visible cluster is Pinterest pins saved as pure URLs with no fetched media or structured metadata yet

Current domain clusters visible in the inbox:

- `github.com`: 16
- `pinterest.com`: 6
- `reddit.com`: 5

Important product truth from this review:

- manual intake already supports explicit tags, but the current saved inbox items are mostly landing as flat manual fragments with little structured metadata
- Pinterest saves currently have empty metadata and only the raw pin URL as content
- GitHub saves are useful research inputs but there is no built-in repo-specific automation yet

## Recommended Near-Term Priority

Drive FE development from real inbox usage rather than abstract future phases.

The next high-value slice is:

1. Improve manual-intake enrichment for saved URLs and notes
2. Add chronological inbox-review behavior so older richer examples can guide newer sparse saves
3. Add domain-aware actions for GitHub repos and Pinterest pins
4. Add a scheduled reviewer that proposes or executes those actions safely

## Suggested Inbox Behaviors To Build Next

### GitHub repos

- detect repo URLs at intake/review time
- extract owner/repo and fetch minimal deterministic metadata
- write normalized repo/project entities
- route or publish a repo packet to Stack Explorer
- preserve provenance inside FE as the canonical record

### Pinterest pins

- detect Pinterest pin URLs
- fetch the canonical pin page and main image
- store the image as an FE-owned attachment in a local corpus/cache path
- generate previews and searchable image metadata in FE/Sysop
- allow later tagging/grouping around inspiration themes, UI patterns, aesthetics, and related projects

### Chronological review

- process older inbox items first
- let reviewed/richer items influence later sparse items from the same pattern cluster
- keep the first pass deterministic where possible, with agentic suggestions layered on top

## Durable-Agent Fit

The referenced durable-agent prompt is not in this repo; the nearby copy is:

- `../agridd/docs/durable-agents/portfolio-agent-implementer-boot-prompt.md`

That prompt explicitly treats FE as optional provenance infrastructure for a
portfolio knowledge/content-agent MVP. For the current inbox-review use case,
the likely best shape is:

- FE remains the canonical inbox/provenance/search layer
- a scheduled reviewer agent runs over staged FE inbox items
- the agent proposes or executes domain-specific actions like "send repo to Stack Explorer" or "fetch pin image and preview"

This could run inside Hadron/Nanite durable-agent infrastructure if that is now
operationally convenient, but FE does not need a heavyweight external
orchestrator just to start. A small FE-owned scheduled reviewer or CLI-driven
worker is enough for the first slice.

## Current Multimodal Checkpoint

- deterministic attachment extraction is working for markdown/text/code/json/xml and `.docx`
- local attachment metadata is persisted and visible through fragment detail
- local image attachments now have:
  - deterministic image metadata
  - deterministic FE-owned image summaries/tags/signals
  - optional OCR
  - optional provider-backed vision summaries/tags/entities/confidence
  - preview generation in file bundles
- OpenAI is currently the strongest provider-backed default for local image analysis
- Ollama remains wired as a secondary local adapter, but current local `gemma3` behavior is not a reliable structured-vision default here
- Apple Vision OCR is the preferred local OCR path on this macOS machine
- `tesseract` remains available as fallback, but current local behavior is not healthy enough to be the primary OCR path
- PDF ingest now exposes page-level text snippets, which is the foundation for later page-image and page-vision work

## Recommended Next Session Options

1. **PDF multimodal depth**
   Add page-image rendering and page-level vision analysis for PDFs so FE can reason about figures, diagrams, and scanned pages instead of only extracted text.

2. **Audio ingest**
   Add a first real audio source path for voice notes, recordings, or extracted audio tracks, with transcription plus attachment-linked timing metadata.

3. **Video understanding**
   Move beyond transcript-only ingest by adding frame sampling, thumbnail analysis, and better timeline/chapter metadata.

4. **Remote media fetch policy**
   Add a first-class FE fetch/cache policy for remote images, PDFs, and media references so enrichment can run on referenced assets without ad hoc workflows.

5. **Attachment-native recall**
   Start treating attachments as first-class searchable/analyzable units instead of only fragment-appended content, while still preserving parent-fragment provenance.

## Remaining Near-Term Decisions

1. **PDF page rendering strategy** — decide whether FE should use a Go-native renderer, local OSS tool, or macOS-native path for page-image generation
2. **Audio provider strategy** — decide which local and hosted transcription providers FE should support first
3. **Remote fetch policy** — decide when FE copies/caches remote referenced assets versus keeping them as pure references
4. **CLI destination contract** — decide whether the current JSON-stdin contract needs templating, args interpolation, or richer stdout result semantics
5. **Deterministic relation expansion** — decide which additional durable edge types and entity kinds FE should own before deeper multimodal enrichment enters the loop

## Recommended Next Build Phase

### Goal: deepen multimodal coverage on top of the stable core

The FE-owned recall/entity/queue layer is now established. The next build step should be:

1. Add PDF page-image generation and page-level PDF vision
2. Add the first real audio ingest/transcription path
3. Add remote media fetch/cache policy for referenced assets
4. Expand FE-owned multimodal entities and durable relationship types
5. Only after that, widen format coverage further (`.pptx`, `.xlsx`, richer media)

The key architectural rule is still that FE keeps the canonical memory of fragment provenance and recall even after exporting artifacts elsewhere.
