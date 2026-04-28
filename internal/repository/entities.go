package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type EntityRecord struct {
	ID            string
	Kind          string
	Value         string
	FragmentCount int
	CreatedAt     time.Time
}

type EntityRepository struct {
	db *sql.DB
}

func NewEntityRepository(db *sql.DB) *EntityRepository {
	return &EntityRepository{db: db}
}

func (r *EntityRepository) ReplaceFragmentEntities(ctx context.Context, fragmentID string, entities []domain.FragmentEntity) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace fragment entities: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fragment_entities WHERE fragment_id = ?`, fragmentID); err != nil {
		return fmt.Errorf("clear fragment entities: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, entity := range entities {
		entityID := stableEntityID(entity.Kind, entity.Value)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO entities (id, kind, value, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  kind = excluded.kind,
  value = excluded.value`,
			entityID, entity.Kind, entity.Value, now,
		); err != nil {
			return fmt.Errorf("upsert entity: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO fragment_entities (fragment_id, entity_id, source, confidence, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(fragment_id, entity_id, source) DO UPDATE SET
  confidence = excluded.confidence,
  created_at = excluded.created_at`,
			fragmentID, entityID, entity.Source, entity.Confidence, now,
		); err != nil {
			return fmt.Errorf("upsert fragment entity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fragment entities: %w", err)
	}
	return nil
}

func (r *EntityRepository) ListByFragment(ctx context.Context, fragmentID string) ([]domain.FragmentEntity, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT e.kind, e.value, fe.source, fe.confidence
FROM fragment_entities fe
JOIN entities e ON e.id = fe.entity_id
WHERE fe.fragment_id = ?
ORDER BY e.kind, e.value, fe.source`, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("list fragment entities: %w", err)
	}
	defer rows.Close()

	var out []domain.FragmentEntity
	for rows.Next() {
		var item domain.FragmentEntity
		if err := rows.Scan(&item.Kind, &item.Value, &item.Source, &item.Confidence); err != nil {
			return nil, fmt.Errorf("scan fragment entity: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fragment entities: %w", err)
	}
	return out, nil
}

func (r *EntityRepository) List(ctx context.Context, kind string, limit int) ([]EntityRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT e.id, e.kind, e.value, e.created_at, COUNT(fe.fragment_id) AS fragment_count
FROM entities e
LEFT JOIN fragment_entities fe ON fe.entity_id = e.id
WHERE (? = '' OR e.kind = ?)
GROUP BY e.id, e.kind, e.value, e.created_at
ORDER BY fragment_count DESC, e.kind, e.value
LIMIT ?`, kind, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	defer rows.Close()

	var out []EntityRecord
	for rows.Next() {
		var item EntityRecord
		var createdAt string
		if err := rows.Scan(&item.ID, &item.Kind, &item.Value, &createdAt, &item.FragmentCount); err != nil {
			return nil, fmt.Errorf("scan entity: %w", err)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entities: %w", err)
	}
	return out, nil
}

func (r *EntityRepository) ListFragments(ctx context.Context, kind, value string, limit int) ([]domain.SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT
  f.id, f.source, f.source_type, f.source_id, f.title, f.content, f.content_hash,
  f.created_at, f.ingested_at, f.status, f.summary_text, f.indexed_at, f.metadata_json, f.ingest_name, f.canonical_path,
  fe.confidence
FROM entities e
JOIN fragment_entities fe ON fe.entity_id = e.id
JOIN fragments f ON f.id = fe.fragment_id
WHERE e.kind = ? AND e.value = ?
ORDER BY fe.confidence DESC, f.created_at DESC
LIMIT ?`, kind, value, limit)
	if err != nil {
		return nil, fmt.Errorf("list fragments by entity: %w", err)
	}
	defer rows.Close()

	var out []domain.SearchResult
	for rows.Next() {
		var (
			f                   domain.Fragment
			createdAt, ingested string
			indexedAt           string
			status              string
			confidence          float64
		)
		if err := rows.Scan(
			&f.ID, &f.Source, &f.SourceType, &f.SourceID, &f.Title, &f.Content, &f.ContentHash,
			&createdAt, &ingested, &status, &f.Summary, &indexedAt, &f.MetadataJSON, &f.IngestName, &f.CanonicalPath,
			&confidence,
		); err != nil {
			return nil, fmt.Errorf("scan fragment by entity: %w", err)
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.IngestedAt, _ = time.Parse(time.RFC3339, ingested)
		if indexedAt != "" {
			f.IndexedAt, _ = time.Parse(time.RFC3339, indexedAt)
		}
		f.Status = domain.FragmentStatus(status)
		out = append(out, domain.SearchResult{
			Fragment: f,
			Score:    confidence,
			Snippet:  f.Summary,
			Trace: domain.RecallTrace{
				Backend:      "sqlite",
				Strategy:     "entity_lookup",
				RelationKind: "entity_match",
				Reason:       "persisted_entity_match",
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fragments by entity: %w", err)
	}
	return out, nil
}

func stableEntityID(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + "\n" + value))
	return hex.EncodeToString(sum[:16])
}
