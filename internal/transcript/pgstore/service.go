package pgstore

import "github.com/hollis-labs/libs/core/store"

// Service is the Postgres implementation of transcript.Backend: FE's browse,
// search, enrichment and embedding surface over the corpus. The enrichment and
// embedding methods live in enrich.go, the read side in browse.go.
type Service struct {
	st *store.Store
}

// NewService wraps an opened corpus store (see Open).
func NewService(st *store.Store) *Service { return &Service{st: st} }
