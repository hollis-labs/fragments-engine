package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type AttachmentRepository struct {
	db *sql.DB
}

func NewAttachmentRepository(db *sql.DB) *AttachmentRepository {
	return &AttachmentRepository{db: db}
}

func (r *AttachmentRepository) ReplaceFragmentAttachments(ctx context.Context, fragmentID string, attachments []domain.PipelineAttachment, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attachment tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fragment_attachments WHERE fragment_id = ?`, fragmentID); err != nil {
		return fmt.Errorf("clear fragment attachments: %w", err)
	}
	for _, item := range attachments {
		normalized := normalizePipelineAttachment(item)
		if normalized.Kind == "" {
			continue
		}
		attachmentID := attachmentIdentity(normalized)
		metaJSON := "{}"
		if len(normalized.Metadata) > 0 {
			raw, err := json.Marshal(normalized.Metadata)
			if err != nil {
				return fmt.Errorf("encode attachment metadata: %w", err)
			}
			metaJSON = string(raw)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO attachments (
  id, kind, name, mime_type, source_path, external_url, storage_path, size_bytes, metadata_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  mime_type = excluded.mime_type,
  source_path = excluded.source_path,
  external_url = excluded.external_url,
  storage_path = excluded.storage_path,
  size_bytes = excluded.size_bytes,
  metadata_json = excluded.metadata_json`,
			attachmentID,
			normalized.Kind,
			normalized.Name,
			normalized.MIMEType,
			normalized.SourcePath,
			normalized.ExternalURL,
			normalized.StoragePath,
			normalized.SizeBytes,
			metaJSON,
			now.Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("upsert attachment: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO fragment_attachments (
  fragment_id, attachment_id, role, source, source_item_id, metadata_json, storage_path, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fragmentID,
			attachmentID,
			normalized.Role,
			normalized.Source,
			normalized.SourceItemID,
			metaJSON,
			normalized.StoragePath,
			now.Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("link fragment attachment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attachment tx: %w", err)
	}
	return nil
}

