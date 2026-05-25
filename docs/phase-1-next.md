# Fragments Engine — Current Next

The original Phase 1 recall and routing foundations have landed. FE now has a
working local-first core, attachment-aware enrichment, reviewer automation,
transport destinations, queue controls, FFS materialization, and a sysop Library
browser.

## Immediate Product Direction

Treat FE as the virtual filesystem and finder for high-value saved fragments.

The active next phase is to make the FFS/Library workflow feel more like a
coherent filesystem while keeping FE as the source of truth for provenance,
metadata, search, and routing.

## Current Delivered Foundation

- canonical fragment persistence, inbox state, routing, route log, and recall
- manual intake and editable manual fragments
- URL, Claude, ChatGPT, filesystem docs, and git changes ingest
- first-class attachments with extraction, previews, OCR, and optional vision
- Pinterest review with image download and preview generation
- GitHub repo review and Stack Explorer sync/scan integration
- transport destinations: `file`, `api`, `mcp`, `cli`
- delivery queue controls and destination health/status surfaces
- FFS file destinations and inbox-preserving materialization
- direct sysop `Save to FFS` for `note`, `quote`, `report`, `pin`, and
  `reference`
- sysop Library page with folder-like browsing over processed fragments

## Next Build Tasks

1. Move Library virtual-path derivation closer to backend data by exposing
   materialized output refs in browse rows.
2. Make Library folders first-class enough for saved views and direct links.
3. Add bulk materialization from Library filters and folders.
4. Add repair/backfill commands for FFS output bundles.
5. Add first-class creation flows for notes, quotes, and reports so users do not
   have to manually select source types every time.

## Quality Follow-ups

- add tests around `/v1/fragments/browse` folder path metadata once that moves
  backend-side
- split the sysop bundle if the Library surface keeps growing
- document the FFS namespace as an explicit product contract
- add ADRs for the FFS writer/router boundary and the sysop Library model

