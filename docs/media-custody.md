# Media custody and compatibility

Fragments Engine stores three independent media concepts:

- `media_assets` identifies a logical provider/source object. Identity uses the
  source registration and provider, then prefers provider media ID, a stable
  position-independent source locator, and finally the source/client media key.
  Media kind and gallery position are observations and never enter identity.
- `asset_variants` identifies a representation such as an original, preview,
  thumbnail, poster, audio track, subtitles, or transcript. Custody and
  acquisition state are independent per variant, so a partially acquired
  gallery remains useful.
- `attachment_refs` places a logical asset at a zero-based position in one
  immutable fragment revision. Replacing a legacy attachment manifest creates
  a new fragment revision; refs on prior revisions are never updated or
  deleted.

Provider completion can supply a provider media ID after a browser observation
was keyed by locator. FE retains the original media asset ID, records the
provider identity as an alias, and keeps every observed source key/locator in
`media_asset_source_observations`. Variant source URLs and expiry hints are
append-only `asset_variant_source_observations`; the variant row carries the
latest usable locator without erasing earlier provenance.

## Blob custody

The filesystem blob store accepts a stream, calculates SHA-256 while writing a
private temporary file, verifies any expected digest, and atomically hard-links
verified bytes into `sha256/<first-two-hex>/<digest>`. Storage handles are
validated and cannot escape the configured root. Identical bytes converge on
one file across writers, variants, and fragments.

Mirrored and adopted bytes have `indefinite` retention by default. Reusing a
cached blob from a mirrored or adopted variant upgrades the shared blob record
to indefinite retention. A failed expected-digest check creates neither a blob
row nor a durable file. Variant acquisition holds a SQLite write reservation
across final blob installation and the DB write; if DB attachment or commit
fails, a file newly installed by that transaction is removed before rollback.

## Legacy projection

Migration `013_media_assets.sql` leaves `attachments` and
`fragment_attachments` intact. Existing ingest writes dual-write those tables
and the new model, so fragment detail, preview selection, routing publication,
and `/v1/fragments/attachment` keep their established behavior.

On first open, FE backfills legacy relationships after the stable-identity
backfill. It maps an alias relationship to the revision whose
`fragment_revisions.legacy_fragment_id` names that legacy row. If multiple old
fragment rows collapse to the same exact revision, the revision's selected
`legacy_fragment_id` supplies the single deterministic attachment snapshot.
Distinct historical revisions retain distinct refs.

Old relationships did not store position. Their deterministic fallback order
is `created_at`, attachment name, attachment ID, role, source, and source item
ID; FE assigns contiguous zero-based positions in that order. Existing local
storage and preview paths become available adopted variants while remaining in
the legacy tables for compatibility. External-only attachments become
reference-only variants. When the legacy publisher later writes a local storage
path, the compatibility variant advances to adopted/available with indefinite
retention. Regenerated preview paths replace the current compatibility serving
path; they are publication projections, not source locators or immutable
revision material.
