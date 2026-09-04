package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestEnrichmentPlanClaimGapFillAndCaptureStatus(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	enrichment := newTestEnrichmentService(st, captureTestTime.Add(time.Minute), nil)
	plan, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.IdempotentReplay || len(plan.Jobs) != 5 {
		t.Fatalf("capture plan = %+v, want five missing jobs", plan)
	}
	replayed, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID)
	if err != nil || !replayed.IdempotentReplay || len(replayed.Jobs) != 0 {
		t.Fatalf("capture plan replay = %+v err=%v", replayed, err)
	}
	status, err := captureService.GetStatus(context.Background(), accepted.CaptureID)
	if err != nil || !status.EnrichmentInProgress {
		t.Fatalf("queued provider jobs not reflected in capture status: %+v err=%v", status, err)
	}

	descriptor := testDescriptor(domain.CapabilitySummary)
	claim, found, err := enrichment.Claim(context.Background(), descriptor, "worker-a", time.Minute)
	if err != nil || !found || claim.Job.Capability != domain.CapabilitySummary {
		t.Fatalf("claim summary = %+v found=%v err=%v", claim, found, err)
	}
	if err := ProviderInput(claim).Validate(); err != nil {
		t.Fatalf("claim did not expose valid revision-pinned input: %v", err)
	}
	var descriptorJSON, credentialJSON, materialDigest, assetJSON string
	if err := st.DB.QueryRow(`SELECT descriptor_json, credential_refs_json, input_material_digest, input_asset_digests_json FROM enrichment_job_attempts WHERE claim_token = ?`, claim.Attempt.ClaimToken).Scan(&descriptorJSON, &credentialJSON, &materialDigest, &assetJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(descriptorJSON, `"supported_providers"`) || !strings.Contains(descriptorJSON, `"capabilities"`) ||
		!strings.Contains(credentialJSON, `"youtube-api"`) || materialDigest != claim.Material.MaterialDigest || !json.Valid([]byte(assetJSON)) {
		t.Fatalf("incomplete attempt provenance: descriptor=%s credentials=%s material=%s assets=%s", descriptorJSON, credentialJSON, materialDigest, assetJSON)
	}
	result := provider.ObservationDraft{Capability: domain.CapabilitySummary, Attribution: domain.AttributionProvider,
		ValueJSON: `{"text":"Provider summary","format":"plain_text"}`, ObservedAt: captureTestTime}
	coverage, err := enrichment.CompleteSuccess(context.Background(), claim, result)
	if err != nil || coverage.State != domain.CoverageProvided || coverage.SelectedObservationID == "" {
		t.Fatalf("provider gap fill = %+v err=%v", coverage, err)
	}
	observations, err := repository.NewEnrichmentRepository(st.DB).ListObservations(context.Background(), accepted.FragmentRevisionID, domain.CapabilitySummary)
	if err != nil || len(observations) != 1 || observations[0].Attribution != domain.AttributionProvider {
		t.Fatalf("provider observation missing attribution: %+v err=%v", observations, err)
	}
}

func TestEnrichmentClaimPinsVerifiedAssetDigests(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("poster bytes used by provider")
	digest := digestBytes(content)
	if _, err := captureService.StoreAssetContent(context.Background(), accepted.CaptureID, "poster:maxresdefault", digest, strings.NewReader(string(content))); err != nil {
		t.Fatal(err)
	}
	enrichment := newTestEnrichmentService(st, captureTestTime.Add(time.Minute), nil)
	if _, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID); err != nil {
		t.Fatal(err)
	}
	claim, found, err := enrichment.Claim(context.Background(), testDescriptor(domain.CapabilityVision), "vision-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim vision: found=%v err=%v", found, err)
	}
	if len(claim.AssetDigests) != 1 || claim.AssetDigests[0].Digest.Value != digest.Value {
		t.Fatalf("claim asset provenance = %+v, want verified poster digest", claim.AssetDigests)
	}
	if claim.Attempt.InputMaterialDigest != claim.Material.MaterialDigest || !strings.Contains(claim.Attempt.InputAssetDigestsJSON, digest.Value) {
		t.Fatalf("attempt did not pin revision/assets: %+v", claim.Attempt)
	}
	coverage, err := enrichment.CompleteSuccess(context.Background(), claim, provider.ObservationDraft{Capability: domain.CapabilityVision,
		Attribution: domain.AttributionModel, ValueJSON: `{"text":"A video poster","format":"plain_text"}`, ObservedAt: captureTestTime})
	if err != nil || coverage.State != domain.CoverageProvided {
		t.Fatalf("complete vision: %+v err=%v", coverage, err)
	}
	observations, err := repository.NewEnrichmentRepository(st.DB).ListObservations(context.Background(), accepted.FragmentRevisionID, domain.CapabilityVision)
	if err != nil || len(observations) != 1 || len(observations[0].InputAssetDigests) != 1 || observations[0].InputAssetDigests[0].Digest.Value != digest.Value {
		t.Fatalf("observation lost asset provenance: %+v err=%v", observations, err)
	}
}

