package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type EnrichmentService struct {
	repository  *repository.EnrichmentRepository
	credentials provider.CredentialResolver
	now         func() time.Time
}

func NewEnrichmentService(repo *repository.EnrichmentRepository, credentials provider.CredentialResolver) *EnrichmentService {
	return &EnrichmentService{repository: repo, credentials: credentials, now: time.Now}
}

type CapabilityReRequest struct {
	FragmentRevisionID string
	Capability         domain.EnrichmentCapability
	IdempotencyKey     string
	RequestedBy        string
	Reason             string
}

func (s *EnrichmentService) Coverage(ctx context.Context, revisionID string) ([]domain.CapabilityCoverage, error) {
	if s == nil || s.repository == nil {
		return nil, fmt.Errorf("get enrichment coverage: repository is required")
	}
	return s.repository.ListCoverage(ctx, strings.TrimSpace(revisionID), s.now().UTC())
}

func (s *EnrichmentService) RequestCapability(ctx context.Context, request CapabilityReRequest) (domain.CapabilityCoverage, []domain.EnrichmentJob, bool, error) {
	if s == nil || s.repository == nil {
		return domain.CapabilityCoverage{}, nil, false, fmt.Errorf("request enrichment capability: repository is required")
	}
	now := s.now().UTC()
	semanticDigest := domain.DigestText(strings.Join([]string{strings.TrimSpace(request.FragmentRevisionID), string(request.Capability), strings.TrimSpace(request.RequestedBy), request.Reason}, "\n"))
	coverage, replay, err := s.repository.RequestCapability(ctx, repository.CapabilityRequest{
		IdempotencyKey: request.IdempotencyKey, SemanticDigest: semanticDigest,
		FragmentRevisionID: request.FragmentRevisionID, Capability: request.Capability,
		RequestedBy: request.RequestedBy, Reason: request.Reason, CreatedAt: now,
	})
	if err != nil || replay {
		return coverage, nil, replay, err
	}
	jobs, err := s.repository.PlanRevision(ctx, request.FragmentRevisionID, now)
	return coverage, jobs, false, err
}

func (s *EnrichmentService) PlanCapture(ctx context.Context, captureID string) (repository.EnrichmentPlan, error) {
	if s == nil || s.repository == nil {
		return repository.EnrichmentPlan{}, fmt.Errorf("plan capture enrichment: repository is required")
	}
	return s.repository.PlanCaptureFollowUp(ctx, strings.TrimSpace(captureID), s.now().UTC())
}

func (s *EnrichmentService) PlanRevision(ctx context.Context, revisionID string) ([]domain.EnrichmentJob, error) {
	if s == nil || s.repository == nil {
		return nil, fmt.Errorf("plan revision enrichment: repository is required")
	}
	return s.repository.PlanRevision(ctx, strings.TrimSpace(revisionID), s.now().UTC())
}

func (s *EnrichmentService) Claim(ctx context.Context, descriptor provider.Descriptor, workerID string, lease time.Duration) (domain.EnrichmentClaim, bool, error) {
	if s == nil || s.repository == nil {
		return domain.EnrichmentClaim{}, false, fmt.Errorf("claim enrichment: repository is required")
	}
	claim, found, err := s.repository.ClaimNext(ctx, repository.ClaimSpec{Descriptor: descriptor, WorkerID: workerID, Lease: lease, Now: s.now().UTC()})
	if err != nil || !found {
		return claim, found, err
	}
	input := provider.Input{FragmentID: claim.Job.FragmentID, FragmentRevisionID: claim.Job.FragmentRevisionID,
		Capability: claim.Job.Capability, Source: claim.Source, MaterialDigest: claim.Material.MaterialDigest,
		AssetDigests: claim.AssetDigests}
	if err := input.Validate(); err != nil {
		return domain.EnrichmentClaim{}, false, fmt.Errorf("validate claimed provider input: %w", err)
	}
	return claim, true, nil
}

func ProviderInput(claim domain.EnrichmentClaim) provider.Input {
	return provider.Input{FragmentID: claim.Job.FragmentID, FragmentRevisionID: claim.Job.FragmentRevisionID,
		Capability: claim.Job.Capability, Source: claim.Source,
		MaterialDigest: claim.Material.MaterialDigest, AssetDigests: claim.AssetDigests}
}

func (s *EnrichmentService) CompleteSuccess(ctx context.Context, claim domain.EnrichmentClaim, result provider.ObservationDraft) (domain.CapabilityCoverage, error) {
	if s == nil || s.repository == nil {
		return domain.CapabilityCoverage{}, fmt.Errorf("complete enrichment: repository is required")
	}
	if result.Capability != claim.Job.Capability {
		return domain.CapabilityCoverage{}, fmt.Errorf("complete enrichment: result capability does not match claim")
	}
	if err := result.Validate(); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	now := s.now().UTC()
	observation := domain.EnrichmentObservation{Capability: result.Capability,
		Attribution: result.Attribution, Producer: claim.Attempt.Adapter,
		ValueJSON: result.ValueJSON, Confidence: result.Confidence,
		ObservedAt: result.ObservedAt, ExpiresAt: result.ExpiresAt,
		AssertedAt: now, CreatedAt: now}
	return s.repository.CompleteSuccess(ctx, claim.Attempt.ClaimToken, observation, now)
}

func (s *EnrichmentService) CompleteFailure(ctx context.Context, claim domain.EnrichmentClaim, adapterErr *provider.AdapterError) (domain.CapabilityCoverage, error) {
	if s == nil || s.repository == nil {
		return domain.CapabilityCoverage{}, fmt.Errorf("fail enrichment: repository is required")
	}
	if err := adapterErr.Validate(); err != nil {
		return domain.CapabilityCoverage{}, err
	}
	return s.repository.CompleteFailure(ctx, claim.Attempt.ClaimToken,
		domain.EnrichmentFailure{Class: adapterErr.Class, Code: adapterErr.Code, Message: adapterErr.Message}, s.now().UTC())
}

// UseCredential resolves one declared reference only for the duration of the
// callback. The service never returns, logs, serializes, or persists the bytes.
func (s *EnrichmentService) UseCredential(ctx context.Context, descriptor provider.Descriptor, name string, use func([]byte) error) error {
	if err := descriptor.Validate(); err != nil {
		return err
	}
	var selected *provider.CredentialReference
	for index := range descriptor.CredentialReferences {
		if descriptor.CredentialReferences[index].Name == name {
			selected = &descriptor.CredentialReferences[index]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("credential reference %q is not declared by adapter %s", name, descriptor.Adapter)
	}
	if s == nil || s.credentials == nil {
		return fmt.Errorf("credential resolver is unavailable")
	}
	return s.credentials.Use(ctx, *selected, use)
}
