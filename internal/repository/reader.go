package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// ReaderQueryer is deliberately narrower than *sql.DB so the fixed query
// budget of the Reader projection can be asserted without coupling the
// repository to a particular SQLite instrumentation hook.
type ReaderQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type ReaderRepository struct {
	db ReaderQueryer
}

func NewReaderRepository(db ReaderQueryer) *ReaderRepository {
	return &ReaderRepository{db: db}
}

type ReaderPageCursor struct {
	SortAt     time.Time
	FragmentID string
}

type ReaderPageRequest struct {
	Scope  string
	Cursor *ReaderPageCursor
	Limit  int
}

type ReaderBase struct {
	FragmentID       string
	FragmentStatus   string
	FragmentRevision domain.FragmentRevision
	Source           domain.SourceIdentity
	SortAt           time.Time
	InInbox          bool
	CaptureCount     int
	CuratedNote      *domain.CuratedNote
}

type ReaderCoverage struct {
	Coverage            domain.CapabilityCoverage
	SelectedValueJSON   string
	SelectedAttribution domain.AttributionSource
}

type ReaderTagFact struct {
	FragmentID         string
	FragmentRevisionID string
	Value              string
	NormalizedValue    string
	Source             domain.AttributionSource
	ObservationID      string
	ValueJSON          string
	ObservedAt         time.Time
}

type ReaderEffect struct {
	FragmentID    string
	RouteID       string
	DestinationID string
	Decision      string
	Reason        string
	CreatedAt     time.Time
}

type ReaderSnapshot struct {
	Bases       []ReaderBase
	Coverage    map[string][]ReaderCoverage
	Media       map[string][]domain.MediaManifestItem
	Tags        map[string][]ReaderTagFact
	Annotations map[string][]domain.CaptureAnnotation
	Effects     map[string][]ReaderEffect
}

// List returns a complete projection snapshot in exactly six SELECT
// statements for every page: one base-page read followed by five
// set-based reads. The statement count does not vary with item cardinality.
func (r *ReaderRepository) List(ctx context.Context, request ReaderPageRequest) (ReaderSnapshot, error) {
	if r == nil || r.db == nil {
		return ReaderSnapshot{}, fmt.Errorf("reader repository is unavailable")
	}
	if request.Scope != "inbox" && request.Scope != "library" && request.Scope != "all" {
		return ReaderSnapshot{}, fmt.Errorf("reader scope %q is invalid", request.Scope)
	}
	if request.Limit <= 0 {
		return ReaderSnapshot{}, fmt.Errorf("reader page limit must be positive")
	}
	cursorAt, cursorID := "", ""
	if request.Cursor != nil {
		if request.Cursor.SortAt.IsZero() || strings.TrimSpace(request.Cursor.FragmentID) == "" {
			return ReaderSnapshot{}, fmt.Errorf("reader cursor is incomplete")
		}
		cursorAt, cursorID = formatTime(request.Cursor.SortAt), strings.TrimSpace(request.Cursor.FragmentID)
	}
	rows, err := r.db.QueryContext(ctx, readerListBaseSQL,
		request.Scope, request.Scope, cursorAt, cursorAt, cursorAt, cursorID, request.Limit)
	if err != nil {
		return ReaderSnapshot{}, fmt.Errorf("list reader bases: %w", err)
	}
	bases, err := scanReaderBases(rows)
	if err != nil {
		return ReaderSnapshot{}, err
	}
	return r.loadBatches(ctx, bases)
}

// Get resolves a historical alias to its canonical fragment in the base query
// and requires an explicitly requested revision to belong to that canonical
// fragment. It uses the same five batch readers as List.
func (r *ReaderRepository) Get(ctx context.Context, fragmentID, revisionID string) (ReaderSnapshot, error) {
	if r == nil || r.db == nil {
		return ReaderSnapshot{}, fmt.Errorf("reader repository is unavailable")
	}
	fragmentID, revisionID = strings.TrimSpace(fragmentID), strings.TrimSpace(revisionID)
	if fragmentID == "" {
		return ReaderSnapshot{}, fmt.Errorf("reader fragment ID is required")
	}
	rows, err := r.db.QueryContext(ctx, readerDetailBaseSQL, fragmentID, fragmentID, revisionID, revisionID)
	if err != nil {
		return ReaderSnapshot{}, fmt.Errorf("get reader base: %w", err)
	}
	bases, err := scanReaderBases(rows)
	if err != nil {
		return ReaderSnapshot{}, err
	}
	if len(bases) == 0 {
		return ReaderSnapshot{}, sql.ErrNoRows
	}
	return r.loadBatches(ctx, bases)
}

