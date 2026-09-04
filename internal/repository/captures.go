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

type CaptureRepository struct {
	db *sql.DB
}

func NewCaptureRepository(db *sql.DB) *CaptureRepository {
	return &CaptureRepository{db: db}
}

// CaptureWrite is the transaction-ready form of one accepted capture. The
// service layer normalizes it and calculates the semantic digest before this
// repository crosses the persistence boundary.
type CaptureWrite struct {
	Fragment               domain.Fragment
	Attempt                domain.CaptureAttempt
	Inbox                  *CaptureInboxWrite
	Annotations            []domain.CaptureAnnotation
	Tags                   []domain.AttributedTag
	Descriptions           []domain.DescriptionObservation
	Media                  []domain.MediaManifestItem
	AssetBindings          []domain.CaptureAssetBinding
	EnrichmentObservations []domain.EnrichmentObservation
	CapabilityCoverage     []domain.CapabilityCoverage
	FollowUpKind           string
	FollowUpPayloadJSON    string
	// LegacyProjection is an optional, narrowly typed compatibility projection.
	// The repository applies it after canonical resolution and before snapshot
	// construction/commit. It cannot change identity or immutable revision rows.
	LegacyProjection *LegacyProjectionWrite
	// BuildAcceptanceSnapshot runs after every row required by manifest
	// acceptance has been written, but before COMMIT. It lets the application
	// persist an exact transport response without moving transaction ownership
	// out of the repository. A projection/validation failure therefore rolls
	// back the fragment, context, media, bindings, and outbox together.
	BuildAcceptanceSnapshot func(domain.CaptureAcceptance) (string, error)
}

// CaptureInboxWrite requests initial inbox staging as part of the same atomic
// transaction that accepts a capture. It is explicit so legacy intake can keep
// running its existing routing and inbox pipeline after capture persistence.
type CaptureInboxWrite struct {
	Reason   string
	StagedAt time.Time
}

// CaptureConflictError reports reuse of a capture ID, idempotency key, or
// append-only occurrence ID with different semantics.
type CaptureConflictError struct {
	CaptureID      string
	IdempotencyKey string
	Reason         string
	Existing       domain.CaptureAttempt
}

func (e *CaptureConflictError) Error() string {
	return fmt.Sprintf("capture conflict for %q: %s", e.CaptureID, e.Reason)
}

// CaptureCompletionConflictError reports a non-monotonic attempt-state change.
type CaptureCompletionConflictError struct {
	CaptureID string
	Current   domain.CaptureCompletion
	Requested domain.CaptureCompletion
}

func (e *CaptureCompletionConflictError) Error() string {
	return fmt.Sprintf("capture %q completion cannot change from %s to %s", e.CaptureID, e.Current, e.Requested)
}

// CuratedNoteConflictError carries the authoritative note state back to an
// optimistic caller. Revision zero represents an absent note.
type CuratedNoteConflictError struct {
	Expected int
	Current  domain.CuratedNote
}

func (e *CuratedNoteConflictError) Error() string {
	return fmt.Sprintf("curated note revision conflict: expected %d, current %d", e.Expected, e.Current.Revision)
}

