# Usage Guide

This is the operator and agent usage guide for Fragments Engine.

## Core Mental Model

- FE ingests source material into fragments.
- FE keeps the canonical metadata, provenance, routing history, recall index, queue state, and entities.
- FE can export fragments to external peer systems through `file`, `mcp`, `api`, and `cli`.

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

## Agent Notes

- Prefer `ingest validate` before enabling a new chat-history source
- Prefer `ingest preview` before `ingest run` for large ChatGPT exports
- Prefer `fragment get` after ingesting new ChatGPT exports to verify attachment capture, `storage_path`, and URL references
- Prefer `fragment get` after `url_source` ingest to verify extracted content type, canonical URL, and reference attachment shape
- Prefer `fragment get` after `filesystem_docs` ingest to verify repo/path/frontmatter metadata and canonical paths
- Prefer `fragment get` after `git_changes` ingest to verify commit/file metadata, especially include/exclude filtering
- Prefer `route preview` before broad inbox bulk actions
- Use `fragment get` and `route destination-status` as the default debugging pair