func TestEnrichmentExplicitRefreshNeverErasesSourceSelection(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	enrichment := newTestEnrichmentService(st, captureTestTime.Add(time.Minute), nil)
	if _, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID); err != nil {
		t.Fatal(err)
	}
	coverage, jobs, replay, err := enrichment.RequestCapability(context.Background(), CapabilityReRequest{
		FragmentRevisionID: accepted.FragmentRevisionID, Capability: domain.CapabilityTitle,
		IdempotencyKey: "refresh-title", RequestedBy: "local-user", Reason: "confirm provider title",
	})
	if err != nil || replay || coverage.SelectedObservationID == "" || len(jobs) != 1 {
		t.Fatalf("request title refresh: coverage=%+v jobs=%+v replay=%v err=%v", coverage, jobs, replay, err)
	}
	claim, found, err := enrichment.Claim(context.Background(), testDescriptor(domain.CapabilityTitle), "worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim title refresh: %v %v", found, err)
	}
	providerResult := provider.ObservationDraft{Capability: domain.CapabilityTitle, Attribution: domain.AttributionProvider,
		ValueJSON: `{"text":"Later provider title"}`, ObservedAt: captureTestTime.Add(time.Minute)}
	resolved, err := enrichment.CompleteSuccess(context.Background(), claim, providerResult)
	if err != nil || resolved.State != domain.CoverageProvided {
		t.Fatalf("complete title refresh: %+v err=%v", resolved, err)
	}
	observations, err := repository.NewEnrichmentRepository(st.DB).ListObservations(context.Background(), accepted.FragmentRevisionID, domain.CapabilityTitle)
	if err != nil || len(observations) != 2 {
		t.Fatalf("title observations = %+v err=%v", observations, err)
	}
	selectedAttribution := domain.AttributionSource("")
	for _, observation := range observations {
		if observation.ID == resolved.SelectedObservationID {
			selectedAttribution = observation.Attribution
		}
	}
	if selectedAttribution != domain.AttributionSourceMaterial {
		t.Fatalf("later provider erased source display selection: %s", selectedAttribution)
	}

	_, moreJobs, replay, err := enrichment.RequestCapability(context.Background(), CapabilityReRequest{
		FragmentRevisionID: accepted.FragmentRevisionID, Capability: domain.CapabilityTitle,
		IdempotencyKey: "refresh-title", RequestedBy: "local-user", Reason: "confirm provider title",
	})
	if err != nil || !replay || len(moreJobs) != 0 {
		t.Fatalf("request replay duplicated work: jobs=%d replay=%v err=%v", len(moreJobs), replay, err)
	}
	_, _, _, err = enrichment.RequestCapability(context.Background(), CapabilityReRequest{
		FragmentRevisionID: accepted.FragmentRevisionID, Capability: domain.CapabilityTitle,
		IdempotencyKey: "refresh-title", RequestedBy: "local-user", Reason: "different",
	})
	var conflict *repository.EnrichmentConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("changed request replay = %T %v, want conflict", err, err)
	}
}

