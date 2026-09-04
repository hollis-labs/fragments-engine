package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type MediaRepository struct {
	db *sql.DB
}

func NewMediaRepository(db *sql.DB) *MediaRepository {
	return &MediaRepository{db: db}
}

// MediaConflictError distinguishes idempotent replay from reuse of a stable
// media/variant/placement identity with incompatible semantics.
type MediaConflictError struct {
	Kind     string
	Identity string
	Reason   string
}

func (e *MediaConflictError) Error() string {
	return fmt.Sprintf("%s conflict for %q: %s", e.Kind, e.Identity, e.Reason)
}

// MediaWriteConn is the transaction-ready boundary used by capture acceptance.
// Both *sql.Conn (inside BEGIN IMMEDIATE) and *sql.Tx satisfy it.
type MediaWriteConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// MediaTransaction exposes a serialized SQLite write transaction to the media
// service without leaking the connection. It is also useful for a future
// capture service that must compose manifest rows with attempt acceptance.
type MediaTransaction struct {
	conn *sql.Conn
	done bool
}

func (r *MediaRepository) BeginImmediate(ctx context.Context, operation string) (*MediaTransaction, error) {
	conn, err := beginImmediateConn(ctx, r.db, operation)
	if err != nil {
		return nil, err
	}
	return &MediaTransaction{conn: conn}, nil
}

func (tx *MediaTransaction) Conn() MediaWriteConn {
	return tx.conn
}

func (tx *MediaTransaction) Commit(ctx context.Context) error {
	if tx == nil || tx.conn == nil || tx.done {
		return fmt.Errorf("media transaction is not active")
	}
	if _, err := tx.conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	tx.done = true
	// COMMIT is the durability boundary. A pooled-connection close error must
	// not be misreported as commit failure because callers compensate CAS files
	// only when COMMIT itself fails.
	_ = tx.conn.Close()
	return nil
}

func (tx *MediaTransaction) Rollback() error {
	if tx == nil || tx.conn == nil || tx.done {
		return nil
	}
	tx.done = true
	_, err := tx.conn.ExecContext(context.Background(), `ROLLBACK`)
	closeErr := tx.conn.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (r *MediaRepository) UpsertManifest(ctx context.Context, revisionID string, items []domain.MediaManifestItem, now time.Time) ([]domain.MediaManifestItem, error) {
	tx, err := r.BeginImmediate(ctx, "media manifest")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	resolved, err := UpsertMediaManifest(ctx, tx.Conn(), revisionID, items, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit media manifest: %w", err)
	}
	return resolved, nil
}

// UpsertMediaManifest persists one complete ordered manifest using a
// caller-owned transaction. Existing refs are compared, never updated or
// deleted, because their fragment revision is immutable.
func UpsertMediaManifest(ctx context.Context, q MediaWriteConn, revisionID string, items []domain.MediaManifestItem, now time.Time) ([]domain.MediaManifestItem, error) {
	revisionID = strings.TrimSpace(revisionID)
	if revisionID == "" {
		return nil, fmt.Errorf("upsert media manifest: fragment revision ID is required")
	}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM fragment_revisions WHERE id = ?`, revisionID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("upsert media manifest: resolve revision: %w", err)
	}

	ordered := append([]domain.MediaManifestItem(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Attachment.Position < ordered[j].Attachment.Position
	})
	for i := range ordered {
		if ordered[i].Attachment.Position != i {
			return nil, fmt.Errorf("upsert media manifest: positions must be contiguous and zero-based (want %d, got %d)", i, ordered[i].Attachment.Position)
		}
	}

	var existingRefs int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM attachment_refs WHERE fragment_revision_id = ?`, revisionID).Scan(&existingRefs); err != nil {
		return nil, fmt.Errorf("count revision attachment refs: %w", err)
	}
	if existingRefs != 0 && existingRefs != len(ordered) {
		return nil, &MediaConflictError{Kind: "attachment manifest", Identity: revisionID, Reason: "immutable revision already has a different attachment count"}
	}

	resolved := make([]domain.MediaManifestItem, 0, len(ordered))
	for i := range ordered {
		item := ordered[i]
		asset, err := upsertMediaAsset(ctx, q, item.Asset, now)
		if err != nil {
			return nil, err
		}
		item.Asset = asset
		item.Variants, err = upsertAssetVariants(ctx, q, asset.ID, item.Variants, now)
		if err != nil {
			return nil, err
		}
		item.Attachment.FragmentRevisionID = revisionID
		item.Attachment.MediaAssetID = asset.ID
		item.Attachment, err = insertOrCompareAttachmentRef(ctx, q, item.Attachment, now)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, item)
	}
	return resolved, nil
}

