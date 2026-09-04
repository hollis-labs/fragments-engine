# Browser Capture and Reader Architecture

**Status:** Implemented v1 baseline; deferred boundaries remain explicit

**Date:** 2026-09-03

**Implementation reviewed:** 2026-09-04

**Scope:** `fe-clipper` browser capture, Fragments Engine intake and media
custody, and the Fragments Engine Reader experience. This is an architecture
record, not an implementation plan.

## Purpose

This document defines the desired contract between the `fe-clipper` Chrome
extension and Fragments Engine (FE), and the read model that FE exposes for a
media-aware Reader.

The immediate product intent is to make Pinterest images, Instagram images and
videos, YouTube videos, and ordinary web articles useful as soon as they are
clipped. The same design must also accommodate article images, PDFs, audio, and
additional media providers without adding provider-specific concepts to FE's
core content model.

This design specializes the broader direction in
[`fragment-operations-direction.md`](./fragment-operations-direction.md). Its
stable fragment, immutable revision, attributed observation, triage case, and
attachment custody concepts are the foundation for this document.

The v1 baseline described here is implemented. The frozen contracts and current
code remain implementation truth where this record discusses deferred work.
Inbox disposition/organization and external agent-session integration remain
separate follow-ups. See [`browser-capture-reader.md`](./browser-capture-reader.md)
for the current operator-facing protocol, runtime, and diagnostic details.

## Agreed outcomes

The target architecture is governed by these decisions:

1. A source item maps to one stable FE fragment. Repeated captures are recorded
   as separate capture attempts, not duplicate fragments.
2. Changed source content creates an immutable fragment revision. Content hashes
   identify revisions and exact duplicates; they do not define stable fragment
   identity.
3. Tags are merged as a set. Capture-time highlights and capture notes are
   additive. A separate curated note is editable using optimistic concurrency.
4. A gallery is one fragment with an ordered set of media attachments.
5. Browser-side acquisition is preferred for user-directed captures. FE can
   complete missing fields or media through provider adapters when necessary.
6. A capture becomes visible immediately after its manifest is accepted. Media
   transfers and enrichment continue asynchronously and may partially fail.
7. Mirrored media remains in FE's blob store indefinitely by default. Eviction
   requires an explicit future retention policy.
8. YouTube playback initially uses a trusted provider embed. Downloading video
   bytes requires an explicit, infrequent acquisition command and a configured
   provider worker; it is never automatic.
9. Reader supports both an inbox-oriented queue view and persistent library
   reading. Reading state is independent of triage, routing, and materialization.
10. Reader locations are URL-addressable, including a canonical
    `/reader/:fragmentId` item route.
11. Reading progress is generic and renderer-specific: article position, video
    time, gallery index, document page, and future audio time all use one
    discriminated contract.
12. FE owns the canonical OpenAPI 3.1 and JSON Schema contracts. The clipper
    consumes pinned generated TypeScript types and runtime validators and checks
    the FE instance's advertised contract compatibility.
13. A future Reader chat receives a revision-pinned context from FE. The system
    hosting the agent session owns messages and session lifecycle; FE stores only
    conversation references and content provenance.

## Architectural principles

### Stable identity is not exact-repeat deduplication

The identity relation is:

```text
source registration + source item key + segment key -> stable Fragment
stable Fragment + normalized material digest         -> FragmentRevision
deliberate clip action                                -> CaptureAttempt
```

For provider content, the preferred source item key is the provider's stable
item ID. For a generic web page, it is a versioned canonical URL derived by the
source adapter. The segment key is normally `root`, but allows a future source
adapter to represent stable sub-items without changing the identity model.

Every deliberate click of the clipper creates a `CaptureAttempt`, even when it
observes an existing revision. Retrying the same transport request uses the
same capture ID and is idempotent; deliberately clipping again uses a new
capture ID and increments the observed capture count.

An exact repeat can therefore add a new attempt, new tags, or new annotations
without creating either a duplicate fragment or a content revision. A changed
normalized body, source title or description, or ordered media manifest creates
a new revision under the same fragment. Earlier descriptions and content remain
available through the immutable observation and revision history.