// Accept atomically resolves/creates the stable Fragment and immutable
// FragmentRevision and records the attempt plus all additive context. An exact
// retry is read-only and returns the immutable outcome snapshot stored on the
// first acceptance.
func (r *CaptureRepository) Accept(ctx context.Context, write CaptureWrite) (domain.CaptureAcceptance, error) {
	if err := validateCaptureWrite(write); err != nil {
		return domain.CaptureAcceptance{}, err
	}

	conn, err := beginImmediateConn(ctx, r.db, "capture acceptance")
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)

	existing, found, err := findAttemptByCaptureOrKey(ctx, conn, write.Attempt.CaptureID, write.Attempt.IdempotencyKey)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	if found {
		if existing.CaptureID != write.Attempt.CaptureID {
			return domain.CaptureAcceptance{}, &CaptureConflictError{
				CaptureID: write.Attempt.CaptureID, IdempotencyKey: write.Attempt.IdempotencyKey,
				Reason: "idempotency key is already bound to another capture ID", Existing: existing,
			}
		}
		if existing.IdempotencyKey != write.Attempt.IdempotencyKey {
			return domain.CaptureAcceptance{}, &CaptureConflictError{
				CaptureID: write.Attempt.CaptureID, IdempotencyKey: write.Attempt.IdempotencyKey,
				Reason: "capture ID is already bound to another idempotency key", Existing: existing,
			}
		}
		if existing.SemanticDigest != write.Attempt.SemanticDigest {
			return domain.CaptureAcceptance{}, &CaptureConflictError{
				CaptureID: write.Attempt.CaptureID, IdempotencyKey: write.Attempt.IdempotencyKey,
				Reason: "capture ID was reused with a different semantic payload", Existing: existing,
			}
		}
		accepted, err := loadCaptureAcceptance(ctx, conn, existing, true)
		if err != nil {
			return domain.CaptureAcceptance{}, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("commit capture replay: %w", err)
		}
		return accepted, nil
	}

	// Occurrence identifiers are global. Reusing one in a new capture would
	// erase its provenance if treated as an ordinary insert conflict.
	for _, annotation := range write.Annotations {
		var captureID string
		err := conn.QueryRowContext(ctx, `SELECT capture_id FROM capture_annotations WHERE id = ?`, annotation.ID).Scan(&captureID)
		if err == nil {
			return domain.CaptureAcceptance{}, &CaptureConflictError{
				CaptureID: write.Attempt.CaptureID, IdempotencyKey: write.Attempt.IdempotencyKey,
				Reason: fmt.Sprintf("annotation ID %q is already bound to capture %q", annotation.ID, captureID),
			}
		}
		if err != sql.ErrNoRows {
			return domain.CaptureAcceptance{}, fmt.Errorf("check capture annotation identity: %w", err)
		}
	}

	fragment, outcome, err := upsertFragmentResolved(ctx, conn, write.Fragment)
	if err != nil {
		return domain.CaptureAcceptance{}, fmt.Errorf("accept capture fragment: %w", err)
	}
	revisionID := fragment.Revision.ID
	if revisionID == "" {
		return domain.CaptureAcceptance{}, fmt.Errorf("accept capture: resolved revision is missing")
	}

	attempt := write.Attempt
	attempt.FragmentID = fragment.ID
	attempt.FragmentRevisionID = revisionID
	attempt.FragmentOutcome = string(outcome)
	if attempt.ID == "" {
		attempt.ID = domain.DigestText("capture-attempt\n" + attempt.CaptureID)
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) + 1 FROM capture_attempts WHERE fragment_id = ?`, fragment.ID).Scan(&attempt.AcceptedCaptureCount); err != nil {
		return domain.CaptureAcceptance{}, fmt.Errorf("count accepted captures: %w", err)
	}
	snapshot := struct {
		SchemaVersion      string `json:"schema_version"`
		CaptureID          string `json:"capture_id"`
		FragmentID         string `json:"fragment_id"`
		FragmentRevisionID string `json:"fragment_revision_id"`
		CaptureAttemptID   string `json:"capture_attempt_id"`
		FragmentOutcome    string `json:"fragment_outcome"`
		CaptureCount       int    `json:"capture_count"`
		Completion         string `json:"completion"`
	}{
		SchemaVersion: "fe.capture.acceptance.v1", CaptureID: attempt.CaptureID,
		FragmentID: fragment.ID, FragmentRevisionID: revisionID,
		CaptureAttemptID: attempt.ID, FragmentOutcome: attempt.FragmentOutcome,
		CaptureCount: attempt.AcceptedCaptureCount, Completion: string(attempt.Completion),
	}
	rawSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return domain.CaptureAcceptance{}, fmt.Errorf("encode capture acceptance snapshot: %w", err)
	}
	attempt.AcceptanceResultJSON = string(rawSnapshot)
	if err := insertCaptureAttempt(ctx, conn, attempt); err != nil {
		return domain.CaptureAcceptance{}, err
	}

	for _, annotation := range write.Annotations {
		annotation.CaptureID = attempt.CaptureID
		annotation.FragmentID = fragment.ID
		selectorJSON := []byte(`{}`)
		if annotation.Selector != nil {
			selectorJSON, err = json.Marshal(annotation.Selector)
			if err != nil {
				return domain.CaptureAcceptance{}, fmt.Errorf("encode capture annotation %q selector: %w", annotation.ID, err)
			}
		}
		positionJSON := []byte(`{}`)
		if annotation.Position != nil {
			positionJSON, err = json.Marshal(annotation.Position)
			if err != nil {
				return domain.CaptureAcceptance{}, fmt.Errorf("encode capture annotation %q position: %w", annotation.ID, err)
			}
		}
		if _, err := conn.ExecContext(ctx, `
