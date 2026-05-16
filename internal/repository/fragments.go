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
	contentHash := hashText(in.Content)
	identity := hashText(in.Source + "\n" + in.SourceID + "\n" + contentHash)
	metaJSON := "{}"
	if len(in.Metadata) > 0 {
		raw, err := json.Marshal(in.Metadata)
		if err != nil {
			return domain.Fragment{}, fmt.Errorf("build fragment metadata: %w", err)
		}
		metaJSON = string(raw)
	}

	return domain.Fragment{
		ID:            identity,
		Source:        in.Source,
		SourceType:    in.SourceType,
		SourceID:      in.SourceID,
		Title:         in.Title,
		Content:       in.Content,
		ContentHash:   contentHash,
		CreatedAt:     in.CreatedAt.UTC(),
		IngestedAt:    now.UTC(),
		Status:        domain.FragmentStatusInbox,
		MetadataJSON:  metaJSON,
		IngestName:    ingestName,
		CanonicalPath: in.CanonicalPath,
	}, nil
}

func (r *FragmentRepository) Upsert(ctx context.Context, fragment domain.Fragment) (UpsertOutcome, error) {
	var existingID string
	err := r.db.QueryRowContext(
		ctx,
		`SELECT id FROM fragments WHERE source = ? AND source_id = ? AND content_hash = ?`,
		fragment.Source,
		fragment.SourceID,
		fragment.ContentHash,
	).Scan(&existingID)
	switch {
	case err == nil && existingID == fragment.ID:
		return UpsertSkipped, nil
	case err != nil && err != sql.ErrNoRows:
		return "", fmt.Errorf("lookup existing fragment: %w", err)
	}

	res, err := r.db.ExecContext(ctx, `
INSERT INTO fragments (
  id, source, source_type, source_id, title, content, content_hash, created_at,
  ingested_at, status, metadata_json, ingest_name, canonical_path
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source, source_id, content_hash) DO UPDATE SET
  title = excluded.title,
  content = excluded.content,
  ingested_at = excluded.ingested_at,
  metadata_json = excluded.metadata_json,
  ingest_name = excluded.ingest_name,
  canonical_path = excluded.canonical_path`,
		fragment.ID,
		fragment.Source,
		fragment.SourceType,
		fragment.SourceID,
		fragment.Title,
		fragment.Content,
		fragment.ContentHash,
		fragment.CreatedAt.Format(time.RFC3339),
		fragment.IngestedAt.Format(time.RFC3339),
		string(fragment.Status),
		fragment.MetadataJSON,
		fragment.IngestName,
		fragment.CanonicalPath,
	)
	if err != nil {
		return "", fmt.Errorf("upsert fragment: %w", err)
	}
	if _, err := res.RowsAffected(); err != nil {
		return "", nil
	}
	if existingID != "" {
		return UpsertUpdated, nil
	}
	return UpsertInserted, nil
}

