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
	tx, err := NewMediaRepository(r.db).BeginImmediate(ctx, "replace fragment attachments")
	if err != nil {
		return fmt.Errorf("begin attachment tx: %w", err)
	}
	defer tx.Rollback()
	canonicalID, err := resolveCanonicalFragmentID(ctx, tx.Conn(), fragmentID)
	if err != nil {
		return fmt.Errorf("resolve attachment fragment: %w", err)
	}
	revisionID, err := ensureAttachmentManifestRevision(ctx, tx.Conn(), canonicalID, attachments, now)
	if err != nil {
		return err
	}
	if err := replaceFragmentRevisionAttachments(ctx, tx.Conn(), canonicalID, revisionID, attachments, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attachment tx: %w", err)
	}
	return nil
}

// ReplaceFragmentRevisionAttachments is the ingest path: fragment resolution
// already created the immutable revision from this exact ordered manifest, so
// the stage only installs its legacy projection and immutable AttachmentRefs.
func (r *AttachmentRepository) ReplaceFragmentRevisionAttachments(ctx context.Context, fragmentID, revisionID string, attachments []domain.PipelineAttachment, now time.Time) error {
	tx, err := NewMediaRepository(r.db).BeginImmediate(ctx, "replace fragment revision attachments")
	if err != nil {
		return fmt.Errorf("begin attachment tx: %w", err)
	}
	defer tx.Rollback()
	canonicalID, err := resolveCanonicalFragmentID(ctx, tx.Conn(), fragmentID)
	if err != nil {
		return fmt.Errorf("resolve attachment fragment: %w", err)
	}
	if err := replaceFragmentRevisionAttachments(ctx, tx.Conn(), canonicalID, revisionID, attachments, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attachment tx: %w", err)
	}
	return nil
}

// ReplaceFragmentProjectionAttachments updates only the mutable legacy
// attachment projection for provider-/model-derived enrichment. It never
// creates a source revision or changes an immutable revision-scoped manifest.
func (r *AttachmentRepository) ReplaceFragmentProjectionAttachments(ctx context.Context, fragmentID string, attachments []domain.PipelineAttachment, now time.Time) error {
	tx, err := NewMediaRepository(r.db).BeginImmediate(ctx, "replace fragment projection attachments")
	if err != nil {
		return fmt.Errorf("begin attachment projection tx: %w", err)
	}
	defer tx.Rollback()
	canonicalID, err := resolveCanonicalFragmentID(ctx, tx.Conn(), fragmentID)
	if err != nil {
		return fmt.Errorf("resolve attachment projection fragment: %w", err)
	}
	var revisionID string
	if err := tx.Conn().QueryRowContext(ctx, `SELECT current_revision_id FROM fragments WHERE id = ?`, canonicalID).Scan(&revisionID); err != nil {
		return fmt.Errorf("resolve attachment projection revision: %w", err)
	}
	if _, err := replaceLegacyAttachmentRows(ctx, tx.Conn(), canonicalID, revisionID, attachments, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attachment projection tx: %w", err)
	}
	return nil
}

func replaceFragmentRevisionAttachments(ctx context.Context, tx MediaWriteConn, fragmentID, revisionID string, attachments []domain.PipelineAttachment, now time.Time) error {
	manifest, err := replaceLegacyAttachmentRows(ctx, tx, fragmentID, revisionID, attachments, now)
	if err != nil {
		return err
	}
	if _, err := UpsertMediaManifest(ctx, tx, revisionID, manifest, now); err != nil {
		return fmt.Errorf("project attachment media manifest: %w", err)
	}
	return nil
}

// replaceLegacyAttachmentRows updates only the mutable compatibility tables.
// Canonical media rows are installed separately by the capture transaction so
// their foreign keys can safely refer back to these legacy attachment rows.
func replaceLegacyAttachmentRows(ctx context.Context, tx MediaWriteConn, fragmentID, revisionID string, attachments []domain.PipelineAttachment, now time.Time) ([]domain.MediaManifestItem, error) {
	var sourceRegistrationID, fragmentProvider string
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(si.source_registration_id, NULLIF(f.ingest_name, ''), f.source),
       COALESCE(si.provider, NULLIF(f.source, ''), 'legacy')
FROM fragments f
LEFT JOIN fragment_source_identities si ON si.fragment_id = f.id
JOIN fragment_revisions fr ON fr.fragment_id = f.id AND fr.id = ?
	WHERE f.id = ?`, revisionID, fragmentID).Scan(&sourceRegistrationID, &fragmentProvider); err != nil {
		return nil, fmt.Errorf("load attachment source identity: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM fragment_attachments WHERE fragment_id = ?`, fragmentID); err != nil {
		return nil, fmt.Errorf("clear fragment attachments: %w", err)
	}
	manifest := make([]domain.MediaManifestItem, 0, len(attachments))
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
				return nil, fmt.Errorf("encode attachment metadata: %w", err)
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
			return nil, fmt.Errorf("upsert attachment: %w", err)
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
			return nil, fmt.Errorf("link fragment attachment: %w", err)
		}
		manifest = append(manifest, LegacyPipelineMediaItem(fragmentID, revisionID, attachmentID, normalized, len(manifest), metaJSON, sourceRegistrationID, fragmentProvider, now))
	}
	return manifest, nil
}

