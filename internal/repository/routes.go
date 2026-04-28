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

type RoutingRepository struct {
	db *sql.DB
}

func NewRoutingRepository(db *sql.DB) *RoutingRepository {
	return &RoutingRepository{db: db}
}

func (r *RoutingRepository) AddDestination(ctx context.Context, in domain.Destination) (domain.Destination, error) {
	now := time.Now().UTC()
	if in.ID == "" {
		in.ID = stableID("destination", in.Name, in.Kind)
	}
	if in.ConfigJSON == "" {
		in.ConfigJSON = "{}"
	}
	in.CreatedAt = now
	in.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
INSERT INTO destinations (id, name, kind, config_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  kind = excluded.kind,
  config_json = excluded.config_json,
  updated_at = excluded.updated_at`,
		in.ID, in.Name, in.Kind, in.ConfigJSON,
		in.CreatedAt.Format(time.RFC3339), in.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return domain.Destination{}, fmt.Errorf("add destination: %w", err)
	}
	return in, nil
}

func (r *RoutingRepository) GetDestination(ctx context.Context, id string) (domain.Destination, error) {
	var item domain.Destination
	var createdAt, updatedAt string
	err := r.db.QueryRowContext(ctx, `
SELECT id, name, kind, config_json, created_at, updated_at
FROM destinations
WHERE id = ?`, id).
		Scan(&item.ID, &item.Name, &item.Kind, &item.ConfigJSON, &createdAt, &updatedAt)
	if err != nil {
		return domain.Destination{}, fmt.Errorf("get destination: %w", err)
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return item, nil
}

func (r *RoutingRepository) DeleteDestination(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM destinations WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete destination: %w", err)
	}
	return nil
}

func (r *RoutingRepository) ListDestinations(ctx context.Context) ([]domain.Destination, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, kind, config_json, created_at, updated_at
FROM destinations
ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list destinations: %w", err)
	}
	defer rows.Close()

	var out []domain.Destination
	for rows.Next() {
		var item domain.Destination
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.ConfigJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan destination: %w", err)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *RoutingRepository) AddRoute(ctx context.Context, in domain.Route) (domain.Route, error) {
	now := time.Now().UTC()
	if in.ID == "" {
		in.ID = stableID("route", in.Name, in.DestinationID)
	}
	in.CreatedAt = now
	in.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
INSERT INTO routes (
  id, name, match_source, match_type, match_entity_kind, match_entity_value, destination_id, auto_route, confidence_min, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  match_source = excluded.match_source,
  match_type = excluded.match_type,
  match_entity_kind = excluded.match_entity_kind,
  match_entity_value = excluded.match_entity_value,
  destination_id = excluded.destination_id,
  auto_route = excluded.auto_route,
  confidence_min = excluded.confidence_min,
  updated_at = excluded.updated_at`,
		in.ID, in.Name, in.MatchSource, in.MatchType, in.MatchEntityKind, in.MatchEntityValue, in.DestinationID,
		boolToInt(in.AutoRoute), in.ConfidenceMin,
		in.CreatedAt.Format(time.RFC3339), in.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return domain.Route{}, fmt.Errorf("add route: %w", err)
	}
	return in, nil
}

func (r *RoutingRepository) ListRoutes(ctx context.Context) ([]domain.Route, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, match_source, match_type, match_entity_kind, match_entity_value, destination_id, auto_route, confidence_min, created_at, updated_at
FROM routes
ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	defer rows.Close()

	var out []domain.Route
	for rows.Next() {
		var item domain.Route
		var autoRoute int
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.MatchSource, &item.MatchType, &item.MatchEntityKind, &item.MatchEntityValue, &item.DestinationID, &autoRoute, &item.ConfidenceMin, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan route: %w", err)
		}
		item.AutoRoute = autoRoute != 0
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *RoutingRepository) GetRoute(ctx context.Context, id string) (domain.Route, error) {
	var item domain.Route
	var autoRoute int
	var createdAt, updatedAt string
	err := r.db.QueryRowContext(ctx, `
SELECT id, name, match_source, match_type, match_entity_kind, match_entity_value, destination_id, auto_route, confidence_min, created_at, updated_at
FROM routes
WHERE id = ?`, id).
		Scan(&item.ID, &item.Name, &item.MatchSource, &item.MatchType, &item.MatchEntityKind, &item.MatchEntityValue, &item.DestinationID, &autoRoute, &item.ConfidenceMin, &createdAt, &updatedAt)
	if err != nil {
		return domain.Route{}, fmt.Errorf("get route: %w", err)
	}
	item.AutoRoute = autoRoute != 0
	item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return item, nil
}

