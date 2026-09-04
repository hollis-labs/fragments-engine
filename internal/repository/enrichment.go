package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
)

type EnrichmentRepository struct {
	db *sql.DB
}

func NewEnrichmentRepository(db *sql.DB) *EnrichmentRepository {
	return &EnrichmentRepository{db: db}
}

type EnrichmentConflictError struct {
	Kind   string
	ID     string
	Reason string
}

func (e *EnrichmentConflictError) Error() string {
	return fmt.Sprintf("%s conflict for %q: %s", e.Kind, e.ID, e.Reason)
}

type EnrichmentPlan struct {
	Jobs             []domain.EnrichmentJob
	IdempotentReplay bool
}

type CapabilityRequest struct {
	ID                 string
	IdempotencyKey     string
	SemanticDigest     string
	FragmentRevisionID string
	Capability         domain.EnrichmentCapability
	RequestedBy        string
	Reason             string
	CreatedAt          time.Time
}

type ClaimSpec struct {
	Descriptor provider.Descriptor
	WorkerID   string
	Lease      time.Duration
	Now        time.Time
}

// InitializeCaptureOn persists source/browser/user observations and all known
// capability rows inside the caller-owned manifest transaction.
func InitializeCaptureOn(ctx context.Context, q MediaWriteConn, captureID, fragmentID, revisionID string, observations []domain.EnrichmentObservation, coverage []domain.CapabilityCoverage, now time.Time) ([]domain.CapabilityCoverage, error) {
	if strings.TrimSpace(captureID) == "" || strings.TrimSpace(fragmentID) == "" || strings.TrimSpace(revisionID) == "" || now.IsZero() {
		return nil, fmt.Errorf("initialize capture enrichment: capture, fragment, revision, and timestamp are required")
	}
	for _, observation := range observations {
		observation.CaptureID = captureID
		observation.FragmentID = fragmentID
		observation.FragmentRevisionID = revisionID
		if observation.ID == "" {
			observation.ID = domain.DigestText(strings.Join([]string{"capture-observation", captureID, revisionID, string(observation.Capability), observation.ValueJSON}, "\n"))
		}
		if observation.AssertedAt.IsZero() {
			observation.AssertedAt = now.UTC()
		}
		if observation.CreatedAt.IsZero() {
			observation.CreatedAt = now.UTC()
		}
		if err := insertEnrichmentObservation(ctx, q, observation); err != nil {
			return nil, err
		}
	}
	for _, item := range coverage {
		item.FragmentID = fragmentID
		item.FragmentRevisionID = revisionID
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = now.UTC()
		}
		if item.Version <= 0 {
			item.Version = 1
		}
		if !item.Capability.Valid() || !item.State.Valid() {
			return nil, fmt.Errorf("initialize capture enrichment: invalid %q/%q coverage", item.Capability, item.State)
		}
		initialState := item.State
		if initialState == domain.CoverageProvided {
			initialState = domain.CoverageMissing
		}
		if _, err := q.ExecContext(ctx, `
INSERT INTO fragment_capability_coverage (
  fragment_id, fragment_revision_id, capability, state, selected_observation_id,
  detail, error_class, error_code, error_retryable, requested_generation,
  satisfied_generation, version, updated_at
) VALUES (?, ?, ?, ?, NULL, ?, '', '', 0, 0, 0, ?, ?)
ON CONFLICT(fragment_revision_id, capability) DO NOTHING`, item.FragmentID,
			item.FragmentRevisionID, string(item.Capability), string(initialState), item.Detail,
			item.Version, formatTime(item.UpdatedAt)); err != nil {
			return nil, fmt.Errorf("initialize %s coverage: %w", item.Capability, err)
		}
		if _, err := resolveCoverageOn(ctx, q, revisionID, item.Capability, now, false); err != nil {
			return nil, err
		}
	}
	return listCoverageOn(ctx, q, revisionID)
}

