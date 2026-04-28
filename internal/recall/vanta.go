package recall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/go-providers/provider"
	conduit "github.com/hollis-labs/vanta-conduit"
	vmemory "github.com/hollis-labs/vanta-conduit/memory"
)

const vantaNamespace = "user/fragments-engine/knowledge/fragments"

type VantaIndexer struct {
	base    *SQLiteIndexer
	repo    *repository.FragmentRepository
	conduit *conduit.Conduit
	status  Status
}

func NewVantaIndexer(ctx context.Context, repo *repository.FragmentRepository, entities *repository.EntityRepository, cfg config.Config) (*VantaIndexer, error) {
	root := strings.TrimSpace(cfg.Recall.Vanta.Root)
	if root == "" {
		root = filepath.Join(filepath.Dir(config.ExpandHome(cfg.Database.Path)), "vanta")
	}
	root = config.ExpandHome(root)

	status := Status{
		Backend:         "vanta",
		VantaRoot:       root,
		RecallMode:      "bm25",
		FallbackBackend: "sqlite",
	}

	opts := []conduit.Option{}
	if embedder, model, providerName := buildEmbedder(cfg.Recall.Vanta); embedder != nil {
		opts = append(opts, conduit.WithEmbedder(embedder))
		if model != "" {
			opts = append(opts, conduit.WithEmbeddingModel(model))
		}
		status.EmbeddingsEnabled = true
		status.EmbeddingProvider = providerName
		status.EmbeddingModel = model
		status.RecallMode = "hybrid"
	} else {
		status.EmbeddingProvider = normalizedProviderName(cfg.Recall.Vanta.EmbeddingProvider)
		status.EmbeddingModel = strings.TrimSpace(cfg.Recall.Vanta.EmbeddingModel)
	}

	c, err := conduit.Open(ctx, conduit.Config{RootDir: root}, opts...)
	if err != nil {
		return nil, fmt.Errorf("open embedded vanta: %w", err)
	}
	return &VantaIndexer{
		base:    NewSQLiteIndexer(repo, entities),
		repo:    repo,
		conduit: c,
		status:  status,
	}, nil
}