func upsertMediaAsset(ctx context.Context, q MediaWriteConn, in domain.MediaAsset, now time.Time) (domain.MediaAsset, error) {
	in.SourceRegistrationID = strings.TrimSpace(in.SourceRegistrationID)
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.ProviderMediaID = strings.TrimSpace(in.ProviderMediaID)
	in.SourceMediaKey = strings.TrimSpace(in.SourceMediaKey)
	in.SourceLocator = strings.TrimSpace(in.SourceLocator)
	in.SourceAuthority = strings.TrimSpace(in.SourceAuthority)
	in.AltText = strings.TrimSpace(in.AltText)
	if in.SourceRegistrationID == "" || in.Provider == "" {
		return domain.MediaAsset{}, fmt.Errorf("upsert media asset: source registration and provider are required")
	}
	if in.ProviderMediaID == "" && in.SourceMediaKey == "" && in.SourceLocator == "" {
		return domain.MediaAsset{}, fmt.Errorf("upsert media asset: provider media ID, source media key, or source locator is required")
	}
	if !in.Kind.Valid() {
		return domain.MediaAsset{}, fmt.Errorf("upsert media asset: invalid media kind %q", in.Kind)
	}
	if in.Width < 0 || in.Height < 0 || in.DurationSeconds < 0 || in.PageCount < 0 {
		return domain.MediaAsset{}, fmt.Errorf("upsert media asset: dimensions, duration, and page count must be non-negative")
	}
	if in.DefaultCustody == "" {
		in.DefaultCustody = domain.CustodyReference
	}
	if !in.DefaultCustody.Valid() {
		return domain.MediaAsset{}, fmt.Errorf("upsert media asset: invalid default custody %q", in.DefaultCustody)
	}
	if in.SourceAuthority == "" {
		in.SourceAuthority = in.Provider
	}
	if strings.TrimSpace(in.MetadataJSON) == "" {
		in.MetadataJSON = "{}"
	}
	in.ID, in.IdentityKey = domain.StableMediaAssetID(in.SourceRegistrationID, in.Provider, in.ProviderMediaID, in.SourceMediaKey, in.SourceLocator)
	stamp := formatTime(now)
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now.UTC()
	}
	in.UpdatedAt = now.UTC()

	existing, found, err := findMediaAssetByIdentity(ctx, q, in.IdentityKey)
	if err != nil {
		return domain.MediaAsset{}, err
	}
	if in.ProviderMediaID != "" {
		observed, observedFound, observationErr := findMediaAssetByObservation(ctx, q, in)
		if observationErr != nil {
			return domain.MediaAsset{}, observationErr
		}
		if found && observedFound && existing.ID != observed.ID {
			return domain.MediaAsset{}, &MediaConflictError{Kind: "media asset", Identity: in.IdentityKey, Reason: "provider identity and prior source observation resolve to different assets"}
		}
		if !found && observedFound {
			existing, found = observed, true
		}
		if found && existing.IdentityKey != in.IdentityKey {
			if existing.ProviderMediaID != "" && existing.ProviderMediaID != in.ProviderMediaID {
				return domain.MediaAsset{}, &MediaConflictError{Kind: "media asset", Identity: in.IdentityKey, Reason: "provider media ID conflicts with the observed asset"}
			}
			if _, err := q.ExecContext(ctx, `
INSERT INTO media_asset_identity_aliases(identity_key, media_asset_id, created_at)
VALUES (?, ?, ?)
ON CONFLICT(identity_key) DO NOTHING`, in.IdentityKey, existing.ID, stamp); err != nil {
				return domain.MediaAsset{}, fmt.Errorf("record stronger media identity: %w", err)
			}
			var claimedID string
			if err := q.QueryRowContext(ctx, `SELECT media_asset_id FROM media_asset_identity_aliases WHERE identity_key = ?`, in.IdentityKey).Scan(&claimedID); err != nil {
				return domain.MediaAsset{}, fmt.Errorf("verify stronger media identity: %w", err)
			}
			if claimedID != existing.ID {
				return domain.MediaAsset{}, &MediaConflictError{Kind: "media identity alias", Identity: in.IdentityKey, Reason: "identity is already claimed by another asset"}
			}
			if _, err := q.ExecContext(ctx, `
UPDATE media_assets SET provider_media_id = CASE WHEN provider_media_id = '' THEN ? ELSE provider_media_id END,
  updated_at = ? WHERE id = ?`, in.ProviderMediaID, stamp, existing.ID); err != nil {
				return domain.MediaAsset{}, fmt.Errorf("complete provider media identity: %w", err)
			}
			existing, err = loadMediaAsset(ctx, q, existing.ID)
			if err != nil {
				return domain.MediaAsset{}, err
			}
		}
	}
	if found {
		if existing.Kind != in.Kind {
			if existing.Kind != domain.MediaOther || in.Kind == domain.MediaOther {
				return domain.MediaAsset{}, &MediaConflictError{Kind: "media asset", Identity: in.IdentityKey, Reason: fmt.Sprintf("kind changed from %s to %s", existing.Kind, in.Kind)}
			}
			if _, err := q.ExecContext(ctx, `UPDATE media_assets SET kind = ?, updated_at = ? WHERE id = ?`, string(in.Kind), stamp, existing.ID); err != nil {
				return domain.MediaAsset{}, fmt.Errorf("refine media asset kind: %w", err)
			}
			existing.Kind = in.Kind
		}
		if existing.SourceRegistrationID != in.SourceRegistrationID || existing.Provider != in.Provider ||
			(existing.ProviderMediaID != "" && in.ProviderMediaID != "" && existing.ProviderMediaID != in.ProviderMediaID) ||
			(existing.ProviderMediaID == "" && existing.SourceLocator == "" && existing.SourceMediaKey != in.SourceMediaKey) {
			return domain.MediaAsset{}, &MediaConflictError{Kind: "media asset", Identity: in.IdentityKey, Reason: "stable source identity changed"}
		}
		// Observed descriptive fields may fill a previously unknown value. A
		// contradictory non-zero value is retained as a conflict, not overwritten.
		if reason := mediaAssetSemanticConflict(existing, in); reason != "" {
			return domain.MediaAsset{}, &MediaConflictError{Kind: "media asset", Identity: in.IdentityKey, Reason: reason}
		}
		_, err := q.ExecContext(ctx, `
UPDATE media_assets SET
  source_locator = CASE WHEN source_locator = '' THEN ? ELSE source_locator END,
  width = CASE WHEN width = 0 THEN ? ELSE width END,
  height = CASE WHEN height = 0 THEN ? ELSE height END,
  duration_seconds = CASE WHEN duration_seconds = 0 THEN ? ELSE duration_seconds END,
  page_count = CASE WHEN page_count = 0 THEN ? ELSE page_count END,
  alt_text = CASE WHEN alt_text = '' THEN ? ELSE alt_text END,
  metadata_json = CASE WHEN metadata_json = '{}' THEN ? ELSE metadata_json END,
  updated_at = ?
WHERE id = ?`, in.SourceLocator, in.Width, in.Height, in.DurationSeconds,
			in.PageCount, in.AltText, in.MetadataJSON, stamp, existing.ID)
		if err != nil {
			return domain.MediaAsset{}, fmt.Errorf("fill media asset observations: %w", err)
		}
		if err := insertMediaSourceObservation(ctx, q, existing.ID, in.SourceMediaKey, in.SourceLocator, now); err != nil {
			return domain.MediaAsset{}, err
		}
		return loadMediaAsset(ctx, q, existing.ID)
	}
	_, err = q.ExecContext(ctx, `
INSERT INTO media_assets (
  id, identity_key, source_registration_id, provider, provider_media_id,
  source_media_key, source_locator, kind, width, height, duration_seconds,
  page_count, alt_text, source_authority, default_custody, metadata_json,
  legacy_attachment_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
		in.ID, in.IdentityKey, in.SourceRegistrationID, in.Provider,
		in.ProviderMediaID, in.SourceMediaKey, in.SourceLocator, string(in.Kind),
		in.Width, in.Height, in.DurationSeconds, in.PageCount, in.AltText,
		in.SourceAuthority, string(in.DefaultCustody), in.MetadataJSON,
		in.LegacyAttachmentID, formatTime(in.CreatedAt), stamp)
	if err != nil {
		return domain.MediaAsset{}, fmt.Errorf("insert media asset: %w", err)
	}
	if err := insertMediaSourceObservation(ctx, q, in.ID, in.SourceMediaKey, in.SourceLocator, now); err != nil {
		return domain.MediaAsset{}, err
	}
	return loadMediaAsset(ctx, q, in.ID)
}

func insertMediaSourceObservation(ctx context.Context, q MediaWriteConn, assetID, sourceMediaKey, sourceLocator string, observedAt time.Time) error {
	id := domain.DigestText("media-source-observation\n" + assetID + "\n" + sourceMediaKey + "\n" + sourceLocator)
	_, err := q.ExecContext(ctx, `
INSERT INTO media_asset_source_observations (
  id, media_asset_id, source_media_key, source_locator, observed_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(media_asset_id, source_media_key, source_locator) DO NOTHING`,
		id, assetID, sourceMediaKey, sourceLocator, formatTime(observedAt))
	if err != nil {
		return fmt.Errorf("record media source observation: %w", err)
	}
	return nil
}

func mediaAssetSemanticConflict(a, b domain.MediaAsset) string {
	checks := []struct {
		name string
		a    any
		b    any
		set  bool
	}{
		{"width", a.Width, b.Width, a.Width != 0 && b.Width != 0},
		{"height", a.Height, b.Height, a.Height != 0 && b.Height != 0},
		{"duration", a.DurationSeconds, b.DurationSeconds, a.DurationSeconds != 0 && b.DurationSeconds != 0},
		{"page count", a.PageCount, b.PageCount, a.PageCount != 0 && b.PageCount != 0},
		{"source authority", a.SourceAuthority, b.SourceAuthority, a.SourceAuthority != "" && b.SourceAuthority != ""},
		{"default custody", a.DefaultCustody, b.DefaultCustody, a.DefaultCustody != "" && b.DefaultCustody != ""},
	}
	for _, check := range checks {
		if check.set && check.a != check.b {
			return check.name + " changed"
		}
	}
	return ""
}

func upsertAssetVariants(ctx context.Context, q MediaWriteConn, assetID string, variants []domain.AssetVariant, now time.Time) ([]domain.AssetVariant, error) {
	if len(variants) == 0 {
		return nil, fmt.Errorf("upsert asset variants: at least one variant is required")
	}
	seen := make(map[string]struct{}, len(variants))
	resolved := make([]domain.AssetVariant, 0, len(variants))
	for _, variant := range variants {
		variant.MediaAssetID = assetID
		variant.VariantIdentity = strings.TrimSpace(variant.VariantIdentity)
		if variant.VariantIdentity == "" {
			return nil, fmt.Errorf("upsert asset variant: variant identity is required")
		}
		if _, ok := seen[variant.VariantIdentity]; ok {
			return nil, fmt.Errorf("upsert asset variant: duplicate identity %q", variant.VariantIdentity)
		}
		seen[variant.VariantIdentity] = struct{}{}
		item, err := upsertAssetVariant(ctx, q, variant, now)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, item)
	}
	return resolved, nil
}

func upsertAssetVariant(ctx context.Context, q MediaWriteConn, in domain.AssetVariant, now time.Time) (domain.AssetVariant, error) {
	if !in.Kind.Valid() {
		return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: invalid variant kind %q", in.Kind)
	}
	in.SourceURL = strings.TrimSpace(in.SourceURL)
	in.SourcePath = strings.TrimSpace(in.SourcePath)
	in.MIMEType = strings.ToLower(strings.TrimSpace(in.MIMEType))
	if in.Width < 0 || in.Height < 0 || in.DurationSeconds < 0 || in.ByteSize < 0 {
		return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: dimensions, duration, and byte size must be non-negative")
	}
	if !in.ExpectedDigest.Empty() {
		var err error
		in.ExpectedDigest, err = in.ExpectedDigest.Normalized()
		if err != nil {
			return domain.AssetVariant{}, fmt.Errorf("upsert asset variant expected %w", err)
		}
	}
	if !in.Digest.Empty() {
		var err error
		in.Digest, err = in.Digest.Normalized()
		if err != nil {
			return domain.AssetVariant{}, fmt.Errorf("upsert asset variant actual %w", err)
		}
		if !in.ExpectedDigest.Empty() && in.ExpectedDigest.Value != in.Digest.Value {
			return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: expected and actual digests differ")
		}
	}
	if strings.TrimSpace(in.BlobDigest) != "" {
		blobDigest, err := (domain.ContentDigest{Algorithm: "sha256", Value: in.BlobDigest}).Normalized()
		if err != nil {
			return domain.AssetVariant{}, fmt.Errorf("upsert asset variant blob %w", err)
		}
		in.BlobDigest = blobDigest.Value
		if in.Digest.Empty() || in.Digest.Value != in.BlobDigest {
			return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: blob digest must equal actual digest")
		}
	}
	if in.Custody == "" {
		in.Custody = domain.CustodyReference
	}
	if !in.Custody.Valid() {
		return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: invalid custody %q", in.Custody)
	}
	if in.AcquisitionState == "" {
		if in.Custody == domain.CustodyReference {
			in.AcquisitionState = domain.AcquisitionReferenceOnly
		} else if in.SourcePath != "" {
			in.AcquisitionState = domain.AcquisitionAvailable
		} else {
			in.AcquisitionState = domain.AcquisitionPending
		}
	}
	if !in.AcquisitionState.Valid() {
		return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: invalid acquisition state %q", in.AcquisitionState)
	}
	if in.AcquisitionState == domain.AcquisitionFailed && (in.Failure == nil || strings.TrimSpace(in.Failure.Code) == "" || strings.TrimSpace(in.Failure.Message) == "") {
		return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: failed state requires a coded failure")
	}
	if in.Retention == "" {
		switch in.Custody {
		case domain.CustodyMirror, domain.CustodyAdopted:
			in.Retention = domain.RetentionIndefinite
		case domain.CustodyCache:
			in.Retention = domain.RetentionCache
		default:
			in.Retention = domain.RetentionExternal
		}
	}
	if (in.Custody == domain.CustodyMirror || in.Custody == domain.CustodyAdopted) && in.Retention != domain.RetentionIndefinite {
		return domain.AssetVariant{}, fmt.Errorf("upsert asset variant: mirrored and adopted bytes default to indefinite retention")
	}
	if strings.TrimSpace(in.MetadataJSON) == "" {
		in.MetadataJSON = "{}"
	}
	in.ID = domain.StableAssetVariantID(in.MediaAssetID, in.VariantIdentity)
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now.UTC()
	}
	in.UpdatedAt = now.UTC()

	existing, found, err := findAssetVariantByIdentity(ctx, q, in.MediaAssetID, in.VariantIdentity)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	if found {
		if reason := assetVariantSemanticConflict(existing, in); reason != "" {
			return domain.AssetVariant{}, &MediaConflictError{Kind: "asset variant", Identity: in.VariantIdentity, Reason: reason}
		}
		// URLs and expiry hints can legitimately rotate. Fill descriptive gaps,
		// but acquisition state/digest are changed only by explicit operations.
		_, err := q.ExecContext(ctx, `
UPDATE asset_variants SET
  source_url = CASE WHEN ? != '' THEN ? ELSE source_url END,
  source_expires_at = CASE WHEN ? != '' THEN ? ELSE source_expires_at END,
  source_path = CASE WHEN source_path = '' THEN ? ELSE source_path END,
  mime_type = CASE WHEN mime_type = '' THEN ? ELSE mime_type END,
  width = CASE WHEN width = 0 THEN ? ELSE width END,
  height = CASE WHEN height = 0 THEN ? ELSE height END,
  duration_seconds = CASE WHEN duration_seconds = 0 THEN ? ELSE duration_seconds END,
  byte_size = CASE WHEN byte_size = 0 THEN ? ELSE byte_size END,
  expected_digest = CASE WHEN expected_digest = '' THEN ? ELSE expected_digest END,
  metadata_json = CASE WHEN metadata_json = '{}' THEN ? ELSE metadata_json END,
  updated_at = ?
WHERE id = ?`, in.SourceURL, in.SourceURL, formatOptionalTime(in.SourceExpiresAt),
			formatOptionalTime(in.SourceExpiresAt), in.SourcePath, in.MIMEType, in.Width,
			in.Height, in.DurationSeconds, in.ByteSize, in.ExpectedDigest.Value,
			in.MetadataJSON, formatTime(now), existing.ID)
		if err != nil {
			return domain.AssetVariant{}, fmt.Errorf("fill asset variant observations: %w", err)
		}
		if err := insertVariantSourceObservation(ctx, q, existing.ID, in.SourceURL, in.SourceExpiresAt, now); err != nil {
			return domain.AssetVariant{}, err
		}
		return loadAssetVariant(ctx, q, existing.ID)
	}
	failure := domain.AssetFailure{}
	if in.Failure != nil {
		failure = *in.Failure
	}
	_, err = q.ExecContext(ctx, `
INSERT INTO asset_variants (
  id, media_asset_id, variant_identity, kind, source_url, source_expires_at,
  source_path, mime_type, width, height, duration_seconds, byte_size,
  expected_digest, digest, blob_digest, custody, acquisition_state,
  failure_code, failure_message, failure_retryable, retention, metadata_json,
  legacy_storage_path, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.MediaAssetID, in.VariantIdentity, string(in.Kind), in.SourceURL,
		formatOptionalTime(in.SourceExpiresAt), in.SourcePath, in.MIMEType, in.Width,
		in.Height, in.DurationSeconds, in.ByteSize, in.ExpectedDigest.Value,
		in.Digest.Value, in.BlobDigest, string(in.Custody), string(in.AcquisitionState),
		failure.Code, failure.Message, boolInt(failure.Retryable), string(in.Retention),
		in.MetadataJSON, in.LegacyStoragePath, formatTime(in.CreatedAt), formatTime(now))
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("insert asset variant: %w", err)
	}
	if err := insertVariantSourceObservation(ctx, q, in.ID, in.SourceURL, in.SourceExpiresAt, now); err != nil {
		return domain.AssetVariant{}, err
	}
	return loadAssetVariant(ctx, q, in.ID)
}

