package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type legacyMediaLink struct {
	fragmentID         string
	revisionID         string
	attachmentID       string
	sourceRegistration string
	fragmentProvider   string
	metadataJSON       string
	createdAt          string
	previewStoragePath string
	attachment         domain.PipelineAttachment
}

// backfillMedia projects every legacy fragment_attachments occurrence into
// the logical media model. Identity aliases are deliberately mapped through
// fragment_revisions.legacy_fragment_id first: an attachment on an older
// content-hash-addressed fragment belongs to that exact historical revision,
// not to the canonical fragment's latest revision.
func (s *Store) backfillMedia(ctx context.Context) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire media backfill connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("configure media backfill: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin media backfill: %w", err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)

	var completed string
	err = conn.QueryRowContext(ctx, `SELECT completed_at FROM media_backfill_state WHERE id = 1`).Scan(&completed)
	if err == nil {
		_, err = conn.ExecContext(ctx, `COMMIT`)
		return err
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("read media backfill state: %w", err)
	}

	rows, err := conn.QueryContext(ctx, `
WITH projected AS (
SELECT DISTINCT
  fa.fragment_id,
  COALESCE(
    (SELECT fr.id FROM fragment_revisions fr
      WHERE fr.legacy_fragment_id = fa.fragment_id
      ORDER BY fr.ordinal DESC LIMIT 1),
    NULLIF(f.accepted_revision_id, ''),
    f.current_revision_id
  ) AS revision_id
FROM fragment_attachments fa
JOIN fragments f ON f.id = fa.fragment_id
)
SELECT
  fa.fragment_id, projected.revision_id,
  a.id, a.kind, fa.role, a.name, a.mime_type, a.source_path, a.external_url,
  fa.storage_path, fa.preview_storage_path, a.size_bytes, fa.source, fa.source_item_id,
  fa.metadata_json, fa.created_at,
  COALESCE(si.source_registration_id, NULLIF(cf.ingest_name, ''), cf.source),
  COALESCE(si.provider, NULLIF(cf.source, ''), 'legacy')
FROM fragment_attachments fa
JOIN projected ON projected.fragment_id = fa.fragment_id
JOIN attachments a ON a.id = fa.attachment_id
JOIN fragment_revisions target_revision ON target_revision.id = projected.revision_id
JOIN fragments cf ON cf.id = COALESCE(
  (SELECT fia.fragment_id FROM fragment_identity_aliases fia
    WHERE fia.alias_fragment_id = fa.fragment_id),
  fa.fragment_id
)
LEFT JOIN fragment_source_identities si ON si.fragment_id = cf.id
WHERE target_revision.legacy_fragment_id IS NULL
   OR target_revision.legacy_fragment_id = fa.fragment_id
ORDER BY revision_id, fa.created_at, a.name, a.id, fa.role, fa.source, fa.source_item_id`)
	if err != nil {
		return fmt.Errorf("list legacy media links: %w", err)
	}
	var links []legacyMediaLink
	for rows.Next() {
		var item legacyMediaLink
		if err := rows.Scan(
			&item.fragmentID, &item.revisionID, &item.attachmentID,
			&item.attachment.Kind, &item.attachment.Role, &item.attachment.Name,
			&item.attachment.MIMEType, &item.attachment.SourcePath,
			&item.attachment.ExternalURL, &item.attachment.StoragePath,
			&item.previewStoragePath, &item.attachment.SizeBytes, &item.attachment.Source,
			&item.attachment.SourceItemID, &item.metadataJSON, &item.createdAt,
			&item.sourceRegistration, &item.fragmentProvider,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy media link: %w", err)
		}
		links = append(links, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate legacy media links: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy media links: %w", err)
	}

	// Legacy fragment_attachments had no position column. The ORDER BY above
	// is the compatibility fallback used by the old digest backfill too:
	// created_at, attachment name/ID, then relationship identity. Positions
	// are assigned contiguously within each exact historical revision.
	for start := 0; start < len(links); {
		end := start + 1
		for end < len(links) && links[end].revisionID == links[start].revisionID {
			end++
		}
		manifest := make([]domain.MediaManifestItem, 0, end-start)
		for i := start; i < end; i++ {
			item := links[i]
			createdAt := parseLegacyTime(item.createdAt)
			manifest = append(manifest, legacyBackfillMediaItem(
				item.fragmentID, item.revisionID, item.attachmentID, item.attachment,
				i-start, item.metadataJSON, item.sourceRegistration,
				item.fragmentProvider, item.previewStoragePath, createdAt,
			))
		}
		if err := insertBackfilledMediaManifest(ctx, conn, manifest); err != nil {
			return fmt.Errorf("backfill media revision %s: %w", links[start].revisionID, err)
		}
		start = end
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO media_backfill_state(id, completed_at) VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("complete media backfill: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit media backfill: %w", err)
	}
	return nil
}

