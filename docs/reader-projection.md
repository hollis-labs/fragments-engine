# Reader projection v1

The v1 Reader endpoints expose complete, server-resolved views of durable
fragment state:

- `GET /v1/reader/items?scope=inbox|library|all&cursor=...` plus optional filters (below)
- `GET /v1/reader/items/{fragmentId}?revision_id=...`

Both endpoints return the frozen Browser Capture and Reader v1 schemas. The
service validates the whole response against that schema before the transport
writes it. Because FE's HTTP server has no authentication layer and Reader
projections contain private source material, both routes use the existing
loopback-only gate.

## List filters

`GET /v1/reader/items` accepts these optional query parameters, combinable
with `scope`. Items always stay newest first, 50 per page.

| Parameter | Meaning |
|---|---|
| `renderer` | Any-of `article`, `image`, `gallery`, `video`, `audio`, `document`, `text`. Repeat or comma-separate. |
| `tag` | Repeatable; every tag must be on the item (case-insensitive, combined tags). |
| `state` | `unread`, `in_progress` or `read` for the requesting principal. No recorded state counts as `unread`. |
| `provider` | Source provider, case-insensitive exact match. |
| `since`, `until` | RFC 3339, inclusive, on the item's capture (list sort) time. |
| `q` | Keyword text over title and content, using the same FTS index and term quoting as `GET /v1/search`. |
| `later`, `starred`, `archived` | `true` or `false`, on the requesting principal's flag (below). Without `archived`, archived items are left out of `scope=inbox` and `scope=library`, but not `scope=all`. `archived=true` returns only archived items. |

An unknown `renderer` or `state`, a malformed timestamp, `since` after `until`,
or a repeated single-valued parameter is a 400. The cursor carries a
fingerprint of the filters it was issued for; presenting it with different
filters is a 400. With no filters the response and cursors are unchanged.

`renderer` and `tag` are derived during projection, so those filters are
applied after the SQL filters over a bounded scan (5000 rows per request). If
the budget runs out before a page fills, the page is short but carries a
cursor; keep following `next_cursor` until it is absent.

## Later, archived and starred

Three independent per-principal flags live on the reading state:
`later`, `archived` and `starred`. In `reading_state` each appears as a boolean
plus `<flag>_at`, the time it was set, and both are omitted while the flag is
clear. They are changed with `set_later`, `set_archived` and `set_starred`,
each carrying `{"value": true|false}` plus the usual command and idempotency
identities. Like `mark_read`, they carry the reading state's `revision` as
`expected_revision` (a stale one is a 409 with the current item) and every
flag change advances it. Setting a flag that already has the requested value is
a no-op and keeps the original time. The flags never move an item into or out of
the inbox, and archiving is not deletion.

## Scope semantics

Fragments Engine does not yet persist the library disposition or collection
semantics reserved for follow-up work. Reader v1 therefore uses the narrowest
meaning supported by current persistence:

- `inbox` contains canonical fragments with a current `inbox` row.
- `library` contains every canonical, persistently visible fragment.
- `all` currently contains that same persistently visible universe.

Inbox is a subset of library and the scopes intentionally overlap. In
particular, Reader does not treat routing, materialization, or the absence of an
inbox row as an invented library disposition.

Pages contain at most 50 items and use an opaque, versioned, base64url cursor.
The cursor is bound to its scope and pins the last `(sort timestamp, canonical
fragment ID)` pair. Inbox sorts by staging time; library/all sort by the latest
capture time, falling back to ingest time.

## Batched read model

A list page (including an empty page) and a detail request each execute exactly six SELECT
statements, regardless of item count:

1. canonical fragment, selected revision, source identity, inbox marker,
   capture count, and curated note;
2. all revision capability coverage plus each selected observation;
3. ordered attachment references, logical media, and all physical variants;
4. attributed fragment tags plus typed revision tag observations;
5. capture annotations;
6. routing and materialization log facts.

The five dependent reads use page-wide `IN` batches. Repository tests count
queries for one-item and multi-item pages to prevent per-item repository calls.
Detail resolution performs alias canonicalization and requested-revision
ownership in the first SQL statement; a revision owned by another fragment is
not visible through the requested item.

