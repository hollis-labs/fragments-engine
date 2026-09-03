# Fragments Engine Content Intake and Fragment Operations Direction

**Status:** Directional architecture draft

**Date:** 2026-08-22
**Scope:** Desired product boundaries and architecture; not an implementation plan

## Purpose

This document describes the intended place of Fragments Engine in the Hollis
Labs portfolio. It evaluates source ingestion, normalization, provenance,
attachments, enrichment, triage, routing, delivery, FFS materialization,
recall, credentials, persistence, observability, and extensions against the
portfolio's shared engineering boundaries.

It deliberately does not define phases, estimates, task breakdowns, migration
steps, or compatibility sequencing. Existing documentation and code remain the
current implementation truth until explicitly superseded. This document
defines a target direction for a later architecture and planning session.

## Portfolio axiom

> Hollis tools own execution and operational state, but not the business
> definitions or business data they operate on.

For Fragments Engine, this requires a precise distinction between semantic
authority and operational custody:

- Source applications and publishers own the meaning and current value of
  material imported from them.
- Users and projects own the meaning, sensitivity, acceptance, and disposition
  of material captured directly through Fragments Engine.
- Publishers own ingest definitions, classification policy, routing rules,
  materialization formats, and other authored content-processing logic.
- Credential authorities own provider secrets and their lifecycle.
- Destination systems own the data and processing state created after an
  accepted delivery.
- Fragments Engine owns the durable operational record of content deliberately
  placed under its care: what it observed or accepted, the immutable source
  snapshot, normalization and enrichment observations, provenance, triage
  decisions, route evaluation, delivery attempts, materialization references,
  domain search projections, and audit history.

Retaining a normalized content body does not make Fragments Engine the current
authority for the external source. Its record is canonical for what Fragments
Engine observed or adopted at a particular revision, not for what a source URL,
repository, chat application, Nil vault, or destination says now.

For a Fragments Engine-native note or capture, Fragments Engine may be the
custodial system of record while the user or project remains the semantic
authority. The distinction still matters for authorization, publication,
retention, and automated changes.

## Product definition

Fragments Engine is a local-first content intake, normalization, triage,
routing, and materialization control plane for fragment-sized information. It:

1. Acquires or accepts material through registered source connectors and
   capture clients.
2. Preserves immutable source observations with explicit provenance and
   custody policy.
3. Normalizes observations into stable fragment identities and revisions.
4. Attaches deterministic and model-produced enrichment as attributed
   observations rather than silently rewriting source truth.
5. Maintains a human-operable inbox for unresolved content decisions.
6. Evaluates publisher-owned route policy against a pinned fragment revision.
7. Delivers or materializes content through registered destination adapters
   with durable attempts, retries, receipts, and audit.
8. Provides domain-local search and relationship views over the fragment
   catalog.
9. Exposes the same semantics through GUI, CLI, HTTP, MCP, events, and narrow
   integration contracts.

Fragments Engine is not a general memory or knowledge authority, content
generation engine, workflow language, task manager, agent runtime, general
message broker, secret manager, infrastructure control plane, source
repository, or universal binary store.

The useful shorthand is:

> Fragments Engine turns captured or observed material into governed,
> traceable content operations.

## Architecture sketch

```text
Source systems and capture clients
URLs · files · chats · repositories · Nil · browser clipper · manual intake
                           |
                           | SourceObservation / NativeCapture
                           v
              +-------------------------------+
              |       Fragments Engine        |
              |                               |
              | source + provenance catalog   |
              | fragment identities/revisions |
              | enrichment observations       |
              | triage cases + decisions      |
              | route evaluation              |
              | delivery/materialization log  |
              | domain-local recall views     |
              +---------------+---------------+
                              |
                    immutable DeliveryBinding
                              |
          +-------------------+-------------------+
          |                   |                   |
          v                   v                   v
    FFS projection      peer application     Loom compilation
    or file export      API/MCP/CLI/Tether   request by reference
          |                   |                   |
          +-------------------+-------------------+
                              |
                    receipt / outcome reference

Tesseract ----- optional pointer/index/promotion ---- domain learning
Hadron -------- optional complex pipeline run ------- execution handle
Cerberus ------ deployment and attached resources --- no content semantics
```

The acquisition path records source truth as an observation. The delivery path
binds a specific fragment revision and route revision. Neither path transfers
semantic authority implicitly.

## Responsibility boundaries

| Concern | Authoritative owner | Fragments Engine responsibility |
|---|---|---|
| External source content | Source application or publisher | Preserve source reference, observed revision, digest, and acquisition facts |
| Native capture content | User or project | Provide custodial record, revisions, policy, and retrieval |
| Fragment identity and provenance | Fragments Engine | Maintain stable identity, immutable revisions, lineage, and custody mode |
| Source and ingest definition | Publisher or operator | Validate, register, resolve, snapshot, and execute |
| Extraction or enrichment definition | Publisher or operator | Bind definition revision and record attributed outputs |
| Triage policy and decision | Publisher and authorized reviewer | Stage cases, present evidence, authenticate decisions, and apply them |
| Route definition | Publisher or operator | Compile and evaluate a pinned definition |
| Delivery attempt | Fragments Engine | Queue, retry, correlate, and record receipt or failure |
| Destination-native processing | Destination application | Store normalized handle and reported outcome |
| FFS files | Declared per root | Materialize or ingest according to one explicit authority mode |
| Generated pages and drafts | Loom or other output system | Deliver source references and retain outcome references |
| Managed work | Torque | Optionally create or reference work through an adapter |
| Complex workflow execution | Hadron | Invoke a pinned workflow and correlate its run |
| Agent session | Nanite, Tether, Torque, or Hadron caller | Deliver a request or source reference; never infer session ownership |
| General messages and federation | Tether when composed | Own fragment routing meaning and use a delivery adapter |
| Portfolio memory and knowledge | Tesseract namespace authority | Maintain local fragment recall; explicitly project or promote across the boundary |
| Infrastructure and local service | Cerberus and the OS | Expose health/readiness and consume attached resources |
| Provider secrets | External credential authority | Carry references and consume narrow runtime grants |