func (r *AttachmentRepository) ListByFragment(ctx context.Context, fragmentID string) ([]domain.FragmentAttachment, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT
  a.id, a.kind, fa.role, a.name, a.mime_type, a.source_path, a.external_url, fa.storage_path, fa.preview_storage_path,
  a.size_bytes, fa.source, fa.source_item_id, fa.metadata_json, fa.created_at
FROM fragment_attachments fa
JOIN attachments a ON a.id = fa.attachment_id
WHERE fa.fragment_id = ?
ORDER BY fa.created_at ASC, a.name ASC`, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("list fragment attachments: %w", err)
	}
	defer rows.Close()

	var items []domain.FragmentAttachment
	for rows.Next() {
		var item domain.FragmentAttachment
		var createdAt string
		if err := rows.Scan(
			&item.ID,
			&item.Kind,
			&item.Role,
			&item.Name,
			&item.MIMEType,
			&item.SourcePath,
			&item.ExternalURL,
			&item.StoragePath,
			&item.PreviewStoragePath,
			&item.SizeBytes,
			&item.Source,
			&item.SourceItemID,
			&item.MetadataJSON,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan fragment attachment: %w", err)
		}
		if strings.TrimSpace(item.MetadataJSON) != "" && item.MetadataJSON != "{}" {
			_ = json.Unmarshal([]byte(item.MetadataJSON), &item.Metadata)
			populateAttachmentAnalysisFields(&item)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fragment attachments: %w", err)
	}
	return items, nil
}

func populateAttachmentAnalysisFields(item *domain.FragmentAttachment) {
	if item == nil || len(item.Metadata) == 0 {
		return
	}
	if analysis, ok := item.Metadata["analysis"].(map[string]any); ok {
		item.AnalysisSummary = metaString(analysis, "summary")
		item.AnalysisTags = metaStringSlice(analysis, "tags")
	}
	item.VisionBackend = metaString(item.Metadata, "vision_analysis_backend")
	item.VisionAnalysisBackend = item.VisionBackend
	if vision, ok := item.Metadata["vision_analysis"].(map[string]any); ok {
		item.VisionSummary = metaString(vision, "summary")
		item.VisionAnalysis = item.VisionSummary
		item.VisionTags = metaStringSlice(vision, "tags")
		item.VisionEntities = metaStringSlice(vision, "entities")
		if present, ok := metaBool(vision, "text_present"); ok {
			item.VisionTextPresent = &present
		}
		if confidence, ok := metaFloat64(vision, "confidence"); ok {
			item.VisionConfidence = &confidence
		}
	}
	// Extracted-text (OCR) fields written by the attachment-enrichment stage.
	item.ExtractedTextPreview = metaString(item.Metadata, "extracted_text_preview")
	item.OCRStatus = metaString(item.Metadata, "ocr_status")
	if bytes, ok := metaFloat64(item.Metadata, "extracted_text_bytes"); ok {
		item.ExtractedTextBytes = int(bytes)
	}
}

func metaString(meta map[string]any, key string) string {
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

func metaStringSlice(meta map[string]any, key string) []string {
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]string); ok {
			return typed
		}
		return nil
	}
	items := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		items = append(items, text)
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func metaBool(meta map[string]any, key string) (bool, bool) {
	raw, ok := meta[key]
	if !ok {
		return false, false
	}
	value, ok := raw.(bool)
	return value, ok
}

func metaFloat64(meta map[string]any, key string) (float64, bool) {
	raw, ok := meta[key]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		f, err := value.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func (r *AttachmentRepository) UpdateFragmentAttachmentStoragePaths(ctx context.Context, fragmentID string, storage map[string]domain.PublishedAttachmentInfo) error {
	for attachmentID, published := range storage {
		if _, err := r.db.ExecContext(ctx, `
UPDATE fragment_attachments
SET storage_path = ?, preview_storage_path = ?
WHERE fragment_id = ? AND attachment_id = ?`,
			published.StoragePath,
			published.PreviewStoragePath,
			fragmentID,
			attachmentID,
		); err != nil {
			return fmt.Errorf("update fragment attachment storage path: %w", err)
		}
	}
	return nil
}

func (r *AttachmentRepository) UpdateFragmentAttachmentMetadata(ctx context.Context, fragmentID, attachmentID string, metadata map[string]any) error {
	metaJSON := "{}"
	if len(metadata) > 0 {
		raw, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("encode attachment metadata: %w", err)
		}
		metaJSON = string(raw)
	}
	if _, err := r.db.ExecContext(ctx, `
UPDATE attachments
SET metadata_json = ?
WHERE id = ?`, metaJSON, attachmentID); err != nil {
		return fmt.Errorf("update attachments metadata: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `
UPDATE fragment_attachments
SET metadata_json = ?
WHERE fragment_id = ? AND attachment_id = ?`, metaJSON, fragmentID, attachmentID); err != nil {
		return fmt.Errorf("update fragment_attachments metadata: %w", err)
	}
	return nil
}

func normalizePipelineAttachment(in domain.PipelineAttachment) domain.PipelineAttachment {
	in.Kind = strings.TrimSpace(strings.ToLower(in.Kind))
	in.Role = strings.TrimSpace(strings.ToLower(in.Role))
	in.Name = strings.TrimSpace(in.Name)
	in.MIMEType = strings.TrimSpace(strings.ToLower(in.MIMEType))
	in.SourcePath = strings.TrimSpace(in.SourcePath)
	in.ExternalURL = strings.TrimSpace(in.ExternalURL)
	in.StoragePath = strings.TrimSpace(in.StoragePath)
	in.Source = strings.TrimSpace(in.Source)
	in.SourceItemID = strings.TrimSpace(in.SourceItemID)
	if in.Role == "" {
		in.Role = "attachment"
	}
	if in.Source == "" {
		in.Source = "ingest"
	}
	return in
}

func attachmentIdentity(in domain.PipelineAttachment) string {
	key := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(in.Kind)),
		strings.TrimSpace(in.SourcePath),
		strings.TrimSpace(in.ExternalURL),
		strings.TrimSpace(in.Name),
	}, "\n")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
