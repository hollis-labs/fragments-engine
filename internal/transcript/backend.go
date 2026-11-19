package transcript

import (
	"context"
	"time"
)

// Backend is what FE's enrichment, embedding, browse and search code needs from a
// transcript corpus. The SQLite Store (being retired) and the Postgres service
// (pgstore.Service) both satisfy it, so callers do not depend on either.
type Backend interface {
	EnrichBackend
	BrowseBackend
}

// EnrichBackend is the enrichment, embedding and spend surface.
type EnrichBackend interface {
	MainSessions(ctx context.Context, minIdle time.Duration, now time.Time, nativeID string) ([]SessionRef, error)
	LegacySessions(ctx context.Context, nativeID string) ([]SessionRef, error)
	SessionFacts(ctx context.Context, pk string) (SessionFacts, error)
	EnrichmentInput(ctx context.Context, ref SessionRef) (EnrichmentInput, error)
	EnrichmentDone(ctx context.Context, pk string, tier int, promptVersion, model, contentDigest string) (bool, error)
	SaveEnrichment(ctx context.Context, r EnrichmentRecord) error
	SaveEnrichmentWithin(ctx context.Context, r EnrichmentRecord, deadline time.Duration) error
	LatestEnrichment(ctx context.Context, pk string, tier int) (EnrichmentRecord, bool, error)
	AppendSpend(ctx context.Context, r SpendRecord) error
	Spend(ctx context.Context, runID string) (SpendTotals, error)

	EmbeddingCurrent(ctx context.Context, pk, kind string, idx int, model, format, textDigest string) (bool, error)
	SaveEmbedding(ctx context.Context, pk, kind string, idx int, model, format, textDigest string, vec []float32) error
	SummaryTexts(ctx context.Context) ([]SummaryText, error)
	SearchEnriched(ctx context.Context, query string, limit int) ([]SearchHit, error)
	NearestSummaries(ctx context.Context, model, format string, vec []float32, k int) ([]SearchHit, error)
}

// BrowseBackend is the read side behind FE's transcript HTTP API and CLI.
type BrowseBackend interface {
	ListSessions(ctx context.Context, f SessionFilter) (SessionPage, error)
	SearchSessions(ctx context.Context, text string, f SessionFilter) (SessionPage, error)
	SearchSummaries(ctx context.Context, text string, f SessionFilter) (SessionPage, error)
	SemanticSessions(ctx context.Context, model, format string, vec []float32, f SessionFilter) (SessionPage, error)
	EmbeddingGroups(ctx context.Context) ([]EmbeddingGroup, error)
	SuggestProjects(ctx context.Context, q string, limit int) ([]ProjectCount, error)
	SessionFacets(ctx context.Context) (Facets, error)
	SessionDetail(ctx context.Context, id string, turnOffset, turnLimit int) (SessionDetail, error)
	FindInSession(ctx context.Context, id, text string, afterOffset, limit int) (FindResult, error)
	SessionErrors(ctx context.Context, id string) (ErrorList, error)
	Ingest(ctx context.Context) (IngestStatus, error)
	Stats(ctx context.Context, f SessionFilter) (Stats, error)
	QueryToolCalls(ctx context.Context, f ToolCallFilter) ([]ToolCall, error)
	SessionUsage(ctx context.Context, nativeSessionID string) (UsageTotals, error)
}

var _ Backend = (*Store)(nil)