This replaced FE's earlier `source + source_id + content_hash` uniqueness rule,
which skipped exact repeats but changed identity when content changed and could
not safely merge new metadata into a skipped intake.

### Captured facts are attributed observations

The browser is often the best available acquisition environment because it is
acting on a page the user deliberately opened and may have access to rendered
DOM, page state, and authenticated media URLs. Its extracted title,
description, body, tags, transcript, and media candidates are still observations
with an adapter identity and version; they are not unqualified writes to
canonical user metadata.

FE retains source observations, user annotations, deterministic enrichment,
provider responses, and model output as distinguishable records. A display
projection can select a preferred current value without erasing competing or
older observations.

### Provider is orthogonal to media kind

`youtube`, `instagram`, and `pinterest` are source providers. `article`,
`image`, `gallery`, `video`, `audio`, `document`, and `text` are renderer kinds.
FE does not create permanent core types such as `youtube_fragment` or
`pinterest_image`.

Provider adapters produce the same versioned capture and media contracts. The
Reader selects a renderer by content and media capabilities, then optionally
uses a provider playback adapter.

### Useful partial state is valid state

The text and provenance of a capture commit before optional media transfer or
enrichment finishes. A failed original image download, missing transcript, or
unavailable provider API does not discard the capture.

Each asset and enrichment capability has its own lifecycle. Reader projections
represent pending, available, reference-only, and failed states explicitly.

### Reading and processing are independent axes

Reading an item does not route it, close a triage case, materialize it to FFS,
or delete it. Conversely, routing or materializing content does not imply that
it was read. The Reader can present controls for all of those domains without
collapsing their state machines.

## System boundaries

```text
Rendered browser page
  -> fe-clipper provider capture adapter
  -> versioned CaptureEnvelope manifest
  -> Fragments Engine capture service
       -> source observation + stable fragment/revision
       -> media asset catalog + content-addressed blob store
       -> deterministic/provider/model enrichment
       -> triage, route, materialization, and recall services
       -> ReaderItem projection
  -> Sysop Reader UI
       -> media renderer + semantic commands
       -> optional future agent sidecar by external session reference
```

### `fe-clipper` owns

- The explicit user capture interaction and client-generated capture ID.
- Page-context extraction through a registry of provider adapters.
- Highlight and capture-note collection.
- Best-effort discovery of canonical identity, document content, media items,
  variants, and provider metadata available in the rendered browser context.
- A durable browser-side capture job that can resume when the extension service
  worker is restarted.
- Browser-preferred media transfer without sending browser credentials, cookies,
  or provider tokens to FE.
- Reporting extraction and transfer warnings as structured facts.

The popup is a control surface, not the owner of a long-running transfer. Once
FE accepts the manifest, the popup may report success and close while the
extension's persisted job continues.

### Fragments Engine owns

- The capture protocol and its schemas.
- Stable fragment identity, observations, revisions, and capture-attempt history.
- Validation, idempotency, provenance, and field-resolution policy.
- Logical media identities, variant records, custody, integrity, and the
  content-addressed blob store.
- Provider-side completion, transcript acquisition, and derived previews.
- Enrichment, triage, routing, FFS materialization, recall, and their independent
  lifecycles.
- The Reader projection, read-state commands, and semantic quick actions.
- Capability discovery for capture clients and Reader surfaces.

### Source and provider systems own

- The current externally published source content.
- Provider-specific playback behavior and availability.
- Official provider identity, metadata, and API semantics.

FE is authoritative for what it observed and mirrored, not for what the source
currently publishes.

### Agent/session systems own

- Agent selection and execution.
- Conversation messages, tools, and session lifecycle.
- Any generated answer or downstream action.

FE supplies revision-pinned reading context and retains a conversation reference
when a Reader session is associated with a fragment.

## Core domain model

The names below describe semantic records. They do not require one table per
type, but their identities and lifecycles must remain distinct.

### Source identity

```text
SourceIdentity
  source registration ID
  provider: pinterest | instagram | youtube | web | ...
  provider item ID, when available
  submitted URL
  canonical URL
  segment key, default "root"
  canonicalizer adapter + version
```