func insertVariantSourceObservation(ctx context.Context, q MediaWriteConn, variantID, sourceURL string, expiresAt, observedAt time.Time) error {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return nil
	}
	expiry := formatOptionalTime(expiresAt)
	id := domain.DigestText("variant-source-observation\n" + variantID + "\n" + sourceURL + "\n" + expiry)
	_, err := q.ExecContext(ctx, `
INSERT INTO asset_variant_source_observations (
  id, asset_variant_id, source_url, source_expires_at, observed_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(asset_variant_id, source_url, source_expires_at) DO NOTHING`,
		id, variantID, sourceURL, expiry, formatTime(observedAt))
	if err != nil {
		return fmt.Errorf("record asset variant source observation: %w", err)
	}
	return nil
}

func assetVariantSemanticConflict(a, b domain.AssetVariant) string {
	if a.Kind != b.Kind {
		return fmt.Sprintf("kind changed from %s to %s", a.Kind, b.Kind)
	}
	if a.Custody != b.Custody {
		return fmt.Sprintf("custody changed from %s to %s", a.Custody, b.Custody)
	}
	if !a.ExpectedDigest.Empty() && !b.ExpectedDigest.Empty() && a.ExpectedDigest.Value != b.ExpectedDigest.Value {
		return "expected digest changed"
	}
	if !a.Digest.Empty() && !b.ExpectedDigest.Empty() && a.Digest.Value != b.ExpectedDigest.Value {
		return "expected digest conflicts with acquired bytes"
	}
	if !a.Digest.Empty() && !b.Digest.Empty() && a.Digest.Value != b.Digest.Value {
		return "actual digest changed"
	}
	checks := []struct {
		name string
		a    any
		b    any
		set  bool
	}{
		{"MIME type", a.MIMEType, b.MIMEType, a.MIMEType != "" && b.MIMEType != ""},
		{"width", a.Width, b.Width, a.Width != 0 && b.Width != 0},
		{"height", a.Height, b.Height, a.Height != 0 && b.Height != 0},
		{"duration", a.DurationSeconds, b.DurationSeconds, a.DurationSeconds != 0 && b.DurationSeconds != 0},
		{"byte size", a.ByteSize, b.ByteSize, a.ByteSize != 0 && b.ByteSize != 0},
	}
	for _, check := range checks {
		if check.set && check.a != check.b {
			return check.name + " changed"
		}
	}
	return ""
}