INSERT INTO capture_annotations (
  id, capture_id, fragment_id, kind, text, selector_json, position_json,
  actor_id, captured_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			annotation.ID, annotation.CaptureID, annotation.FragmentID,
			string(annotation.Kind), annotation.Text, string(selectorJSON),
			string(positionJSON), annotation.ActorID,
			formatTime(annotation.CapturedAt), formatTime(annotation.CreatedAt),
		); err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("insert capture annotation %q: %w", annotation.ID, err)
		}
	}

	for _, tag := range write.Tags {
		tag.FragmentID = fragment.ID
		tag.CaptureID = attempt.CaptureID
		if err := insertCaptureTag(ctx, conn, tag); err != nil {
			return domain.CaptureAcceptance{}, err
		}
	}

	for _, description := range write.Descriptions {
		description.FragmentID = fragment.ID
		description.FragmentRevisionID = revisionID
		description.CaptureID = attempt.CaptureID
		if _, err := conn.ExecContext(ctx, `
INSERT INTO fragment_description_observations (
  id, fragment_id, fragment_revision_id, capture_id, value,
  attribution_source, producer, producer_version, actor_id, observed_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			description.ID, description.FragmentID, description.FragmentRevisionID,
			description.CaptureID, description.Value, string(description.Source),
			description.Producer.Adapter, description.Producer.Version,
			description.ActorID, formatTime(description.ObservedAt), formatTime(description.CreatedAt),
		); err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("insert description observation %q: %w", description.ID, err)
		}
	}
	// Legacy rows must exist before canonical media inserts because the media
	// compatibility foreign keys may point back to legacy attachment IDs.
	if write.LegacyProjection != nil {
		if err := applyLegacyProjectionOn(ctx, conn, fragment, *write.LegacyProjection, attempt.CreatedAt); err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("accept capture legacy projection: %w", err)
		}
	}

	var resolvedMedia []domain.MediaManifestItem
	if write.Media != nil {
		for index := range write.Media {
			if strings.TrimSpace(write.Media[index].Attachment.LegacyAttachmentID) != "" {
				write.Media[index].Attachment.LegacyFragmentID = fragment.ID
			}
		}
		resolvedMedia, err = UpsertMediaManifest(ctx, conn, revisionID, write.Media, attempt.CreatedAt)
		if err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("accept capture media: %w", err)
		}
	}
	resolvedBindings, err := r.insertCaptureAssetBindings(ctx, conn, attempt, resolvedMedia, write.AssetBindings)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	// Known-digest reuse may have advanced variants to available while bindings
	// were resolved. Re-read the revision projection before snapshotting so the
	// optimistic ReaderItem and instructions describe one transaction state.
	if write.Media != nil {
		resolvedMedia, err = listMediaByRevision(ctx, conn, revisionID)
		if err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("reload accepted capture media: %w", err)
		}
	}
	var resolvedCoverage []domain.CapabilityCoverage
	if len(write.CapabilityCoverage) != 0 || len(write.EnrichmentObservations) != 0 {
		resolvedCoverage, err = InitializeCaptureOn(ctx, conn, attempt.CaptureID, fragment.ID,
			revisionID, write.EnrichmentObservations, write.CapabilityCoverage, attempt.CreatedAt)
		if err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("accept capture enrichment: %w", err)
		}
	}
	if strings.TrimSpace(write.FollowUpKind) != "" {
		payload := strings.TrimSpace(write.FollowUpPayloadJSON)
		if payload == "" {
			payload = "{}"
		}
		if !json.Valid([]byte(payload)) {
			return domain.CaptureAcceptance{}, fmt.Errorf("accept capture follow-up: payload must be valid JSON")
		}
		outboxID := domain.DigestText("capture-followup\n" + attempt.CaptureID)
		if _, err := conn.ExecContext(ctx, `
INSERT INTO capture_followup_outbox (
  id, capture_id, fragment_revision_id, kind, payload_json, state,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`, outboxID, attempt.CaptureID,
			revisionID, strings.TrimSpace(write.FollowUpKind), payload,
			formatTime(attempt.CreatedAt), formatTime(attempt.UpdatedAt)); err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("insert capture follow-up intent: %w", err)
		}
	}
	if write.Inbox != nil && outcome != UpsertSkipped && fragment.Status == domain.FragmentStatusInbox {
		if _, err := conn.ExecContext(ctx, `
INSERT INTO inbox (fragment_id, reason, staged_at, route_id)
VALUES (?, ?, ?, NULL)
ON CONFLICT(fragment_id) DO UPDATE SET
  reason = excluded.reason,
  staged_at = excluded.staged_at`,
			fragment.ID, strings.TrimSpace(write.Inbox.Reason), formatTime(write.Inbox.StagedAt)); err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("stage accepted capture in inbox: %w", err)
		}
	}
	accepted, err := loadCaptureAcceptance(ctx, conn, attempt, false)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	accepted.Media = resolvedMedia
	accepted.AssetBindings = resolvedBindings
	accepted.Coverage = resolvedCoverage
	if write.BuildAcceptanceSnapshot != nil {
		raw, err := write.BuildAcceptanceSnapshot(accepted)
		if err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("build capture acceptance snapshot: %w", err)
		}
		if strings.TrimSpace(raw) == "" || !json.Valid([]byte(raw)) {
			return domain.CaptureAcceptance{}, fmt.Errorf("build capture acceptance snapshot: valid JSON is required")
		}
		if _, err := conn.ExecContext(ctx, `UPDATE capture_attempts SET acceptance_result_json = ? WHERE capture_id = ?`, raw, attempt.CaptureID); err != nil {
			return domain.CaptureAcceptance{}, fmt.Errorf("store capture acceptance snapshot: %w", err)
		}
		attempt.AcceptanceResultJSON = raw
		accepted.Attempt.AcceptanceResultJSON = raw
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.CaptureAcceptance{}, fmt.Errorf("commit capture acceptance: %w", err)
	}
	return accepted, nil
}

func validateCaptureWrite(write CaptureWrite) error {
	a := write.Attempt
	if strings.TrimSpace(a.CaptureID) == "" {
		return fmt.Errorf("accept capture: capture_id is required")
	}
	if strings.TrimSpace(a.IdempotencyKey) == "" {
		return fmt.Errorf("accept capture: idempotency_key is required")
	}
	if strings.TrimSpace(a.SemanticDigest) == "" {
		return fmt.Errorf("accept capture: semantic digest is required")
	}
	if strings.TrimSpace(a.PrincipalID) == "" || strings.TrimSpace(a.ActorID) == "" {
		return fmt.Errorf("accept capture: principal_id and actor_id are required")
	}
	if strings.TrimSpace(a.Client.Kind) == "" || strings.TrimSpace(a.Client.Version) == "" {
		return fmt.Errorf("accept capture: client kind and version are required")
	}
	if a.CapturedAt.IsZero() || a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		return fmt.Errorf("accept capture: capture timestamps are required")
	}
	if !a.Completion.Valid() {
		return fmt.Errorf("accept capture: invalid completion %q", a.Completion)
	}
	seenAnnotations := make(map[string]struct{}, len(write.Annotations))
	for _, annotation := range write.Annotations {
		if strings.TrimSpace(annotation.ID) == "" || !annotation.Kind.Valid() || strings.TrimSpace(annotation.Text) == "" {
			return fmt.Errorf("accept capture: annotation ID, kind, and text are required")
		}
		if strings.TrimSpace(annotation.ActorID) == "" || annotation.CapturedAt.IsZero() || annotation.CreatedAt.IsZero() {
			return fmt.Errorf("accept capture: annotation actor and timestamps are required")
		}
		if annotation.Selector != nil && strings.TrimSpace(annotation.Selector.Exact) == "" {
			return fmt.Errorf("accept capture: annotation selector exact text is required")
		}
		if _, exists := seenAnnotations[annotation.ID]; exists {
			return fmt.Errorf("accept capture: duplicate annotation ID %q", annotation.ID)
		}
		seenAnnotations[annotation.ID] = struct{}{}
	}
	for _, tag := range write.Tags {
		if strings.TrimSpace(tag.ID) == "" || strings.TrimSpace(tag.Value) == "" || strings.TrimSpace(tag.NormalizedValue) == "" || !tag.Source.ValidTagSource() {
			return fmt.Errorf("accept capture: tag value, normalized value, and attribution are required")
		}
		if strings.TrimSpace(tag.Producer.Adapter) == "" || strings.TrimSpace(tag.Producer.Version) == "" || tag.ObservedAt.IsZero() || tag.CreatedAt.IsZero() {
			return fmt.Errorf("accept capture: tag producer and timestamps are required")
		}
		if tag.Source == domain.AttributionUser && strings.TrimSpace(tag.ActorID) == "" {
			return fmt.Errorf("accept capture: user tag actor is required")
		}
	}
	seenDescriptions := make(map[string]struct{}, len(write.Descriptions))
	for _, description := range write.Descriptions {
		if strings.TrimSpace(description.ID) == "" || strings.TrimSpace(description.Value) == "" || !description.Source.ValidDescriptionSource() {
			return fmt.Errorf("accept capture: description ID, value, and attribution are required")
		}
		if strings.TrimSpace(description.Producer.Adapter) == "" || strings.TrimSpace(description.Producer.Version) == "" || description.ObservedAt.IsZero() || description.CreatedAt.IsZero() {
			return fmt.Errorf("accept capture: description producer and timestamps are required")
		}
		if description.Source == domain.AttributionUser && strings.TrimSpace(description.ActorID) == "" {
			return fmt.Errorf("accept capture: user description actor is required")
		}
		if _, exists := seenDescriptions[description.ID]; exists {
			return fmt.Errorf("accept capture: duplicate description ID %q", description.ID)
		}
		seenDescriptions[description.ID] = struct{}{}
	}
	seenBindings := make(map[string]struct{}, len(write.AssetBindings))
	for _, binding := range write.AssetBindings {
		if strings.TrimSpace(binding.ClientVariantID) == "" || strings.TrimSpace(binding.VariantIdentity) == "" || !binding.Action.Valid() {
			return fmt.Errorf("accept capture: asset binding client ID, variant identity, and action are required")
		}
		if _, exists := seenBindings[binding.ClientVariantID]; exists {
			return fmt.Errorf("accept capture: duplicate client variant ID %q", binding.ClientVariantID)
		}
		seenBindings[binding.ClientVariantID] = struct{}{}
	}
	if (strings.TrimSpace(write.FollowUpKind) != "" || len(write.EnrichmentObservations) != 0 || len(write.CapabilityCoverage) != 0) && len(write.CapabilityCoverage) != len(domain.AllEnrichmentCapabilities()) {
		return fmt.Errorf("accept capture: all enrichment capabilities must be initialized")
	}
	if write.Inbox != nil {
		if strings.TrimSpace(write.Inbox.Reason) == "" || write.Inbox.StagedAt.IsZero() {
			return fmt.Errorf("accept capture: inbox reason and staged timestamp are required")
		}
	}
	seenCoverage := make(map[domain.EnrichmentCapability]struct{}, len(write.CapabilityCoverage))
	for _, item := range write.CapabilityCoverage {
		if !item.Capability.Valid() || !item.State.Valid() {
			return fmt.Errorf("accept capture: invalid capability coverage %q/%q", item.Capability, item.State)
		}
		if _, duplicate := seenCoverage[item.Capability]; duplicate {
			return fmt.Errorf("accept capture: duplicate capability coverage %q", item.Capability)
		}
		seenCoverage[item.Capability] = struct{}{}
	}
	return nil
}

func (r *CaptureRepository) insertCaptureAssetBindings(ctx context.Context, conn *sql.Conn, attempt domain.CaptureAttempt, media []domain.MediaManifestItem, writes []domain.CaptureAssetBinding) ([]domain.CaptureAssetBinding, error) {
	resolved := make([]domain.CaptureAssetBinding, 0, len(writes))
	mediaByPosition := make(map[int]domain.MediaManifestItem, len(media))
	for _, item := range media {
		mediaByPosition[item.Attachment.Position] = item
	}
	mediaRepo := NewMediaRepository(r.db)
	for _, binding := range writes {
		item, ok := mediaByPosition[binding.MediaPosition]
		if !ok {
			return nil, fmt.Errorf("accept capture asset binding %q: media position %d was not resolved", binding.ClientVariantID, binding.MediaPosition)
		}
		var variant domain.AssetVariant
		for _, candidate := range item.Variants {
			if candidate.VariantIdentity == binding.VariantIdentity {
				variant = candidate
				break
			}
		}
		if variant.ID == "" {
			return nil, fmt.Errorf("accept capture asset binding %q: variant %q was not resolved", binding.ClientVariantID, binding.VariantIdentity)
		}
		binding.CaptureID = attempt.CaptureID
		binding.FragmentRevisionID = attempt.FragmentRevisionID
		binding.AssetVariantID = variant.ID
		binding.CreatedAt = attempt.CreatedAt
		if binding.ExpectedDigest.Empty() {
			binding.ExpectedDigest = variant.ExpectedDigest
		}
		if variant.AcquisitionState == domain.AcquisitionAvailable && !variant.Digest.Empty() {
			binding.Action = domain.AssetReuseBlob
			binding.Digest = variant.Digest
		} else if !binding.ExpectedDigest.Empty() && binding.Action != domain.AssetReferenceOnly && binding.Action != domain.AssetRejected {
			blob, found, err := mediaRepo.FindBlobByDigestOn(ctx, conn, binding.ExpectedDigest.Value)
			if err != nil {
				return nil, fmt.Errorf("resolve known blob for %q: %w", binding.ClientVariantID, err)
			}
			if found {
				variant, err = mediaRepo.AttachBlobOn(ctx, conn, variant.ID, blob, attempt.CreatedAt)
				if err != nil {
					return nil, fmt.Errorf("reuse known blob for %q: %w", binding.ClientVariantID, err)
				}
				binding.Action = domain.AssetReuseBlob
				binding.Digest = variant.Digest
			}
		}
		if _, err := conn.ExecContext(ctx, `
INSERT INTO capture_asset_bindings (
  capture_id, fragment_revision_id, client_variant_id, asset_variant_id,
  instruction_action, expected_digest, reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, binding.CaptureID,
			binding.FragmentRevisionID, binding.ClientVariantID,
			binding.AssetVariantID, string(binding.Action),
			binding.ExpectedDigest.Value, binding.Reason,
			formatTime(binding.CreatedAt)); err != nil {
			return nil, fmt.Errorf("insert capture asset binding %q: %w", binding.ClientVariantID, err)
		}
		resolved = append(resolved, binding)
	}
	return resolved, nil
}

func insertCaptureAttempt(ctx context.Context, conn *sql.Conn, attempt domain.CaptureAttempt) error {
	_, err := conn.ExecContext(ctx, `
INSERT INTO capture_attempts (
  id, capture_id, idempotency_key, semantic_digest, fragment_id,
  fragment_revision_id, fragment_outcome, accepted_capture_count,
  acceptance_result_json, principal_id, actor_id, client_kind, client_version,
  captured_at, submitted_url, page_context_json, extraction_adapter,
  extraction_adapter_version, extraction_json, completion, warnings_json,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.CaptureID, attempt.IdempotencyKey, attempt.SemanticDigest,
		attempt.FragmentID, attempt.FragmentRevisionID, attempt.FragmentOutcome,
		attempt.AcceptedCaptureCount, attempt.AcceptanceResultJSON,
		attempt.PrincipalID, attempt.ActorID, attempt.Client.Kind, attempt.Client.Version,
		formatTime(attempt.CapturedAt), attempt.SubmittedURL, attempt.PageContextJSON,
		attempt.ExtractionAdapter.Adapter, attempt.ExtractionAdapter.Version,
		attempt.ExtractionJSON, string(attempt.Completion), attempt.WarningsJSON,
		formatTime(attempt.CreatedAt), formatTime(attempt.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert capture attempt %q: %w", attempt.CaptureID, err)
	}
	return nil
}

func insertCaptureTag(ctx context.Context, conn *sql.Conn, tag domain.AttributedTag) error {
	_, err := conn.ExecContext(ctx, `
INSERT INTO fragment_tag_observations (
  id, fragment_id, value, normalized_value, attribution_source,
  observation_id, capture_id, producer, producer_version, actor_id,
  observed_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(
  fragment_id, normalized_value, attribution_source,
  producer, producer_version, actor_id
) DO NOTHING`,
		tag.ID, tag.FragmentID, tag.Value, tag.NormalizedValue, string(tag.Source),
		tag.ObservationID, nullIfEmpty(tag.CaptureID), tag.Producer.Adapter,
		tag.Producer.Version, tag.ActorID, formatTime(tag.ObservedAt),
		formatTime(tag.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert capture tag %q: %w", tag.Value, err)
	}
	// The observation table owns attribution. The legacy entity relation is a
	// compatibility search/index projection and must contain at most one
	// case-folded tag value for this fragment, irrespective of how many sources
	// or captures asserted it.
	rows, err := conn.QueryContext(ctx, `
SELECT e.id, e.value
FROM fragment_entities fe
JOIN entities e ON e.id = fe.entity_id
WHERE fe.fragment_id = ? AND e.kind = 'tag'
ORDER BY e.id`, tag.FragmentID)
	if err != nil {
		return fmt.Errorf("list fragment compatibility tags: %w", err)
	}
	for rows.Next() {
		var existingID, existingValue string
		if err := rows.Scan(&existingID, &existingValue); err != nil {
			rows.Close()
			return fmt.Errorf("scan fragment compatibility tag: %w", err)
		}
		if strings.ToLower(strings.TrimSpace(existingValue)) == tag.NormalizedValue {
			rows.Close()
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate fragment compatibility tags: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close fragment compatibility tags: %w", err)
	}

	// New capture-owned tag entities use normalized identity even though the
	// legacy entity schema stores only the first display spelling.
	entityID := stableEntityID("tag", tag.NormalizedValue)
	if _, err := conn.ExecContext(ctx, `
INSERT INTO entities (id, kind, value, created_at)
VALUES (?, 'tag', ?, ?)
ON CONFLICT(id) DO NOTHING`, entityID, tag.Value, formatTime(tag.CreatedAt)); err != nil {
		return fmt.Errorf("insert compatibility tag entity %q: %w", tag.Value, err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO fragment_entities (fragment_id, entity_id, source, confidence, created_at)
VALUES (?, ?, 'capture', 1, ?)
ON CONFLICT(fragment_id, entity_id, source) DO NOTHING`,
		tag.FragmentID, entityID, formatTime(tag.CreatedAt)); err != nil {
		return fmt.Errorf("insert fragment compatibility tag %q: %w", tag.Value, err)
	}
	return nil
}

func findAttemptByCaptureOrKey(ctx context.Context, conn *sql.Conn, captureID, idempotencyKey string) (domain.CaptureAttempt, bool, error) {
	byCapture, captureFound, err := findCaptureAttempt(ctx, conn, `capture_id = ?`, captureID)
	if err != nil {
		return domain.CaptureAttempt{}, false, err
	}
	byKey, keyFound, err := findCaptureAttempt(ctx, conn, `idempotency_key = ?`, idempotencyKey)
	if err != nil {
		return domain.CaptureAttempt{}, false, err
	}
	if captureFound && keyFound && byCapture.ID != byKey.ID {
		return domain.CaptureAttempt{}, false, &CaptureConflictError{
			CaptureID: captureID, IdempotencyKey: idempotencyKey,
			Reason: "capture ID and idempotency key are bound to different attempts", Existing: byCapture,
		}
	}
	if captureFound {
		return byCapture, true, nil
	}
	if keyFound {
		return byKey, true, nil
	}
	return domain.CaptureAttempt{}, false, nil
}

const captureAttemptColumns = `
  id, capture_id, idempotency_key, semantic_digest, fragment_id,
  fragment_revision_id, fragment_outcome, accepted_capture_count,
  acceptance_result_json, principal_id, actor_id, client_kind, client_version,
  captured_at, submitted_url, page_context_json, extraction_adapter,
  extraction_adapter_version, extraction_json, completion, warnings_json,
  created_at, updated_at`

func findCaptureAttempt(ctx context.Context, conn *sql.Conn, predicate string, value any) (domain.CaptureAttempt, bool, error) {
	row := conn.QueryRowContext(ctx, `SELECT `+captureAttemptColumns+` FROM capture_attempts WHERE `+predicate, value)
	attempt, err := scanCaptureAttempt(row)
	if err == sql.ErrNoRows {
		return domain.CaptureAttempt{}, false, nil
	}
	if err != nil {
		return domain.CaptureAttempt{}, false, fmt.Errorf("find capture attempt: %w", err)
	}
	return attempt, true, nil
}

func scanCaptureAttempt(scanner rowScanner) (domain.CaptureAttempt, error) {
	var attempt domain.CaptureAttempt
	var capturedAt, createdAt, updatedAt, completion string
	err := scanner.Scan(
		&attempt.ID, &attempt.CaptureID, &attempt.IdempotencyKey,
		&attempt.SemanticDigest, &attempt.FragmentID,
		&attempt.FragmentRevisionID, &attempt.FragmentOutcome,
		&attempt.AcceptedCaptureCount, &attempt.AcceptanceResultJSON,
		&attempt.PrincipalID, &attempt.ActorID, &attempt.Client.Kind,
		&attempt.Client.Version, &capturedAt, &attempt.SubmittedURL,
		&attempt.PageContextJSON, &attempt.ExtractionAdapter.Adapter,
		&attempt.ExtractionAdapter.Version, &attempt.ExtractionJSON,
		&completion, &attempt.WarningsJSON, &createdAt, &updatedAt,
	)
	if err != nil {
		return domain.CaptureAttempt{}, err
	}
	attempt.Completion = domain.CaptureCompletion(completion)
	attempt.CapturedAt = parseTime(capturedAt)
	attempt.CreatedAt = parseTime(createdAt)
	attempt.UpdatedAt = parseTime(updatedAt)
	return attempt, nil
}

func loadCaptureAcceptance(ctx context.Context, conn *sql.Conn, attempt domain.CaptureAttempt, replay bool) (domain.CaptureAcceptance, error) {
	fragment, err := getFragmentByID(ctx, conn, attempt.FragmentID)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	revision, err := scanRevision(conn.QueryRowContext(ctx, `
SELECT id, fragment_id, ordinal, material_digest, content_digest, title,
       description, content, content_format, ordered_media_digest, metadata_json,
       normalizer_adapter, normalizer_version, observed_at, committed_at,
       COALESCE(legacy_fragment_id, '')
FROM fragment_revisions WHERE id = ?`, attempt.FragmentRevisionID))
	if err != nil {
		return domain.CaptureAcceptance{}, fmt.Errorf("load accepted fragment revision: %w", err)
	}
	annotations, err := listCaptureAnnotations(ctx, conn, attempt.CaptureID)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	tags, err := listAttributedTags(ctx, conn, attempt.FragmentID)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	descriptions, err := listDescriptionObservations(ctx, conn, attempt.FragmentID)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	media, err := listMediaByRevision(ctx, conn, attempt.FragmentRevisionID)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	bindings, err := listCaptureAssetBindings(ctx, conn, attempt.CaptureID)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	coverage, err := listCoverageOn(ctx, conn, attempt.FragmentRevisionID)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	var inInbox int
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM inbox WHERE fragment_id = ?)`, attempt.FragmentID).Scan(&inInbox); err != nil {
		return domain.CaptureAcceptance{}, fmt.Errorf("load accepted capture inbox state: %w", err)
	}
	return domain.CaptureAcceptance{
		Fragment: fragment, ObservedRevision: revision, Attempt: attempt,
		InInbox:     inInbox != 0,
		Annotations: annotations, Tags: tags, Descriptions: descriptions,
		Media: media, AssetBindings: bindings, Coverage: coverage,
		IdempotentReplay: replay,
	}, nil
}

func (r *CaptureRepository) GetAttempt(ctx context.Context, captureID string) (domain.CaptureAttempt, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+captureAttemptColumns+` FROM capture_attempts WHERE capture_id = ?`, captureID)
	attempt, err := scanCaptureAttempt(row)
	if err != nil {
		return domain.CaptureAttempt{}, fmt.Errorf("get capture attempt: %w", err)
	}
	return attempt, nil
}

func (r *CaptureRepository) ListAttempts(ctx context.Context, fragmentID string) ([]domain.CaptureAttempt, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("resolve capture attempt owner: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+captureAttemptColumns+`
FROM capture_attempts WHERE fragment_id = ? ORDER BY captured_at, capture_id`, canonicalID)
	if err != nil {
		return nil, fmt.Errorf("list capture attempts: %w", err)
	}
	defer rows.Close()
	var out []domain.CaptureAttempt
	for rows.Next() {
		attempt, err := scanCaptureAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan capture attempt: %w", err)
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func (r *CaptureRepository) CaptureCount(ctx context.Context, fragmentID string) (int, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, fragmentID)
	if err != nil {
		return 0, fmt.Errorf("resolve capture count owner: %w", err)
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_attempts WHERE fragment_id = ?`, canonicalID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count capture attempts: %w", err)
	}
	return count, nil
}

