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

## YouTube provider completion

The YouTube adapter accepts only exact allowlisted watch, short-link, Shorts,
live, and embed URL forms. Every provider ID, source key, submitted URL,
canonical URL, and source locator present in one request must resolve to the
same 11-character video ID. Canonical identity is an attributed `entities`
observation; it strengthens provider/canonical fields but retains the existing
source item key, so it does not silently rewrite a fragment or revision identity.

Execution remains capability-scoped. Title, description, tags, and entities
each return only the claimed fact. Channel facts use `youtube_channel_id` and
`youtube_channel`. Chapters preserve provider order as repeated
`youtube_chapter` entities with the stable value
`<seconds to exactly three decimals><TAB><plain-text title>`. Chapter starts
must be finite, non-negative, millisecond-precise, strictly increasing, unique
by time, and before a known duration. This is a typed fact list, not encoded
JSON or a provider response blob.

The logical video asset is derived from the validated YouTube provider ID even
when the caller has not yet created it. Original video is reference-only by
default. Posters and transcripts use independent variants and default to
mirror custody; a failure in one capability does not invalidate another.
`ProviderMediaCompletion` creates or resolves the asset before the variant,
hash-verifies managed bytes through `MediaService`, and intentionally leaves
observation publication to the enrichment claim fence.

An explicit `AssetAcquisitionService.Request` records an idempotent generic
custody request against a media asset and variant kind. It creates at most one
deterministic target when that kind is absent and advances only valid custody
transitions; an eventual worker uses the ordinary media lifecycle to acquire
bytes. The YouTube adapter never downloads original video automatically.

## Legacy compatibility

Migration 015 seeds only immutable legacy revision title, description, and body
as source observations. It intentionally ignores mutable fragment summaries,
metadata-level `enrichment_status`, and attachment analysis flags. Existing
manual enrichment and inbox-review behavior continues to use those legacy flags
unchanged; browser capture and new provider work never depends on them.

`POST /v1/intake` and all six configured ingest kinds (`claude_code`,
`chatgpt_export`, `url_source`, `filesystem_docs`, `git_changes`, and
`nil_vault`) now enter through one application-layer compatibility adapter.
The adapter resolves the same stable identity and immutable source revision as
browser capture, records a durable capture attempt, merges explicit user tags
and capture-time annotations additively, and initializes all twelve coverage
rows in the same transaction. Exact semantic retries are read-only; changed
source material creates a new revision. Enriched titles/bodies, provider or
user metadata, OCR/vision analysis, and expiring attachment locations remain
mutable legacy projections and do not affect source material digests.

Coverage is evidence-accurate. Source-adapter taxonomy values are source
observations, inline hashtags found deterministically in manual content are
deterministic observations, and only explicit intake `tags` are user
observations. A transcript is provided only when an extractor supplies the
exact typed `transcript_text`; a YouTube body containing description plus
transcript or an unavailable-transcript placeholder is not itself transcript
evidence. Prefetched `source_url + content` therefore supplies only the typed
fields present and leaves absent provider/media capabilities eligible.
