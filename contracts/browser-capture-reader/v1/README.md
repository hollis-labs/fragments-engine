# Browser Capture and Reader contract v1

This directory is the Fragments Engine source of truth for browser capture and
Reader transport contracts. `openapi.json` is OpenAPI 3.1; every schema under
`schema/` uses JSON Schema 2020-12. The Go package embeds those exact files and
validates JSON before binding it to transport-edge types.

The contract keeps provider data inside `extensions` objects whose keys use the
form `provider.<provider>`. Playback is a closed union and never accepts raw
provider HTML. Reading, triage, routing, materialization, enrichment, and asset
acquisition are independent fields. Inbox disposition is deliberately absent;
it remains owned by `CW-20260903-0060`.

The general `SourceIdentity` uses `source_item_key` plus `segment_key`, with an
optional source-native locator and optional URL provenance. This lets Reader
represent manual, chat, git, filesystem, and Nil fragments without inventing
HTTP URLs. `CaptureEnvelope` narrows that general identity for browser clients
and still requires both submitted and canonical URLs.

## Versions and compatibility

- Capture manifest: `fe.capture.v1`
- Manifest response: `fe.capture.result.v1`
- Capture completion: `fe.capture.completion.v1`
- Capture status: `fe.capture.status.v1`
- Reader projection/list/command: `fe.reader.item.v1`, `fe.reader.list.v1`, and
  `fe.reader.command.v1`
- Revision-pinned chat seam: `fe.reader.context.v1` and
  `fe.reader.conversation-ref.v1`
- Capability discovery: `fe.capabilities.v1`

Additive optional fields may be added within v1. Removing a field, changing a
field's meaning, or changing a discriminator requires a new major contract.
`GET /v1/capabilities` reports accepted versions separately from runtime
operation readiness, so a client must check both. Manifest acceptance, raw asset
upload, capture lookup, and client completion are ready at `/v1/captures` and
its capture-scoped subresources. Capability discovery reports
`capture_manifest`, `asset_upload`, and `capture_completion` as `true`, while
the later Reader operations remain `false`. It also advertises the 2 MiB
manifest and 256 MiB asset limits; completion JSON is limited to 1 MiB.

## Go edge validation

Use the schema validator before handing a request to application services:

```go
envelope, err := capturecontract.DecodeCaptureEnvelope(body)
command, err := capturecontract.DecodeReaderCommand(body)
err := capturecontract.ValidateJSON(capturecontract.SchemaReaderItem, response)
```

The Go structs are bindings for service edges. They do not supersede or generate
the language-neutral schemas.

## Reproducible `fe-clipper` consumption

The clipper must vendor this directory from one reviewed FE commit and record
that commit beside the generated output. It must not copy or maintain parallel
DTO declarations.

```bash
FE_REPO=/Users/chrispian/dev/hollis-labs/apps/fragments-engine
FE_CONTRACT_COMMIT=<reviewed-40-character-FE-commit>
DEST=vendor/fragments-engine-browser-contract-v1

mkdir -p "$DEST"
git -C "$FE_REPO" archive "$FE_CONTRACT_COMMIT" contracts/browser-capture-reader/v1 \
  | tar -x --strip-components=3 -C "$DEST"
printf '%s\n' "$FE_CONTRACT_COMMIT" > "$DEST/FE_COMMIT"
```

Pin generators in the clipper's lockfile, then generate API types directly from
the vendored OpenAPI document. The versions below are part of this handoff
recipe; consumer upgrades are explicit reviewable changes.

```bash
npm install --save-dev --save-exact openapi-typescript@7.10.1 ajv@8.17.1 ajv-formats@3.0.1
npx openapi-typescript@7.10.1 \
  vendor/fragments-engine-browser-contract-v1/openapi.json \
  --output src/generated/fragments-engine-api.ts
```

Runtime validation imports the vendored JSON schemas into Ajv 2020. Register
every file under `schema/` by its `$id`, then compile the roots named in
`contract-manifest.json`; do not transcribe their shapes into TypeScript. Ajv's
strict mode should remain enabled. Schema `$id` values are stable identifiers,
not a requirement to fetch schemas over the network. A consumer conformance test should validate
the valid fixtures, reject every `invalid-*` fixture, and assert that its pinned
capture version occurs in the live capability response with the required
operation marked ready before submitting a capture.

The published fixture corpus is intentionally portable: it covers a YouTube
manifest with reference-only video custody, trusted playback, partial client
completion, renderer-specific progress, semantic commands, standardized errors,
and strict rejection of unknown fields, invalid discriminator combinations, raw
embed HTML, unnamespaced provider extensions, and any attempt to put conversation
messages into FE's Reader context.