## Authority and custody model

Every fragment revision needs an explicit authority mode. One undifferentiated
`canonical` claim is insufficient.

### External observation

An external observation is an immutable snapshot of material owned elsewhere:

```text
ExternalObservation
  source authority and connector
  source item identity
  source revision or observed version
  locator
  acquired and source-modified times
  raw or normalized digest
  observation payload or content reference
  acquisition policy and custody class
```

Fragments Engine may retain the content required for local recall and routing,
but refresh creates a new observation. It does not overwrite history or imply
that the prior observation was false.

### Native or adopted capture

A native capture is deliberately authored into or adopted by Fragments
Engine. It has a user or project authority and a Fragments Engine custodial
record. Edits create content revisions with actor and reason.

Adoption is explicit. Importing a URL or reading a peer database does not
silently convert external content into Fragments Engine-owned content.

### Derived observation

Summaries, extracted entities, OCR text, vision analysis, relationship hints,
classification scores, and generated metadata are derived observations. Each
records:

- producer and version
- definition or prompt revision
- input fragment revision and attachment digests
- deterministic or model-produced classification
- model and provider disclosure where applicable
- confidence and explanation
- creation and expiry or invalidation policy

Derived observations never replace the source snapshot. A later processor run
creates another observation or explicitly supersedes a prior observation under
its own policy.

### Projection or materialization

An FTS row, embedding, thumbnail, FFS bundle, rendered Markdown file, cached
remote body, or destination payload is a projection unless a declared custody
policy says otherwise. Rebuildability, retention, and backup requirements are
recorded rather than inferred from file location.

### Delivery fact

Fragments Engine is authoritative for what it attempted and what response it
observed. An HTTP `2xx`, MCP result, CLI exit code, or file write proves only
the adapter's delivery contract. It does not prove that Loom compiled a good
page, Nanite completed an agent turn, Torque accepted work, or a human reviewed
the destination record.

## Core domain model

### Source registration

A source connector is code; a source registration is configured use of that
code:

```text
SourceConnector
  contract and version
  supported source kinds
  discovery and acquisition capabilities
  required effects and credential references
  checkpoint and incremental-sync semantics

SourceRegistration
  identity and owning scope
  connector reference
  source authority and endpoint/root
  segmentation and normalization policy references
  retention and sensitivity policy
  activation policy reference
  non-secret configuration
  credential references
  revision, digest, and provenance
```

Source definitions remain publisher-owned inputs. Fragments Engine may provide
a native registry and editing surface, but every acquisition binds the exact
registration revision it used.

The source model supports pull connectors, push intake, filesystem observation,
event subscription, export import, and manual capture without pretending those
mechanisms have identical freshness or authority guarantees.

### Acquisition run and source item

An acquisition run is an operational execution record:

```text
AcquisitionRun
  source registration revision
  trigger and actor
  checkpoint before and after
  start, finish, and lifecycle
  counts and item outcomes
  errors and retry disposition
  causation and correlation
```

Each acquired item has its own outcome. One malformed item need not erase the
facts for successfully observed siblings, and a run-level `done` status must
not imply every item was accepted or enriched.

### Fragment and fragment revision

A fragment has stable identity independent of content changes:

```text
Fragment
  stable identity and owning scope
  authority and custody mode
  source-item lineage
  current accepted revision pointer
  sensitivity and retention policy
  lifecycle

FragmentRevision
  immutable revision identity
  normalized content and digest
  title and media type
  source observation references
  segmentation definition revision
  author or producer
  created and observed times
  supersession or derivation relationship
```

Content hash is a revision identity input and exact-dedup signal, not the sole
stable fragment identity. A changed source item produces a related revision,
not an unrelated fragment with no lineage. Near-duplicate detection proposes a
relationship or merge; it does not silently discard content.

Normalization and segmentation are versioned. A later connector deciding to
split a conversation differently must remain explainable and must not make old
fragment references meaningless.

### Attachments and blobs

An attachment is a relationship from a fragment revision to content or a
locator:

```text
AttachmentRef
  identity, role, and media type
  source authority and locator
  immutable digest or source revision
  custody mode: reference | cache | mirror | adopted
  size and verification observations
  retention and sensitivity policy
  optional blob handle and derived previews
```

`source_path` alone is not durable provenance. A local path requires an
authority, observation time, digest, access policy, and missing-source
behavior. Mirrored or adopted blobs belong in an explicit content-addressed
blob resource with backup and garbage-collection policy.

OCR, extracted text, thumbnails, and vision summaries are derived records,
not mutations of the attachment's source identity.

### Enrichment assertions

Entities, tags, summaries, directives, classifications, and durable relations
share an attributed assertion core:

```text
Assertion
  subject fragment revision
  typed value
  producer and method
  definition/model version
  confidence and explanation
  asserted, observed, and expiry times
  status: proposed | accepted | rejected | superseded
```

Human tags and verified relations are distinguishable from deterministic
extractor output and model suggestions. Retrieval-time similarity remains a
query result and is not promoted into a durable relation without an explicit
write decision.

### Triage case and decision

The inbox is a view over unresolved `TriageCase` records, not a fragment
status:

```text
TriageCase
  identity and reason
  fragment revision
  requested decision schema
  suggested routes and evidence
  eligible reviewers
  lifecycle and due policy

TriageDecision
  authenticated actor
  selected disposition or routes
  annotations and corrections
  policy applied
  decided time
```

A fragment may have more than one triage concern: source verification,
sensitivity review, enrichment failure, route ambiguity, publication approval,
or attachment handling. Resolving one concern does not erase the others.

### Route and destination registration

Routes and destinations are authored definitions, while evaluation and
delivery are operational facts:

