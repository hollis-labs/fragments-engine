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

type FragmentRepository struct {
	db *sql.DB
}

type UpsertOutcome string

const (
	UpsertInserted UpsertOutcome = "inserted"
	UpsertUpdated  UpsertOutcome = "updated"
	UpsertSkipped  UpsertOutcome = "skipped"
)

func NewFragmentRepository(db *sql.DB) *FragmentRepository {
	return &FragmentRepository{db: db}
}

func BuildFragment(in domain.PipelineFragment, ingestName string, now time.Time) (domain.Fragment, error) {
	if strings.TrimSpace(in.Source) == "" {
		return domain.Fragment{}, fmt.Errorf("build fragment: source is required")
	}
	if strings.TrimSpace(in.SourceID) == "" {
		return domain.Fragment{}, fmt.Errorf("build fragment: source_id is required")
	}
	if strings.TrimSpace(in.Content) == "" {
		return domain.Fragment{}, fmt.Errorf("build fragment: content is required")
	}

	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	identity := domain.NormalizeSourceIdentity(in, ingestName)
	if identity.SourceRegistrationID == "" {
		return domain.Fragment{}, fmt.Errorf("build fragment: source registration id is required")
	}
	if identity.SourceItemKey == "" {
		return domain.Fragment{}, fmt.Errorf("build fragment: source item key is required")
	}
	if identity.SegmentKey == "" {
		return domain.Fragment{}, fmt.Errorf("build fragment: segment key is required")
	}
	normalizer := in.Normalizer
	if strings.TrimSpace(normalizer.Adapter) == "" {
		normalizer.Adapter = domain.DefaultNormalizerAdapter
	}
	if strings.TrimSpace(normalizer.Version) == "" {
		normalizer.Version = domain.DefaultAdapterVersion
	}
	material := domain.NormalizeMaterial(in.Title, in.Description, in.Content, in.ContentFormat, in.Attachments)
	contentHash := domain.DigestText(material.Content)
	fragmentID := domain.StableFragmentID(identity)
	metaJSON := "{}"
	if len(in.Metadata) > 0 {
		raw, err := json.Marshal(in.Metadata)
		if err != nil {
			return domain.Fragment{}, fmt.Errorf("build fragment metadata: %w", err)
		}
		metaJSON = string(raw)
	}

	return domain.Fragment{
		ID:             fragmentID,
		Source:         in.Source,
		SourceType:     in.SourceType,
		SourceID:       in.SourceID,
		SourceIdentity: identity,
		Title:          material.Title,
		Content:        material.Content,
		ContentHash:    contentHash,
		CreatedAt:      in.CreatedAt.UTC(),
		IngestedAt:     now.UTC(),
		Status:         domain.FragmentStatusInbox,
		MetadataJSON:   metaJSON,
		IngestName:     ingestName,
		CanonicalPath:  in.CanonicalPath,
		Revision: domain.FragmentRevision{
			FragmentID:         fragmentID,
			MaterialDigest:     material.Digest(),
			ContentDigest:      contentHash,
			Title:              material.Title,
			Description:        material.Description,
			Content:            material.Content,
			ContentFormat:      material.ContentFormat,
			OrderedMediaDigest: material.OrderedMediaDigest,
			MetadataJSON:       metaJSON,
			Normalizer:         normalizer,
			ObservedAt:         in.CreatedAt.UTC(),
			CommittedAt:        now.UTC(),
		},
	}, nil
}

func (r *FragmentRepository) Upsert(ctx context.Context, fragment domain.Fragment) (UpsertOutcome, error) {
	_, outcome, err := r.UpsertResolved(ctx, fragment)
	return outcome, err
}

// UpsertResolved applies the stable identity/revision invariant and returns the
// persisted canonical Fragment. Callers that perform downstream writes must use
// this result because a migrated identity can resolve a freshly built candidate
// to a preserved legacy primary key.
func (r *FragmentRepository) UpsertResolved(ctx context.Context, fragment domain.Fragment) (domain.Fragment, UpsertOutcome, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return domain.Fragment{}, "", fmt.Errorf("acquire fragment upsert connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return domain.Fragment{}, "", fmt.Errorf("configure fragment upsert connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return domain.Fragment{}, "", fmt.Errorf("enable fragment upsert foreign keys: %w", err)
	}
	// Acquire the SQLite write reservation before identity lookup so separate
	// repository instances and database handles cannot both observe a missing
	// identity and race to create it.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return domain.Fragment{}, "", fmt.Errorf("begin immediate fragment upsert: %w", err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)

	resolved, outcome, err := upsertFragmentResolved(ctx, conn, fragment)
	if err != nil {
		return domain.Fragment{}, "", err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.Fragment{}, "", fmt.Errorf("commit fragment upsert: %w", err)
	}
	return resolved, outcome, nil
}