func insertOrCompareAttachmentRef(ctx context.Context, q MediaWriteConn, in domain.AttachmentRef, now time.Time) (domain.AttachmentRef, error) {
	if !in.Role.Valid() {
		return domain.AttachmentRef{}, fmt.Errorf("insert attachment ref: invalid role %q", in.Role)
	}
	if in.Position < 0 {
		return domain.AttachmentRef{}, fmt.Errorf("insert attachment ref: position must be non-negative")
	}
	in.ID = domain.StableAttachmentRefID(in.FragmentRevisionID, in.Position)
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now.UTC()
	}
	existing, found, err := findAttachmentRefAtPosition(ctx, q, in.FragmentRevisionID, in.Position)
	if err != nil {
		return domain.AttachmentRef{}, err
	}
	if found {
		if existing.MediaAssetID != in.MediaAssetID || existing.Role != in.Role ||
			existing.Caption != in.Caption || existing.SourceContext != in.SourceContext {
			return domain.AttachmentRef{}, &MediaConflictError{Kind: "attachment ref", Identity: in.ID, Reason: "immutable placement semantics changed"}
		}
		return existing, nil
	}
	_, err = q.ExecContext(ctx, `
INSERT INTO attachment_refs (
  id, fragment_revision_id, media_asset_id, role, position, caption,
  source_context, legacy_fragment_id, legacy_attachment_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)`,
		in.ID, in.FragmentRevisionID, in.MediaAssetID, string(in.Role), in.Position,
		in.Caption, in.SourceContext, in.LegacyFragmentID, in.LegacyAttachmentID,
		formatTime(in.CreatedAt))
	if err != nil {
		return domain.AttachmentRef{}, fmt.Errorf("insert attachment ref: %w", err)
	}
	return in, nil
}

