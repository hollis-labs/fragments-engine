package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

// CaptureContextService owns durable capture provenance and user context. The
// browser capture transport is introduced separately; this service accepts
// transport-independent domain values and composes one atomic repository write.
type CaptureContextService struct {
	repo *repository.CaptureRepository
	now  func() time.Time
}

func NewCaptureContextService(repo *repository.CaptureRepository) *CaptureContextService {
	return &CaptureContextService{repo: repo, now: time.Now}
}

type CaptureContextRequest struct {
	Fragment              domain.Fragment
	CaptureID             string
	IdempotencyKey        string
	ProtocolPayloadDigest string
	PrincipalID           string
	ActorID               string
	Client                domain.CaptureClient
	CapturedAt            time.Time
	SubmittedURL          string
	PageContextJSON       string
	ExtractionAdapter     domain.AdapterVersion
	ExtractionJSON        string
	Completion            domain.CaptureCompletion
	WarningsJSON          string
	Annotations           []domain.CaptureAnnotation
	Tags                  []domain.AttributedTag
	Descriptions          []domain.DescriptionObservation
}

// Accept validates and normalizes capture context, derives a server-controlled
// semantic digest, and atomically persists it with fragment/revision resolution.
// ProtocolPayloadDigest is an extension seam for the canonical full-envelope
// digest that the manifest service adds once media records are in scope.
func (s *CaptureContextService) Accept(ctx context.Context, req CaptureContextRequest) (domain.CaptureAcceptance, error) {
	if s == nil || s.repo == nil {
		return domain.CaptureAcceptance{}, fmt.Errorf("accept capture context: repository is required")
	}
	write, err := s.normalize(req)
	if err != nil {
		return domain.CaptureAcceptance{}, err
	}
	return s.repo.Accept(ctx, write)
}