Provider IDs take precedence over URLs for identity. Tracking parameters,
fragments, mobile/desktop host aliases, and other provider-specific URL forms
are normalized only by a versioned canonicalizer. FE records both the submitted
and canonical URLs so normalization never destroys provenance.

### Fragment and revision

```text
Fragment
  stable ID
  owning scope
  authority and custody mode
  SourceIdentity lineage
  accepted/current revision pointer
  lifecycle and policy references

FragmentRevision
  immutable revision ID and ordinal
  fragment ID
  normalized document content and format
  source title and description observations
  ordered media-manifest digest
  source observation references
  normalization adapter + version
  content digest
  observed and committed times
```

The digest covers normalized source material, not user state such as tags,
notes, triage, or reading position. Those change independently and therefore do
not manufacture source-content revisions.

### Capture attempt

```text
CaptureAttempt
  capture ID and idempotency key
  fragment ID and observed revision ID
  client kind + version
  actor and captured time
  submitted URL and page context
  extraction adapter reports
  completion: accepting | transferring | complete | partial | failed
  per-item transfer outcomes and warnings
```

The attempt provides the requested "how many times did I add this?" history.
An attempt is append-only apart from its bounded operational completion state.

### Annotations and notes

```text
CaptureAnnotation
  annotation ID
  capture ID and fragment ID
  kind: highlight | capture_note
  text
  optional text-quote selector and document position
  actor and captured time

CuratedNote
  fragment ID
  Markdown body
  optimistic revision
  actor and updated time
```

Capture annotations are append-only. Exact repeated annotation values may be
shown once in the UI while retaining each capture occurrence. A new highlight
or capture note never deletes an older one.

The curated note is intentionally different: it is one user-maintained note
whose edits use an expected revision/ETag. It is not mixed into captured page
content and does not change the source-content digest.

### Tags and descriptive observations

User-supplied tags use set-union semantics for capture intake. Removing a tag
requires an explicit remove-tag command; an omitted tag never means removal.
Provider, deterministic, model-suggested, and user tags retain attribution even
when the Reader displays a combined set.

Source titles and descriptions belong to their source observation/revision.
User-authored alternatives are assertions. Reader resolution selects the
preferred value by explicit precedence and retains the history rather than
overwriting all prior descriptions.

### Media asset, variant, and attachment

```text
MediaAsset
  stable logical media ID
  provider media ID, when available
  kind: image | video | audio | document | timed_text | other
  source locator and position-independent identity
  dimensions, duration, alt text, and provider observations
  source authority and default custody

AssetVariant
  variant ID
  media asset ID
  kind: original | preview | thumbnail | poster | audio | subtitles | transcript
  source URL and expiry hint
  MIME type, dimensions, duration, byte size
  digest and optional FE blob handle
  custody: reference | cache | mirror | adopted
  acquisition state

AttachmentRef
  fragment revision ID
  media asset ID
  role: primary | gallery_item | hero | inline | poster | transcript | other
  zero-based position
  optional caption and source context
```

`MediaAsset` identifies the provider's logical media object. `AssetVariant`
identifies a representation of it. `AttachmentRef` supplies ordered placement
within a fragment revision. This allows one gallery to retain stable item
identities and order while sharing duplicate bytes by digest.

The content-addressed blob store is FE's custody boundary for mirrored and
adopted bytes. Mirrored originals are retained indefinitely by default. Source
URLs remain attached for provenance even after successful mirroring. Derived
previews are rebuildable but are governed by explicit retention records rather
than inferred from their filesystem location.

Timed transcripts and subtitle tracks are variants or derived observations
associated with the video asset. Searchable transcript text can be projected
into recall without pretending it is the video itself.

### Derived observations and capability coverage

An enrichment result records producer, version, input revision and asset
digests, confidence, and time. Coverage is tracked by capability instead of one
blanket `enriched` flag:

```text
title             description       body
gallery_manifest  original_media    thumbnail_or_poster
transcript        OCR               vision
summary           tags              entities
```