// upsertFragmentResolved applies the identity/revision write using a
// caller-owned SQLite transaction. Keeping the commit boundary outside this
// helper lets capture acceptance compose fragment/revision resolution with the
// attempt and additive context in one BEGIN IMMEDIATE transaction.
func upsertFragmentResolved(ctx context.Context, conn fragmentWriteConn, fragment domain.Fragment) (domain.Fragment, UpsertOutcome, error) {
	identity := fragment.SourceIdentity
	if identity.SourceRegistrationID == "" || identity.SourceItemKey == "" || identity.SegmentKey == "" {
		return domain.Fragment{}, "", fmt.Errorf("upsert fragment: stable source identity is incomplete")
	}
	if fragment.Revision.MaterialDigest == "" {
		return domain.Fragment{}, "", fmt.Errorf("upsert fragment: material digest is required")
	}

	resolvedID, err := findFragmentByIdentity(ctx, conn, identity)
	inserted := false
	if err == sql.ErrNoRows {
		resolvedID = fragment.ID
		storageHash, err := availableLegacyContentHash(ctx, conn, fragment, fragment.ID)
		if err != nil {
			return domain.Fragment{}, "", err
		}
		if _, err := conn.ExecContext(ctx, `
INSERT INTO fragments (
  id, source, source_type, source_id, title, content, content_hash, created_at,
  ingested_at, status, metadata_json, ingest_name, canonical_path,
  accepted_revision_id, current_revision_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '')`,
			fragment.ID, fragment.Source, fragment.SourceType, fragment.SourceID,
			fragment.Title, fragment.Content, storageHash,
			fragment.CreatedAt.Format(time.RFC3339Nano), fragment.IngestedAt.Format(time.RFC3339Nano),
			string(fragment.Status), fragment.MetadataJSON, fragment.IngestName, fragment.CanonicalPath,
		); err != nil {
			return domain.Fragment{}, "", fmt.Errorf("insert stable fragment: %w", err)
		}
		if err := insertSourceIdentity(ctx, conn, resolvedID, identity, fragment.CreatedAt); err != nil {
			return domain.Fragment{}, "", err
		}
		inserted = true
	} else if err != nil {
		return domain.Fragment{}, "", fmt.Errorf("resolve stable fragment identity: %w", err)
	} else if err := fillSourceIdentityProvenance(ctx, conn, resolvedID, identity, fragment.IngestedAt); err != nil {
		return domain.Fragment{}, "", err
	}

	revisionID, err := findRevisionByDigest(ctx, conn, resolvedID, fragment.Revision.MaterialDigest)
	if err == nil {
		var currentRevisionID string
		if err := conn.QueryRowContext(ctx, `SELECT current_revision_id FROM fragments WHERE id = ?`, resolvedID).Scan(&currentRevisionID); err != nil {
			return domain.Fragment{}, "", fmt.Errorf("read current fragment revision: %w", err)
		}
		outcome := UpsertSkipped
		if currentRevisionID != revisionID {
			if err := updateCurrentFragment(ctx, conn, resolvedID, revisionID, fragment); err != nil {
				return domain.Fragment{}, "", err
			}
			outcome = UpsertUpdated
		}
		resolved, err := getFragmentByID(ctx, conn, resolvedID)
		if err != nil {
			return domain.Fragment{}, "", err
		}
		return resolved, outcome, nil
	}
	if err != sql.ErrNoRows {
		return domain.Fragment{}, "", fmt.Errorf("resolve fragment revision: %w", err)
	}

	var ordinal int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), 0) + 1 FROM fragment_revisions WHERE fragment_id = ?`, resolvedID).Scan(&ordinal); err != nil {
		return domain.Fragment{}, "", fmt.Errorf("allocate fragment revision ordinal: %w", err)
	}
	revisionID = domain.DigestText(resolvedID + "\n" + fragment.Revision.MaterialDigest)
	if _, err := conn.ExecContext(ctx, `
INSERT INTO fragment_revisions (
  id, fragment_id, ordinal, material_digest, content_digest, title,
  description, content, content_format, ordered_media_digest, metadata_json,
  normalizer_adapter, normalizer_version, observed_at, committed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revisionID, resolvedID, ordinal, fragment.Revision.MaterialDigest,
		fragment.Revision.ContentDigest, fragment.Revision.Title,
		fragment.Revision.Description, fragment.Revision.Content,
		fragment.Revision.ContentFormat, fragment.Revision.OrderedMediaDigest,
		fragment.Revision.MetadataJSON, fragment.Revision.Normalizer.Adapter,
		fragment.Revision.Normalizer.Version,
		fragment.Revision.ObservedAt.Format(time.RFC3339Nano),
		fragment.Revision.CommittedAt.Format(time.RFC3339Nano),
	); err != nil {
		return domain.Fragment{}, "", fmt.Errorf("insert immutable fragment revision: %w", err)
	}
	if err := updateCurrentFragment(ctx, conn, resolvedID, revisionID, fragment); err != nil {
		return domain.Fragment{}, "", err
	}
	resolved, err := getFragmentByID(ctx, conn, resolvedID)
	if err != nil {
		return domain.Fragment{}, "", err
	}
	if inserted {
		return resolved, UpsertInserted, nil
	}
	return resolved, UpsertUpdated, nil
}

type fragmentWriteConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func findFragmentByIdentity(ctx context.Context, tx fragmentWriteConn, identity domain.SourceIdentity) (string, error) {
	var fragmentID string
	err := tx.QueryRowContext(ctx, `
SELECT fragment_id
FROM fragment_source_identities
WHERE source_registration_id = ? AND source_item_key = ? AND segment_key = ?`,
		identity.SourceRegistrationID, identity.SourceItemKey, identity.SegmentKey,
	).Scan(&fragmentID)
	return fragmentID, err
}

