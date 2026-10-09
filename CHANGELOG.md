# Changelog

All notable changes to Fragments Engine are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Pre-1.0: minor bumps for additive surface, patch bumps for fixes — breaking changes can land in any minor.

This file was backfilled from the git history as a good-faith summary, not an exhaustive one; `git log` is the complete record. Consumers should watch it for new ingest sources, destination kinds, HTTP routes, MCP tools and configuration changes.

## [Unreleased]

### Added

- **Browser capture v1 and a media-aware Reader.** A published capture/Reader contract (`contracts/browser-capture-reader/v1/`: OpenAPI, types, capabilities and validator), media custody, and Sysop Reader improvements for readability, media and inline controls.
- **Pre-fetched intake.** Intake accepts `source_url`, `description` and `selection`; such content is never re-fetched or re-enriched by the inbox reviewer.
- **`callback` destination kind**, dispatched through the existing delivery queue, alongside `file`, `mcp`, `api` and `cli`.
- **`nil` ingest source** reading notes from Nil vault SQLite databases directly.
- **Link content.** A `linkcontent` provider interface with a local backend and a Firecrawl fallback, server-side hashtag extraction and `#link`/URL trigger detection, and a retry cap for pending links.
- **`go-directives` wired into the ingest path.**
- MIT license file.

### Changed

- Adopt published `libs/ui-go`, `libs/plugin-mcp`, `libs/util` and `substrate/llm-core` modules and Tesseract v0.11.0, replacing moved standalone imports without local module replacements.
- Adapt the existing ingest scheduler to durable fire snapshots and fenced claim recovery. A source-schema migration adds scheduler fire and accepted dispatch records; acceptance atomically writes the fire identity, ingest run and SQLite queue envelope, preventing duplicate dispatch after recovery. Existing schedule rows and configuration remain in place.
- Align embedded projection writes to the app-owned project scope while recalling legacy user-scope records read-only. Use Tesseract’s supported generic `note` kind with a `fragment` tag, and deduplicate projection hits against current FE records; no user actor is asserted or projection data migrated.
- Add CI for existing backend, race, vet, frontend build, tests and typecheck commands.

- The MCP surface moved from `mark3labs/mcp-go` to the official `go-mcp` SDK.
- Relative config and destination paths are anchored to the install directory, not the process working directory.
- README rewritten as a pre-release identity and stack-fit document.

## [0.1.1] - 2026-05-15

### Fixed

- Patch release over v0.1.0; see `git log v0.1.0..v0.1.1`.

## [0.1.0] - 2026-05-15

### Added

- Initial tagged release: local-first ingest → classify → route → recall engine with CLI, HTTP API, MCP and the Sysop UI (ingest source CRUD, async runs, cron schedules).
- Search modes, background jobs and workers, and config endpoints.
- Git-docs ingestion, Pinterest corpus browsing, and FFS file-destination routing with inbox-preserving materialization.

[Unreleased]: https://github.com/hollis-labs/fragments-engine/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/hollis-labs/fragments-engine/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/hollis-labs/fragments-engine/releases/tag/v0.1.0