Each capability reports `provided`, `missing`, `pending`, `failed`, `stale`, or
`not_applicable` and identifies its supplying observation. FE invokes an
enricher only for missing, failed, stale, or explicitly re-requested
capabilities. It does not skip all enrichment merely because the clipper sent a
page body, and it does not overwrite user-authored fields.

### Reading state

```text
ReadingState
  principal ID + fragment ID
  state: unread | in_progress | read
  position: discriminated renderer position
  last opened and completed times
  optimistic revision
```

Initial position variants include:

- `article`: normalized progress plus a stable block anchor and local offset
- `video`: elapsed seconds, optional duration, and provider media ID
- `gallery`: selected attachment ID and index
- `document`: page number plus optional normalized progress
- `audio`: elapsed seconds plus optional duration
- `none`: for content without meaningful positional progress

Progress is principal-scoped even in a single-user deployment so the schema
does not later conflate content state with a process-global UI preference.

## Canonical capture contract

FE publishes the canonical contract as OpenAPI 3.1 with referenced JSON Schema
2020-12 documents. The JSON schemas are strict, use discriminated unions for
media and playback, and provide an explicit extension container rather than
allowing arbitrary provider fields throughout the core envelope.

The clipper pins a supported contract version. Generated TypeScript types and
runtime validators come from the FE-owned schemas. FE advertises accepted
capture versions and relevant capabilities through its capability-discovery
endpoint. Additive optional changes may remain within `fe.capture.v1`; breaking
semantic changes require a new major contract.

An illustrative envelope is:

```json
{
  "schema_version": "fe.capture.v1",
  "capture_id": "01K...",
  "captured_at": "2026-09-03T18:42:00Z",
  "client": {
    "kind": "fe-clipper",
    "version": "..."
  },
  "source": {
    "submitted_url": "https://www.youtube.com/watch?v=3RmtNXqnreI",
    "canonical_url": "https://www.youtube.com/watch?v=3RmtNXqnreI",
    "provider": "youtube",
    "provider_item_id": "3RmtNXqnreI",
    "segment_key": "root"
  },
  "document": {
    "title": "...",
    "description": "...",
    "language": "en",
    "content": {
      "format": "markdown",
      "body": "..."
    }
  },
  "tags": ["..."],
  "annotations": [
    {
      "annotation_id": "01K...",
      "kind": "highlight",
      "text": "..."
    }
  ],
  "media": [
    {
      "client_media_id": "youtube:3RmtNXqnreI",
      "provider_media_id": "3RmtNXqnreI",
      "kind": "video",
      "role": "primary",
      "position": 0,
      "variants": [
        {
          "client_variant_id": "poster:maxresdefault",
          "kind": "poster",
          "source_url": "https://...",
          "transfer_preference": "browser_preferred"
        }
      ]
    }
  ],
  "extraction": {
    "adapter": "youtube",
    "adapter_version": "...",
    "observed_capabilities": ["title", "description", "body", "poster"],
    "warnings": []
  }
}
```

The contract is intentionally capable of expressing many ordered media items,
multiple variants per item, timed text, inline article media, PDF and audio
attachments, and provider-specific observations without changing its root
shape.

## Capture protocol and optimistic flow

Capture uses a manifest-first protocol:

1. The clipper creates and durably records a capture job.
2. It submits the `CaptureEnvelope` manifest without requiring all media bytes
   to finish first.
3. FE validates the contract, resolves stable identity, commits the source
   observation/capture attempt/fragment revision transactionally, and returns a
   Reader projection plus required asset operations.
4. The item is immediately visible in Reader with pending media states.
5. The clipper streams browser-acquirable variants independently. Each transfer
   is idempotent by capture variant identity and verified digest.
6. FE or provider adapters attempt permitted fallback acquisition for remaining
   variants and capabilities.
7. The client marks its contribution complete. FE derives the attempt's final
   `complete` or `partial` state from individual outcomes; optional enrichment
   may continue independently.

Conceptually, the API exposes:

