# Browser Capture and Reader Execution Plan

**Date:** 2026-09-03

**Project:** Fragments Engine (`PRJ-20260514-0004`)

**Torque plan:** `CW-20260903-0029`

**Specification:**
[`2026-09-03-browser-capture-reader.md`](../specs/2026-09-03-browser-capture-reader.md)

**Architecture:**
[`browser-capture-reader-architecture.md`](../../browser-capture-reader-architecture.md)

## Execution model

Torque is the live execution graph. Every child task is manual until the
orchestrator deliberately promotes a dependency-ready task. Execute by dependency
eligibility, using the waves as integration gates rather than a mandate to
serialize unrelated tasks.

The FE integration checkout is
`/Users/chrispian/dev/hollis-labs/apps/fragments-engine`. Concurrent implementation
tasks use isolated worktrees and disjoint ownership. Schema sources, generated
artifacts, migrations, shared domain interfaces, and integration state are
serialized.

The parallel clipper plan is `CW-20260903-0030` in project
`PRJ-20260903-0001`. Cross-project dependencies below are deliberate and narrow.

## Wave 1 — Contracts and durable domain

Gate: FE publishes validated contracts and persists stable fragments, immutable
revisions, additive capture context, and media custody primitives.

| Task | Purpose | Depends on |
|---|---|---|
| `CW-20260903-0031` | Publish capture, media, Reader, playback, progress, command, error, and capability contracts | — |
| `CW-20260903-0033` | Add stable fragment identity and immutable revisions across every ingest | `0031` |
| `CW-20260903-0034` | Persist capture attempts, append-only annotations, additive tags, and optimistic curated notes | `0033` |
| `CW-20260903-0035` | Add media assets, variants, ordered revision refs, custody, and content-addressed blobs | `0033` |

`CW-20260903-0031` is the first cross-project handoff. Once its generated
artifacts are integrated and green, clipper task `CW-20260903-0048` is eligible.

## Wave 2 — Capture and provider services

Gate: manifest-first capture, independent asset transfer, capability-aware
enrichment, provider completion, trusted playback, and legacy compatibility work
through FE services and thin transports.

| Task | Purpose | Depends on |
|---|---|---|
| `CW-20260903-0036` | Implement atomic manifest acceptance, optimistic response, per-asset transfer, lookup, and completion | `0031`, `0033`, `0034`, `0035` |
| `CW-20260903-0037` | Add per-capability enrichment and narrow provider adapter contracts | `0031`, `0033`, `0034`, `0035` |
| `CW-20260903-0039` | Complete Pinterest and Instagram identity/metadata/media capabilities | `0036`, `0037` |
| `CW-20260903-0038` | Add YouTube metadata, transcript, trusted playback, and optional custody seam | `0036`, `0037` |
| `CW-20260903-0040` | Adapt legacy `/v1/intake` and existing ingests to shared stable capture semantics | `0036`, `0037` |

`CW-20260903-0036` unlocks clipper durable manifest task
`CW-20260903-0049`. Tasks `0038`, `0039`, and `0040` are required by clipper
conformance task `CW-20260903-0056`.

## Wave 3 — Reader service layer

Gate: batched Reader projections, sanitized content/media resources, semantic
commands, and principal-scoped reading progress are contract-tested.

| Task | Purpose | Depends on |
|---|---|---|
| `CW-20260903-0041` | Build ReaderItem list/detail projections and inbox/library/all query scopes | `0036`, `0037` |
| `CW-20260903-0042` | Add independent reading state and idempotent semantic Reader commands | `0034`, `0036` |
| `CW-20260903-0043` | Serve sanitized article, authorized media, transcript, and trusted playback resources | `0035`, `0036`, `0038` |

The projection and command tasks may run concurrently after their dependencies.
Reader projections must represent provider work still in progress; they do not
wait for every provider adapter to finish before becoming useful.

## Wave 4 — Reader interface

Gate: Sysop provides addressable Reader scopes/details, media-aware renderers,
fullscreen video, and optimistic quick actions.

| Task | Purpose | Depends on |
|---|---|---|
| `CW-20260903-0045` | Build Reader routes, scopes, cards, and deep-linked overlay/detail navigation | `0041` |
| `CW-20260903-0046` | Implement article, image, ordered gallery, video, and fallback renderers | `0041`, `0043` |
| `CW-20260903-0047` | Add optimistic quick actions and renderer-specific consumption progress | `0042`, `0045`, `0046` |

## Wave 5 — Cross-project acceptance

Gate: the architecture's validation scenarios pass end to end and the FE branch
is reviewable with complete verification evidence.

| Task | Purpose | Depends on |
|---|---|---|
| `CW-20260903-0058` | Run cross-project Pinterest, Instagram, YouTube, article, idempotency, progress, and chat-seam acceptance | `0038`, `0039`, `0040`, `0047`, clipper `0056` |
| `CW-20260903-0059` | Final architecture review, full verification, documentation, and release/PR handoff | `0058` |

## Cross-project coordination

- FE owns schemas. The clipper must not create a competing protocol definition.
- Publish or regenerate contract artifacts in the same FE task that changes their
  source schema.
- Contract-breaking changes after clipper consumption begins require explicit
  coordination and task/dependency updates in both plans.
- Browser/provider fixture findings belong to the repository that owns the
  adapter. FE-side resolution, persistence, projection, or media-serving findings
  remain in FE.
- The clipper may finish its final review independently after shared conformance;
  FE owns the combined Reader acceptance gate.

## Task completion protocol

For each Torque task:

1. Verify every dependency is integrated and green.
2. Read the full task, required subtodos, specification, and relevant architecture
   sections.
3. Inspect current code and tests before choosing implementation details.
4. Work in an isolated task worktree/branch when another write task is active.
5. Implement focused tests with the behavior, including migrations and generated
   artifacts where applicable.
6. Run task-specific gates and the affected package regression suite.
7. Review the complete diff and rerun important verification from the integration
   checkout.
8. Integrate dependencies before dependents.
9. Attach commit, commands/results, public contract changes, migrations, risks,
   and skipped checks to Torque; complete required subtodos only with evidence.
10. Run the complete wave gate before promoting work whose correctness depends on
    that wave.

## Stop and escalation conditions

Continue through all eligible work. A blocked task does not pause disjoint work.
Escalate when evidence would require changing a specification invariant, custody
default, public contract ownership, repository boundary, destructive migration,
exit criterion, or the separate disposition decision.

New in-scope work becomes an explicit child task with dependencies. Useful
out-of-scope work becomes a separate Torque issue/decision in the correct
project. Do not silently expand an existing task.

## Separate decision

`CW-20260903-0060` owns inbox disposition and organization semantics. It is not a
dependency of this plan and must not be implemented opportunistically.