func insertEnrichmentObservation(ctx context.Context, q MediaWriteConn, observation domain.EnrichmentObservation) error {
	if strings.TrimSpace(observation.ID) == "" || strings.TrimSpace(observation.FragmentID) == "" ||
		strings.TrimSpace(observation.FragmentRevisionID) == "" || !observation.Capability.Valid() ||
		!observation.Attribution.ValidDescriptionSource() || strings.TrimSpace(observation.Producer.Adapter) == "" ||
		strings.TrimSpace(observation.Producer.Version) == "" || strings.TrimSpace(observation.InputMaterialDigest) == "" ||
		observation.ObservedAt.IsZero() || observation.AssertedAt.IsZero() || observation.CreatedAt.IsZero() {
		return fmt.Errorf("append enrichment observation: identity, ownership, capability, attribution, producer, input digest, and timestamps are required")
	}
	if observation.Attribution == domain.AttributionUser && strings.TrimSpace(observation.ActorID) == "" {
		return fmt.Errorf("append enrichment observation: user attribution requires actor")
	}
	if observation.Confidence != nil && (*observation.Confidence < 0 || *observation.Confidence > 1) {
		return fmt.Errorf("append enrichment observation: confidence must be between zero and one")
	}
	if !observation.ExpiresAt.IsZero() && observation.ExpiresAt.Before(observation.ObservedAt) {
		return fmt.Errorf("append enrichment observation: expiry cannot precede observation")
	}
	if observation.Attribution == domain.AttributionProvider || observation.Attribution == domain.AttributionDeterministic || observation.Attribution == domain.AttributionModel {
		if err := provider.ValidateCapabilityValue(observation.Capability, []byte(observation.ValueJSON)); err != nil {
			return fmt.Errorf("append enrichment observation: %w", err)
		}
	} else if !json.Valid([]byte(observation.ValueJSON)) {
		return fmt.Errorf("append enrichment observation: source/user value must be valid typed JSON")
	}
	assetJSON, err := json.Marshal(observation.InputAssetDigests)
	if err != nil {
		return fmt.Errorf("encode enrichment input asset digests: %w", err)
	}
	for _, asset := range observation.InputAssetDigests {
		if strings.TrimSpace(asset.AssetVariantID) == "" {
			return fmt.Errorf("append enrichment observation: asset digest requires a variant ID")
		}
		if _, err := asset.Digest.Normalized(); err != nil {
			return fmt.Errorf("append enrichment observation asset %q: %w", asset.AssetVariantID, err)
		}
	}
	var captureID any
	if strings.TrimSpace(observation.CaptureID) != "" {
		captureID = strings.TrimSpace(observation.CaptureID)
	}
	var confidence any
	if observation.Confidence != nil {
		confidence = *observation.Confidence
	}
	expiresAt := ""
	if !observation.ExpiresAt.IsZero() {
		expiresAt = formatTime(observation.ExpiresAt)
	}
	_, err = q.ExecContext(ctx, `
INSERT INTO enrichment_observations (
  id, fragment_id, fragment_revision_id, capture_id, capability,
  attribution_source, producer, producer_version, input_material_digest,
  input_asset_digests_json, value_json, confidence, actor_id, observed_at,
  asserted_at, expires_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		observation.ID, observation.FragmentID, observation.FragmentRevisionID,
		captureID, string(observation.Capability), string(observation.Attribution),
		observation.Producer.Adapter, observation.Producer.Version,
		observation.InputMaterialDigest, string(assetJSON), observation.ValueJSON,
		confidence, observation.ActorID, formatTime(observation.ObservedAt),
		formatTime(observation.AssertedAt), expiresAt, formatTime(observation.CreatedAt))
	if err != nil {
		return fmt.Errorf("append enrichment observation %q: %w", observation.ID, err)
	}
	return nil
}

func (r *EnrichmentRepository) ListCoverage(ctx context.Context, revisionID string, at time.Time) ([]domain.CapabilityCoverage, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	conn, err := beginImmediateConn(ctx, r.db, "refresh capability coverage")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if err := refreshExpiredCoverageOn(ctx, conn, revisionID, at); err != nil {
		return nil, err
	}
	items, err := listCoverageOn(ctx, conn, revisionID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, fmt.Errorf("commit coverage refresh: %w", err)
	}
	return items, nil
}

func listCoverageOn(ctx context.Context, q MediaWriteConn, revisionID string) ([]domain.CapabilityCoverage, error) {
	rows, err := q.QueryContext(ctx, `
SELECT fragment_id, fragment_revision_id, capability, state,
       COALESCE(selected_observation_id, ''), detail, error_class, error_code,
       error_retryable, requested_generation, satisfied_generation, version,
       updated_at
FROM fragment_capability_coverage WHERE fragment_revision_id = ?
ORDER BY CASE capability
  WHEN 'title' THEN 1 WHEN 'description' THEN 2 WHEN 'body' THEN 3
  WHEN 'gallery_manifest' THEN 4 WHEN 'original_media' THEN 5
  WHEN 'thumbnail_or_poster' THEN 6 WHEN 'transcript' THEN 7
  WHEN 'OCR' THEN 8 WHEN 'vision' THEN 9 WHEN 'summary' THEN 10
  WHEN 'tags' THEN 11 WHEN 'entities' THEN 12 ELSE 99 END`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("list capability coverage: %w", err)
	}
	defer rows.Close()
	var out []domain.CapabilityCoverage
	for rows.Next() {
		var item domain.CapabilityCoverage
		var capability, state, errorClass, updatedAt string
		var retryable int
		if err := rows.Scan(&item.FragmentID, &item.FragmentRevisionID, &capability,
			&state, &item.SelectedObservationID, &item.Detail, &errorClass,
			&item.ErrorCode, &retryable, &item.RequestedGeneration,
			&item.SatisfiedGeneration, &item.Version, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan capability coverage: %w", err)
		}
		item.Capability = domain.EnrichmentCapability(capability)
		item.State = domain.CapabilityState(state)
		item.ErrorClass = domain.EnrichmentErrorClass(errorClass)
		item.ErrorRetryable = retryable == 1
		item.UpdatedAt = parseTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *EnrichmentRepository) ListObservations(ctx context.Context, revisionID string, capability domain.EnrichmentCapability) ([]domain.EnrichmentObservation, error) {
	rows, err := r.db.QueryContext(ctx, observationSelect+`
WHERE fragment_revision_id = ? AND capability = ?
ORDER BY asserted_at, id`, revisionID, string(capability))
	if err != nil {
		return nil, fmt.Errorf("list enrichment observations: %w", err)
	}
	defer rows.Close()
	var out []domain.EnrichmentObservation
	for rows.Next() {
		item, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

const observationSelect = `SELECT id, fragment_id, fragment_revision_id,
       COALESCE(capture_id, ''), capability, attribution_source, producer,
       producer_version, input_material_digest, input_asset_digests_json,
       value_json, confidence, actor_id, observed_at, asserted_at, expires_at,
       created_at FROM enrichment_observations `

func scanObservation(scanner rowScanner) (domain.EnrichmentObservation, error) {
	var item domain.EnrichmentObservation
	var capability, attribution, assetJSON, observedAt, assertedAt, expiresAt, createdAt string
	var confidence sql.NullFloat64
	if err := scanner.Scan(&item.ID, &item.FragmentID, &item.FragmentRevisionID,
		&item.CaptureID, &capability, &attribution, &item.Producer.Adapter,
		&item.Producer.Version, &item.InputMaterialDigest, &assetJSON,
		&item.ValueJSON, &confidence, &item.ActorID, &observedAt, &assertedAt,
		&expiresAt, &createdAt); err != nil {
		return domain.EnrichmentObservation{}, fmt.Errorf("scan enrichment observation: %w", err)
	}
	item.Capability = domain.EnrichmentCapability(capability)
	item.Attribution = domain.AttributionSource(attribution)
	if confidence.Valid {
		item.Confidence = &confidence.Float64
	}
	if err := json.Unmarshal([]byte(assetJSON), &item.InputAssetDigests); err != nil {
		return domain.EnrichmentObservation{}, fmt.Errorf("decode enrichment asset digests: %w", err)
	}
	item.ObservedAt, item.AssertedAt, item.CreatedAt = parseTime(observedAt), parseTime(assertedAt), parseTime(createdAt)
	if expiresAt != "" {
		item.ExpiresAt = parseTime(expiresAt)
	}
	return item, nil
}

func resolveCoverageOn(ctx context.Context, q MediaWriteConn, revisionID string, capability domain.EnrichmentCapability, at time.Time, forceProvided bool) (domain.CapabilityCoverage, error) {
	rows, err := q.QueryContext(ctx, observationSelect+`
WHERE fragment_revision_id = ? AND capability = ?`, revisionID, string(capability))
	if err != nil {
		return domain.CapabilityCoverage{}, fmt.Errorf("resolve capability observations: %w", err)
	}
	var active, expired []domain.EnrichmentObservation
	for rows.Next() {
		item, err := scanObservation(rows)
		if err != nil {
			rows.Close()
			return domain.CapabilityCoverage{}, err
		}
		if item.Expired(at) {
			expired = append(expired, item)
		} else {
			active = append(active, item)
		}
	}
	if err := rows.Close(); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	sortObservations(active)
	sortObservations(expired)
	var state domain.CapabilityState
	var selected string
	if len(active) != 0 {
		state, selected = domain.CoverageProvided, active[0].ID
	} else if len(expired) != 0 {
		state, selected = domain.CoverageStale, expired[0].ID
	} else {
		var current string
		if err := q.QueryRowContext(ctx, `SELECT state FROM fragment_capability_coverage WHERE fragment_revision_id = ? AND capability = ?`, revisionID, string(capability)).Scan(&current); err != nil {
			return domain.CapabilityCoverage{}, fmt.Errorf("load unresolved capability: %w", err)
		}
		state = domain.CapabilityState(current)
	}
	if selected != "" && (forceProvided || state == domain.CoverageProvided || state == domain.CoverageStale) {
		if _, err := q.ExecContext(ctx, `
UPDATE fragment_capability_coverage
SET state = ?, selected_observation_id = ?, detail = '', error_class = '',
    error_code = '', error_retryable = 0, version = version + 1, updated_at = ?
WHERE fragment_revision_id = ? AND capability = ?`, string(state), selected,
			formatTime(at), revisionID, string(capability)); err != nil {
			return domain.CapabilityCoverage{}, fmt.Errorf("resolve %s coverage: %w", capability, err)
		}
	}
	return getCoverageOn(ctx, q, revisionID, capability)
}

func sortObservations(items []domain.EnrichmentObservation) {
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if attributionRank(left.Attribution) != attributionRank(right.Attribution) {
			return attributionRank(left.Attribution) > attributionRank(right.Attribution)
		}
		leftConfidence, rightConfidence := -1.0, -1.0
		if left.Confidence != nil {
			leftConfidence = *left.Confidence
		}
		if right.Confidence != nil {
			rightConfidence = *right.Confidence
		}
		if leftConfidence != rightConfidence {
			return leftConfidence > rightConfidence
		}
		if !left.AssertedAt.Equal(right.AssertedAt) {
			return left.AssertedAt.After(right.AssertedAt)
		}
		return left.ID < right.ID
	})
}

func attributionRank(source domain.AttributionSource) int {
	switch source {
	case domain.AttributionUser:
		return 5
	case domain.AttributionSourceMaterial:
		return 4
	case domain.AttributionProvider:
		return 3
	case domain.AttributionDeterministic:
		return 2
	case domain.AttributionModel:
		return 1
	default:
		return 0
	}
}

func getCoverageOn(ctx context.Context, q MediaWriteConn, revisionID string, capability domain.EnrichmentCapability) (domain.CapabilityCoverage, error) {
	var item domain.CapabilityCoverage
	var capValue, state, errorClass, updatedAt string
	var retryable int
	err := q.QueryRowContext(ctx, `
SELECT fragment_id, fragment_revision_id, capability, state,
       COALESCE(selected_observation_id, ''), detail, error_class, error_code,
       error_retryable, requested_generation, satisfied_generation, version,
       updated_at
FROM fragment_capability_coverage WHERE fragment_revision_id = ? AND capability = ?`, revisionID, string(capability)).Scan(
		&item.FragmentID, &item.FragmentRevisionID, &capValue, &state,
		&item.SelectedObservationID, &item.Detail, &errorClass, &item.ErrorCode,
		&retryable, &item.RequestedGeneration, &item.SatisfiedGeneration,
		&item.Version, &updatedAt)
	if err != nil {
		return domain.CapabilityCoverage{}, fmt.Errorf("get capability coverage: %w", err)
	}
	item.Capability, item.State = domain.EnrichmentCapability(capValue), domain.CapabilityState(state)
	item.ErrorClass, item.ErrorRetryable = domain.EnrichmentErrorClass(errorClass), retryable == 1
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func refreshExpiredCoverageOn(ctx context.Context, q MediaWriteConn, revisionID string, at time.Time) error {
	_, err := q.ExecContext(ctx, `
UPDATE fragment_capability_coverage
SET state = 'stale', detail = 'selected observation expired', version = version + 1,
    updated_at = ?
WHERE fragment_revision_id = ? AND state = 'provided'
  AND selected_observation_id IN (
    SELECT id FROM enrichment_observations
    WHERE expires_at <> '' AND expires_at <= ?
  )`, formatTime(at), revisionID, formatTime(at))
	if err != nil {
		return fmt.Errorf("refresh expired capability coverage: %w", err)
	}
	return nil
}

func (r *EnrichmentRepository) RequestCapability(ctx context.Context, request CapabilityRequest) (domain.CapabilityCoverage, bool, error) {
	if strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.SemanticDigest) == "" ||
		strings.TrimSpace(request.FragmentRevisionID) == "" || !request.Capability.Valid() ||
		strings.TrimSpace(request.RequestedBy) == "" || request.CreatedAt.IsZero() {
		return domain.CapabilityCoverage{}, false, fmt.Errorf("request capability: idempotency, revision, capability, actor, and timestamp are required")
	}
	conn, err := beginImmediateConn(ctx, r.db, "request enrichment capability")
	if err != nil {
		return domain.CapabilityCoverage{}, false, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var existingID, existingDigest, existingRevision, existingCapability string
	err = conn.QueryRowContext(ctx, `
SELECT id, semantic_digest, fragment_revision_id, capability
FROM enrichment_capability_requests WHERE idempotency_key = ?`, request.IdempotencyKey).Scan(
		&existingID, &existingDigest, &existingRevision, &existingCapability)
	if err == nil {
		if existingDigest != request.SemanticDigest || existingRevision != request.FragmentRevisionID || existingCapability != string(request.Capability) {
			return domain.CapabilityCoverage{}, false, &EnrichmentConflictError{Kind: "capability request", ID: request.IdempotencyKey, Reason: "idempotency key was reused with different semantics"}
		}
		coverage, err := getCoverageOn(ctx, conn, request.FragmentRevisionID, request.Capability)
		if err != nil {
			return domain.CapabilityCoverage{}, false, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return domain.CapabilityCoverage{}, false, err
		}
		return coverage, true, nil
	}
	if err != sql.ErrNoRows {
		return domain.CapabilityCoverage{}, false, fmt.Errorf("find capability request: %w", err)
	}
	coverage, err := getCoverageOn(ctx, conn, request.FragmentRevisionID, request.Capability)
	if err != nil {
		return domain.CapabilityCoverage{}, false, err
	}
	nextGeneration := coverage.RequestedGeneration + 1
	if request.ID == "" {
		request.ID = domain.DigestText("enrichment-request\n" + request.IdempotencyKey)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO enrichment_capability_requests (
  id, idempotency_key, semantic_digest, fragment_id, fragment_revision_id,
  capability, requested_generation, requested_by, reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.ID, request.IdempotencyKey,
		request.SemanticDigest, coverage.FragmentID, request.FragmentRevisionID,
		string(request.Capability), nextGeneration, request.RequestedBy,
		request.Reason, formatTime(request.CreatedAt)); err != nil {
		return domain.CapabilityCoverage{}, false, fmt.Errorf("insert capability request: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE fragment_capability_coverage
SET requested_generation = ?, version = version + 1, updated_at = ?
WHERE fragment_revision_id = ? AND capability = ?`, nextGeneration,
		formatTime(request.CreatedAt), request.FragmentRevisionID, string(request.Capability)); err != nil {
		return domain.CapabilityCoverage{}, false, fmt.Errorf("advance capability request generation: %w", err)
	}
	coverage, err = getCoverageOn(ctx, conn, request.FragmentRevisionID, request.Capability)
	if err != nil {
		return domain.CapabilityCoverage{}, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.CapabilityCoverage{}, false, err
	}
	return coverage, false, nil
}