func findRevisionByDigest(ctx context.Context, tx fragmentWriteConn, fragmentID, materialDigest string) (string, error) {
	var revisionID string
	err := tx.QueryRowContext(ctx, `
SELECT id FROM fragment_revisions WHERE fragment_id = ? AND material_digest = ?`,
		fragmentID, materialDigest,
	).Scan(&revisionID)
	return revisionID, err
}

func insertSourceIdentity(ctx context.Context, tx fragmentWriteConn, fragmentID string, identity domain.SourceIdentity, createdAt time.Time) error {
	stamp := createdAt.UTC().Format(time.RFC3339Nano)
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
		stamp, stamp,
	)
	if err != nil {
		return fmt.Errorf("insert fragment source identity: %w", err)
	}
	return nil
}

func fillSourceIdentityProvenance(ctx context.Context, tx fragmentWriteConn, fragmentID string, identity domain.SourceIdentity, observedAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
UPDATE fragment_source_identities
SET provider_item_id = CASE WHEN provider_item_id = '' THEN ? ELSE provider_item_id END,
    source_locator = CASE WHEN source_locator = '' THEN ? ELSE source_locator END,
    submitted_url = CASE WHEN submitted_url = '' THEN ? ELSE submitted_url END,
    canonical_url = CASE WHEN canonical_url = '' THEN ? ELSE canonical_url END,
    updated_at = ?
WHERE fragment_id = ?`,
		identity.ProviderItemID, identity.SourceLocator, identity.SubmittedURL,
		identity.CanonicalURL, observedAt.UTC().Format(time.RFC3339Nano), fragmentID,
	)
	if err != nil {
		return fmt.Errorf("update fragment source provenance: %w", err)
	}
	return nil
}

// The original fragments table still carries its historical three-column
// uniqueness constraint. Revisions are authoritative for content digests; this
// compatibility value is salted only when two source registrations ingest the
// same legacy tuple, avoiding a destructive table rebuild.
func availableLegacyContentHash(ctx context.Context, tx fragmentWriteConn, fragment domain.Fragment, targetID string) (string, error) {
	storageHash := fragment.ContentHash
	var existingID string
	err := tx.QueryRowContext(ctx, `
SELECT id FROM fragments WHERE source = ? AND source_id = ? AND content_hash = ?`,
		fragment.Source, fragment.SourceID, storageHash,
	).Scan(&existingID)
	if err == sql.ErrNoRows || existingID == targetID {
		return storageHash, nil
	}
	if err != nil {
		return "", fmt.Errorf("check legacy fragment uniqueness: %w", err)
	}
	return domain.DigestText(storageHash + "\n" + targetID), nil
}

func updateCurrentFragment(ctx context.Context, tx fragmentWriteConn, fragmentID, revisionID string, fragment domain.Fragment) error {
	storageHash, err := availableLegacyContentHash(ctx, tx, fragment, fragmentID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE fragments
SET source = ?, source_type = ?, source_id = ?, title = ?, content = ?,
    content_hash = ?, ingested_at = ?, metadata_json = ?, ingest_name = ?,
    canonical_path = ?, accepted_revision_id = ?, current_revision_id = ?
WHERE id = ?`,
		fragment.Source, fragment.SourceType, fragment.SourceID, fragment.Title,
		fragment.Content, storageHash, fragment.IngestedAt.Format(time.RFC3339Nano),
		fragment.MetadataJSON, fragment.IngestName, fragment.CanonicalPath,
		revisionID, revisionID, fragmentID,
	)
	if err != nil {
		return fmt.Errorf("select current fragment revision: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

const fragmentReadColumns = `
  f.id, f.source, f.source_type, f.source_id, f.title, f.content,
  COALESCE(NULLIF(fr.content_digest, ''), f.content_hash),
  f.created_at, f.ingested_at, f.status, f.summary_text, f.indexed_at,
  f.metadata_json, f.ingest_name, f.canonical_path,
  f.accepted_revision_id, f.current_revision_id,
  COALESCE(si.source_registration_id, ''), COALESCE(si.provider, ''),
  COALESCE(si.provider_item_id, ''), COALESCE(si.source_item_key, ''),
  COALESCE(si.source_locator, ''), COALESCE(si.segment_key, ''),
  COALESCE(si.submitted_url, ''), COALESCE(si.canonical_url, ''),
  COALESCE(si.source_adapter, ''), COALESCE(si.source_adapter_version, ''),
  COALESCE(si.canonicalizer_adapter, ''), COALESCE(si.canonicalizer_version, ''),
  COALESCE(fr.id, ''), COALESCE(fr.ordinal, 0),
  COALESCE(fr.material_digest, ''), COALESCE(fr.content_digest, ''),
  COALESCE(fr.title, ''), COALESCE(fr.description, ''),
  COALESCE(fr.content, ''), COALESCE(fr.content_format, ''),
  COALESCE(fr.ordered_media_digest, ''), COALESCE(fr.metadata_json, '{}'),
  COALESCE(fr.normalizer_adapter, ''), COALESCE(fr.normalizer_version, ''),
  COALESCE(fr.observed_at, ''), COALESCE(fr.committed_at, ''),
  COALESCE(fr.legacy_fragment_id, '')`

const fragmentReadJoins = `
LEFT JOIN fragment_source_identities si ON si.fragment_id = f.id
LEFT JOIN fragment_revisions fr ON fr.id = f.current_revision_id`

type fragmentScanState struct {
	createdAt   string
	ingestedAt  string
	indexedAt   string
	status      string
	observedAt  string
	committedAt string
}

func (s *fragmentScanState) destinations(f *domain.Fragment) []any {
	return []any{
		&f.ID, &f.Source, &f.SourceType, &f.SourceID, &f.Title, &f.Content,
		&f.ContentHash, &s.createdAt, &s.ingestedAt, &s.status, &f.Summary, &s.indexedAt,
		&f.MetadataJSON, &f.IngestName, &f.CanonicalPath,
		&f.AcceptedRevisionID, &f.CurrentRevisionID,
		&f.SourceIdentity.SourceRegistrationID, &f.SourceIdentity.Provider,
		&f.SourceIdentity.ProviderItemID, &f.SourceIdentity.SourceItemKey,
		&f.SourceIdentity.SourceLocator, &f.SourceIdentity.SegmentKey,
		&f.SourceIdentity.SubmittedURL, &f.SourceIdentity.CanonicalURL,
		&f.SourceIdentity.SourceAdapter.Adapter, &f.SourceIdentity.SourceAdapter.Version,
		&f.SourceIdentity.Canonicalizer.Adapter, &f.SourceIdentity.Canonicalizer.Version,
		&f.Revision.ID, &f.Revision.Ordinal, &f.Revision.MaterialDigest,
		&f.Revision.ContentDigest, &f.Revision.Title, &f.Revision.Description,
		&f.Revision.Content, &f.Revision.ContentFormat, &f.Revision.OrderedMediaDigest,
		&f.Revision.MetadataJSON, &f.Revision.Normalizer.Adapter,
		&f.Revision.Normalizer.Version, &s.observedAt, &s.committedAt,
		&f.Revision.LegacyFragmentID,
	}
}

func (s *fragmentScanState) finish(f *domain.Fragment) {
	f.CreatedAt, _ = time.Parse(time.RFC3339Nano, s.createdAt)
	if f.CreatedAt.IsZero() {
		f.CreatedAt, _ = time.Parse(time.RFC3339, s.createdAt)
	}
	f.IngestedAt, _ = time.Parse(time.RFC3339Nano, s.ingestedAt)
	if f.IngestedAt.IsZero() {
		f.IngestedAt, _ = time.Parse(time.RFC3339, s.ingestedAt)
	}
	if s.indexedAt != "" {
		f.IndexedAt, _ = time.Parse(time.RFC3339Nano, s.indexedAt)
		if f.IndexedAt.IsZero() {
			f.IndexedAt, _ = time.Parse(time.RFC3339, s.indexedAt)
		}
	}
	f.Status = domain.FragmentStatus(s.status)
	f.Revision.FragmentID = f.ID
	f.Revision.ObservedAt, _ = time.Parse(time.RFC3339Nano, s.observedAt)
	f.Revision.CommittedAt, _ = time.Parse(time.RFC3339Nano, s.committedAt)
	if f.Revision.ObservedAt.IsZero() {
		f.Revision.ObservedAt, _ = time.Parse(time.RFC3339, s.observedAt)
	}
	if f.Revision.CommittedAt.IsZero() {
		f.Revision.CommittedAt, _ = time.Parse(time.RFC3339, s.committedAt)
	}
}

func scanFragment(scanner rowScanner) (domain.Fragment, error) {
	var f domain.Fragment
	var state fragmentScanState
	if err := scanner.Scan(state.destinations(&f)...); err != nil {
		return domain.Fragment{}, err
	}
	state.finish(&f)
	return f, nil
}

func getFragmentByID(ctx context.Context, tx fragmentWriteConn, fragmentID string) (domain.Fragment, error) {
	query := `SELECT ` + fragmentReadColumns + ` FROM fragments f ` + fragmentReadJoins + ` WHERE f.id = ?`
	f, err := scanFragment(tx.QueryRowContext(ctx, query, fragmentID))
	if err != nil {
		return domain.Fragment{}, fmt.Errorf("get resolved fragment: %w", err)
	}
	return f, nil
}

func (r *FragmentRepository) UpdateEditableFields(ctx context.Context, fragmentID, title, summary, metadataJSON string) error {
	canonicalID, err := r.ResolveCanonicalFragmentID(ctx, fragmentID)
	if err != nil {
		return fmt.Errorf("resolve editable fragment: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
UPDATE fragments
SET title = ?,
    summary_text = ?,
    metadata_json = ?,
    indexed_at = ?
WHERE id = ?`,
		title,
		summary,
		metadataJSON,
		time.Now().UTC().Format(time.RFC3339),
		canonicalID,
	)
	if err != nil {
		return fmt.Errorf("update editable fragment fields: %w", err)
	}
	return nil
}

func (r *FragmentRepository) Search(ctx context.Context, query string, limit int) ([]domain.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	readQuery := `SELECT ` + fragmentReadColumns + `,
  (
    SELECT fa.attachment_id
    FROM fragment_attachments fa
    JOIN attachments a ON a.id = fa.attachment_id
    WHERE fa.fragment_id = f.id
      AND a.kind = 'image'
    ORDER BY fa.created_at ASC
    LIMIT 1
  ) AS preview_attachment_id,
  bm25(fragments_fts) AS rank,
  snippet(fragments_fts, 2, '[', ']', ' … ', 18) AS snippet
FROM fragments_fts
JOIN fragments f ON f.id = fragments_fts.fragment_id
` + fragmentReadJoins + `
WHERE fragments_fts MATCH ?
  AND NOT EXISTS (
    SELECT 1 FROM fragment_identity_aliases fia WHERE fia.alias_fragment_id = f.id
  )
ORDER BY rank
LIMIT ?`
	rows, err := r.db.QueryContext(ctx, readQuery, query, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	results := make([]domain.SearchResult, 0, limit)
	for rows.Next() {
		var (
			f                   domain.Fragment
			state               fragmentScanState
			previewAttachmentID sql.NullString
			rank                float64
			snippet             string
		)
		destinations := state.destinations(&f)
		destinations = append(destinations, &previewAttachmentID, &rank, &snippet)
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		state.finish(&f)
		item := domain.SearchResult{
			Fragment: f,
			Score:    -rank,
			Snippet:  snippet,
			Trace: domain.RecallTrace{
				Backend:  "sqlite",
				Strategy: "fts",
				Reason:   "sqlite_fts_match",
			},
		}
		if previewAttachmentID.Valid {
			item.PreviewAttachmentID = previewAttachmentID.String
		}
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search results: %w", err)
	}
	return results, nil
}

func (r *FragmentRepository) RecordIngestRun(ctx context.Context, run domain.IngestRun) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO ingest_runs (
  ingest_name, ingest_kind, started_at, finished_at, inserted_count, updated_count, skipped_count
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.Name,
		run.Kind,
		run.StartedAt.Format(time.RFC3339),
		run.FinishedAt.Format(time.RFC3339),
		run.Inserted,
		run.Updated,
		run.Skipped,
	)
	if err != nil {
		return fmt.Errorf("record ingest run: %w", err)
	}
	return nil
}

// CreateIngestRun inserts a queued ingest run row and returns its id. The
// async worker later marks it running and completes/fails it.
func (r *FragmentRepository) CreateIngestRun(ctx context.Context, name, kind string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
INSERT INTO ingest_runs (
  ingest_name, ingest_kind, started_at, finished_at, status, error
) VALUES (?, ?, '', '', 'queued', '')`, name, kind)
	if err != nil {
		return 0, fmt.Errorf("create ingest run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create ingest run id: %w", err)
	}
	return id, nil
}

// MarkIngestRunRunning flips a queued run to running and stamps started_at.
func (r *FragmentRepository) MarkIngestRunRunning(ctx context.Context, id int64, startedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE ingest_runs SET status = 'running', started_at = ? WHERE id = ?`,
		startedAt.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("mark ingest run running: %w", err)
	}
	return nil
}

// CompleteIngestRun records a successful run's counts and finish time.
func (r *FragmentRepository) CompleteIngestRun(ctx context.Context, id int64, run domain.IngestRun, finishedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE ingest_runs
SET status = 'done', finished_at = ?, inserted_count = ?, updated_count = ?, skipped_count = ?, error = ''
WHERE id = ?`,
		finishedAt.Format(time.RFC3339), run.Inserted, run.Updated, run.Skipped, id)
	if err != nil {
		return fmt.Errorf("complete ingest run: %w", err)
	}
	return nil
}

// FailIngestRun marks a run failed with an error message.
func (r *FragmentRepository) FailIngestRun(ctx context.Context, id int64, finishedAt time.Time, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE ingest_runs SET status = 'failed', finished_at = ?, error = ? WHERE id = ?`,
		finishedAt.Format(time.RFC3339), errMsg, id)
	if err != nil {
		return fmt.Errorf("fail ingest run: %w", err)
	}
	return nil
}

// ListIngestRuns returns recent ingest runs, newest first.
func (r *FragmentRepository) ListIngestRuns(ctx context.Context, limit int) ([]domain.IngestRunRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, ingest_name, ingest_kind, status, started_at, finished_at,
       inserted_count, updated_count, skipped_count, error
FROM ingest_runs
ORDER BY id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list ingest runs: %w", err)
	}
	defer rows.Close()
	out := make([]domain.IngestRunRecord, 0, limit)
	for rows.Next() {
		var rec domain.IngestRunRecord
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Kind, &rec.Status,
			&rec.StartedAt, &rec.FinishedAt, &rec.Inserted, &rec.Updated, &rec.Skipped, &rec.Error); err != nil {
			return nil, fmt.Errorf("scan ingest run: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *FragmentRepository) UpdateStatus(ctx context.Context, fragmentID string, status domain.FragmentStatus) error {
	canonicalID, err := r.ResolveCanonicalFragmentID(ctx, fragmentID)
	if err != nil {
		return fmt.Errorf("resolve fragment status target: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
UPDATE fragments
SET status = ?
WHERE id = ?`,
		string(status),
		canonicalID,
	)
	if err != nil {
		return fmt.Errorf("update fragment status: %w", err)
	}
	return nil
}

func (r *FragmentRepository) UpdateIndexMetadata(ctx context.Context, fragmentID, summary string, indexedAt time.Time) error {
	canonicalID, err := r.ResolveCanonicalFragmentID(ctx, fragmentID)
	if err != nil {
		return fmt.Errorf("resolve fragment index target: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
UPDATE fragments
SET summary_text = ?, indexed_at = ?
WHERE id = ?`,
		summary,
		indexedAt.Format(time.RFC3339),
		canonicalID,
	)
	if err != nil {
		return fmt.Errorf("update index metadata: %w", err)
	}
	return nil
}

func (r *FragmentRepository) UpdateDerivedFields(ctx context.Context, fragmentID, title, sourceType, metadataJSON, canonicalPath string) error {
	canonicalID, err := r.ResolveCanonicalFragmentID(ctx, fragmentID)
	if err != nil {
		return fmt.Errorf("resolve derived fragment target: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
UPDATE fragments
SET title = ?, source_type = ?, metadata_json = ?, canonical_path = ?
WHERE id = ?`,
		title,
		sourceType,
		metadataJSON,
		canonicalPath,
		canonicalID,
	)
	if err != nil {
		return fmt.Errorf("update fragment derived fields: %w", err)
	}
	return nil
}

func (r *FragmentRepository) GetByID(ctx context.Context, fragmentID string) (domain.Fragment, error) {
	canonicalID, err := r.ResolveCanonicalFragmentID(ctx, fragmentID)
	if err != nil {
		return domain.Fragment{}, fmt.Errorf("get fragment: %w", err)
	}
	query := `SELECT ` + fragmentReadColumns + ` FROM fragments f ` + fragmentReadJoins + ` WHERE f.id = ?`
	f, err := scanFragment(r.db.QueryRowContext(ctx, query, canonicalID))
	if err != nil {
		return domain.Fragment{}, fmt.Errorf("get fragment: %w", err)
	}
	return f, nil
}

// ResolveCanonicalFragmentID keeps legacy IDs usable without exposing retained
// content-hash rows as additional active fragments.
func (r *FragmentRepository) ResolveCanonicalFragmentID(ctx context.Context, fragmentID string) (string, error) {
	var canonicalID sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE((
  SELECT fragment_id FROM fragment_identity_aliases WHERE alias_fragment_id = ?
), (
  SELECT id FROM fragments WHERE id = ?
))`, fragmentID, fragmentID).Scan(&canonicalID)
	if err != nil {
		return "", err
	}
	if !canonicalID.Valid || canonicalID.String == "" {
		return "", sql.ErrNoRows
	}
	return canonicalID.String, nil
}

func (r *FragmentRepository) GetRevision(ctx context.Context, revisionID string) (domain.FragmentRevision, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, fragment_id, ordinal, material_digest, content_digest, title,
       description, content, content_format, ordered_media_digest, metadata_json,
       normalizer_adapter, normalizer_version, observed_at, committed_at,
       COALESCE(legacy_fragment_id, '')
FROM fragment_revisions
WHERE id = ?`, revisionID)
	revision, err := scanRevision(row)
	if err != nil {
		return domain.FragmentRevision{}, fmt.Errorf("get fragment revision: %w", err)
	}
	return revision, nil
}

func (r *FragmentRepository) ListRevisions(ctx context.Context, fragmentID string) ([]domain.FragmentRevision, error) {
	canonicalID, err := r.ResolveCanonicalFragmentID(ctx, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("resolve fragment revision owner: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, fragment_id, ordinal, material_digest, content_digest, title,
       description, content, content_format, ordered_media_digest, metadata_json,
       normalizer_adapter, normalizer_version, observed_at, committed_at,
       COALESCE(legacy_fragment_id, '')
FROM fragment_revisions
WHERE fragment_id = ?
ORDER BY ordinal`, canonicalID)
	if err != nil {
		return nil, fmt.Errorf("list fragment revisions: %w", err)
	}
	defer rows.Close()
	var out []domain.FragmentRevision
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan fragment revision: %w", err)
		}
		out = append(out, revision)
	}
	return out, rows.Err()
}

func scanRevision(scanner rowScanner) (domain.FragmentRevision, error) {
	var revision domain.FragmentRevision
	var observedAt, committedAt string
	err := scanner.Scan(
		&revision.ID, &revision.FragmentID, &revision.Ordinal,
		&revision.MaterialDigest, &revision.ContentDigest, &revision.Title,
		&revision.Description, &revision.Content, &revision.ContentFormat,
		&revision.OrderedMediaDigest, &revision.MetadataJSON,
		&revision.Normalizer.Adapter, &revision.Normalizer.Version,
		&observedAt, &committedAt, &revision.LegacyFragmentID,
	)
	if err != nil {
		return domain.FragmentRevision{}, err
	}
	revision.ObservedAt, _ = time.Parse(time.RFC3339Nano, observedAt)
	if revision.ObservedAt.IsZero() {
		revision.ObservedAt, _ = time.Parse(time.RFC3339, observedAt)
	}
	revision.CommittedAt, _ = time.Parse(time.RFC3339Nano, committedAt)
	if revision.CommittedAt.IsZero() {
		revision.CommittedAt, _ = time.Parse(time.RFC3339, committedAt)
	}
	return revision, nil
}

// ListOptions filters and paginates FragmentRepository.List.
type ListOptions struct {
	Status     domain.FragmentStatus // optional; empty = all statuses
	Source     string                // optional; empty = all sources
	SourceType string                // optional; empty = all source types
	Limit      int                   // defaults to 50, capped at 200
	Offset     int
}

// List returns fragments newest-first, optionally filtered by status, plus the
// total count of the (status-filtered) set so callers can paginate.
func (r *FragmentRepository) List(ctx context.Context, opts ListOptions) ([]domain.Fragment, int, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	var (
		clauses []string
		args    []any
		where   string
	)
	if opts.Status != "" {
		clauses = append(clauses, "f.status = ?")
		args = append(args, opts.Status)
	}
	if strings.TrimSpace(opts.Source) != "" {
		clauses = append(clauses, "f.source = ?")
		args = append(args, strings.TrimSpace(opts.Source))
	}
	if strings.TrimSpace(opts.SourceType) != "" {
		clauses = append(clauses, "f.source_type = ?")
		args = append(args, strings.TrimSpace(opts.SourceType))
	}
	clauses = append(clauses, `NOT EXISTS (
  SELECT 1 FROM fragment_identity_aliases fia WHERE fia.alias_fragment_id = f.id
)`)
	where = " WHERE " + strings.Join(clauses, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fragments f`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count fragments: %w", err)
	}

	listArgs := append(append([]any{}, args...), limit, offset)
	query := `SELECT ` + fragmentReadColumns + `
FROM fragments f ` + fragmentReadJoins + where + `
ORDER BY f.created_at DESC, f.id DESC
LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list fragments: %w", err)
	}
	defer rows.Close()
	items, err := scanFragments(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *FragmentRepository) ListBrowse(ctx context.Context, opts ListOptions) ([]domain.FragmentBrowseItem, int, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	var (
		clauses []string
		args    []any
		where   string
	)
	if opts.Status != "" {
		clauses = append(clauses, "f.status = ?")
		args = append(args, opts.Status)
	}
	if strings.TrimSpace(opts.Source) != "" {
		clauses = append(clauses, "f.source = ?")
		args = append(args, strings.TrimSpace(opts.Source))
	}
	if strings.TrimSpace(opts.SourceType) != "" {
		clauses = append(clauses, "f.source_type = ?")
		args = append(args, strings.TrimSpace(opts.SourceType))
	}
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	if where == "" {
		where = " WHERE "
	} else {
		where += " AND "
	}
	where += `NOT EXISTS (
  SELECT 1 FROM fragment_identity_aliases fia WHERE fia.alias_fragment_id = f.id
)`

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fragments f`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count browse fragments: %w", err)
	}

	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.QueryContext(ctx, `
SELECT
  f.id,
  f.title,
  f.source,
  f.source_type,
  f.status,
  f.summary_text,
  f.canonical_path,
  f.source_id,
  f.created_at,
  COALESCE(NULLIF(f.indexed_at, ''), f.ingested_at, f.created_at) AS modified_at,
  COALESCE((
    SELECT fa.attachment_id
    FROM fragment_attachments fa
    JOIN attachments a ON a.id = fa.attachment_id
    WHERE fa.fragment_id = f.id
      AND a.kind = 'image'
    ORDER BY fa.created_at ASC
    LIMIT 1
  ), '') AS preview_attachment_id,
  COALESCE((
    SELECT GROUP_CONCAT(e.value, char(31))
    FROM fragment_entities fe
    JOIN entities e ON e.id = fe.entity_id
    WHERE fe.fragment_id = f.id
      AND e.kind = 'tag'
  ), '') AS tags,
  EXISTS(
    SELECT 1
    FROM inbox i
    WHERE i.fragment_id = f.id
      AND LOWER(i.reason) LIKE '%materialized%'
  ) AS materialized
FROM fragments f`+where+`
ORDER BY modified_at DESC, f.id DESC
LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list browse fragments: %w", err)
	}
	defer rows.Close()

	items := make([]domain.FragmentBrowseItem, 0, limit)
	for rows.Next() {
		var (
			item              domain.FragmentBrowseItem
			createdAt         string
			modifiedAt        string
			previewAttachment string
			tagsRaw           string
			materializedInt   int
		)
		if err := rows.Scan(
			&item.FragmentID,
			&item.Title,
			&item.Source,
			&item.SourceType,
			&item.Status,
			&item.Summary,
			&item.CanonicalPath,
			&item.SourceID,
			&createdAt,
			&modifiedAt,
			&previewAttachment,
			&tagsRaw,
			&materializedInt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan browse fragment: %w", err)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		item.ModifiedAt, _ = time.Parse(time.RFC3339, modifiedAt)
		item.PreviewAttachmentID = strings.TrimSpace(previewAttachment)
		if strings.TrimSpace(tagsRaw) != "" {
			item.Tags = strings.Split(tagsRaw, string(rune(31)))
		}
		item.Materialized = materializedInt != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate browse fragments: %w", err)
	}
	return items, total, nil
}

func (r *FragmentRepository) FindRelationCandidates(ctx context.Context, fragment domain.Fragment, limit int) ([]domain.Fragment, error) {
	if limit <= 0 {
		limit = 5
	}
	query := `SELECT ` + fragmentReadColumns + `
FROM fragments f ` + fragmentReadJoins + `
WHERE f.id <> ? AND f.source = ? AND f.source_type = ?
  AND NOT EXISTS (
    SELECT 1 FROM fragment_identity_aliases fia WHERE fia.alias_fragment_id = f.id
  )
	ORDER BY f.created_at DESC
LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, fragment.ID, fragment.Source, fragment.SourceType, limit)
	if err != nil {
		return nil, fmt.Errorf("find relation candidates: %w", err)
	}
	defer rows.Close()
	return scanFragments(rows)
}

func (r *FragmentRepository) UpsertRelation(ctx context.Context, relation domain.FragmentRelation) error {
	if relation.MetadataJSON == "" {
		relation.MetadataJSON = "{}"
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO fragment_links (
  fragment_id, related_fragment_id, kind, score, metadata_json, created_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(fragment_id, related_fragment_id, kind) DO UPDATE SET
  score = excluded.score,
  metadata_json = excluded.metadata_json,
  created_at = excluded.created_at`,
		relation.FragmentID,
		relation.RelatedFragmentID,
		relation.Kind,
		relation.Score,
		relation.MetadataJSON,
		relation.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upsert relation: %w", err)
	}
	return nil
}

func (r *FragmentRepository) ListRelated(ctx context.Context, fragmentID string, limit int) ([]domain.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	query := `SELECT ` + fragmentReadColumns + `,
  (
    SELECT fa.attachment_id
    FROM fragment_attachments fa
    JOIN attachments a ON a.id = fa.attachment_id
    WHERE fa.fragment_id = f.id
      AND a.kind = 'image'
    ORDER BY fa.created_at ASC
    LIMIT 1
  ) AS preview_attachment_id,
  l.kind, l.score, l.metadata_json
FROM fragment_links l
JOIN fragments f ON f.id = l.related_fragment_id
` + fragmentReadJoins + `
WHERE l.fragment_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM fragment_identity_aliases fia WHERE fia.alias_fragment_id = f.id
  )
ORDER BY l.score DESC, f.created_at DESC
LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, fragmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list related: %w", err)
	}
	defer rows.Close()

	results := make([]domain.SearchResult, 0, limit)
	for rows.Next() {
		var (
			f                   domain.Fragment
			state               fragmentScanState
			previewAttachmentID sql.NullString
			kind                string
			score               float64
			relationMeta        string
		)
		destinations := state.destinations(&f)
		destinations = append(destinations, &previewAttachmentID, &kind, &score, &relationMeta)
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan related fragment: %w", err)
		}
		state.finish(&f)
		item := domain.SearchResult{
			Fragment: f,
			Score:    score,
			Snippet:  f.Summary,
			Trace: domain.RecallTrace{
				Backend:      "sqlite",
				Strategy:     "fragment_link",
				RelationKind: kind,
				Reason:       "deterministic_relation_match",
				MetadataJSON: relationMeta,
			},
		}
		if previewAttachmentID.Valid {
			item.PreviewAttachmentID = previewAttachmentID.String
		}
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate related fragments: %w", err)
	}
	return results, nil
}