func insertBackfilledMediaManifest(ctx context.Context, q *sql.Conn, manifest []domain.MediaManifestItem) error {
	for _, item := range manifest {
		asset := item.Asset
		asset.ID, asset.IdentityKey = domain.StableMediaAssetID(asset.SourceRegistrationID, asset.Provider, asset.ProviderMediaID, asset.SourceMediaKey, asset.SourceLocator)
		if _, err := q.ExecContext(ctx, `
INSERT INTO media_assets (
  id, identity_key, source_registration_id, provider, provider_media_id,
  source_media_key, source_locator, kind, width, height, duration_seconds,
  page_count, alt_text, source_authority, default_custody, metadata_json,
  legacy_attachment_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
ON CONFLICT(identity_key) DO NOTHING`,
			asset.ID, asset.IdentityKey, asset.SourceRegistrationID, asset.Provider,
			asset.ProviderMediaID, asset.SourceMediaKey, asset.SourceLocator,
			string(asset.Kind), asset.Width, asset.Height, asset.DurationSeconds,
			asset.PageCount, asset.AltText, asset.SourceAuthority,
			string(asset.DefaultCustody), asset.MetadataJSON, asset.LegacyAttachmentID,
			asset.CreatedAt.Format(time.RFC3339Nano), asset.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert backfilled media asset: %w", err)
		}
		observationID := domain.DigestText("media-source-observation\n" + asset.ID + "\n" + asset.SourceMediaKey + "\n" + asset.SourceLocator)
		if _, err := q.ExecContext(ctx, `
INSERT INTO media_asset_source_observations (
  id, media_asset_id, source_media_key, source_locator, observed_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(media_asset_id, source_media_key, source_locator) DO NOTHING`,
			observationID, asset.ID, asset.SourceMediaKey, asset.SourceLocator,
			asset.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert backfilled media source observation: %w", err)
		}
		for _, variant := range item.Variants {
			variant.MediaAssetID = asset.ID
			variant.ID = domain.StableAssetVariantID(asset.ID, variant.VariantIdentity)
			if _, err := q.ExecContext(ctx, `
INSERT INTO asset_variants (
  id, media_asset_id, variant_identity, kind, source_url, source_expires_at,
  source_path, mime_type, width, height, duration_seconds, byte_size,
  expected_digest, digest, blob_digest, custody, acquisition_state,
  failure_code, failure_message, failure_retryable, retention, metadata_json,
  legacy_storage_path, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, '', '', NULL, ?, ?, '', '', 0, ?, ?, ?, ?, ?)
ON CONFLICT(media_asset_id, variant_identity) DO NOTHING`,
				variant.ID, asset.ID, variant.VariantIdentity, string(variant.Kind),
				variant.SourceURL, variant.SourcePath, variant.MIMEType, variant.Width,
				variant.Height, variant.DurationSeconds, variant.ByteSize,
				string(variant.Custody), string(variant.AcquisitionState),
				string(variant.Retention), variant.MetadataJSON, variant.LegacyStoragePath,
				variant.CreatedAt.Format(time.RFC3339Nano), variant.CreatedAt.Format(time.RFC3339Nano)); err != nil {
				return fmt.Errorf("insert backfilled media variant: %w", err)
			}
			if strings.TrimSpace(variant.SourceURL) != "" {
				observationID := domain.DigestText("variant-source-observation\n" + variant.ID + "\n" + variant.SourceURL + "\n")
				if _, err := q.ExecContext(ctx, `
INSERT INTO asset_variant_source_observations (
  id, asset_variant_id, source_url, source_expires_at, observed_at
) VALUES (?, ?, ?, '', ?)
ON CONFLICT(asset_variant_id, source_url, source_expires_at) DO NOTHING`,
					observationID, variant.ID, variant.SourceURL,
					variant.CreatedAt.Format(time.RFC3339Nano)); err != nil {
					return fmt.Errorf("insert backfilled variant source observation: %w", err)
				}
			}
		}
		ref := item.Attachment
		ref.MediaAssetID = asset.ID
		ref.ID = domain.StableAttachmentRefID(ref.FragmentRevisionID, ref.Position)
		if _, err := q.ExecContext(ctx, `
INSERT INTO attachment_refs (
  id, fragment_revision_id, media_asset_id, role, position, caption,
  source_context, legacy_fragment_id, legacy_attachment_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
ON CONFLICT(fragment_revision_id, position) DO NOTHING`,
			ref.ID, ref.FragmentRevisionID, ref.MediaAssetID, string(ref.Role),
			ref.Position, ref.Caption, ref.SourceContext, ref.LegacyFragmentID,
			ref.LegacyAttachmentID, ref.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert backfilled attachment ref: %w", err)
		}
	}
	return nil
}

