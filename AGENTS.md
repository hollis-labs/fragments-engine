# Fragments Engine

Fragments Engine ingests information fragments from configured sources,
classifies and routes them, and owns the recall index over their content and
provenance. What it cannot route confidently stages in an inbox for human review
rather than being filed silently. It is a peer application, not an orchestrator
or a coordinator for other apps: external systems are reached only through
transport-kind destination adapters, and app names never enter the core
destination model.

## Start Here

- `docs/architecture.md` — the peer-application boundary and the
  ingest → classify → route → recall pipeline.
- `cmd/fragments-engine/main.go` — the single binary; every transport starts here.
- `internal/service/` — the canonical service layer. The CLI, the HTTP API
  (`internal/api/`) and MCP (`internal/mcp/`) are thin wrappers over it and must
  stay behavior-equivalent.
- `internal/ingest/delivery.go:88` — the five destination transport kinds
  (`file`, `mcp`, `api`, `cli`, `callback`), and where an unsupported one is
  refused.
- `internal/store/store.go` — the SQLite connection and the embedded migrations.
- `contracts/browser-capture-reader/v1/` — the published capture and Reader
  contract: OpenAPI, types, capabilities and the validator that enforces them.
- `apps/sysop/` — the React UI, built and embedded into the Go binary.
- `README.md` — the operational command, config and destination reference.

## Commands

```bash
make test      # go test ./...
make lint      # go vet ./...
go build ./cmd/fragments-engine
```

`make build` runs `sysop-build` first, which npm-installs and regenerates
`apps/sysop/dist`. Build the binary directly when the UI has not changed.

## Boundaries

`fragments.example.yaml` is a hand-commented template and is read-only at
runtime. The ingest CRUD endpoints rewrite the live config file in place through
Go's YAML marshaller, which strips comments, reorders keys and adds machine
defaults, so the server must run against the gitignored `fragments.yaml`. Seed it
once with `make seed-config`; `scripts/seed-config.sh` carries the full reasoning.

`apps/sysop/dist` is gitignored but embedded with `//go:embed all:dist`
(`apps/sysop/embed.go`), so a fresh clone cannot `go build` until `make
sysop-build` has produced the bundle once. Git will not restore that bundle.

Destination kinds are transport-oriented. Reaching a new peer application means
configuring one of the existing kinds, never adding a kind named after the app.

Content that arrives pre-fetched — an intake carrying `source_url`, as the web
clipper sends — is never re-fetched or re-enriched by the inbox reviewer.
`TestInboxReviewer_NeverTouchesPrefetchedSourceURLFragments` guards this.