```text
RouteDefinition
  publisher and revision
  typed predicate
  route mode: exclusive | fan_out | advisory
  target purposes
  minimum evidence and review policy

DestinationRegistration
  adapter reference and version
  address and capabilities
  payload contract revision
  delivery, idempotency, and receipt policy
  effect grant and credential references
  non-secret configuration
```

Transport and application semantics are separate. HTTP, MCP, CLI, and file are
transport capabilities. Nil inbox, Nanite messaging, Loom compilation, FFS,
and Stack Explorer synchronization are adapter or profile semantics built on
those transports. Application-specific request shapes do not belong in the
core fragment model.

### Delivery binding and attempt

Every selected delivery is pinned:

```text
DeliveryBinding
  fragment identity + immutable revision
  route identity + immutable revision
  destination registration + immutable revision
  purpose and payload schema
  rendered payload digest or content reference
  effect and credential references
  idempotency key
  actor, causation, and correlation

DeliveryAttempt
  binding identity
  queue claim and fencing token
  start, finish, and lifecycle
  adapter-native handle
  receipt and outcome references
  failure class and retry decision
```

Deleting or editing a current route cannot make historical delivery records
uninterpretable. The immutable binding preserves the relevant definitions.

### Materialization record

A materialization is a specialized delivery whose output can be inspected and
reconciled:

```text
Materialization
  binding identity
  target root and authority mode
  rendered file manifest
  content and attachment digests
  write receipt
  source fragment revision
  reconciliation and disposition policy
```

Materialization does not automatically resolve triage or mark a fragment
delivered for every other purpose.

## Lifecycle separation

Fragments Engine needs independent state machines rather than one fragment
status with values such as `inbox`, `routed`, and `indexed`.

### Fragment custody lifecycle

```text
Observed / Captured -> Active -> Superseded / Withdrawn
                               -> Archived -> Deleted by policy
```

This answers whether a fragment revision remains part of the governed catalog.

### Acquisition lifecycle

```text
Planned -> Queued -> Running -> Succeeded
                         |----> PartiallySucceeded
                         |----> Failed / Canceled / Lost
```

### Enrichment lifecycle

Each enrichment job and assertion has its own pending, running, produced,
accepted, rejected, stale, and failed states. A fragment can remain fully
searchable while one optional enrichment is unavailable.

### Triage lifecycle

```text
Open -> Proposed -> Decided -> Applied -> Closed
  |         |          |
  +------> Deferred / Expired / Canceled
```

### Route evaluation lifecycle

A route evaluation records matched, not matched, review required, suppressed,
or invalid policy. It does not reuse delivery state.

### Delivery lifecycle

```text
Planned -> Queued -> Leased -> Delivering -> ReceiptAccepted
                           |              |-> ReceiptRejected
                           |              |-> Failed / TimedOut
                           +---------------> Canceled / Lost / DeadLettered
```

Downstream completion, when available, is a separate correlated observation.

### Index and materialization lifecycle

Index projections and materializations independently report pending, current,
stale, failed, and removed. `Indexed` is not a content disposition; `Routed` is
not proof that every intended target received the fragment.

## Ingestion and pipeline execution

The desired logical pipeline is:

```text
acquire
  -> verify and snapshot source
  -> normalize and segment
  -> commit fragment revision + provenance
  -> emit durable content event
  -> deterministic enrichment
  -> optional asynchronous enrichment
  -> open or update triage cases
  -> evaluate route definitions
  -> create durable delivery bindings
  -> dispatch and materialize asynchronously
  -> update recall projections
```

The ordering is a dependency graph, not a requirement that one synchronous
request perform every effect. Captured content and provenance commit before
external delivery. Optional model or destination failures do not roll back a
valid source observation.

Each stage declares its input schema, output schema, effects, idempotency,
retry behavior, and definition revision. Reprocessing a fragment revision is
safe and produces attributable new observations.

Material domain changes and their outbox events share a transaction. Workers
claim jobs durably and use fencing so a stale worker cannot overwrite a later
decision. Reconciliation can prove whether every committed intent has a job or
terminal disposition.

### Activation and schedules

Simple recurring acquisition is part of Fragments Engine's operational domain.
A schedule is an activation registration bound to a source-registration
revision, not the source definition itself.

Hadron is the preferred executor when acquisition becomes a reusable,
multi-step workflow with branching, waits, gates, or several applications.
Fragments Engine then binds a pinned Hadron definition and correlates its run;
it does not grow a second general workflow language.

Push intake and event activation remain peer mechanisms. A polling schedule,
webhook, browser clipper request, and manual capture all record distinct trigger
provenance.

## Classification and model use

Deterministic processing remains the default:

1. source facts and explicit publisher rules
2. deterministic extractors and patterns
3. trained statistical classifiers
4. model-produced suggestions
5. authenticated human decision where policy requires it

Confidence belongs to a specific assertion, classifier, and definition
revision. There is no universal fragment confidence that safely governs every
route.

Human corrections are valuable training evidence, but a triage decision does
not silently retrain a classifier. A training policy chooses eligible decisions,
records the resulting sample set, and produces a versioned model artifact with
evaluation and rollback provenance.

Model outputs are untrusted derived observations. Prompts, model IDs, provider
capabilities, input digests, latency, cost, and confidence are disclosed. A
model can propose tags, summaries, relationships, or routes; it receives
decision authority only through an explicit policy.

Local processing and deterministic fallbacks preserve useful ingestion and
search when a model provider is absent.

## Routing and delivery

Route evaluation returns an explicit set of matches. It supports:

- exclusive routing with a deterministic conflict rule
- fan-out to several independent purposes or destinations
- advisory suggestions requiring a triage decision
- suppression with a recorded policy reason

Evaluation never depends on database row order. Priority, exclusivity, and
conflict behavior are part of the route definition.

External delivery is asynchronous after commit by default. Fast local writes
may be optimized internally, but the semantic contract remains a durable
binding and attempt rather than inline I/O deciding whether ingestion exists.