// BuildLegacyPipelineMediaManifest projects the source-owned portion of a
// legacy PipelineFragment into the canonical media model. It performs no I/O;
// callers can therefore include the result in a larger capture transaction.
// The returned IDs are the same deterministic IDs UpsertMediaManifest resolves.
func BuildLegacyPipelineMediaManifest(fragment domain.Fragment, attachments []domain.PipelineAttachment, now time.Time) []domain.MediaManifestItem {
	manifest := make([]domain.MediaManifestItem, 0, len(attachments))
	for _, item := range attachments {
		normalized := normalizePipelineAttachment(item)
		if normalized.Kind == "" {
			continue
		}
		attachmentID := attachmentIdentity(normalized)
		metaJSON := "{}"
		if len(normalized.Metadata) > 0 {
			if raw, err := json.Marshal(normalized.Metadata); err == nil {
				metaJSON = string(raw)
			}
		}
		projected := LegacyPipelineMediaItem(fragment.ID, fragment.Revision.ID,
			attachmentID, normalized, len(manifest), metaJSON,
			fragment.SourceIdentity.SourceRegistrationID,
			fragment.SourceIdentity.Provider, now)
		projected.Asset.ID, projected.Asset.IdentityKey = domain.StableMediaAssetID(
			projected.Asset.SourceRegistrationID, projected.Asset.Provider,
			projected.Asset.ProviderMediaID, projected.Asset.SourceMediaKey,
			projected.Asset.SourceLocator)
		for i := range projected.Variants {
			projected.Variants[i].MediaAssetID = projected.Asset.ID
			projected.Variants[i].ID = domain.StableAssetVariantID(projected.Asset.ID, projected.Variants[i].VariantIdentity)
		}
		manifest = append(manifest, projected)
	}
	return manifest
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
	tx, err := NewMediaRepository(r.db).BeginImmediate(ctx, "update attachment storage paths")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	canonicalID, err := resolveCanonicalFragmentID(ctx, tx.Conn(), fragmentID)
	if err != nil {
		return fmt.Errorf("resolve attachment storage owner: %w", err)
	}
	for attachmentID, published := range storage {
		if _, err := tx.Conn().ExecContext(ctx, `
UPDATE fragment_attachments
SET storage_path = ?, preview_storage_path = ?
WHERE fragment_id = ? AND attachment_id = ?`,
			published.StoragePath,
			published.PreviewStoragePath,
			canonicalID,
			attachmentID,
		); err != nil {
			return fmt.Errorf("update fragment attachment storage path: %w", err)
		}
		var revisionID, assetID, mimeType, metaJSON string
		err := tx.Conn().QueryRowContext(ctx, `
SELECT f.current_revision_id, ar.media_asset_id, a.mime_type, fa.metadata_json
FROM fragments f
JOIN fragment_attachments fa ON fa.fragment_id = f.id AND fa.attachment_id = ?
JOIN attachments a ON a.id = fa.attachment_id
JOIN attachment_refs ar ON ar.fragment_revision_id = f.current_revision_id
  AND ar.legacy_attachment_id = fa.attachment_id
WHERE f.id = ?`, attachmentID, canonicalID).Scan(&revisionID, &assetID, &mimeType, &metaJSON)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("resolve media storage projection: %w", err)
		}
		if err == sql.ErrNoRows {
			continue
		}
		if strings.TrimSpace(published.StoragePath) != "" {
			if _, err := tx.Conn().ExecContext(ctx, `
UPDATE asset_variants
SET legacy_storage_path = ?, custody = 'adopted', acquisition_state = 'available',
    retention = 'indefinite', failure_code = '', failure_message = '',
    failure_retryable = 0, updated_at = ?
WHERE media_asset_id = ? AND variant_identity = ?`, published.StoragePath,
				formatTime(time.Now().UTC()), assetID, "legacy:"+attachmentID+":original"); err != nil {
				return fmt.Errorf("project original storage path: %w", err)
			}
		}
		if strings.TrimSpace(published.PreviewStoragePath) != "" {
			if _, err := upsertAssetVariant(ctx, tx.Conn(), domain.AssetVariant{
				MediaAssetID: assetID, VariantIdentity: "legacy:" + attachmentID + ":preview",
				Kind: domain.VariantPreview, SourcePath: published.PreviewStoragePath,
				MIMEType: previewMIMEType(mimeType), Custody: domain.CustodyAdopted,
				AcquisitionState: domain.AcquisitionAvailable,
				Retention:        domain.RetentionIndefinite, MetadataJSON: metaJSON,
				LegacyStoragePath: published.PreviewStoragePath,
			}, time.Now().UTC()); err != nil {
				return fmt.Errorf("project preview storage path: %w", err)
			}
			// A destination can regenerate/move its compatibility preview. This
			// path is a mutable serving projection, not source provenance; keep
			// the media variant in sync rather than silently serving a stale path.
			if _, err := tx.Conn().ExecContext(ctx, `
UPDATE asset_variants
SET source_path = ?, legacy_storage_path = ?, custody = 'adopted',
    acquisition_state = 'available', retention = 'indefinite', updated_at = ?
WHERE media_asset_id = ? AND variant_identity = ?`, published.PreviewStoragePath,
				published.PreviewStoragePath, formatTime(time.Now().UTC()), assetID,
				"legacy:"+attachmentID+":preview"); err != nil {
				return fmt.Errorf("refresh preview storage path: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attachment storage paths: %w", err)
	}
	return nil
}

func previewMIMEType(original string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(original)), "image/") {
		return "image/jpeg"
	}
	return "application/octet-stream"
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