## Resolution and safety

Selected title, description, and summary observations are decoded through the
closed capability value validator before display. Invalid or unsafe selected
values are ignored in favor of immutable source fields or deterministic
fallbacks. Coverage work state remains independent of display selection, so a
pending, stale, or failed refresh does not blank a useful selected observation.
Provider observations fill gaps only through the shared enrichment resolver;
the Reader projection does not apply a later-wins rule.

The article preview is bounded to 4,000 Unicode code points. A revision-scoped
content reference is included when the immutable revision has full content.
Media placement is ordered only by
`attachment_refs.position`. Every variant retains its own
`pending`/`available`/`reference_only`/`failed` acquisition state; sibling states
are never collapsed.

Renderer selection uses provider-neutral content and recognized media roles.
An item whose primary media is a video is a `video` whatever its body. An item
with a body, meaning at least 280 characters of content after trimming, is
otherwise an `article` however many images it carries (an audio or document
attachment keeps its own renderer). The gallery rule below therefore applies to
items without a body. `display.featured_media_id` names the media asset of the
featured image (the `hero` image, else the first renderable image by position,
absent when there is none) and `display.image_count` counts the renderable
images (roles primary, hero, gallery_item, inline), so a client can show the
featured image with a gallery affordance when the count is above one.
More than one renderable visual attachment (image or video) forms an ordered
gallery, including mixed-media carousels. A single video then wins over a
single image, followed by audio, document, article, text, or unknown.
Acquisition state never changes the discriminator: a failed
image/gallery/video remains that renderer so the failure can be shown. Timed
text, poster, transcript, and other auxiliary attachments cannot turn a video
or article into a gallery. Provider playback is emitted only for a video
projection whose canonical source is YouTube and whose provider item ID passes
the closed YouTube playback validator. No provider HTML or arbitrary embed
instructions participate in projection.

## Independent operational state

- Triage reports one unresolved item when an inbox row exists, but does not
  invent a triage case identifier.
- Routing and materialization are reduced independently from the latest durable
  result for each route/destination reference.
- Enrichment mirrors every revision capability row, including retained selected
  observations while work is pending, stale, or failed.
- Acquisition counts physical variant states independently.
- Capture count uses accepted capture attempts and is floored at one for legacy
  fragments whose original ingest predates capture-attempt persistence.

Reader state is principal-scoped and independent of source revisions and
operational state. A fragment without saved state is projected as `unread` at
position `none`, revision zero, for the local Reader principal. Saved positions
use the renderer-specific `article`, `video`, `gallery`, `document`, or `audio`
shape. `ReaderItem.revision` is the optimistic Reader aggregate revision, not
the immutable source revision ordinal; immutable content remains identified by
`fragment_revision_id`.

The projection advertises only commands that apply to the item. The closed v1
registry contains `add_tag`, `remove_tag`, `append_capture_note`,
`update_curated_note`, `set_reading_progress`, `mark_read`, `mark_unread`,
`request_asset_acquisition`, `route`, `materialize`, `set_later`,
`set_archived`, and `set_starred`. Every mutation carries
the expected aggregate revision plus stable command and idempotency identities.
Principal tag overlays do not erase attributed source/provider/user facts.
`tags.attributed` has one entry per tag and source. When several observations
of the same tag come from the same source, such as a capture's tag row and the
enrichment record of the same tags, they collapse into one entry whose
`observation_id` is the tag-observation row's id (else the first) and whose
`observation_ids` lists all of them. `observation_ids` is omitted when there is
only one observation. `tags.combined` is unaffected.
External route and materialize effects retain durable receipts and surface
uncertain outcomes instead of risking an automatic duplicate effect.

The six-query read budget remains intact: principal reading state and the
aggregate revision are joined into the base query, while tag overlays are
folded into the existing batched tag read. Generated media links use the
fragment-and-revision-authorized resource route described in
[`reader-resources.md`](./reader-resources.md). The command schema still defines
no triage or disposition mutation.
