# Fragments Engine — Identity

## Name

**Fragments Engine** (`github.com/hollis-labs/fragments-engine`)

The Go module name is already established. "Carrier" is too generic for a public release and will be renamed. The vision locked in this folder supersedes Carrier's current identity.

## Tagline

*Tame information overload. Every fragment finds its place.*

## Core User Story

> As someone drowning in information across chat history, notes, articles, code, and conversations — I want to drop anything into a single inbox, have it classified and enriched automatically, and have it routed to the right place — so I can find anything later with perfect recall, and agents can do the same.

The operative words: **inbox → classify → route → recall.**

When the system is confident, it routes automatically. When it isn't, the item stays in the inbox for a human decision. Nothing gets lost; nothing gets mis-filed silently.

## What It Is (and Is Not)

**Is:**

- An information ingestion + triage + routing hub
- A local-first, privacy-respecting system (no uploads, no cloud required)
- The canonical knowledge/search engine for fragments in its own domain
- An embeddable recall substrate for agents
- A platform: connectors in, external destinations out, user rules + AI in the middle

**Is not:**

- A content generation pipeline (that's Carrier's current scope; Carrier becomes an ingest connector)
- A notes app
- An agent framework
- A privileged orchestrator over other portfolio apps

## Relationship to Existing Projects

| Project | Relationship |
|---|---|
| **Carrier** (current Python) | Becomes an ingest connector — git, Claude logs, ChatGPT exports feed into FE |
| **fragments-engine** (Go, existing) | The Go module namespace; the long-term runtime will live here |
| **Vanta Conduit** | Candidate embedded implementation for FE's internal recall/search subsystem |
| **Nil** | Peer app that FE would reach through `mcp` or `api` if integrated |
| **Nanite** | Peer app; not a special destination type in FE |

## External Destination Rule

Anything outside FE is external, even if it is another Hollis Labs application.

Destination kinds should therefore be transport-based:

- `file`
- `api`
- `mcp`
- `cli`

App names do not belong in the FE core destination model. They are integration configs layered on top of those transport kinds.

## Problem Space

Information overload has three failure modes:

1. **Lost fragments** — saved somewhere you never find again
2. **Duplicated effort** — re-researching what you already worked through in a chat
3. **Agent amnesia** — starting a session from scratch when the context exists but isn't surfaced

Fragments Engine addresses all three with a single pipeline: ingest → classify → route → index for recall.

Routing does not replace FE's own memory of the fragment. FE still keeps provenance, route history, relationships, and recall metadata after export.
