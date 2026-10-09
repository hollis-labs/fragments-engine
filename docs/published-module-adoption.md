# Published module adoption

Fragments Engine pins the published replacements for directives (`libs/ui-go`), MCP (`libs/plugin-mcp`), queues and scheduling (`libs/util`), and embedder contracts (`substrate/llm-core`). Embedded Tesseract v0.11.0 shares those embedder and queue types. No local module replacements are required.

The current scheduler API requires durable fire snapshots. Migration 018 adds the published SQL fire schema and an app-owned accepted-dispatch identity table. Existing ingest schedule rows remain the configuration authority; schedule advancement and fire insertion commit together. The supported SQL store owns claim leases and transition fencing. Dispatch acceptance commits the stable fire ID, run row and queue envelope together, and recovered accepted dispatches return the supported duplicate-job result even after the queue envelope has been consumed.

This change is source-only. No live database migration, schedule activation, transcript ingestion, indexing or deployment was performed. An eventual normal startup against a database will apply the new schema through the existing migration mechanism; deployment and any live operation require their own scope. Existing row-only scheduling has no historical fire records to backfill; future occurrences receive durable identities. Native ingest-worker retries retain their existing policy. The scheduler zero retry policy also retains unlimited dispatch retries at the normal poll cadence.

Verification uses synthetic content and temporary databases. It does not read private transcript payloads or ambient provider credentials.

Arbitrary fragment projections use Tesseract’s supported canonical fallback `note` kind plus the descriptive `fragment` tag, replacing its rejected legacy `fragment` kind. This follows the released `facets-and-kinds` fallback guidance and retains the original body, source, fragment identity and `fe` pointer/locator; it adds no vocabulary or promotion behavior.

Future automated writes use the app-owned `project/fragments-engine/knowledge/fragments` scope. The released embedded policy accepts default agent writes to an undeclared project scope and honors any declared ownership policy; the app adds no privileged registry declaration. The former `user/fragments-engine/knowledge/fragments` scope remains read-only in recall. Duplicate projection hits hydrate one current FE fragment. No user actor is asserted and no existing projection is migrated or backfilled.