func (r *CaptureRepository) ListAnnotations(ctx context.Context, fragmentID string) ([]domain.CaptureAnnotation, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("resolve annotation owner: %w", err)
	}
	return listFragmentAnnotations(ctx, r.db, canonicalID)
}

type queryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listCaptureAnnotations(ctx context.Context, q queryContext, captureID string) ([]domain.CaptureAnnotation, error) {
	return queryAnnotations(ctx, q, `ca.capture_id = ?`, captureID)
}

func listFragmentAnnotations(ctx context.Context, q queryContext, fragmentID string) ([]domain.CaptureAnnotation, error) {
	return queryAnnotations(ctx, q, `ca.fragment_id = ?`, fragmentID)
}

func queryAnnotations(ctx context.Context, q queryContext, predicate string, value any) ([]domain.CaptureAnnotation, error) {
	rows, err := q.QueryContext(ctx, `
SELECT ca.id, ca.capture_id, ca.fragment_id, ca.kind, ca.text,
       ca.selector_json, ca.position_json, ca.actor_id, ca.captured_at, ca.created_at
FROM capture_annotations ca WHERE `+predicate+`
ORDER BY ca.captured_at, ca.capture_id, ca.id`, value)
	if err != nil {
		return nil, fmt.Errorf("list capture annotations: %w", err)
	}
	defer rows.Close()
	var out []domain.CaptureAnnotation
	for rows.Next() {
		var item domain.CaptureAnnotation
		var kind, selectorJSON, positionJSON, capturedAt, createdAt string
		if err := rows.Scan(&item.ID, &item.CaptureID, &item.FragmentID, &kind,
			&item.Text, &selectorJSON, &positionJSON, &item.ActorID,
			&capturedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan capture annotation: %w", err)
		}
		item.Kind = domain.CaptureAnnotationKind(kind)
		if selectorJSON != "" && selectorJSON != "{}" {
			if err := json.Unmarshal([]byte(selectorJSON), &item.Selector); err != nil {
				return nil, fmt.Errorf("decode capture annotation selector: %w", err)
			}
		}
		if positionJSON != "" && positionJSON != "{}" {
			if err := json.Unmarshal([]byte(positionJSON), &item.Position); err != nil {
				return nil, fmt.Errorf("decode capture annotation position: %w", err)
			}
		}
		item.CapturedAt = parseTime(capturedAt)
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *CaptureRepository) ListTags(ctx context.Context, fragmentID string) ([]domain.AttributedTag, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("resolve tag owner: %w", err)
	}
	return listAttributedTags(ctx, r.db, canonicalID)
}