func TestEnrichmentRequestDuringRunningJobQueuesOneSuccessorAfterCompletion(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	now := captureTestTime.Add(time.Minute)
	enrichment := newTestEnrichmentService(st, now, nil)
	if _, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID); err != nil {
		t.Fatal(err)
	}
	descriptor := testDescriptor(domain.CapabilitySummary)
	claim, found, err := enrichment.Claim(context.Background(), descriptor, "worker-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim initial summary: found=%v err=%v", found, err)
	}
	requested, jobs, replay, err := enrichment.RequestCapability(context.Background(), CapabilityReRequest{
		FragmentRevisionID: accepted.FragmentRevisionID, Capability: domain.CapabilitySummary,
		IdempotencyKey: "refresh-running-summary", RequestedBy: "local-user", Reason: "refresh after this attempt",
	})
	if err != nil || replay || len(jobs) != 0 || requested.RequestedGeneration != 1 {
		t.Fatalf("request during running job: coverage=%+v jobs=%+v replay=%v err=%v", requested, jobs, replay, err)
	}
	assertSQLCount(t, st.DB, `SELECT COUNT(*) FROM enrichment_jobs WHERE capability = 'summary' AND status IN ('queued', 'running')`, 1)

	afterFirst, err := enrichment.CompleteSuccess(context.Background(), claim, provider.ObservationDraft{
		Capability: domain.CapabilitySummary, Attribution: domain.AttributionProvider,
		ValueJSON: `{"text":"first result","format":"plain_text"}`, ObservedAt: now,
	})
	if err != nil || afterFirst.State != domain.CoveragePending || afterFirst.SelectedObservationID == "" ||
		afterFirst.RequestedGeneration != 1 || afterFirst.SatisfiedGeneration != 0 {
		t.Fatalf("first completion did not hand off requested refresh: coverage=%+v err=%v", afterFirst, err)
	}
	assertSQLCount(t, st.DB, `SELECT COUNT(*) FROM enrichment_jobs WHERE capability = 'summary' AND status = 'succeeded'`, 1)
	assertSQLCount(t, st.DB, `SELECT COUNT(*) FROM enrichment_jobs WHERE capability = 'summary' AND status = 'queued' AND request_generation = 1`, 1)
	assertSQLCount(t, st.DB, `SELECT COUNT(*) FROM enrichment_jobs WHERE capability = 'summary' AND status = 'running'`, 0)

	successor, found, err := enrichment.Claim(context.Background(), descriptor, "worker-2", time.Minute)
	if err != nil || !found || successor.Job.RequestGeneration != 1 || successor.Job.Capability != domain.CapabilitySummary {
		t.Fatalf("claim requested successor: claim=%+v found=%v err=%v", successor, found, err)
	}
	afterSuccessor, err := enrichment.CompleteSuccess(context.Background(), successor, provider.ObservationDraft{
		Capability: domain.CapabilitySummary, Attribution: domain.AttributionProvider,
		ValueJSON: `{"text":"refreshed result","format":"plain_text"}`, ObservedAt: now.Add(time.Second),
	})
	if err != nil || afterSuccessor.State != domain.CoverageProvided || afterSuccessor.SatisfiedGeneration != 1 || afterSuccessor.ExplicitRequestPending() {
		t.Fatalf("successor did not satisfy request: coverage=%+v err=%v", afterSuccessor, err)
	}
	assertSQLCount(t, st.DB, `SELECT COUNT(*) FROM enrichment_jobs WHERE capability = 'summary'`, 2)
}

func TestEnrichmentFailureRetryPermanentAndLeaseFencing(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	now := captureTestTime.Add(time.Minute)
	enrichment := newTestEnrichmentService(st, now, nil)
	if _, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID); err != nil {
		t.Fatal(err)
	}
	descriptor := testDescriptor(domain.CapabilitySummary)
	claim, found, err := enrichment.Claim(context.Background(), descriptor, "worker", time.Minute)
	if err != nil || !found {
		t.Fatal(err)
	}
	// An expired token cannot publish even before another worker reclaims it.
	enrichment.now = func() time.Time { return now.Add(2 * time.Minute) }
	_, err = enrichment.CompleteSuccess(context.Background(), claim, provider.ObservationDraft{Capability: domain.CapabilitySummary,
		Attribution: domain.AttributionProvider, ValueJSON: `{"text":"late"}`, ObservedAt: now})
	var conflict *repository.EnrichmentConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expired lease completion = %T %v, want conflict", err, err)
	}
	reclaimed, found, err := enrichment.Claim(context.Background(), descriptor, "worker-2", time.Minute)
	if err != nil || !found || reclaimed.Attempt.AttemptNumber != 2 || reclaimed.Attempt.ClaimToken == claim.Attempt.ClaimToken {
		t.Fatalf("reclaim = %+v found=%v err=%v", reclaimed, found, err)
	}
	failed, err := enrichment.CompleteFailure(context.Background(), reclaimed, &provider.AdapterError{
		Class: domain.EnrichmentErrorRetryable, Code: "upstream_timeout", Message: "provider timed out"})
	if err != nil || failed.State != domain.CoverageFailed || !failed.ErrorRetryable {
		t.Fatalf("retryable failure = %+v err=%v", failed, err)
	}
	jobs, err := enrichment.PlanRevision(context.Background(), accepted.FragmentRevisionID)
	if err != nil || countCapabilityJobs(jobs, domain.CapabilitySummary) != 1 {
		t.Fatalf("retry planning = %+v err=%v", jobs, err)
	}
	retryClaim, found, err := enrichment.Claim(context.Background(), descriptor, "worker-3", time.Minute)
	if err != nil || !found {
		t.Fatal(err)
	}
	permanent, err := enrichment.CompleteFailure(context.Background(), retryClaim, &provider.AdapterError{
		Class: domain.EnrichmentErrorPermanent, Code: "unsupported", Message: "provider has no summary"})
	if err != nil || permanent.State != domain.CoverageFailed || permanent.ErrorRetryable {
		t.Fatalf("permanent failure = %+v err=%v", permanent, err)
	}
	jobs, err = enrichment.PlanRevision(context.Background(), accepted.FragmentRevisionID)
	if err != nil || countCapabilityJobs(jobs, domain.CapabilitySummary) != 0 {
		t.Fatalf("permanent failure automatically replanned: %+v err=%v", jobs, err)
	}
	_, jobs, _, err = enrichment.RequestCapability(context.Background(), CapabilityReRequest{FragmentRevisionID: accepted.FragmentRevisionID,
		Capability: domain.CapabilitySummary, IdempotencyKey: "retry-permanent", RequestedBy: "local-user", Reason: "try a new adapter"})
	if err != nil || countCapabilityJobs(jobs, domain.CapabilitySummary) != 1 {
		t.Fatalf("explicit retry after permanent failure = %+v err=%v", jobs, err)
	}
}

