package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type InboxRepository struct {
	db *sql.DB
}

func NewInboxRepository(db *sql.DB) *InboxRepository {
	return &InboxRepository{db: db}
}

func (r *InboxRepository) Stage(ctx context.Context, fragmentID, reason string, stagedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO inbox (fragment_id, reason, staged_at, route_id)
VALUES (?, ?, ?, NULL)
ON CONFLICT(fragment_id) DO UPDATE SET
  reason = excluded.reason,
  staged_at = excluded.staged_at`,
		fragmentID,
		reason,
		stagedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("stage inbox item: %w", err)
	}
	return nil
}

func (r *InboxRepository) List(ctx context.Context, limit int) ([]domain.InboxItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT fragment_id, reason, staged_at, route_id
FROM inbox
ORDER BY staged_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}
	defer rows.Close()

	items := make([]domain.InboxItem, 0, limit)
	for rows.Next() {
		var item domain.InboxItem
		var stagedAt string
		var routeID sql.NullString
		if err := rows.Scan(&item.FragmentID, &item.Reason, &stagedAt, &routeID); err != nil {
			return nil, fmt.Errorf("scan inbox item: %w", err)
		}
		item.StagedAt, _ = time.Parse(time.RFC3339, stagedAt)
		if routeID.Valid {
			item.RouteID = routeID.String
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox: %w", err)
	}
	return items, nil
}

func (r *InboxRepository) Remove(ctx context.Context, fragmentID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM inbox WHERE fragment_id = ?`, fragmentID)
	if err != nil {
		return fmt.Errorf("remove inbox item: %w", err)
	}
	return nil
}

func (r *InboxRepository) ListEntityGroups(ctx context.Context, kind string, limit int) ([]domain.InboxEntityGroup, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT e.kind, e.value, COUNT(DISTINCT i.fragment_id) AS fragment_count
FROM inbox i
JOIN fragment_entities fe ON fe.fragment_id = i.fragment_id
JOIN entities e ON e.id = fe.entity_id
WHERE (? = '' OR e.kind = ?)
GROUP BY e.kind, e.value
ORDER BY fragment_count DESC, e.kind, e.value
LIMIT ?`, kind, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("list inbox entity groups: %w", err)
	}
	defer rows.Close()

	var out []domain.InboxEntityGroup
	for rows.Next() {
		var item domain.InboxEntityGroup
		if err := rows.Scan(&item.Kind, &item.Value, &item.FragmentCount); err != nil {
			return nil, fmt.Errorf("scan inbox entity group: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox entity groups: %w", err)
	}
	return out, nil
}

func (r *InboxRepository) ListByEntity(ctx context.Context, kind, value string, limit int) ([]domain.InboxItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT i.fragment_id, i.reason, i.staged_at, i.route_id
FROM inbox i
JOIN fragment_entities fe ON fe.fragment_id = i.fragment_id
JOIN entities e ON e.id = fe.entity_id
WHERE e.kind = ? AND e.value = ?
ORDER BY i.staged_at DESC
LIMIT ?`, kind, value, limit)
	if err != nil {
		return nil, fmt.Errorf("list inbox by entity: %w", err)
	}
	defer rows.Close()

	items := make([]domain.InboxItem, 0, limit)
	for rows.Next() {
		var item domain.InboxItem
		var stagedAt string
		var routeID sql.NullString
		if err := rows.Scan(&item.FragmentID, &item.Reason, &stagedAt, &routeID); err != nil {
			return nil, fmt.Errorf("scan inbox by entity item: %w", err)
		}
		item.StagedAt, _ = time.Parse(time.RFC3339, stagedAt)
		if routeID.Valid {
			item.RouteID = routeID.String
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox by entity: %w", err)
	}
	return items, nil
}
