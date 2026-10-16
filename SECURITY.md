# Security policy

## Supported versions

Fragments Engine is pre-1.0 software. Security fixes are made on `main` and the
newest tagged release. Older releases may not receive backports.

## Report a vulnerability

Do not include an exploit, token, API key, database, captured content, or other
sensitive material in a public issue.

Use GitHub's private vulnerability-reporting flow when the repository's Security
tab offers it. If it is unavailable, contact a repository maintainer privately
through a contact channel published on the Hollis Labs organization or
maintainer profile. Include:

- the affected commit or version and operating system
- how the API was bound (address and port) and whether it was reachable remotely
- reproduction steps and the security impact
- whether credentials or user data may have been exposed
- a safe way to contact you about coordination

Maintainers will acknowledge a private report, investigate it, and coordinate
disclosure; response times are best effort during the pre-release period.

## Deployment boundary

Fragments Engine is a single-owner service. Configure `--token-file` or
`FRAGMENTS_API_TOKEN_FILE` to require bearer authentication on all `/v1` routes,
including loopback calls. The shared guard runs in enforce mode. A missing or
unsafe configured token file refuses startup and fails closed at runtime.

- The default listener is `127.0.0.1:8091`. Keep it behind your existing HTTPS
  reverse proxy. Non-loopback listeners require authentication; transcript-enabled
  instances still refuse non-loopback listeners.
- Reader, media and administrative loopback gates remain. Transcript requests
  additionally require a loopback Host (the trusted local proxy rewrites it).
- Sysop uses opaque eight-hour HttpOnly/SameSite=Strict sessions, Secure over
  HTTPS. Sign-out, restart and credential rotation revoke sessions. Cookie
  mutations require an allowed Origin. Configure public origins explicitly.
- The owner bearer grants full API access. It does not isolate applications or
  processes sharing the OS account; the token file is readable by that account.
- Capture `principal_id` remains submitted provenance, not authenticated identity.
- There is no built-in TLS. Public shell/health/auth-status routes contain no
  private content. Without a configured token file, local operation is unauthenticated.
- MCP and CLI continue to act with the full authority of the local user.

See [`docs/browser-capture-reader.md`](docs/browser-capture-reader.md).

## Data at rest

There is no built-in at-rest encryption. The SQLite database, stored media and
any filesystem destinations (for example an FFS output directory) hold the
content you ingest — chat history, notes, saved pages, images — and may be
sensitive. Protect them with normal account and disk-encryption controls.

The config file (`fragments.yaml`) is rewritten in place by the ingest CRUD
endpoints. Config fields such as `openai.api_key_env` and
`firecrawl.api_key_env` name an *environment variable*, not the secret; keep
credential values out of the config file, manifests, adapter descriptors and
logs. `fragments.yaml` and the database are gitignored — do not commit them.

## External data processors

Ingest and enrichment are local-first, but some features are network-backed
when you configure them: fetching saved URLs, the Firecrawl fallback for link
content, OpenAI or Ollama for embeddings and image analysis, and any `api`,
`mcp` or `callback` destination you define. Content sent to those services is
governed by their terms. Browser acquisition does not export session cookies or
page tokens to Fragments Engine.

## Current security limitations

- authentication is opt-in; without a token file local API calls are unauthenticated
- one owner credential, without per-client scopes; capture `principal_id` is provenance
- no built-in TLS
- no at-rest encryption of the database, media or exports
- pre-1.0 contracts and migration guarantees

These are deployment constraints, not hidden roadmap promises. Operate within
them or place Fragments Engine behind controls that provide the missing
boundary.
