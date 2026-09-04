// Package legacycapture adapts pre-browser intake producers to the canonical
// stable fragment, immutable revision, additive capture-context, media, and
// capability-coverage model.
package legacycapture

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

const (
	adapterKind    = "fe.legacy-capture-adapter"
	adapterVersion = "1.0.0"
)

type captureAcceptor interface {
	Accept(context.Context, repository.CaptureWrite) (domain.CaptureAcceptance, error)
}

// Service is the single application boundary used by manual intake and every
// configured legacy ingest. Material is source-owned input; Projection may
// contain derived display/attachment enrichment and never affects a revision.
type Service struct {
	captures captureAcceptor
	now      func() time.Time
}

func NewService(captures *repository.CaptureRepository) *Service {
	return &Service{captures: captures, now: time.Now}
}

type Request struct {
	IngestName         string
	Material           domain.PipelineFragment
	Projection         domain.PipelineFragment
	UserTags           []string
	SourceTags         []string
	DeterministicTags  []string
	Highlights         []string
	Notes              []string
	ActorID            string
	ProjectionEntities []domain.FragmentEntity
}

type Result struct {
	Fragment         domain.Fragment
	Outcome          repository.UpsertOutcome
	IdempotentReplay bool
}

// SourceTags returns only taxonomy values explicitly carried by a legacy
// source adapter. enrichment_status and other mutable metadata are ignored.
func SourceTags(candidate domain.PipelineFragment) []string {
	if candidate.Metadata == nil {
		return nil
	}
	var values []string
	for _, key := range []string{"labels", "tags"} {
		switch raw := candidate.Metadata[key].(type) {
		case []string:
			values = append(values, raw...)
		case []any:
			for _, item := range raw {
				if value, ok := item.(string); ok {
					values = append(values, value)
				}
			}
		}
	}
	return normalizeSet(values)
}

type acceptanceSnapshot struct {
	SchemaVersion      string `json:"schema_version"`
	ProjectionComplete bool   `json:"projection_complete"`
}

// Accept writes canonical identity/revision/context/coverage and the mutable
// legacy projection in one repository-owned transaction. A committed exact
// replay returns before the projection seam and is therefore read-only.
func (s *Service) Accept(ctx context.Context, req Request) (Result, error) {
	if s == nil || s.captures == nil {
		return Result{}, fmt.Errorf("legacy capture: service is not configured")
	}
	ingestName := strings.TrimSpace(req.IngestName)
	if ingestName == "" {
		return Result{}, fmt.Errorf("legacy capture: ingest name is required")
	}
	if emptyProjection(req.Projection) {
		req.Projection = req.Material
	}
	now := s.now().UTC()
	fragment, err := repository.BuildFragment(req.Material, ingestName, now)
	if err != nil {
		return Result{}, fmt.Errorf("legacy capture: build material: %w", err)
	}
	// This predicted ID is deterministic for a newly-written revision. The
	// repository replaces placement ownership with the actually resolved ID,
	// including when a migrated legacy fragment keeps its historical primary key.
	fragment.Revision.ID = domain.DigestText(fragment.ID + "\n" + fragment.Revision.MaterialDigest)
	media := repository.BuildLegacyPipelineMediaManifest(fragment, req.Material.Attachments, now)
	// Canonical media is source-owned revision evidence. Do not couple those
	// immutable refs to the mutable legacy attachment projection: attachment
	// enrichment can change kind/metadata or introduce provider-only display
	// assets without changing the source revision.
	for index := range media {
		media[index].Asset.LegacyAttachmentID = ""
		media[index].Attachment.LegacyFragmentID = ""
		media[index].Attachment.LegacyAttachmentID = ""
	}

	userTags := normalizeSet(req.UserTags)
	sourceTags := normalizeSet(req.SourceTags)
	deterministicTags := normalizeSet(req.DeterministicTags)
	highlights := normalizeOccurrences(req.Highlights)
	notes := normalizeOccurrences(req.Notes)
	actorID := strings.TrimSpace(req.ActorID)
	if actorID == "" {
		actorID = "legacy:" + ingestName
	}
	semantic := legacySemanticPayload{
		Identity: fragment.SourceIdentity, MaterialDigest: fragment.Revision.MaterialDigest,
		UserTags: userTags, SourceTags: sourceTags, DeterministicTags: deterministicTags,
		Highlights: highlights, Notes: notes, ActorID: actorID,
	}
	rawSemantic, err := json.Marshal(semantic)
	if err != nil {
		return Result{}, fmt.Errorf("legacy capture: encode semantics: %w", err)
	}
	semanticDigest := domain.DigestText(string(rawSemantic))
	captureID := "legacy:" + semanticDigest
	producer := fragment.SourceIdentity.SourceAdapter
	if producer.Adapter == "" {
		producer = domain.AdapterVersion{Adapter: adapterKind, Version: adapterVersion}
	}
	capturedAt := fragment.Revision.ObservedAt
	if capturedAt.IsZero() {
		capturedAt = now
	}

	annotations := buildAnnotations(captureID, actorID, capturedAt, now, highlights, notes)
	tags := buildAttributedTags(captureID, actorID, capturedAt, now, userTags)
	descriptions := buildDescriptions(captureID, capturedAt, now, producer, fragment.Revision.Description)
	observations, coverage, err := buildEnrichment(captureID, fragment, media, sourceTags, deterministicTags, userTags, actorID, now)
	if err != nil {
		return Result{}, err
	}
	completeJSON := mustSnapshot(true)
	projection := repository.LegacyProjectionWrite{
		Title: req.Projection.Title, Content: req.Projection.Content,
		SourceType: req.Projection.SourceType, Metadata: req.Projection.Metadata,
		CanonicalPath: req.Projection.CanonicalPath, Attachments: req.Projection.Attachments,
		Entities: req.ProjectionEntities,
	}
	accepted, err := s.captures.Accept(ctx, repository.CaptureWrite{
		Fragment: fragment,
		Attempt: domain.CaptureAttempt{
			CaptureID: captureID, IdempotencyKey: captureID, SemanticDigest: semanticDigest,
			PrincipalID: actorID, ActorID: actorID,
			Client:     domain.CaptureClient{Kind: adapterKind, Version: adapterVersion},
			CapturedAt: capturedAt, SubmittedURL: fragment.SourceIdentity.SubmittedURL,
			PageContextJSON: "{}", ExtractionAdapter: producer,
			ExtractionJSON: `{"compatibility_adapter":"` + adapterKind + `"}`,
			Completion:     domain.CaptureComplete, WarningsJSON: "[]",
			CreatedAt: now, UpdatedAt: now,
		},
		Annotations: annotations, Tags: tags, Descriptions: descriptions, Media: media,
		EnrichmentObservations: observations, CapabilityCoverage: coverage,
		LegacyProjection:        &projection,
		BuildAcceptanceSnapshot: func(domain.CaptureAcceptance) (string, error) { return completeJSON, nil },
	})
	if err != nil {
		return Result{}, fmt.Errorf("legacy capture: accept: %w", err)
	}
	accepted.Fragment.Revision = accepted.ObservedRevision
	outcome := repository.UpsertOutcome(accepted.Attempt.FragmentOutcome)
	if accepted.IdempotentReplay {
		outcome = repository.UpsertSkipped
	}
	return Result{Fragment: accepted.Fragment, Outcome: outcome, IdempotentReplay: accepted.IdempotentReplay}, nil
}

