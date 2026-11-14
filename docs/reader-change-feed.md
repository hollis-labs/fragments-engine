# Reader change feed

A client that keeps its own index of Reader items asks one question: what
changed since I last looked? This note is the design the route, tables and
triggers follow (CW-20261114-0048).

```
GET /v1/reader/changes?after=<cursor>&limit=<1..200, default 50>
```

```json
{
  "schema_version": "fe.reader.changes.v1",
  "items": [
    {"fragment_id": "…", "change_seq": 41, "kinds": ["note", "tags"], "item": { "…": "a Reader item" }}
  ],
  "next_cursor": "…",
  "caught_up": true
}
```

The route needs the `fragments-engine.read` scope and answers for the Reader
principal, like the other Reader reads.

## How a client uses it

1. Call with no `after`. Store every `item`, then call again with
   `next_cursor`, until `caught_up` is true. That walk returns every Reader
   item once, at its current state.
2. Keep the last `next_cursor`. Later, call with it: the response holds only
   the items that changed since, and again a `next_cursor` to keep.

`next_cursor` is always present, including on an empty, caught-up page.
`item` is the same projection `GET /v1/reader/items/{fragmentId}` returns, so
the client replaces its copy and needs no second request.

## What the feed promises

- **Order.** Items come oldest change first. `change_seq` only grows.
- **Once per walk.** An item changed several times since the cursor appears
  once, at its latest change, with every kind of change listed in `kinds`.
- **Current state.** `item` is read when the page is built, so it is never
  older than `change_seq`. If it changes again, it comes back later.
- **Nothing missed.** A change committed before a page is read is in that page
  or a later one. Sequence numbers are handed out inside the transaction that
  makes the change, and SQLite commits writers one at a time, so sequence order
  is commit order: no change can appear behind a cursor that has passed it.
- **Private changes stay private.** A change to one principal's reading
  state, tag overlay, commands or collections appears only in that
  principal's feed.
- **The cursor is durable.** It is a position in stored data, not in server
  memory, so it works across restarts. It is opaque and bound to the principal
  it was issued to; a malformed or foreign cursor is a 400.

`kinds` is a hint about why the item is in the page. It lists what changed
since the client was last caught up (during a first sync, since the
beginning), and a client must ignore a kind it does not know. The values:

| Kind | Meaning |
|---|---|
| `capture` | The fragment was created or captured again, or its source identity changed. |
| `revision` | Its current revision moved, or a revision's title, description, content or metadata was edited. |
| `tags` | A tag observation, an enrichment tag observation, or the principal's tag overlay. |
| `annotations` | A capture note or highlight. |
| `note` | The curated note. |
| `reading_state` | The principal's reading state, position, or later/archived/starred flags. |
| `media` | Attachments, the media asset, or a variant's acquisition state (media becoming available). |
| `enrichment` | Capability coverage, which selects the shown title, description and summary. |
| `triage` | The fragment entered or left the inbox. |
| `routing` | A route-log fact, or the principal's route or materialize command. |
| `collections` | The principal's collection membership, or a rename of a collection it is in. |

Not covered: deletions (there is no delete route) and push delivery.
A fragment that becomes an alias of another simply stops appearing.

## Storage (`023_reader_change_feed.sql`)

`reader_change_sequence` is one row holding the last sequence number.

`reader_changes(fragment_id, principal_id, kind, change_seq)` has one row per
fragment, principal scope and kind: the sequence number of the latest change
of that kind. An empty `principal_id` is a change every principal sees. The
table stays bounded by fragments × kinds; it is not a log.

A page is the fragments whose newest row in the caller's scope (shared plus
their own) lies after the cursor, in `(change_seq, fragment_id)` order, joined
to the same base query the Reader list uses. Fragments that are not Reader
items fall out of that join. The migration seeds one `capture` row per
existing fragment, so a database that predates the feed still syncs in full.

A page costs nine SELECT statements whatever its size: the sequence, the page,
the six set-based reads of the Reader list, and the kinds.

## Write paths that advance the sequence

The sequence is advanced by SQLite triggers on the tables a Reader item is
projected from, not by calls in the Go write paths. A write path cannot
forget to record a change, and one added later (including by the Substrate
port) is covered as long as it writes the same tables. Each trigger runs in the
transaction of the write it observes. `UPDATE` triggers fire only when a
column the item shows actually changes, so rewriting a row with the same
values does not put the item back in the feed.

This is the complete list. `internal/service/reader_changes_paths_test.go` has
one case per row.

| Table | Events | Kind | Scope |
|---|---|---|---|
| `fragments` | insert | `capture` | shared |
| `capture_attempts` | insert | `capture` | shared |
| `fragment_source_identities` | insert, update | `capture` | shared |
| `fragments` | update of `current_revision_id` | `revision` | shared |
| `fragment_revisions` | update | `revision` | shared |
| `fragment_tag_observations` | insert, update, delete | `tags` | the actor for a tag added by a Reader command, otherwise shared |
| `enrichment_observations` | insert with capability `tags` | `tags` | shared |
| `reader_tag_overlay_events` | insert | `tags` | principal |
| `capture_annotations` | insert, update, delete | `annotations` | shared |
| `curated_notes` | insert, update, delete | `note` | shared |
| `reading_states` | insert, update, delete | `reading_state` | principal |
| `attachment_refs` | insert, update, delete | `media` | shared |
| `media_assets` | update | `media` | shared, every fragment the asset is attached to |
| `asset_variants` | insert, update, delete | `media` | shared, every fragment the asset is attached to |
| `reader_command_receipts` | state change of `request_asset_acquisition` | `media` | principal |
| `fragment_capability_coverage` | insert, update, delete | `enrichment` | shared |
| `inbox` | insert, delete | `triage` | shared |
| `route_log` | insert | `routing` | shared |
| `reader_command_effects` | insert, state change | `routing` | principal |
| `reader_collection_items` | insert, delete | `collections` | the collection's owner |
| `reader_collections` | update of name or kind | `collections` | the owner, every member |

Reordering a collection is deliberately absent: position does not show on a
Reader item. A client tracks order through the collection's own `revision`.

## Limits of the design

- The feed over-reports rather than under-reports. A media asset attached to
  an old revision still marks its fragment changed.
- The triggers are defined in SQL, away from the Go code that writes the
  tables. A new table that a Reader item starts to show needs a trigger and a
  row in the list above.
- The migration runner splits statements on semicolons and counts two
  keywords to find trigger bodies, so the migration avoids `CASE` expressions
  and those words in comments. The file's header says so.