func legacyBackfillMediaItem(fragmentID, revisionID, attachmentID string, item domain.PipelineAttachment, position int, metadataJSON, sourceRegistrationID, fragmentProvider, previewStoragePath string, now time.Time) domain.MediaManifestItem {
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
	// Legacy source_item_id is relationship provenance, not a guaranteed
	// provider media identity.
	sourceMediaKey := attachmentID
	locator := firstLegacyMediaValue(item.ExternalURL, item.SourcePath, item.Name, attachmentID)
	mediaKind := backfillMediaKind(item.Kind, item.MIMEType)
	custody := domain.CustodyReference
	state := domain.AcquisitionReferenceOnly
	retention := domain.RetentionExternal
	if item.SourcePath != "" || item.StoragePath != "" {
		custody, state, retention = domain.CustodyAdopted, domain.AcquisitionAvailable, domain.RetentionIndefinite
	}
	variantKind := domain.VariantOriginal
	if mediaKind == domain.MediaAudio {
		variantKind = domain.VariantAudio
	} else if mediaKind == domain.MediaTimedText {
		variantKind = domain.VariantTranscript
	}
	meta := map[string]any{}
	_ = json.Unmarshal([]byte(metadataJSON), &meta)
	role := backfillAttachmentRole(item.Role)
	sourceContext := backfillMetadataText(meta, "source_context")
	if sourceContext == "" && item.Role != string(role) {
		sourceContext = "legacy_role:" + item.Role
	}
	variants := []domain.AssetVariant{{
		VariantIdentity: "legacy:" + attachmentID + ":original", Kind: variantKind,
		SourceURL: item.ExternalURL, SourcePath: item.SourcePath, MIMEType: item.MIMEType,
		ByteSize: item.SizeBytes, Custody: custody, AcquisitionState: state,
		Retention: retention, MetadataJSON: metadataJSON,
		LegacyStoragePath: item.StoragePath, CreatedAt: now,
	}}
	if strings.TrimSpace(previewStoragePath) != "" {
		variants = append(variants, domain.AssetVariant{
			VariantIdentity: "legacy:" + attachmentID + ":preview",
			Kind:            domain.VariantPreview, SourcePath: previewStoragePath,
			MIMEType: "image/jpeg", Custody: domain.CustodyAdopted,
			AcquisitionState: domain.AcquisitionAvailable,
			Retention:        domain.RetentionIndefinite, MetadataJSON: metadataJSON,
			LegacyStoragePath: previewStoragePath, CreatedAt: now,
		})
	}
	return domain.MediaManifestItem{
		Asset: domain.MediaAsset{
			SourceRegistrationID: registration, Provider: provider,
			SourceMediaKey: sourceMediaKey, SourceLocator: locator, Kind: mediaKind,
			AltText:         backfillMetadataText(meta, "alt_text"),
			SourceAuthority: provider, DefaultCustody: custody, MetadataJSON: metadataJSON,
			LegacyAttachmentID: attachmentID, CreatedAt: now,
		},
		Variants: variants,
		Attachment: domain.AttachmentRef{
			FragmentRevisionID: revisionID, Role: role, Position: position,
			Caption: backfillMetadataText(meta, "caption"), SourceContext: sourceContext,
			LegacyFragmentID: fragmentID, LegacyAttachmentID: attachmentID, CreatedAt: now,
		},
	}
}

func backfillMediaKind(kind, mimeType string) domain.MediaKind {
	kind, mimeType = strings.ToLower(strings.TrimSpace(kind)), strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case kind == "image" || strings.HasPrefix(mimeType, "image/"):
		return domain.MediaImage
	case kind == "video" || strings.HasPrefix(mimeType, "video/"):
		return domain.MediaVideo
	case kind == "audio" || strings.HasPrefix(mimeType, "audio/"):
		return domain.MediaAudio
	case kind == "transcript" || kind == "subtitles" || strings.Contains(mimeType, "vtt"):
		return domain.MediaTimedText
	case kind == "pdf" || kind == "document" || kind == "markdown" || kind == "text" || strings.HasPrefix(mimeType, "text/") || mimeType == "application/pdf":
		return domain.MediaDocument
	default:
		return domain.MediaOther
	}
}

func backfillAttachmentRole(role string) domain.AttachmentRole {
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

func firstLegacyMediaValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func backfillMetadataText(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func parseLegacyTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	if parsed.IsZero() {
		parsed, _ = time.Parse(time.RFC3339, value)
	}
	if parsed.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return parsed.UTC()
}