func emptyProjection(candidate domain.PipelineFragment) bool {
	return strings.TrimSpace(candidate.Source) == "" &&
		strings.TrimSpace(candidate.SourceType) == "" &&
		strings.TrimSpace(candidate.SourceID) == "" &&
		strings.TrimSpace(candidate.Title) == "" &&
		strings.TrimSpace(candidate.Description) == "" &&
		strings.TrimSpace(candidate.Content) == "" &&
		len(candidate.Metadata) == 0 &&
		strings.TrimSpace(candidate.CanonicalPath) == "" &&
		len(candidate.Attachments) == 0
}

type legacySemanticPayload struct {
	Identity          domain.SourceIdentity `json:"identity"`
	MaterialDigest    string                `json:"material_digest"`
	UserTags          []string              `json:"user_tags"`
	SourceTags        []string              `json:"source_tags"`
	DeterministicTags []string              `json:"deterministic_tags"`
	Highlights        []string              `json:"highlights"`
	Notes             []string              `json:"notes"`
	ActorID           string                `json:"actor_id"`
}

func normalizeSet(values []string) []string {
	seen := make(map[string]string, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; !exists {
			seen[key] = value
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out
}

func normalizeOccurrences(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func buildAnnotations(captureID, actorID string, capturedAt, now time.Time, highlights, notes []string) []domain.CaptureAnnotation {
	out := make([]domain.CaptureAnnotation, 0, len(highlights)+len(notes))
	appendKind := func(kind domain.CaptureAnnotationKind, values []string) {
		for index, value := range values {
			out = append(out, domain.CaptureAnnotation{
				ID:   domain.DigestText(fmt.Sprintf("legacy-annotation\n%s\n%s\n%d\n%s", captureID, kind, index, value)),
				Kind: kind, Text: value, ActorID: actorID, CapturedAt: capturedAt, CreatedAt: now,
			})
		}
	}
	appendKind(domain.CaptureAnnotationHighlight, highlights)
	appendKind(domain.CaptureAnnotationNote, notes)
	return out
}

func buildAttributedTags(captureID, actorID string, observedAt, now time.Time, values []string) []domain.AttributedTag {
	out := make([]domain.AttributedTag, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(value)
		out = append(out, domain.AttributedTag{
			ID:    domain.DigestText("legacy-tag\n" + captureID + "\n" + normalized),
			Value: value, NormalizedValue: normalized, Source: domain.AttributionUser,
			ObservationID: captureID, Producer: domain.AdapterVersion{Adapter: adapterKind, Version: adapterVersion},
			ActorID: actorID, ObservedAt: observedAt, CreatedAt: now,
		})
	}
	return out
}

func buildDescriptions(captureID string, observedAt, now time.Time, producer domain.AdapterVersion, value string) []domain.DescriptionObservation {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []domain.DescriptionObservation{{
		ID:    domain.DigestText("legacy-description\n" + captureID + "\n" + value),
		Value: value, Source: domain.AttributionSourceMaterial, Producer: producer,
		ObservedAt: observedAt, CreatedAt: now,
	}}
}

func mustSnapshot(complete bool) string {
	raw, _ := json.Marshal(acceptanceSnapshot{SchemaVersion: "fe.legacy-capture-adapter.v1", ProjectionComplete: complete})
	return string(raw)
}
