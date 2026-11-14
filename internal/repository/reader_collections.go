package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const (
	// MaxReaderCollectionsPerPrincipal and MaxReaderCollectionItems bound the
	// unpaged collection list and the full-order rewrite a reorder performs.
	MaxReaderCollectionsPerPrincipal = 1000
	MaxReaderCollectionItems         = 5000
)

// ReaderCollectionRepository persists principal-owned collections and their
// ordered membership. Every method is scoped to one principal: a collection
// owned by anyone else is reported as sql.ErrNoRows.
type ReaderCollectionRepository struct {
	db *sql.DB
}

func NewReaderCollectionRepository(db *sql.DB) *ReaderCollectionRepository {
	return &ReaderCollectionRepository{db: db}
}

// ReaderCollectionConflictError reports a refused write that is not a stale
// revision: a name already in use, or a size limit.
type ReaderCollectionConflictError struct{ Reason string }

func (e *ReaderCollectionConflictError) Error() string {
	return "reader collection conflict: " + e.Reason
}

// ReaderCollectionOrderError reports a reorder whose fragment list is not
// exactly the current membership.
type ReaderCollectionOrderError struct{ Reason string }

func (e *ReaderCollectionOrderError) Error() string {
	return "invalid reader collection order: " + e.Reason
}

// ReaderCollectionPatch carries the fields an update changes; nil leaves a
// field as it is.
type ReaderCollectionPatch struct {
	Name        *string
	Description *string
	Kind        *string
}

const readerCollectionColumns = `c.id, c.principal_id, c.name, c.description, c.kind,
  (SELECT COUNT(*) FROM reader_collection_items m WHERE m.collection_id = c.id),
  c.revision, c.created_at, c.updated_at`

func scanReaderCollection(row interface{ Scan(...any) error }) (domain.ReaderCollection, error) {
	var item domain.ReaderCollection
	var createdAt, updatedAt string
	if err := row.Scan(&item.ID, &item.PrincipalID, &item.Name, &item.Description, &item.Kind,
		&item.ItemCount, &item.Revision, &createdAt, &updatedAt); err != nil {
		return domain.ReaderCollection{}, err
	}
	item.CreatedAt, item.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	return item, nil
}