func (r *FragmentRepository) ListRelations(ctx context.Context, fragmentID string, limit int) ([]domain.FragmentRelationDetail, error) {
	if limit <= 0 {
		limit = 10
	}
	query := `SELECT
  l.fragment_id, l.related_fragment_id, l.kind, l.score, l.metadata_json, l.created_at,
` + fragmentReadColumns + `
FROM fragment_links l
JOIN fragments f ON f.id = l.related_fragment_id
` + fragmentReadJoins + `
WHERE l.fragment_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM fragment_identity_aliases fia WHERE fia.alias_fragment_id = f.id
  )
ORDER BY l.score DESC, l.created_at DESC
LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, fragmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	defer rows.Close()

	items := make([]domain.FragmentRelationDetail, 0, limit)
	for rows.Next() {
		var (
			item              domain.FragmentRelationDetail
			related           domain.Fragment
			state             fragmentScanState
			relationCreatedAt string
		)
		destinations := []any{
			&item.Relation.FragmentID,
			&item.Relation.RelatedFragmentID,
			&item.Relation.Kind,
			&item.Relation.Score,
			&item.Relation.MetadataJSON,
			&relationCreatedAt,
		}
		destinations = append(destinations, state.destinations(&related)...)
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan relation detail: %w", err)
		}
		item.Relation.CreatedAt, _ = time.Parse(time.RFC3339, relationCreatedAt)
		state.finish(&related)
		item.Related = related
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relations: %w", err)
	}
	return items, nil
}

func scanFragments(rows *sql.Rows) ([]domain.Fragment, error) {
	var out []domain.Fragment
	for rows.Next() {
		f, err := scanFragment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan fragment: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fragments: %w", err)
	}
	return out, nil
}
