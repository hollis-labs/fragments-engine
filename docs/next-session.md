# Fragments Engine — Next Session Handoff

## Current State

Fragments Engine is now operating as the local-first inbox, recall layer, and
first-pass virtual filesystem for saved information.

Recent work moved FE beyond ingestion and triage into an FFS-backed browsing
workflow:

- reviewed Pinterest pins are downloaded, previewed, searchable, editable, and
  materialized under `~/Documents/ffs/media/pins`
- GitHub repo saves sync into Stack Explorer with tags and scan queueing
- file destinations support `provider` and `path_template`
- `route materialize` writes through destinations without removing fragments
  from inbox
- manual fragments can be saved directly to FFS from sysop using `Save to FFS`
- `note`, `quote`, `report`, `pin`, and `reference` source types map to default
  FFS destinations
- sysop has a top-level `Library` page with a folder-like virtual browser over
  processed fragments
- the fragment modal exposes copy/open controls for paths, URLs, output refs,
  and attachments, and now shows image previews near the top

The live dev service is managed by Cerberus as `fragments-engine-dev` on
`http://127.0.0.1:8091/sysop/`.

## Latest Local Commits

The current local `main` includes these recent commits:

- `5d1c5a8` `Refine library folder browser layout`
- `6fb57cd` `Add library file browser for fragments`
- `ebfd484` `Add FFS save flow for notes quotes and reports`
- `7738167` `Refine ffs reference path naming`
- `dfeb8f2` `Expand ffs routing and route controls`

At handoff time the FE worktree was clean.

## Live FFS Layout

The chosen filesystem root is:

```text
~/Documents/ffs/
```

The intended namespace is:

```text
docs/
  notes/
  quotes/
  reports/
  references/
media/
  pins/
  images/
  videos/
  audio/
artifacts/
  exports/
  captures/
  source-files/
  downloads/
views/
  projects/
  topics/
  people/
inbox/
  manual/
  imports/
```

Current live destinations created in the runtime DB:

- `ffs-pins`
- `ffs-references`
- `ffs-notes`
- `ffs-quotes`
- `ffs-reports`

Current behavior:

- `ffs-pins` uses the FFS Pinterest bundle writer
- generic file destinations use `path_template`
- materialization keeps fragments in inbox and marks the inbox reason with
  `materialized to <destination>`
- Library derives virtual folder paths from fragment type and materialization
  state, not by scanning the filesystem

## Verification From This Session

Passed locally:

```bash
go test ./...
npm --prefix apps/sysop run typecheck
npm --prefix apps/sysop run build
make build
```

Cerberus reload succeeded:

```bash
/Users/chrispian/go/bin/cerberus resource reload fragments-engine-dev
```

Live smoke checks passed:

- `/v1/fragments/browse?limit=5` returns browse rows
- sysop serves the updated embedded bundle
- `Save to FFS` created a real note bundle under
  `~/Documents/ffs/docs/notes/ffs-note-flow-smoke-test/fragment.md`

## Recommended Pickup

The next session should continue from the Library/FFS product surface.

Priority order:

1. Make Library folder semantics more durable by deriving virtual paths from
   actual FFS output refs when available, falling back to source type only when
   not materialized.
2. Add drill-down affordances for folder counts, breadcrumbs, and back/up
   navigation polish.
3. Add saved views for `docs/notes`, `docs/quotes`, `docs/reports`,
   `docs/references`, and `media/pins`.
4. Add bulk materialization controls from Library for filtered sets that are not
   yet saved.
5. Add filesystem backfill/repair tooling for FFS bundles so output paths can be
   regenerated or cleaned without changing user metadata.

## Known Gaps

- Library currently presents a virtual filesystem; it does not yet read FFS dirs
  from disk.
- Browse rows do not yet include output refs directly; the modal gets them from
  route log detail.
- Folder paths are currently inferred in the sysop frontend from source type and
  materialized state.
- `docs/notes`, `docs/quotes`, and `docs/reports` workflows are usable, but they
  still rely on manual source-type selection.
- Chunk splitting/code-splitting is still a frontend follow-up; Vite reports the
  main bundle above 500 kB.

## Operational Notes

- Run the dev API against gitignored `fragments.yaml`, not
  `fragments.example.yaml`.
- `fragments.yaml` currently points Pinterest corpus output at
  `~/Documents/ffs/media/pins`.
- The runtime DB owns destinations and routes; they are not declared in YAML.
- `fragments.example.yaml` is a commented template and should not be used as the
  mutable runtime config.

