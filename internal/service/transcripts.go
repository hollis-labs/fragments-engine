package service

import (
	"context"
	"log"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// TranscriptBrowseService is the owner-only read surface over the private
// transcript store. It is not agent-facing: no MCP tool or agent CLI wraps it,
// and gated agent access is a separate design. Every read is audited by route,
// id and count, never by content.
type TranscriptBrowseService struct {
	store *transcript.Store
	audit func(route, id string, count int)
}

func NewTranscriptBrowseService(store *transcript.Store) *TranscriptBrowseService {
	return &TranscriptBrowseService{store: store, audit: defaultTranscriptAudit}
}

func defaultTranscriptAudit(route, id string, count int) {
	log.Printf("transcripts-read route=%s id=%s count=%d at=%s", route, id, count, time.Now().UTC().Format(time.RFC3339))
}

// SetAudit replaces the audit sink; tests use it to capture entries.
func (s *TranscriptBrowseService) SetAudit(fn func(route, id string, count int)) { s.audit = fn }

// Available reports whether a private store is configured.
func (s *TranscriptBrowseService) Available() bool { return s != nil && s.store != nil }

func (s *TranscriptBrowseService) List(ctx context.Context, f transcript.SessionFilter) (transcript.SessionPage, error) {
	page, err := s.store.ListSessions(ctx, f)
	if err == nil {
		s.audit("sessions", "", len(page.Items))
	}
	return page, err
}

func (s *TranscriptBrowseService) Search(ctx context.Context, text string, f transcript.SessionFilter) (transcript.SessionPage, error) {
	page, err := s.store.SearchSessions(ctx, text, f)
	if err == nil {
		s.audit("search", "", len(page.Items))
	}
	return page, err
}

func (s *TranscriptBrowseService) Detail(ctx context.Context, id string, turnOffset, turnLimit int) (transcript.SessionDetail, error) {
	d, err := s.store.SessionDetail(ctx, id, turnOffset, turnLimit)
	if err == nil {
		s.audit("session", id, len(d.Turns))
	}
	return d, err
}

func (s *TranscriptBrowseService) Facets(ctx context.Context) (transcript.Facets, error) {
	f, err := s.store.SessionFacets(ctx)
	if err == nil {
		s.audit("facets", "", f.Total)
	}
	return f, err
}

// Projects suggests working directories containing q. The audit line records
// only the count, never the typed text.
func (s *TranscriptBrowseService) Projects(ctx context.Context, q string, limit int) ([]transcript.ProjectCount, error) {
	out, err := s.store.SuggestProjects(ctx, q, limit)
	if err == nil {
		s.audit("projects", "", len(out))
	}
	return out, err
}
