# Usage Guide

This is the operator and agent usage guide for Fragments Engine.

## Core Mental Model

- FE ingests source material into fragments.
- FE keeps the canonical metadata, provenance, routing history, recall index, queue state, and entities.
- FE can export fragments to external peer systems through `file`, `mcp`, `api`, and `cli`.
- Browser capture and the Sysop Reader have a dedicated operator guide at
  [`browser-capture-reader.md`](./browser-capture-reader.md).

## Common Flows

### 1. Inspect configured ingests

```bash
go run ./cmd/fragments-engine ingest list -config ./fragments.yaml
go run ./cmd/fragments-engine ingest validate -config ./fragments.yaml -name chatgpt-export
go run ./cmd/fragments-engine ingest preview -config ./fragments.yaml -name chatgpt-export -limit 10
```

Notes:

- `ingest preview` is side-effect-free for `chatgpt_export`
- preview does not copy source files
- preview does not delete source files

### 2. Set ChatGPT archive policy

```bash
go run ./cmd/fragments-engine ingest archive-policy-set \
  -config ./fragments.yaml \
  -name chatgpt-export \
  -archive-root ~/Documents/corpus/ai-chat-logs/chatgpt/logs \
  -copy-text-exports=true \
  -delete-copied-source=false
```

Rules:

- FE only allows ChatGPT archive roots under `~/Documents/corpus/ai-chat-logs/chatgpt/logs`
- `delete_copied_source` requires `copy_text_exports=true`
- current delete behavior applies only to copied text-export files, not attachments

### 2a. Configure the inbox reviewer

The optional FE-owned reviewer can process staged inbox items from oldest to newest:

```yaml
reviewer:
  enabled: true
  poll_interval_seconds: 300
  batch_size: 10
  download_root: ./data/inbox-reviewer
  corpus_root: ~/Documents/ffs/media/pins
  github_token_env: GITHUB_TOKEN
  stack_explorer_api_base: http://localhost:8081
  stack_explorer_scan: se-repo-scan
```

Current behavior:

- enriches manual single-URL saves with deterministic metadata/entities
- can upsert reviewed GitHub repos into Stack Explorer when `stack_explorer_api_base` is set
- can sync reviewed GitHub topics and FE input tags into Stack Explorer repo tags
- can optionally enqueue a Stack Explorer scan for those repos when `stack_explorer_scan` is set
- can send authenticated GitHub repo metadata requests when `github_token_env` points at a token-bearing environment variable
- can fetch Pinterest pin metadata and download the main image locally for attachment analysis
- can export reviewed Pinterest pins into FE-owned markdown bundles under `reviewer.corpus_root`, including copied local image assets and previews

Run it once manually:

```bash
go run ./cmd/fragments-engine inbox review -config ./fragments.yaml -limit 10
```

Backfill corpus bundles for already-reviewed Pinterest pins:

```bash
go run ./cmd/fragments-engine fragment backfill-pinterest-corpus -config ./fragments.yaml
```

### File destination bundle contract

- `file` destinations publish a fragment bundle, not a lone markdown file
- bundle layout is:

```text
<root>/<canonical_path>/fragment.md
<root>/<canonical_path>/attachments/<files...>
```

- FE copies only local attachments into `attachments/`
- FE persists the published attachment `storage_path`
- image attachments can publish previews under `attachments/previews/` in file bundles
- local markdown, text, and code-like attachments can be indexed into fragment content before persistence
- markdown attachments get YAML frontmatter parsed into attachment metadata
- `.docx` attachments can be extracted through the embedded zip/XML path
- image attachments now persist deterministic visual metadata like width, height, and format
- image attachments can use OCR when `tesseract` is available on `PATH`
- image attachments now also persist FE-owned `analysis` metadata with summary, tags, and signals
- image attachments can optionally persist vendor-backed `vision_analysis` metadata through provider adapters, currently `ollama` and `openai`
- fragment detail over CLI/API/MCP now also exposes structured attachment fields like `analysis_summary`, `analysis_tags`, `vision_summary`, `vision_tags`, `vision_entities`, and `vision_confidence`
- `fragment reanalyze-attachments` lets FE rerun provider-backed image analysis on stored attachments without reingesting the whole fragment

Optional attachment vision config:

```yaml
analysis:
  attachments:
    backend: openai
    fallback_backend: none
    min_confidence: 0
    openai:
      api_key_env: OPENAI_API_KEY
      model: gpt-5.4-mini
      detail: low
      timeout_seconds: 30
      prompt: ""
```