func (r *MediaRepository) GetVariant(ctx context.Context, variantID string) (domain.AssetVariant, error) {
	return loadAssetVariant(ctx, r.db, strings.TrimSpace(variantID))
}

func (r *MediaRepository) GetVariantOn(ctx context.Context, q MediaWriteConn, variantID string) (domain.AssetVariant, error) {
	return loadAssetVariant(ctx, q, strings.TrimSpace(variantID))
}

func (r *MediaRepository) AttachBlobOn(ctx context.Context, q MediaWriteConn, variantID string, blob domain.MediaBlob, now time.Time) (domain.AssetVariant, error) {
	variant, err := loadAssetVariant(ctx, q, strings.TrimSpace(variantID))
	if err != nil {
		return domain.AssetVariant{}, err
	}
	digest, err := blob.Digest.Normalized()
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("attach blob: %w", err)
	}
	if !variant.ExpectedDigest.Empty() && variant.ExpectedDigest.Value != digest.Value {
		return domain.AssetVariant{}, &MediaConflictError{Kind: "asset variant", Identity: variant.ID, Reason: "blob digest differs from expected digest"}
	}
	if !variant.Digest.Empty() && variant.Digest.Value != digest.Value {
		return domain.AssetVariant{}, &MediaConflictError{Kind: "asset variant", Identity: variant.ID, Reason: "variant already owns different bytes"}
	}
	if variant.Custody == domain.CustodyReference {
		return domain.AssetVariant{}, &MediaConflictError{Kind: "asset variant", Identity: variant.ID, Reason: "reference-only custody cannot own a blob"}
	}
	retention := blob.Retention
	if retention == "" {
		if variant.Custody == domain.CustodyCache {
			retention = domain.RetentionCache
		} else {
			retention = domain.RetentionIndefinite
		}
	}
	if (variant.Custody == domain.CustodyMirror || variant.Custody == domain.CustodyAdopted) && retention != domain.RetentionIndefinite {
		return domain.AssetVariant{}, fmt.Errorf("attach blob: mirrored/adopted retention must be indefinite")
	}
	_, err = q.ExecContext(ctx, `
INSERT INTO media_blobs (digest, algorithm, storage_handle, byte_size, retention, created_at, verified_at)
VALUES (?, 'sha256', ?, ?, ?, ?, ?)
ON CONFLICT(digest) DO UPDATE SET
  retention = CASE
    WHEN media_blobs.retention = 'indefinite' OR excluded.retention = 'indefinite'
      THEN 'indefinite'
    ELSE 'cache'
  END,
  verified_at = excluded.verified_at`, digest.Value, blob.StorageHandle, blob.ByteSize,
		string(retention), formatTime(blob.CreatedAt), formatTime(blob.VerifiedAt))
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("record media blob: %w", err)
	}
	var storedHandle string
	var storedSize int64
	if err := q.QueryRowContext(ctx, `SELECT storage_handle, byte_size FROM media_blobs WHERE digest = ?`, digest.Value).Scan(&storedHandle, &storedSize); err != nil {
		return domain.AssetVariant{}, fmt.Errorf("verify media blob row: %w", err)
	}
	if storedHandle != blob.StorageHandle || storedSize != blob.ByteSize {
		return domain.AssetVariant{}, &MediaConflictError{Kind: "media blob", Identity: digest.Value, Reason: "digest is bound to conflicting storage semantics"}
	}
	_, err = q.ExecContext(ctx, `
UPDATE asset_variants
SET expected_digest = CASE WHEN expected_digest = '' THEN ? ELSE expected_digest END,
    digest = ?, blob_digest = ?, byte_size = ?, acquisition_state = 'available',
    failure_code = '', failure_message = '', failure_retryable = 0,
    retention = ?, updated_at = ?
WHERE id = ?`, digest.Value, digest.Value, digest.Value, blob.ByteSize,
		string(retention), formatTime(now), variant.ID)
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("attach blob to variant: %w", err)
	}
	return loadAssetVariant(ctx, q, variant.ID)
}