func (r *EnrichmentRepository) PlanCaptureFollowUp(ctx context.Context, captureID string, now time.Time) (EnrichmentPlan, error) {
	conn, err := beginImmediateConn(ctx, r.db, "plan capture enrichment")
	if err != nil {
		return EnrichmentPlan{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var revisionID, state string
	if err := conn.QueryRowContext(ctx, `SELECT fragment_revision_id, state FROM capture_followup_outbox WHERE capture_id = ?`, captureID).Scan(&revisionID, &state); err != nil {
		return EnrichmentPlan{}, fmt.Errorf("get capture enrichment outbox: %w", err)
	}
	if state == "complete" {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return EnrichmentPlan{}, err
		}
		return EnrichmentPlan{IdempotentReplay: true}, nil
	}
	if _, err := conn.ExecContext(ctx, `UPDATE capture_followup_outbox SET state = 'processing', updated_at = ? WHERE capture_id = ?`, formatTime(now), captureID); err != nil {
		return EnrichmentPlan{}, fmt.Errorf("claim capture enrichment outbox: %w", err)
	}
	jobs, err := planRevisionOn(ctx, conn, revisionID, now, true)
	if err != nil {
		return EnrichmentPlan{}, err
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE capture_followup_outbox
SET state = 'complete', planned_at = ?, updated_at = ? WHERE capture_id = ?`,
		formatTime(now), formatTime(now), captureID); err != nil {
		return EnrichmentPlan{}, fmt.Errorf("complete capture enrichment outbox: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return EnrichmentPlan{}, fmt.Errorf("commit capture enrichment plan: %w", err)
	}
	return EnrichmentPlan{Jobs: jobs}, nil
}

func (r *EnrichmentRepository) PlanRevision(ctx context.Context, revisionID string, now time.Time) ([]domain.EnrichmentJob, error) {
	conn, err := beginImmediateConn(ctx, r.db, "plan revision enrichment")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	jobs, err := planRevisionOn(ctx, conn, revisionID, now, false)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, fmt.Errorf("commit revision enrichment plan: %w", err)
	}
	return jobs, nil
}

func planRevisionOn(ctx context.Context, q MediaWriteConn, revisionID string, now time.Time, fromCapture bool) ([]domain.EnrichmentJob, error) {
	if err := refreshExpiredCoverageOn(ctx, q, revisionID, now); err != nil {
		return nil, err
	}
	coverage, err := listCoverageOn(ctx, q, revisionID)
	if err != nil {
		return nil, err
	}
	var jobs []domain.EnrichmentJob
	for _, item := range coverage {
		explicit := item.ExplicitRequestPending()
		eligible := explicit || item.State == domain.CoverageMissing || item.State == domain.CoverageStale ||
			(item.State == domain.CoverageFailed && item.ErrorRetryable)
		if !eligible || item.State == domain.CoveragePending {
			continue
		}
		trigger := domain.EnrichmentTriggerMissing
		switch {
		case explicit:
			trigger = domain.EnrichmentTriggerExplicit
		case item.State == domain.CoverageStale:
			trigger = domain.EnrichmentTriggerStale
		case item.State == domain.CoverageFailed:
			trigger = domain.EnrichmentTriggerRetry
		case fromCapture:
			trigger = domain.EnrichmentTriggerCapture
		}
		job := domain.EnrichmentJob{FragmentID: item.FragmentID, FragmentRevisionID: revisionID,
			Capability: item.Capability, Trigger: trigger, CoverageVersion: item.Version,
			RequestGeneration: item.RequestedGeneration, Status: domain.EnrichmentJobQueued,
			CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
		job.ID = domain.DigestText(fmt.Sprintf("enrichment-job\n%s\n%s\n%d\n%d", revisionID, item.Capability, item.Version, item.RequestedGeneration))
		res, err := q.ExecContext(ctx, `
INSERT INTO enrichment_jobs (
  id, fragment_id, fragment_revision_id, capability, trigger_kind,
  coverage_version, request_generation, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?)
ON CONFLICT(fragment_revision_id, capability, coverage_version, request_generation) DO NOTHING`,
			job.ID, job.FragmentID, job.FragmentRevisionID, string(job.Capability),
			string(job.Trigger), job.CoverageVersion, job.RequestGeneration,
			formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
		if err != nil {
			return nil, fmt.Errorf("plan %s enrichment: %w", item.Capability, err)
		}
		inserted, _ := res.RowsAffected()
		if inserted == 0 {
			continue
		}
		if _, err := q.ExecContext(ctx, `
UPDATE fragment_capability_coverage
SET state = 'pending', detail = ?, error_class = '', error_code = '',
    error_retryable = 0, version = version + 1, updated_at = ?
WHERE fragment_revision_id = ? AND capability = ? AND version = ?`,
			"enrichment queued", formatTime(now), revisionID, string(item.Capability), item.Version); err != nil {
			return nil, fmt.Errorf("mark %s enrichment pending: %w", item.Capability, err)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (r *EnrichmentRepository) ClaimNext(ctx context.Context, spec ClaimSpec) (domain.EnrichmentClaim, bool, error) {
	if err := spec.Descriptor.Validate(); err != nil {
		return domain.EnrichmentClaim{}, false, err
	}
	if strings.TrimSpace(spec.WorkerID) == "" || spec.Lease <= 0 || spec.Now.IsZero() {
		return domain.EnrichmentClaim{}, false, fmt.Errorf("claim enrichment: worker, positive lease, and timestamp are required")
	}
	conn, err := beginImmediateConn(ctx, r.db, "claim enrichment job")
	if err != nil {
		return domain.EnrichmentClaim{}, false, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if err := releaseExpiredClaimsOn(ctx, conn, spec.Now); err != nil {
		return domain.EnrichmentClaim{}, false, err
	}
	rows, err := conn.QueryContext(ctx, `
SELECT j.id, j.fragment_id, j.fragment_revision_id, j.capability,
       j.trigger_kind, j.coverage_version, j.request_generation, j.status,
       j.attempt_count, j.created_at, j.updated_at, f.source_type, si.provider
FROM enrichment_jobs j
JOIN fragments f ON f.id = j.fragment_id
JOIN fragment_source_identities si ON si.fragment_id = j.fragment_id
WHERE j.status = 'queued' ORDER BY j.created_at, j.id`)
	if err != nil {
		return domain.EnrichmentClaim{}, false, fmt.Errorf("list claimable enrichment jobs: %w", err)
	}
	type candidate struct {
		job                      domain.EnrichmentJob
		sourceType, providerName string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		var capability, trigger, status, createdAt, updatedAt string
		if err := rows.Scan(&item.job.ID, &item.job.FragmentID, &item.job.FragmentRevisionID,
			&capability, &trigger, &item.job.CoverageVersion, &item.job.RequestGeneration,
			&status, &item.job.AttemptCount, &createdAt, &updatedAt,
			&item.sourceType, &item.providerName); err != nil {
			rows.Close()
			return domain.EnrichmentClaim{}, false, err
		}
		item.job.Capability, item.job.Trigger, item.job.Status = domain.EnrichmentCapability(capability), domain.EnrichmentJobTrigger(trigger), domain.EnrichmentJobStatus(status)
		item.job.CreatedAt, item.job.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return domain.EnrichmentClaim{}, false, err
	}
	var selected domain.EnrichmentJob
	for _, candidate := range candidates {
		mediaKinds, err := listRevisionMediaKindsOn(ctx, conn, candidate.job.FragmentRevisionID)
		if err != nil {
			return domain.EnrichmentClaim{}, false, err
		}
		if spec.Descriptor.Supports(candidate.job.Capability, candidate.providerName, candidate.sourceType, mediaKinds) {
			selected = candidate.job
			break
		}
	}
	if selected.ID == "" {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return domain.EnrichmentClaim{}, false, err
		}
		return domain.EnrichmentClaim{}, false, nil
	}
	selected.AttemptCount++
	selected.ClaimedBy = spec.WorkerID
	selected.ClaimToken = domain.DigestText(fmt.Sprintf("enrichment-claim\n%s\n%d\n%s\n%s", selected.ID, selected.AttemptCount, spec.WorkerID, formatTime(spec.Now)))
	selected.LeaseExpiresAt = spec.Now.Add(spec.Lease).UTC()
	selected.Status, selected.UpdatedAt = domain.EnrichmentJobRunning, spec.Now.UTC()
	res, err := conn.ExecContext(ctx, `
UPDATE enrichment_jobs
SET status = 'running', attempt_count = ?, claim_token = ?, claimed_by = ?,
    lease_expires_at = ?, updated_at = ?
WHERE id = ? AND status = 'queued'`, selected.AttemptCount, selected.ClaimToken,
		spec.WorkerID, formatTime(selected.LeaseExpiresAt), formatTime(spec.Now), selected.ID)
	if err != nil {
		return domain.EnrichmentClaim{}, false, fmt.Errorf("claim enrichment job: %w", err)
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		return domain.EnrichmentClaim{}, false, &EnrichmentConflictError{Kind: "enrichment claim", ID: selected.ID, Reason: "job was claimed concurrently"}
	}
	fragment, err := getFragmentByID(ctx, conn, selected.FragmentID)
	if err != nil {
		return domain.EnrichmentClaim{}, false, err
	}
	revision, err := loadRevisionOn(ctx, conn, selected.FragmentRevisionID)
	if err != nil {
		return domain.EnrichmentClaim{}, false, err
	}
	assetDigests, err := listRevisionAssetDigestsOn(ctx, conn, selected.FragmentRevisionID)
	if err != nil {
		return domain.EnrichmentClaim{}, false, err
	}
	descriptorJSON, _ := json.Marshal(spec.Descriptor)
	effectsJSON, _ := json.Marshal(spec.Descriptor.Effects)
	credentialsJSON, _ := json.Marshal(spec.Descriptor.CredentialReferences)
	assetJSON, _ := json.Marshal(assetDigests)
	attempt := domain.EnrichmentJobAttempt{ID: domain.DigestText(fmt.Sprintf("enrichment-attempt\n%s\n%d", selected.ID, selected.AttemptCount)),
		JobID: selected.ID, AttemptNumber: selected.AttemptCount, ClaimToken: selected.ClaimToken,
		Adapter:       domain.AdapterVersion{Adapter: spec.Descriptor.Adapter, Version: spec.Descriptor.Version},
		InputSchemaID: spec.Descriptor.InputSchema.ID, InputSchemaVersion: spec.Descriptor.InputSchema.Version,
		OutputSchemaID: spec.Descriptor.OutputSchema.ID, OutputSchemaVersion: spec.Descriptor.OutputSchema.Version,
		NetworkClass: string(spec.Descriptor.NetworkClass), EffectsJSON: string(effectsJSON),
		CredentialRefsJSON: string(credentialsJSON), DescriptorJSON: string(descriptorJSON),
		InputMaterialDigest: revision.MaterialDigest, InputAssetDigestsJSON: string(assetJSON),
		Status: domain.EnrichmentJobRunning, ClaimedAt: spec.Now.UTC()}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO enrichment_job_attempts (
  id, job_id, fragment_revision_id, capability, attempt_number, claim_token,
  adapter, adapter_version, input_schema_id, input_schema_version,
  output_schema_id, output_schema_version, network_class, effects_json,
  credential_refs_json, descriptor_json, input_material_digest,
  input_asset_digests_json, status, claimed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?)`,
		attempt.ID, attempt.JobID, selected.FragmentRevisionID, string(selected.Capability),
		attempt.AttemptNumber, attempt.ClaimToken, attempt.Adapter.Adapter,
		attempt.Adapter.Version, attempt.InputSchemaID, attempt.InputSchemaVersion,
		attempt.OutputSchemaID, attempt.OutputSchemaVersion, attempt.NetworkClass,
		attempt.EffectsJSON, attempt.CredentialRefsJSON, attempt.DescriptorJSON,
		attempt.InputMaterialDigest, attempt.InputAssetDigestsJSON, formatTime(attempt.ClaimedAt)); err != nil {
		return domain.EnrichmentClaim{}, false, fmt.Errorf("insert enrichment job attempt: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.EnrichmentClaim{}, false, fmt.Errorf("commit enrichment claim: %w", err)
	}
	return domain.EnrichmentClaim{Job: selected, Attempt: attempt, Source: fragment.SourceIdentity, Material: revision, AssetDigests: assetDigests}, true, nil
}

func releaseExpiredClaimsOn(ctx context.Context, q MediaWriteConn, now time.Time) error {
	if _, err := q.ExecContext(ctx, `
UPDATE enrichment_job_attempts
SET status = 'failed', error_class = 'retryable', error_code = 'lease_expired',
    error_message = 'worker lease expired before completion', completed_at = ?
WHERE status = 'running' AND job_id IN (
  SELECT id FROM enrichment_jobs
  WHERE status = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?
)`, formatTime(now), formatTime(now)); err != nil {
		return fmt.Errorf("expire enrichment attempts: %w", err)
	}
	if _, err := q.ExecContext(ctx, `
UPDATE enrichment_jobs
SET status = 'queued', claim_token = '', claimed_by = '', lease_expires_at = '',
    updated_at = ?
WHERE status = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?`,
		formatTime(now), formatTime(now)); err != nil {
		return fmt.Errorf("release expired enrichment claims: %w", err)
	}
	return nil
}

func listRevisionMediaKindsOn(ctx context.Context, q MediaWriteConn, revisionID string) ([]domain.MediaKind, error) {
	rows, err := q.QueryContext(ctx, `
SELECT DISTINCT m.kind FROM attachment_refs a
JOIN media_assets m ON m.id = a.media_asset_id
WHERE a.fragment_revision_id = ? ORDER BY m.kind`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MediaKind
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		out = append(out, domain.MediaKind(kind))
	}
	return out, rows.Err()
}

func listRevisionAssetDigestsOn(ctx context.Context, q MediaWriteConn, revisionID string) ([]domain.ObservationAssetDigest, error) {
	rows, err := q.QueryContext(ctx, `
SELECT v.id, v.digest FROM attachment_refs a
JOIN asset_variants v ON v.media_asset_id = a.media_asset_id
WHERE a.fragment_revision_id = ? AND v.digest <> ''
ORDER BY v.id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ObservationAssetDigest
	for rows.Next() {
		var item domain.ObservationAssetDigest
		if err := rows.Scan(&item.AssetVariantID, &item.Digest.Value); err != nil {
			return nil, err
		}
		item.Digest.Algorithm = "sha256"
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadRevisionOn(ctx context.Context, q MediaWriteConn, revisionID string) (domain.FragmentRevision, error) {
	revision, err := scanRevision(q.QueryRowContext(ctx, `
SELECT id, fragment_id, ordinal, material_digest, content_digest, title,
       description, content, content_format, ordered_media_digest, metadata_json,
       normalizer_adapter, normalizer_version, observed_at, committed_at,
       COALESCE(legacy_fragment_id, '')
FROM fragment_revisions WHERE id = ?`, revisionID))
	if err != nil {
		return domain.FragmentRevision{}, fmt.Errorf("load enrichment revision: %w", err)
	}
	return revision, nil
}

func (r *EnrichmentRepository) CompleteSuccess(ctx context.Context, claimToken string, observation domain.EnrichmentObservation, now time.Time) (domain.CapabilityCoverage, error) {
	conn, err := beginImmediateConn(ctx, r.db, "complete enrichment success")
	if err != nil {
		return domain.CapabilityCoverage{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	job, attempt, err := loadRunningClaimOn(ctx, conn, claimToken)
	if err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if !job.LeaseExpiresAt.After(now) {
		return domain.CapabilityCoverage{}, &EnrichmentConflictError{Kind: "enrichment result", ID: claimToken, Reason: "claim lease expired"}
	}
	if observation.Capability != job.Capability || observation.Producer.Adapter != attempt.Adapter.Adapter || observation.Producer.Version != attempt.Adapter.Version {
		return domain.CapabilityCoverage{}, &EnrichmentConflictError{Kind: "enrichment result", ID: claimToken, Reason: "capability or producer differs from claimed descriptor"}
	}
	observation.FragmentID, observation.FragmentRevisionID = job.FragmentID, job.FragmentRevisionID
	observation.CaptureID = ""
	observation.InputMaterialDigest = attempt.InputMaterialDigest
	if err := json.Unmarshal([]byte(attempt.InputAssetDigestsJSON), &observation.InputAssetDigests); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if observation.ID == "" {
		observation.ID = domain.DigestText("enrichment-observation\n" + attempt.ID + "\n" + observation.ValueJSON)
	}
	if observation.AssertedAt.IsZero() {
		observation.AssertedAt = now.UTC()
	}
	if observation.CreatedAt.IsZero() {
		observation.CreatedAt = now.UTC()
	}
	if err := insertEnrichmentObservation(ctx, conn, observation); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE enrichment_job_attempts SET status = 'succeeded', completed_at = ?
WHERE claim_token = ? AND status = 'running'`, formatTime(now), claimToken); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE enrichment_jobs SET status = 'succeeded', claim_token = '', claimed_by = '',
  lease_expires_at = '', updated_at = ? WHERE id = ? AND status = 'running'`,
		formatTime(now), job.ID); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE fragment_capability_coverage
SET satisfied_generation = CASE WHEN satisfied_generation < ? THEN ? ELSE satisfied_generation END,
    version = version + 1, updated_at = ?
WHERE fragment_revision_id = ? AND capability = ?`, job.RequestGeneration,
		job.RequestGeneration, formatTime(now), job.FragmentRevisionID, string(job.Capability)); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	coverage, err := resolveCoverageOn(ctx, conn, job.FragmentRevisionID, job.Capability, now, true)
	if err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if coverage.RequestedGeneration > job.RequestGeneration {
		if _, _, err := enqueueExplicitSuccessorOn(ctx, conn, coverage, now); err != nil {
			return domain.CapabilityCoverage{}, err
		}
		coverage, err = getCoverageOn(ctx, conn, job.FragmentRevisionID, job.Capability)
		if err != nil {
			return domain.CapabilityCoverage{}, err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	return coverage, nil
}

func (r *EnrichmentRepository) CompleteFailure(ctx context.Context, claimToken string, failure domain.EnrichmentFailure, now time.Time) (domain.CapabilityCoverage, error) {
	if err := failure.Validate(); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	conn, err := beginImmediateConn(ctx, r.db, "complete enrichment failure")
	if err != nil {
		return domain.CapabilityCoverage{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	job, _, err := loadRunningClaimOn(ctx, conn, claimToken)
	if err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if !job.LeaseExpiresAt.After(now) {
		return domain.CapabilityCoverage{}, &EnrichmentConflictError{Kind: "enrichment result", ID: claimToken, Reason: "claim lease expired"}
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE enrichment_job_attempts SET status = 'failed', error_class = ?,
  error_code = ?, error_message = ?, completed_at = ?
WHERE claim_token = ? AND status = 'running'`, string(failure.Class), failure.Code,
		failure.Message, formatTime(now), claimToken); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE enrichment_jobs SET status = 'failed', claim_token = '', claimed_by = '',
  lease_expires_at = '', updated_at = ? WHERE id = ? AND status = 'running'`,
		formatTime(now), job.ID); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	satisfied := 0
	if failure.Class == domain.EnrichmentErrorPermanent {
		satisfied = job.RequestGeneration
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE fragment_capability_coverage
SET state = 'failed', detail = ?, error_class = ?, error_code = ?,
    error_retryable = ?,
    satisfied_generation = CASE WHEN satisfied_generation < ? THEN ? ELSE satisfied_generation END,
    version = version + 1, updated_at = ?
WHERE fragment_revision_id = ? AND capability = ?`, failure.Message,
		string(failure.Class), failure.Code, boolInt(failure.Class == domain.EnrichmentErrorRetryable),
		satisfied, satisfied, formatTime(now), job.FragmentRevisionID, string(job.Capability)); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	coverage, err := getCoverageOn(ctx, conn, job.FragmentRevisionID, job.Capability)
	if err != nil {
		return domain.CapabilityCoverage{}, err
	}
	if coverage.RequestedGeneration > job.RequestGeneration {
		if _, _, err := enqueueExplicitSuccessorOn(ctx, conn, coverage, now); err != nil {
			return domain.CapabilityCoverage{}, err
		}
		coverage, err = getCoverageOn(ctx, conn, job.FragmentRevisionID, job.Capability)
		if err != nil {
			return domain.CapabilityCoverage{}, err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	return coverage, nil
}

func enqueueExplicitSuccessorOn(ctx context.Context, q MediaWriteConn, coverage domain.CapabilityCoverage, now time.Time) (domain.EnrichmentJob, bool, error) {
	job := domain.EnrichmentJob{FragmentID: coverage.FragmentID, FragmentRevisionID: coverage.FragmentRevisionID,
		Capability: coverage.Capability, Trigger: domain.EnrichmentTriggerExplicit,
		CoverageVersion: coverage.Version, RequestGeneration: coverage.RequestedGeneration,
		Status: domain.EnrichmentJobQueued, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	job.ID = domain.DigestText(fmt.Sprintf("enrichment-job\n%s\n%s\n%d\n%d", coverage.FragmentRevisionID, coverage.Capability, coverage.Version, coverage.RequestedGeneration))
	result, err := q.ExecContext(ctx, `
INSERT INTO enrichment_jobs (
  id, fragment_id, fragment_revision_id, capability, trigger_kind,
  coverage_version, request_generation, status, created_at, updated_at
) VALUES (?, ?, ?, ?, 'explicit', ?, ?, 'queued', ?, ?)
ON CONFLICT(fragment_revision_id, capability, coverage_version, request_generation) DO NOTHING`,
		job.ID, job.FragmentID, job.FragmentRevisionID, string(job.Capability),
		job.CoverageVersion, job.RequestGeneration, formatTime(now), formatTime(now))
	if err != nil {
		return domain.EnrichmentJob{}, false, fmt.Errorf("queue explicit successor for %s: %w", coverage.Capability, err)
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		return job, false, nil
	}
	if _, err := q.ExecContext(ctx, `
UPDATE fragment_capability_coverage
SET state = 'pending', detail = 'explicit enrichment refresh queued',
    error_class = '', error_code = '', error_retryable = 0,
    version = version + 1, updated_at = ?
WHERE fragment_revision_id = ? AND capability = ? AND version = ?`,
		formatTime(now), coverage.FragmentRevisionID, string(coverage.Capability), coverage.Version); err != nil {
		return domain.EnrichmentJob{}, false, fmt.Errorf("mark explicit successor pending: %w", err)
	}
	return job, true, nil
}

func loadRunningClaimOn(ctx context.Context, q MediaWriteConn, claimToken string) (domain.EnrichmentJob, domain.EnrichmentJobAttempt, error) {
	var job domain.EnrichmentJob
	var attempt domain.EnrichmentJobAttempt
	var capability, trigger, jobStatus, leaseExpiresAt, createdAt, updatedAt, attemptStatus, claimedAt string
	err := q.QueryRowContext(ctx, `
SELECT j.id, j.fragment_id, j.fragment_revision_id, j.capability, j.trigger_kind,
       j.coverage_version, j.request_generation, j.status, j.attempt_count,
       j.claim_token, j.claimed_by, j.lease_expires_at, j.created_at, j.updated_at,
       a.id, a.attempt_number, a.adapter, a.adapter_version,
       a.input_schema_id, a.input_schema_version, a.output_schema_id,
       a.output_schema_version, a.network_class, a.effects_json,
       a.credential_refs_json, a.descriptor_json, a.input_material_digest,
       a.input_asset_digests_json, a.status, a.claimed_at
FROM enrichment_jobs j JOIN enrichment_job_attempts a ON a.job_id = j.id
WHERE j.claim_token = ? AND j.status = 'running' AND a.claim_token = ? AND a.status = 'running'`, claimToken, claimToken).Scan(
		&job.ID, &job.FragmentID, &job.FragmentRevisionID, &capability, &trigger,
		&job.CoverageVersion, &job.RequestGeneration, &jobStatus, &job.AttemptCount,
		&job.ClaimToken, &job.ClaimedBy, &leaseExpiresAt, &createdAt, &updatedAt,
		&attempt.ID, &attempt.AttemptNumber, &attempt.Adapter.Adapter, &attempt.Adapter.Version,
		&attempt.InputSchemaID, &attempt.InputSchemaVersion, &attempt.OutputSchemaID,
		&attempt.OutputSchemaVersion, &attempt.NetworkClass, &attempt.EffectsJSON,
		&attempt.CredentialRefsJSON, &attempt.DescriptorJSON, &attempt.InputMaterialDigest,
		&attempt.InputAssetDigestsJSON, &attemptStatus, &claimedAt)
	if err != nil {
		return domain.EnrichmentJob{}, domain.EnrichmentJobAttempt{}, fmt.Errorf("load running enrichment claim: %w", err)
	}
	job.Capability, job.Trigger, job.Status = domain.EnrichmentCapability(capability), domain.EnrichmentJobTrigger(trigger), domain.EnrichmentJobStatus(jobStatus)
	job.CreatedAt, job.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	if leaseExpiresAt != "" {
		job.LeaseExpiresAt = parseTime(leaseExpiresAt)
	}
	attempt.JobID, attempt.ClaimToken, attempt.Status = job.ID, claimToken, domain.EnrichmentJobStatus(attemptStatus)
	attempt.ClaimedAt = parseTime(claimedAt)
	return job, attempt, nil
}
