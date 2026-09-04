package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type CaptureProtocolState struct {
	Attempt              domain.CaptureAttempt
	Bindings             []domain.CaptureAssetBinding
	Outcomes             []domain.CaptureVariantOutcome
	EnrichmentInProgress bool
	CompletionStatusJSON string
}

type CaptureCompletionWrite struct {
	CaptureID           string
	IdempotencyKey      string
	SemanticDigest      string
	ReportJSON          string
	WarningsJSON        string
	Outcomes            []domain.CaptureVariantOutcome
	UpdatedAt           time.Time
	BuildStatusSnapshot func(CaptureProtocolState) (string, error)
}

type CaptureCompletionReportConflictError struct {
	CaptureID string
	Reason    string
}

func (e *CaptureCompletionReportConflictError) Error() string {
	return fmt.Sprintf("capture completion conflict for %q: %s", e.CaptureID, e.Reason)
}

func listCaptureAssetBindings(ctx context.Context, q queryContext, captureID string) ([]domain.CaptureAssetBinding, error) {
	rows, err := q.QueryContext(ctx, `
SELECT b.capture_id, b.fragment_revision_id, b.client_variant_id,
       b.asset_variant_id, b.instruction_action, b.expected_digest,
       b.reason, b.created_at, COALESCE(v.digest, '')
FROM capture_asset_bindings b
JOIN asset_variants v ON v.id = b.asset_variant_id
WHERE b.capture_id = ?
ORDER BY b.client_variant_id`, strings.TrimSpace(captureID))
	if err != nil {
		return nil, fmt.Errorf("list capture asset bindings: %w", err)
	}
	defer rows.Close()
	var out []domain.CaptureAssetBinding
	for rows.Next() {
		var item domain.CaptureAssetBinding
		var action, expectedDigest, digest, createdAt string
		if err := rows.Scan(&item.CaptureID, &item.FragmentRevisionID,
			&item.ClientVariantID, &item.AssetVariantID, &action,
			&expectedDigest, &item.Reason, &createdAt, &digest); err != nil {
			return nil, fmt.Errorf("scan capture asset binding: %w", err)
		}
		item.Action = domain.AssetInstructionAction(action)
		if expectedDigest != "" {
			item.ExpectedDigest = domain.ContentDigest{Algorithm: "sha256", Value: expectedDigest}
		}
		if digest != "" {
			item.Digest = domain.ContentDigest{Algorithm: "sha256", Value: digest}
		}
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *CaptureRepository) GetAssetBinding(ctx context.Context, captureID, clientVariantID string) (domain.CaptureAssetBinding, error) {
	return getCaptureAssetBinding(ctx, r.db, captureID, clientVariantID)
}

func getCaptureAssetBinding(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, captureID, clientVariantID string) (domain.CaptureAssetBinding, error) {
	var item domain.CaptureAssetBinding
	var action, expectedDigest, digest, createdAt string
	err := q.QueryRowContext(ctx, `
SELECT b.capture_id, b.fragment_revision_id, b.client_variant_id,
       b.asset_variant_id, b.instruction_action, b.expected_digest,
       b.reason, b.created_at, COALESCE(v.digest, '')
FROM capture_asset_bindings b
JOIN capture_attempts a ON a.capture_id = b.capture_id
  AND a.fragment_revision_id = b.fragment_revision_id
JOIN asset_variants v ON v.id = b.asset_variant_id
WHERE b.capture_id = ? AND b.client_variant_id = ?`,
		strings.TrimSpace(captureID), strings.TrimSpace(clientVariantID)).Scan(
		&item.CaptureID, &item.FragmentRevisionID, &item.ClientVariantID,
		&item.AssetVariantID, &action, &expectedDigest, &item.Reason,
		&createdAt, &digest)
	if err != nil {
		return domain.CaptureAssetBinding{}, fmt.Errorf("get capture asset binding: %w", err)
	}
	item.Action = domain.AssetInstructionAction(action)
	if expectedDigest != "" {
		item.ExpectedDigest = domain.ContentDigest{Algorithm: "sha256", Value: expectedDigest}
	}
	if digest != "" {
		item.Digest = domain.ContentDigest{Algorithm: "sha256", Value: digest}
	}
	item.CreatedAt = parseTime(createdAt)
	return item, nil
}

func (r *CaptureRepository) RecordUploadedOutcome(ctx context.Context, binding domain.CaptureAssetBinding, variant domain.AssetVariant, now time.Time) error {
	conn, err := beginImmediateConn(ctx, r.db, "record capture asset upload")
	if err != nil {
		return err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	current, err := getCaptureAssetBinding(ctx, conn, binding.CaptureID, binding.ClientVariantID)
	if err != nil {
		return err
	}
	if current.FragmentRevisionID != binding.FragmentRevisionID || current.AssetVariantID != variant.ID {
		return &MediaConflictError{Kind: "capture asset binding", Identity: binding.ClientVariantID, Reason: "resolved capture/revision variant changed"}
	}
	stored, err := loadAssetVariant(ctx, conn, variant.ID)
	if err != nil {
		return err
	}
	if stored.AcquisitionState != domain.AcquisitionAvailable || stored.Digest.Empty() {
		return fmt.Errorf("record capture asset upload: variant is not available")
	}
	outcome := domain.CaptureVariantOutcome{
		CaptureID: binding.CaptureID, ClientVariantID: binding.ClientVariantID,
		Outcome: domain.AssetOutcomeUploaded, Digest: stored.Digest,
		ByteSize: stored.ByteSize, UpdatedAt: now.UTC(),
	}
	if err := upsertCaptureAssetOutcome(ctx, conn, outcome); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE capture_attempts SET completion = CASE WHEN completion = 'accepting' THEN 'transferring' ELSE completion END,
  updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END
WHERE capture_id = ?`, formatTime(now), formatTime(now), binding.CaptureID); err != nil {
		return fmt.Errorf("mark capture transferring: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit capture upload outcome: %w", err)
	}
	return nil
}

// ValidateAssetUploadOn proves that a client variant is pinned to this capture's
// accepted revision and that client contribution is still open. It is designed
// to run while MediaService holds the same BEGIN IMMEDIATE transaction used to
// attach the blob, closing the completion-vs-late-upload race.
func (r *CaptureRepository) ValidateAssetUploadOn(ctx context.Context, q MediaWriteConn, captureID, clientVariantID, variantID string) (domain.CaptureAssetBinding, error) {
	binding, err := getCaptureAssetBinding(ctx, q, captureID, clientVariantID)
	if err != nil {
		return domain.CaptureAssetBinding{}, err
	}
	if binding.AssetVariantID != strings.TrimSpace(variantID) {
		return domain.CaptureAssetBinding{}, &MediaConflictError{Kind: "capture asset binding", Identity: clientVariantID, Reason: "variant does not belong to capture revision"}
	}
	var completion string
	if err := q.QueryRowContext(ctx, `SELECT completion FROM capture_attempts WHERE capture_id = ?`, binding.CaptureID).Scan(&completion); err != nil {
		return domain.CaptureAssetBinding{}, fmt.Errorf("inspect capture upload lifecycle: %w", err)
	}
	if domain.CaptureCompletion(completion).Terminal() {
		return domain.CaptureAssetBinding{}, &CaptureCompletionReportConflictError{CaptureID: captureID, Reason: "client contribution is already complete"}
	}
	if binding.Action != domain.AssetRequestUpload && binding.Action != domain.AssetReuseBlob {
		return domain.CaptureAssetBinding{}, &MediaConflictError{Kind: "capture asset binding", Identity: clientVariantID, Reason: fmt.Sprintf("instruction action %s does not accept browser bytes", binding.Action)}
	}
	return binding, nil
}

func (r *CaptureRepository) RecordUploadedOutcomeOn(ctx context.Context, q MediaWriteConn, binding domain.CaptureAssetBinding, variant domain.AssetVariant, now time.Time) error {
	if variant.AcquisitionState != domain.AcquisitionAvailable || variant.Digest.Empty() {
		return fmt.Errorf("record capture asset upload: variant is not available")
	}
	outcome := domain.CaptureVariantOutcome{
		CaptureID: binding.CaptureID, ClientVariantID: binding.ClientVariantID,
		Outcome: domain.AssetOutcomeUploaded, Digest: variant.Digest,
		ByteSize: variant.ByteSize, UpdatedAt: now.UTC(),
	}
	if err := upsertCaptureAssetOutcome(ctx, q, outcome); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `
UPDATE capture_attempts SET completion = CASE WHEN completion = 'accepting' THEN 'transferring' ELSE completion END,
  updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END
WHERE capture_id = ?`, formatTime(now), formatTime(now), binding.CaptureID); err != nil {
		return fmt.Errorf("mark capture transferring: %w", err)
	}
	return nil
}

func upsertCaptureAssetOutcome(ctx context.Context, q MediaWriteConn, item domain.CaptureVariantOutcome) error {
	if !item.Outcome.Valid() || strings.TrimSpace(item.CaptureID) == "" || strings.TrimSpace(item.ClientVariantID) == "" || item.UpdatedAt.IsZero() {
		return fmt.Errorf("record capture asset outcome: capture, client variant, outcome, and timestamp are required")
	}
	if item.ByteSize < 0 {
		return fmt.Errorf("record capture asset outcome: byte size must be non-negative")
	}
	if !item.Digest.Empty() {
		var err error
		item.Digest, err = item.Digest.Normalized()
		if err != nil {
			return fmt.Errorf("record capture asset outcome: %w", err)
		}
	}
	_, err := q.ExecContext(ctx, `
INSERT INTO capture_asset_outcomes (
  capture_id, client_variant_id, outcome, digest, byte_size, reason,
  retryable, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(capture_id, client_variant_id) DO UPDATE SET
  outcome = excluded.outcome, digest = excluded.digest,
  byte_size = excluded.byte_size, reason = excluded.reason,
  retryable = excluded.retryable, updated_at = excluded.updated_at`,
		item.CaptureID, item.ClientVariantID, string(item.Outcome),
		item.Digest.Value, item.ByteSize, item.Reason, boolInt(item.Retryable),
		formatTime(item.UpdatedAt))
	if err != nil {
		return fmt.Errorf("record capture asset outcome %q: %w", item.ClientVariantID, err)
	}
	return nil
}

func listCaptureAssetOutcomes(ctx context.Context, q queryContext, captureID string) ([]domain.CaptureVariantOutcome, error) {
	rows, err := q.QueryContext(ctx, `
SELECT capture_id, client_variant_id, outcome, digest, byte_size, reason,
       retryable, updated_at
FROM capture_asset_outcomes WHERE capture_id = ? ORDER BY client_variant_id`, captureID)
	if err != nil {
		return nil, fmt.Errorf("list capture asset outcomes: %w", err)
	}
	defer rows.Close()
	var out []domain.CaptureVariantOutcome
	for rows.Next() {
		var item domain.CaptureVariantOutcome
		var outcome, digest, updatedAt string
		var retryable int
		if err := rows.Scan(&item.CaptureID, &item.ClientVariantID, &outcome,
			&digest, &item.ByteSize, &item.Reason, &retryable, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan capture asset outcome: %w", err)
		}
		item.Outcome = domain.CaptureAssetOutcome(outcome)
		if digest != "" {
			item.Digest = domain.ContentDigest{Algorithm: "sha256", Value: digest}
		}
		item.Retryable = retryable == 1
		item.UpdatedAt = parseTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *CaptureRepository) GetProtocolState(ctx context.Context, captureID string) (CaptureProtocolState, error) {
	attempt, err := r.GetAttempt(ctx, strings.TrimSpace(captureID))
	if err != nil {
		return CaptureProtocolState{}, err
	}
	bindings, err := listCaptureAssetBindings(ctx, r.db, attempt.CaptureID)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	outcomes, err := listCaptureAssetOutcomes(ctx, r.db, attempt.CaptureID)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	var pending int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM capture_followup_outbox
WHERE capture_id = ? AND state IN ('pending', 'processing')`, attempt.CaptureID).Scan(&pending); err != nil {
		return CaptureProtocolState{}, fmt.Errorf("inspect capture follow-up state: %w", err)
	}
	var activeJobs int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM enrichment_jobs
WHERE fragment_revision_id = ? AND status IN ('queued', 'running')`, attempt.FragmentRevisionID).Scan(&activeJobs); err != nil {
		return CaptureProtocolState{}, fmt.Errorf("inspect capture enrichment jobs: %w", err)
	}
	return CaptureProtocolState{Attempt: attempt, Bindings: bindings, Outcomes: outcomes, EnrichmentInProgress: pending > 0 || activeJobs > 0}, nil
}

func (r *CaptureRepository) CompleteCapture(ctx context.Context, write CaptureCompletionWrite) (CaptureProtocolState, error) {
	if strings.TrimSpace(write.CaptureID) == "" || strings.TrimSpace(write.IdempotencyKey) == "" || strings.TrimSpace(write.SemanticDigest) == "" || write.UpdatedAt.IsZero() {
		return CaptureProtocolState{}, fmt.Errorf("complete capture: capture ID, idempotency key, semantic digest, and timestamp are required")
	}
	conn, err := beginImmediateConn(ctx, r.db, "capture client completion")
	if err != nil {
		return CaptureProtocolState{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	attempt, found, err := findCaptureAttempt(ctx, conn, `capture_id = ?`, write.CaptureID)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	if !found {
		return CaptureProtocolState{}, sql.ErrNoRows
	}
	var existingCaptureID, existingKey, existingDigest, existingStatus string
	err = conn.QueryRowContext(ctx, `
SELECT capture_id, idempotency_key, semantic_digest, status_json FROM capture_completion_reports
WHERE capture_id = ? OR idempotency_key = ?`, write.CaptureID, write.IdempotencyKey).Scan(
		&existingCaptureID, &existingKey, &existingDigest, &existingStatus)
	if err == nil {
		if existingCaptureID != write.CaptureID || existingKey != write.IdempotencyKey || existingDigest != write.SemanticDigest {
			return CaptureProtocolState{}, &CaptureCompletionReportConflictError{CaptureID: write.CaptureID, Reason: "idempotency key or capture ID was reused with different completion semantics"}
		}
		state, err := loadProtocolState(ctx, conn, attempt)
		if err != nil {
			return CaptureProtocolState{}, err
		}
		state.CompletionStatusJSON = existingStatus
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return CaptureProtocolState{}, fmt.Errorf("commit completion replay: %w", err)
		}
		return state, nil
	}
	if err != sql.ErrNoRows {
		return CaptureProtocolState{}, fmt.Errorf("find capture completion report: %w", err)
	}
	if attempt.Completion.Terminal() {
		return CaptureProtocolState{}, &CaptureCompletionReportConflictError{
			CaptureID: write.CaptureID,
			Reason:    "capture attempt is already terminal",
		}
	}

	bindings, err := listCaptureAssetBindings(ctx, conn, write.CaptureID)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	existingOutcomes, err := listCaptureAssetOutcomes(ctx, conn, write.CaptureID)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	existingByVariant := make(map[string]domain.CaptureVariantOutcome, len(existingOutcomes))
	for _, item := range existingOutcomes {
		existingByVariant[item.ClientVariantID] = item
	}
	reported := make(map[string]domain.CaptureVariantOutcome, len(write.Outcomes))
	for _, item := range write.Outcomes {
		if item.CaptureID != "" && item.CaptureID != write.CaptureID {
			return CaptureProtocolState{}, fmt.Errorf("complete capture: outcome capture ID mismatch")
		}
		item.CaptureID = write.CaptureID
		item.UpdatedAt = write.UpdatedAt.UTC()
		if _, duplicate := reported[item.ClientVariantID]; duplicate {
			return CaptureProtocolState{}, fmt.Errorf("complete capture: duplicate client variant %q", item.ClientVariantID)
		}
		reported[item.ClientVariantID] = item
	}
	partial := false
	mediaRepo := NewMediaRepository(r.db)
	for _, binding := range bindings {
		variant, err := mediaRepo.GetVariantOn(ctx, conn, binding.AssetVariantID)
		if err != nil {
			return CaptureProtocolState{}, err
		}
		item, supplied := reported[binding.ClientVariantID]
		if supplied {
			delete(reported, binding.ClientVariantID)
		}
		item.CaptureID = write.CaptureID
		item.ClientVariantID = binding.ClientVariantID
		item.UpdatedAt = write.UpdatedAt.UTC()
		if variant.AcquisitionState == domain.AcquisitionAvailable && !variant.Digest.Empty() {
			if supplied && (item.Outcome == domain.AssetOutcomeUploaded || item.Outcome == domain.AssetOutcomeAlreadyAvailable) {
				if !item.Digest.Empty() && item.Digest.Value != variant.Digest.Value {
					return CaptureProtocolState{}, &MediaConflictError{Kind: "capture asset outcome", Identity: binding.ClientVariantID, Reason: "reported digest differs from stored variant"}
				}
				if item.ByteSize != 0 && item.ByteSize != variant.ByteSize {
					return CaptureProtocolState{}, &MediaConflictError{Kind: "capture asset outcome", Identity: binding.ClientVariantID, Reason: "reported byte size differs from stored variant"}
				}
			}
			if prior, uploadedByThisCapture := existingByVariant[binding.ClientVariantID]; uploadedByThisCapture && prior.Outcome == domain.AssetOutcomeUploaded {
				item = prior
			} else {
				item.Outcome = domain.AssetOutcomeAlreadyAvailable
			}
			item.CaptureID = write.CaptureID
			item.ClientVariantID = binding.ClientVariantID
			item.UpdatedAt = write.UpdatedAt.UTC()
			item.Digest = variant.Digest
			item.ByteSize = variant.ByteSize
			item.Reason = ""
			item.Retryable = false
		} else if !supplied {
			switch binding.Action {
			case domain.AssetReferenceOnly, domain.AssetServerAcquire:
				item.Outcome = domain.AssetOutcomeDeferred
			case domain.AssetRejected:
				item.Outcome = domain.AssetOutcomeNotAvailable
				item.Reason = binding.Reason
				partial = true
			default:
				item.Outcome = domain.AssetOutcomeNotAvailable
				item.Reason = "client did not report a final transfer outcome"
				partial = true
			}
		} else {
			if item.Outcome == domain.AssetOutcomeUploaded || item.Outcome == domain.AssetOutcomeAlreadyAvailable {
				return CaptureProtocolState{}, &MediaConflictError{Kind: "capture asset outcome", Identity: binding.ClientVariantID, Reason: "client reported availability but FE has no verified bytes"}
			}
			if !item.Outcome.Valid() {
				return CaptureProtocolState{}, fmt.Errorf("complete capture: invalid asset outcome %q", item.Outcome)
			}
			if (item.Outcome == domain.AssetOutcomeNotAvailable || item.Outcome == domain.AssetOutcomeFailed) && strings.TrimSpace(item.Reason) == "" {
				return CaptureProtocolState{}, fmt.Errorf("complete capture: failed/unavailable outcome requires a reason")
			}
			if binding.Action == domain.AssetRequestUpload && item.Outcome != domain.AssetOutcomeUploaded && item.Outcome != domain.AssetOutcomeAlreadyAvailable {
				partial = true
			}
			if binding.Action == domain.AssetRejected {
				partial = true
			}
			if (item.Outcome == domain.AssetOutcomeFailed || item.Outcome == domain.AssetOutcomeNotAvailable) && binding.Action == domain.AssetRequestUpload {
				failure := domain.AssetFailure{Code: "client." + string(item.Outcome), Message: item.Reason, Retryable: item.Retryable}
				if _, err := mediaRepo.MarkVariantFailedOn(ctx, conn, variant.ID, failure, write.UpdatedAt); err != nil {
					return CaptureProtocolState{}, err
				}
			}
		}
		if err := upsertCaptureAssetOutcome(ctx, conn, item); err != nil {
			return CaptureProtocolState{}, err
		}
	}
	if len(reported) != 0 {
		for clientID := range reported {
			return CaptureProtocolState{}, fmt.Errorf("complete capture: client variant %q does not belong to capture", clientID)
		}
	}
	completion := domain.CaptureComplete
	if partial {
		completion = domain.CapturePartial
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO capture_completion_reports (
  capture_id, idempotency_key, semantic_digest, report_json, status_json, completion, created_at
) VALUES (?, ?, ?, ?, '{}', ?, ?)`, write.CaptureID, write.IdempotencyKey,
		write.SemanticDigest, write.ReportJSON, string(completion), formatTime(write.UpdatedAt)); err != nil {
		return CaptureProtocolState{}, fmt.Errorf("insert capture completion report: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE capture_attempts SET completion = ?, warnings_json = ?, updated_at = ?
WHERE capture_id = ?`, string(completion), write.WarningsJSON,
		formatTime(write.UpdatedAt), write.CaptureID); err != nil {
		return CaptureProtocolState{}, fmt.Errorf("finish capture attempt: %w", err)
	}
	attempt.Completion = completion
	attempt.WarningsJSON = write.WarningsJSON
	attempt.UpdatedAt = write.UpdatedAt.UTC()
	state, err := loadProtocolState(ctx, conn, attempt)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	if write.BuildStatusSnapshot != nil {
		raw, err := write.BuildStatusSnapshot(state)
		if err != nil {
			return CaptureProtocolState{}, fmt.Errorf("build capture completion snapshot: %w", err)
		}
		if strings.TrimSpace(raw) == "" {
			return CaptureProtocolState{}, fmt.Errorf("build capture completion snapshot: JSON is required")
		}
		if _, err := conn.ExecContext(ctx, `UPDATE capture_completion_reports SET status_json = ? WHERE capture_id = ?`, raw, write.CaptureID); err != nil {
			return CaptureProtocolState{}, fmt.Errorf("store capture completion snapshot: %w", err)
		}
		state.CompletionStatusJSON = raw
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return CaptureProtocolState{}, fmt.Errorf("commit capture completion: %w", err)
	}
	return state, nil
}

func loadProtocolState(ctx context.Context, conn *sql.Conn, attempt domain.CaptureAttempt) (CaptureProtocolState, error) {
	bindings, err := listCaptureAssetBindings(ctx, conn, attempt.CaptureID)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	outcomes, err := listCaptureAssetOutcomes(ctx, conn, attempt.CaptureID)
	if err != nil {
		return CaptureProtocolState{}, err
	}
	var pending int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_followup_outbox WHERE capture_id = ? AND state IN ('pending', 'processing')`, attempt.CaptureID).Scan(&pending); err != nil {
		return CaptureProtocolState{}, err
	}
	var activeJobs int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrichment_jobs WHERE fragment_revision_id = ? AND status IN ('queued', 'running')`, attempt.FragmentRevisionID).Scan(&activeJobs); err != nil {
		return CaptureProtocolState{}, err
	}
	return CaptureProtocolState{Attempt: attempt, Bindings: bindings, Outcomes: outcomes, EnrichmentInProgress: pending > 0 || activeJobs > 0}, nil
}
