package service

import (
	"context"
	"fmt"

	"github.com/hollis-labs/fragments-engine/internal/analyze"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/recall"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type FragmentService struct {
	repo        *repository.FragmentRepository
	entities    *repository.EntityRepository
	attachments *repository.AttachmentRepository
	routes      *repository.RoutingRepository
	recall      recall.Indexer
	pipeline    *ingest.Pipeline
	vision      analyze.VisionAnalyzer
}

func NewFragmentService(repo *repository.FragmentRepository, entities *repository.EntityRepository, attachments *repository.AttachmentRepository, routes *repository.RoutingRepository, recallIndex recall.Indexer, pipeline *ingest.Pipeline, vision analyze.VisionAnalyzer) *FragmentService {
	return &FragmentService{
		repo:        repo,
		entities:    entities,
		attachments: attachments,
		routes:      routes,
		recall:      recallIndex,
		pipeline:    pipeline,
		vision:      vision,
	}
}

func (s *FragmentService) RunAllIngests(ctx context.Context, cfg config.Config) ([]domain.IngestRun, error) {
	results := make([]domain.IngestRun, 0, len(cfg.Ingests))
	for _, ingestCfg := range cfg.Ingests {
		if !ingestCfg.Enabled {
			continue
		}
		run, err := s.pipeline.Run(ctx, ingestCfg)
		if err != nil {
			return nil, fmt.Errorf("run ingest %q: %w", ingestCfg.Name, err)
		}
		results = append(results, run)
	}
	return results, nil
}

func (s *FragmentService) Search(ctx context.Context, query string, limit int) ([]domain.SearchResult, error) {
	return s.recall.Search(ctx, query, limit)
}

func (s *FragmentService) SearchFiltered(ctx context.Context, query, entityKind, entityValue string, limit int) ([]domain.SearchResult, error) {
	if entityKind == "" || entityValue == "" {
		return s.Search(ctx, query, limit)
	}
	entityMatches, err := s.recall.ListFragmentsByEntity(ctx, entityKind, entityValue, max(limit, 200))
	if err != nil {
		return nil, err
	}
	if query == "" {
		if limit > 0 && len(entityMatches) > limit {
			return entityMatches[:limit], nil
		}
		return entityMatches, nil
	}
	searchResults, err := s.recall.Search(ctx, query, max(limit, 50))
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]domain.SearchResult, len(entityMatches))
	for _, item := range entityMatches {
		allowed[item.Fragment.ID] = item
	}
	filtered := make([]domain.SearchResult, 0, limit)
	for _, item := range searchResults {
		if _, ok := allowed[item.Fragment.ID]; !ok {
			continue
		}
		filtered = append(filtered, item)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}

func (s *FragmentService) GetDetail(ctx context.Context, fragmentID string, relatedLimit int) (domain.FragmentDetail, error) {
	fragment, err := s.recall.GetFragment(ctx, fragmentID)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	entities, err := s.entities.ListByFragment(ctx, fragmentID)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	attachments, err := s.attachments.ListByFragment(ctx, fragmentID)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	related, err := s.recall.Related(ctx, fragmentID, relatedLimit)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	relations, err := s.repo.ListRelations(ctx, fragmentID, relatedLimit)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	routeLog, err := s.routes.ListRouteLog(ctx, fragmentID)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	return domain.FragmentDetail{
		Fragment:    fragment,
		Entities:    entities,
		Attachments: attachments,
		RouteLog:    routeLog,
		Relations:   relations,
		Related:     related,
	}, nil
}

func (s *FragmentService) Related(ctx context.Context, fragmentID string, limit int) ([]domain.SearchResult, error) {
	return s.recall.Related(ctx, fragmentID, limit)
}

func (s *FragmentService) ListEntities(ctx context.Context, kind string, limit int) ([]repository.EntityRecord, error) {
	return s.recall.ListEntities(ctx, kind, limit)
}

func (s *FragmentService) FragmentsByEntity(ctx context.Context, kind, value string, limit int) ([]domain.SearchResult, error) {
	return s.recall.ListFragmentsByEntity(ctx, kind, value, limit)
}

func (s *FragmentService) ReanalyzeAttachments(ctx context.Context, fragmentID, attachmentID string) (domain.AttachmentReanalysisResult, error) {
	result := domain.AttachmentReanalysisResult{FragmentID: fragmentID}
	if s.vision == nil {
		return result, fmt.Errorf("attachment vision analyzer is not configured")
	}
	items, err := s.attachments.ListByFragment(ctx, fragmentID)
	if err != nil {
		return result, err
	}
	for _, item := range items {
		if attachmentID != "" && item.ID != attachmentID {
			continue
		}
		result.AttachmentIDs = append(result.AttachmentIDs, item.ID)
		if item.Kind != "image" || item.SourcePath == "" {
			result.SkippedCount++
			continue
		}
		meta := item.Metadata
		if meta == nil {
			meta = map[string]any{}
		}
		out, err := extract.ExtractLocalAttachment(item.SourcePath, item.MIMEType, item.Kind)
		if err == nil {
			for key, value := range out.Metadata {
				meta[key] = value
			}
			enriched := analyze.EnrichAttachmentMetadata(domain.PipelineAttachment{
				Kind:     item.Kind,
				Metadata: meta,
			})
			meta = enriched.Metadata
		}
		visionOut, err := s.vision.AnalyzeImage(ctx, item.SourcePath, meta)
		meta["vision_analysis_backend"] = s.vision.Backend()
		delete(meta, "vision_analysis")
		delete(meta, "vision_analysis_error")
		if err != nil {
			meta["vision_analysis_error"] = err.Error()
		} else {
			meta["vision_analysis"] = visionOut
		}
		if err := s.attachments.UpdateFragmentAttachmentMetadata(ctx, fragmentID, item.ID, meta); err != nil {
			return result, err
		}
		result.UpdatedCount++
	}
	result.ProviderBackend = s.vision.Backend()
	return result, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