const readerBaseColumns = `
  f.id, f.status,
  r.id, r.ordinal, r.material_digest, r.content_digest, r.title,
  r.description, r.content, r.content_format, r.ordered_media_digest,
  r.metadata_json, r.normalizer_adapter, r.normalizer_version,
  r.observed_at, r.committed_at, COALESCE(r.legacy_fragment_id, ''),
  si.source_registration_id, si.provider, si.provider_item_id,
  si.source_item_key, si.source_locator, si.segment_key, si.submitted_url,
  si.canonical_url, si.source_adapter, si.source_adapter_version,
  si.canonicalizer_adapter, si.canonicalizer_version,
  candidates.sort_at, CASE WHEN i.fragment_id IS NULL THEN 0 ELSE 1 END,
  MAX(1, COALESCE(ca.capture_count, 0)),
  cn.body_markdown, cn.revision, cn.actor_id, cn.updated_at`

const readerCaptureAggregate = `
capture_aggregate AS (
  SELECT c.fragment_id, COUNT(*) AS capture_count,
    MAX(c.captured_at) AS last_captured_at
  FROM capture_attempts c
  GROUP BY c.fragment_id
)`

const readerListBaseSQL = `
WITH ` + readerCaptureAggregate + `,
candidates AS (
  SELECT f.id AS fragment_id,
    CASE WHEN ? = 'inbox' THEN i.staged_at
         ELSE COALESCE(ca.last_captured_at, f.ingested_at) END AS sort_at
  FROM fragments f
  LEFT JOIN fragment_identity_aliases aliases ON aliases.alias_fragment_id = f.id
  LEFT JOIN inbox i ON i.fragment_id = f.id
  LEFT JOIN capture_aggregate ca ON ca.fragment_id = f.id
  WHERE aliases.alias_fragment_id IS NULL
    AND (? <> 'inbox' OR i.fragment_id IS NOT NULL)
)
SELECT ` + readerBaseColumns + `
FROM candidates
JOIN fragments f ON f.id = candidates.fragment_id
JOIN fragment_source_identities si ON si.fragment_id = f.id
JOIN fragment_revisions r ON r.id = f.current_revision_id AND r.fragment_id = f.id
LEFT JOIN inbox i ON i.fragment_id = f.id
LEFT JOIN capture_aggregate ca ON ca.fragment_id = f.id
LEFT JOIN curated_notes cn ON cn.fragment_id = f.id
WHERE (? = '') OR candidates.sort_at < ?
   OR (candidates.sort_at = ? AND f.id < ?)
ORDER BY candidates.sort_at DESC, f.id DESC
LIMIT ?`

const readerDetailBaseSQL = `
WITH requested(fragment_id) AS (
  SELECT COALESCE(
    (SELECT fragment_id FROM fragment_identity_aliases WHERE alias_fragment_id = ?),
    (SELECT id FROM fragments WHERE id = ?)
  )
), ` + readerCaptureAggregate + `,
candidates AS (
  SELECT f.id AS fragment_id, COALESCE(ca.last_captured_at, f.ingested_at) AS sort_at
  FROM requested
  JOIN fragments f ON f.id = requested.fragment_id
  LEFT JOIN capture_aggregate ca ON ca.fragment_id = f.id
)
SELECT ` + readerBaseColumns + `
FROM candidates
JOIN fragments f ON f.id = candidates.fragment_id
JOIN fragment_source_identities si ON si.fragment_id = f.id
JOIN fragment_revisions r ON r.id = CASE WHEN ? = '' THEN f.current_revision_id ELSE ? END
  AND r.fragment_id = f.id
LEFT JOIN inbox i ON i.fragment_id = f.id
LEFT JOIN capture_aggregate ca ON ca.fragment_id = f.id
LEFT JOIN curated_notes cn ON cn.fragment_id = f.id`