func getReaderCollectionOn(ctx context.Context, q queryRowContext, principalID, collectionID string) (domain.ReaderCollection, error) {
	item, err := scanReaderCollection(q.QueryRowContext(ctx, `
SELECT `+readerCollectionColumns+`
FROM reader_collections c WHERE c.id = ? AND c.principal_id = ?`, collectionID, principalID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.ReaderCollection{}, fmt.Errorf("get reader collection: %w", err)
	}
	return item, err
}

func readerCollectionNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (r *ReaderCollectionRepository) Create(ctx context.Context, item domain.ReaderCollection) (domain.ReaderCollection, error) {
	conn, err := beginImmediateConn(ctx, r.db, "create reader collection")
	if err != nil {
		return domain.ReaderCollection{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var count, taken int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(name_key = ?), 0) FROM reader_collections WHERE principal_id = ?`,
		readerCollectionNameKey(item.Name), item.PrincipalID).Scan(&count, &taken); err != nil {
		return domain.ReaderCollection{}, fmt.Errorf("check reader collections: %w", err)
	}
	if taken != 0 {
		return domain.ReaderCollection{}, &ReaderCollectionConflictError{Reason: fmt.Sprintf("a collection named %q already exists", item.Name)}
	}
	if count >= MaxReaderCollectionsPerPrincipal {
		return domain.ReaderCollection{}, &ReaderCollectionConflictError{Reason: fmt.Sprintf("the limit of %d collections is reached", MaxReaderCollectionsPerPrincipal)}
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO reader_collections (
  id, principal_id, name, name_key, description, kind, revision, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, item.ID, item.PrincipalID, item.Name,
		readerCollectionNameKey(item.Name), item.Description, item.Kind,
		formatTime(item.CreatedAt), formatTime(item.CreatedAt)); err != nil {
		return domain.ReaderCollection{}, fmt.Errorf("insert reader collection: %w", err)
	}
	created, err := getReaderCollectionOn(ctx, conn, item.PrincipalID, item.ID)
	if err != nil {
		return domain.ReaderCollection{}, err
	}
	if err := commitReaderCommand(ctx, conn, "collection create"); err != nil {
		return domain.ReaderCollection{}, err
	}
	return created, nil
}

// List returns the principal's collections ordered by name.
func (r *ReaderCollectionRepository) List(ctx context.Context, principalID string) ([]domain.ReaderCollection, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT `+readerCollectionColumns+`
FROM reader_collections c WHERE c.principal_id = ?
ORDER BY c.name_key, c.id`, principalID)
	if err != nil {
		return nil, fmt.Errorf("list reader collections: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ReaderCollection, 0)
	for rows.Next() {
		item, err := scanReaderCollection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan reader collection: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ReaderCollectionRepository) Get(ctx context.Context, principalID, collectionID string) (domain.ReaderCollection, error) {
	return getReaderCollectionOn(ctx, r.db, principalID, collectionID)
}

// mutate runs one collection change in an immediate transaction. strict
// requires expected to equal the current revision; otherwise a revision at or
// behind the current one is accepted. apply reports whether it changed
// anything, and only then is the revision advanced.
func (r *ReaderCollectionRepository) mutate(ctx context.Context, principalID, collectionID string, expected int, strict bool, now time.Time,
	apply func(context.Context, *sql.Conn, domain.ReaderCollection) (bool, error)) (domain.ReaderCollection, error) {
	conn, err := beginImmediateConn(ctx, r.db, "reader collection")
	if err != nil {
		return domain.ReaderCollection{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	current, err := getReaderCollectionOn(ctx, conn, principalID, collectionID)
	if err != nil {
		return domain.ReaderCollection{}, err
	}
	if expected > current.Revision || (strict && expected != current.Revision) {
		return domain.ReaderCollection{}, &ReaderRevisionConflictError{Entity: "reader collection", Expected: expected, Current: current.Revision}
	}
	changed, err := apply(ctx, conn, current)
	if err != nil {
		return domain.ReaderCollection{}, err
	}
	if changed {
		if _, err := conn.ExecContext(ctx, `
UPDATE reader_collections SET revision = revision + 1, updated_at = ? WHERE id = ?`,
			formatTime(now), collectionID); err != nil {
			return domain.ReaderCollection{}, fmt.Errorf("advance reader collection revision: %w", err)
		}
		if current, err = getReaderCollectionOn(ctx, conn, principalID, collectionID); err != nil {
			return domain.ReaderCollection{}, err
		}
	}
	if err := commitReaderCommand(ctx, conn, "collection change"); err != nil {
		return domain.ReaderCollection{}, err
	}
	return current, nil
}

func (r *ReaderCollectionRepository) Update(ctx context.Context, principalID, collectionID string, expected int, patch ReaderCollectionPatch, now time.Time) (domain.ReaderCollection, error) {
	return r.mutate(ctx, principalID, collectionID, expected, true, now,
		func(ctx context.Context, conn *sql.Conn, current domain.ReaderCollection) (bool, error) {
			next := current
			if patch.Name != nil {
				next.Name = *patch.Name
			}
			if patch.Description != nil {
				next.Description = *patch.Description
			}
			if patch.Kind != nil {
				next.Kind = *patch.Kind
			}
			if next.Name == current.Name && next.Description == current.Description && next.Kind == current.Kind {
				return false, nil
			}
			var taken int
			if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM reader_collections WHERE principal_id = ? AND name_key = ? AND id <> ?`,
				principalID, readerCollectionNameKey(next.Name), collectionID).Scan(&taken); err != nil {
				return false, fmt.Errorf("check reader collection name: %w", err)
			}
			if taken != 0 {
				return false, &ReaderCollectionConflictError{Reason: fmt.Sprintf("a collection named %q already exists", next.Name)}
			}
			if _, err := conn.ExecContext(ctx, `
UPDATE reader_collections SET name = ?, name_key = ?, description = ?, kind = ? WHERE id = ?`,
				next.Name, readerCollectionNameKey(next.Name), next.Description, next.Kind, collectionID); err != nil {
				return false, fmt.Errorf("update reader collection: %w", err)
			}
			return true, nil
		})
}

// Delete removes the collection and its membership rows. Fragments are not
// touched.
func (r *ReaderCollectionRepository) Delete(ctx context.Context, principalID, collectionID string, expected int) error {
	conn, err := beginImmediateConn(ctx, r.db, "delete reader collection")
	if err != nil {
		return err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	current, err := getReaderCollectionOn(ctx, conn, principalID, collectionID)
	if err != nil {
		return err
	}
	if expected != current.Revision {
		return &ReaderRevisionConflictError{Entity: "reader collection", Expected: expected, Current: current.Revision}
	}
	for _, statement := range []string{
		`DELETE FROM reader_collection_items WHERE collection_id = ?`,
		`DELETE FROM reader_collections WHERE id = ?`,
	} {
		if _, err := conn.ExecContext(ctx, statement, collectionID); err != nil {
			return fmt.Errorf("delete reader collection: %w", err)
		}
	}
	return commitReaderCommand(ctx, conn, "collection delete")
}

// AddItem adds a fragment at position, or at the end when position is nil or
// past it. An append is accepted from any revision at or behind the current
// one; a positioned add must carry the current revision. Adding a fragment
// that is already a member changes nothing.
func (r *ReaderCollectionRepository) AddItem(ctx context.Context, principalID, collectionID string, expected int, fragmentID string, position *int, now time.Time) (domain.ReaderCollection, error) {
	return r.mutate(ctx, principalID, collectionID, expected, position != nil, now,
		func(ctx context.Context, conn *sql.Conn, _ domain.ReaderCollection) (bool, error) {
			canonicalID, err := resolveCanonicalFragmentID(ctx, conn, fragmentID)
			if err != nil {
				return false, err
			}
			order, err := readerCollectionOrderOn(ctx, conn, collectionID)
			if err != nil {
				return false, err
			}
			for _, member := range order {
				if member == canonicalID {
					return false, nil
				}
			}
			if len(order) >= MaxReaderCollectionItems {
				return false, &ReaderCollectionConflictError{Reason: fmt.Sprintf("the limit of %d items is reached", MaxReaderCollectionItems)}
			}
			index := len(order)
			if position != nil && *position < index {
				index = *position
			}
			// The new row takes a position past every existing one so the
			// rewrite below can move it into place.
			if _, err := conn.ExecContext(ctx, `
INSERT INTO reader_collection_items(collection_id, fragment_id, position, added_at)
VALUES (?, ?, (SELECT COALESCE(MAX(position), -1) + 1 FROM reader_collection_items WHERE collection_id = ?), ?)`,
				collectionID, canonicalID, collectionID, formatTime(now)); err != nil {
				return false, fmt.Errorf("add reader collection item: %w", err)
			}
			next := make([]string, 0, len(order)+1)
			next = append(next, order[:index]...)
			next = append(next, canonicalID)
			next = append(next, order[index:]...)
			return true, writeReaderCollectionOrderOn(ctx, conn, collectionID, next)
		})
}

// RemoveItem removes a fragment from the collection. Removing a fragment that
// is not a member changes nothing.
func (r *ReaderCollectionRepository) RemoveItem(ctx context.Context, principalID, collectionID string, expected int, fragmentID string, now time.Time) (domain.ReaderCollection, error) {
	return r.mutate(ctx, principalID, collectionID, expected, true, now,
		func(ctx context.Context, conn *sql.Conn, _ domain.ReaderCollection) (bool, error) {
			canonicalID, err := resolveCanonicalFragmentID(ctx, conn, fragmentID)
			if err != nil {
				return false, err
			}
			res, err := conn.ExecContext(ctx, `
DELETE FROM reader_collection_items WHERE collection_id = ? AND fragment_id = ?`, collectionID, canonicalID)
			if err != nil {
				return false, fmt.Errorf("remove reader collection item: %w", err)
			}
			if removed, err := res.RowsAffected(); err != nil || removed == 0 {
				return false, err
			}
			order, err := readerCollectionOrderOn(ctx, conn, collectionID)
			if err != nil {
				return false, err
			}
			return true, writeReaderCollectionOrderOn(ctx, conn, collectionID, order)
		})
}

// Reorder replaces the order. fragmentIDs must be exactly the current
// members, each once.
func (r *ReaderCollectionRepository) Reorder(ctx context.Context, principalID, collectionID string, expected int, fragmentIDs []string, now time.Time) (domain.ReaderCollection, error) {
	return r.mutate(ctx, principalID, collectionID, expected, true, now,
		func(ctx context.Context, conn *sql.Conn, _ domain.ReaderCollection) (bool, error) {
			order, err := readerCollectionOrderOn(ctx, conn, collectionID)
			if err != nil {
				return false, err
			}
			members := make(map[string]bool, len(order))
			for _, member := range order {
				members[member] = true
			}
			next := make([]string, 0, len(fragmentIDs))
			seen := make(map[string]bool, len(fragmentIDs))
			for _, fragmentID := range fragmentIDs {
				canonicalID := fragmentID
				if !members[canonicalID] {
					// An alias of a member is accepted, as it is on add and remove.
					canonicalID, err = resolveCanonicalFragmentID(ctx, conn, fragmentID)
					if err != nil && !errors.Is(err, sql.ErrNoRows) {
						return false, err
					}
				}
				if !members[canonicalID] {
					return false, &ReaderCollectionOrderError{Reason: fmt.Sprintf("%q is not in the collection", fragmentID)}
				}
				if seen[canonicalID] {
					return false, &ReaderCollectionOrderError{Reason: fmt.Sprintf("%q appears more than once", fragmentID)}
				}
				seen[canonicalID] = true
				next = append(next, canonicalID)
			}
			if len(next) != len(order) {
				return false, &ReaderCollectionOrderError{Reason: fmt.Sprintf("the order names %d of the collection's %d items", len(next), len(order))}
			}
			same := true
			for index := range order {
				if order[index] != next[index] {
					same = false
					break
				}
			}
			if same {
				return false, nil
			}
			return true, writeReaderCollectionOrderOn(ctx, conn, collectionID, next)
		})
}

func readerCollectionOrderOn(ctx context.Context, conn *sql.Conn, collectionID string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `
SELECT fragment_id FROM reader_collection_items
WHERE collection_id = ? ORDER BY position, added_at, fragment_id`, collectionID)
	if err != nil {
		return nil, fmt.Errorf("read reader collection order: %w", err)
	}
	defer rows.Close()
	var order []string
	for rows.Next() {
		var fragmentID string
		if err := rows.Scan(&fragmentID); err != nil {
			return nil, fmt.Errorf("scan reader collection order: %w", err)
		}
		order = append(order, fragmentID)
	}
	return order, rows.Err()
}

// writeReaderCollectionOrderOn rewrites positions to 0..n-1 in the given
// order, touching only the rows whose position differs.
func writeReaderCollectionOrderOn(ctx context.Context, conn *sql.Conn, collectionID string, order []string) error {
	for position, fragmentID := range order {
		if _, err := conn.ExecContext(ctx, `
UPDATE reader_collection_items SET position = ?
WHERE collection_id = ? AND fragment_id = ? AND position <> ?`,
			position, collectionID, fragmentID, position); err != nil {
			return fmt.Errorf("write reader collection order: %w", err)
		}
	}
	return nil
}