func (r *FragmentRepository) Search(ctx context.Context, query string, limit int) ([]domain.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT
  f.id, f.source, f.source_type, f.source_id, f.title, f.content, f.content_hash,
  f.created_at, f.ingested_at, f.status, f.summary_text, f.indexed_at, f.metadata_json, f.ingest_name, f.canonical_path,
  bm25(fragments_fts) AS rank,
  snippet(fragments_fts, 2, '[', ']', ' … ', 18) AS snippet
FROM fragments_fts
JOIN fragments f ON f.id = fragments_fts.fragment_id
WHERE fragments_fts MATCH ?
ORDER BY rank
LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	results := make([]domain.SearchResult, 0, limit)
	for rows.Next() {
		var (
			f                   domain.Fragment
			createdAt, ingested string
			indexedAt           string
			status              string
			rank                float64
			snippet             string
		)
		if err := rows.Scan(
			&f.ID,
			&f.Source,
			&f.SourceType,
			&f.SourceID,
			&f.Title,
			&f.Content,
			&f.ContentHash,
			&createdAt,
			&ingested,
			&status,
			&f.Summary,
			&indexedAt,
			&f.MetadataJSON,
			&f.IngestName,
			&f.CanonicalPath,
			&rank,
			&snippet,
		); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.IngestedAt, _ = time.Parse(time.RFC3339, ingested)
		if indexedAt != "" {
			f.IndexedAt, _ = time.Parse(time.RFC3339, indexedAt)
		}
		f.Status = domain.FragmentStatus(status)
		results = append(results, domain.SearchResult{
			Fragment: f,
			Score:    -rank,
			Snippet:  snippet,
			Trace: domain.RecallTrace{
				Backend:  "sqlite",
				Strategy: "fts",
				Reason:   "sqlite_fts_match",
			},
		})
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
	_, err := r.db.ExecContext(ctx, `
UPDATE fragments
SET status = ?
WHERE id = ?`,
		string(status),
		fragmentID,
	)
	if err != nil {
		return fmt.Errorf("update fragment status: %w", err)
	}
	return nil
}

func (r *FragmentRepository) UpdateIndexMetadata(ctx context.Context, fragmentID, summary string, indexedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE fragments
SET summary_text = ?, indexed_at = ?
WHERE id = ?`,
		summary,
		indexedAt.Format(time.RFC3339),
		fragmentID,
	)
	if err != nil {
		return fmt.Errorf("update index metadata: %w", err)
	}
	return nil
}

func (r *FragmentRepository) GetByID(ctx context.Context, fragmentID string) (domain.Fragment, error) {
	var f domain.Fragment
	var createdAt, ingestedAt, indexedAt, status string
	err := r.db.QueryRowContext(ctx, `
SELECT
  id, source, source_type, source_id, title, content, content_hash, created_at,
  ingested_at, status, summary_text, indexed_at, metadata_json, ingest_name, canonical_path
FROM fragments
WHERE id = ?`, fragmentID).Scan(
		&f.ID,
		&f.Source,
		&f.SourceType,
		&f.SourceID,
		&f.Title,
		&f.Content,
		&f.ContentHash,
		&createdAt,
		&ingestedAt,
		&status,
		&f.Summary,
		&indexedAt,
		&f.MetadataJSON,
		&f.IngestName,
		&f.CanonicalPath,
	)
	if err != nil {
		return domain.Fragment{}, fmt.Errorf("get fragment: %w", err)
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.IngestedAt, _ = time.Parse(time.RFC3339, ingestedAt)
	if indexedAt != "" {
		f.IndexedAt, _ = time.Parse(time.RFC3339, indexedAt)
	}
	f.Status = domain.FragmentStatus(status)
	return f, nil
}

// ListOptions filters and paginates FragmentRepository.List.
type ListOptions struct {
	Status domain.FragmentStatus // optional; empty = all statuses
	Limit  int                   // defaults to 50, capped at 200
	Offset int
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
		where string
		args  []any
	)
	if opts.Status != "" {
		where = " WHERE status = ?"
		args = append(args, opts.Status)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fragments`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count fragments: %w", err)
	}

	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.QueryContext(ctx, `
SELECT
  id, source, source_type, source_id, title, content, content_hash, created_at,
  ingested_at, status, summary_text, indexed_at, metadata_json, ingest_name, canonical_path
FROM fragments`+where+`
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?`, listArgs...)
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

func (r *FragmentRepository) FindRelationCandidates(ctx context.Context, fragment domain.Fragment, limit int) ([]domain.Fragment, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT
  id, source, source_type, source_id, title, content, content_hash, created_at,
  ingested_at, status, summary_text, indexed_at, metadata_json, ingest_name, canonical_path
FROM fragments
WHERE id <> ? AND source = ? AND source_type = ?
ORDER BY created_at DESC
LIMIT ?`, fragment.ID, fragment.Source, fragment.SourceType, limit)
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
	rows, err := r.db.QueryContext(ctx, `
SELECT
  f.id, f.source, f.source_type, f.source_id, f.title, f.content, f.content_hash,
  f.created_at, f.ingested_at, f.status, f.summary_text, f.indexed_at, f.metadata_json, f.ingest_name, f.canonical_path,
  l.kind, l.score, l.metadata_json
FROM fragment_links l
JOIN fragments f ON f.id = l.related_fragment_id
WHERE l.fragment_id = ?
ORDER BY l.score DESC, f.created_at DESC
LIMIT ?`, fragmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list related: %w", err)
	}
	defer rows.Close()

	results := make([]domain.SearchResult, 0, limit)
	for rows.Next() {
		var (
			f                   domain.Fragment
			createdAt, ingested string
			indexedAt           string
			status              string
			kind                string
			score               float64
			relationMeta        string
		)
		if err := rows.Scan(
			&f.ID, &f.Source, &f.SourceType, &f.SourceID, &f.Title, &f.Content, &f.ContentHash,
			&createdAt, &ingested, &status, &f.Summary, &indexedAt, &f.MetadataJSON, &f.IngestName, &f.CanonicalPath,
			&kind, &score, &relationMeta,
		); err != nil {
			return nil, fmt.Errorf("scan related fragment: %w", err)
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.IngestedAt, _ = time.Parse(time.RFC3339, ingested)
		if indexedAt != "" {
			f.IndexedAt, _ = time.Parse(time.RFC3339, indexedAt)
		}
		f.Status = domain.FragmentStatus(status)
		results = append(results, domain.SearchResult{
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
		})
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
	rows, err := r.db.QueryContext(ctx, `
SELECT
  l.fragment_id, l.related_fragment_id, l.kind, l.score, l.metadata_json, l.created_at,
  f.id, f.source, f.source_type, f.source_id, f.title, f.content, f.content_hash,
  f.created_at, f.ingested_at, f.status, f.summary_text, f.indexed_at, f.metadata_json, f.ingest_name, f.canonical_path
FROM fragment_links l
JOIN fragments f ON f.id = l.related_fragment_id
WHERE l.fragment_id = ?
ORDER BY l.score DESC, l.created_at DESC
LIMIT ?`, fragmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	defer rows.Close()

	items := make([]domain.FragmentRelationDetail, 0, limit)
	for rows.Next() {
		var (
			item                         domain.FragmentRelationDetail
			related                      domain.Fragment
			relationCreatedAt            string
			relatedCreatedAt, ingestedAt string
			indexedAt, status            string
		)
		if err := rows.Scan(
			&item.Relation.FragmentID,
			&item.Relation.RelatedFragmentID,
			&item.Relation.Kind,
			&item.Relation.Score,
			&item.Relation.MetadataJSON,
			&relationCreatedAt,
			&related.ID,
			&related.Source,
			&related.SourceType,
			&related.SourceID,
			&related.Title,
			&related.Content,
			&related.ContentHash,
			&relatedCreatedAt,
			&ingestedAt,
			&status,
			&related.Summary,
			&indexedAt,
			&related.MetadataJSON,
			&related.IngestName,
			&related.CanonicalPath,
		); err != nil {
			return nil, fmt.Errorf("scan relation detail: %w", err)
		}
		item.Relation.CreatedAt, _ = time.Parse(time.RFC3339, relationCreatedAt)
		related.CreatedAt, _ = time.Parse(time.RFC3339, relatedCreatedAt)
		related.IngestedAt, _ = time.Parse(time.RFC3339, ingestedAt)
		if indexedAt != "" {
			related.IndexedAt, _ = time.Parse(time.RFC3339, indexedAt)
		}
		related.Status = domain.FragmentStatus(status)
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
		var (
			f                   domain.Fragment
			createdAt, ingested string
			indexedAt           string
			status              string
		)
		if err := rows.Scan(
			&f.ID, &f.Source, &f.SourceType, &f.SourceID, &f.Title, &f.Content, &f.ContentHash,
			&createdAt, &ingested, &status, &f.Summary, &indexedAt, &f.MetadataJSON, &f.IngestName, &f.CanonicalPath,
		); err != nil {
			return nil, fmt.Errorf("scan fragment: %w", err)
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.IngestedAt, _ = time.Parse(time.RFC3339, ingested)
		if indexedAt != "" {
			f.IndexedAt, _ = time.Parse(time.RFC3339, indexedAt)
		}
		f.Status = domain.FragmentStatus(status)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fragments: %w", err)
	}
	return out, nil
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
