# Sysop

`apps/sysop` is a vendored copy of Torque's GUI from:

- Source: `/Users/chrispian/dev/hollis-labs/apps/torque/apps/gui`

This copy is intentionally trimmed for Fragments Engine Phase 1. Only the inbox route is mounted, and upstream pages and controls that are out of scope have been removed so future Torque sync diffs stay easy to review.

## Local commands

```bash
make sysop-build
make sysop-dev
make sysop-clean
```

## Build flow

`apps/sysop/dist` is embedded into the Fragments Engine Go binary at compile time. The normal repo build now runs the sysop bundle first:

```bash
make build
```

From a clean checkout, or after deleting frontend artifacts, the end-to-end flow is:

```bash
make sysop-build
make build
./fragments-engine serve-api
```

`make sysop-build` installs `apps/sysop/node_modules` when needed, then runs the Vite production build. `make sysop-clean` removes both `apps/sysop/dist` and `apps/sysop/node_modules`.

## Updating from upstream Torque

1. Replace `apps/sysop/` from `apps/torque/apps/gui/`, excluding `node_modules` and `dist`.
2. Re-apply the Sysop-specific edits:
   - package metadata (`name`, `description`)
   - `src/App.tsx` inbox-only routing
   - `src/pages/InboxPage.tsx`
   - product naming and env vars (`SYSOP_API_ORIGIN`)
   - this README
3. Delete Phase 1-excluded pages and controls again before building.
4. Run `make sysop-build`.
5. Commit the resync as a single vendor-style commit.