`fallback_backend` lets FE retry another provider if the primary provider fails.
`min_confidence` lets FE fall back when the primary provider returns a lower confidence than you want to accept.

On macOS, FE now prefers Apple Vision OCR for local image attachments and only falls back to `tesseract` if needed.
- remote URLs remain reference records and are not fetched or copied

### 3. Run ingest and search

```bash
go run ./cmd/fragments-engine init -config ./fragments.yaml
go run ./cmd/fragments-engine ingest run -config ./fragments.yaml
go run ./cmd/fragments-engine search -config ./fragments.yaml -q "roadmap"
go run ./cmd/fragments-engine search -config ./fragments.yaml -entity-kind repo -entity-value sample-project
```

### 3a. URL source manifests

`url_source` reads URL manifests from a directory tree:

- `.txt`
  One URL per line. Lines starting with `#` are ignored.
- `.json`
  JSON array of URL strings or objects like `{"url":"https://...","title":"...","created_at":"2026-04-27T12:00:00Z"}`
- `.jsonl`
  One JSON string or object per line

Current deterministic behavior:

- FE fetches `http/https` URLs directly
- HTML and plain text responses are indexed as fragment content
- text PDFs are extracted into fragment content with embedded-first and optional local-tool fallback adapters
- YouTube URLs are ingested as `source_type=youtube` with caption transcript text when available
- known non-YouTube video-page URLs can be ingested as `source_type=video` through optional `yt-dlp` subtitle extraction
- other images and videos are staged with deterministic placeholder content plus a URL reference record
- results default to inbox unless a route explicitly matches `source=url`

### 3b. Filesystem docs ingest

`filesystem_docs` walks a local root and indexes matching docs/source files without modifying them:

- `include` / `exclude` accept glob patterns like `**/*.md` and `**/node_modules/**`
- when `include` is empty, FE falls back to a deterministic default set of markdown/text/source extensions
- markdown frontmatter is parsed into metadata and removed from the visible fragment body
- `source=file:///abs/path`, `source_type=filesystem_docs`, and metadata carries repo/path/content-hash provenance

Example preview flow:

```bash
go run ./cmd/fragments-engine ingest validate -config ./fragments.yaml -name hollis-docs
go run ./cmd/fragments-engine ingest preview -config ./fragments.yaml -name hollis-docs -limit 10
```

### 3c. Git change ingest

`git_changes` collects recent commits from one repo root or a configured repo list:

- `repos` is optional; if omitted, FE treats `source.root` itself as the repo
- `since` and `until` accept RFC3339, `YYYY-MM-DD`, or duration-style values like `72h`
- include/exclude globs filter the changed file set before FE emits a commit fragment
- `emit_doc_file_fragments=true` also emits committed file-content fragments for changed doc/source files

Example preview flow:

```bash
go run ./cmd/fragments-engine ingest validate -config ./fragments.yaml -name hollis-git-changes
go run ./cmd/fragments-engine ingest preview -config ./fragments.yaml -name hollis-git-changes -limit 10
```

### 3d. Link ingest (single link and batch-from-file)

Adding a link deterministically pulls a title and summary from the source. Primary extraction is local (readability-style article parsing plus `og:title`/`og:description`/meta-description scraping); an optional Firecrawl fallback (`link_content.fallback_backend: firecrawl` in `fragments.yaml`, see the `link_content` block near the top of `fragments.example.yaml`) is retried asynchronously by the inbox reviewer when the primary fetch is blocked or errors — it is never called synchronously on the ingest hot path. Summary precedence: `og:description`/meta description when present, else the first ~200 characters of extracted article text, trimmed at a word boundary.

**Single link, via manual intake:**

Manual intake is HTTP-only today (no CLI equivalent yet) — start the API server first:

```bash
go run ./cmd/fragments-engine serve-api -config ./fragments.yaml -addr :8091
```

Then POST content containing a URL. Either a bare URL alone, or a `#link` hashtag anywhere alongside a URL embedded in other text, triggers link enrichment — the hashtag is recorded as a tag and left in place in the stored content, never stripped:

```bash
curl -s -X POST http://127.0.0.1:8091/v1/intake \
  -H 'Content-Type: application/json' \
  -d '{"content": "https://example.com/some-article"}'

curl -s -X POST http://127.0.0.1:8091/v1/intake \
  -H 'Content-Type: application/json' \
  -d '{"content": "worth reading later #link https://example.com/some-article"}'
```

