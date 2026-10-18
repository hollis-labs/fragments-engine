# Scoped capture authentication

Consumer contract for CW-20261018-0060. Tesseract reference:
`project/fragments-engine/workspace/api-references/fe_scoped_basalisk_capture_auth`,
item `01M58BQNR9ZMJ91R9F5RRAVJ4A`.

This source adds optional Tether JWT verification alongside the existing owner
bearer. It does not activate that configuration or provision production grants.
At inspection on 2026-10-18, agent-os still ran the 2026-10-16 artifact from
`fragments-engine-wt-auth` at `0624642`. The service was active on loopback8091.

## Credentials and scopes

Send `Authorization: Bearer <credential>`. Supported credentials are the existing
private owner API key and, when configured, Ed25519 JWTs issued by the pinned
Tether issuer. The exact JWT audience is the string `fragments-engine`; audience
arrays are rejected. Hub currently uses issuer `tether`. FE enforces its own
authentication and authorization independently of Tether's observation modes.

| Grant | Operations |
| --- | --- |
| `fragments-engine.capture` | POST capture manifest; GET own capture status; PUT own capture asset; POST own completion |
| `fragments-engine.read` | General fragments/search/entities/inbox, Reader and media reads |
| `fragments-engine.write` | Legacy intake, fragment edits/materialization/reanalysis, Reader commands |
| `fragments-engine.admin` | Ingest/config/destination/route/queue administration and operational status |

The [route table](../internal/api/security_scopes.go) lists each method and path.
There is no default grant. Unknown protected routes fail closed, including for
the owner key. Scope implication is limited to shared auth's exact matching,
`fragments-engine.*`, or `*`; admin does not imply capture/read/write.

Browser sessions and transcript reads additionally require the actual local
owner credential, or a browser cookie derived from it. Even an admin JWT whose
subject resembles the owner cannot use these surfaces. Existing transcript
feature/listen/peer/Host guards and proxy restrictions still apply. Origin
admission, cookie CSRF checks, explicit-bearer precedence, owner-token rotation
and the eight-hour cookie lifetime retain their prior behavior.

## Basalisk submission

Use a Tether-enrolled service principal with only `fragments-engine.capture`.
Keep its subject stable across credential renewal. Neither a Basalisk device
credential nor a Basalisk activity-session ID authenticates to FE.

1. POST `/v1/captures` with `Content-Type: application/json` and an existing
   `fe.capture.v1` envelope. See the full sanitized
   [request fixture](../contracts/browser-capture-reader/v1/fixtures/valid-capture-service.json).
   `principal_id` must equal the verified JWT subject; mismatch returns403.
2. A new acceptance returns201; an exact replay returns200. Both conform to
   `fe.capture.result.v1`. The [response fixture](../contracts/browser-capture-reader/v1/fixtures/valid-capture-service-response.json)
   was generated against a disposable database. Confirm top-level `capture_id`,
   `fragment_id`, `fragment_revision_id`, and `capture_attempt_id` from the actual
   response before recording provenance. `reader_item` is an optimistic receipt
   preview; its hrefs do not grant general Reader/media access. Its tags include
   only this capture's observations for mesh callers.
3. GET `/v1/captures/{capture_id}` returns `fe.capture.status.v1` for that verified
   subject. A missing, foreign, or historic unbound capture returns404.
4. PUT `/v1/captures/{capture_id}/assets/{client_variant_id}/content` follows the
   existing manifest's instructions and `Digest: sha-256=:base64:` wire format.
5. POST `/v1/captures/{capture_id}/complete` uses `fe.capture.completion.v1`.
   A text-only completion is:

   ```json
   {"schema_version":"fe.capture.completion.v1","idempotency_key":"complete-basalisk-note-01","assets":[],"warnings":[]}
   ```

The existing browser manifest requires stable submitted/canonical URLs. For a
Basalisk note, supply an actual stable Basalisk note URL and source item key;
the example hostname is illustrative. FE accepts the prefetched body without
fetching that URL. This change does not publish a native note shape without a
URL. Legacy `/v1/intake` accepts content/tags and returns
`fragment_id`/`outcome`/`status` inside `result`, but has no capture ID, revision response, or safe
ambiguous-retry contract. The capture scope deliberately does not admit it.

