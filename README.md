# Fragments Engine

Fragments Engine is a local-first ingestion, triage, and recall hub for information fragments.

FE is both:

- the inbox/routing system for fragments
- the knowledge/search engine for fragment provenance and recall

External destinations are always peers reached through explicit transports such as `file`, `api`, `mcp`, or `cli`.

## Current v0 scope

- ingest Claude Code chat history from configured directories
- normalize sessions into canonical fragments
- ingest saved URL manifests for articles, PDFs, videos, images, and text resources
- persist fragment metadata, provenance, inbox state, and route history in SQLite
- persist FE-owned attachment and URL-reference records alongside fragments
- publish routed filesystem bundles as `.../<fragment-id>/fragment.md` with sibling `attachments/` for copied local files
- index fragments for deterministic search inside FE
- route fragments to external destinations, starting with `file`
- support external destination providers for `mcp` and `api`, starting with Nil inbox and Nanite messaging
- support FE-owned recall through `sqlite` or embedded `vanta`
- expose the same service operations through CLI, HTTP API, and MCP

## Status

This repo is the greenfield build for the architecture in [`docs/`](./docs/). UI is intentionally deferred. The first milestone is an agent-first core with a stable service layer and thin transport wrappers.

## Quick Start

```bash
make test
go run ./cmd/fragments-engine init
go run ./cmd/fragments-engine ingest run -config ./fragments.yaml
go run ./cmd/fragments-engine search -q "project roadmap"
go run ./cmd/fragments-engine fragment backfill-pinterest-corpus -config ./fragments.yaml
```

## Configuration

The committed `fragments.example.yaml` is a hand-maintained, commented template.
The server rewrites its `--config` file in place whenever ingests are mutated
through the CRUD endpoints (`POST /v1/ingests/...`), so the live server must run
against a gitignored runtime copy — `fragments.yaml` — never the template itself.
Seed it once with `make seed-config` (copies `fragments.example.yaml` →
`fragments.yaml`), then edit `fragments.yaml`:

```yaml
database:
  path: ./data/fragments-engine.db

delivery:
  file:
    max_attempts: 1
    backoff_ms: 0
  mcp:
    max_attempts: 3
    backoff_ms: 500
  api:
    max_attempts: 3
    backoff_ms: 500
  cli:
    max_attempts: 3
    backoff_ms: 500

queue:
  auto_drain: true
  poll_interval_seconds: 5
  batch_size: 20
  replay_cooldown_seconds: 60
  max_replays_per_hour: 10
  alert_pending_threshold: 10
  alert_dead_letter_threshold: 3

reviewer:
  enabled: false
  poll_interval_seconds: 300
  batch_size: 10
  download_root: ./data/inbox-reviewer
  corpus_root: ~/Documents/corpus/visuals/pinterest
  github_token_env: GITHUB_TOKEN
  stack_explorer_api_base: http://localhost:8081
  stack_explorer_scan: se-repo-scan

recall:
  backend: vanta
  vanta:
    root: ./data/vanta
    embedding_provider: ollama
    embedding_model: nomic-embed-text

ingests:
  - name: claude-default
    kind: claude_code
    enabled: true
    source:
      root: ~/.claude
    routing:
      namespace: fragments/chats/claude
    rules:
      max_file_size_mb: 50
  - name: chatgpt-export
    kind: chatgpt_export
    enabled: false
    source:
      root: ~/Downloads
    routing:
      namespace: fragments/chats/chatgpt
    rules:
      max_file_size_mb: 50
      archive_root: ~/Documents/corpus/ai-chat-logs/chatgpt/logs
      copy_text_exports: true
      delete_copied_source: false
  - name: saved-urls
    kind: url_source
    enabled: false
    source:
      root: ~/Documents/corpus/url-inbox
    routing:
      namespace: fragments/web
    rules:
      request_timeout_seconds: 20
      max_body_mb: 5
      user_agent: FragmentsEngine/0.1 (+url_source)
  - name: hollis-docs
    kind: filesystem_docs
    enabled: false
    source:
      root: ~/dev/hollis-labs/apps
    routing:
      namespace: fragments/repos/docs
    rules:
      include:
        - "**/*.md"
        - "**/*.mdx"
        - "**/*.txt"
        - "**/*.go"
        - "**/*.ts"
        - "**/*.tsx"
        - "**/*.yaml"
        - "**/*.yml"
      exclude:
        - "**/.git/**"
        - "**/node_modules/**"
        - "**/dist/**"
        - "**/build/**"
      max_file_size_mb: 2
      project_from_path: true
  - name: hollis-git-changes
    kind: git_changes
    enabled: false
    source:
      root: ~/dev/hollis-labs/apps
    routing:
      namespace: fragments/repos/git
    rules:
      repos:
        - fragments-engine
        - nanite
        - tesseract
      branch: main
      since: 72h
      max_commits: 100
      include:
        - "**/*.md"
        - "**/*.go"
        - "**/*.ts"
        - "**/*.tsx"
        - "**/*.yaml"
        - "**/*.yml"
      exclude:
        - "**/node_modules/**"
        - "**/dist/**"
        - "**/build/**"
      emit_doc_file_fragments: false
```