func TestEnrichmentExpiryRetainsSelectionAndPlansStaleWork(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	now := captureTestTime.Add(time.Minute)
	enrichment := newTestEnrichmentService(st, now, nil)
	if _, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID); err != nil {
		t.Fatal(err)
	}
	claim, found, err := enrichment.Claim(context.Background(), testDescriptor(domain.CapabilitySummary), "worker", time.Minute)
	if err != nil || !found {
		t.Fatal(err)
	}
	expires := now.Add(time.Minute)
	provided, err := enrichment.CompleteSuccess(context.Background(), claim, provider.ObservationDraft{Capability: domain.CapabilitySummary,
		Attribution: domain.AttributionProvider, ValueJSON: `{"text":"temporary","format":"plain_text"}`,
		ObservedAt: now, ExpiresAt: expires})
	if err != nil || provided.State != domain.CoverageProvided {
		t.Fatalf("temporary result: %+v err=%v", provided, err)
	}
	enrichment.now = func() time.Time { return expires.Add(time.Second) }
	coverage, err := enrichment.Coverage(context.Background(), accepted.FragmentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	stale := findCoverage(coverage, domain.CapabilitySummary)
	if stale.State != domain.CoverageStale || stale.SelectedObservationID != provided.SelectedObservationID {
		t.Fatalf("stale refresh blanked display selection: before=%+v after=%+v", provided, stale)
	}
	jobs, err := enrichment.PlanRevision(context.Background(), accepted.FragmentRevisionID)
	if err != nil || countCapabilityJobs(jobs, domain.CapabilitySummary) != 1 {
		t.Fatalf("stale work not planned: %+v err=%v", jobs, err)
	}
}

func TestEnrichmentPlanningAndClaimsConvergeAcrossHandles(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	firstStore, captureService, _ := openManifestTestService(t, dbPath)
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	services := []*EnrichmentService{
		newTestEnrichmentService(firstStore, captureTestTime.Add(time.Minute), nil),
		newTestEnrichmentService(secondStore, captureTestTime.Add(time.Minute), nil),
	}
	var wg sync.WaitGroup
	plans := make([]repository.EnrichmentPlan, 2)
	errs := make([]error, 2)
	for index := range services {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			plans[index], errs[index] = services[index].PlanCapture(context.Background(), accepted.CaptureID)
		}(index)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if (plans[0].IdempotentReplay == plans[1].IdempotentReplay) || len(plans[0].Jobs)+len(plans[1].Jobs) != 5 {
		t.Fatalf("concurrent plans diverged: %+v %+v", plans[0], plans[1])
	}
	claims := make([]domain.EnrichmentClaim, 2)
	found := make([]bool, 2)
	descriptor := testDescriptor(domain.CapabilitySummary)
	for index := range services {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			claims[index], found[index], errs[index] = services[index].Claim(context.Background(), descriptor, "worker", time.Minute)
		}(index)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if found[0] == found[1] {
		t.Fatalf("expected exactly one claim, found=%v", found)
	}
	assertSQLCount(t, firstStore.DB, `SELECT COUNT(*) FROM enrichment_jobs WHERE capability = 'summary' AND status = 'running'`, 1)
	assertSQLCount(t, firstStore.DB, `SELECT COUNT(*) FROM enrichment_job_attempts WHERE capability = 'summary'`, 1)
}

