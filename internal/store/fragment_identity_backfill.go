package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type legacyFragment struct {
	ID            string
	Source        string
	SourceType    string
	SourceID      string
	Title         string
	Content       string
	ContentHash   string
	CreatedAt     string
	IngestedAt    string
	Status        string
	Summary       string
	IndexedAt     string
	MetadataJSON  string
	IngestName    string
	CanonicalPath string
	Identity      domain.SourceIdentity
	Material      domain.NormalizedMaterial
}

func (s *Store) backfillFragmentIdentity(ctx context.Context) error {
	var completed string
	err := s.DB.QueryRowContext(ctx, `SELECT completed_at FROM fragment_identity_backfill_state WHERE id = 1`).Scan(&completed)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("read fragment identity backfill state: %w", err)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fragment identity backfill: %w", err)
	}
	defer tx.Rollback()

	legacy, err := loadLegacyFragments(ctx, tx)
	if err != nil {
		return err
	}
	groups := make(map[string][]*legacyFragment)
	for i := range legacy {
		item := &legacy[i]
		metadata := make(map[string]any)
		if strings.TrimSpace(item.MetadataJSON) != "" {
			_ = json.Unmarshal([]byte(item.MetadataJSON), &metadata)
		}
		candidate := domain.PipelineFragment{
			Source:     item.Source,
			SourceType: item.SourceType,
			SourceID:   item.SourceID,
			Title:      item.Title,
			Content:    item.Content,
			Metadata:   metadata,
		}
		item.Identity = domain.NormalizeSourceIdentity(candidate, item.IngestName)
		attachments, err := loadLegacyAttachments(ctx, tx, item.ID)
		if err != nil {
			return err
		}
		item.Material = domain.NormalizeMaterial(item.Title, "", item.Content, domain.DefaultContentFormat, attachments)
		groups[identityGroupKey(item.Identity)] = append(groups[identityGroupKey(item.Identity)], item)
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		items := groups[key]
		// Revision ordinal is observation order, independently of which legacy
		// primary key was selected as the stable canonical row.
		sort.Slice(items, func(i, j int) bool {
			if items[i].IngestedAt != items[j].IngestedAt {
				return items[i].IngestedAt < items[j].IngestedAt
			}
			if items[i].CreatedAt != items[j].CreatedAt {
				return items[i].CreatedAt < items[j].CreatedAt
			}
			return items[i].ID < items[j].ID
		})
		// The latest observation is the active canonical row. That row is the
		// most likely to own the operational inbox/entity/attachment state that
		// users most recently acted on; older rows remain compatibility aliases.
		canonical := items[len(items)-1]
		if err := insertBackfilledIdentity(ctx, tx, canonical.ID, canonical.Identity, items[0].CreatedAt); err != nil {
			return err
		}
		revisionByDigest := make(map[string]string)
		ordinal := 0
		for _, item := range items {
			digest := item.Material.Digest()
			revisionID, exists := revisionByDigest[digest]
			if !exists {
				ordinal++
				revisionID = domain.DigestText(canonical.ID + "\n" + digest)
				if _, err := tx.ExecContext(ctx, `
INSERT INTO fragment_revisions (
  id, fragment_id, ordinal, material_digest, content_digest, title,
  description, content, content_format, ordered_media_digest, metadata_json,
  normalizer_adapter, normalizer_version, observed_at, committed_at,
  legacy_fragment_id
) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					revisionID, canonical.ID, ordinal, digest, domain.DigestText(item.Material.Content), item.Material.Title,
					item.Material.Content, item.Material.ContentFormat, item.Material.OrderedMediaDigest, item.MetadataJSON,
					domain.DefaultNormalizerAdapter, domain.DefaultAdapterVersion,
					firstTimestamp(item.CreatedAt, item.IngestedAt), firstTimestamp(item.IngestedAt, item.CreatedAt), item.ID,
				); err != nil {
					return fmt.Errorf("backfill revision %s: %w", revisionID, err)
				}
				revisionByDigest[digest] = revisionID
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE fragments SET accepted_revision_id = ?, current_revision_id = ? WHERE id = ?`,
				revisionID, revisionID, item.ID); err != nil {
				return fmt.Errorf("backfill fragment revision pointer %s: %w", item.ID, err)
			}
			if item.ID != canonical.ID {
				if _, err := tx.ExecContext(ctx, `
INSERT INTO fragment_identity_aliases(alias_fragment_id, fragment_id, created_at)
VALUES (?, ?, ?)`, item.ID, canonical.ID, firstTimestamp(item.CreatedAt, item.IngestedAt)); err != nil {
					return fmt.Errorf("backfill fragment alias %s: %w", item.ID, err)
				}
			}
		}

		latest := items[len(items)-1]
		latestRevisionID := revisionByDigest[latest.Material.Digest()]
		if _, err := tx.ExecContext(ctx, `
UPDATE fragments
SET source = ?, source_type = ?, source_id = ?, title = ?, content = ?,
    content_hash = ?, ingested_at = ?, metadata_json = ?, ingest_name = ?,
    canonical_path = ?, accepted_revision_id = ?, current_revision_id = ?
WHERE id = ?`,
			latest.Source, latest.SourceType, latest.SourceID, latest.Material.Title, latest.Material.Content,
			domain.DigestText(latest.Material.Content), latest.IngestedAt, latest.MetadataJSON, latest.IngestName,
			latest.CanonicalPath, latestRevisionID, latestRevisionID, canonical.ID,
		); err != nil {
			return fmt.Errorf("select current legacy revision for %s: %w", canonical.ID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO fragment_identity_backfill_state(id, completed_at)
VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("complete fragment identity backfill: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fragment identity backfill: %w", err)
	}
	return nil
}

func loadLegacyFragments(ctx context.Context, tx *sql.Tx) ([]legacyFragment, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, source, source_type, source_id, title, content, content_hash,
       created_at, ingested_at, status, summary_text, indexed_at,
       metadata_json, ingest_name, canonical_path
FROM fragments
ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list legacy fragments for identity backfill: %w", err)
	}
	defer rows.Close()
	var out []legacyFragment
	for rows.Next() {
		var item legacyFragment
		if err := rows.Scan(
			&item.ID, &item.Source, &item.SourceType, &item.SourceID, &item.Title,
			&item.Content, &item.ContentHash, &item.CreatedAt, &item.IngestedAt,
			&item.Status, &item.Summary, &item.IndexedAt, &item.MetadataJSON,
			&item.IngestName, &item.CanonicalPath,
		); err != nil {
			return nil, fmt.Errorf("scan legacy fragment for identity backfill: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy fragments for identity backfill: %w", err)
	}
	return out, nil
}

func loadLegacyAttachments(ctx context.Context, tx *sql.Tx, fragmentID string) ([]domain.PipelineAttachment, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT a.kind, fa.role, a.name, a.mime_type, a.source_path, a.external_url,
       fa.storage_path, a.size_bytes, fa.source, fa.source_item_id
FROM fragment_attachments fa
JOIN attachments a ON a.id = fa.attachment_id
WHERE fa.fragment_id = ?
ORDER BY fa.created_at, a.name, a.id`, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("list legacy attachments for %s: %w", fragmentID, err)
	}
	defer rows.Close()
	var out []domain.PipelineAttachment
	for rows.Next() {
		var item domain.PipelineAttachment
		if err := rows.Scan(
			&item.Kind, &item.Role, &item.Name, &item.MIMEType, &item.SourcePath,
			&item.ExternalURL, &item.StoragePath, &item.SizeBytes, &item.Source,
			&item.SourceItemID,
		); err != nil {
			return nil, fmt.Errorf("scan legacy attachment for %s: %w", fragmentID, err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func insertBackfilledIdentity(ctx context.Context, tx *sql.Tx, fragmentID string, identity domain.SourceIdentity, createdAt string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO fragment_source_identities (
  fragment_id, source_registration_id, provider, provider_item_id,
  source_item_key, source_locator, segment_key, submitted_url, canonical_url,
  source_adapter, source_adapter_version, canonicalizer_adapter,
  canonicalizer_version, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fragmentID, identity.SourceRegistrationID, identity.Provider,
		identity.ProviderItemID, identity.SourceItemKey, identity.SourceLocator,
		identity.SegmentKey, identity.SubmittedURL, identity.CanonicalURL,
		identity.SourceAdapter.Adapter, identity.SourceAdapter.Version,
		identity.Canonicalizer.Adapter, identity.Canonicalizer.Version,
		firstTimestamp(createdAt, time.Now().UTC().Format(time.RFC3339Nano)),
		firstTimestamp(createdAt, time.Now().UTC().Format(time.RFC3339Nano)),
	)
	if err != nil {
		return fmt.Errorf("backfill source identity for %s: %w", fragmentID, err)
	}
	return nil
}

func identityGroupKey(identity domain.SourceIdentity) string {
	return identity.SourceRegistrationID + "\x00" + identity.SourceItemKey + "\x00" + identity.SegmentKey
}

func firstTimestamp(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return time.Unix(0, 0).UTC().Format(time.RFC3339)
}