```text
POST /v1/captures
PUT  /v1/captures/{captureId}/assets/{clientVariantId}/content
POST /v1/captures/{captureId}/complete
GET  /v1/captures/{captureId}
GET  /v1/capabilities
```

The exact transport paths may follow FE's API conventions, but these semantic
operations remain separate. A single large multipart request is not the
contract because it would block visibility on the slowest or least reliable
asset.

The manifest response identifies the stable fragment, observed revision,
capture attempt, already-known blob digests, requested uploads, rejected
variants, server-side acquisition choices, and initial `ReaderItem`. Repeating
the same `capture_id` returns the same semantic outcome. Uploading bytes already
present by digest reuses the blob.

Browser acquisition is an explicit user-directed action, not authorization for
FE to crawl a site. The extension requests provider host permissions required
for supported adapters, streams bytes promptly when URLs may expire, and never
exports session cookies or page tokens. If the browser cannot retrieve a
variant, it records the source reference and error; FE applies its own network,
provider, credential, and retention policy before attempting a fallback.

## Capture adapter architecture

`fe-clipper` selects a `PageCaptureAdapter` from a provider registry. A generic
adapter always remains available.

```text
PageCaptureAdapter
  matches(location, document) -> confidence
  identify(context) -> SourceIdentity observation
  extractDocument(context) -> document observation
  extractMedia(context) -> ordered media candidates
  extractProviderMetadata(context) -> attributed extensions
  reportCapabilities() -> adapter capability declaration
```

Adapters may inspect rendered DOM, Open Graph fields, JSON-LD, stable serialized
page state, media elements, and official provider APIs available to the client.
Every result names the adapter version and extraction warnings. Provider page
internals are treated as replaceable adapter details, never as FE domain fields.

All adapters also run the ordinary page-content and selection path. A Pinterest,
Instagram, or YouTube clip therefore preserves the user's highlight and the
readable text available on the page in addition to media-specific observations.

### Pinterest

The Pinterest adapter resolves the pin ID and canonical URL, title,
description, creator or board observations, provider tags when available, and
the primary image. It emits the largest observed source variant as `original`
and any genuinely available smaller representation as `thumbnail`; it does not
label an upscaled image as a full-size original.

Browser transfer is preferred for the original and thumbnail. FE can use a
Pinterest provider adapter or public presentation metadata to fill gaps, but a
single public metadata response is not assumed to describe every durable asset.

### Instagram

The Instagram adapter resolves the post or reel identity, canonical URL,
caption/description, author and tag observations, and every ordered carousel
item visible in page state. Image items include original and thumbnail
candidates where observed. Video items include a stream/reference candidate and
poster.

Authenticated page state and expiring media URLs make browser-side streaming
especially valuable. No browser credentials are transferred to FE. When an
arbitrary public post is outside the capabilities of an official configured
API, the browser observation remains valid and the FE provider adapter reports
the missing capability rather than inventing metadata.

### YouTube

The YouTube adapter resolves the video ID, canonical watch URL, title,
description, channel, tag and chapter observations, page content, poster
variants, caption-track candidates, and any transcript the browser can
legitimately acquire.

FE's YouTube provider adapter can complete metadata and captions using configured
official API credentials or other explicitly registered acquisition
capabilities. Playback uses the video ID through a trusted YouTube player
adapter. FE does not execute arbitrary embed HTML returned by metadata services.

The initial video asset normally has `reference` custody for the video stream
and `mirror` custody for selected posters and transcripts. A later explicit
`request_asset_acquisition` command can ask for an `original` video variant.
That command uses the same asset lifecycle and does not require a new fragment
or Reader renderer.

### Generic web articles and future providers

The generic adapter continues readable-document extraction and Markdown
normalization. It may also emit a hero image and ordered inline images. PDF,
audio, and other video providers implement the same source/media contracts and
add renderer or playback capabilities, not parallel intake systems.

### oEmbed

oEmbed is an optional provider-adapter input for presentation metadata and
provider identity. It is not the capture contract, a durable media acquisition
API, a complete gallery representation, or a transcript API. Returned HTML is
never inserted directly into Reader. FE converts recognized providers into a
validated `PlaybackSpec` or ordinary attributed metadata.