Response:

```json
{"result": {"fragment_id": "<id>", "outcome": "inserted", "status": "inbox"}}
```

Verify the enriched title/summary landed:

```bash
go run ./cmd/fragments-engine fragment get -config ./fragments.yaml -fragment-id <id>
```

If the source blocks the fetch (bot detection, 403/429/503, or a near-empty extraction), intake still returns immediately with a placeholder summary and `metadata.enrichment_status = "pending"`. The inbox reviewer (see [§2a](#2a-configure-the-inbox-reviewer)) retries it on its normal poll cycle with the full local-then-Firecrawl chain, up to a bounded number of attempts (`metadata.enrichment_attempts`) before giving up and marking `metadata.enrichment_status = "failed"`.

**Batch, from a file of links:**

Reuses the `url_source` ingest kind ([§3a](#3a-url-source-manifests)) — no separate batch-intake mechanism exists. Point an ingest entry's `source.root` at a directory and drop a manifest file in it:

```bash
mkdir -p ~/Documents/corpus/url-inbox
cat > ~/Documents/corpus/url-inbox/reading-list.txt <<'EOF'
https://example.com/some-article
https://example.com/another-post
EOF
```

Enable the matching entry in `fragments.yaml` (`fragments.example.yaml` ships this as `name: saved-urls`, `enabled: false` — flip it to `true` in your live config), then run:

```bash
go run ./cmd/fragments-engine ingest preview -config ./fragments.yaml -name saved-urls -limit 10
go run ./cmd/fragments-engine ingest run -config ./fragments.yaml
```

`ingest run` executes every enabled ingest, not just this one — use `ingest preview` first to check just this source. Note `preview` still performs a real fetch of every URL in the manifest (it shares the same `Collect` path as a real run) — it just doesn't insert anything into FE's database. A blocked/failed URL within the batch does not abort the run; it lands as a placeholder fragment with `enrichment_status = "pending"` and gets picked up by the same inbox-reviewer retry cycle as the single-link path above, alongside every other blocked URL in the batch.

**Pre-fetched content (legacy compatibility clients):**

For compatibility callers that already fetched/extracted a page client-side, `/v1/intake` also accepts `source_url`, `description`, `selection`, `highlights`, and `notes` alongside `content`:

```bash
curl -s -X POST http://127.0.0.1:8091/v1/intake \
  -H 'Content-Type: application/json' \
  -d '{
    "content": "<full page content the caller already extracted, markdown or text>",
    "title": "<page title>",
    "source_url": "https://example.com/some-article",
    "description": "<page'"'"'s own meta description, if the caller captured one>",
    "selection": "<text the user had highlighted at capture time, if any>",
    "highlights": ["<additional independent highlight>", "<another highlight>"],
    "notes": ["<capture-time note>"],
    "tags": ["link"]
  }'
```

When `source_url` is present, FE never fetches the URL itself — the submitted `content` is treated as final. `description` takes the same precedence over a derived summary that the fetch-based path already uses. `selection` is retained as a single-value compatibility alias and becomes an additive highlight without replacing `highlights`; highlights and notes never enter immutable source material. Exact semantic retries are read-only, new tags/notes/highlights merge additively, and changed content at the same stable source URL creates a new revision. Inline hashtags remain deterministic source-derived evidence, while only values in `tags` are attributed to the user.

This endpoint is a legacy adapter, not an alternate browser-capture protocol. New browser integrations should use `POST /v1/captures` plus its capture-scoped upload, lookup, and completion routes. Prefetched intake initializes all twelve capability rows but marks only supplied typed source/user evidence as `provided`; legacy `enrichment_status`, derived summaries, provider display metadata, and attachment analysis flags do not manufacture coverage for capabilities that remain missing.

### 3e. Nil vault ingest

`nil_vault` reads `note` and `scratch` items (never `todo`) directly out of [Nil](https://github.com/hollis-labs/nil)'s per-vault SQLite databases — no Nil-side changes or running Nil process required, since Nil's HTTP API/MCP only work while its desktop app is open, but FE reads the vault files directly.

`source.root` points at Nil's own config directory (`~/.config/nil` by default), which FE reads to discover configured vault paths — not at a vault directly:

```yaml
  - name: nil-vaults
    kind: nil_vault
    enabled: true
    source:
      root: ~/.config/nil
    routing:
      namespace: fragments/nil
    rules:
      include_vaults: []   # vault names/ids to include; empty = all configured vaults
      exclude_vaults: []
```

Current deterministic behavior:

- discovers every vault from Nil's `config.json`; a vault whose directory no longer exists on disk is skipped with a log line, not a fatal error
- only `note`/`scratch` kinds are ingested; `todo` items are never included
- Nil stores note bodies as TipTap/ProseMirror JSON, not markdown (confirmed against Nil's actual `INSERT`/`UPDATE` SQL) — FE has its own small PM-JSON → plain-text converter (`internal/ingest/nilvault/pmjson.go`), so no live Nil process or export step is needed
- wikilink targets embedded in a note's body are captured into fragment metadata (`nil_linked_item_ids`) but not yet wired into FE's own fragment-relation graph
- taxonomy (Nil projects/contexts/tags) and vault/section/priority/pin state all carry into fragment metadata
- a fragment with genuinely empty note content (e.g. a title-only note with no body) is correctly skipped, same convention every other FE ingest source already follows
- full rescan every run, relying on FE's existing content-hash dedup for idempotency — no incremental/since-last-run state to manage

Example flow:

```bash
go run ./cmd/fragments-engine ingest validate -config ./fragments.yaml -name nil-vaults
go run ./cmd/fragments-engine ingest preview -config ./fragments.yaml -name nil-vaults -limit 10
go run ./cmd/fragments-engine ingest run -config ./fragments.yaml
```

### 4. Inspect fragment provenance

```bash
go run ./cmd/fragments-engine fragment get -config ./fragments.yaml -fragment-id <fragment-id>
go run ./cmd/fragments-engine fragment related -config ./fragments.yaml -fragment-id <fragment-id>
go run ./cmd/fragments-engine route log -config ./fragments.yaml -fragment-id <fragment-id>
```

`fragment get` distinguishes:

- persisted FE relations
- retrieval-time related results from the active recall backend
- FE-owned attachment and URL-reference records
- decoded attachment metadata, including frontmatter and extractor details
- image metadata such as format, dimensions, OCR status, and preview path when available
- image analysis summary/tags for FE’s current deterministic multimodal baseline

For manual saved URLs reviewed by the inbox reviewer, `fragment get` is now the
main way to verify:

- normalized URL/domain/platform metadata
- GitHub repo identity hints and suggested downstream destination
- downloaded Pinterest image attachments, local paths, and image analysis metadata

### 5. Add destinations

File:

```bash
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name corpus \
  -kind file \
  -config-json '{"root":"~/Documents/corpus/fragments"}'
```

FFS media pins:

```bash
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name ffs-pins \
  -kind file \
  -config-json '{
    "root":"~/Documents/ffs/media/pins",
    "provider":"ffs"
  }'
```

Notes:

- `file` destinations now support `provider` and optional `path_template`
- `provider: "ffs"` gives Pinterest pin routes the same bundle layout FE already uses in `~/Documents/ffs/media/pins`
- `path_template` can place generic file outputs under a stable namespace like `docs/references/{platform}/{source_id}`
- `route materialize` writes through a destination without changing fragment status or removing the inbox item
- sysop manual fragments can also use `Save to FFS`, which updates the current fragment and materializes it to the default destination for `source_type`
- supported `path_template` tokens currently include `{id}`, `{title}`, `{ref_name}`, `{source}`, `{source_type}`, `{source_id}`, `{canonical_path}`, `{domain}`, `{platform}`, `{pin_id}`, `{repo_owner}`, and `{repo_name}`

Nil via MCP:

```bash
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name nil-inbox \
  -kind mcp \
  -config-json '{"transport":"stdio","command":"/path/to/nil-mcp","provider":"nil_inbox"}'
```

Nanite via API:

```bash
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name nanite-user-mailbox \
  -kind api \
  -config-json '{"base_url":"http://127.0.0.1:8090","provider":"nanite_user_mailbox","nanite_messaging":{"to_session_id":"<session-id>"}}'
```

Local CLI:

```bash
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name cli-export \
  -kind cli \
  -config-json '{"command":"/path/to/receiver","args":["--channel","inbox"],"provider":"corpus_cli"}'
```

CLI destination contract:

- FE sends JSON on stdin
- payload includes `destination` and full `fragment` fields
- `fragment.markdown` contains the rendered export artifact
- stdout may return a delivery reference string

### 6. Add and preview routes

```bash
go run ./cmd/fragments-engine route add \
  -config ./fragments.yaml \
  -name claude-to-corpus \
  -match-source claude \
  -match-type chat \
  -destination-id <destination-id> \
  -auto-route=true

go run ./cmd/fragments-engine route preview \
  -config ./fragments.yaml \
  -route-id <route-id>
```

### 7. Work the inbox

```bash
go run ./cmd/fragments-engine inbox list -config ./fragments.yaml
go run ./cmd/fragments-engine inbox entities -config ./fragments.yaml
go run ./cmd/fragments-engine inbox entity-items -config ./fragments.yaml -kind repo -value sample-project
go run ./cmd/fragments-engine route apply-entity -config ./fragments.yaml -route-id <route-id> -kind repo -value sample-project
```

### 8. Operate the delivery queue

```bash
go run ./cmd/fragments-engine queue status -config ./fragments.yaml
go run ./cmd/fragments-engine queue pending -config ./fragments.yaml
go run ./cmd/fragments-engine queue failed -config ./fragments.yaml
go run ./cmd/fragments-engine queue replay -config ./fragments.yaml -id <job-id>
go run ./cmd/fragments-engine queue destinations -config ./fragments.yaml
```

### 9. Destination lifecycle and policy edits

```bash
go run ./cmd/fragments-engine route destination-status -config ./fragments.yaml
go run ./cmd/fragments-engine route destination-validate -config ./fragments.yaml -kind cli -config-json '{"command":"/path/to/receiver"}'
go run ./cmd/fragments-engine route destination-retry-set -config ./fragments.yaml -destination-id <id> -max-attempts 5 -backoff-ms 1000
go run ./cmd/fragments-engine route destination-queue-policy-set -config ./fragments.yaml -destination-id <id> -replay-cooldown-seconds 120
```

### 10. Provision the pilot route (nanite wiki callback)

The Loom pilot's one production route (loom-architecture.md §6, §10): any
fragment carrying a `directive` entity (tagged by `DirectiveStage` from an
inline `::command`, CW-20260816-0012) is routed to a `callback` destination
that wakes Curator to generate a `nanite` wiki page. FE does not decide what
kind of wiki content it becomes -- the match is deliberately broad
(`-match-entity-kind directive` with no `-match-entity-value`), matching any
directive-tagged fragment; Curator does the actual classification downstream.

This is not yet provisioned in any migration, seed, or config -- there is no
live Curator wake endpoint to point at. Once Curator's real endpoint is
known, provision the destination and route with:

```bash
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name nanite-wiki-callback \
  -kind callback \
  -config-json '{"target":"<curator-wake-url>","generator":"wiki_page"}'

go run ./cmd/fragments-engine route add \
  -config ./fragments.yaml \
  -name nanite-wiki-route \
  -match-entity-kind directive \
  -destination-id <destination-id-from-previous-command> \
  -auto-route=true
```

Replace `<curator-wake-url>` with Curator's actual Nanite durable-agent wake
endpoint, and `<destination-id-from-previous-command>` with the ID printed by
the `destination-add` command above. Callback destinations never fire
synchronously (see `CallbackDestinationConfig` in
`internal/domain/fragment.go`) -- matched fragments are always dispatched via
the async delivery queue (Section 8), never inline during routing.

## Agent Notes

- Prefer `ingest validate` before enabling a new chat-history source
- Prefer `ingest preview` before `ingest run` for large ChatGPT exports
- Prefer `fragment get` after ingesting new ChatGPT exports to verify attachment capture, `storage_path`, and URL references
- Prefer `fragment get` after `url_source` ingest to verify extracted content type, canonical URL, and reference attachment shape
- Prefer `fragment get` after `filesystem_docs` ingest to verify repo/path/frontmatter metadata and canonical paths
- Prefer `fragment get` after `git_changes` ingest to verify commit/file metadata, especially include/exclude filtering
- Prefer `fragment get` after single or batch link ingest to check whether `enrichment_status` landed on `done` versus `pending`/`failed` before assuming the title/summary is final
- Prefer `fragment get` after `nil_vault` ingest to verify vault/taxonomy metadata and that note content reads as clean prose, not raw PM-JSON
- Prefer `route preview` before broad inbox bulk actions
- Use `fragment get` and `route destination-status` as the default debugging pair