func TestEnrichmentClaimDoesNotStarveBehindUnsupportedJobs(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	enrichment := newTestEnrichmentService(st, captureTestTime.Add(time.Minute), nil)
	if _, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 101; index++ {
		if _, err := st.DB.Exec(`
INSERT INTO enrichment_jobs (
  id, fragment_id, fragment_revision_id, capability, trigger_kind,
  coverage_version, request_generation, status, created_at, updated_at
) VALUES (?, ?, ?, 'transcript', 'missing', ?, 0, 'queued', ?, ?)`,
			"unsupported-job-"+time.Unix(int64(index), 0).UTC().Format("150405.000000000"), accepted.FragmentID,
			accepted.FragmentRevisionID, index+100, captureTestTime.Add(-time.Hour), captureTestTime.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	claim, found, err := enrichment.Claim(context.Background(), testDescriptor(domain.CapabilitySummary), "summary-worker", time.Minute)
	if err != nil || !found || claim.Job.Capability != domain.CapabilitySummary {
		t.Fatalf("supported job starved behind unsupported backlog: claim=%+v found=%v err=%v", claim, found, err)
	}
}

func TestCredentialResolutionIsEphemeralAndOnlyReferencesPersist(t *testing.T) {
	st, captureService, _ := openManifestTestService(t, filepath.Join(t.TempDir(), "capture.db"))
	accepted, err := captureService.AcceptManifest(context.Background(), captureFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	resolver := &testCredentialResolver{secret: []byte("super-secret-provider-token")}
	enrichment := newTestEnrichmentService(st, captureTestTime.Add(time.Minute), resolver)
	if _, err := enrichment.PlanCapture(context.Background(), accepted.CaptureID); err != nil {
		t.Fatal(err)
	}
	descriptor := testDescriptor(domain.CapabilitySummary)
	if _, _, err := enrichment.Claim(context.Background(), descriptor, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	var observed string
	if err := enrichment.UseCredential(context.Background(), descriptor, "youtube-api", func(secret []byte) error {
		observed = string(secret)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if observed != string(resolver.secret) {
		t.Fatalf("credential resolver callback saw %q", observed)
	}
	var persisted string
	if err := st.DB.QueryRow(`SELECT descriptor_json || effects_json || credential_refs_json || input_asset_digests_json FROM enrichment_job_attempts LIMIT 1`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, observed) || !strings.Contains(persisted, "youtube-api") {
		t.Fatalf("credential value persisted or reference missing: %s", persisted)
	}
}

func newTestEnrichmentService(st *store.Store, now time.Time, credentials provider.CredentialResolver) *EnrichmentService {
	svc := NewEnrichmentService(repository.NewEnrichmentRepository(st.DB), credentials)
	svc.now = func() time.Time { return now }
	return svc
}

func testDescriptor(capability domain.EnrichmentCapability) provider.Descriptor {
	return provider.Descriptor{Adapter: "youtube-provider", Version: "1.0.0", SupportedProviders: []string{"youtube"},
		SupportedSourceKinds: []string{"video"}, SupportedMediaKinds: []domain.MediaKind{domain.MediaVideo},
		Capabilities: []domain.EnrichmentCapability{capability},
		InputSchema:  provider.SchemaRef{ID: "fe.provider.input", Version: "1"},
		OutputSchema: provider.SchemaRef{ID: "fe.provider.output", Version: "1"},
		Effects:      []provider.ExternalEffect{provider.EffectNetworkRead}, NetworkClass: provider.NetworkAuthenticated,
		CredentialReferences: []provider.CredentialReference{{Name: "youtube-api", Purpose: "metadata-read"}}}
}

func countCapabilityJobs(items []domain.EnrichmentJob, capability domain.EnrichmentCapability) int {
	count := 0
	for _, item := range items {
		if item.Capability == capability {
			count++
		}
	}
	return count
}

func findCoverage(items []domain.CapabilityCoverage, capability domain.EnrichmentCapability) domain.CapabilityCoverage {
	for _, item := range items {
		if item.Capability == capability {
			return item
		}
	}
	return domain.CapabilityCoverage{}
}

func assertSQLCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query count = %d, want %d: %s", got, want, query)
	}
}

type testCredentialResolver struct {
	secret []byte
}

func (r *testCredentialResolver) Use(_ context.Context, _ provider.CredentialReference, use func([]byte) error) error {
	return use(append([]byte(nil), r.secret...))
}
