# Fragments Engine — Next Session Handoff

## Current State

This session shipped four features end to end, each live-tested against real data (not just unit tests), plus documentation to match:

1. **Link ingest** — adding a link (bare URL, or `#link` alongside a URL embedded in other text) deterministically pulls a title/summary. Local extraction is primary; an optional Firecrawl fallback is retried asynchronously by the inbox reviewer, never synchronously on the intake/batch-ingest hot path. New `internal/linkcontent` package mirrors the existing `internal/analyze.VisionAnalyzer` primary/fallback pattern.
2. **Loom pilot's `callback` destination** — a fifth FE destination kind, fire-and-forget, dispatched only through FE's existing async delivery queue (reused, not reinvented) for waking Nanite's Curator durable agent. `go-directives` (`::command`) parsing wired into ingest, tagging fragments with a `directive` entity; one route matches any directive-tagged fragment to the callback destination.
3. **Nil vault ingest** (`nil_vault` kind) — reads `note`/`scratch` items directly out of Nil's per-vault SQLite databases (no running Nil process required), converting Nil's TipTap/ProseMirror note bodies to plain text via a small local converter. Live-tested against 3 real vaults: 190 notes ingested correctly, 0 todos leaked, missing vault directory skipped gracefully, dedup confirmed idempotent on a second run.
4. **Web clipper** (`apps/fe-clipper`, a separate sibling repo) — a Chrome MV3 extension using Mozilla Readability + Turndown to capture full-page content client-side (sidesteps bot-blocking entirely, since capture happens in a real authenticated browser tab) and POSTs it to a new pre-fetched-content mode on `/v1/intake` (`source_url`/`description`/`selection` fields — FE never re-fetches when `source_url` is present). User-confirmed working in the browser.

The live dev service is managed by Cerberus as `fragments-engine-dev` on `http://127.0.0.1:8091/sysop/`.

## Latest Local Commits

Current local `main` (already pushed to `origin/main`) includes, most recent first:

- `8e06288` `FE: intake support for pre-fetched content (source_url/description/selection, skip re-fetch) (#18)`
- `c76a569` `FE: add nil ingest source (notes/scratch from Nil vaults via direct SQLite read) (#17)`
- `653010e` `FE: route tagging fragments into the nanite wiki bundle (CW-20260816-0018)`
- `0e93f77` `FE: dispatch callback destinations through the existing delivery queue (CW-20260816-0034)`
- `c80c67d` `FE: wire go-directives into ingest path (CW-20260816-0012)`
- `b8892ca` `FE: add callback destination type to routing schema (CW-20260816-0011)`
- `36ef041` `Capture review_error on link-fallback retry failures`
- `fd7f0c2` `docs: add real single-link and batch-from-file examples for link ingest`
- `4b85de9` `Document env-var loading for API keys; identify launchd env gap (CW-20260816-0057)`
- 9 more commits for the initial link-ingest epic (`CW-20260816-0025` through `0033`)

Plus this session's doc-currency pass (README.md, docs/usage.md, docs/next-session.md, docs/roadmap.md) — check `git log` for the exact commit if picking up right after handoff.

`apps/fe-clipper` is a separate sibling repo with its own commit history — not reflected in fragments-engine's `git log`.

## Live Config State

- `fragments.yaml` (gitignored runtime config) now has `link_content` (backend: local, fallback_backend: firecrawl) and a `nil-vaults` ingest entry (`kind: nil_vault`, `enabled: true`) live and enabled.
- `FIRECRAWL_API_KEY` and `OPENAI_API_KEY` are both set in the `fragments-engine-dev` Cerberus resource's launchd `env:` block — confirmed live via `cerberus_resource_inspect`, not just assumed.
- `reviewer.batch_size` is back to `10` (its normal value) — it was temporarily bumped to `100` during live debugging this session and reverted afterward; don't be surprised if you see that in shell history, it's not a leftover live setting.

## Known Gaps / Deferred Work (filed in Torque, not yet picked up)

- **`CW-20260816-0063`** (priority 1) — the inbox reviewer's oldest-first, no-rotation, fixed-batch-size selection means a freshly-staged item can be starved indefinitely behind an existing inbox backlog (confirmed live: a fragment at rank 73/73 by age was mathematically unreachable by the automatic 5-minute cycle). Needs a targeted, index-backed `enrichment_status='pending'` lookup, decoupled from the general oldest-first sweep — explicit design constraint from this session: do NOT fix this by scanning the whole inbox faster/bigger, inbox is a valid long-term resting state for most items and the fix must cost nothing proportional to inbox size for settled items.
- **`CW-20260816-0064`** (priority 3, capture-only, not yet architected) — every service restart hits `PRAGMA journal_mode=WAL: database is locked` as multiple background workers (queue drainer, ingest worker, scheduler, inbox reviewer) race to open the DB simultaneously at startup, silently eating that worker's first pass. Needs a real design pass on read/write racing in general plus deterministic startup ordering — deliberately deferred, not scoped yet.
- **`CW-20260816-0086`** (priority 2, issue) — route fan-out: currently one fragment can only match one route. The Loom pilot's directive-tagged-fragment route may eventually need to also route to Torque or elsewhere from the same fragment; not needed for the pilot's current scope, filed for later.

## Recommended Pickup

No single obvious next thread — this session closed out several previously-open threads rather than opening one big new one. In priority order:

1. `CW-20260816-0063` (reviewer starvation) is the highest-value pickup — it's the reason a real link added via the Frag app sat unenriched for the length of this session until manually forced. Anyone relying on `#link`/inbox-reviewer retry in normal use will hit this on any inbox with a nontrivial backlog.
2. Continue the pre-existing Library/FFS product surface work if that's still the active thread (see git history before this session's work for that context — `21cf4c2` and earlier).
3. `CW-20260816-0064` (startup DB lock race) whenever there's appetite for an ops/reliability pass — low urgency, real annoyance.

## Operational Notes

- Run the dev API against gitignored `fragments.yaml`, not `fragments.example.yaml`.
- `fragments.example.yaml` is a commented template and should not be used as the mutable runtime config — it was kept in sync with this session's new config blocks (`link_content`, `nil-vaults`, `callback` delivery defaults) but is not itself live anywhere.
- The runtime DB owns destinations and routes; they are not declared in YAML.
- Cerberus resource `fragments-engine-dev` runs from the workspace directory directly (`./fragments-engine` binary built into the repo root, not a separate install path) — `cerberus_resource_deploy` rebuilds but does NOT always restart if it thinks the launchd config itself is unchanged; use `cerberus_resource_reload` explicitly after any code change to be sure the running process picked up a new binary (confirm via PID/run-count change in `cerberus_resource_inspect`, don't just trust the deploy/reload call's own success message).