## Fragments Engine provider adapters

FE provider adapters operate behind narrow capability contracts:

```text
CanonicalIdentityResolver
MetadataProvider
MediaAcquirer
TranscriptProvider
PlaybackProvider
```

One provider implementation may implement several contracts, but intake does
not depend on a monolithic provider switch. Adapters declare required network
effects and credential references, input and output schemas, supported source
and media kinds, adapter version, and error classification.

The browser and FE may observe the same fact. FE retains both observations and
resolves the Reader projection by policy. A provider adapter fills missing or
stale capability coverage; it does not replace richer browser capture solely
because it ran later.

## Playback contract

Reader playback is a closed discriminated union. The initial external form is:

```json
{
  "kind": "provider_embed",
  "provider": "youtube",
  "provider_item_id": "3RmtNXqnreI",
  "start_seconds": 0
}
```

Future forms can include FE-custodied media and additional trusted providers:

```text
provider_embed   -> allowlisted provider + stable provider item ID
blob_stream      -> authorized FE asset-variant ID
external_stream  -> validated URL plus explicit policy and MIME information
```

The server, not the browser, determines allowed playback forms and exposes a
sanitized `PlaybackSpec`. The UI constructs provider players from IDs and
allowlisted parameters, applies a restrictive frame/content security policy,
and supports native fullscreen where the provider/player permits it.

## Reader architecture

### Reader is a projection, not a new content authority

Reader consumes a purpose-built, versioned `ReaderItem`; it does not reconstruct
cards from fragment rows and a sequence of attachment requests. FE produces the
projection from fragments, revisions, observations, asset state, reading state,
triage, routing, materialization, and server-advertised command capabilities.

An illustrative projection contains:

```text
ReaderItem
  schema version
  fragment ID + selected revision ID + optimistic revision
  source identity and canonical URL
  renderer kind
  resolved title, description, byline, date, and summary
  article preview and full-content availability
  ordered media with selected variants and acquisition states
  optional validated PlaybackSpec
  attributed/combined tags
  capture annotations + curated note summary
  capture count
  reading state
  triage-case summaries
  route and materialization summaries
  enrichment and acquisition summaries
  semantic action capabilities
```

List responses return complete card projections in batches so rich cards do not
cause attachment or status N+1 requests. Full content and high-resolution media
may remain separately fetched resources.

### Routes and navigation

```text
/reader?scope=inbox     unresolved processing-oriented items
/reader?scope=library   retained reading catalog
/reader?scope=all       all visible content
/reader/:fragmentId     canonical item location
```

A revision query parameter may pin a historical view. Opening an item from a
card can use an overlay while updating browser history to the canonical item
URL, so refresh, copy-link, forward/back, and direct navigation work. The page
reserves a sidecar region for future chat without requiring chat in the initial
Reader contract.

The scope is a query over independent state. It is not stored as a fragment
type, and an item can qualify for more than one scope.

### Renderers

- `article` renders a card excerpt and a full sanitized HTML projection generated
  from FE's normalized Markdown. The Markdown remains searchable source content.
- `image` renders the preferred thumbnail or preview and opens the original or
  largest available variant.
- `gallery` renders an ordered thumbnail strip/grid and a larger navigable view.
- `video` renders an inline player with fullscreen and preserves an article-like
  body, description, transcript availability, and metadata alongside it.
- `audio` renders an inline audio player and timed progress.
- `document` renders a preview/metadata card and a page-aware detail experience.
- `text` and `unknown` provide safe fallback renderers.

Cards use a WordPress-style bounded summary by default. Summary provenance is
retained: the value may be an explicit source description, a deterministic
excerpt, or a derived summary. The full reading view uses the normalized body,
not the card truncation.

The implemented baseline keeps list cards inert and bounded: one plain-text
excerpt plus at most one authorized visual preview. Players, gallery navigation,
and full resource states live on the detail page. Audio and document detail use
safe resource-state/link fallbacks until richer inline renderers are added.

Clicking a card outside its interactive media and controls opens the item detail.
Player controls, gallery navigation, links, selection, and quick-action controls
stop card navigation. Keyboard interaction follows the same boundary.

