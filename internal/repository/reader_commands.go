package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type ReaderCommandRepository struct {
	db *sql.DB
}

type readerWriteContext interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func NewReaderCommandRepository(db *sql.DB) *ReaderCommandRepository {
	return &ReaderCommandRepository{db: db}
}

type ReaderCommandWrite struct {
	CommandID          string
	IdempotencyKey     string
	SemanticDigest     string
	PrincipalID        string
	FragmentID         string
	Command            string
	ExpectedRevision   int
	Tag                string
	NormalizedTag      string
	AnnotationID       string
	AnnotationText     string
	AnnotationSelector *domain.TextQuoteSelector
	ExpectedNote       int
	BodyMarkdown       string
	Position           domain.ReadingPosition
	TargetID           string
	CreatedAt          time.Time
}

type ReaderCommandResult struct {
	Receipt          domain.ReaderCommandReceipt
	CanonicalID      string
	ReadingState     domain.ReadingState
	HasReadingState  bool
	Changed          bool
	IdempotentReplay bool
}

type ReaderCommandConflictError struct {
	CommandID      string
	IdempotencyKey string
	Reason         string
}

func (e *ReaderCommandConflictError) Error() string {
	return fmt.Sprintf("reader command conflict for %q: %s", e.CommandID, e.Reason)
}

type ReaderRevisionConflictError struct {
	Entity   string
	Expected int
	Current  int
}

type ReaderPositionError struct{ Reason string }

func (e *ReaderPositionError) Error() string { return "invalid reading position: " + e.Reason }

func (e *ReaderRevisionConflictError) Error() string {
	return fmt.Sprintf("%s revision conflict: expected %d, current %d", e.Entity, e.Expected, e.Current)
}

func (r *ReaderCommandRepository) ResolveCanonicalFragmentID(ctx context.Context, fragmentID string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("resolve reader fragment: repository is required")
	}
	return resolveCanonicalFragmentID(ctx, r.db, strings.TrimSpace(fragmentID))
}