// ensureAttachmentManifestRevision protects revision-scoped refs when a legacy
// caller replaces the current attachment set outside the ingest pipeline. A
// changed ordered manifest becomes a new immutable revision; old refs remain
// attached to the old revision.
func ensureAttachmentManifestRevision(ctx context.Context, q MediaWriteConn, fragmentID string, attachments []domain.PipelineAttachment, now time.Time) (string, error) {
	var current domain.FragmentRevision
	var normalizerAdapter, normalizerVersion, observedAt string
	err := q.QueryRowContext(ctx, `
SELECT fr.id, fr.ordinal, fr.material_digest, fr.content_digest, fr.title,
       fr.description, fr.content, fr.content_format, fr.ordered_media_digest,
       fr.metadata_json, fr.normalizer_adapter, fr.normalizer_version,
       fr.observed_at
FROM fragments f
JOIN fragment_revisions fr ON fr.id = f.current_revision_id
WHERE f.id = ?`, fragmentID).Scan(
		&current.ID, &current.Ordinal, &current.MaterialDigest, &current.ContentDigest,
		&current.Title, &current.Description, &current.Content, &current.ContentFormat,
		&current.OrderedMediaDigest, &current.MetadataJSON, &normalizerAdapter,
		&normalizerVersion, &observedAt)
	if err != nil {
		return "", fmt.Errorf("load current attachment revision: %w", err)
	}
	material := domain.NormalizeMaterial(current.Title, current.Description, current.Content, current.ContentFormat, attachments)
	if material.OrderedMediaDigest == current.OrderedMediaDigest {
		return current.ID, nil
	}
	materialDigest := material.Digest()
	var revisionID string
	err = q.QueryRowContext(ctx, `SELECT id FROM fragment_revisions WHERE fragment_id = ? AND material_digest = ?`, fragmentID, materialDigest).Scan(&revisionID)
	if err != nil && err != sql.ErrNoRows {
		return "", fmt.Errorf("find attachment manifest revision: %w", err)
	}
	if err == sql.ErrNoRows {
		var ordinal int
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), 0) + 1 FROM fragment_revisions WHERE fragment_id = ?`, fragmentID).Scan(&ordinal); err != nil {
			return "", fmt.Errorf("allocate attachment manifest revision: %w", err)
		}
		revisionID = domain.DigestText(fragmentID + "\n" + materialDigest)
		if _, err := q.ExecContext(ctx, `
INSERT INTO fragment_revisions (
  id, fragment_id, ordinal, material_digest, content_digest, title,
  description, content, content_format, ordered_media_digest, metadata_json,
  normalizer_adapter, normalizer_version, observed_at, committed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			revisionID, fragmentID, ordinal, materialDigest,
			domain.DigestText(material.Content), material.Title, material.Description,
			material.Content, material.ContentFormat, material.OrderedMediaDigest,
			current.MetadataJSON, normalizerAdapter, normalizerVersion,
			firstNonEmptyTimestamp(observedAt, formatTime(now)), formatTime(now)); err != nil {
			return "", fmt.Errorf("insert attachment manifest revision: %w", err)
		}
	}
	if _, err := q.ExecContext(ctx, `
UPDATE fragments SET accepted_revision_id = ?, current_revision_id = ?,
  content_hash = ?, ingested_at = ? WHERE id = ?`,
		revisionID, revisionID, domain.DigestText(material.Content),
		formatTime(now), fragmentID); err != nil {
		return "", fmt.Errorf("select attachment manifest revision: %w", err)
	}
	return revisionID, nil
}