### Quick actions and optimistic UI

Reader invokes semantic commands rather than arbitrary metadata patches:

```text
add_tag / remove_tag
append_capture_note
update_curated_note
set_reading_progress / mark_read / mark_unread
request_asset_acquisition
route
materialize
```

`apply_triage_decision` is intentionally not part of the v1 registry; it belongs
to the separate disposition decision below.

The server advertises which actions are valid for each item and the schema each
command requires. The UI applies safe mutations optimistically, submits an
idempotency key and expected entity revision, and reconciles with the returned
projection or durable event. Conflicts preserve the user's unsent input and
present the authoritative state; they do not silently overwrite a concurrent
curated-note or progress update.

Set additions such as tags are naturally commutative. Explicit removals,
replacement edits, and decisions remain separate commands so omission is never
misinterpreted as deletion.

## Reader chat seam

The future sidecar uses a revision-pinned contract:

```text
ReaderContext
  fragment ID and immutable revision ID
  resolved readable content references
  selected attachment/asset references and digests
  transcript/OCR/derived observation references
  current user selection, when deliberately supplied
  provenance and sensitivity policy

ConversationRef
  fragment ID
  owning application/provider
  session or conversation ID
  pinned starting revision
  created-by actor and time
```

FE can expose authorized content by reference and store `ConversationRef`. It
does not store the message stream as Reader state, infer that a provider receipt
means the agent finished, or let an agent silently mutate tags, notes, routes,
or content without invoking the same authenticated semantic commands as any
other client.

## Disposition and organization: deliberately open decision

Reader exposes current triage state plus route and materialization controls, but
this architecture does not define what it means to finish processing an inbox
item. That remains a separate product/domain decision.

The unresolved question is which deliberate action closes an inbox/triage case
and how these concepts relate:

- route to a destination
- save/materialize to FFS
- retain only in FE
- assign a folder or collection
- defer/snooze
- archive or dismiss from the active queue
- delete or apply retention policy

The expected interaction is manual and case-by-case, not an automatic
read-implies-done rule. The eventual design should preserve these constraints:

- `read` is never equivalent to `processed`.
- Routing and FFS materialization are independent effects and do not
  automatically close every triage concern.
- Library organization is not encoded by moving the canonical fragment between
  physical folders.
- A triage decision records actor, reason, decision schema, and target revision.
- Reader asks FE for valid disposition commands instead of hard-coding one
  universal workflow.

This leaves room for an inbox-to-folder experience similar to consumer reading
tools while preserving FE's provenance, routing, and materialization semantics.

## Failure, recovery, and idempotency semantics

- Manifest acceptance is atomic with the capture attempt, source observation,
  stable fragment resolution, revision resolution, and durable follow-up work.
- A repeated request with the same capture ID returns the prior result and does
  not increment capture count.
- A new capture ID for the same stable source item records another attempt and
  increments capture count.
- The same normalized material digest reuses the current revision. Changed
  material creates a new revision under the same fragment.
- Asset uploads are idempotent by capture variant identity and verified digest.
  Blob identity allows bytes to be reused across variants and fragments.
- New capture tags are unioned. Removal requires a separate command.
- New annotations are appended. Exact repeats retain occurrence provenance and
  may be collapsed only in a display projection.
- Media failure is per variant. A gallery can be partially available without
  losing its order or successfully mirrored siblings.
- Enrichment failure is per capability and remains retryable without replaying
  the source capture.
- Capture client restart resumes jobs from durable browser storage and queries FE
  for already accepted state before resending bytes.
- Reader projections disclose pending and partial state and can update from FE's
  normal event/revalidation mechanism.

## Security and trust

- Web content, extracted Markdown, OCR, transcripts, provider metadata, oEmbed
  responses, and model output are untrusted inputs.
- Rendered article HTML is produced from normalized Markdown through an
  allowlist sanitizer. Source scripts, event handlers, styles, and embed markup
  are not retained as executable Reader content.
- Provider players are built from validated `PlaybackSpec` values and an
  allowlist, with restrictive iframe permissions and content security policy.
