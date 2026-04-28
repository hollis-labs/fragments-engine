# Roadmap

This is the high-level Fragments Engine roadmap after the current deterministic core.

## Current State

FE already has:

- Claude Code ingest
- ChatGPT text-export ingest
- canonical fragment persistence and provenance
- FE-owned recall with SQLite and embedded Vanta options
- entities and deterministic durable relations
- inbox, routes, external destinations, and queue operations
- thin CLI, API, and MCP wrappers over the same service layer
- multimodal attachment enrichment with deterministic and provider-backed image analysis
- explicit attachment reanalysis flows through CLI, API, and MCP

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

## Phase 5: Recall Expansion

Goal:

- deepen FE’s search and relationship model without losing explainability

Likely additions:

- more deterministic entity kinds
- explicit project/workspace/model/tool taxonomies
- entity-centric browse flows
- better related-fragment reasoning that shows durable edges and retrieval-time signals together
- richer embedded Vanta indexing once new fragment types arrive

## Phase 6: External Integration Depth

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
