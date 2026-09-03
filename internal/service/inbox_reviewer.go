package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type InboxReviewerService struct {
	fragments   *repository.FragmentRepository
	entities    *repository.EntityRepository
	attachments *repository.AttachmentRepository
	inbox       *repository.InboxRepository
	enricher    *ManualIntakeEnricher
	corpus      *PinterestCorpusWriter
	stackClient *StackExplorerClient
	stackScan   string
	now         func() time.Time
}

func NewInboxReviewerService(
	fragments *repository.FragmentRepository,
	entities *repository.EntityRepository,
	attachments *repository.AttachmentRepository,
	inbox *repository.InboxRepository,
	enricher *ManualIntakeEnricher,
	corpus *PinterestCorpusWriter,
	stackClient *StackExplorerClient,
	stackScan string,
) *InboxReviewerService {
	return &InboxReviewerService{
		fragments:   fragments,
		entities:    entities,
		attachments: attachments,
		inbox:       inbox,
		enricher:    enricher,
		corpus:      corpus,
		stackClient: stackClient,
		stackScan:   strings.TrimSpace(stackScan),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

func (s *InboxReviewerService) ReviewOnce(ctx context.Context, limit int) (domain.InboxReviewResult, error) {
	items, err := s.inbox.ListDetailedOldestFirst(ctx, limit)
	if err != nil {
		return domain.InboxReviewResult{}, err
	}
	result := domain.InboxReviewResult{
		Items: make([]domain.InboxReviewItemResult, 0, len(items)),
	}
	for _, item := range items {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		reviewed, updated, detail, err := s.reviewFragment(ctx, item.FragmentID)
		if err != nil {
			return result, err
		}
		if reviewed {
			result.ReviewedCount++
		} else {
			result.SkippedCount++
		}
		if updated {
			result.UpdatedCount++
		}
		result.Items = append(result.Items, domain.InboxReviewItemResult{
			FragmentID: item.FragmentID,
			Title:      item.Title,
			Action:     detail.action,
			Updated:    updated,
			Detail:     detail.detail,
		})
	}
	return result, nil
}

type reviewDetail struct {
	action string
	detail string
}

func (s *InboxReviewerService) reviewFragment(ctx context.Context, fragmentID string) (reviewed, updated bool, detail reviewDetail, err error) {
	fragment, err := s.fragments.GetByID(ctx, fragmentID)
	if err != nil {
		return false, false, detail, err
	}
	fragmentID = fragment.ID
	if fragment.Source != "manual" {
		return false, false, reviewDetail{action: "skip_non_manual"}, nil
	}
	meta := decodeFragmentMetadata(fragment.MetadataJSON)
	if currentMetaValue(meta, "review_version") == inboxReviewerVersion && !s.needsReReview(fragment, meta) {
		return false, false, reviewDetail{action: "skip_already_reviewed"}, nil
	}
	attachments, err := s.attachments.ListByFragment(ctx, fragmentID)
	if err != nil {
		return false, false, detail, err
	}
	currentEntities, err := s.entities.ListByFragment(ctx, fragmentID)
	if err != nil {
		return false, false, detail, err
	}

	enriched, shouldReview, err := s.enricher.ReviewURL(ctx, fragment, meta)
	if err != nil {
		return false, false, detail, err
	}
	if !shouldReview {
		return false, false, reviewDetail{action: "skip_unsupported"}, nil
	}
	s.syncRepoToStackExplorer(ctx, &enriched)
	if err := s.applyEnrichment(ctx, fragment, attachments, currentEntities, enriched); err != nil {
		return true, false, detail, err
	}
	reason := "awaiting routing; reviewed"
	if enriched.SourceType == "repo" {
		reason = "awaiting routing; reviewed github repo"
	}
	if enriched.SourceType == "pin" {
		reason = "awaiting routing; reviewed pinterest pin"
	}
	if err := s.inbox.UpdateReason(ctx, fragmentID, reason); err != nil {
		return true, false, detail, err
	}
	if err := s.syncPinterestCorpus(ctx, fragmentID); err != nil {
		return true, false, detail, err
	}
	return true, true, reviewDetail{
		action: "reviewed_" + enriched.SourceType,
		detail: strings.TrimSpace(enriched.Summary),
	}, nil
}

func (s *InboxReviewerService) syncPinterestCorpus(ctx context.Context, fragmentID string) error {
	if s == nil || s.corpus == nil {
		return nil
	}
	fragment, err := s.fragments.GetByID(ctx, fragmentID)
	if err != nil {
		return err
	}
	fragmentID = fragment.ID
	if fragment.Source != "manual" || fragment.SourceType != "pin" {
		return nil
	}
	entities, err := s.entities.ListByFragment(ctx, fragmentID)
	if err != nil {
		return err
	}
	attachments, err := s.attachments.ListByFragment(ctx, fragmentID)
	if err != nil {
		return err
	}
	_, err = s.corpus.Write(domain.FragmentDetail{
		Fragment:    fragment,
		Entities:    entities,
		Attachments: attachments,
	})
	return err
}

func (s *InboxReviewerService) applyEnrichment(
	ctx context.Context,
	fragment domain.Fragment,
	currentAttachments []domain.FragmentAttachment,
	currentEntities []domain.FragmentEntity,
	enriched ManualEnrichment,
) error {
	metaJSON, err := json.Marshal(enriched.Metadata)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(enriched.Title)
	if title == "" {
		title = fragment.Title
	}
	sourceType := strings.TrimSpace(enriched.SourceType)
	if sourceType == "" {
		sourceType = fragment.SourceType
	}
	canonicalPath := strings.TrimSpace(enriched.CanonicalPath)
	if canonicalPath == "" {
		canonicalPath = fragment.CanonicalPath
	}
	if err := s.fragments.UpdateDerivedFields(ctx, fragment.ID, title, sourceType, string(metaJSON), canonicalPath); err != nil {
		return err
	}
	if strings.TrimSpace(enriched.Summary) != "" {
		if err := s.fragments.UpdateIndexMetadata(ctx, fragment.ID, enriched.Summary, s.now()); err != nil {
			return err
		}
	}
	allEntities := mergeEntities(currentEntities, enriched.Entities)
	if len(allEntities) > 0 {
		if err := s.entities.ReplaceFragmentEntities(ctx, fragment.ID, allEntities); err != nil {
			return err
		}
	}
	if len(enriched.Attachments) > 0 || len(currentAttachments) > 0 {
		mergedAttachments := mergeAttachments(currentAttachments, enriched.Attachments)
		if err := s.attachments.ReplaceFragmentAttachments(ctx, fragment.ID, mergedAttachments, s.now()); err != nil {
			return err
		}
		storage := map[string]domain.PublishedAttachmentInfo{}
		for _, item := range mergedAttachments {
			if strings.TrimSpace(item.StoragePath) == "" && strings.TrimSpace(metadataString(item.Metadata, "preview_storage_path")) == "" {
				continue
			}
			attachmentID := matchAttachmentID(fragment.ID, item, currentAttachments)
			if attachmentID == "" {
				fresh, err := s.attachments.ListByFragment(ctx, fragment.ID)
				if err != nil {
					return err
				}
				attachmentID = matchAttachmentID(fragment.ID, item, fresh)
			}
			if attachmentID == "" {
				continue
			}
			storage[attachmentID] = domain.PublishedAttachmentInfo{
				StoragePath:        item.StoragePath,
				PreviewStoragePath: metadataString(item.Metadata, "preview_storage_path"),
			}
		}
		if len(storage) > 0 {
			if err := s.attachments.UpdateFragmentAttachmentStoragePaths(ctx, fragment.ID, storage); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeFragmentMetadata(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func mergeEntities(current, additions []domain.FragmentEntity) []domain.FragmentEntity {
	out := make([]domain.FragmentEntity, 0, len(current)+len(additions))
	seen := map[string]struct{}{}
	for _, item := range append(current, additions...) {
		if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.Value) == "" {
			continue
		}
		key := item.Kind + "\n" + item.Value + "\n" + item.Source
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func mergeAttachments(current []domain.FragmentAttachment, additions []domain.PipelineAttachment) []domain.PipelineAttachment {
	out := make([]domain.PipelineAttachment, 0, len(current)+len(additions))
	seen := map[string]struct{}{}
	for _, item := range current {
		normalized := domain.PipelineAttachment{
			Kind:         item.Kind,
			Role:         item.Role,
			Name:         item.Name,
			MIMEType:     item.MIMEType,
			SourcePath:   item.SourcePath,
			ExternalURL:  item.ExternalURL,
			StoragePath:  item.StoragePath,
			SizeBytes:    item.SizeBytes,
			Metadata:     item.Metadata,
			Source:       item.Source,
			SourceItemID: item.SourceItemID,
		}
		key := attachmentMergeKey(normalized)
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	for _, item := range additions {
		key := attachmentMergeKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func attachmentMergeKey(item domain.PipelineAttachment) string {
	return strings.Join([]string{
		strings.TrimSpace(item.Kind),
		strings.TrimSpace(item.Name),
		strings.TrimSpace(item.SourcePath),
		strings.TrimSpace(item.ExternalURL),
		strings.TrimSpace(item.Source),
		strings.TrimSpace(item.SourceItemID),
	}, "\n")
}

type attachmentLike interface {
	GetID() string
	GetSourcePath() string
	GetExternalURL() string
	GetName() string
}

func matchAttachmentID[T any](fragmentID string, item domain.PipelineAttachment, current []T) string {
	for _, raw := range current {
		switch typed := any(raw).(type) {
		case domain.FragmentAttachment:
			if typed.SourcePath == item.SourcePath && typed.ExternalURL == item.ExternalURL && typed.Name == item.Name {
				return typed.ID
			}
		}
	}
	return ""
}

func metadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	raw, ok := meta[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *InboxReviewerService) needsReReview(fragment domain.Fragment, meta map[string]any) bool {
	// A fragment left "pending" by ReviewURL's generic-URL fallback retry
	// (see ManualIntakeEnricher.ReviewURL's default branch) always needs
	// another look, regardless of source type -- ReviewURL sets
	// review_version on every pass (success or still-pending), so without
	// this check reviewFragment's top-level "review_version already
	// matches" gate would permanently skip it after the very first pass.
	if currentMetaValue(meta, "enrichment_status") == "pending" {
		return true
	}
	if strings.TrimSpace(fragment.SourceType) == "pin" {
		return currentMetaValue(meta, "pin_image_url") == ""
	}
	if strings.TrimSpace(fragment.SourceType) == "repo" && s.stackClient != nil {
		if currentMetaValue(meta, "stack_explorer_synced_at") == "" {
			return true
		}
		if currentMetaValue(meta, "stack_explorer_tag_synced_at") == "" || currentMetaValue(meta, "stack_explorer_tag_sync_status") == "error" {
			return true
		}
		if stackExplorerTagCount(meta) < len(stackExplorerTags(meta)) {
			return true
		}
		if s.stackScan == "" {
			return false
		}
		if currentMetaValue(meta, "stack_explorer_scan_id") == "" {
			return true
		}
		return currentMetaValue(meta, "stack_explorer_scan_repo_id") != currentMetaValue(meta, "stack_explorer_repo_id")
	}
	return false
}

func (s *InboxReviewerService) syncRepoToStackExplorer(ctx context.Context, enriched *ManualEnrichment) {
	if s == nil || s.stackClient == nil || enriched == nil || strings.TrimSpace(enriched.SourceType) != "repo" {
		return
	}
	meta := enriched.Metadata
	if meta == nil {
		meta = map[string]any{}
		enriched.Metadata = meta
	}
	owner := currentMetaValue(meta, "repo_owner")
	repo := currentMetaValue(meta, "repo_name")
	if owner == "" || repo == "" {
		return
	}
	record := StackExplorerRepoRecord{
		ID:          stackExplorerRepoID(owner, repo),
		Name:        firstNonEmpty(currentMetaValue(meta, "repo_full_name"), enriched.Title, owner+"/"+repo),
		URL:         currentMetaValue(meta, "url"),
		Description: firstNonEmpty(currentMetaValue(meta, "repo_description"), strings.TrimPrefix(enriched.Summary, "GitHub repo: ")),
		Stack:       strings.ToLower(currentMetaValue(meta, "repo_language")),
		Category:    "other",
		Tags:        stackExplorerTags(meta),
	}
	result, err := s.stackClient.UpsertRepo(ctx, record)
	if err != nil {
		meta["stack_explorer_sync_status"] = "error"
		meta["stack_explorer_sync_error"] = err.Error()
		return
	}
	meta["stack_explorer_repo_id"] = result.RepoID
	meta["stack_explorer_sync_status"] = result.Status
	meta["stack_explorer_synced_at"] = s.now().Format(time.RFC3339)
	delete(meta, "stack_explorer_sync_error")
	tags := stackExplorerTags(meta)
	if len(tags) > 0 {
		tagSync, err := s.stackClient.AddRepoTags(ctx, result.RepoID, tags)
		if err != nil {
			meta["stack_explorer_tag_sync_status"] = "error"
			meta["stack_explorer_tag_sync_error"] = err.Error()
		} else {
			meta["stack_explorer_tags"] = tagSync.Tags
			meta["stack_explorer_tag_sync_status"] = "synced"
			meta["stack_explorer_tag_synced_at"] = s.now().Format(time.RFC3339)
			delete(meta, "stack_explorer_tag_sync_error")
		}
	}
	if s.stackScan != "" && shouldCreateStackExplorerScan(meta, result.RepoID, s.stackScan) {
		scan, err := s.stackClient.EnsureScan(ctx, result.RepoID, s.stackScan)
		if err != nil {
			meta["stack_explorer_scan_status"] = "error"
			meta["stack_explorer_scan_error"] = err.Error()
			return
		}
		meta["stack_explorer_scan_blueprint"] = s.stackScan
		meta["stack_explorer_scan_id"] = scan.ScanID
		meta["stack_explorer_scan_repo_id"] = result.RepoID
		meta["stack_explorer_scan_status"] = scan.Status
		meta["stack_explorer_scan_synced_at"] = s.now().Format(time.RFC3339)
		delete(meta, "stack_explorer_scan_error")
	}
}

func stackExplorerTags(meta map[string]any) []string {
	var tags []string
	tags = append(tags, "github")
	if items, ok := meta["repo_topics"].([]string); ok {
		tags = append(tags, items...)
	}
	if items, ok := meta["repo_topics"].([]any); ok {
		for _, item := range items {
			if text, ok := item.(string); ok {
				tags = append(tags, text)
			}
		}
	}
	if items, ok := meta["input_tags"].([]string); ok {
		tags = append(tags, items...)
	}
	if items, ok := meta["input_tags"].([]any); ok {
		for _, item := range items {
			if text, ok := item.(string); ok {
				tags = append(tags, text)
			}
		}
	}
	return dedupeStrings(tags)
}

func stackExplorerTagCount(meta map[string]any) int {
	if meta == nil {
		return 0
	}
	if items, ok := meta["stack_explorer_tags"].([]string); ok {
		return len(dedupeStrings(items))
	}
	if items, ok := meta["stack_explorer_tags"].([]any); ok {
		count := 0
		for _, item := range items {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				count++
			}
		}
		return count
	}
	return 0
}

func shouldCreateStackExplorerScan(meta map[string]any, repoID, blueprint string) bool {
	if strings.TrimSpace(repoID) == "" || strings.TrimSpace(blueprint) == "" {
		return false
	}
	if currentMetaValue(meta, "stack_explorer_scan_id") == "" {
		return true
	}
	if currentMetaValue(meta, "stack_explorer_scan_repo_id") != repoID {
		return true
	}
	if currentMetaValue(meta, "stack_explorer_scan_blueprint") != blueprint {
		return true
	}
	switch currentMetaValue(meta, "stack_explorer_scan_status") {
	case "", "error":
		return true
	default:
		return false
	}
}