Destinations are persisted through FE itself rather than declared in the ingest YAML. Add them through CLI, API, or MCP. Real examples:

```bash
# Nil inbox over stdio MCP
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name nil-inbox \
  -kind mcp \
  -config-json '{
    "transport": "stdio",
    "command": "/Users/chrispian/Projects-apps/nil/nil-mcp",
    "provider": "nil_inbox",
    "queue_policy": {
      "replay_cooldown_seconds": 120,
      "max_replays_per_hour": 6
    },
    "nil_inbox": {
      "title_prefix": "[FE] ",
      "item_type": "note",
      "tags": ["fragment","fe"]
    }
  }'

# Nanite user mailbox over HTTP
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name nanite-user-mailbox \
  -kind api \
  -config-json '{
    "base_url": "http://127.0.0.1:8090",
    "provider": "nanite_user_mailbox",
    "nanite_messaging": {
      "to_session_id": "32ee2abe-1c33-4261-8d66-cccca35016c1",
      "subject_prefix": "[FE] "
    }
  }'

# Nanite session message to a specific agent or user
go run ./cmd/fragments-engine route destination-add \
  -config ./fragments.yaml \
  -name nanite-session-message \
  -kind api \
  -config-json '{
    "base_url": "http://127.0.0.1:8090",
    "provider": "nanite_messaging",
    "nanite_messaging": {
      "to_session_id": "32ee2abe-1c33-4261-8d66-cccca35016c1",
      "to_agent_id": "user",
      "subject_prefix": "[FE] "
    }
  }'
```

Delivery retry defaults are transport-aware:

- `file`: `max_attempts=1`
- `mcp`: `max_attempts=3`, `backoff_ms=500`
- `api`: `max_attempts=3`, `backoff_ms=500`
- `cli`: `max_attempts=3`, `backoff_ms=500`

These can be changed globally with the top-level `delivery` config block and overridden per destination with a `retry` block in `config-json`.

The optional top-level `reviewer` block enables the first FE-owned oldest-first
inbox reviewer. The current reviewer shape:

- scans staged inbox items from oldest to newest
- enriches manual single-URL saves with deterministic metadata/entities/reference attachments
- adds GitHub repo review hints and can upsert repos into Stack Explorer when `reviewer.stack_explorer_api_base` is set
- can sync GitHub topics and FE input tags into Stack Explorer repo tags after repo upsert
- can optionally queue a Stack Explorer scan for synced GitHub repos when `reviewer.stack_explorer_scan` is set
- can authenticate GitHub repo metadata fetches with the token named by `reviewer.github_token_env`
- can fetch Pinterest pin page metadata and download the main image into `download_root`

For `chatgpt_export`, FE currently ingests text conversations from `conversations-*.json`. FE records attachment and URL references when the export metadata provides them. Local text-like attachments now get a deterministic pre-upsert enrichment pass: markdown frontmatter is parsed, extracted attachment text is appended into fragment content for recall, and extraction metadata is persisted on the attachment record. FE also has embedded `.docx` text extraction plus a first multimodal image path: image dimensions/format are captured deterministically, image OCR is optional through `tesseract`, FE stores an image analysis summary/tags/signals record, and image previews can be published in file bundles. An optional vendor-backed image analyzer now runs behind a provider adapter layer and can use either Ollama or OpenAI to persist FE-owned `vision_analysis` metadata on attachments. Fragment detail now exposes decoded attachment metadata directly. If a fragment later routes to a `file` destination, FE copies local attachments into the fragment bundle and persists the published `storage_path`; remote URLs remain references only. If `copy_text_exports` is enabled, FE mirrors the text export files into `archive_root/<export-folder>/` before parsing. FE now enforces that `archive_root` lives under `~/Documents/corpus/ai-chat-logs/chatgpt/logs`, and `delete_copied_source` is only allowed when that copy step is enabled. `delete_copied_source` still removes only copied text input files, not image or binary attachments.

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