Retries are at-least-once. The idempotency key includes fragment revision,
route revision, destination revision, and purpose. A retry never broadens
credentials, changes payload policy, or silently binds current mutable config.

Generic transport adapters may exist, but arbitrary command execution, HTTP
templates, MCP tools, filesystem writes, and environment injection are effectful
capabilities. They require operator-authorized registrations and narrow grants;
an ordinary captured fragment cannot select or configure them.

Official APIs, SDKs, and CLIs are preferred inside adapters. A custom protocol
is justified only when an official contract cannot meet the product need.

### Callbacks and receipts

`callback` is not fundamentally a fifth transport beside HTTP. It describes
queued dispatch and completion semantics over a transport. The target model
separates:

```text
transport: http | mcp | cli | file | message | plugin
dispatch: queued | synchronous-admin
receipt: accepted | completed | none
outcome: optional correlated callback or event
```

The Loom pilot's callback should remain a minimal wake or work-request adapter.
It carries a fragment revision reference, purpose, correlation, and idempotency
key. It does not carry template logic or cause Fragments Engine to interpret
Loom's generator vocabulary.

A delivery receipt answers whether Nanite accepted the wake request. Loom's
compile job and page quality remain Loom state. If Fragments Engine needs to
display that outcome, it stores a normalized `OutcomeRef`; it does not assume
ownership of the compile lifecycle.

### Directives

Directives found inside content are parsed assertions. They are inert data in
Fragments Engine. A `::draft`, `::log-adr`, or future command can influence
routing only through authorized policy; it cannot grant itself tools,
credentials, filesystem access, or execution authority.

Directive execution belongs to the application that owns the requested
business behavior, such as Loom. The parser can remain shared and deterministic
while handlers remain outside Fragments Engine.

## FFS and file materialization

FFS is a portable, human-readable materialization of selected content. It is
not automatically a second canonical database or a general virtual filesystem.

Each materialization root has exactly one authority mode:

```text
managed_projection
  Fragments Engine is the write path. Files are reproducible outputs with a
  manifest. Human edits are not silently imported.

external_file_authority
  Files are the source. Fragments Engine observes them through a source
  connector and never overwrites them as projections.
```

The recommended default for current FFS bundles is `managed_projection`:
Fragments Engine remains the operational catalog for provenance, triage, and
materialization state; FFS remains durable, inspectable, and portable output.
Edits occur through a write-through Fragments Engine command or return as a new
source observation under an explicit round-trip policy.

Every bundle carries a manifest with fragment revision, renderer revision,
content digest, attachment digests, and generated paths. Repair or rebuild can
prove drift before replacing output. User-created files outside managed paths
are never treated as garbage.

Materialized output references are first-class records. Frontend path inference
is a view convenience, not the source of folder truth.

## Recall and Tesseract boundary

Fragments Engine needs domain-local recall because search, related fragments,
entity browsing, inbox review, and route preview are core content operations.
That read model remains available with local deterministic indexing.

Tesseract is the portfolio's authority-aware memory and knowledge substrate.
Fragments Engine must not become a competing general memory namespace system,
and embedded Tesseract must not create a silent second canonical copy of every
fragment.

The composition contract is:

- Fragments Engine owns fragment identities, revisions, provenance, triage,
  and current content-operation state.
- SQLite FTS or another local index is a rebuildable Fragments Engine read
  model.
- Tesseract may serve as an embedded or attached retrieval projection only
  under an explicit store-authority and namespace policy.
- Projection records identify the Fragments Engine revision and digest and are
  not presented as source-authoritative knowledge.
- Cross-portfolio discovery favors pointer-backed Tesseract knowledge that
  resolves to Fragments Engine.
- Copying content into a user or project knowledge namespace is an explicit
  write or promotion governed by that namespace's authority—not an automatic
  side effect of ingestion.

Search results disclose backend, projection generation, query strategy, source
revision, and fallback. Retrieval-time similarity does not become a durable
relationship merely because it scored highly once.

## Loom boundary

Fragments Engine and Loom have a clean content boundary:

```text
Fragments Engine: What source material was captured, how was it normalized,
                  what needs review, and where should it be delivered?

Loom:             Given selected source revisions and an authored compilation
                  definition, what generated content should be produced and
                  maintained?
```

Fragments Engine owns raw/adopted fragment operations and delivery evidence.
Loom owns templates, lenses, compilation policy, generated pages and drafts,
their lifecycle, output provenance, and content-quality decisions.

The seam is reference-first. Loom receives immutable fragment revision
references and fetches authorized content when needed. It returns a compile job
or output reference. Fragments Engine never stores Loom templates or decides
whether a generated page is good enough.

Generated Loom pages can later enter Fragments Engine only as an explicit Loom
source registration if there is a real intake use case. That is a new source
observation, not a hidden feedback loop.

## Connector, adapter, and plugin model

The Hollis Labs plugin SDK supplies common discovery, loading, lifecycle,
configuration, health, diagnostics, permissions, and compatibility mechanics.
Fragments Engine defines narrow domain contracts above it.

Useful extension classes include:

- source connector
- normalizer or segmenter
- attachment extractor
- deterministic or model enrichment provider
- classifier and training provider
- route predicate
- destination adapter
- blob or attachment store
- recall projection backend
- source verifier
- materialization renderer
- policy evaluator
- audit or event sink
- UI projection

Each extension declares:

- contract and version
- supported schemas and media types
- deterministic or model-produced behavior
- effects and idempotency
- filesystem, network, process, and credential requirements
- custody and retention implications
- health and readiness behavior
- concurrency and cancellation behavior

Plugins receive scoped services and content capabilities, not unrestricted SQL
or the entire filesystem. Registration is transactional. Durable observations
and delivery bindings preserve the extension identity and version required to
interpret them later.

App-specific integrations such as Nil vault import, Nanite messaging, Loom
wake, FFS rendering, and Stack Explorer sync become adapters or registered
profiles. They may ship as first-party extensions without becoming special
cases in the core fragment schema.

