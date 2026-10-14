package service

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/enrich"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// queryEmbedTimeout bounds the one network call a semantic search makes. It
// happens before any database read begins.
const queryEmbedTimeout = 3 * time.Second

// QueryEmbedder turns a search query into a vector with the same model and
// prompt format that made the stored vectors.
type QueryEmbedder interface {
	// Model and Format identify the vectors the query vector is comparable to.
	Model() string
	Format() string
	// Dim is the configured vector dimension, or 0 when unset.
	Dim() int
	EmbedQuery(ctx context.Context, q string) ([]float32, error)
}

type localQueryEmbedder struct {
	e     enrich.Embedder
	model string
	dim   int
}

func (l localQueryEmbedder) Model() string { return l.model }
func (l localQueryEmbedder) Dim() int      { return l.dim }
func (l localQueryEmbedder) Format() string {
	if f, ok := l.e.(enrich.Formatter); ok {
		return f.Format()
	}
	return "plain"
}
func (l localQueryEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	return enrich.EmbedQuery(ctx, l.e, l.model, q)
}

// NewLocalQueryEmbedder builds the query embedder from the same
// transcripts.enrichment.embedding config that `transcript embed` uses, so a
// query is framed exactly as the stored vectors expect. It returns nil when no
// embedding model is configured. Both providers refuse non-local hosts.
func NewLocalQueryEmbedder(emb config.EnrichmentEmbedding) QueryEmbedder {
	model := emb.Model
	switch emb.Provider {
	case "openai_compat_local":
		if model == "" {
			return nil
		}
		e, err := enrich.NewOpenAICompatEmbedder(emb.BaseURL, emb.Dim, emb.QueryInstruction, emb.BatchSize)
		if err != nil {
			return nil
		}
		return localQueryEmbedder{e: e, model: model, dim: emb.Dim}
	case "", "ollama":
		if model == "" {
			model = "nomic-embed-text"
		}
		host := emb.BaseURL
		if host == "" {
			host = os.Getenv("OLLAMA_HOST")
		}
		e, err := enrich.NewOllamaEmbedder(host)
		if err != nil {
			return nil
		}
		return localQueryEmbedder{e: e, model: model, dim: emb.Dim}
	}
	return nil
}

// SetEmbedder attaches the query embedder; without one, semantic search reports
// itself unavailable.
func (s *TranscriptBrowseService) SetEmbedder(e QueryEmbedder) { s.embedder = e }

// SemanticReadiness checks, without any network call, whether stored vectors
// match the configured embedder. It returns "" when semantic search can be
// tried, or a transcript.Notice* code.
func (s *TranscriptBrowseService) SemanticReadiness(ctx context.Context) string {
	if s.embedder == nil {
		return transcript.NoticeSemanticUnavailable
	}
	groups, err := s.store.EmbeddingGroups(ctx)
	if err != nil || len(groups) == 0 {
		return transcript.NoticeNoVectors
	}
	for _, g := range groups {
		if g.Model == s.embedder.Model() && g.Format == s.embedder.Format() && (s.embedder.Dim() == 0 || g.Dim == s.embedder.Dim()) {
			return ""
		}
	}
	return transcript.NoticeModelMismatch
}

// ProbeEmbedder reports whether the embedding server answers with a vector of
// the stored dimension. The probe text is fixed; no user text is involved.
func (s *TranscriptBrowseService) ProbeEmbedder(ctx context.Context) string {
	if code := s.SemanticReadiness(ctx); code != "" {
		return code
	}
	_, code := s.embedQuery(ctx, "ping")
	return code
}

// embedQuery embeds q, bounded by queryEmbedTimeout. On failure it returns a
// notice code and no detail: error text never carries the query.
func (s *TranscriptBrowseService) embedQuery(ctx context.Context, q string) ([]float32, string) {
	ctx, cancel := context.WithTimeout(ctx, queryEmbedTimeout)
	defer cancel()
	vec, err := s.embedder.EmbedQuery(ctx, q)
	if err != nil {
		// The embedder's errors are fixed strings, never the query; a wrong
		// vector size is reported as a mismatch rather than as an outage.
		if strings.Contains(err.Error(), "vector dimension") {
			return nil, transcript.NoticeDimMismatch
		}
		return nil, transcript.NoticeSemanticUnavailable
	}
	if len(vec) == 0 {
		return nil, transcript.NoticeSemanticUnavailable
	}
	if d := s.embedder.Dim(); d != 0 && len(vec) != d {
		return nil, transcript.NoticeDimMismatch
	}
	return vec, ""
}

