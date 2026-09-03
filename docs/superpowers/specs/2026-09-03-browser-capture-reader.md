# Browser Capture and Reader Execution Specification

**Status:** Accepted

**Date:** 2026-09-03

**Project:** Fragments Engine (`PRJ-20260514-0004`)

**Torque plan:** `CW-20260903-0029`

**Canonical architecture:**
[`browser-capture-reader-architecture.md`](../../browser-capture-reader-architecture.md)

## 1. Outcome

Fragments Engine accepts versioned, media-aware browser captures into one stable
fragment identity; preserves immutable source revisions and additive user
context; acquires and serves media through explicit custody contracts; completes
missing provider capabilities without erasing browser observations; and exposes a
URL-addressable Reader for articles, images, galleries, and YouTube videos.

The Reader is both an inbox-oriented processing surface and a persistent library
view. Reading state, triage, routing, acquisition, and FFS materialization remain
independent.

## 2. Current-state problem

The current `/v1/intake` path treats prefetched page content as a generic article,
can suppress provider-specific completion, and relies on exact content-hash
deduplication. Repeated content with new metadata can be skipped or destructively
replaced, changed content can become an unrelated fragment, and URLs with
identical extracted text can collide.

Current Reader-like Sysop surfaces expose only one preview attachment and do not
have a batched media-aware read model, ordered galleries, trusted video playback,
generic consumption progress, or a durable capture/asset lifecycle.

## 3. Locked domain model

Stable identity is:

```text
source registration + source item key + segment key -> Fragment
Fragment + normalized material digest               -> FragmentRevision
deliberate clip action                               -> CaptureAttempt
```

The following records remain semantically distinct:

- stable `Fragment` and immutable `FragmentRevision`
- append-only `CaptureAttempt` and `CaptureAnnotation`
- one optimistic, editable `CuratedNote`
- attributed tags and descriptive observations
- logical `MediaAsset`, physical/logical `AssetVariant`, and ordered
  revision-scoped `AttachmentRef`
- capability-scoped derived observations
- principal-scoped `ReadingState`
- purpose-built `ReaderItem` projection
- revision-pinned `ReaderContext` and external `ConversationRef`

All FE ingests use the stable identity/revision invariant. Browser capture is the
first concrete consumer, not a special exception.

## 4. Contract and service boundaries

FE owns canonical OpenAPI 3.1 and JSON Schema 2020-12 documents for capture,
media, Reader, playback, progress, commands, errors, and capability discovery.
The clipper consumes pinned generated TypeScript types and runtime validators.

Capture is manifest-first:

1. Validate and accept the capture envelope.
2. Atomically resolve stable identity/revision, record capture provenance and
   additive context, persist ordered media references, and enqueue follow-up
   work.
3. Return stable IDs, an optimistic `ReaderItem`, and asset instructions.
4. Accept per-variant bytes independently and idempotently.
5. Represent final client contribution as complete or partial while enrichment
   continues independently.

FE owns provider completion through narrow identity, metadata, media,
transcript, and playback adapters. Browser observations remain attributed source
facts; FE fills only missing, failed, stale, or explicitly re-requested
capabilities.

## 5. Media and provider behavior

FE supports image, video, audio, document, timed-text, and other media with
original, preview, thumbnail, poster, audio, subtitle, and transcript variants.
Each variant has independent acquisition and custody state.

Mirrored bytes use an FE content-addressed blob store, retain source provenance,
and remain indefinitely by default until an explicit retention policy exists.
Galleries are one fragment with ordered attachment references.

Pinterest and Instagram adapters complete missing identity, metadata, and media
observations without depending on a universal official-API path. YouTube
completion supports metadata, posters, captions/transcript, and a validated
provider-ID playback specification. Video bytes remain reference-only by default;
a generic explicit asset-acquisition command preserves the future download seam.

oEmbed may be attributed metadata. FE never executes its raw HTML.

## 6. Reader behavior

The service exposes batched `ReaderItem` list projections for `inbox`, `library`,
and `all`, plus canonical `/reader/:fragmentId` navigation and optional historical
revision selection. The projection includes resolved display data, bounded
summary, ordered media, playback, annotations/notes, capture count, reading
state, independent operational summaries, and advertised semantic actions.

Initial renderers are:

- article: bounded card excerpt and full sanitized HTML rendered from normalized
  searchable Markdown
- image: preview/thumbnail with larger original view
- gallery: ordered thumbnail and larger navigable views
- video: inline trusted player, fullscreen, poster, metadata/body, and transcript
  availability
- safe text/unknown fallbacks, with audio/document extension seams

Quick actions use semantic commands and optimistic revisions rather than
arbitrary metadata patches. Reading progress supports article anchors/progress,
video/audio time, gallery item/index, and document page.

## 7. Scope fences

This execution does not decide inbox disposition or folder/collection semantics.
That decision is tracked separately as `CW-20260903-0060`.

It also does not:

- automatically download YouTube video bytes
- make oEmbed a universal extraction/playback protocol
- create a general crawler or bypass provider access controls
- make FE authoritative for current publisher content
- turn FFS into a hidden second canonical database
- store or own agent messages/session execution
- harden this personal local tool for arbitrary multi-tenant exposure

## 8. Exit gate

The Fragments Engine track is complete only when all of the following are true:

1. FE publishes validated OpenAPI/JSON Schema contracts and compatible capability
   discovery, and the clipper can reproducibly consume the pinned artifacts.
2. Every ingest resolves one stable fragment per source-item/segment identity,
   reuses exact revisions, creates immutable changed revisions, and preserves
   existing data through a deterministic compatibility path.
3. Repeated captures record attempts, union tags, append highlights/capture notes,
   protect optimistic curated-note edits, and never mix user state into source
   revision identity.
4. Media assets, variants, ordered attachment references, custody modes,
   content-addressed blobs, indefinite default mirroring, and partial per-variant
   acquisition are persisted and tested.
5. Manifest-first capture is immediately visible, idempotent, recoverable, and
   useful when optional transfers or enrichments fail.
6. Pinterest, Instagram, YouTube, and generic content use capability-aware
   adapters that preserve browser observations and never execute raw provider
   markup.
7. Reader APIs return batched complete card projections without attachment/status
   N+1 behavior and expose independent read, triage, route, materialization,
   enrichment, and acquisition state.
8. `/reader/:fragmentId` and Reader scopes render articles, images, ordered
   galleries, and inline/fullscreen YouTube video with safe partial-state
   fallbacks.
9. Semantic quick actions and renderer-specific reading progress work
   optimistically without making `read`, `routed`, `materialized`, or `processed`
   synonymous.
10. The architecture's Pinterest, Instagram, YouTube, article, idempotency,
    concurrency, and revision-pinned chat-context validation scenarios pass with
    automated and real-browser evidence.
11. Go, Sysop, schema, API, compatibility, and integration verification passes;
    operational and contract documentation is current; and the final review
    confirms every scope fence above.

## 9. Execution authority

This specification and the canonical architecture own scope and semantics. The
companion plan owns the execution sequence. Torque plan `CW-20260903-0029` owns
live task status, dependencies, checklists, and evidence.

If repository reality invalidates a task's implementation detail, the
orchestrator may correct the task and plan while preserving this specification.
A change to a locked invariant, public contract boundary, custody default,
cross-project ownership, or exit criterion requires owner review.
