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

type Indexer interface {
	IndexFragment(context.Context, domain.Fragment) error
	Search(context.Context, string, int) ([]domain.SearchResult, error)
	Related(context.Context, string, int) ([]domain.SearchResult, error)
	GetFragment(context.Context, string) (domain.Fragment, error)
	ListEntities(context.Context, string, int) ([]repository.EntityRecord, error)
	ListFragmentsByEntity(context.Context, string, string, int) ([]domain.SearchResult, error)
	Status() Status
	Close() error
}