func (r *RoutingRepository) DeleteRoute(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM routes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete route: %w", err)
	}
	return nil
}

func (r *RoutingRepository) CountRouteLogRefs(ctx context.Context, routeID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM route_log WHERE route_id = ?`, routeID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count route log refs: %w", err)
	}
	return count, nil
}

func (r *RoutingRepository) CountInboxRefs(ctx context.Context, routeID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox WHERE route_id = ?`, routeID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count inbox refs: %w", err)
	}
	return count, nil
}

func (r *RoutingRepository) ClearRouteRefs(ctx context.Context, routeID string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE route_log SET route_id = NULL WHERE route_id = ?`, routeID); err != nil {
		return fmt.Errorf("clear route_log refs: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE inbox SET route_id = NULL WHERE route_id = ?`, routeID); err != nil {
		return fmt.Errorf("clear inbox refs: %w", err)
	}
	return nil
}

func (r *RoutingRepository) ListRoutesForDestination(ctx context.Context, destinationID string) ([]domain.Route, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, match_source, match_type, match_entity_kind, match_entity_value, destination_id, auto_route, confidence_min, created_at, updated_at
FROM routes
WHERE destination_id = ?
ORDER BY name`, destinationID)
	if err != nil {
		return nil, fmt.Errorf("list routes for destination: %w", err)
	}
	defer rows.Close()

	var out []domain.Route
	for rows.Next() {
		var item domain.Route
		var autoRoute int
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.MatchSource, &item.MatchType, &item.MatchEntityKind, &item.MatchEntityValue, &item.DestinationID, &autoRoute, &item.ConfidenceMin, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan route for destination: %w", err)
		}
		item.AutoRoute = autoRoute != 0
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *RoutingRepository) DeleteRoutesForDestination(ctx context.Context, destinationID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM routes WHERE destination_id = ?`, destinationID); err != nil {
		return fmt.Errorf("delete routes for destination: %w", err)
	}
	return nil
}

func (r *RoutingRepository) LogDecision(ctx context.Context, in domain.RouteLogEntry) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO route_log (
  fragment_id, route_id, destination_id, decision, reason, created_at
) VALUES (?, ?, ?, ?, ?, ?)`,
		nullIfEmpty(in.FragmentID),
		nullIfEmpty(in.RouteID),
		nullIfEmpty(in.DestinationID),
		in.Decision,
		in.Reason,
		in.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("log route decision: %w", err)
	}
	return nil
}

func (r *RoutingRepository) ListRouteLog(ctx context.Context, fragmentID string) ([]domain.RouteLogEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, fragment_id, COALESCE(route_id, ''), COALESCE(destination_id, ''), decision, reason, created_at
FROM route_log
WHERE fragment_id = ?
ORDER BY id`, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("list route log: %w", err)
	}
	defer rows.Close()

	var out []domain.RouteLogEntry
	for rows.Next() {
		var item domain.RouteLogEntry
		var createdAt string
		if err := rows.Scan(&item.ID, &item.FragmentID, &item.RouteID, &item.DestinationID, &item.Decision, &item.Reason, &createdAt); err != nil {
			return nil, fmt.Errorf("scan route log: %w", err)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *RoutingRepository) ListRouteLogForDestination(ctx context.Context, destinationID string, limit int) ([]domain.RouteLogEntry, error) {
	query := `
SELECT id, fragment_id, COALESCE(route_id, ''), COALESCE(destination_id, ''), decision, reason, created_at
FROM route_log
WHERE destination_id = ?
ORDER BY id DESC`
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		query += "\nLIMIT ?"
		rows, err = r.db.QueryContext(ctx, query, destinationID, limit)
	} else {
		rows, err = r.db.QueryContext(ctx, query, destinationID)
	}
	if err != nil {
		return nil, fmt.Errorf("list route log by destination: %w", err)
	}
	defer rows.Close()

	var out []domain.RouteLogEntry
	for rows.Next() {
		var item domain.RouteLogEntry
		var createdAt string
		if err := rows.Scan(&item.ID, &item.FragmentID, &item.RouteID, &item.DestinationID, &item.Decision, &item.Reason, &createdAt); err != nil {
			return nil, fmt.Errorf("scan route log by destination: %w", err)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func stableID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(h[:16])
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
