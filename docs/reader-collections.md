# Reader collections

A collection is a named, ordered set of fragments that one principal keeps: a
video playlist, a reading list, the research for one article. This note is the
design the routes and tables follow (CW-20261114-0047).

## Shape

- A collection belongs to exactly one principal. Nobody else can read or change
  it; to another principal it does not exist (404, never 403).
- A fragment may be in any number of collections, and at most once in each.
- Membership is a reference. Deleting a collection removes its membership rows
  and nothing else; fragments, reading state, tags and notes are untouched.
- Order is explicit. Members have a position, new members go to the end unless
  a position is given, and the whole order can be replaced in one call.
- `kind` is a display hint, not behavior: `playlist`, `reading_list` and
  `research` are the well-known values, `collection` is the default, and any
  lowercase slug (`^[a-z][a-z0-9_]{0,31}$`) is accepted so a client can add its
  own without a contract change. The server treats every kind the same.

Not in this design: sharing between principals, smart or rule-based
collections, nesting, routing or destinations, and the Sysop web UI.

## Tables (`022_reader_collections.sql`)

`reader_collections`: `id`, `principal_id`, `name`, `name_key` (trimmed,
lowercased), `description`, `kind`, `revision`, `created_at`, `updated_at`.
`(principal_id, name_key)` is unique, so one principal cannot have two
collections whose names differ only by case.

`reader_collection_items`: `collection_id`, `fragment_id`, `position`,
`added_at`; primary key `(collection_id, fragment_id)`. Both columns are
foreign keys with `ON DELETE CASCADE`, so a deleted collection takes its
membership with it and a membership row can never point at a missing fragment.
Members are stored under the canonical fragment ID; a historical alias is
resolved when it is added or removed.

Positions are an ordering, not an index a client may rely on: the server
rewrites them to `0..n-1` on every membership change.

Limits: 1000 collections per principal, 5000 members per collection, names of
1 to 200 characters, descriptions up to 2000.

## Concurrency

Each collection carries a `revision`, starting at 1 and advancing by one on
every change that takes effect (rename, describe, kind, add, remove, reorder).
A request that changes nothing does not advance it.

Every mutation carries `expected_revision`, under the rule the Reader commands
use:

- An `expected_revision` ahead of the current revision is always a 409.
- Appending a member (no `position`) is accepted from any revision at or
  behind the current one, as `add_tag` is: an append does not depend on what
  the client last saw.
- Everything else (update, delete, add at a position, remove, reorder) must
  carry exactly the current revision, as `remove_tag` and the reading-state
  commands do; anything else is a 409.

A 409 is a `conflict` problem whose detail names the expected and current
revisions. The client re-reads the collection and retries. Adding a fragment
that is already a member and removing one that is not are no-ops that return
the unchanged collection.

## Routes

All under `/v1/reader/collections`. Reads need the `fragments-engine.read`
scope and writes `fragments-engine.write`, the same as the other Reader routes.
The principal is the Reader principal (`local-user` over HTTP today, as for
reading state); the service takes it as a parameter.

| Route | Body or query | Result |
|---|---|---|
| `GET /v1/reader/collections` | | 200 collection list, ordered by name, each with `item_count` |
| `POST /v1/reader/collections` | `{name, description?, kind?}` | 201 collection; 409 if the name is taken |
| `GET /v1/reader/collections/{collectionId}` | | 200 collection |
| `PATCH /v1/reader/collections/{collectionId}` | `{expected_revision, name?, description?, kind?}` | 200 collection |
| `DELETE /v1/reader/collections/{collectionId}` | `?expected_revision=` | 204 |
| `GET /v1/reader/collections/{collectionId}/items` | `?cursor=` | 200 the collection plus a page of Reader items in position order |
| `POST /v1/reader/collections/{collectionId}/items` | `{expected_revision, fragment_id, position?}` | 200 collection |
| `DELETE /v1/reader/collections/{collectionId}/items/{fragmentId}` | `?expected_revision=` | 200 collection |
| `PUT /v1/reader/collections/{collectionId}/order` | `{expected_revision, fragment_ids}` | 200 collection |

`position` on add is a zero-based index; at or past the end means append.
`fragment_ids` on reorder must be exactly the current members, each once, in
the new order; anything else is a 400.

The item list returns ordinary Reader items, 50 per page, with an opaque
`next_cursor` bound to the collection. Every page repeats the collection with
its `revision`: a client walking pages that sees the revision change knows the
order may have moved under it and can restart. Archived items stay listed; a
collection is an explicit choice, not an inbox view.

## On the Reader item

A Reader item gains an optional `collections` array: one
`{collection_id, name, kind}` per collection of the requesting principal that
contains the fragment, ordered by name. It is omitted when the fragment is in
none, so responses for anyone not using collections are unchanged. It is read
with one extra set-based query per page, which takes the fixed Reader query
budget from six statements to seven.

Membership is not part of the item's own `revision`: adding a fragment to a
collection does not disturb `expected_revision` for tags or reading state.

## Contract

New in `contracts/browser-capture-reader/v1/`: `fe.reader.collection.v1`,
`fe.reader.collection-list.v1`, `fe.reader.collection-items.v1` and the four
request bodies, with the routes in `openapi.json`. The `collections` field on
the Reader item is an additive optional field within v1. A client discovers
the feature from `reader.schema_versions` in `GET /v1/capabilities`, which
lists the three collection versions. The capabilities schema itself is
unchanged: it is closed, so a new operation flag would be rejected by clients
pinned to the published v1 with strict validation.