func scanReaderBases(rows *sql.Rows) ([]ReaderBase, error) {
	defer rows.Close()
	bases := make([]ReaderBase, 0)
	for rows.Next() {
		var base ReaderBase
		var observedAt, committedAt, sortAt string
		var inInbox int
		var noteBody, noteActor, noteUpdated sql.NullString
		var noteRevision sql.NullInt64
		if err := rows.Scan(
			&base.FragmentID, &base.FragmentStatus,
			&base.FragmentRevision.ID, &base.FragmentRevision.Ordinal,
			&base.FragmentRevision.MaterialDigest, &base.FragmentRevision.ContentDigest,
			&base.FragmentRevision.Title, &base.FragmentRevision.Description,
			&base.FragmentRevision.Content, &base.FragmentRevision.ContentFormat,
			&base.FragmentRevision.OrderedMediaDigest, &base.FragmentRevision.MetadataJSON,
			&base.FragmentRevision.Normalizer.Adapter, &base.FragmentRevision.Normalizer.Version,
			&observedAt, &committedAt, &base.FragmentRevision.LegacyFragmentID,
			&base.Source.SourceRegistrationID, &base.Source.Provider,
			&base.Source.ProviderItemID, &base.Source.SourceItemKey,
			&base.Source.SourceLocator, &base.Source.SegmentKey,
			&base.Source.SubmittedURL, &base.Source.CanonicalURL,
			&base.Source.SourceAdapter.Adapter, &base.Source.SourceAdapter.Version,
			&base.Source.Canonicalizer.Adapter, &base.Source.Canonicalizer.Version,
			&sortAt, &inInbox, &base.CaptureCount,
			&noteBody, &noteRevision, &noteActor, &noteUpdated,
		); err != nil {
			return nil, fmt.Errorf("scan reader base: %w", err)
		}
		base.FragmentRevision.FragmentID = base.FragmentID
		base.FragmentRevision.ObservedAt = parseTime(observedAt)
		base.FragmentRevision.CommittedAt = parseTime(committedAt)
		base.SortAt = parseTime(sortAt)
		base.InInbox = inInbox != 0
		if noteBody.Valid && noteRevision.Valid && noteUpdated.Valid {
			base.CuratedNote = &domain.CuratedNote{FragmentID: base.FragmentID,
				BodyMarkdown: noteBody.String, Revision: int(noteRevision.Int64),
				ActorID: noteActor.String, UpdatedAt: parseTime(noteUpdated.String)}
		}
		bases = append(bases, base)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reader bases: %w", err)
	}
	return bases, nil
}

func (r *ReaderRepository) loadBatches(ctx context.Context, bases []ReaderBase) (ReaderSnapshot, error) {
	snapshot := ReaderSnapshot{
		Bases: bases, Coverage: map[string][]ReaderCoverage{}, Media: map[string][]domain.MediaManifestItem{},
		Tags: map[string][]ReaderTagFact{}, Annotations: map[string][]domain.CaptureAnnotation{},
		Effects: map[string][]ReaderEffect{},
	}
	fragmentIDs, revisionIDs := make([]string, 0, len(bases)), make([]string, 0, len(bases))
	for _, base := range bases {
		fragmentIDs = append(fragmentIDs, base.FragmentID)
		revisionIDs = append(revisionIDs, base.FragmentRevision.ID)
	}
	// Empty identifiers cannot own persisted rows. They let an empty page keep
	// the same five batch statement shapes and query budget as a populated page.
	if len(bases) == 0 {
		fragmentIDs, revisionIDs = []string{""}, []string{""}
	}
	var err error
	if snapshot.Coverage, err = r.loadCoverage(ctx, revisionIDs); err != nil {
		return ReaderSnapshot{}, err
	}
	if snapshot.Media, err = r.loadMedia(ctx, revisionIDs); err != nil {
		return ReaderSnapshot{}, err
	}
	if snapshot.Tags, err = r.loadTags(ctx, fragmentIDs, revisionIDs); err != nil {
		return ReaderSnapshot{}, err
	}
	if snapshot.Annotations, err = r.loadAnnotations(ctx, fragmentIDs); err != nil {
		return ReaderSnapshot{}, err
	}
	if snapshot.Effects, err = r.loadEffects(ctx, fragmentIDs); err != nil {
		return ReaderSnapshot{}, err
	}
	return snapshot, nil
}

func queryPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringsToAny(values []string) []any {
	args := make([]any, len(values))
	for i := range values {
		args[i] = values[i]
	}
	return args
}