func (s *CaptureContextService) normalize(req CaptureContextRequest) (repository.CaptureWrite, error) {
	captureID := strings.TrimSpace(req.CaptureID)
	if captureID == "" {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context: capture_id is required")
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = captureID
	}
	principalID := strings.TrimSpace(req.PrincipalID)
	if principalID == "" {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context: principal_id is required")
	}
	actorID := strings.TrimSpace(req.ActorID)
	if actorID == "" {
		actorID = principalID
	}
	client := req.Client
	client.Kind = strings.TrimSpace(client.Kind)
	client.Version = strings.TrimSpace(client.Version)
	if client.Kind == "" || client.Version == "" {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context: client kind and version are required")
	}
	capturedAt := req.CapturedAt.UTC()
	if capturedAt.IsZero() {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context: captured_at is required")
	}
	completion := req.Completion
	if completion == "" {
		completion = domain.CaptureAccepting
	}
	if !completion.Valid() {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context: invalid completion %q", completion)
	}
	pageContext, err := canonicalJSON(req.PageContextJSON, `{}`)
	if err != nil {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context page context: %w", err)
	}
	extraction, err := canonicalJSON(req.ExtractionJSON, `{}`)
	if err != nil {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context extraction: %w", err)
	}
	warnings, err := canonicalJSON(req.WarningsJSON, `[]`)
	if err != nil {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context warnings: %w", err)
	}
	extractionAdapter := req.ExtractionAdapter
	extractionAdapter.Adapter = strings.TrimSpace(extractionAdapter.Adapter)
	extractionAdapter.Version = strings.TrimSpace(extractionAdapter.Version)
	if extractionAdapter.Adapter == "" || extractionAdapter.Version == "" {
		return repository.CaptureWrite{}, fmt.Errorf("accept capture context: extraction adapter and version are required")
	}

	now := s.now().UTC()
	annotations := make([]domain.CaptureAnnotation, 0, len(req.Annotations))
	annotationIDs := make(map[string]struct{}, len(req.Annotations))
	for _, annotation := range req.Annotations {
		annotation.ID = strings.TrimSpace(annotation.ID)
		if annotation.ID == "" || !annotation.Kind.Valid() || strings.TrimSpace(annotation.Text) == "" {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context: annotation ID, kind, and text are required")
		}
		if _, exists := annotationIDs[annotation.ID]; exists {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context: duplicate annotation ID %q", annotation.ID)
		}
		annotationIDs[annotation.ID] = struct{}{}
		if annotation.Selector != nil && strings.TrimSpace(annotation.Selector.Exact) == "" {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context annotation %q selector exact text is required", annotation.ID)
		}
		if annotation.Position != nil {
			if annotation.Position.StartOffset != nil && *annotation.Position.StartOffset < 0 {
				return repository.CaptureWrite{}, fmt.Errorf("accept capture context annotation %q start offset must be non-negative", annotation.ID)
			}
			if annotation.Position.EndOffset != nil && *annotation.Position.EndOffset < 0 {
				return repository.CaptureWrite{}, fmt.Errorf("accept capture context annotation %q end offset must be non-negative", annotation.ID)
			}
		}
		if strings.TrimSpace(annotation.ActorID) == "" {
			annotation.ActorID = actorID
		}
		if annotation.CapturedAt.IsZero() {
			annotation.CapturedAt = capturedAt
		} else {
			annotation.CapturedAt = annotation.CapturedAt.UTC()
		}
		annotation.CreatedAt = now
		annotations = append(annotations, annotation)
	}
	sort.Slice(annotations, func(i, j int) bool { return annotations[i].ID < annotations[j].ID })

	tags := make([]domain.AttributedTag, 0, len(req.Tags))
	seenTags := make(map[string]struct{}, len(req.Tags))
	for _, tag := range req.Tags {
		tag.Value = strings.TrimSpace(tag.Value)
		if tag.Value == "" {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context: tag value is required")
		}
		if tag.Source == "" {
			tag.Source = domain.AttributionUser
		}
		if !tag.Source.ValidTagSource() {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context: invalid tag source %q", tag.Source)
		}
		tag.NormalizedValue = strings.ToLower(tag.Value)
		if tag.ObservationID == "" {
			tag.ObservationID = captureID
		}
		if tag.Producer.Adapter == "" {
			tag.Producer.Adapter = client.Kind
		}
		if tag.Producer.Version == "" {
			tag.Producer.Version = client.Version
		}
		if tag.ActorID == "" && tag.Source == domain.AttributionUser {
			tag.ActorID = actorID
		}
		key := strings.Join([]string{
			tag.NormalizedValue, string(tag.Source), tag.Producer.Adapter,
			tag.Producer.Version, tag.ActorID,
		}, "\x00")
		if _, exists := seenTags[key]; exists {
			continue
		}
		seenTags[key] = struct{}{}
		if tag.ObservedAt.IsZero() {
			tag.ObservedAt = capturedAt
		} else {
			tag.ObservedAt = tag.ObservedAt.UTC()
		}
		tag.CreatedAt = now
		tag.ID = domain.DigestText("capture-tag\n" + captureID + "\n" + key)
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].NormalizedValue != tags[j].NormalizedValue {
			return tags[i].NormalizedValue < tags[j].NormalizedValue
		}
		return tags[i].Source < tags[j].Source
	})

	descriptions := make([]domain.DescriptionObservation, 0, len(req.Descriptions))
	descriptionIDs := make(map[string]struct{}, len(req.Descriptions))
	for i, description := range req.Descriptions {
		if strings.TrimSpace(description.Value) == "" {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context: description value is required")
		}
		if description.Source == "" {
			description.Source = domain.AttributionSourceMaterial
		}
		if !description.Source.ValidDescriptionSource() {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context: invalid description source %q", description.Source)
		}
		if description.Producer.Adapter == "" {
			description.Producer = extractionAdapter
		}
		if description.ActorID == "" && description.Source == domain.AttributionUser {
			description.ActorID = actorID
		}
		if description.ObservedAt.IsZero() {
			description.ObservedAt = capturedAt
		} else {
			description.ObservedAt = description.ObservedAt.UTC()
		}
		description.CreatedAt = now
		description.ID = strings.TrimSpace(description.ID)
		if description.ID == "" {
			description.ID = domain.DigestText(fmt.Sprintf("capture-description\n%s\n%d\n%s\n%s", captureID, i, description.Source, description.Value))
		}
		if _, exists := descriptionIDs[description.ID]; exists {
			return repository.CaptureWrite{}, fmt.Errorf("accept capture context: duplicate description ID %q", description.ID)
		}
		descriptionIDs[description.ID] = struct{}{}
		descriptions = append(descriptions, description)
	}
	sort.Slice(descriptions, func(i, j int) bool { return descriptions[i].ID < descriptions[j].ID })

	attempt := domain.CaptureAttempt{
		CaptureID: captureID, IdempotencyKey: idempotencyKey,
		PrincipalID: principalID, ActorID: actorID, Client: client,
		CapturedAt: capturedAt, SubmittedURL: strings.TrimSpace(req.SubmittedURL),
		PageContextJSON: pageContext, ExtractionAdapter: extractionAdapter,
		ExtractionJSON: extraction, Completion: completion, WarningsJSON: warnings,
		CreatedAt: now, UpdatedAt: now,
	}
	attempt.SemanticDigest = captureSemanticDigest(req.Fragment, attempt, annotations, tags, descriptions, strings.TrimSpace(req.ProtocolPayloadDigest))

	return repository.CaptureWrite{
		Fragment: req.Fragment, Attempt: attempt, Annotations: annotations,
		Tags: tags, Descriptions: descriptions,
	}, nil
}