func listAttributedTags(ctx context.Context, q queryContext, fragmentID string) ([]domain.AttributedTag, error) {
	rows, err := q.QueryContext(ctx, `
SELECT id, fragment_id, value, normalized_value, attribution_source,
       observation_id, capture_id, producer, producer_version, actor_id,
       observed_at, created_at
FROM fragment_tag_observations
WHERE fragment_id = ?
ORDER BY normalized_value, attribution_source`, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("list attributed tags: %w", err)
	}
	defer rows.Close()
	var out []domain.AttributedTag
	for rows.Next() {
		var item domain.AttributedTag
		var source, observedAt, createdAt string
		var captureID sql.NullString
		if err := rows.Scan(&item.ID, &item.FragmentID, &item.Value,
			&item.NormalizedValue, &source, &item.ObservationID, &captureID,
			&item.Producer.Adapter, &item.Producer.Version, &item.ActorID,
			&observedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan attributed tag: %w", err)
		}
		item.Source = domain.AttributionSource(source)
		if captureID.Valid {
			item.CaptureID = captureID.String
		}
		item.ObservedAt = parseTime(observedAt)
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *CaptureRepository) ListDescriptions(ctx context.Context, fragmentID string) ([]domain.DescriptionObservation, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("resolve description owner: %w", err)
	}
	return listDescriptionObservations(ctx, r.db, canonicalID)
}