`backend: none` keeps attachment analysis deterministic-only.
`backend: ollama` keeps the same FE interface but uses the local Ollama adapter instead.
`fallback_backend` and `min_confidence` let FE retry a second provider when the primary provider fails or returns low-confidence output.

Reanalyze stored attachment vision metadata without reingesting the fragment:

```bash
fragments-engine fragment reanalyze-attachments -config ./fragments.yaml -fragment-id <fragment-id>
```

For `url_source`, FE reads URL manifests from a directory of `.txt`, `.json`, or `.jsonl` files. The current FE-owned extraction path is:

- HTML/article pages via embedded `go-readability`
- plain text/markdown/json/xml via deterministic body capture
- PDFs via embedded `ledongthuc/pdf` text extraction first, with optional `pdftotext` and `ocrmypdf` fallbacks when available on `PATH`
- YouTube URLs via embedded watch-page caption extraction first, with optional `yt-dlp` subtitle fallback when available on `PATH`
- known non-YouTube video-page URLs via optional `yt-dlp` subtitle extraction when available on `PATH`
- other images/videos still stage as reference fragments with placeholder content until richer extraction is added

For `filesystem_docs`, FE walks a configured root deterministically and ingests
matching markdown/text/source files as standalone fragments. The current shape:

- preserves `repo_name`, `repo_root`, `relative_path`, `file_ext`, and `content_hash`
- parses markdown frontmatter into fragment metadata without injecting it into visible content
- uses include/exclude globs plus `max_file_size_mb`
- treats the source as a stable `file:///...` URI and keeps canonical paths namespace-friendly

For `git_changes`, FE collects recent commits from one repo root or a configured repo list. The current shape:

- emits one commit fragment per matching commit with commit message, author, time, changed files, and diff-stat metadata
- supports `branch`, `since`, `until`, `max_commits`, and include/exclude globs
- can also emit changed-doc file fragments with committed file content when `emit_doc_file_fragments: true`
- never mutates the scanned repos

For embedded Vanta recall, FE currently supports:

- `embedding_provider: ollama`
- `embedding_provider: llama` as an alias for `ollama`
- `embedding_provider: openai`

`recall status` reports whether embeddings are actually active or whether FE is currently running BM25 fallback.

## Commands

- `init` initializes the database.
- `ingest list|validate|preview|archive-policy-set|run` manages FE ingest definitions and execution.
  Supported ingest kinds: `claude_code`, `chatgpt_export`, `url_source`, `filesystem_docs`, `git_changes`.
  `ingest preview` is side-effect-free for `chatgpt_export`: it does not copy or delete source files.
- `inbox list` shows staged fragments awaiting manual review.
- `inbox review` runs the oldest-first reviewer once against staged inbox items.
- `inbox entities|entity-items` groups staged fragments by persisted entity so triage can happen at the cluster level.
- `route destination-add|destination-list|destination-status|destination-rename|destination-validate|destination-delete|add|rename|list|log|preview|delete` manages external destinations and route lifecycle.
  `route destination-retry-set` and `route destination-queue-policy-set` provide first-class policy updates without editing raw destination JSON.
  `route add` can also match on normalized entities with `-match-entity-kind` and `-match-entity-value`.
  `route apply-entity` manually applies a route to all staged inbox items matching a persisted entity.
- `queue status|drain|destinations|events|pending|failed|replay|purge` inspects and operates FE’s external delivery retry queue.
  `queue pending` and `queue failed` support `-destination-id`, and `queue replay` now refuses unhealthy destinations unless `-force` is set.
- `recall status` shows the active recall backend plus embedding mode.
- `fragment get|related` inspects FE fragment provenance and related recall.
  `fragment get` distinguishes durable FE relations from retrieval-time recall matches.
  It now also shows FE-owned attachment and URL-reference records for the fragment.
- `entity list|fragments` inspects persisted FE entities and the fragments linked to them.
- `search` runs FE recall against the active backend and can be constrained by persisted entity filters.
- `serve-api` starts the HTTP API. Run it against the gitignored `fragments.yaml`
  runtime config (`make serve-api` builds, seeds, and launches it); never point it
  at the `fragments.example.yaml` template.