Direct reading of a peer application's SQLite database is a compatibility
adapter, not a preferred integration contract. It is read-only, pins the peer
schema revision it understands, validates identity and freshness, and fails
closed on incompatible schema. Official API, export, SDK, or CLI contracts are
preferred when they can serve standalone use.

## Configuration and definition ownership

Fragments Engine separates three things currently mixed across YAML and
database rows:

```text
deployment configuration
  ports, store locators, enabled runtime modules, credential references

publisher definitions
  source registrations, enrichment policy, routes, destinations, renderers

operational state
  runs, observations, fragments, cases, attempts, receipts, projections
```

Deployment configuration is external and immutable for a release. APIs do not
rewrite the process's YAML file.

Publisher definitions may come from files, a registry, API calls, or plugins.
Fragments Engine validates and registers them with owner, revision, digest, and
provenance. A deployment chooses which authority wins when the same logical
definition is supplied more than once.

Operational rows never become accidental configuration. Effective definitions
are compiled per instance or run without ambient package-global path state.
Relative paths resolve against the definition's source or an explicit base, not
the process working directory or the last config file loaded.

Secrets are never configuration values. Definitions contain credential
references and declared purposes.

## Interfaces and parity

Fragments Engine follows one core, several doors:

```text
Domain services
  -> HTTP
  -> CLI client
  -> MCP adapter
  -> Sysop GUI
  -> event subscriptions
  -> plugin contracts
```

Every surface preserves identity, authorization, revision, idempotency,
lifecycle, and error semantics. Transport-specific interaction patterns are
allowed; transport-specific business rules are not.

The daemon exposes capability discovery so clients know which connectors,
extractors, destinations, recall modes, and optional system tools are ready.
Schemas are derived from registered contracts rather than duplicated across
large hand-maintained handlers.

Mutations use optimistic revisions and idempotency keys. Destructive or
high-effect actions expose preview, authenticated actor, reason, and durable
audit. `force=true` is a break-glass command, not ordinary authorization.

## Daemon and process model

One Fragments Engine daemon is the runtime authority for a local instance. It
opens the store and compiled configuration once and owns:

- migrations and store handles
- source activations and schedules
- acquisition, enrichment, and delivery workers
- claims, leases, and reconciliation
- configured plugins and provider clients
- local recall projections
- HTTP API and GUI
- live event delivery

The operating system or Cerberus supervises this process. Fragments Engine
does not install or manage its own daemon.

Normal CLI and stdio MCP operations are clients of the daemon. They do not
open the same database, run migrations, start their own queue drainer, or
become another background-work authority. A stdio MCP proxy preserves harness
compatibility while forwarding semantic operations.

An exclusive embedded or offline mode may exist for tests, portable tooling,
and maintenance, but it declares sole store authority and cannot run alongside
the daemon against the same instance. One-off admin commands fence the daemon
or use backend-safe service operations.

Health means the process is alive. Readiness reports store and migration state,
worker leadership, definition validity, required connector capabilities,
credential-reference resolution readiness, and degraded optional providers.

## Persistence, queueing, and recovery

### Authoritative state

The authoritative backup set includes:

- fragment identities and immutable revisions
- source observations and provenance
- custody, sensitivity, and retention policy
- source, route, destination, and renderer registrations and revisions
- triage cases and decisions
- delivery bindings, attempts, and receipts
- acquisition and enrichment runs
- durable assertions and accepted relations
- materialization manifests
- authenticated actor and audit facts
- adopted or mirrored blob manifests and content where Fragments Engine is
  responsible for custody

### Derived and rebuildable state

FTS tables, embeddings, thumbnails, cached remote bodies, health summaries,
queue dashboards, and other projections may be rebuilt when their manifests
declare the inputs and generation versions needed to do so.

FFS output is rebuildable only for `managed_projection` roots. External file
authorities require their own backup contract.

### Atomicity

Fragment revision, provenance, and durable content event share one commit.
Triage decisions and their applied effects share a commit or an outbox.
Delivery binding and dispatch intent share a commit. Separate queues use an
outbox and reconciliation rather than relying on a sequence of best-effort
writes.

Restore starts in a non-delivering recovery mode. It verifies blobs,
projections, claims, leases, destination registrations, and pending intents
before re-enabling outbound effects. A restored queued callback must not fire
twice merely because the operator inspected the backup.

### SQLite and larger deployments

SQLite is a strong default for one local authority. The daemon uses an explicit
writer/read model, deterministic startup, bounded connections, busy handling,
and transaction discipline.

Multi-process concurrency requires a backend and leadership model designed for
it. Several processes independently opening the same SQLite file, changing WAL
mode, running migrations, and starting workers is not horizontal scaling.

## Authentication, authorization, secrets, and privacy

### Identity and authorization

Every mutating command and sensitive read has an authenticated principal and
client identity. Authorization distinguishes:

- capturing or reading content
- accessing sensitive source bodies and attachments
- registering source roots or network endpoints
- changing enrichment and route policy
- executing filesystem, process, MCP, HTTP, or messaging effects
- reviewing triage cases
- publishing or materializing content
- replaying deliveries and using break-glass actions
- managing plugins, retention, export, and deletion

Loopback-only open mode can be an explicit personal deployment profile. A
service bound beyond loopback requires authentication and scoped authorization.
Localhost origin is a network property, not actor identity.

### Secret boundary

Fragments Engine stores credential references, never provider secret values,
in YAML, source registrations, destination config, HTTP headers, MCP environment
lists, CLI arguments, database rows, events, logs, or backups.

```text
CredentialRef
  authority
  logical identity
  permitted connector or destination purpose
  scope and principal
  expiry or rotation metadata
```

An authorized resolver delivers a narrow runtime grant directly to the adapter.
Environment-variable names may remain a compatibility mechanism, but the
desired contract does not require real secret values to be copied into a
Cerberus resource definition or launchd plist.