func (r *MediaRepository) MarkVariantFailed(ctx context.Context, variantID string, failure domain.AssetFailure, now time.Time) (domain.AssetVariant, error) {
	if strings.TrimSpace(failure.Code) == "" || strings.TrimSpace(failure.Message) == "" {
		return domain.AssetVariant{}, fmt.Errorf("mark variant failed: code and message are required")
	}
	tx, err := r.BeginImmediate(ctx, "mark media variant failed")
	if err != nil {
		return domain.AssetVariant{}, err
	}
	defer tx.Rollback()
	current, err := loadAssetVariant(ctx, tx.Conn(), variantID)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	if current.AcquisitionState == domain.AcquisitionAvailable {
		return domain.AssetVariant{}, &MediaConflictError{Kind: "asset variant", Identity: current.ID, Reason: "available verified bytes cannot be degraded to failed"}
	}
	if _, err := tx.Conn().ExecContext(ctx, `
UPDATE asset_variants SET acquisition_state = 'failed', failure_code = ?,
  failure_message = ?, failure_retryable = ?, updated_at = ? WHERE id = ?`,
		failure.Code, failure.Message, boolInt(failure.Retryable), formatTime(now), variantID); err != nil {
		return domain.AssetVariant{}, fmt.Errorf("mark variant failed: %w", err)
	}
	item, err := loadAssetVariant(ctx, tx.Conn(), variantID)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AssetVariant{}, fmt.Errorf("commit variant failure: %w", err)
	}
	return item, nil
}

func (r *MediaRepository) MarkVariantFailedOn(ctx context.Context, q MediaWriteConn, variantID string, failure domain.AssetFailure, now time.Time) (domain.AssetVariant, error) {
	if strings.TrimSpace(failure.Code) == "" || strings.TrimSpace(failure.Message) == "" {
		return domain.AssetVariant{}, fmt.Errorf("mark variant failed: code and message are required")
	}
	current, err := loadAssetVariant(ctx, q, variantID)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	if current.AcquisitionState == domain.AcquisitionAvailable {
		return domain.AssetVariant{}, &MediaConflictError{Kind: "asset variant", Identity: current.ID, Reason: "available verified bytes cannot be degraded to failed"}
	}
	if _, err := q.ExecContext(ctx, `
UPDATE asset_variants SET acquisition_state = 'failed', failure_code = ?,
  failure_message = ?, failure_retryable = ?, updated_at = ? WHERE id = ?`,
		failure.Code, failure.Message, boolInt(failure.Retryable), formatTime(now), variantID); err != nil {
		return domain.AssetVariant{}, fmt.Errorf("mark variant failed: %w", err)
	}
	return loadAssetVariant(ctx, q, variantID)
}