- `serve-mcp` starts the MCP server on stdio.

Current destination implementations:

- `file` — writes fragment markdown artifacts to a corpus path
- `mcp` — typed stdio MCP execution, first provider: `nil_inbox`
- `api` — typed HTTP execution, first providers: `nanite_messaging`, `nanite_user_mailbox`
- `cli` — typed local command execution over JSON stdin/stdout

CLI destination note:

- FE sends a JSON payload on stdin containing `destination` metadata plus the full fragment, including rendered markdown.
- The command may print a single reference string on stdout; FE records that as the delivery ref.
- Example config:

```json
{
  "command": "/Users/chrispian/bin/fragment-receiver",
  "args": ["--channel", "inbox"],
  "provider": "corpus_cli"
}
```

Queue note:

- FE now uses `~/Projects-apps/framework/libs/go-queue` as the storage layer for its delivery retry queue.
- We are intentionally using the SQLite driver while keeping FE’s own delivery semantics in the service layer, rather than delegating routing behavior to the generic worker.
- The library remains a good fit because it already provides SQLite-backed queue persistence, retry/release semantics, and per-job max tries.
- `serve-api` and `serve-mcp` now run a lightweight background drainer when `queue.auto_drain=true`, so queued deliveries can recover automatically in long-running FE processes.
- Queue views can now be scoped by destination id, which makes it practical to inspect one peer integration at a time.
- Failed-job replay now does a destination-health preflight and requires an explicit override when a destination is still invalid or unreachable.
- FE now keeps queue audit events for enqueue, replay, purge, dead-letter, and queued-delivery success.
- `queue destinations` now provides a per-destination summary with pending/failed counts, replay/dead-letter history, and an alert flag for unhealthy peers.
- Queue policy is now configurable in FE itself:
  - `replay_cooldown_seconds`
  - `max_replays_per_hour`
  - `alert_pending_threshold`
  - `alert_dead_letter_threshold`
- Destination configs can now override those queue-policy defaults with a `queue_policy` block inside `config-json`.

Destination status is available through every wrapper:

- CLI: `route destination-status`
- API: `GET /v1/destinations/status`
- MCP: `get_destination_status`

Destination policy mutation is available through every wrapper:

- CLI: `route destination-retry-set`, `route destination-queue-policy-set`
- API: `POST /v1/destinations/retry`, `POST /v1/destinations/queue-policy`
- MCP: `set_destination_retry_policy`, `set_destination_queue_policy`

Destination lifecycle mutation is available through every wrapper:

- CLI: `route destination-rename`, `route destination-validate`, `route destination-delete`
- API: `POST /v1/destinations/rename`, `POST /v1/destinations/validate`, `POST /v1/destinations/delete`
- MCP: `rename_destination`, `validate_destination`, `delete_destination`

Route lifecycle mutation is available through every wrapper:

- CLI: `route rename`, `route preview`, `route delete`
- API: `POST /v1/routes/rename`, `POST /v1/routes/preview`, `POST /v1/routes/delete`
- MCP: `rename_route`, `preview_route`, `delete_route`

Ingest lifecycle mutation is available through every wrapper:

- CLI: `ingest list`, `ingest validate`, `ingest preview`, `ingest archive-policy-set`
- API: `GET /v1/ingests`, `POST /v1/ingests/validate`, `POST /v1/ingests/preview`, `POST /v1/ingests/archive-policy`
- MCP: `list_ingests`, `validate_ingest`, `preview_ingest`, `set_ingest_archive_policy`

The status view includes:

- config validity
- live reachability
- effective retry policy
- effective queue policy
- last delivery attempt, success, and failure
- FE-owned delivery metrics per destination, including total attempts, success count, and failure count

## Architecture

- `internal/service`: canonical application services
- `internal/ingest`: ingest pipeline contracts and orchestration
- `internal/store`: SQLite connection and migrations
- `internal/repository`: persistence adapters
- `internal/api`: HTTP transport
- `internal/mcp`: MCP transport

See [`docs/architecture.md`](./docs/architecture.md), [`docs/identity.md`](./docs/identity.md), and [`docs/next-session.md`](./docs/next-session.md).
See also [`docs/usage.md`](./docs/usage.md) and [`docs/roadmap.md`](./docs/roadmap.md).
On macOS, FE now prefers the Apple Vision OCR path for local image OCR when available, with `tesseract` as a fallback.