- Media MIME type and dimensions are verified from received bytes where
  possible; source declarations are observations, not proof.
- The clipper does not transmit cookies, bearer tokens, local storage, or page
  authentication material to FE.
- FE stores credential references for official provider APIs. Tokens/keys are
  supplied to adapters through the configured credential boundary and are not
  part of capture envelopes or Reader projections.
- Capture is a user-directed operation. It does not grant a provider adapter
  permission for bulk crawling or bypass network, provider, retention, or egress
  policy.
- Local personal deployment can use a simple loopback/API-token profile, while
  contracts still identify principal and client so content and reading state do
  not depend on anonymous global mutation.

## Provider evidence and portability assumptions

Provider integrations are intentionally adapter-based because public metadata
and API capabilities differ:

- The public [oEmbed provider registry](https://oembed.com/providers.json)
  currently lists endpoints for YouTube, Instagram, and Pinterest, but oEmbed's
  presentation response is not a durable media or transcript contract.
- YouTube provides an official
  [IFrame Player API](https://developers.google.com/youtube/iframe_api_reference)
  and its Data API exposes video resource metadata through
  [`videos.list`](https://developers.google.com/youtube/v3/docs/videos/list).
- Meta's current
  [Instagram API documentation](https://www.postman.com/meta/instagram/documentation/6yqw8pt/instagram-api)
  is account- and permission-oriented, so arbitrary clips must not depend on one
  universal official-API path.
- Chrome extension cross-origin acquisition depends on declared host permissions;
  see Chrome's
  [cross-origin network request guidance](https://developer.chrome.com/docs/extensions/develop/concepts/network-requests).

These are adapter implementation inputs, not stable FE domain assumptions.

## Architectural validation scenarios

The design is considered internally coherent when these scenarios have one
unambiguous outcome:

1. The same Pinterest pin is clipped twice with the same content. FE holds one
   fragment and one revision, records two capture attempts, reuses media blobs,
   unions new tags, and appends new highlights.
2. An Instagram carousel is clipped again after its caption or item order
   changes. FE keeps one fragment, creates a new revision with a new ordered
   attachment manifest, and preserves the prior revision.
3. A gallery manifest is accepted but one original image cannot be downloaded.
   Reader shows the card immediately, renders available siblings in order, and
   exposes a partial acquisition state for the failed item.
4. A YouTube capture contains body text and a highlight but no transcript. FE
   preserves both immediately, plays through a trusted provider spec, and tracks
   transcript completion separately.
5. The user later requests custody of YouTube video bytes. The request targets an
   existing media asset and creates/acquires an `original` variant; it does not
   create a new fragment type or duplicate capture.
6. An article is read to 70 percent and then routed and materialized. Reading,
   triage, route delivery, and FFS materialization each retain independent state.
7. A Reader quick action is optimistically applied while another client updates
   the curated note. Commutative tag additions merge; the note edit detects the
   optimistic revision conflict and does not discard either body.
8. A future agent discussion starts from revision 3 while revision 4 is later
   captured. The conversation reference still identifies revision 3 and FE can
   deliberately provide the newer revision as additional context without
   rewriting history.

## Non-goals of this architecture

- Defining delivery phases, estimates, sequencing, or migrations.
- Choosing the final inbox disposition and folder/collection semantics.
- Automatically downloading every video.
- Treating oEmbed as a universal extraction or playback contract.
- Building a general web crawler or bypassing provider access controls.
- Making FE the authority for the current external source.
- Making FFS a hidden second canonical database.
- Owning agent conversations or agent execution in FE.
- Hardening a personal local tool for arbitrary multi-tenant internet exposure.

## Consequences

This design adds explicit records and versioned projections in exchange for
predictable behavior. It prevents provider-specific extraction details from
leaking into the fragment core, makes background acquisition and partial failure
first-class, and gives Reader enough information to present media without
assembling state through ad hoc requests.

Most importantly, it turns repeat capture from a lossy deduplication check into
an auditable merge: one durable item, immutable source history, additive user
context, and independent reading and processing state.