New mesh captures persist `authenticated_subject` from the verified principal,
separately from historical caller-supplied `principal_id`. Annotation/tag actors
use the verified subject. Migration020 leaves historic bindings empty; it never
retroactively attests them. Existing local-owner/CLI/MCP capture attribution is
compatible. Caller extensions remain assertions, including human/project/session
correlation. They do not confer delegation. Unknown top-level fields such as
`from` are rejected by the existing schema. FE does not interpret a JWT `par` or
an asserted human ID as proof of on-behalf-of authority.

## Retry and failure behavior

Persist the original manifest, including capture ID, capture time, source
identity and principal ID, before sending. Retry that manifest under the same
verified subject after an ambiguous response. FE atomically records acceptance
and returns its immutable original snapshot on replay, with only
`idempotent_replay` changing. Changed semantic payload under the same ID gives409.
The digest is over the canonical decoded envelope, so whitespace/key order does
not create a new capture. A changed submission needs a new capture ID.

Capture IDs are globally unique, not per-subject. Use UUIDs/ULIDs across producers.
A foreign subject collision returns409 without returning the earlier receipt.
Historic unbound IDs cannot be claimed by matching their payload principal.
Upload and completion are separate stages. Exact completion retry uses its own
stable key and stored response; changed completion payload with that key gives409.
This is not an exactly-once guarantee over the entire multi-stage operation.

| HTTP | Meaning / wire code |
| --- | --- |
| 401 | Missing/invalid/expired/wrong issuer/audience/revoked credential: `unauthorized` |
| 403 | Missing scope: `scope_required`; unmapped protected operation: `route_not_mapped`; principal mismatch: `forbidden`; Origin/owner restrictions |
| 503 | Verification/revocation unavailable: `auth_unavailable` |
| 400 | Capture `validation_failed` or `unsupported_contract_version` |
| 404 | Capture `not_found` |
| 409 | Capture `conflict` or `digest_mismatch` |
| 413 | Capture `payload_too_large` |
| 500 | Capture `internal_error` |

Shared guard and capture-principal authorization errors use `application/json` with
`{"error":{"code":"...","message":"..."}}`; Origin and owner-only guards use
plain text. Capture errors use the existing `application/problem+json` schema.
An invalid explicit bearer is never rescued by an owner cookie. No credential
is returned by FE, written in config YAML, or logged by these handlers.

## Issuer configuration and lifecycle

Keep `FRAGMENTS_API_TOKEN_FILE` and `FRAGMENTS_BROWSER_ORIGINS` as configured.
Optional issuer settings have equivalent kebab-case CLI flags:

| Environment variable | CLI flag | Value |
| --- | --- | --- |
| `FRAGMENTS_AUTH_ISSUER` | `--auth-issuer` | Exact trusted `iss`, currently `tether` |
| `FRAGMENTS_AUTH_ISSUER_URL` | `--auth-issuer-url` | Trusted HTTPS base URL, or explicit local HTTP |
| `FRAGMENTS_AUTH_ISSUER_TOKEN_FILE` | `--auth-issuer-token-file` | Private0600 dedicated Tether maintain credential |
| `FRAGMENTS_AUTH_ISSUER_SOCKET` | `--auth-issuer-socket` | Optional local Unix socket instead of TCP |
| `FRAGMENTS_AUTH_BOOTSTRAP_KEY_FILE` | `--auth-bootstrap-key-file` | Optional public JWKS/Ed25519 PEM |

Partial issuer configuration fails startup. The owner token remains required
when adding the issuer. Issuer credentials are watched for rotation, never put
in argv. Redirects are refused to prevent forwarding those credentials elsewhere.
JWKS is read at `/.well-known/jwks.json`; revocations at `/auth/denylist` with the
dedicated maintain credential. The shared verifier refreshes keys every5m and
refetches unknown kids at most once per10s. Configured bootstrap keys are public.

Before the first JWT admission and at least every30s when requests arrive, FE
fetches the revocation list. An unsuccessful required poll gives503 until a
successful retry; an uninitialized or stale list never silently admits JWTs.
Successful polls may use ETag/304. Revocation latency for requests is bounded by
that30s cache interval; cached public keys remain usable through transient JWKS
outages only when revocation checks remain current. The local owner credential
continues independently for recovery. Background key refresh runs with the HTTP
server lifetime. Expiry/not-before checks use the shared30s clock leeway.