// LegacyPipelineMediaItem is shared by the live compatibility dual-write and
// Store.Open's deterministic historical projection.
func LegacyPipelineMediaItem(fragmentID, revisionID, attachmentID string, item domain.PipelineAttachment, position int, metadataJSON, sourceRegistrationID, fragmentProvider string, now time.Time) domain.MediaManifestItem {
	provider := strings.ToLower(strings.TrimSpace(item.Source))
	if provider == "" || provider == "ingest" {
		provider = strings.ToLower(strings.TrimSpace(fragmentProvider))
	}
	if provider == "" {
		provider = "legacy"
	}
	registration := strings.TrimSpace(sourceRegistrationID)
	if registration == "" {
		registration = provider
	}
	// Legacy source_item_id is relationship provenance (often a message ID or
	// the literal "manual-intake"), not a guaranteed provider media ID.
	sourceMediaKey := attachmentID
	locator := firstNonEmptyString(item.ExternalURL, item.SourcePath, item.Name, attachmentID)
	mediaKind := legacyMediaKind(item.Kind, item.MIMEType)
	custody := domain.CustodyReference
	state := domain.AcquisitionReferenceOnly
	retention := domain.RetentionExternal
	if item.SourcePath != "" || item.StoragePath != "" {
		custody = domain.CustodyAdopted
		state = domain.AcquisitionAvailable
		retention = domain.RetentionIndefinite
	}
	role := legacyAttachmentRole(item.Role)
	variantKind := domain.VariantOriginal
	if mediaKind == domain.MediaAudio {
		variantKind = domain.VariantAudio
	} else if mediaKind == domain.MediaTimedText {
		variantKind = domain.VariantTranscript
	}
	caption := metadataText(item.Metadata, "caption")
	sourceContext := metadataText(item.Metadata, "source_context")
	if sourceContext == "" && item.Role != string(role) {
		sourceContext = "legacy_role:" + item.Role
	}
	return domain.MediaManifestItem{
		Asset: domain.MediaAsset{
			SourceRegistrationID: registration, Provider: provider,
			SourceMediaKey: sourceMediaKey, SourceLocator: locator, Kind: mediaKind,
			AltText: metadataText(item.Metadata, "alt_text"), SourceAuthority: provider,
			DefaultCustody: custody, MetadataJSON: metadataJSON,
			LegacyAttachmentID: attachmentID, CreatedAt: now,
		},
		Variants: []domain.AssetVariant{{
			VariantIdentity: "legacy:" + attachmentID + ":original", Kind: variantKind,
			SourceURL: item.ExternalURL, SourcePath: item.SourcePath,
			MIMEType: item.MIMEType, ByteSize: item.SizeBytes, Custody: custody,
			AcquisitionState: state, Retention: retention, MetadataJSON: metadataJSON,
			LegacyStoragePath: item.StoragePath, CreatedAt: now,
		}},
		Attachment: domain.AttachmentRef{
			FragmentRevisionID: revisionID, Role: role, Position: position,
			Caption: caption, SourceContext: sourceContext,
			LegacyFragmentID: fragmentID, LegacyAttachmentID: attachmentID,
			CreatedAt: now,
		},
	}
}

func legacyMediaKind(kind, mimeType string) domain.MediaKind {
	kind = strings.ToLower(strings.TrimSpace(kind))
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case kind == "image" || strings.HasPrefix(mimeType, "image/"):
		return domain.MediaImage
	case kind == "video" || strings.HasPrefix(mimeType, "video/"):
		return domain.MediaVideo
	case kind == "audio" || strings.HasPrefix(mimeType, "audio/"):
		return domain.MediaAudio
	case kind == "transcript" || kind == "subtitles" || strings.Contains(mimeType, "vtt"):
		return domain.MediaTimedText
	case kind == "pdf" || kind == "document" || kind == "markdown" || kind == "text" ||
		strings.HasPrefix(mimeType, "text/") || mimeType == "application/pdf":
		return domain.MediaDocument
	default:
		return domain.MediaOther
	}
}

func legacyAttachmentRole(role string) domain.AttachmentRole {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "primary":
		return domain.AttachmentPrimary
	case "gallery_item", "gallery-item", "gallery":
		return domain.AttachmentGalleryItem
	case "hero":
		return domain.AttachmentHero
	case "inline", "content":
		return domain.AttachmentInline
	case "poster":
		return domain.AttachmentPoster
	case "transcript", "subtitles":
		return domain.AttachmentTranscript
	default:
		return domain.AttachmentOther
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyTimestamp(values ...string) string {
	return firstNonEmptyString(values...)
}

func metadataText(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