func (r *MediaRepository) ListByRevision(ctx context.Context, revisionID string) ([]domain.MediaManifestItem, error) {
	refs, err := listAttachmentRefs(ctx, r.db, revisionID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.MediaManifestItem, 0, len(refs))
	for _, ref := range refs {
		asset, err := loadMediaAsset(ctx, r.db, ref.MediaAssetID)
		if err != nil {
			return nil, err
		}
		variants, err := listAssetVariants(ctx, r.db, asset.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.MediaManifestItem{Asset: asset, Variants: variants, Attachment: ref})
	}
	return out, nil
}

func (r *MediaRepository) BlobReferenceCount(ctx context.Context, digest string) (int, error) {
	return r.BlobReferenceCountOn(ctx, r.db, digest)
}

func (r *MediaRepository) BlobReferenceCountOn(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, digest string) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_variants WHERE blob_digest = ?`, strings.TrimSpace(digest)).Scan(&count)
	return count, err
}

func (r *MediaRepository) FindBlobByDigestOn(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, digest string) (domain.MediaBlob, bool, error) {
	digest = strings.TrimSpace(digest)
	var blob domain.MediaBlob
	var algorithm, retention, createdAt, verifiedAt string
	err := q.QueryRowContext(ctx, `
SELECT digest, algorithm, storage_handle, byte_size, retention, created_at, verified_at
FROM media_blobs WHERE digest = ?`, digest).Scan(&blob.Digest.Value, &algorithm,
		&blob.StorageHandle, &blob.ByteSize, &retention, &createdAt, &verifiedAt)
	if err == sql.ErrNoRows {
		return domain.MediaBlob{}, false, nil
	}
	if err != nil {
		return domain.MediaBlob{}, false, err
	}
	blob.Digest.Algorithm = algorithm
	blob.Retention = domain.RetentionPolicy(retention)
	blob.CreatedAt = parseTime(createdAt)
	blob.VerifiedAt = parseTime(verifiedAt)
	return blob, true, nil
}

func findMediaAssetByIdentity(ctx context.Context, q MediaWriteConn, identity string) (domain.MediaAsset, bool, error) {
	var id sql.NullString
	err := q.QueryRowContext(ctx, `
SELECT COALESCE(
  (SELECT id FROM media_assets WHERE identity_key = ?),
  (SELECT media_asset_id FROM media_asset_identity_aliases WHERE identity_key = ?)
)`, identity, identity).Scan(&id)
	if err == sql.ErrNoRows || !id.Valid || id.String == "" {
		return domain.MediaAsset{}, false, nil
	}
	if err != nil {
		return domain.MediaAsset{}, false, fmt.Errorf("find media asset: %w", err)
	}
	item, err := loadMediaAsset(ctx, q, id.String)
	return item, err == nil, err
}

func findMediaAssetByObservation(ctx context.Context, q MediaWriteConn, in domain.MediaAsset) (domain.MediaAsset, bool, error) {
	if strings.TrimSpace(in.SourceLocator) != "" {
		item, found, err := findMediaAssetByObservedField(ctx, q, in, `o.source_locator = ?`, in.SourceLocator)
		if err != nil || found {
			return item, found, err
		}
	}
	if strings.TrimSpace(in.SourceMediaKey) == "" {
		return domain.MediaAsset{}, false, nil
	}
	// A source/client key is only an identity fallback when the earlier asset
	// had no locator. Positional keys such as generic:image:0 legitimately recur
	// for unrelated pages and must never join locator-keyed assets.
	return findMediaAssetByObservedField(ctx, q, in, `o.source_media_key = ? AND ma.source_locator = ''`, in.SourceMediaKey)
}

func findMediaAssetByObservedField(ctx context.Context, q MediaWriteConn, in domain.MediaAsset, predicate, value string) (domain.MediaAsset, bool, error) {
	var id string
	err := q.QueryRowContext(ctx, `
SELECT ma.id
FROM media_assets ma
JOIN media_asset_source_observations o ON o.media_asset_id = ma.id
WHERE ma.source_registration_id = ? AND ma.provider = ? AND `+predicate+`
ORDER BY ma.created_at, ma.id
LIMIT 1`, in.SourceRegistrationID, in.Provider, value).Scan(&id)
	if err == sql.ErrNoRows {
		return domain.MediaAsset{}, false, nil
	}
	if err != nil {
		return domain.MediaAsset{}, false, fmt.Errorf("find media asset by source observation: %w", err)
	}
	item, err := loadMediaAsset(ctx, q, id)
	return item, err == nil, err
}

func loadMediaAsset(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.MediaAsset, error) {
	var item domain.MediaAsset
	var kind, custody, createdAt, updatedAt string
	err := q.QueryRowContext(ctx, `
SELECT id, identity_key, source_registration_id, provider, provider_media_id,
       source_media_key, source_locator, kind, width, height, duration_seconds,
       page_count, alt_text, source_authority, default_custody, metadata_json,
       COALESCE(legacy_attachment_id, ''), created_at, updated_at
FROM media_assets WHERE id = ?`, id).Scan(
		&item.ID, &item.IdentityKey, &item.SourceRegistrationID, &item.Provider,
		&item.ProviderMediaID, &item.SourceMediaKey, &item.SourceLocator, &kind,
		&item.Width, &item.Height, &item.DurationSeconds, &item.PageCount,
		&item.AltText, &item.SourceAuthority, &custody, &item.MetadataJSON,
		&item.LegacyAttachmentID, &createdAt, &updatedAt)
	if err != nil {
		return domain.MediaAsset{}, fmt.Errorf("load media asset: %w", err)
	}
	item.Kind = domain.MediaKind(kind)
	item.DefaultCustody = domain.CustodyMode(custody)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func findAssetVariantByIdentity(ctx context.Context, q MediaWriteConn, assetID, identity string) (domain.AssetVariant, bool, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT id FROM asset_variants WHERE media_asset_id = ? AND variant_identity = ?`, assetID, identity).Scan(&id)
	if err == sql.ErrNoRows {
		return domain.AssetVariant{}, false, nil
	}
	if err != nil {
		return domain.AssetVariant{}, false, fmt.Errorf("find asset variant: %w", err)
	}
	item, err := loadAssetVariant(ctx, q, id)
	return item, err == nil, err
}

