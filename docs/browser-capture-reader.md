# Browser capture and Reader operations

This guide describes the implemented local v1 browser-capture and Reader
surface. The design rationale and deferred boundaries live in
[`browser-capture-reader-architecture.md`](./browser-capture-reader-architecture.md).
The language-neutral contract source is
[`contracts/browser-capture-reader/v1`](../contracts/browser-capture-reader/v1/README.md).

## Runtime profile

Run FE with the gitignored runtime configuration, never the commented example:

```bash
make seed-config
make serve-api
```

The standard Cerberus development resource serves FE at
`http://127.0.0.1:8091`. Its Sysop Reader is at
`http://127.0.0.1:8091/sysop/reader?scope=inbox`.

`GET /v1/capabilities` is the runtime source of truth. A client must confirm its
contract version appears in `accepted_versions` and the operation it needs is
ready. The current server reports manifest acceptance, asset upload, capture
completion, Reader query, and Reader commands as ready. Reader context and
conversation-reference schemas are published compatibility seams but their
operations are not ready.

The current runtime advertises no server-side provider adapters or optional
tools (`providers` and `optional_tools` are empty). Ordinary browser capture
therefore needs no provider API credential in FE. The provider adapter boundary
supports opaque credential references and callback-supplied secret bytes, but
no browser-provider credential setting is wired into `fragments.yaml` today.
Never put credential values in manifests, adapter descriptors, logs, or config
fields that expect an environment-variable or opaque reference name.

Reader queries, commands, article bodies, and media resources are explicitly
loopback-only because FE has no general HTTP authentication layer. Capture
routes are not protected by that handler gate; the submitted `principal_id` is
provenance, not authentication. Keep the API bound or firewalled to a trusted
local environment.

## Capture protocol

The durable flow is manifest first, asset transfer second, completion last:

1. `POST /v1/captures` with an `fe.capture.v1` envelope. FE commits the stable
   fragment identity, immutable revision, deliberate capture attempt, ordered
   media manifest, annotations, tags, capability coverage, and follow-up work in
   one transaction. The item is immediately visible in Reader.
2. Follow each returned upload binding with
   `PUT /v1/captures/{captureId}/assets/{clientVariantId}/content`. Send an
   allowed content type and RFC 9530 `Digest: sha-256=:<base64>:` header. FE
   streams, verifies, and content-addresses the bytes before attaching them to
   the variant.
3. `POST /v1/captures/{captureId}/complete` with an
   `fe.capture.completion.v1` result for every binding. Partial failures stay
   local to their variants; they do not erase the accepted fragment or working
   siblings.
4. Inspect `GET /v1/captures/{captureId}` at any point for the durable attempt,
   binding, completion, and follow-up state.

The manifest limit is 2 MiB, completion JSON is limited to 1 MiB, and one asset
upload is limited to 256 MiB. Discovery advertises the manifest and asset limits.
Uploads accept `application/octet-stream`, `image/*`, `video/*`, `audio/*`, or
`text/*`; FE still validates the received bytes before serving them.

Transport retry and deliberate recapture are different operations. Retrying an
uncertain request reuses the same capture ID and exact semantic request. Clicking
Clip again creates a new capture ID and a new capture attempt, while stable
source identity and unchanged material still resolve to the existing fragment
and revision. Changed source material creates a new immutable revision.

The capture HTTP handlers are thin bindings over `App.Captures`, media custody,
and the shared stable-identity repositories. The binary does not currently
expose the streaming capture protocol as CLI or MCP commands; this is an
intentional transport-surface difference, not a second ingestion model.
`POST /v1/intake` and configured ingests enter the same identity/revision model
through the legacy compatibility adapter.

## Media custody and backup

With a filesystem database such as `./data/fragments-engine.db`, FE places the
content-addressed capture store beside it at
`./data/.fragments-engine-blobs/`. Verified bytes live under
`sha256/<first-two-hex>/<digest>`, and identical content is reused across
variants and fragments. Mirrored and adopted blobs have indefinite retention by
default. Source URLs and legacy `source_path` values are provenance; Reader
never fetches, redirects to, or opens them as if FE owned the bytes.

Treat the SQLite database and `.fragments-engine-blobs` as one backup unit. The
database contains identity, revision, attachment, digest, and storage-handle
records; the blob directory contains the bytes. A database-only backup preserves
metadata but cannot restore captured media. Take a coordinated snapshot while
FE is stopped/quiescent, or use a SQLite-consistent backup plus a matching blob
snapshot. Preserve relative placement when restoring and verify Reader media
before discarding the source backup.