Tether's LLM or MCP gateway may resolve provider access when composed. Direct
official SDK use remains valid in standalone mode. Neither arrangement changes
the external credential authority.

### Content safety and privacy

Fragments are untrusted content. Text from chats, web pages, repositories,
directives, OCR, and model outputs cannot alter system policy or grant effects.

Source and destination registrations carry sensitivity, redaction, retention,
and allowed-egress policy. Public or remote destinations can require explicit
review even when a route predicate matches. Logs, traces, metrics, and errors
exclude content bodies and secret-bearing metadata by default.

Deletion distinguishes source withdrawal, Fragments Engine retention expiry,
user erasure, cache eviction, projection rebuild, and audit preservation.
Cascade deletion is not a substitute for a governed content-erasure protocol.

## Events, audit, and observability

Every material domain change emits a durable typed event:

```text
ContentEvent
  event identity and schema version
  scope and entity identity
  event kind
  authenticated actor and client
  causation and correlation
  command identity and idempotency key
  prior and resulting revision
  policy decision
  occurred and recorded times
```

Important facts include source observation, fragment revision creation,
enrichment production and acceptance, triage proposal and decision, route
evaluation, delivery binding, attempt transition, receipt, downstream outcome,
materialization, retention, and deletion.

`route_log`, queue events, run rows, and process logs currently cover useful
parts of this story. The target unifies their identity and correlation without
forcing high-volume extraction telemetry into the domain event stream.

OpenTelemetry traces, metrics, structured logs, SSE, and Sysop dashboards are
operational projections. They never become the authority for content state.
Every trace can correlate acquisition run, fragment revision, triage case,
route evaluation, delivery binding, destination, and downstream handle without
recording sensitive body content.

## Portfolio composition

### Tesseract

Tesseract provides portfolio memory, knowledge, namespaces, promotion, and
cross-domain retrieval. Fragments Engine provides governed content intake and
domain-local search. The systems compose through pointers, revision digests,
retrieval projections, and explicit promotion.

Tesseract never presents a cached fragment projection as current without
resolving Fragments Engine. Fragments Engine never writes imported content into
a user knowledge namespace merely because it was ingested.

### Loom

Loom owns content compilation, templates, lenses, generated pages, drafts,
wiki bundles, output review, and export. Fragments Engine owns captured source
material and delivery of immutable references. The existing Curator callback
is one adapter for that composition, not a product-specific core destination.

### Nanite

Nanite owns interactive and durable agent definitions, sessions, roles, skills,
teams, and conversation behavior. It may capture content into Fragments Engine
or host an agent that responds to a Fragments Engine delivery.

Fragments Engine stores the wake receipt and optional session reference. It
does not own the Curator agent, infer agent completion from HTTP acceptance, or
embed agent behavior in a route definition.

### Tether

Tether is optional composition for general messaging and federation, MCP and
LLM gateways, public identity, and delegated sessions. Fragments Engine retains
source, triage, route, and delivery meaning while Tether owns its gateway or
message-delivery state.

Standalone Fragments Engine may use local HTTP, MCP, CLI, and queue adapters.
That independence must not grow into a second portfolio-wide message broker or
MCP control plane.

### Torque

Torque owns managed work. A fragment, triage decision, failed delivery, or
content opportunity may create a Torque work item through an authorized
adapter. Fragments Engine stores the task reference; Torque owns priority,
dependencies, dispatch, review, and completion.

A fragment lifecycle is not a task lifecycle, and a completed task does not
silently change source authority or publish content.

### Hadron

Hadron owns execution of declared workflows. Fragments Engine can select a
Hadron-backed acquisition, enrichment, or delivery adapter using a pinned
workflow definition. Fragments Engine owns the content operation and applies
the normalized workflow result; Hadron owns nodes, waits, internal retries, and
workflow provenance.

### Cerberus

Cerberus deploys and observes Fragments Engine and manages its attached
resources. The OS supervises the daemon. Cerberus passes endpoints and
credential references, not content semantics or provider secret values.

If a heavy extractor ever needs a Coder workspace, Fragments Engine consumes a
Cerberus `WorkspaceHandle` and `WorkspaceLease` as an execution target. A
content entity mentioning a workspace, a local source directory, and a Coder
Workspace are different concepts.

### Nil, Frag, browser clipper, and source peers

Frag and browser extensions are capture clients. Nil and other content systems
are source or destination peers. Each interaction records whether content was
copied, referenced, adopted, or delivered.

Reading Nil's local database can preserve standalone operation, but it remains
a versioned compatibility connector rather than a transfer of Nil's data
authority to Fragments Engine.

### Stack Explorer and other specialist apps

Stack Explorer owns repository analysis and catalog semantics. Fragments
Engine may deliver a repository observation or request a scan through a typed
adapter and store the returned handle. Domain-specific synchronization logic
does not live inside a generic inbox reviewer.

## Twelve-factor and Go operating model

Fragments Engine follows the portfolio's adapted twelve-factor direction:

- One version-controlled codebase produces versioned binaries and packages for
  many deployments.
- Go dependencies and required external tools are explicitly declared and
  isolated. Optional tools such as OCR, PDF, browser, and video extractors are
  advertised capabilities, not ambient assumptions about `PATH`.
- Configuration is external and contains no environment-specific values or
  secret material.
- Databases, blob stores, source systems, credential authorities, Tether
  gateways, Tesseract projections, and Cerberus resources are attached
  resources.
- Build creates an immutable binary and embedded frontend; release binds the
  artifact to configuration and registered adapters; run executes it.
- Process-local state is disposable unless explicitly modeled as durable
  operational state.
- The daemon embeds its HTTP server and binds a declared port.
- Concurrency scales through durable claims and an appropriate store rather
  than competing uncoordinated SQLite processes.
- Startup is deterministic, shutdown is graceful, and in-flight acquisition,
  enrichment, delivery, and projection work has recovery semantics.
- Development, test, and production exercise the same authority, connector,
  queue, and persistence contracts.