func (v *VantaIndexer) IndexFragment(ctx context.Context, fragment domain.Fragment) error {
	if err := v.base.IndexFragment(ctx, fragment); err != nil {
		return err
	}
	summary := fragment.Summary
	if strings.TrimSpace(summary) == "" {
		summary = summarize(fragment)
	}

	existing, err := v.conduit.GetCurrentRevision(ctx, vantaNamespace, fragment.ID)
	var supersedes string
	if err == nil {
		supersedes = existing.RevisionID
	} else if !errors.Is(err, vmemory.ErrNotFound) {
		return fmt.Errorf("lookup vanta revision: %w", err)
	}

	rev, err := v.conduit.WriteMemory(ctx, vmemory.WriteInput{
		Domain:     vmemory.DomainKnowledge,
		Namespace:  vantaNamespace,
		MemoryKey:  fragment.ID,
		Supersedes: supersedes,
		Status:     vmemory.StatusCanonical,
		Author:     vmemory.Author{AgentID: "fragments-engine", AgentVersion: "0.1.0"},
		Trigger:    vmemory.TriggerManual,
		SessionID:  "fragments-engine",
		Origin:     vmemory.OriginReference,
		Confidence: 1.0,
		Tags:       recallTags(fragment),
		Payload: vmemory.Payload{
			Summary: summary,
			Body:    fragment.Content,
		},
		Facets: vmemory.Facets{
			Kind:   "fragment",
			Source: fragment.Source,
			Pointer: &vmemory.Pointer{
				Scheme:  "fe",
				Locator: "fragment/" + fragment.ID,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("write vanta memory: %w", err)
	}

	_ = v.conduit.EmbedRevision(ctx, rev.RevisionID)
	return nil
}

func (v *VantaIndexer) Search(ctx context.Context, query string, limit int) ([]domain.SearchResult, error) {
	results, err := v.conduit.RecallMemory(ctx, vmemory.RecallInput{
		Namespaces: []string{vantaNamespace},
		Query:      query,
		Limit:      limit,
	})
	if err != nil {
		return v.base.Search(ctx, query, limit)
	}
	resolved, err := v.resolveResults(ctx, results, "", limit)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return v.base.Search(ctx, query, limit)
	}
	return resolved, nil
}

func (v *VantaIndexer) Related(ctx context.Context, fragmentID string, limit int) ([]domain.SearchResult, error) {
	fragment, err := v.repo.GetByID(ctx, fragmentID)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(fragment.Summary)
	if query == "" {
		query = fragment.Title + " " + fragment.Content
	}
	results, err := v.conduit.RecallMemory(ctx, vmemory.RecallInput{
		Namespaces: []string{vantaNamespace},
		Query:      query,
		Limit:      limit + 3,
	})
	if err != nil {
		return v.base.Related(ctx, fragmentID, limit)
	}
	resolved, err := v.resolveResults(ctx, results, fragmentID, limit)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return v.base.Related(ctx, fragmentID, limit)
	}
	return resolved, nil
}

func (v *VantaIndexer) GetFragment(ctx context.Context, fragmentID string) (domain.Fragment, error) {
	return v.repo.GetByID(ctx, fragmentID)
}

func (v *VantaIndexer) ListEntities(ctx context.Context, kind string, limit int) ([]repository.EntityRecord, error) {
	return v.base.ListEntities(ctx, kind, limit)
}

func (v *VantaIndexer) ListFragmentsByEntity(ctx context.Context, kind, value string, limit int) ([]domain.SearchResult, error) {
	return v.base.ListFragmentsByEntity(ctx, kind, value, limit)
}

func (v *VantaIndexer) Status() Status {
	return v.status
}

func (v *VantaIndexer) Close() error {
	if v.conduit == nil {
		return nil
	}
	return v.conduit.Close()
}

func (v *VantaIndexer) resolveResults(ctx context.Context, recalls []vmemory.RecallResult, skipID string, limit int) ([]domain.SearchResult, error) {
	out := make([]domain.SearchResult, 0, limit)
	for _, item := range recalls {
		key := strings.TrimSpace(item.Revision.MemoryKey)
		if key == "" || key == skipID {
			continue
		}
		fragment, err := v.repo.GetByID(ctx, key)
		if err != nil {
			continue
		}
		snippet := item.Revision.Payload.Summary
		if snippet == "" {
			snippet = fragment.Summary
		}
		out = append(out, domain.SearchResult{
			Fragment: fragment,
			Score:    item.Score,
			Snippet:  snippet,
			Trace: domain.RecallTrace{
				Backend:          "vanta",
				Strategy:         v.status.RecallMode,
				Reason:           "vanta_memory_recall",
				MemoryKey:        key,
				EmbeddingEnabled: v.status.EmbeddingsEnabled,
			},
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func buildEmbedder(cfg config.RecallVantaConfig) (provider.Embedder, string, string) {
	switch normalizedProviderName(cfg.EmbeddingProvider) {
	case "", "none":
		return nil, "", ""
	case "openai":
		if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
			return nil, "", ""
		}
		embedder := provider.NewOpenAI()
		model := defaultEmbeddingModel(embedder, strings.TrimSpace(cfg.EmbeddingModel))
		return embedder, model, "openai"
	case "ollama", "llama":
		if !ollamaAvailable() {
			return nil, "", ""
		}
		embedder := provider.NewOllama()
		model := defaultEmbeddingModel(embedder, strings.TrimSpace(cfg.EmbeddingModel))
		return embedder, model, "ollama"
	default:
		return nil, "", ""
	}
}

func normalizedProviderName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "llama":
		return "ollama"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

type providerCapabilities interface {
	Capabilities() provider.ProviderCapabilities
}

func defaultEmbeddingModel(embedder provider.Embedder, configured string) string {
	if configured != "" {
		return configured
	}
	caps, ok := embedder.(providerCapabilities)
	if !ok {
		return ""
	}
	return strings.TrimSpace(caps.Capabilities().DefaultEmbeddingModel)
}

func ollamaAvailable() bool {
	host := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if host == "" {
		host = "http://localhost:11434"
	}
	host = strings.TrimRight(host, "/")

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, host+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func recallTags(fragment domain.Fragment) []string {
	tags := []string{
		"source:" + fragment.Source,
		"source_type:" + fragment.SourceType,
		"status:" + string(fragment.Status),
	}
	if !fragment.IndexedAt.IsZero() {
		tags = append(tags, "indexed")
	}
	return tags
}
