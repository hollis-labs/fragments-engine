package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
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

	if err := upsertFragmentEntities(ctx, tx, fragmentID, entities); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fragment entities: %w", err)
	}
	return nil
}

// ReplaceFragmentEntitiesByKind replaces the fragment_entities rows for the
// given kind(s) only, leaving entities of any other kind untouched.
//
// This exists because independent ingest stages own disjoint entity kinds
// for the same fragment within a single pipeline run — e.g. DirectiveStage
// owns Kind:"directive", extract.FromFragment (written by RecallStage) owns
// Kind:"workspace"/"repo"/"model"/"tool", and manual intake owns
// Kind:"tag". ReplaceFragmentEntities does a blanket delete-all-then-insert
// for the fragment; if two stages in the same run both called it, whichever
// ran last would silently wipe out the other's rows. This method scopes the
// delete+reinsert to just the kind(s) the caller is responsible for, so
// stages can safely write in sequence without clobbering each other.
//
// Every entity in entities must have a Kind present in kinds; entities
// outside that scope are rejected rather than silently written or dropped.
func (r *EntityRepository) ReplaceFragmentEntitiesByKind(ctx context.Context, fragmentID string, entities []domain.FragmentEntity, kinds ...string) error {
	if len(kinds) == 0 {
		return fmt.Errorf("replace fragment entities by kind: at least one kind is required")
	}
	kindSet := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		kindSet[kind] = struct{}{}
	}
	for _, entity := range entities {
		if _, ok := kindSet[entity.Kind]; !ok {
			return fmt.Errorf("replace fragment entities by kind: entity kind %q is outside scope %v", entity.Kind, kinds)
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace fragment entities by kind: %w", err)
	}
	defer tx.Rollback()

	placeholders := make([]string, len(kinds))
	args := make([]any, 0, len(kinds)+1)
	args = append(args, fragmentID)
	for i, kind := range kinds {
		placeholders[i] = "?"
		args = append(args, kind)
	}
	deleteQuery := fmt.Sprintf(`
DELETE FROM fragment_entities
WHERE fragment_id = ?
  AND entity_id IN (SELECT id FROM entities WHERE kind IN (%s))`, strings.Join(placeholders, ", "))
	if _, err := tx.ExecContext(ctx, deleteQuery, args...); err != nil {
		return fmt.Errorf("clear fragment entities by kind: %w", err)
	}

	if err := upsertFragmentEntities(ctx, tx, fragmentID, entities); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fragment entities by kind: %w", err)
	}
	return nil
}

// upsertFragmentEntities upserts each entity and its fragment link within an
// already-open transaction. Shared by ReplaceFragmentEntities and
// ReplaceFragmentEntitiesByKind, which differ only in how much of the
// fragment's existing entities they clear first.
func upsertFragmentEntities(ctx context.Context, tx *sql.Tx, fragmentID string, entities []domain.FragmentEntity) error {
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
  fe.confidence
FROM entities e
JOIN fragment_entities fe ON fe.entity_id = e.id
JOIN fragments f ON f.id = fe.fragment_id
` + fragmentReadJoins + `
WHERE e.kind = ? AND e.value = ?
  AND NOT EXISTS (
    SELECT 1 FROM fragment_identity_aliases fia WHERE fia.alias_fragment_id = f.id
  )
ORDER BY fe.confidence DESC, f.created_at DESC
LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, kind, value, limit)
	if err != nil {
		return nil, fmt.Errorf("list fragments by entity: %w", err)
	}
	defer rows.Close()

	var out []domain.SearchResult
	for rows.Next() {
		var (
			f                   domain.Fragment
			state               fragmentScanState
			previewAttachmentID sql.NullString
			confidence          float64
		)
		destinations := state.destinations(&f)
		destinations = append(destinations, &previewAttachmentID, &confidence)
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan fragment by entity: %w", err)
		}
		state.finish(&f)
		item := domain.SearchResult{
			Fragment: f,
			Score:    confidence,
			Snippet:  f.Summary,
			Trace: domain.RecallTrace{
				Backend:      "sqlite",
				Strategy:     "entity_lookup",
				RelationKind: "entity_match",
				Reason:       "persisted_entity_match",
			},
		}
		if previewAttachmentID.Valid {
			item.PreviewAttachmentID = previewAttachmentID.String
		}
		out = append(out, item)
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