func (r *ReaderCommandRepository) ApplyLocal(ctx context.Context, write ReaderCommandWrite) (ReaderCommandResult, error) {
	if err := validateReaderCommandWrite(write); err != nil {
		return ReaderCommandResult{}, err
	}
	conn, err := beginImmediateConn(ctx, r.db, "reader command")
	if err != nil {
		return ReaderCommandResult{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)

	canonicalID, err := resolveCanonicalFragmentID(ctx, conn, write.FragmentID)
	if err != nil {
		return ReaderCommandResult{}, err
	}
	write.FragmentID = canonicalID
	replay, found, err := findReaderCommandReceipt(ctx, conn, write)
	if err != nil {
		return ReaderCommandResult{}, err
	}
	if found {
		if err := commitReaderCommand(ctx, conn, "replay"); err != nil {
			return ReaderCommandResult{}, err
		}
		return ReaderCommandResult{Receipt: replay, CanonicalID: canonicalID, IdempotentReplay: true}, nil
	}

	aggregate, err := readerAggregateRevisionOn(ctx, conn, write.PrincipalID, canonicalID)
	if err != nil {
		return ReaderCommandResult{}, err
	}
	if write.ExpectedRevision > aggregate {
		return ReaderCommandResult{}, &ReaderRevisionConflictError{Entity: "reader aggregate", Expected: write.ExpectedRevision, Current: aggregate}
	}
	receipt := receiptFromWrite(write, aggregate, domain.ReaderCommandPending)
	if err := insertReaderCommandReceipt(ctx, conn, receipt); err != nil {
		return ReaderCommandResult{}, err
	}

	result := ReaderCommandResult{CanonicalID: canonicalID}
	switch write.Command {
	case "add_tag":
		result.Changed, err = applyReaderTagOn(ctx, conn, write, aggregate, false)
	case "remove_tag":
		if write.ExpectedRevision != aggregate {
			return ReaderCommandResult{}, &ReaderRevisionConflictError{Entity: "reader tag overlay", Expected: write.ExpectedRevision, Current: aggregate}
		}
		result.Changed, err = applyReaderTagOn(ctx, conn, write, aggregate, true)
	case "append_capture_note":
		result.Changed, err = appendReaderCaptureNoteOn(ctx, conn, write)
	case "update_curated_note":
		result.Changed, err = updateReaderCuratedNoteOn(ctx, conn, write)
	case "set_reading_progress", "mark_read", "mark_unread":
		result.ReadingState, result.Changed, err = applyReadingStateOn(ctx, conn, write)
		result.HasReadingState = true
	default:
		err = fmt.Errorf("apply local reader command: unsupported command %q", write.Command)
	}
	if err != nil {
		return ReaderCommandResult{}, err
	}
	if result.Changed {
		aggregate, err = bumpReaderAggregateOn(ctx, conn, write.PrincipalID, canonicalID, write.CreatedAt)
		if err != nil {
			return ReaderCommandResult{}, err
		}
		if write.Command == "add_tag" || write.Command == "remove_tag" {
			if _, err := conn.ExecContext(ctx, `
UPDATE reader_tag_overlay_events SET aggregate_revision = ? WHERE command_id = ?`, aggregate, write.CommandID); err != nil {
				return ReaderCommandResult{}, fmt.Errorf("set reader tag aggregate revision: %w", err)
			}
		}
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE reader_command_receipts
SET state = 'succeeded', aggregate_revision = ?, updated_at = ?
WHERE command_id = ?`, aggregate, formatTime(write.CreatedAt), write.CommandID); err != nil {
		return ReaderCommandResult{}, fmt.Errorf("complete local reader receipt: %w", err)
	}
	receipt.State = domain.ReaderCommandSucceeded
	receipt.AggregateRevision = aggregate
	if err := commitReaderCommand(ctx, conn, "local command"); err != nil {
		return ReaderCommandResult{}, err
	}
	result.Receipt = receipt
	return result, nil
}

// ReserveAssetCommand records an acquisition command before invoking the
// separately idempotent AssetAcquisitionService. Pending replay is safe to
// resume with the same command and idempotency identities.
func (r *ReaderCommandRepository) ReserveAssetCommand(ctx context.Context, write ReaderCommandWrite) (ReaderCommandResult, error) {
	return r.reserveExternal(ctx, write, "", false)
}

// ReserveEffect atomically persists both the semantic receipt and an effect
// intent. Route and destination existence is validated inside the same write
// transaction before an intent can become visible.
func (r *ReaderCommandRepository) ReserveEffect(ctx context.Context, write ReaderCommandWrite) (ReaderCommandResult, error) {
	return r.reserveExternal(ctx, write, write.Command, true)
}

func (r *ReaderCommandRepository) reserveExternal(ctx context.Context, write ReaderCommandWrite, effectKind string, withEffect bool) (ReaderCommandResult, error) {
	if err := validateReaderCommandWrite(write); err != nil {
		return ReaderCommandResult{}, err
	}
	conn, err := beginImmediateConn(ctx, r.db, "reserve reader command")
	if err != nil {
		return ReaderCommandResult{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	canonicalID, err := resolveCanonicalFragmentID(ctx, conn, write.FragmentID)
	if err != nil {
		return ReaderCommandResult{}, err
	}
	write.FragmentID = canonicalID
	replay, found, err := findReaderCommandReceipt(ctx, conn, write)
	if err != nil {
		return ReaderCommandResult{}, err
	}
	if found {
		if err := commitReaderCommand(ctx, conn, "reservation replay"); err != nil {
			return ReaderCommandResult{}, err
		}
		return ReaderCommandResult{Receipt: replay, CanonicalID: canonicalID, IdempotentReplay: true}, nil
	}
	aggregate, err := readerAggregateRevisionOn(ctx, conn, write.PrincipalID, canonicalID)
	if err != nil {
		return ReaderCommandResult{}, err
	}
	if write.ExpectedRevision > aggregate {
		return ReaderCommandResult{}, &ReaderRevisionConflictError{Entity: "reader aggregate", Expected: write.ExpectedRevision, Current: aggregate}
	}
	if withEffect && write.ExpectedRevision != aggregate {
		return ReaderCommandResult{}, &ReaderRevisionConflictError{Entity: "reader aggregate", Expected: write.ExpectedRevision, Current: aggregate}
	}
	if write.Command == "request_asset_acquisition" {
		if err := validateAttachedMediaAssetOn(ctx, conn, canonicalID, write.TargetID); err != nil {
			return ReaderCommandResult{}, err
		}
	}
	if withEffect {
		if err := validateReaderEffectTargetOn(ctx, conn, effectKind, write.TargetID); err != nil {
			return ReaderCommandResult{}, err
		}
	}
	receipt := receiptFromWrite(write, aggregate, domain.ReaderCommandPending)
	if err := insertReaderCommandReceipt(ctx, conn, receipt); err != nil {
		return ReaderCommandResult{}, err
	}
	if withEffect {
		if _, err := conn.ExecContext(ctx, `
INSERT INTO reader_command_effects (
  command_id, principal_id, fragment_id, kind, target_id, state,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`, write.CommandID, write.PrincipalID,
			canonicalID, effectKind, write.TargetID, formatTime(write.CreatedAt), formatTime(write.CreatedAt)); err != nil {
			return ReaderCommandResult{}, fmt.Errorf("insert reader command effect: %w", err)
		}
	}
	if err := commitReaderCommand(ctx, conn, "reservation"); err != nil {
		return ReaderCommandResult{}, err
	}
	return ReaderCommandResult{Receipt: receipt, CanonicalID: canonicalID}, nil
}

func (r *ReaderCommandRepository) ClaimEffect(ctx context.Context, commandID string, now time.Time) (domain.ReaderCommandEffect, bool, error) {
	conn, err := beginImmediateConn(ctx, r.db, "claim reader effect")
	if err != nil {
		return domain.ReaderCommandEffect{}, false, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	effect, err := loadReaderCommandEffect(ctx, conn, commandID)
	if err != nil {
		return domain.ReaderCommandEffect{}, false, err
	}
	if effect.State != domain.ReaderCommandPending {
		if err := commitReaderCommand(ctx, conn, "effect replay"); err != nil {
			return domain.ReaderCommandEffect{}, false, err
		}
		return effect, false, nil
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE reader_command_effects
SET state = 'executing', claimed_at = ?, updated_at = ?
WHERE command_id = ? AND state = 'pending'`, formatTime(now), formatTime(now), commandID); err != nil {
		return domain.ReaderCommandEffect{}, false, fmt.Errorf("claim reader effect: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE reader_command_receipts SET state = 'executing', updated_at = ?
WHERE command_id = ? AND state = 'pending'`, formatTime(now), commandID); err != nil {
		return domain.ReaderCommandEffect{}, false, fmt.Errorf("advance reader receipt claim: %w", err)
	}
	if err := commitReaderCommand(ctx, conn, "effect claim"); err != nil {
		return domain.ReaderCommandEffect{}, false, err
	}
	effect.State = domain.ReaderCommandExecuting
	effect.ClaimedAt = timePtr(now.UTC())
	effect.UpdatedAt = now.UTC()
	return effect, true, nil
}

func (r *ReaderCommandRepository) FinalizeExternal(ctx context.Context, commandID string, state domain.ReaderCommandState, resultJSON, errorCode, errorDetail string, now time.Time) (domain.ReaderCommandReceipt, error) {
	if state != domain.ReaderCommandSucceeded && state != domain.ReaderCommandFailed && state != domain.ReaderCommandUncertain {
		return domain.ReaderCommandReceipt{}, fmt.Errorf("finalize reader command: invalid terminal state %q", state)
	}
	if strings.TrimSpace(resultJSON) == "" {
		resultJSON = `{}`
	}
	if !json.Valid([]byte(resultJSON)) {
		return domain.ReaderCommandReceipt{}, fmt.Errorf("finalize reader command: result must be valid JSON")
	}
	conn, err := beginImmediateConn(ctx, r.db, "finalize reader command")
	if err != nil {
		return domain.ReaderCommandReceipt{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	receipt, err := loadReaderCommandReceipt(ctx, conn, "command_id", strings.TrimSpace(commandID))
	if err != nil {
		return domain.ReaderCommandReceipt{}, err
	}
	if receipt.State.Terminal() {
		if err := commitReaderCommand(ctx, conn, "terminal replay"); err != nil {
			return domain.ReaderCommandReceipt{}, err
		}
		return receipt, nil
	}
	aggregate, err := bumpReaderAggregateOn(ctx, conn, receipt.PrincipalID, receipt.FragmentID, now)
	if err != nil {
		return domain.ReaderCommandReceipt{}, err
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE reader_command_receipts
SET state = ?, aggregate_revision = ?, result_json = ?, error_code = ?,
    error_detail = ?, updated_at = ?
WHERE command_id = ?`, string(state), aggregate, resultJSON, errorCode,
		errorDetail, formatTime(now), receipt.CommandID); err != nil {
		return domain.ReaderCommandReceipt{}, fmt.Errorf("finalize reader command receipt: %w", err)
	}
	var effectCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM reader_command_effects WHERE command_id = ?`, receipt.CommandID).Scan(&effectCount); err != nil {
		return domain.ReaderCommandReceipt{}, fmt.Errorf("check reader effect: %w", err)
	}
	if effectCount != 0 {
		if _, err := conn.ExecContext(ctx, `
UPDATE reader_command_effects
SET state = ?, result_json = ?, error_detail = ?, completed_at = ?, updated_at = ?
WHERE command_id = ?`, string(state), resultJSON, errorDetail, formatTime(now),
			formatTime(now), receipt.CommandID); err != nil {
			return domain.ReaderCommandReceipt{}, fmt.Errorf("finalize reader command effect: %w", err)
		}
	}
	if err := commitReaderCommand(ctx, conn, "finalization"); err != nil {
		return domain.ReaderCommandReceipt{}, err
	}
	receipt.State = state
	receipt.AggregateRevision = aggregate
	receipt.ResultJSON = resultJSON
	receipt.ErrorCode = errorCode
	receipt.ErrorDetail = errorDetail
	receipt.UpdatedAt = now.UTC()
	return receipt, nil
}

func (r *ReaderCommandRepository) GetReadingState(ctx context.Context, principalID, fragmentID string) (domain.ReadingState, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, strings.TrimSpace(fragmentID))
	if err != nil {
		return domain.ReadingState{}, err
	}
	state, _, err := getReadingStateOn(ctx, r.db, strings.TrimSpace(principalID), canonicalID)
	return state, err
}

func (r *ReaderCommandRepository) GetAggregateRevision(ctx context.Context, principalID, fragmentID string) (int, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, strings.TrimSpace(fragmentID))
	if err != nil {
		return 0, err
	}
	return readerAggregateRevisionOn(ctx, r.db, strings.TrimSpace(principalID), canonicalID)
}

func (r *ReaderCommandRepository) ListTagOverlays(ctx context.Context, principalID, fragmentID string) ([]domain.ReaderTagOverlay, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, strings.TrimSpace(fragmentID))
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT e.command_id, e.principal_id, e.fragment_id, e.normalized_value,
       e.display_value, e.action, e.aggregate_revision, e.created_at
FROM reader_tag_overlay_events e
WHERE e.principal_id = ? AND e.fragment_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM reader_tag_overlay_events newer
    WHERE newer.principal_id = e.principal_id
      AND newer.fragment_id = e.fragment_id
      AND newer.normalized_value = e.normalized_value
      AND newer.aggregate_revision > e.aggregate_revision
  )
ORDER BY e.normalized_value`, strings.TrimSpace(principalID), canonicalID)
	if err != nil {
		return nil, fmt.Errorf("list reader tag overlays: %w", err)
	}
	defer rows.Close()
	var out []domain.ReaderTagOverlay
	for rows.Next() {
		var item domain.ReaderTagOverlay
		var action, createdAt string
		if err := rows.Scan(&item.CommandID, &item.PrincipalID, &item.FragmentID,
			&item.NormalizedValue, &item.DisplayValue, &action, &item.AggregateRevision,
			&createdAt); err != nil {
			return nil, fmt.Errorf("scan reader tag overlay: %w", err)
		}
		item.Suppressed = action == "suppress"
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ReaderCommandRepository) HasCaptureAttempt(ctx context.Context, fragmentID string) (bool, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, strings.TrimSpace(fragmentID))
	if err != nil {
		return false, err
	}
	var count int
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_attempts WHERE fragment_id = ?`, canonicalID).Scan(&count)
	return count > 0, err
}

func (r *ReaderCommandRepository) ListEffects(ctx context.Context, principalID, fragmentID string) ([]domain.ReaderCommandEffect, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, strings.TrimSpace(fragmentID))
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT command_id FROM reader_command_effects
WHERE principal_id = ? AND fragment_id = ? ORDER BY created_at, command_id`,
		strings.TrimSpace(principalID), canonicalID)
	if err != nil {
		return nil, fmt.Errorf("list reader effects: %w", err)
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
	out := make([]domain.ReaderCommandEffect, 0, len(ids))
	for _, id := range ids {
		effect, err := loadReaderCommandEffect(ctx, r.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, effect)
	}
	return out, nil
}

func validateReaderCommandWrite(write ReaderCommandWrite) error {
	if strings.TrimSpace(write.CommandID) == "" || strings.TrimSpace(write.IdempotencyKey) == "" ||
		len(strings.TrimSpace(write.SemanticDigest)) != 64 || strings.TrimSpace(write.PrincipalID) == "" ||
		strings.TrimSpace(write.FragmentID) == "" || strings.TrimSpace(write.Command) == "" ||
		write.ExpectedRevision < 0 || write.CreatedAt.IsZero() {
		return fmt.Errorf("reader command identity, semantic digest, principal, fragment, command, non-negative revision, and timestamp are required")
	}
	return nil
}

func findReaderCommandReceipt(ctx context.Context, q readerWriteContext, write ReaderCommandWrite) (domain.ReaderCommandReceipt, bool, error) {
	byID, idFound, err := maybeLoadReaderCommandReceipt(ctx, q, "command_id", write.CommandID)
	if err != nil {
		return domain.ReaderCommandReceipt{}, false, err
	}
	byKey, keyFound, err := maybeLoadReaderCommandReceipt(ctx, q, "idempotency_key", write.IdempotencyKey)
	if err != nil {
		return domain.ReaderCommandReceipt{}, false, err
	}
	if !idFound && !keyFound {
		return domain.ReaderCommandReceipt{}, false, nil
	}
	existing := byID
	if !idFound {
		existing = byKey
	}
	if idFound && keyFound && byID.CommandID != byKey.CommandID {
		return domain.ReaderCommandReceipt{}, false, &ReaderCommandConflictError{CommandID: write.CommandID, IdempotencyKey: write.IdempotencyKey, Reason: "command ID and idempotency key identify different commands"}
	}
	if existing.CommandID != write.CommandID || existing.IdempotencyKey != write.IdempotencyKey ||
		existing.SemanticDigest != write.SemanticDigest || existing.PrincipalID != write.PrincipalID ||
		existing.FragmentID != write.FragmentID || existing.Command != write.Command {
		return domain.ReaderCommandReceipt{}, false, &ReaderCommandConflictError{CommandID: write.CommandID, IdempotencyKey: write.IdempotencyKey, Reason: "command or idempotency identity was reused with different semantics"}
	}
	return existing, true, nil
}

func maybeLoadReaderCommandReceipt(ctx context.Context, q readerWriteContext, column, value string) (domain.ReaderCommandReceipt, bool, error) {
	receipt, err := loadReaderCommandReceipt(ctx, q, column, value)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReaderCommandReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func loadReaderCommandReceipt(ctx context.Context, q readerWriteContext, column, value string) (domain.ReaderCommandReceipt, error) {
	if column != "command_id" && column != "idempotency_key" {
		return domain.ReaderCommandReceipt{}, fmt.Errorf("invalid reader receipt lookup")
	}
	var item domain.ReaderCommandReceipt
	var state, createdAt, updatedAt string
	err := q.QueryRowContext(ctx, `
SELECT command_id, idempotency_key, semantic_digest, principal_id, fragment_id,
       command, state, aggregate_revision, result_json, error_code, error_detail,
       created_at, updated_at
FROM reader_command_receipts WHERE `+column+` = ?`, value).Scan(
		&item.CommandID, &item.IdempotencyKey, &item.SemanticDigest, &item.PrincipalID,
		&item.FragmentID, &item.Command, &state, &item.AggregateRevision,
		&item.ResultJSON, &item.ErrorCode, &item.ErrorDetail, &createdAt, &updatedAt)
	if err != nil {
		return domain.ReaderCommandReceipt{}, err
	}
	item.State = domain.ReaderCommandState(state)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func insertReaderCommandReceipt(ctx context.Context, q readerWriteContext, item domain.ReaderCommandReceipt) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO reader_command_receipts (
  command_id, idempotency_key, semantic_digest, principal_id, fragment_id,
  command, state, aggregate_revision, result_json, error_code, error_detail,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.CommandID,
		item.IdempotencyKey, item.SemanticDigest, item.PrincipalID, item.FragmentID,
		item.Command, string(item.State), item.AggregateRevision, item.ResultJSON,
		item.ErrorCode, item.ErrorDetail, formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert reader command receipt: %w", err)
	}
	return nil
}

func receiptFromWrite(write ReaderCommandWrite, aggregate int, state domain.ReaderCommandState) domain.ReaderCommandReceipt {
	return domain.ReaderCommandReceipt{CommandID: write.CommandID, IdempotencyKey: write.IdempotencyKey,
		SemanticDigest: write.SemanticDigest, PrincipalID: write.PrincipalID,
		FragmentID: write.FragmentID, Command: write.Command, State: state,
		AggregateRevision: aggregate, ResultJSON: `{}`, CreatedAt: write.CreatedAt.UTC(), UpdatedAt: write.CreatedAt.UTC()}
}

func readerAggregateRevisionOn(ctx context.Context, q queryRowContext, principalID, fragmentID string) (int, error) {
	var revision int
	err := q.QueryRowContext(ctx, `
SELECT revision FROM reader_command_aggregates
WHERE principal_id = ? AND fragment_id = ?`, principalID, fragmentID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get reader aggregate revision: %w", err)
	}
	return revision, nil
}

func bumpReaderAggregateOn(ctx context.Context, q readerWriteContext, principalID, fragmentID string, now time.Time) (int, error) {
	if _, err := q.ExecContext(ctx, `
INSERT INTO reader_command_aggregates(principal_id, fragment_id, revision, updated_at)
VALUES (?, ?, 1, ?)
ON CONFLICT(principal_id, fragment_id) DO UPDATE SET
  revision = revision + 1,
  updated_at = excluded.updated_at`, principalID, fragmentID, formatTime(now)); err != nil {
		return 0, fmt.Errorf("bump reader aggregate revision: %w", err)
	}
	return readerAggregateRevisionOn(ctx, q, principalID, fragmentID)
}

func applyReaderTagOn(ctx context.Context, conn *sql.Conn, write ReaderCommandWrite, aggregate int, suppress bool) (bool, error) {
	var previous string
	err := conn.QueryRowContext(ctx, `
SELECT action FROM reader_tag_overlay_events
WHERE principal_id = ? AND fragment_id = ? AND normalized_value = ?
ORDER BY aggregate_revision DESC LIMIT 1`, write.PrincipalID, write.FragmentID, write.NormalizedTag).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("get reader tag overlay: %w", err)
	}
	action := "add"
	if suppress {
		action = "suppress"
	}
	if err == nil && previous == action {
		return false, nil
	}
	if !suppress {
		tag := domain.AttributedTag{ID: domain.DigestText("reader-tag\n" + write.CommandID),
			FragmentID: write.FragmentID, Value: write.Tag, NormalizedValue: write.NormalizedTag,
			Source: domain.AttributionUser, ObservationID: write.CommandID,
			Producer: domain.AdapterVersion{Adapter: "reader-command", Version: "v1"},
			ActorID:  write.PrincipalID, ObservedAt: write.CreatedAt.UTC(), CreatedAt: write.CreatedAt.UTC()}
		if err := insertCaptureTag(ctx, conn, tag); err != nil {
			return false, err
		}
	}
	// The aggregate revision is filled after the shared aggregate is bumped.
	if _, err := conn.ExecContext(ctx, `
INSERT INTO reader_tag_overlay_events (
  command_id, principal_id, fragment_id, normalized_value, display_value,
  action, aggregate_revision, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, write.CommandID, write.PrincipalID,
		write.FragmentID, write.NormalizedTag, write.Tag, action, aggregate+1,
		formatTime(write.CreatedAt)); err != nil {
		return false, fmt.Errorf("insert reader tag overlay: %w", err)
	}
	return true, nil
}

func appendReaderCaptureNoteOn(ctx context.Context, conn *sql.Conn, write ReaderCommandWrite) (bool, error) {
	var captureID string
	err := conn.QueryRowContext(ctx, `
SELECT capture_id FROM capture_attempts
WHERE fragment_id = ? ORDER BY captured_at DESC, capture_id DESC LIMIT 1`, write.FragmentID).Scan(&captureID)
	if err != nil {
		return false, err
	}
	var existingCapture string
	err = conn.QueryRowContext(ctx, `SELECT capture_id FROM capture_annotations WHERE id = ?`, write.AnnotationID).Scan(&existingCapture)
	if err == nil {
		return false, &ReaderCommandConflictError{CommandID: write.CommandID, IdempotencyKey: write.IdempotencyKey, Reason: fmt.Sprintf("annotation ID %q is already bound to capture %q", write.AnnotationID, existingCapture)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("check reader annotation identity: %w", err)
	}
	selectorJSON := `{}`
	if write.AnnotationSelector != nil {
		raw, err := json.Marshal(write.AnnotationSelector)
		if err != nil {
			return false, fmt.Errorf("encode reader annotation selector: %w", err)
		}
		selectorJSON = string(raw)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO capture_annotations (
  id, capture_id, fragment_id, kind, text, selector_json, position_json,
  actor_id, captured_at, created_at
) VALUES (?, ?, ?, 'capture_note', ?, ?, '{}', ?, ?, ?)`, write.AnnotationID,
		captureID, write.FragmentID, write.AnnotationText, selectorJSON,
		write.PrincipalID, formatTime(write.CreatedAt), formatTime(write.CreatedAt)); err != nil {
		return false, fmt.Errorf("append reader capture note: %w", err)
	}
	return true, nil
}

func updateReaderCuratedNoteOn(ctx context.Context, conn *sql.Conn, write ReaderCommandWrite) (bool, error) {
	current, found, err := getCuratedNote(ctx, conn, write.FragmentID)
	if err != nil {
		return false, err
	}
	if !found {
		if write.ExpectedNote != 0 {
			return false, &CuratedNoteConflictError{Expected: write.ExpectedNote, Current: current}
		}
		if _, err := conn.ExecContext(ctx, `
INSERT INTO curated_notes(fragment_id, body_markdown, revision, actor_id, updated_at)
VALUES (?, ?, 1, ?, ?)`, write.FragmentID, write.BodyMarkdown, write.PrincipalID,
			formatTime(write.CreatedAt)); err != nil {
			return false, fmt.Errorf("create reader curated note: %w", err)
		}
		return true, nil
	}
	if current.Revision != write.ExpectedNote {
		return false, &CuratedNoteConflictError{Expected: write.ExpectedNote, Current: current}
	}
	if current.BodyMarkdown == write.BodyMarkdown {
		return false, nil
	}
	res, err := conn.ExecContext(ctx, `
UPDATE curated_notes SET body_markdown = ?, revision = revision + 1,
  actor_id = ?, updated_at = ?
WHERE fragment_id = ? AND revision = ?`, write.BodyMarkdown, write.PrincipalID,
		formatTime(write.CreatedAt), write.FragmentID, write.ExpectedNote)
	if err != nil {
		return false, fmt.Errorf("update reader curated note: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed != 1 {
		authoritative, _, loadErr := getCuratedNote(ctx, conn, write.FragmentID)
		if loadErr != nil {
			return false, loadErr
		}
		return false, &CuratedNoteConflictError{Expected: write.ExpectedNote, Current: authoritative}
	}
	return true, nil
}

func applyReadingStateOn(ctx context.Context, q readerWriteContext, write ReaderCommandWrite) (domain.ReadingState, bool, error) {
	current, found, err := getReadingStateOn(ctx, q, write.PrincipalID, write.FragmentID)
	if err != nil {
		return domain.ReadingState{}, false, err
	}
	if write.ExpectedRevision != current.Revision {
		return domain.ReadingState{}, false, &ReaderRevisionConflictError{Entity: "reading state", Expected: write.ExpectedRevision, Current: current.Revision}
	}
	next := current
	switch write.Command {
	case "set_reading_progress":
		if err := validateReadingPositionOwnershipOn(ctx, q, write.FragmentID, write.Position); err != nil {
			return domain.ReadingState{}, false, err
		}
		if found && current.State == domain.ReadingInProgress && readingPositionEqual(current.Position, write.Position) {
			return current, false, nil
		}
		next.State = domain.ReadingInProgress
		next.Position = write.Position
		next.LastOpened = timePtr(write.CreatedAt.UTC())
		next.CompletedAt = nil
	case "mark_read":
		if found && current.State == domain.ReadingRead {
			return current, false, nil
		}
		next.State = domain.ReadingRead
		next.LastOpened = timePtr(write.CreatedAt.UTC())
		next.CompletedAt = timePtr(write.CreatedAt.UTC())
	case "mark_unread":
		if current.State == domain.ReadingUnread && current.Position.Kind == domain.ReadingPositionNone {
			return current, false, nil
		}
		next.State = domain.ReadingUnread
		next.Position = domain.ReadingPosition{Kind: domain.ReadingPositionNone}
		next.CompletedAt = nil
	}
	if found && readingStateSemanticallyEqual(current, next) {
		return current, false, nil
	}
	if !found && next.State == domain.ReadingUnread && next.Position.Kind == domain.ReadingPositionNone {
		return current, false, nil
	}
	next.Revision = current.Revision + 1
	next.UpdatedAt = write.CreatedAt.UTC()
	positionJSON, err := json.Marshal(next.Position)
	if err != nil {
		return domain.ReadingState{}, false, fmt.Errorf("encode reading position: %w", err)
	}
	_, err = q.ExecContext(ctx, `
INSERT INTO reading_states (
  principal_id, fragment_id, state, position_kind, position_json,
  last_opened_at, completed_at, revision, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(principal_id, fragment_id) DO UPDATE SET
  state = excluded.state,
  position_kind = excluded.position_kind,
  position_json = excluded.position_json,
  last_opened_at = excluded.last_opened_at,
  completed_at = excluded.completed_at,
  revision = excluded.revision,
  updated_at = excluded.updated_at`, next.PrincipalID, next.FragmentID,
		string(next.State), string(next.Position.Kind), string(positionJSON),
		nullableTime(next.LastOpened), nullableTime(next.CompletedAt), next.Revision,
		formatTime(next.UpdatedAt))
	if err != nil {
		return domain.ReadingState{}, false, fmt.Errorf("persist reading state: %w", err)
	}
	return next, true, nil
}

func getReadingStateOn(ctx context.Context, q queryRowContext, principalID, fragmentID string) (domain.ReadingState, bool, error) {
	item := domain.ReadingState{PrincipalID: principalID, FragmentID: fragmentID,
		State: domain.ReadingUnread, Position: domain.ReadingPosition{Kind: domain.ReadingPositionNone}}
	var state, kind, positionJSON, updatedAt string
	var openedAt, completedAt sql.NullString
	err := q.QueryRowContext(ctx, `
SELECT state, position_kind, position_json, last_opened_at, completed_at,
       revision, updated_at
FROM reading_states WHERE principal_id = ? AND fragment_id = ?`, principalID, fragmentID).Scan(
		&state, &kind, &positionJSON, &openedAt, &completedAt, &item.Revision, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return domain.ReadingState{}, false, fmt.Errorf("get reading state: %w", err)
	}
	item.State = domain.ReadingStatus(state)
	if err := json.Unmarshal([]byte(positionJSON), &item.Position); err != nil {
		return domain.ReadingState{}, false, fmt.Errorf("decode reading position: %w", err)
	}
	item.Position.Kind = domain.ReadingPositionKind(kind)
	if openedAt.Valid {
		item.LastOpened = timePtr(parseTime(openedAt.String))
	}
	if completedAt.Valid {
		item.CompletedAt = timePtr(parseTime(completedAt.String))
	}
	item.UpdatedAt = parseTime(updatedAt)
	return item, true, nil
}

func validateReadingPositionOwnershipOn(ctx context.Context, q queryRowContext, fragmentID string, position domain.ReadingPosition) error {
	switch position.Kind {
	case domain.ReadingPositionGallery:
		var count int
		err := q.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fragments f
JOIN attachment_refs ar ON ar.fragment_revision_id = f.current_revision_id
WHERE f.id = ? AND ar.id = ? AND ar.position = ?`, fragmentID, position.AttachmentID, *position.Index).Scan(&count)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("gallery position attachment does not belong to the fragment's current revision: %w", sql.ErrNoRows)
		}
	case domain.ReadingPositionVideo:
		predicate := "ma.kind = 'video'"
		args := []any{fragmentID}
		if position.ProviderMediaID != "" {
			predicate += " AND ma.provider_media_id = ?"
			args = append(args, position.ProviderMediaID)
		}
		if err := requireCurrentMediaOn(ctx, q, predicate, args...); err != nil {
			return fmt.Errorf("video position provider media does not belong to the fragment's current revision: %w", err)
		}
	case domain.ReadingPositionDocument:
		var count, maxPages int
		err := q.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MAX(ma.page_count), 0) FROM fragments f
JOIN attachment_refs ar ON ar.fragment_revision_id = f.current_revision_id
JOIN media_assets ma ON ma.id = ar.media_asset_id
WHERE f.id = ? AND ma.kind = 'document'`, fragmentID).Scan(&count, &maxPages)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("document position requires a document on the fragment's current revision: %w", sql.ErrNoRows)
		}
		if maxPages > 0 && *position.Page > maxPages {
			return &ReaderPositionError{Reason: fmt.Sprintf("document page %d exceeds current document page count %d", *position.Page, maxPages)}
		}
	case domain.ReadingPositionAudio:
		if err := requireCurrentMediaOn(ctx, q, "ma.kind = 'audio'", fragmentID); err != nil {
			return fmt.Errorf("audio position requires audio on the fragment's current revision: %w", err)
		}
	}
	return nil
}

func requireCurrentMediaOn(ctx context.Context, q queryRowContext, predicate string, args ...any) error {
	var count int
	query := `
SELECT COUNT(*) FROM fragments f
JOIN attachment_refs ar ON ar.fragment_revision_id = f.current_revision_id
JOIN media_assets ma ON ma.id = ar.media_asset_id
WHERE f.id = ? AND ` + predicate
	if err := q.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func validateAttachedMediaAssetOn(ctx context.Context, q queryRowContext, fragmentID, mediaAssetID string) error {
	var count int
	err := q.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fragments f
JOIN attachment_refs ar ON ar.fragment_revision_id = f.current_revision_id
WHERE f.id = ? AND ar.media_asset_id = ?`, fragmentID, mediaAssetID).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func validateReaderEffectTargetOn(ctx context.Context, q queryRowContext, kind, targetID string) error {
	var count int
	var query string
	switch kind {
	case "route":
		query = `SELECT COUNT(*) FROM routes r JOIN destinations d ON d.id = r.destination_id WHERE r.id = ?`
	case "materialize":
		query = `SELECT COUNT(*) FROM destinations WHERE id = ?`
	default:
		return fmt.Errorf("invalid reader effect kind %q", kind)
	}
	if err := q.QueryRowContext(ctx, query, targetID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func loadReaderCommandEffect(ctx context.Context, q queryRowContext, commandID string) (domain.ReaderCommandEffect, error) {
	var item domain.ReaderCommandEffect
	var state, claimedAt, completedAt sql.NullString
	var createdAt, updatedAt string
	err := q.QueryRowContext(ctx, `
SELECT command_id, principal_id, fragment_id, kind, target_id, state,
       result_json, error_detail, claimed_at, completed_at, created_at, updated_at
FROM reader_command_effects WHERE command_id = ?`, commandID).Scan(&item.CommandID,
		&item.PrincipalID, &item.FragmentID, &item.Kind, &item.TargetID, &state,
		&item.ResultJSON, &item.ErrorDetail, &claimedAt, &completedAt, &createdAt, &updatedAt)
	if err != nil {
		return domain.ReaderCommandEffect{}, err
	}
	item.State = domain.ReaderCommandState(state.String)
	if claimedAt.Valid {
		item.ClaimedAt = timePtr(parseTime(claimedAt.String))
	}
	if completedAt.Valid {
		item.CompletedAt = timePtr(parseTime(completedAt.String))
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func readingStateSemanticallyEqual(a, b domain.ReadingState) bool {
	if a.State != b.State || !readingPositionEqual(a.Position, b.Position) {
		return false
	}
	if (a.CompletedAt == nil) != (b.CompletedAt == nil) {
		return false
	}
	return a.CompletedAt == nil || a.CompletedAt.Equal(*b.CompletedAt)
}

func readingPositionEqual(a, b domain.ReadingPosition) bool {
	rawA, _ := json.Marshal(a)
	rawB, _ := json.Marshal(b)
	return string(rawA) == string(rawB)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func timePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func commitReaderCommand(ctx context.Context, conn *sql.Conn, description string) error {
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit reader command %s: %w", description, err)
	}
	return nil
}