Tether token exchange is POST `/auth/token` authenticated with the enrolled
parent credential:

```json
{"audience":"fragments-engine","scopes":["fragments-engine.capture"],"ttl_seconds":900}
```

Omit `session_id` for the Basalisk service. Default TTL15m, maximum60m. Renew by
repeating exchange with the enrolled parent before expiry; an issued JWT cannot
mint. Revoke enrollment to stop future issuance and POST `/auth/denylist` with
`{"kind":"jti","value":"<token-id>"}` or
`{"kind":"sub","value":"<stable-subject>"}` to revoke existing JWTs. `sid`
revocation addresses Tether agent sessions, not Basalisk activity sessions.

**Provisioning prerequisite (CW-20261018-0067):** service-principal issuance is implemented in
Tether's internal identity/issuer stores, but no operator-facing service enrollment
command/API was verified in the checked build. Do not edit its DB or distribute
its operator token as a substitute. Tether's owner must provide an authorized
service enrollment/rotation/revocation surface, then enroll Basalisk with the
capture grant and FE with a separate maintain-only grant. Basalisk owns its
parent-credential storage and exchange/renewal. FE only verifies tokens.

Versions checked against released module tags2026-10-18: `security/auth v0.1.0`
and `security/secrets v0.1.0`, already pinned in go.mod. The separate
`security/policy v0.1.0` supplies a PDP boundary; it does not replace bearer scopes
and is unnecessary here. Shared pairing PR92 was still unmerged; no dependency
on it or private replacement issuer is introduced.

## Agent-os migration and rollback

1. Resolve the Tether service-enrollment prerequisite above. Verify the actual
   stable Basalisk subject and narrowly scoped grants. Establish an authenticated
   issuer transport from agent-os to hub. The verified hub socket is
   `/var/lib/fabric/tether/run/tetherd.sock`; agent-os has no default local daemon.
   Do not expose that socket unauthenticated, broaden existing network listeners,
   or point FE at its own host's nonexistent daemon. An existing approved HTTPS
   endpoint or managed authenticated private forwarding must be selected and
   verified before production activation.
2. Review/merge the FE PR. Create a dedicated agent-os source worktree at its
   reviewed commit, preserving `fragments-engine-wt-auth` and any other session's
   edits. Produce the ignored Sysop dist bundle there before building. Retain
   the private live config and original owner token/origin settings.
3. Before activation, record current Cerberus/resource/unit state and save the
   installed artifact and registration privately. Back up SQLite with its online
   backup API, including a restoration check; do not copy only a live main DB
   while ignoring WAL. Migration020 only adds a non-null text column with an
   empty default; no historic identity or content is rewritten. The old binary
   tolerates the extra column, allowing artifact/config rollback without deleting
   new captures. A full DB restore would discard later writes and requires a
   separate deliberate data-loss decision.
4. Prepare the exact new source path and issuer environment in
   `/home/chrispian/.config/fragments-engine/agent-os.cerberus.yaml` for resource
   `fragments-engine-agent-os`. Preserve loopback8091, the live config path,
   systemd runtime drop-in and proxy. Review the concrete diff/restart before
   applying it. Check resource status, then deploy with Cerberus
   `resource deploy fragments-engine-agent-os --ack`; reload alone does not
   install a rebuilt artifact. Confirm the new installed artifact and active PID.
5. Validate owner401/200, browser session exchange, permitted origins and
   unchanged transcript guards. With disposable test data/credential only, verify
   capture201/replay200/changed409, foreign404, revoked401, missing scope403 and
   issuer-unavailable503; verify forbidden config/transcript/session access.
   Record confirmed IDs and clean up through owner administration as appropriate.
6. If rollback is needed, restore the saved artifact/registration through
   Cerberus, retaining the DB and original owner credential. Removing issuer
   configuration disables mesh access while leaving owner access intact. Record
   the actual version/PID/settings and post the outcome to the task/reference.

No production provisioning, restart, probe capture, or global token rotation was
performed while preparing this change on home-laptop.
