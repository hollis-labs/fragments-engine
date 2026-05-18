# AGENTS.md — Fragments Engine

Orientation for agents working in this repo. Pair this with `.agent-ops/project.yaml`
for machine-readable IDs, build authority, and locations.

## (a) What is this and why

**Fragments Engine (FE)** is a local-first ingestion, triage, and recall hub for
information fragments. It is two things at once:

- the **inbox / routing system** for fragments (ingest → classify → route)
- the **knowledge / search engine** for fragment provenance and recall

The problem it solves: information overload has three failure modes — lost
fragments, duplicated effort, and agent amnesia. FE addresses all three with a
single pipeline: ingest → classify → route → index for recall. When the system is
confident it routes automatically; when it isn't, the fragment stays staged in the
inbox for a human decision. Nothing gets lost; nothing gets mis-filed silently.

FE is a **peer application** in the portfolio — not a privileged orchestrator. It
owns its own pipeline, inbox, routing state, provenance, and recall layer. Anything
outside FE is an *external destination* reached through a transport-based adapter
(`file`, `api`, `mcp`, `cli`) — app names never appear in the FE core destination
model.

Go module: `github.com/hollis-labs/fragments-engine`. This repo is the greenfield
Go build that supersedes the older "Carrier" Python identity (Carrier becomes an
ingest connector for FE).

> **Project status note.** Torque is described elsewhere as "planned to replace
> Fragments Engine." Based on repo reality this project is **active**: git commits
> through 2026-05-14, an in-progress feature branch (`cw-20260515-0072-sysop-phase1`),
> a freshly vendored GUI shell, and a live Cerberus dev resource. Treat FE as active
> until a portfolio decision says otherwise; do not assume archival.

## (b) Where to start

Entry points:

- `cmd/fragments-engine/main.go` — the single CLI binary; all transports start here.
- `README.md` — the most complete operational reference (commands, config, examples).
- `docs/architecture.md` — architecture boundary and pipeline overview.
- `docs/identity.md` — what FE is and is not; relationship to other projects.
- `docs/roadmap.md` — phased roadmap (Phase 1 Attachment Planning → Phase 6).
- `docs/next-session.md` / `docs/phase-1-next.md` — locked decisions and next work.
- `docs/usage.md` — usage walkthrough.
- `Makefile` — `build`, `test`, `lint`, `run`, `seed-config`, `serve-api`, plus
  `sysop-*` targets for the GUI.

Internal layout (`internal/`):

- `service` — canonical application services (the FE service layer)
- `ingest` — ingest pipeline contracts and orchestration
- `store` — SQLite connection and embedded migrations
- `repository` — persistence adapters
- `api` — HTTP transport
- `mcp` — MCP transport
- `domain`, `config`, `analyze`, `extract`, `recall`, `app` — supporting packages

`apps/sysop` is a vendored, trimmed copy of Torque's GUI shell; its `dist/` bundle
is embedded into the Go binary at compile time (`make build` runs `sysop-build`
first).

## (c) Key domain concepts

- **Fragment** — the canonical normalized unit. FE persists its metadata,
  provenance, inbox state, and route history in SQLite.
- **Ingest** — a configured source. Kinds: `claude_code`, `chatgpt_export`,
  `url_source`. Defined in `fragments.yaml`.
- **Inbox** — staging area for unrouted or low-confidence fragments awaiting
  manual review. Inbox review teaches the Bayesian classifiers over time.
- **Entity** — normalized extracted entities (repo, workspace, model, tool, etc.)
  used for grouping, routing matches, and search filters.
- **Route** — a rule that sends matching fragments to an external destination.
- **Destination** — an external peer, modeled by transport kind: `file`, `api`,
  `mcp`, `cli`. Destinations are persisted through FE itself (CLI/API/MCP), not
  declared in ingest YAML.
- **Queue** — FE's delivery retry queue (SQLite-backed via `go-queue`), with
  per-destination policy, replay, dead-letter, and audit events.
- **Recall** — FE-owned search/index layer. Backends: `sqlite` (BM25 FTS) or
  embedded `vanta`. An embedded recall engine must sit behind FE-owned interfaces.
- **Transport wrappers** — CLI, HTTP API, and MCP are thin wrappers over the same
  service layer; they must stay behavior-equivalent.

Classification strategy: deterministic first, Bayesian second, AI enrichment async
and non-blocking.

## (d) Common operations + examples

```bash
make test                  # go test ./...
make lint                  # go vet ./...
make build                 # builds sysop GUI bundle then the Go binary
make run                   # go run ./cmd/fragments-engine

# Core CLI flow
go run ./cmd/fragments-engine init
go run ./cmd/fragments-engine ingest run -config ./fragments.yaml
go run ./cmd/fragments-engine search -q "project roadmap"

# Inspect
go run ./cmd/fragments-engine inbox list
go run ./cmd/fragments-engine recall status
go run ./cmd/fragments-engine fragment get -id <fragment-id>

# Transports
go run ./cmd/fragments-engine serve-api    # HTTP API
go run ./cmd/fragments-engine serve-mcp    # MCP server on stdio
```

Run the dev API via Cerberus (resource `fragments-engine-dev`, port 8091):

```bash
cerberus_resource_status   # check
cerberus_resource_deploy   # build + (re)deploy
```

The dev API must launch with `--config fragments.yaml` (the gitignored runtime
config), never `fragments.example.yaml`. `make serve-api` builds, seeds
`fragments.yaml` from the template, and starts the API against it. The Cerberus
service definition (`~/.cerberus/config.yaml`, outside this repo) must point its
launch command / `--config` flag at `fragments.yaml`.

**Config files.** `fragments.example.yaml` is a hand-maintained, *commented*
template — treat it as read-only at runtime. The ingest CRUD endpoints rewrite
the live config file in place (YAML marshal strips comments, re-indents, and adds
machine defaults), so the server must run against the gitignored `fragments.yaml`
runtime copy. Seed it once with `make seed-config` (or `scripts/seed-config.sh`),
then edit `fragments.yaml` before running ingests.

## (e) Where to look for more

- `README.md` — exhaustive command, config, and destination reference.
- `docs/` — `architecture.md`, `identity.md`, `roadmap.md`, `usage.md`,
  `next-session.md`, `phase-1-next.md`, `go-classification-libs.md`.
- `.agent-ops/project.yaml` — IDs, build authority, locations, scopes.
- Cerberus config (`~/.cerberus/config.yaml`) — dev resources `fragments-engine-dev`
  and `frag-dev` (the companion macOS menu-bar app, separate `frag` repo).
- No `docs/adrs/` directory exists yet; ADRs for service boundaries and the
  transport-wrapper rule are a noted roadmap follow-up.