Reference-only and pending variants are truthful partial states, not broken
custody. They have no Reader content link. YouTube playback uses a closed trusted
provider embed; FE never stores or executes provider HTML and never downloads
original YouTube video automatically. A user must explicitly request an asset
acquisition, and a separately configured worker/provider must be available to
fulfill it.

## Reader

The Sysop routes are:

- `/sysop/reader?scope=inbox` for fragments with a current inbox row;
- `/sysop/reader?scope=library` and `scope=all` for all persistently visible
  fragments (these scopes intentionally overlap until disposition is designed);
- `/sysop/reader/{fragmentId}` for a canonical, shareable detail page, with an
  optional `revision_id` query pin.

The API equivalents are `GET /v1/reader/items?scope=...` and
`GET /v1/reader/items/{fragmentId}`. Pages contain at most 50 records and use an
opaque cursor. Full article content and FE-custodied media are fetched from
revision- and ownership-scoped resource links in the projection. List cards use
one bounded text excerpt and, when authorized bytes exist, one inert visual
preview. Detail pages render sanitized Markdown and renderer-specific image,
mixed gallery, video, audio, document, text, or fallback states. A source-only
legacy record says so instead of loading its remote URL as media.

Reader state belongs to the local principal and stays independent of triage,
routing, materialization, enrichment, and acquisition. Progress uses a closed
renderer-specific position: article progress/anchor, video or audio time,
gallery attachment/index, or document page. `mark_read` and `mark_unread` do not
move an item into or out of the inbox.

The action menu is projected per item from this closed registry:
`add_tag`, `remove_tag`, `append_capture_note`, `update_curated_note`,
`set_reading_progress`, `mark_read`, `mark_unread`,
`request_asset_acquisition`, `route`, and `materialize`. Commands require the
projected aggregate revision and stable command/idempotency IDs. A conflict
returns the current item so the UI can preserve a draft and reconcile. Triage
decisions, inbox disposition, and folders/collections are not Reader v1 actions.

Reader routes and commands are HTTP/Sysop surfaces over `App.Reader`,
`App.ReaderResources`, and `App.ReaderCommands`. Existing route and materialize
effects call the canonical routing service. No separate capture/Reader CLI or
MCP wrappers are advertised; existing FE operations exposed in multiple
transports continue to share their application service implementations.

## Legacy compatibility

Manual intake and all configured ingest kinds resolve through the same stable
fragment identity, immutable revision, capture-attempt, additive context, and
capability-coverage model. Existing attachment tables remain dual-written and
are backfilled into ordered media assets/variants. A compatible local legacy
file is served only when it is adopted/available, remains under
`reviewer.download_root`, and passes no-symlink containment checks. Remote-only
legacy attachments remain references.

Older article bodies may contain source-site navigation or Markdown image links
without captured image bytes. Reader sanitizes the HTML, removes remote image
markup without changing canonical Markdown, and presents the text that FE
actually owns. Re-capture through the manifest protocol is the way to add
browser-observed media to those items.

## Diagnostics

Start with public, read-only endpoints:

```bash
curl -fsS http://127.0.0.1:8091/healthz
curl -fsS http://127.0.0.1:8091/v1/capabilities | jq .
curl -fsS http://127.0.0.1:8091/v1/captures/<capture-id> | jq .
curl -fsS 'http://127.0.0.1:8091/v1/reader/items?scope=all' | jq .
curl -fsS http://127.0.0.1:8091/v1/workers/status | jq .
```

Interpret failures by boundary:

- no capture status means the manifest never committed to that FE instance;
- a capture visible with pending bindings means the manifest committed but the
  browser has not finished transferring or completing it;
- a failed binding is representation-specific; inspect its code/retryability;
- a Reader item with `reference_only` media has provenance but no FE-owned bytes;
- `providers: []` / `optional_tools: []` means the server has no advertised
  provider-side completion path, so do not wait for one silently;
- an empty Reader on one port commonly means the clipper and UI are using
  different FE databases or processes—compare the extension endpoint with the
  address used for the Reader and query the capture ID directly;
- after rebuilding the embedded Sysop bundle, reload/restart the long-running
  FE process; replacing the binary on disk does not update an existing process.

For the normal development service, confirm Cerberus launches with
`--config fragments.yaml`, not `fragments.example.yaml`. Use the browser network
panel to verify the manifest reached `/v1/captures`, then follow the same capture
ID through lookup, binding transfer, completion, and Reader projection.

## Deliberate non-goals

The v1 baseline does not implement inbox disposition or persistent
folder/collection semantics, automatic YouTube video download, FE-owned agent
messages/session lifecycle, arbitrary provider HTML, or remote media proxying.
The published Reader context and conversation-reference schemas reserve a
future revision-pinned handoff to an external session owner; their runtime
operations remain disabled.
