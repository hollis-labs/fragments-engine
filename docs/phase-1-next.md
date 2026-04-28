# Fragments Engine — Phase 1 Next

This repo now has the first deterministic core:

- SQLite store with embedded migrations
- canonical fragment repository
- pipeline-oriented ingest service
- Claude Code connector
- FE-owned inbox, routing, route-log, and external destination model
- FTS-backed search
- `file` destination execution
- thin CLI, HTTP API, and MCP wrappers over the same service layer
- end-to-end ingest/search integration coverage for the Claude v0 path

## Immediate next phase

1. Add an FE-owned recall/index interface and keep SQLite FTS as the current implementation
2. Add relationship/link tables plus summary/cache fields
3. Index fragments into FE's own recall layer, not just route them outward
4. Add destination config types and execution skeletons for `api`, `mcp`, and `cli`
5. Add a second connector for ChatGPT export once the export format is confirmed
6. Decide whether embedded Vanta should become a later internal implementation of the FE recall/index interface for hybrid recall and embeddings

## Internal Recall Shape

Keep the service contracts FE-owned and add an internal recall interface behind them:

```go
type RecallIndexer interface {
    IndexFragment(ctx context.Context, fragment domain.Fragment) error
    Search(ctx context.Context, query string, limit int) ([]domain.SearchResult, error)
    Related(ctx context.Context, fragmentID string, limit int) ([]domain.SearchResult, error)
}
```

SQLite can satisfy this first. An embedded Vanta-backed implementation can be introduced later without changing CLI/API/MCP entry points.

## Schema additions expected soon

- `classifiers`
- relationship/link tables
- summary/cache fields
- indexing metadata tables as needed by the internal recall subsystem

## Quality follow-ups

- add ADRs for service boundaries and the transport-wrapper rule
- expand `make lint` to the portfolio baseline once external tools are standardized here
- add integration tests for destination execution failure paths and future destination kinds