// semantic embeds the query first, outside any database read, then scans.
func (s *TranscriptBrowseService) semantic(ctx context.Context, text string, f transcript.SessionFilter) (transcript.SessionPage, string, error) {
	if code := s.SemanticReadiness(ctx); code != "" {
		return transcript.SessionPage{Items: []transcript.SessionSummary{}, Mode: transcript.ModeSemantic}, code, nil
	}
	vec, code := s.embedQuery(ctx, text)
	if code != "" {
		return transcript.SessionPage{Items: []transcript.SessionSummary{}, Mode: transcript.ModeSemantic}, code, nil
	}
	page, err := s.store.SemanticSessions(ctx, s.embedder.Model(), s.embedder.Format(), vec, f)
	if err == nil && len(page.Items) == 0 && page.Hint == "" {
		// Vectors exist for the model, but none have this dimension.
		code = transcript.NoticeDimMismatch
		if groups, gerr := s.store.EmbeddingGroups(ctx); gerr == nil {
			for _, g := range groups {
				if g.Model == s.embedder.Model() && g.Format == s.embedder.Format() && g.Dim == len(vec) {
					code = ""
				}
			}
		}
	}
	return page, code, err
}

// SearchMode runs a search in the requested mode. Semantic and hybrid return a
// top-k rather than a pageable list. The audit line records the mode and count,
// never the query.
func (s *TranscriptBrowseService) SearchMode(ctx context.Context, mode, text string, f transcript.SessionFilter) (transcript.SessionPage, error) {
	var page transcript.SessionPage
	var err error
	switch mode {
	case transcript.ModeSummary:
		page, err = s.store.SearchSummaries(ctx, text, f)
	case transcript.ModeSemantic:
		var code string
		page, code, err = s.semantic(ctx, text, f)
		page.Notice = code
	case transcript.ModeHybrid:
		page, err = s.hybrid(ctx, text, f)
	default:
		return s.Search(ctx, text, f)
	}
	if err == nil {
		if page.Mode == "" {
			page.Mode = mode
		}
		s.audit("search-"+page.Mode, "", len(page.Items))
	}
	return page, err
}

// hybrid fuses the summary list, the semantic list and the transcript-text
// list by reciprocal rank fusion. Each list is a separate short read. If the
// embedding server is down the semantic list is dropped and the page says so;
// if transcript text is too broad for its time limit that list is dropped.
func (s *TranscriptBrowseService) hybrid(ctx context.Context, text string, f transcript.SessionFilter) (transcript.SessionPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	notice := ""
	var vec []float32
	if code := s.SemanticReadiness(ctx); code != "" {
		notice = code
	} else if vec, code = s.embedQuery(ctx, text); code != "" {
		notice = code
	}
	wide := f
	wide.Limit, wide.Cursor = 100, ""
	summaries, err := s.store.SearchSummaries(ctx, text, wide)
	if err != nil {
		return transcript.SessionPage{}, err
	}
	lists := [][]transcript.SessionSummary{summaries.Items}
	sources := []string{"summary"}
	if vec != nil {
		sem, err := s.store.SemanticSessions(ctx, s.embedder.Model(), s.embedder.Format(), vec, wide)
		if err != nil {
			return transcript.SessionPage{}, err
		}
		lists = append(lists, sem.Items)
		sources = append(sources, "semantic")
	}
	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	text50 := f
	text50.Limit, text50.Cursor = 50, ""
	tx, err := s.store.SearchSessions(tctx, text, text50)
	if err != nil {
		return transcript.SessionPage{}, err
	}
	if tx.Hint == transcript.HintTooBroad {
		if notice == "" {
			notice = transcript.NoticeTranscriptTooBroad
		}
	} else {
		lists = append(lists, tx.Items)
		sources = append(sources, "transcript")
	}
	items := transcript.FuseRankings(limit, sources, lists...)
	page := transcript.SessionPage{Items: items, Mode: transcript.ModeHybrid, Notice: notice, SnippetsSkipped: tx.SnippetsSkipped}
	if len(items) == 0 && tx.Hint == transcript.HintTooBroad {
		page.Hint = transcript.HintTooBroad
	}
	return page, nil
}