func listDescriptionObservations(ctx context.Context, q queryContext, fragmentID string) ([]domain.DescriptionObservation, error) {
	rows, err := q.QueryContext(ctx, `
SELECT id, fragment_id, fragment_revision_id, capture_id, value,
       attribution_source, producer, producer_version, actor_id,
       observed_at, created_at
FROM fragment_description_observations
WHERE fragment_id = ?
ORDER BY observed_at, capture_id, id`, fragmentID)
	if err != nil {
		return nil, fmt.Errorf("list description observations: %w", err)
	}
	defer rows.Close()
	var out []domain.DescriptionObservation
	for rows.Next() {
		var item domain.DescriptionObservation
		var source, observedAt, createdAt string
		var revisionID, captureID sql.NullString
		if err := rows.Scan(&item.ID, &item.FragmentID, &revisionID, &captureID,
			&item.Value, &source, &item.Producer.Adapter, &item.Producer.Version,
			&item.ActorID, &observedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan description observation: %w", err)
		}
		item.Source = domain.AttributionSource(source)
		if revisionID.Valid {
			item.FragmentRevisionID = revisionID.String
		}
		if captureID.Valid {
			item.CaptureID = captureID.String
		}
		item.ObservedAt = parseTime(observedAt)
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

// AdvanceCompletion applies the bounded monotonic attempt lifecycle. Repeating
// the current state is idempotent; complete, partial, and failed are terminal.
func (r *CaptureRepository) AdvanceCompletion(ctx context.Context, captureID string, next domain.CaptureCompletion, warningsJSON string, updatedAt time.Time) (domain.CaptureAttempt, error) {
	if strings.TrimSpace(captureID) == "" || !next.Valid() || updatedAt.IsZero() {
		return domain.CaptureAttempt{}, fmt.Errorf("advance capture completion: capture ID, valid state, and timestamp are required")
	}
	conn, err := beginImmediateConn(ctx, r.db, "capture completion")
	if err != nil {
		return domain.CaptureAttempt{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	current, found, err := findCaptureAttempt(ctx, conn, `capture_id = ?`, captureID)
	if err != nil {
		return domain.CaptureAttempt{}, err
	}
	if !found {
		return domain.CaptureAttempt{}, sql.ErrNoRows
	}
	if current.Completion == next {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return domain.CaptureAttempt{}, fmt.Errorf("commit repeated capture completion: %w", err)
		}
		return current, nil
	}
	if !validCompletionTransition(current.Completion, next) {
		return domain.CaptureAttempt{}, &CaptureCompletionConflictError{
			CaptureID: captureID, Current: current.Completion, Requested: next,
		}
	}
	if updatedAt.UTC().Before(current.UpdatedAt) {
		return domain.CaptureAttempt{}, fmt.Errorf("advance capture completion: updated_at cannot move backwards")
	}
	if strings.TrimSpace(warningsJSON) == "" {
		warningsJSON = current.WarningsJSON
	}
	if _, err := conn.ExecContext(ctx, `
UPDATE capture_attempts
SET completion = ?, warnings_json = ?, updated_at = ?
WHERE capture_id = ?`, string(next), warningsJSON, formatTime(updatedAt), captureID); err != nil {
		return domain.CaptureAttempt{}, fmt.Errorf("advance capture completion: %w", err)
	}
	current.Completion = next
	current.WarningsJSON = warningsJSON
	current.UpdatedAt = updatedAt.UTC()
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.CaptureAttempt{}, fmt.Errorf("commit capture completion: %w", err)
	}
	return current, nil
}

func validCompletionTransition(current, next domain.CaptureCompletion) bool {
	switch current {
	case domain.CaptureAccepting:
		return next == domain.CaptureTransferring || next.Terminal()
	case domain.CaptureTransferring:
		return next.Terminal()
	default:
		return false
	}
}

func (r *CaptureRepository) GetCuratedNote(ctx context.Context, fragmentID string) (domain.CuratedNote, bool, error) {
	canonicalID, err := resolveCanonicalFragmentID(ctx, r.db, fragmentID)
	if err != nil {
		return domain.CuratedNote{}, false, fmt.Errorf("resolve curated note owner: %w", err)
	}
	note, found, err := getCuratedNote(ctx, r.db, canonicalID)
	if err != nil {
		return domain.CuratedNote{}, false, err
	}
	return note, found, nil
}

type queryRowContext interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getCuratedNote(ctx context.Context, q queryRowContext, fragmentID string) (domain.CuratedNote, bool, error) {
	var note domain.CuratedNote
	var updatedAt string
	err := q.QueryRowContext(ctx, `
SELECT fragment_id, body_markdown, revision, actor_id, updated_at
FROM curated_notes WHERE fragment_id = ?`, fragmentID).Scan(
		&note.FragmentID, &note.BodyMarkdown, &note.Revision, &note.ActorID, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return domain.CuratedNote{FragmentID: fragmentID}, false, nil
	}
	if err != nil {
		return domain.CuratedNote{}, false, fmt.Errorf("get curated note: %w", err)
	}
	note.UpdatedAt = parseTime(updatedAt)
	return note, true, nil
}

// UpdateCuratedNote is the only replacement primitive for curated note text.
// It compares and increments the note revision inside BEGIN IMMEDIATE so two
// independent Store handles cannot both accept the same expected revision.
func (r *CaptureRepository) UpdateCuratedNote(ctx context.Context, fragmentID string, expectedRevision int, bodyMarkdown, actorID string, updatedAt time.Time) (domain.CuratedNote, error) {
	if strings.TrimSpace(fragmentID) == "" || expectedRevision < 0 || strings.TrimSpace(actorID) == "" || updatedAt.IsZero() {
		return domain.CuratedNote{}, fmt.Errorf("update curated note: fragment ID, non-negative expected revision, actor, and timestamp are required")
	}
	conn, err := beginImmediateConn(ctx, r.db, "curated note update")
	if err != nil {
		return domain.CuratedNote{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	canonicalID, err := resolveCanonicalFragmentID(ctx, conn, fragmentID)
	if err != nil {
		return domain.CuratedNote{}, fmt.Errorf("resolve curated note owner: %w", err)
	}
	current, found, err := getCuratedNote(ctx, conn, canonicalID)
	if err != nil {
		return domain.CuratedNote{}, err
	}
	if !found {
		if expectedRevision != 0 {
			return domain.CuratedNote{}, &CuratedNoteConflictError{Expected: expectedRevision, Current: current}
		}
		current = domain.CuratedNote{
			FragmentID: canonicalID, BodyMarkdown: bodyMarkdown, Revision: 1,
			ActorID: strings.TrimSpace(actorID), UpdatedAt: updatedAt.UTC(),
		}
		if _, err := conn.ExecContext(ctx, `
INSERT INTO curated_notes(fragment_id, body_markdown, revision, actor_id, updated_at)
VALUES (?, ?, 1, ?, ?)`, canonicalID, bodyMarkdown, current.ActorID, formatTime(updatedAt)); err != nil {
			return domain.CuratedNote{}, fmt.Errorf("create curated note: %w", err)
		}
	} else {
		if current.Revision != expectedRevision {
			return domain.CuratedNote{}, &CuratedNoteConflictError{Expected: expectedRevision, Current: current}
		}
		nextRevision := current.Revision + 1
		res, err := conn.ExecContext(ctx, `
UPDATE curated_notes
SET body_markdown = ?, revision = ?, actor_id = ?, updated_at = ?
WHERE fragment_id = ? AND revision = ?`,
			bodyMarkdown, nextRevision, strings.TrimSpace(actorID), formatTime(updatedAt),
			canonicalID, expectedRevision,
		)
		if err != nil {
			return domain.CuratedNote{}, fmt.Errorf("replace curated note: %w", err)
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return domain.CuratedNote{}, fmt.Errorf("inspect curated note replacement: %w", err)
		}
		if changed != 1 {
			authoritative, _, loadErr := getCuratedNote(ctx, conn, canonicalID)
			if loadErr != nil {
				return domain.CuratedNote{}, loadErr
			}
			return domain.CuratedNote{}, &CuratedNoteConflictError{Expected: expectedRevision, Current: authoritative}
		}
		current = domain.CuratedNote{
			FragmentID: canonicalID, BodyMarkdown: bodyMarkdown, Revision: nextRevision,
			ActorID: strings.TrimSpace(actorID), UpdatedAt: updatedAt.UTC(),
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return domain.CuratedNote{}, fmt.Errorf("commit curated note update: %w", err)
	}
	return current, nil
}

func beginImmediateConn(ctx context.Context, db *sql.DB, operation string) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire %s connection: %w", operation, err)
	}
	for _, pragma := range []string{`PRAGMA busy_timeout=5000`, `PRAGMA foreign_keys=ON`} {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			conn.Close()
			return nil, fmt.Errorf("configure %s connection: %w", operation, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("begin immediate %s: %w", operation, err)
	}
	return conn, nil
}

func resolveCanonicalFragmentID(ctx context.Context, q queryRowContext, fragmentID string) (string, error) {
	var canonicalID sql.NullString
	err := q.QueryRowContext(ctx, `
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

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	if parsed.IsZero() {
		parsed, _ = time.Parse(time.RFC3339, value)
	}
	return parsed
}
