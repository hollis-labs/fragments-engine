package recall

import (
	"context"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type Status struct {
	Backend           string `json:"backend"`
	EmbeddingsEnabled bool   `json:"embeddings_enabled"`
	EmbeddingProvider string `json:"embedding_provider,omitempty"`
	EmbeddingModel    string `json:"embedding_model,omitempty"`
	VantaRoot         string `json:"vanta_root,omitempty"`
	RecallMode        string `json:"recall_mode"`
	FallbackBackend   string `json:"fallback_backend,omitempty"`
}

// SearchMode selects the retrieval strategy for a search request.
//   - ModeAuto   lets the backend pick its default (embeddings if available).
//   - ModeSemantic forces embedding/vector recall.
//   - ModeKeyword forces lexical (BM25 / FTS) recall.
type SearchMode string

const (
	ModeAuto     SearchMode = "auto"
	ModeSemantic SearchMode = "semantic"
	ModeKeyword  SearchMode = "keyword"
)

// ParseSearchMode normalizes a raw query-param value into a SearchMode. An
// empty or unrecognized value falls back to ModeAuto; ok reports whether the
// input was a recognized mode.
func ParseSearchMode(raw string) (SearchMode, bool) {
	switch SearchMode(raw) {
	case ModeAuto, "":
		return ModeAuto, raw == "" || raw == string(ModeAuto)
	case ModeSemantic:
		return ModeSemantic, true
	case ModeKeyword:
		return ModeKeyword, true
	default:
		return ModeAuto, false
	}
}

type Indexer interface {
	IndexFragment(context.Context, domain.Fragment) error
	Search(context.Context, string, int) ([]domain.SearchResult, error)
	// SearchMode runs a search under an explicit retrieval mode and reports
	// the mode that actually ran (e.g. semantic falls back to keyword when
	// embeddings are unavailable).
	SearchMode(ctx context.Context, query string, limit int, mode SearchMode) ([]domain.SearchResult, SearchMode, error)
	Related(context.Context, string, int) ([]domain.SearchResult, error)
	GetFragment(context.Context, string) (domain.Fragment, error)
	ListEntities(context.Context, string, int) ([]repository.EntityRecord, error)
	ListFragmentsByEntity(context.Context, string, string, int) ([]domain.SearchResult, error)
	Status() Status
	Close() error
}
