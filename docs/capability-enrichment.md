# Capability-aware enrichment

Fragments Engine plans enrichment per immutable fragment revision and per
capability. The canonical capabilities are `title`, `description`, `body`,
`gallery_manifest`, `original_media`, `thumbnail_or_poster`, `transcript`,
`OCR`, `vision`, `summary`, `tags`, and `entities`. Each coverage row reports
`provided`, `missing`, `pending`, `failed`, `stale`, or `not_applicable`.

Coverage work state and display selection are deliberately independent. The
selected observation pointer remains available while a refresh is pending,
stale, or failed. Provider results append attributed observations and then run
the deterministic resolver; they never mutate a fragment revision or delete a
browser, source, or user observation. Resolution precedence is user, source,
provider, deterministic, then model. Within one attribution class, confidence,
asserted time, and observation ID provide deterministic tie-breaks.

## Capture initialization

Manifest acceptance initializes all twelve rows in the same transaction as the
capture attempt, immutable revision, media references, and follow-up outbox.
Only the following typed evidence produces `provided`; the extraction adapter's
bare `observed_capabilities` declaration remains provenance but is not evidence.

| Capability | Typed evidence | Otherwise |
|---|---|---|
| title | non-empty captured document title (never FE fallback title) | missing |
| description | non-empty captured description | missing |
| body | non-empty `markdown` or `plain_text` body | missing |
| gallery_manifest | more than one ordered stable media asset reference | missing; YouTube is not applicable |
| original_media | at least one logical `original` variant reference | missing |
| thumbnail_or_poster | at least one preview, thumbnail, or poster reference | missing |
| transcript | a transcript/subtitle variant reference | missing for timed/provider media; otherwise not applicable |
| OCR | no v1 capture field supplies OCR text | missing for visual media; otherwise not applicable |
| vision | no v1 capture field supplies vision output | missing for visual media; otherwise not applicable |
| summary | no v1 capture field supplies a derived summary | missing |
| tags | non-empty user tag set | missing |
| entities | no v1 capture field supplies typed entities | missing |

`provided` media coverage means that the source supplied a logical reference.
It does not imply that bytes are available: reference-only, pending, available,
and failed acquisition remain independent variant states. `not_applicable` is
conservative. In particular, one observed Instagram item leaves gallery
coverage missing because provider completion may reveal a carousel, and a
YouTube video without captions leaves transcript coverage missing.

## Planning, attempts, and retries

The capture follow-up outbox is consumed transactionally into capability jobs.
Exact outbox replay creates no duplicate jobs. A periodic planner can also plan
missing, stale, retryable-failed, or explicitly requested coverage without
replaying capture. Permanent failures are not automatically planned again;
an explicit idempotent re-request advances a generation and makes the
capability eligible.

Workers claim jobs through a serialized, leased, fenced operation. Each attempt
snapshots the full adapter descriptor, versioned input/output schemas, declared
provider/source/media/capability support, network/effect class, credential
reference names, immutable material digest, and verified asset digests. An
expired claim token cannot publish. Reclaim records the expired attempt as a
retryable failure and issues a new fencing token.

An explicit request that arrives while the same capability is already queued or
running advances the durable request generation without starting concurrent
work. Completion of the older job queues exactly one successor for the newest
generation in the same transaction; the selected display observation remains
available throughout that handoff.

Provider descriptors contain credential references only. Runtime credential
bytes are supplied through an injected callback boundary and are never returned
by the service or serialized into descriptors, observations, jobs, attempts, or
logs.

## Provider and oEmbed safety

The provider package defines separate `CanonicalIdentityResolver`,
`MetadataProvider`, `MediaAcquirer`, `TranscriptProvider`, and
`PlaybackProvider` interfaces over one revision-pinned input model. Provider
observations use closed capability-specific JSON shapes and non-HTML formats.
Unknown fields, ambiguous reference lists, duplicate/blank references, trailing
JSON values, script/iframe markup, event handlers, and JavaScript URLs are
rejected before provider results reach persistence. Valid contract Markdown,
including autolinks, remains accepted; server-side sanitization or escaped
rendering remains mandatory when normalized text is projected as HTML.

oEmbed is decoded into a closed metadata structure. Its `html` field and all
unknown fields are dropped. Playback is produced separately from a closed,
validated spec (currently an allowlisted YouTube provider ID or an FE variant
reference), never from provider markup.

## Legacy compatibility

Migration 015 seeds only immutable legacy revision title, description, and body
as source observations. It intentionally ignores mutable fragment summaries,
metadata-level `enrichment_status`, and attachment analysis flags. Existing
manual enrichment and inbox-review behavior continues to use those legacy flags
unchanged; browser capture and new provider work never depends on them. The
legacy intake/ingest adapter task is responsible for moving those paths onto
this revision-aware model without rewriting historical observations.