func captureSemanticDigest(fragment domain.Fragment, attempt domain.CaptureAttempt, annotations []domain.CaptureAnnotation, tags []domain.AttributedTag, descriptions []domain.DescriptionObservation, protocolPayloadDigest string) string {
	type semanticAnnotation struct {
		ID         string                       `json:"id"`
		Kind       domain.CaptureAnnotationKind `json:"kind"`
		Text       string                       `json:"text"`
		Selector   *domain.TextQuoteSelector    `json:"selector,omitempty"`
		Position   *domain.DocumentPosition     `json:"position,omitempty"`
		ActorID    string                       `json:"actor_id"`
		CapturedAt time.Time                    `json:"captured_at"`
	}
	type semanticTag struct {
		NormalizedValue string                   `json:"normalized_value"`
		Source          domain.AttributionSource `json:"source"`
		ObservationID   string                   `json:"observation_id"`
		Producer        domain.AdapterVersion    `json:"producer"`
		ActorID         string                   `json:"actor_id"`
		ObservedAt      time.Time                `json:"observed_at"`
	}
	type semanticDescription struct {
		ID         string                   `json:"id"`
		Value      string                   `json:"value"`
		Source     domain.AttributionSource `json:"source"`
		Producer   domain.AdapterVersion    `json:"producer"`
		ActorID    string                   `json:"actor_id"`
		ObservedAt time.Time                `json:"observed_at"`
	}
	semanticAnnotations := make([]semanticAnnotation, 0, len(annotations))
	for _, item := range annotations {
		semanticAnnotations = append(semanticAnnotations, semanticAnnotation{
			item.ID, item.Kind, item.Text, item.Selector, item.Position,
			item.ActorID, item.CapturedAt,
		})
	}
	semanticTags := make([]semanticTag, 0, len(tags))
	for _, item := range tags {
		semanticTags = append(semanticTags, semanticTag{
			item.NormalizedValue, item.Source, item.ObservationID, item.Producer,
			item.ActorID, item.ObservedAt,
		})
	}
	semanticDescriptions := make([]semanticDescription, 0, len(descriptions))
	for _, item := range descriptions {
		semanticDescriptions = append(semanticDescriptions, semanticDescription{
			item.ID, item.Value, item.Source, item.Producer, item.ActorID, item.ObservedAt,
		})
	}
	payload := struct {
		ProtocolPayloadDigest string                   `json:"protocol_payload_digest,omitempty"`
		SourceIdentity        domain.SourceIdentity    `json:"source_identity"`
		MaterialDigest        string                   `json:"material_digest"`
		CaptureID             string                   `json:"capture_id"`
		IdempotencyKey        string                   `json:"idempotency_key"`
		PrincipalID           string                   `json:"principal_id"`
		ActorID               string                   `json:"actor_id"`
		Client                domain.CaptureClient     `json:"client"`
		CapturedAt            time.Time                `json:"captured_at"`
		SubmittedURL          string                   `json:"submitted_url"`
		PageContextJSON       string                   `json:"page_context_json"`
		ExtractionAdapter     domain.AdapterVersion    `json:"extraction_adapter"`
		ExtractionJSON        string                   `json:"extraction_json"`
		Completion            domain.CaptureCompletion `json:"completion"`
		WarningsJSON          string                   `json:"warnings_json"`
		Annotations           []semanticAnnotation     `json:"annotations"`
		Tags                  []semanticTag            `json:"tags"`
		Descriptions          []semanticDescription    `json:"descriptions"`
	}{
		protocolPayloadDigest, fragment.SourceIdentity, fragment.Revision.MaterialDigest,
		attempt.CaptureID, attempt.IdempotencyKey, attempt.PrincipalID,
		attempt.ActorID, attempt.Client, attempt.CapturedAt, attempt.SubmittedURL,
		attempt.PageContextJSON, attempt.ExtractionAdapter, attempt.ExtractionJSON,
		attempt.Completion, attempt.WarningsJSON, semanticAnnotations, semanticTags,
		semanticDescriptions,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return domain.DigestText(string(raw))
}

func canonicalJSON(raw, fallback string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("multiple JSON values")
		}
		return "", err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

func (s *CaptureContextService) AdvanceCompletion(ctx context.Context, captureID string, next domain.CaptureCompletion, warningsJSON string, updatedAt time.Time) (domain.CaptureAttempt, error) {
	warnings := ""
	if strings.TrimSpace(warningsJSON) != "" {
		var err error
		warnings, err = canonicalJSON(warningsJSON, `[]`)
		if err != nil {
			return domain.CaptureAttempt{}, fmt.Errorf("advance capture completion warnings: %w", err)
		}
	}
	return s.repo.AdvanceCompletion(ctx, captureID, next, warnings, updatedAt)
}

func (s *CaptureContextService) GetAttempt(ctx context.Context, captureID string) (domain.CaptureAttempt, error) {
	return s.repo.GetAttempt(ctx, captureID)
}

func (s *CaptureContextService) ListAttempts(ctx context.Context, fragmentID string) ([]domain.CaptureAttempt, error) {
	return s.repo.ListAttempts(ctx, fragmentID)
}

func (s *CaptureContextService) CaptureCount(ctx context.Context, fragmentID string) (int, error) {
	return s.repo.CaptureCount(ctx, fragmentID)
}

func (s *CaptureContextService) ListAnnotations(ctx context.Context, fragmentID string) ([]domain.CaptureAnnotation, error) {
	return s.repo.ListAnnotations(ctx, fragmentID)
}

func (s *CaptureContextService) ListTags(ctx context.Context, fragmentID string) ([]domain.AttributedTag, error) {
	return s.repo.ListTags(ctx, fragmentID)
}

func (s *CaptureContextService) ListDescriptions(ctx context.Context, fragmentID string) ([]domain.DescriptionObservation, error) {
	return s.repo.ListDescriptions(ctx, fragmentID)
}

func (s *CaptureContextService) UpdateCuratedNote(ctx context.Context, fragmentID string, expectedRevision int, bodyMarkdown, actorID string, updatedAt time.Time) (domain.CuratedNote, error) {
	return s.repo.UpdateCuratedNote(ctx, fragmentID, expectedRevision, bodyMarkdown, actorID, updatedAt)
}

func (s *CaptureContextService) GetCuratedNote(ctx context.Context, fragmentID string) (domain.CuratedNote, bool, error) {
	return s.repo.GetCuratedNote(ctx, fragmentID)
}