func (r *ReaderRepository) loadCoverage(ctx context.Context, revisionIDs []string) (map[string][]ReaderCoverage, error) {
	query := `SELECT c.fragment_revision_id, c.fragment_id, c.capability, c.state,
  COALESCE(c.selected_observation_id, ''), c.detail, c.error_class, c.error_code,
  c.error_retryable, c.requested_generation, c.satisfied_generation, c.version,
  c.updated_at, COALESCE(o.value_json, ''), COALESCE(o.attribution_source, '')
FROM fragment_capability_coverage c
LEFT JOIN enrichment_observations o ON o.id = c.selected_observation_id
WHERE c.fragment_revision_id IN (` + queryPlaceholders(len(revisionIDs)) + `)
ORDER BY c.fragment_revision_id, CASE c.capability
  WHEN 'title' THEN 1 WHEN 'description' THEN 2 WHEN 'body' THEN 3
  WHEN 'gallery_manifest' THEN 4 WHEN 'original_media' THEN 5
  WHEN 'thumbnail_or_poster' THEN 6 WHEN 'transcript' THEN 7
  WHEN 'OCR' THEN 8 WHEN 'vision' THEN 9 WHEN 'summary' THEN 10
  WHEN 'tags' THEN 11 WHEN 'entities' THEN 12 ELSE 99 END`
	rows, err := r.db.QueryContext(ctx, query, stringsToAny(revisionIDs)...)
	if err != nil {
		return nil, fmt.Errorf("load reader coverage: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]ReaderCoverage, len(revisionIDs))
	for rows.Next() {
		var revisionID, updatedAt, attribution string
		var item ReaderCoverage
		if err := rows.Scan(&revisionID, &item.Coverage.FragmentID,
			&item.Coverage.Capability, &item.Coverage.State,
			&item.Coverage.SelectedObservationID, &item.Coverage.Detail,
			&item.Coverage.ErrorClass, &item.Coverage.ErrorCode,
			&item.Coverage.ErrorRetryable, &item.Coverage.RequestedGeneration,
			&item.Coverage.SatisfiedGeneration, &item.Coverage.Version, &updatedAt,
			&item.SelectedValueJSON, &attribution); err != nil {
			return nil, fmt.Errorf("scan reader coverage: %w", err)
		}
		item.Coverage.FragmentRevisionID = revisionID
		item.Coverage.UpdatedAt = parseTime(updatedAt)
		item.SelectedAttribution = domain.AttributionSource(attribution)
		out[revisionID] = append(out[revisionID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reader coverage: %w", err)
	}
	return out, nil
}

func (r *ReaderRepository) loadMedia(ctx context.Context, revisionIDs []string) (map[string][]domain.MediaManifestItem, error) {
	query := `SELECT ar.fragment_revision_id, ar.id, ar.media_asset_id, ar.role,
  ar.position, ar.caption, ar.source_context, ar.created_at,
  ma.identity_key, ma.source_registration_id, ma.provider, ma.provider_media_id,
  ma.source_media_key, ma.source_locator, ma.kind, ma.width, ma.height,
  ma.duration_seconds, ma.page_count, ma.alt_text, ma.source_authority,
  ma.default_custody, ma.metadata_json, ma.created_at, ma.updated_at,
  COALESCE(av.id, ''), COALESCE(av.variant_identity, ''), COALESCE(av.kind, ''),
  COALESCE(av.source_url, ''), COALESCE(av.source_expires_at, ''),
  COALESCE(av.source_path, ''), COALESCE(av.mime_type, ''), COALESCE(av.width, 0),
  COALESCE(av.height, 0), COALESCE(av.duration_seconds, 0), COALESCE(av.byte_size, 0),
  COALESCE(av.expected_digest, ''), COALESCE(av.digest, ''), COALESCE(av.blob_digest, ''),
  COALESCE(av.custody, ''), COALESCE(av.acquisition_state, ''),
  COALESCE(av.failure_code, ''), COALESCE(av.failure_message, ''),
  COALESCE(av.failure_retryable, 0), COALESCE(av.retention, ''),
  COALESCE(av.metadata_json, ''), COALESCE(av.legacy_storage_path, ''),
  COALESCE(av.created_at, ''), COALESCE(av.updated_at, '')
FROM attachment_refs ar
JOIN media_assets ma ON ma.id = ar.media_asset_id
LEFT JOIN asset_variants av ON av.media_asset_id = ma.id
WHERE ar.fragment_revision_id IN (` + queryPlaceholders(len(revisionIDs)) + `)
ORDER BY ar.fragment_revision_id, ar.position,
  CASE av.kind WHEN 'original' THEN 1 WHEN 'preview' THEN 2
    WHEN 'thumbnail' THEN 3 WHEN 'poster' THEN 4 WHEN 'audio' THEN 5
    WHEN 'subtitles' THEN 6 WHEN 'transcript' THEN 7 ELSE 99 END,
  av.id`
	rows, err := r.db.QueryContext(ctx, query, stringsToAny(revisionIDs)...)
	if err != nil {
		return nil, fmt.Errorf("load reader media: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]domain.MediaManifestItem, len(revisionIDs))
	var currentRevision, currentAttachment string
	for rows.Next() {
		var revisionID, attachmentCreated, assetCreated, assetUpdated string
		var variantID, variantIdentity, variantKind, sourceURL, sourceExpiresAt string
		var sourcePath, mimeType, expectedDigest, digest, blobDigest string
		var custody, acquisition, failureCode, failureMessage, retention string
		var variantMetadata, legacyPath, variantCreated, variantUpdated string
		var variantWidth, variantHeight int
		var variantDuration float64
		var variantBytes int64
		var failureRetryable int
		var item domain.MediaManifestItem
		if err := rows.Scan(&revisionID, &item.Attachment.ID, &item.Attachment.MediaAssetID,
			&item.Attachment.Role, &item.Attachment.Position, &item.Attachment.Caption,
			&item.Attachment.SourceContext, &attachmentCreated, &item.Asset.IdentityKey,
			&item.Asset.SourceRegistrationID, &item.Asset.Provider, &item.Asset.ProviderMediaID,
			&item.Asset.SourceMediaKey, &item.Asset.SourceLocator, &item.Asset.Kind,
			&item.Asset.Width, &item.Asset.Height, &item.Asset.DurationSeconds,
			&item.Asset.PageCount, &item.Asset.AltText, &item.Asset.SourceAuthority,
			&item.Asset.DefaultCustody, &item.Asset.MetadataJSON, &assetCreated, &assetUpdated,
			&variantID, &variantIdentity, &variantKind, &sourceURL, &sourceExpiresAt,
			&sourcePath, &mimeType, &variantWidth, &variantHeight, &variantDuration,
			&variantBytes, &expectedDigest, &digest, &blobDigest, &custody, &acquisition,
			&failureCode, &failureMessage, &failureRetryable, &retention, &variantMetadata,
			&legacyPath, &variantCreated, &variantUpdated); err != nil {
			return nil, fmt.Errorf("scan reader media: %w", err)
		}
		item.Attachment.FragmentRevisionID = revisionID
		item.Attachment.CreatedAt = parseTime(attachmentCreated)
		item.Asset.ID = item.Attachment.MediaAssetID
		item.Asset.CreatedAt, item.Asset.UpdatedAt = parseTime(assetCreated), parseTime(assetUpdated)
		if revisionID != currentRevision || item.Attachment.ID != currentAttachment {
			out[revisionID] = append(out[revisionID], item)
			currentRevision, currentAttachment = revisionID, item.Attachment.ID
		}
		if variantID != "" {
			variant := domain.AssetVariant{ID: variantID, MediaAssetID: item.Asset.ID,
				VariantIdentity: variantIdentity, Kind: domain.AssetVariantKind(variantKind),
				SourceURL: sourceURL, SourceExpiresAt: parseTime(sourceExpiresAt), SourcePath: sourcePath,
				MIMEType: mimeType, Width: variantWidth, Height: variantHeight,
				DurationSeconds: variantDuration, ByteSize: variantBytes, BlobDigest: blobDigest,
				Custody: domain.CustodyMode(custody), AcquisitionState: domain.AcquisitionState(acquisition),
				Retention: domain.RetentionPolicy(retention), MetadataJSON: variantMetadata,
				LegacyStoragePath: legacyPath, CreatedAt: parseTime(variantCreated), UpdatedAt: parseTime(variantUpdated)}
			if expectedDigest != "" {
				variant.ExpectedDigest = domain.ContentDigest{Algorithm: "sha256", Value: expectedDigest}
			}
			if digest != "" {
				variant.Digest = domain.ContentDigest{Algorithm: "sha256", Value: digest}
			}
			if failureCode != "" || failureMessage != "" {
				variant.Failure = &domain.AssetFailure{Code: failureCode, Message: failureMessage, Retryable: failureRetryable != 0}
			}
			last := len(out[revisionID]) - 1
			out[revisionID][last].Variants = append(out[revisionID][last].Variants, variant)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reader media: %w", err)
	}
	return out, nil
}

func (r *ReaderRepository) loadTags(ctx context.Context, fragmentIDs, revisionIDs []string) (map[string][]ReaderTagFact, error) {
	query := `SELECT fragment_id, '' AS fragment_revision_id, value, normalized_value,
  attribution_source, observation_id, '' AS value_json, observed_at, 0 AS source_order
FROM fragment_tag_observations
WHERE fragment_id IN (` + queryPlaceholders(len(fragmentIDs)) + `)
UNION ALL
SELECT fragment_id, fragment_revision_id, '', '', attribution_source, id,
  value_json, observed_at, 1 AS source_order
FROM enrichment_observations
WHERE capability = 'tags' AND fragment_revision_id IN (` + queryPlaceholders(len(revisionIDs)) + `)
ORDER BY fragment_id, source_order, observed_at, observation_id`
	args := append(stringsToAny(fragmentIDs), stringsToAny(revisionIDs)...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load reader tags: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]ReaderTagFact, len(fragmentIDs))
	for rows.Next() {
		var item ReaderTagFact
		var observedAt string
		var sourceOrder int
		if err := rows.Scan(&item.FragmentID, &item.FragmentRevisionID, &item.Value,
			&item.NormalizedValue, &item.Source, &item.ObservationID, &item.ValueJSON,
			&observedAt, &sourceOrder); err != nil {
			return nil, fmt.Errorf("scan reader tag: %w", err)
		}
		item.ObservedAt = parseTime(observedAt)
		out[item.FragmentID] = append(out[item.FragmentID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reader tags: %w", err)
	}
	return out, nil
}

func (r *ReaderRepository) loadAnnotations(ctx context.Context, fragmentIDs []string) (map[string][]domain.CaptureAnnotation, error) {
	query := `SELECT id, capture_id, fragment_id, kind, text, selector_json,
  position_json, actor_id, captured_at, created_at
FROM capture_annotations
WHERE fragment_id IN (` + queryPlaceholders(len(fragmentIDs)) + `)
ORDER BY fragment_id, captured_at, id`
	rows, err := r.db.QueryContext(ctx, query, stringsToAny(fragmentIDs)...)
	if err != nil {
		return nil, fmt.Errorf("load reader annotations: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]domain.CaptureAnnotation, len(fragmentIDs))
	for rows.Next() {
		var item domain.CaptureAnnotation
		var selectorJSON, positionJSON, capturedAt, createdAt string
		if err := rows.Scan(&item.ID, &item.CaptureID, &item.FragmentID, &item.Kind,
			&item.Text, &selectorJSON, &positionJSON, &item.ActorID,
			&capturedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan reader annotation: %w", err)
		}
		item.CapturedAt, item.CreatedAt = parseTime(capturedAt), parseTime(createdAt)
		if selectorJSON != "" && selectorJSON != "{}" {
			if err := json.Unmarshal([]byte(selectorJSON), &item.Selector); err != nil {
				return nil, fmt.Errorf("decode reader annotation selector: %w", err)
			}
		}
		if positionJSON != "" && positionJSON != "{}" {
			if err := json.Unmarshal([]byte(positionJSON), &item.Position); err != nil {
				return nil, fmt.Errorf("decode reader annotation position: %w", err)
			}
		}
		out[item.FragmentID] = append(out[item.FragmentID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reader annotations: %w", err)
	}
	return out, nil
}

func (r *ReaderRepository) loadEffects(ctx context.Context, fragmentIDs []string) (map[string][]ReaderEffect, error) {
	query := `SELECT fragment_id, COALESCE(route_id, ''), COALESCE(destination_id, ''),
  decision, reason, created_at
FROM route_log
WHERE fragment_id IN (` + queryPlaceholders(len(fragmentIDs)) + `)
ORDER BY fragment_id, created_at, id`
	rows, err := r.db.QueryContext(ctx, query, stringsToAny(fragmentIDs)...)
	if err != nil {
		return nil, fmt.Errorf("load reader effects: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]ReaderEffect, len(fragmentIDs))
	for rows.Next() {
		var item ReaderEffect
		var createdAt string
		if err := rows.Scan(&item.FragmentID, &item.RouteID, &item.DestinationID,
			&item.Decision, &item.Reason, &createdAt); err != nil {
			return nil, fmt.Errorf("scan reader effect: %w", err)
		}
		item.CreatedAt = parseTime(createdAt)
		out[item.FragmentID] = append(out[item.FragmentID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reader effects: %w", err)
	}
	return out, nil
}
