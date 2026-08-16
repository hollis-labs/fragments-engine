package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	queue "github.com/hollis-labs/go-queue"

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
	enricher    *ManualIntakeEnricher
	corpus      *PinterestCorpusWriter
}

func NewFragmentService(repo *repository.FragmentRepository, entities *repository.EntityRepository, attachments *repository.AttachmentRepository, routes *repository.RoutingRepository, recallIndex recall.Indexer, pipeline *ingest.Pipeline, vision analyze.VisionAnalyzer, enricher *ManualIntakeEnricher, corpus *PinterestCorpusWriter) *FragmentService {
	return &FragmentService{
		repo:        repo,
		entities:    entities,
		attachments: attachments,
		routes:      routes,
		recall:      recallIndex,
		pipeline:    pipeline,
		vision:      vision,
		enricher:    enricher,
		corpus:      corpus,
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

// go-queue identifiers for async ingest jobs. Shared between the enqueue path
// (EnqueueIngestRun) and the worker that registers the handler.
const (
	IngestQueueName  = "ingests"
	IngestRunJobType = "ingest_run"
)

// IngestRunPayload is the go-queue job payload for an ingest run. RunID points
// at the ingest_runs row the worker updates as it executes.
type IngestRunPayload struct {
	RunID      int64  `json:"run_id"`
	IngestName string `json:"ingest_name"`
}

// EnqueuedIngestRun identifies a queued ingest run returned to API callers.
type EnqueuedIngestRun struct {
	RunID      int64  `json:"run_id"`
	IngestName string `json:"ingest_name"`
}

// EnqueueIngestRun creates a queued ingest_runs row and pushes a go-queue job
// for the worker to execute. Returns the run id immediately.
func (s *FragmentService) EnqueueIngestRun(ctx context.Context, q queue.Queue, ingestCfg config.IngestConfig) (int64, error) {
	runID, err := s.repo.CreateIngestRun(ctx, ingestCfg.Name, ingestCfg.Kind)
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(IngestRunPayload{RunID: runID, IngestName: ingestCfg.Name})
	if err != nil {
		return 0, err
	}
	if err := q.Push(ctx, IngestRunJobType, payload, queue.OnQueue(IngestQueueName)); err != nil {
		_ = s.repo.FailIngestRun(ctx, runID, time.Now().UTC(), "enqueue failed: "+err.Error())
		return 0, fmt.Errorf("enqueue ingest run: %w", err)
	}
	return runID, nil
}

// EnqueueAllIngests queues a run for every enabled ingest source.
func (s *FragmentService) EnqueueAllIngests(ctx context.Context, q queue.Queue, cfg config.Config) ([]EnqueuedIngestRun, error) {
	out := make([]EnqueuedIngestRun, 0, len(cfg.Ingests))
	for _, ingestCfg := range cfg.Ingests {
		if !ingestCfg.Enabled {
			continue
		}
		runID, err := s.EnqueueIngestRun(ctx, q, ingestCfg)
		if err != nil {
			return out, err
		}
		out = append(out, EnqueuedIngestRun{RunID: runID, IngestName: ingestCfg.Name})
	}
	return out, nil
}

// ExecuteIngestRun runs a single ingest against a pre-created run row, driving
// it through running → done/failed. Called by the go-queue worker.
func (s *FragmentService) ExecuteIngestRun(ctx context.Context, runID int64, ingestCfg config.IngestConfig) error {
	if err := s.repo.MarkIngestRunRunning(ctx, runID, time.Now().UTC()); err != nil {
		return err
	}
	run, err := s.pipeline.RunOnce(ctx, ingestCfg)
	if err != nil {
		_ = s.repo.FailIngestRun(ctx, runID, time.Now().UTC(), err.Error())
		return err
	}
	return s.repo.CompleteIngestRun(ctx, runID, run, time.Now().UTC())
}

// FailIngestRun marks a run failed; used when a worker cannot resolve a job's
// target ingest before ExecuteIngestRun takes over the row lifecycle.
func (s *FragmentService) FailIngestRun(ctx context.Context, runID int64, errMsg string) error {
	return s.repo.FailIngestRun(ctx, runID, time.Now().UTC(), errMsg)
}

// ListIngestRuns returns recent ingest runs, newest first.
func (s *FragmentService) ListIngestRuns(ctx context.Context, limit int) ([]domain.IngestRunRecord, error) {
	return s.repo.ListIngestRuns(ctx, limit)
}

func (s *FragmentService) Search(ctx context.Context, query string, limit int) ([]domain.SearchResult, error) {
	return s.recall.Search(ctx, query, limit)
}

// MaxSearchLimit caps the result count a single search may request. It bounds
// downstream slice preallocation and over-fetch (limit*4), so a large
// user-supplied limit cannot exhaust server memory.
const MaxSearchLimit = 200

func (s *FragmentService) SearchFiltered(ctx context.Context, query, entityKind, entityValue string, limit int) ([]domain.SearchResult, error) {
	results, _, err := s.SearchFilteredMode(ctx, query, entityKind, entityValue, "", recall.ModeAuto, limit)
	return results, err
}

// SearchFilteredMode is the mode-aware search surface. It threads an explicit
// retrieval mode down to the recall layer, applies the optional entity filter
// and fragment-status filter, and reports the mode that actually ran. When
// status is non-empty, only fragments with that status are returned.
func (s *FragmentService) SearchFilteredMode(ctx context.Context, query, entityKind, entityValue, status string, mode recall.SearchMode, limit int) ([]domain.SearchResult, recall.SearchMode, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	statusFilter := domain.FragmentStatus(strings.TrimSpace(status))
	keep := func(item domain.SearchResult) bool {
		return statusFilter == "" || item.Fragment.Status == statusFilter
	}

	// No entity filter: a plain mode-aware recall search, then status filter.
	if entityKind == "" || entityValue == "" {
		fetch := limit
		if statusFilter != "" {
			fetch = max(limit*4, 50)
		}
		results, used, err := s.recall.SearchMode(ctx, query, fetch, mode)
		if err != nil {
			return nil, used, err
		}
		return applyStatusLimit(results, keep, limit), used, nil
	}

	entityMatches, err := s.recall.ListFragmentsByEntity(ctx, entityKind, entityValue, max(limit*4, 200))
	if err != nil {
		return nil, mode, err
	}
	// Entity filter with no query: entity matches are not produced by the
	// recall search layer, so the effective mode used is keyword.
	if query == "" {
		return applyStatusLimit(entityMatches, keep, limit), recall.ModeKeyword, nil
	}
	searchResults, used, err := s.recall.SearchMode(ctx, query, max(limit*4, 50), mode)
	if err != nil {
		return nil, used, err
	}
	allowed := make(map[string]struct{}, len(entityMatches))
	for _, item := range entityMatches {
		allowed[item.Fragment.ID] = struct{}{}
	}
	filtered := make([]domain.SearchResult, 0, limit)
	for _, item := range searchResults {
		if _, ok := allowed[item.Fragment.ID]; !ok {
			continue
		}
		if !keep(item) {
			continue
		}
		filtered = append(filtered, item)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered, used, nil
}

// applyStatusLimit filters results by the keep predicate and truncates to
// limit. It always returns a non-nil slice.
func applyStatusLimit(results []domain.SearchResult, keep func(domain.SearchResult) bool, limit int) []domain.SearchResult {
	out := make([]domain.SearchResult, 0, limit)
	for _, item := range results {
		if !keep(item) {
			continue
		}
		out = append(out, item)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
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

// List returns fragments newest-first with an optional status filter, plus the
// total count of the filtered set for pagination.
func (s *FragmentService) List(ctx context.Context, status string, limit, offset int) ([]domain.Fragment, int, error) {
	return s.repo.List(ctx, repository.ListOptions{Status: domain.FragmentStatus(status), Limit: limit, Offset: offset})
}

func (s *FragmentService) ListBrowse(ctx context.Context, status string, limit, offset int) ([]domain.FragmentBrowseItem, int, error) {
	return s.repo.ListBrowse(ctx, repository.ListOptions{Status: domain.FragmentStatus(status), Limit: limit, Offset: offset})
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

// IntakeRequest is a manually submitted fragment (e.g. from Raycast, CLI, etc.)
type IntakeRequest struct {
	Content    string   // required
	Title      string   // optional; derived from content if blank
	SourceType string   // optional hint: "url", "youtube", "code", "markdown", "text"
	Tags       []string // optional tag entities written as kind="tag"
}

// IntakeResult is returned after a successful intake.
type IntakeResult struct {
	FragmentID string `json:"fragment_id"`
	Outcome    string `json:"outcome"` // "inserted" | "updated" | "skipped"
	Status     string `json:"status"`  // "inbox" | "routed"
	// LinkURL is set when the intake was treated as link-shaped: either
	// req.Content was a bare URL, or the merged tag set (req.Tags plus any
	// "#tag" tokens extracted from content) contained "link" and an
	// http(s) URL was found somewhere in the content. It surfaces the
	// extracted URL for a future enrichment path that shouldn't require
	// req.Content to be exactly the URL.
	LinkURL string `json:"link_url,omitempty"`
}

type UpdateFragmentRequest struct {
	FragmentID string
	Title      string
	Summary    string
	Notes      string
	Tags       []string
	SourceType string
}

func (s *FragmentService) UpdateManualFragment(ctx context.Context, req UpdateFragmentRequest) (domain.FragmentDetail, error) {
	fragmentID := strings.TrimSpace(req.FragmentID)
	if fragmentID == "" {
		return domain.FragmentDetail{}, fmt.Errorf("update fragment: fragment_id is required")
	}

	fragment, err := s.repo.GetByID(ctx, fragmentID)
	if err != nil {
		return domain.FragmentDetail{}, fmt.Errorf("update fragment: %w", err)
	}
	if fragment.Source != "manual" {
		return domain.FragmentDetail{}, fmt.Errorf("update fragment: only manual fragments are editable")
	}

	meta, err := decodeEditableMetadata(fragment.MetadataJSON)
	if err != nil {
		return domain.FragmentDetail{}, fmt.Errorf("update fragment metadata: %w", err)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = fragment.Title
	}
	sourceType := strings.TrimSpace(req.SourceType)
	if sourceType == "" {
		sourceType = fragment.SourceType
	}
	summary := strings.TrimSpace(req.Summary)
	notes := strings.TrimSpace(req.Notes)
	tags := dedupeTagValues(req.Tags)

	if notes == "" {
		delete(meta, "user_notes")
	} else {
		meta["user_notes"] = notes
	}
	if summary == "" {
		delete(meta, "user_description")
	} else {
		meta["user_description"] = summary
	}
	if len(tags) == 0 {
		delete(meta, "user_tags")
	} else {
		meta["user_tags"] = tags
	}

	rawMeta, err := json.Marshal(meta)
	if err != nil {
		return domain.FragmentDetail{}, fmt.Errorf("update fragment metadata json: %w", err)
	}
	existingEntities, err := s.entities.ListByFragment(ctx, fragmentID)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	nextEntities := make([]domain.FragmentEntity, 0, len(existingEntities)+len(tags))
	for _, entity := range existingEntities {
		if entity.Kind == "tag" {
			continue
		}
		nextEntities = append(nextEntities, entity)
	}
	for _, tag := range tags {
		nextEntities = append(nextEntities, domain.FragmentEntity{
			Kind:       "tag",
			Value:      tag,
			Source:     "manual",
			Confidence: 1,
		})
	}

	if err := s.repo.UpdateEditableFields(ctx, fragmentID, title, summary, string(rawMeta)); err != nil {
		return domain.FragmentDetail{}, err
	}
	if err := s.repo.UpdateDerivedFields(ctx, fragmentID, title, sourceType, string(rawMeta), fragment.CanonicalPath); err != nil {
		return domain.FragmentDetail{}, err
	}

	updated, err := s.repo.GetByID(ctx, fragmentID)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	if err := s.recall.IndexFragment(ctx, updated); err != nil {
		return domain.FragmentDetail{}, fmt.Errorf("update fragment reindex: %w", err)
	}
	// Recall indexing recomputes summary/entity state from content. Restore the
	// explicit manual summary/metadata and authoritative tag set afterward.
	if err := s.repo.UpdateEditableFields(ctx, fragmentID, title, summary, string(rawMeta)); err != nil {
		return domain.FragmentDetail{}, err
	}
	if err := s.repo.UpdateDerivedFields(ctx, fragmentID, title, sourceType, string(rawMeta), fragment.CanonicalPath); err != nil {
		return domain.FragmentDetail{}, err
	}
	if err := s.entities.ReplaceFragmentEntities(ctx, fragmentID, nextEntities); err != nil {
		return domain.FragmentDetail{}, err
	}
	detail, err := s.GetDetail(ctx, fragmentID, 10)
	if err != nil {
		return domain.FragmentDetail{}, err
	}
	if _, err := s.writePinterestCorpus(detail); err != nil {
		return domain.FragmentDetail{}, err
	}
	return detail, nil
}

func (s *FragmentService) BackfillPinterestCorpus(ctx context.Context, limit int) (domain.PinterestCorpusBackfillResult, error) {
	if s == nil || s.corpus == nil {
		return domain.PinterestCorpusBackfillResult{}, nil
	}
	if limit < 0 {
		limit = 0
	}

	result := domain.PinterestCorpusBackfillResult{
		WrittenPaths: make([]string, 0),
	}
	offset := 0
	for {
		pageSize := 200
		if limit > 0 {
			remaining := limit - result.CandidateCount
			if remaining <= 0 {
				break
			}
			if remaining < pageSize {
				pageSize = remaining
			}
		}

		items, total, err := s.repo.List(ctx, repository.ListOptions{
			Source:     "manual",
			SourceType: "pin",
			Limit:      pageSize,
			Offset:     offset,
		})
		if err != nil {
			return result, fmt.Errorf("backfill pinterest corpus: list fragments: %w", err)
		}
		if len(items) == 0 {
			break
		}

		result.ScannedCount += len(items)
		for _, fragment := range items {
			result.CandidateCount++
			detail, err := s.GetDetail(ctx, fragment.ID, 10)
			if err != nil {
				return result, fmt.Errorf("backfill pinterest corpus: detail %s: %w", fragment.ID, err)
			}
			path, err := s.writePinterestCorpus(detail)
			if err != nil {
				return result, fmt.Errorf("backfill pinterest corpus: write %s: %w", fragment.ID, err)
			}
			if strings.TrimSpace(path) == "" {
				continue
			}
			result.WrittenCount++
			result.WrittenPaths = append(result.WrittenPaths, path)
		}

		offset += len(items)
		if offset >= total {
			break
		}
	}
	return result, nil
}

// Intake accepts a manually submitted fragment, writes it to the DB, runs all
// pipeline stages (route, inbox, recall), and optionally attaches tag entities.
func (s *FragmentService) Intake(ctx context.Context, req IntakeRequest) (IntakeResult, error) {
	if strings.TrimSpace(req.Content) == "" {
		return IntakeResult{}, fmt.Errorf("intake: content is required")
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = deriveTitle(req.Content)
	}

	// Merge caller-supplied tags with any "#tag" tokens found inline in the
	// content. Content itself is never mutated -- hashtags stay in place.
	mergedTags := dedupeTagValues(append(append([]string{}, req.Tags...), ExtractHashtags(req.Content)...))
	hasLinkTag := containsTagFold(mergedTags, "link")

	requestedSourceType := strings.TrimSpace(req.SourceType)
	sourceType := requestedSourceType
	if sourceType == "" {
		sourceType = detectSourceType(req.Content)
	}

	// A "link" tag makes the intake link-shaped even when the URL is
	// embedded mid-text (e.g. "check this out #link https://example.com"),
	// not just when content is a bare URL. Surface the extracted URL for a
	// future enrichment path, and -- only when the caller didn't force a
	// source type, and detection didn't already land on something more
	// specific like "youtube" -- treat it as a "url" source.
	_, isBareURL := singleURL(req.Content)
	var linkURL string
	if hasLinkTag || isBareURL {
		if url, ok := ExtractFirstURL(req.Content); ok {
			linkURL = url
		}
	}
	if hasLinkTag && linkURL != "" && requestedSourceType == "" && sourceType != "youtube" {
		sourceType = "url"
	}

	now := time.Now().UTC()
	// Use a content-address as the source_id so identical submissions dedup.
	sourceID := hashContent(req.Content)

	var (
		err             error
		enriched        ManualEnrichment
		derivedEntities []domain.FragmentEntity
	)
	if s.enricher != nil {
		enriched, err = s.enricher.EnrichIntake(ctx, req.Content, title, sourceType, req.Tags, linkURL)
		if err != nil {
			return IntakeResult{}, fmt.Errorf("intake: enrich: %w", err)
		}
		if strings.TrimSpace(enriched.Title) != "" {
			title = enriched.Title
		}
		if strings.TrimSpace(enriched.SourceType) != "" {
			sourceType = enriched.SourceType
		}
		derivedEntities = enriched.Entities
	}

	candidate := domain.PipelineFragment{
		Source:        "manual",
		SourceType:    sourceType,
		SourceID:      sourceID,
		Title:         title,
		Content:       req.Content,
		CreatedAt:     now,
		CanonicalPath: "fragments/manual/" + sourceType + "/" + sourceID,
	}
	if strings.TrimSpace(enriched.CanonicalPath) != "" {
		candidate.CanonicalPath = enriched.CanonicalPath
	}
	if len(enriched.Metadata) > 0 {
		candidate.Metadata = enriched.Metadata
	}
	if len(enriched.Attachments) > 0 {
		candidate.Attachments = enriched.Attachments
	}

	fragment, err := repository.BuildFragment(candidate, "manual-intake", now)
	if err != nil {
		return IntakeResult{}, fmt.Errorf("intake: build fragment: %w", err)
	}

	outcome, err := s.repo.Upsert(ctx, fragment)
	if err != nil {
		return IntakeResult{}, fmt.Errorf("intake: upsert: %w", err)
	}

	manualEntities := make([]domain.FragmentEntity, 0, len(mergedTags)+len(derivedEntities))
	// Write manual entities immediately so they exist even if the fragment later skips.
	if len(mergedTags) > 0 || len(derivedEntities) > 0 {
		for _, t := range mergedTags {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			manualEntities = append(manualEntities, domain.FragmentEntity{
				Kind:       "tag",
				Value:      t,
				Source:     "manual-intake",
				Confidence: 1.0,
			})
		}
		manualEntities = append(manualEntities, derivedEntities...)
		if err := s.entities.ReplaceFragmentEntities(ctx, fragment.ID, manualEntities); err != nil {
			return IntakeResult{}, fmt.Errorf("intake: write tags: %w", err)
		}
	}

	if outcome == repository.UpsertSkipped {
		// Fetch the real status from the DB, since BuildFragment always sets inbox.
		existing, fetchErr := s.repo.GetByID(ctx, fragment.ID)
		status := string(fragment.Status)
		if fetchErr == nil {
			status = string(existing.Status)
		}
		return IntakeResult{
			FragmentID: fragment.ID,
			Outcome:    "skipped",
			Status:     status,
			LinkURL:    linkURL,
		}, nil
	}

	// Run through the ingest pipeline stages.
	stageCtx := &ingest.StageContext{
		IngestConfig: config.IngestConfig{Name: "manual-intake", Kind: "manual"},
		Candidate:    candidate,
		Fragment:     fragment,
		Outcome:      outcome,
		Now:          now,
	}
	for _, stage := range s.pipeline.Stages() {
		if err := stage.Run(ctx, stageCtx); err != nil {
			return IntakeResult{}, fmt.Errorf("intake: stage %s: %w", stage.Name(), err)
		}
	}
	if len(manualEntities) > 0 {
		currentEntities, err := s.entities.ListByFragment(ctx, fragment.ID)
		if err != nil {
			return IntakeResult{}, fmt.Errorf("intake: read extracted entities: %w", err)
		}
		mergedEntities := mergeEntities(currentEntities, manualEntities)
		if err := s.entities.ReplaceFragmentEntities(ctx, fragment.ID, mergedEntities); err != nil {
			return IntakeResult{}, fmt.Errorf("intake: merge entities: %w", err)
		}
	}

	return IntakeResult{
		FragmentID: stageCtx.Fragment.ID,
		Outcome:    string(outcome),
		Status:     string(stageCtx.Fragment.Status),
		LinkURL:    linkURL,
	}, nil
}

func decodeEditableMetadata(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	return meta, nil
}

func dedupeTagValues(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func (s *FragmentService) writePinterestCorpus(detail domain.FragmentDetail) (string, error) {
	if s == nil || s.corpus == nil {
		return "", nil
	}
	return s.corpus.Write(detail)
}

func deriveTitle(content string) string {
	content = strings.TrimSpace(content)
	// Use first non-empty line, truncated to 80 chars.
	for _, line := range strings.SplitN(content, "\n", 5) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 80 {
			return line[:80] + "…"
		}
		return line
	}
	return "Manual fragment"
}

func detectSourceType(content string) string {
	c := strings.TrimSpace(content)
	// YouTube
	if strings.Contains(c, "youtube.com/watch") || strings.Contains(c, "youtu.be/") {
		return "youtube"
	}
	// URL
	if strings.HasPrefix(c, "http://") || strings.HasPrefix(c, "https://") {
		// Check it's a single-line URL (no whitespace other than trailing newline)
		if !strings.ContainsAny(strings.TrimSpace(c), " \t\n") {
			return "url"
		}
		return "article"
	}
	// File path
	if strings.HasPrefix(c, "/") || strings.HasPrefix(c, "~/") {
		if !strings.Contains(c, "\n") {
			return "file"
		}
	}
	// Markdown heuristic
	if strings.Contains(c, "```") || strings.Contains(c, "## ") || strings.Contains(c, "**") {
		return "markdown"
	}
	// Code heuristic
	if strings.Contains(c, "func ") || strings.Contains(c, "def ") ||
		strings.Contains(c, "class ") || strings.Contains(c, "import ") ||
		strings.Contains(c, "const ") || strings.Contains(c, "var ") {
		return "code"
	}
	return "text"
}

func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