func loadAssetVariant(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.AssetVariant, error) {
	var item domain.AssetVariant
	var kind, custody, state, expiresAt, expectedDigest, digest, blobDigest, retention, createdAt, updatedAt string
	var failureCode, failureMessage string
	var failureRetryable int
	err := q.QueryRowContext(ctx, `
SELECT id, media_asset_id, variant_identity, kind, source_url,
       source_expires_at, source_path, mime_type, width, height,
       duration_seconds, byte_size, expected_digest, digest,
       COALESCE(blob_digest, ''), custody, acquisition_state, failure_code,
       failure_message, failure_retryable, retention, metadata_json,
       legacy_storage_path, created_at, updated_at
FROM asset_variants WHERE id = ?`, id).Scan(
		&item.ID, &item.MediaAssetID, &item.VariantIdentity, &kind,
		&item.SourceURL, &expiresAt, &item.SourcePath, &item.MIMEType,
		&item.Width, &item.Height, &item.DurationSeconds, &item.ByteSize,
		&expectedDigest, &digest, &blobDigest, &custody, &state, &failureCode,
		&failureMessage, &failureRetryable, &retention, &item.MetadataJSON,
		&item.LegacyStoragePath, &createdAt, &updatedAt)
	if err != nil {
		return domain.AssetVariant{}, fmt.Errorf("load asset variant: %w", err)
	}
	item.Kind = domain.AssetVariantKind(kind)
	item.Custody = domain.CustodyMode(custody)
	item.AcquisitionState = domain.AcquisitionState(state)
	item.Retention = domain.RetentionPolicy(retention)
	if expectedDigest != "" {
		item.ExpectedDigest = domain.ContentDigest{Algorithm: "sha256", Value: expectedDigest}
	}
	if digest != "" {
		item.Digest = domain.ContentDigest{Algorithm: "sha256", Value: digest}
	}
	item.BlobDigest = blobDigest
	item.SourceExpiresAt = parseTime(expiresAt)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	if failureCode != "" || failureMessage != "" {
		item.Failure = &domain.AssetFailure{Code: failureCode, Message: failureMessage, Retryable: failureRetryable == 1}
	}
	return item, nil
}

func findAttachmentRefAtPosition(ctx context.Context, q MediaWriteConn, revisionID string, position int) (domain.AttachmentRef, bool, error) {
	var item domain.AttachmentRef
	var role, createdAt string
	err := q.QueryRowContext(ctx, `
SELECT id, fragment_revision_id, media_asset_id, role, position, caption,
       source_context, COALESCE(legacy_fragment_id, ''),
       COALESCE(legacy_attachment_id, ''), created_at
FROM attachment_refs WHERE fragment_revision_id = ? AND position = ?`, revisionID, position).Scan(
		&item.ID, &item.FragmentRevisionID, &item.MediaAssetID, &role, &item.Position,
		&item.Caption, &item.SourceContext, &item.LegacyFragmentID,
		&item.LegacyAttachmentID, &createdAt)
	if err == sql.ErrNoRows {
		return domain.AttachmentRef{}, false, nil
	}
	if err != nil {
		return domain.AttachmentRef{}, false, fmt.Errorf("find attachment ref: %w", err)
	}
	item.Role = domain.AttachmentRole(role)
	item.CreatedAt = parseTime(createdAt)
	return item, true, nil
}

type mediaQueryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func listAttachmentRefs(ctx context.Context, q mediaQueryContext, revisionID string) ([]domain.AttachmentRef, error) {
	rows, err := q.QueryContext(ctx, `
SELECT id, fragment_revision_id, media_asset_id, role, position, caption,
       source_context, COALESCE(legacy_fragment_id, ''),
       COALESCE(legacy_attachment_id, ''), created_at
FROM attachment_refs WHERE fragment_revision_id = ? ORDER BY position`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("list attachment refs: %w", err)
	}
	defer rows.Close()
	var out []domain.AttachmentRef
	for rows.Next() {
		var item domain.AttachmentRef
		var role, createdAt string
		if err := rows.Scan(&item.ID, &item.FragmentRevisionID, &item.MediaAssetID,
			&role, &item.Position, &item.Caption, &item.SourceContext,
			&item.LegacyFragmentID, &item.LegacyAttachmentID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan attachment ref: %w", err)
		}
		item.Role = domain.AttachmentRole(role)
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func listAssetVariants(ctx context.Context, q mediaQueryContext, assetID string) ([]domain.AssetVariant, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM asset_variants WHERE media_asset_id = ? ORDER BY kind, id`, assetID)
	if err != nil {
		return nil, fmt.Errorf("list asset variants: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.AssetVariant, 0, len(ids))
	for _, id := range ids {
		item, err := loadAssetVariant(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func listMediaByRevision(ctx context.Context, q mediaQueryContext, revisionID string) ([]domain.MediaManifestItem, error) {
	refs, err := listAttachmentRefs(ctx, q, revisionID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.MediaManifestItem, 0, len(refs))
	for _, ref := range refs {
		asset, err := loadMediaAsset(ctx, q, ref.MediaAssetID)
		if err != nil {
			return nil, err
		}
		variants, err := listAssetVariants(ctx, q, asset.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.MediaManifestItem{Asset: asset, Variants: variants, Attachment: ref})
	}
	return out, nil
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