- Logs go to stdout and stderr; content history and audit use structured durable
  stores.
- Migrations, backup, restore, reindex, reconciliation, import, export,
  retention, and repair run as one-off processes from the same release.

## Current strengths to preserve

The implementation already contains strong architectural choices:

- explicit peer-application positioning rather than privileged portfolio
  orchestration
- local-first operation with SQLite and deterministic FTS fallback
- thin CLI, HTTP, MCP, and Sysop wrappers around shared services
- a clear source-connector and pipeline-stage seam
- deterministic processing before optional provider-backed enrichment
- content hashes, source identifiers, provenance metadata, and route history
- first-class attachments and URL references with OCR, extraction, preview, and
  vision-provider seams
- durable entities and a distinction between stored relations and retrieval
  results
- an inbox that preserves uncertain material instead of silently filing it
- route and destination preview, validation, health, retry, dead-letter, and
  audit surfaces
- persistent queues built on shared `go-queue` and schedules built on shared
  `go-scheduler`
- callback delivery kept off the synchronous ingest hot path
- directive parsing kept separate from directive execution
- path traversal protections and config-relative path anchoring
- side-effect-free preview for destructive archive behavior
- transport-based destination vocabulary and the stated rule that peer apps
  remain external
- embedded Tesseract and SQLite recall behind a Fragments Engine interface
- search traces and graceful keyword fallback when embeddings are unavailable
- FFS bundles that are human-readable outside the application
- the Loom seam keeping generation templates and output data outside Fragments
  Engine
- a single Go binary with an embedded operator UI and Cerberus-managed service
  deployment

The target should clarify these strengths, not replace Fragments Engine with a
generic document database, notes app, message broker, or knowledge graph.

## Architectural tensions to resolve

These are target-state design questions exposed by the current implementation,
not a delivery backlog.

### Canonical language obscures source authority

Current docs call Fragments Engine the canonical knowledge/search engine and
the fragment content canonical. That is accurate only for its observed or
adopted operational record. Imported sources, user judgment, generated output,
and Tesseract namespaces retain their own authority.

### Fragment identity includes content hash

The current fragment ID changes when content changes. This gives exact dedupe
but no stable identity or explicit source revision lineage.

### One status field combines unrelated state

`inbox`, `routed`, and `indexed` describe triage, delivery, and projection.
They are not mutually exclusive content states and cannot represent fan-out,
partial delivery, stale indexes, or several triage concerns.

### Mutable rows replace historical facts

Titles, summaries, metadata, entities, attachments, destinations, routes, and
config can be updated in place. A complete content revision and policy history
is not reconstructable from route logs alone.

### JSON metadata carries too many schemas

`metadata_json`, untyped ingest rules, arbitrary destination arguments and
bodies, and provider-specific nested config accelerate experimentation but
hide ownership, versioning, sensitivity, and validation boundaries.

### Pipeline stages cross several commit boundaries

Fragment upsert, attachments, directives, route attempts, inbox state, recall,
and run completion can partially succeed. A later stage failure can leave valid
content with ambiguous run and dispatch state.

### Routing selects the first matching row

Current matching depends on route iteration order and supports only one target.
It does not express fan-out, exclusivity, conflict resolution, or independent
delivery purposes.

### Classification documentation is ahead of the runtime

The architecture describes weighted and Bayesian classifiers, confidence-based
routing, and training from human decisions. The live route engine primarily
uses exact source/type/entity matches; route confidence is not a complete
implemented authority model.

### App-specific provider schemas live in core types

Nil inbox and Nanite messaging request fields, FFS special cases, callback
generator semantics, and Stack Explorer behavior appear in core or service
packages despite the stated transport-neutral peer boundary.

### Callback conflates transport and delivery semantics

The callback is HTTP plus queued, fire-and-forget behavior. Treating it as a
parallel transport makes receipt, completion, retry, and health meaning harder
to generalize.

### Delivery success can overstate downstream outcome

Route status becomes routed after an adapter succeeds. That does not prove a
downstream agent, compile, import, message consumer, or human completed the
intended business action.

### Mutable definitions are not pinned to attempts

Routes and destinations can change or be force-deleted while historical logs
retain only IDs and reason strings. A retry may observe different current
configuration than the original decision.

### FFS can become a second source of truth

The roadmap describes FFS as a durable human-readable layer and Fragments
Engine as canonical metadata/content. Without a declared authority mode and
round-trip policy, file edits and database edits will diverge.

### Embedded Tesseract duplicates full content

The current embedded Vanta/Tesseract adapter writes full fragment bodies as
canonical knowledge revisions in another store. That is useful for recall but
blurs projection, pointer, and knowledge authority and complicates backup and
deletion.

### Configuration has two ownership models

Ingest definitions live in a mutable YAML file rewritten by API calls, while
routes and destinations live in SQLite. Runtime config, authored definitions,
and operational state do not have one explainable authority and revision model.

### Config loading mutates ambient path state

The last loaded config establishes a process-global install directory used by
destination resolution. It assumes one config and makes effective binding less
explicit than the domain requires.

### The API opens the application per request

Handlers repeatedly load config, open SQLite, apply pragmas, run migrations,
construct provider clients, and close the app. Background workers independently
do similar work. This contributes to lock races and prevents one coherent
runtime authority.

### MCP and CLI can become competing runtimes

Stdio MCP opens the database for each call and also starts a delivery drainer.
Direct CLI commands open the same store. They can compete with the API daemon
for migrations, jobs, and process-local provider state.

### Background work has fairness and startup races

The reviewer scans an oldest-first general inbox window, allowing pending
enrichment to starve behind settled items. Several independently initialized
workers race on SQLite startup. Both symptoms point to missing typed work
queues, indexed readiness, and daemon-owned startup order.

### SQLite authority is not explicit

WAL and a busy timeout help but do not make several uncoordinated stores and
workers one transactional runtime. Connection limits, single-writer policy,
leadership, and maintenance fencing need a declared contract.

### HTTP and MCP lack principal identity

Most HTTP endpoints have no authentication; a few admin operations use
loopback origin as a stopgap. Captured chats, notes, attachments, route
mutations, process execution, and egress policy require stronger identity and
authorization semantics.

### Destination config can carry secret material

API headers, MCP and CLI environment lists, command arguments, and arbitrary
JSON can persist literal credentials even though provider configuration often
uses environment-variable names. External service configs currently require
secret values to be materialized into process environments.

### Peer database reads are schema coupling

The Nil connector reads internal SQLite tables because the app need not be
running. This is pragmatic standalone behavior, but it treats another app's
private storage schema as an integration contract without an explicit version
or authority protocol.

### Audit is partial and destructive operations can erase context

Route logs and queue events are valuable, but fragment edits, configuration
changes, enrichment versions, actor identity, and complete deletion history
are not one durable event model. Force deletion can remove definitions needed
to interpret prior decisions.

### Backup and restore are not a product contract

The database, embedded Tesseract root, downloaded attachments, mirrored chat
exports, FFS output, previews, live YAML, and queue state have different and
partly implicit custody. Restore behavior for pending external effects is
undefined.

### External tool dependencies are ambient capabilities

OCR, PDF, browser, and video paths may depend on globally installed commands.
The runtime needs explicit capability discovery and isolated dependency
contracts rather than assuming `PATH` represents a reproducible release.

### Observability is incomplete

Process logs and domain status surfaces exist, but Fragments Engine lacks the
shared OpenTelemetry, structured event correlation, and content-safe
instrumentation expected across the portfolio.

### Privacy policy is implicit

Fragments Engine ingests highly sensitive chats, notes, source trees,
attachments, and web captures. Retention, redaction, allowed egress, content
logging, source withdrawal, and user erasure need first-class policy.

### Compatibility vocabulary remains visible

Carrier, Ion, Loom, Vanta, Tesseract, corpus, FFS, inbox, routed, indexed, and
workspace terms reflect several eras. Compatibility names may remain, but new
contracts need one canonical vocabulary and must avoid confusing a mentioned
workspace with a Cerberus/Coder Workspace.

## Boundary guidance

When deciding whether a capability belongs in Fragments Engine, use these
tests.

It belongs in Fragments Engine when it primarily:

- acquires or accepts source material under explicit custody policy
- creates and preserves fragment revisions and provenance
- extracts or records attributed content observations
- manages unresolved content triage and authenticated decisions
- evaluates fragment-routing policy
- queues, retries, and audits fragment delivery or materialization
- provides domain-local search and relationship views over the fragment catalog
- operates Fragments Engine's own store, daemon, workers, and adapters

It probably belongs elsewhere when it primarily:

- defines or maintains generated content, templates, lenses, or wiki pages
- provides portfolio-wide memory, knowledge, promotion, or context packets
- coordinates tasks, sprints, dependencies, or work acceptance
- executes a reusable multi-step workflow language
- manages agent definitions, sessions, conversations, or teams
- brokers general messaging, federation, or MCP/LLM access
- provisions infrastructure or Coder workspaces
- manages provider secrets or identity-provider lifecycle
- owns the current source data in another application

If the answer is mixed, keep semantic authority in the owning system and
compose through an immutable source reference, fragment revision,
`DeliveryBinding`, receipt, outcome reference, typed event, or narrow adapter.

## Questions for the next architecture session

1. Which capture classes make Fragments Engine the custodial system of record,
   and which are always external observations or caches?
2. What is the stable identity and revision model for a source item, fragment,
   conversation, attachment, and manually edited capture?
3. Which normalization and segmentation decisions are source-adapter policy,
   and how are later policy revisions reconciled with durable references?
4. What exact fragment custody lifecycle replaces the current `inbox`,
   `routed`, and `indexed` status field?
5. What triage case kinds and decision schemas are core, and which decisions
   require human rather than policy or model authority?
6. Which human corrections become classifier training samples, under what
   explicit policy and evaluation contract?
7. What route predicate language supports deterministic priority, exclusivity,
   fan-out, review, and explanation without becoming a workflow language?
8. What delivery receipt and optional downstream-outcome contract works across
   file, HTTP, MCP, CLI, Tether messaging, Nanite wake, and Loom compilation?
9. Is every external delivery asynchronous after commit, with synchronous
   materialization retained only as an admin interaction?
10. Which FFS roots are managed projections, and is any root intentionally an
    external file authority with reverse ingestion?
11. What attachment custody classes require a Fragments Engine-managed blob
    store, and which remain verified external references?
12. What Tesseract projection and namespace contract provides semantic recall
    without duplicating full fragment authority or automatic user-knowledge
    writes?
13. Which source, enrichment, destination, renderer, and policy contracts are
    stable enough for the new Hollis Labs plugin SDK?
14. Which peer integrations may use local database compatibility adapters, and
    what schema-version and read-consistency contract governs them?
15. What immutable definition-binding model replaces the YAML/database split
    for ingests, routes, destinations, and renderer policy?
16. Is the daemon always authoritative, with CLI and MCP as clients, or is an
    explicitly exclusive embedded mode also a supported product contract?
17. What persistence, claim, outbox, and recovery model supports one-process
    SQLite now and optional larger deployments without changing domain
    semantics?
18. Which identity provider, content scopes, and effect grants protect capture,
    sensitive recall, route policy, delivery, process execution, and deletion?
19. What complete backup, restore, retention, source-withdrawal, and erasure
    manifest covers the database, blobs, projections, queues, config revisions,
    Tesseract indexes, and FFS materializations?
20. Which Carrier, Vanta, corpus, FFS, status, destination, and workspace names
    are durable product vocabulary versus compatibility history?

These questions refine the target without changing the central direction:
Fragments Engine is the authority for governed fragment operations and the
custody of what it explicitly observes or adopts, not the owner of every source,
definition, knowledge record, generated output, agent, message, secret, or
artifact involved in moving information through the portfolio.
