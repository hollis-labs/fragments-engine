package pinterest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

type fixtureClient struct {
	snapshot Snapshot
	err      error
	requests []LookupRequest
}

func (c *fixtureClient) Lookup(_ context.Context, request LookupRequest) (Snapshot, error) {
	c.requests = append(c.requests, request)
	return c.snapshot, c.err
}

func TestParseURLMatrix(t *testing.T) {
	const pinID = "123456789012345678"
	tests := []struct {
		name      string
		raw       string
		wantID    string
		wantShort string
		wantErr   bool
	}{
		{name: "canonical", raw: "https://www.pinterest.com/pin/" + pinID + "/", wantID: pinID},
		{name: "mobile and query", raw: "http://m.pinterest.com/pin/" + pinID + "?utm_source=share#detail", wantID: pinID},
		{name: "country host", raw: "https://www.pinterest.co.uk/pin/" + pinID, wantID: pinID},
		{name: "short reference", raw: "https://pin.it/A_bc-123", wantShort: "A_bc-123"},
		{name: "spoofed host", raw: "https://www.pinterest.com.evil.test/pin/" + pinID, wantErr: true},
		{name: "userinfo", raw: "https://www.pinterest.com@evil.test/pin/" + pinID, wantErr: true},
		{name: "port", raw: "https://www.pinterest.com:443/pin/" + pinID, wantErr: true},
		{name: "non numeric", raw: "https://www.pinterest.com/pin/not-a-pin", wantErr: true},
		{name: "extra path", raw: "https://www.pinterest.com/pin/" + pinID + "/edit", wantErr: true},
		{name: "encoded path", raw: "https://www.pinterest.com/pin/%31%32%33%34%35%36", wantErr: true},
		{name: "short spoof", raw: "https://pin.it.evil.test/A_bc-123", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pinID, shortCode, err := ParseURL(test.raw)
			if (err != nil) != test.wantErr || pinID != test.wantID || shortCode != test.wantShort {
				t.Fatalf("ParseURL(%q) = id %q short %q err %v", test.raw, pinID, shortCode, err)
			}
		})
	}
}

func TestDescriptorDeclaresTypedExecutionBoundary(t *testing.T) {
	adapter := mustAdapter(t, &fixtureClient{snapshot: pinterestFixture(t)})
	descriptor := adapter.Descriptor()
	if err := descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
	if descriptor.Adapter != AdapterName || descriptor.Version != AdapterVersion ||
		descriptor.InputSchema.ID != "fe.provider.pinterest.input" || descriptor.OutputSchema.ID != "fe.provider.pinterest.result" ||
		descriptor.NetworkClass != provider.NetworkPublicRead || !reflect.DeepEqual(descriptor.Effects, []provider.ExternalEffect{provider.EffectNetworkRead}) ||
		len(descriptor.CredentialReferences) != 0 {
		t.Fatalf("unexpected Pinterest descriptor: %+v", descriptor)
	}
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilityTitle, domain.CapabilityDescription,
		domain.CapabilityGalleryManifest, domain.CapabilityOriginalMedia, domain.CapabilityThumbnailOrPoster,
		domain.CapabilityTags, domain.CapabilityEntities} {
		if !descriptor.Supports(capability, "pinterest", "image", []domain.MediaKind{domain.MediaImage}) {
			t.Fatalf("descriptor does not support %s", capability)
		}
	}
}

func TestShortURLIdentityComesOnlyFromAllowlistedFinalURL(t *testing.T) {
	snapshot := pinterestFixture(t)
	client := &fixtureClient{snapshot: snapshot}
	adapter := mustAdapter(t, client)
	input := pinterestInput(domain.CapabilityEntities)
	input.Source.SubmittedURL = "https://pin.it/A_bc-123"
	input.Source.CanonicalURL = ""
	input.Source.ProviderItemID = ""
	input.Source.SourceItemKey = "pinterest:short:A_bc-123"

	result, err := adapter.ResolveCanonicalIdentity(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity.ProviderItemID != snapshot.PinID || result.Identity.SourceItemKey != "pinterest:pin:"+snapshot.PinID ||
		strings.Contains(result.Identity.SourceItemKey, "A_bc-123") {
		t.Fatalf("short token became identity: %+v", result.Identity)
	}
	if len(client.requests) != 1 || client.requests[0].DirectPinID != "" || client.requests[0].ShortCode != "A_bc-123" {
		t.Fatalf("short lookup request = %+v", client.requests)
	}
	if err := result.Observation.Validate(); err != nil {
		t.Fatalf("identity observation invalid: %v", err)
	}

	spoofed := pinterestFixture(t)
	spoofed.CanonicalURL = "https://www.pinterest.com.evil.test/pin/" + spoofed.PinID
	bad := mustAdapter(t, &fixtureClient{snapshot: spoofed})
	_, err = bad.ResolveCanonicalIdentity(context.Background(), input)
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")
}

func TestMetadataHonorsSingleClaimAndDropsRawOEmbed(t *testing.T) {
	adapter := mustAdapter(t, &fixtureClient{snapshot: pinterestFixture(t)})
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilityTitle, domain.CapabilityDescription, domain.CapabilityTags, domain.CapabilityEntities} {
		t.Run(string(capability), func(t *testing.T) {
			result, err := adapter.ProvideMetadata(context.Background(), pinterestInput(capability))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Observations) != 1 || result.Observations[0].Capability != capability {
				t.Fatalf("cross-capability or missing result: %+v", result.Observations)
			}
			if err := result.Observations[0].Validate(); err != nil {
				t.Fatalf("invalid typed observation: %v", err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(string(raw)), "<script") || strings.Contains(string(raw), "must-not-cross") || strings.Contains(string(raw), `"html"`) {
				t.Fatalf("raw oEmbed data crossed boundary: %s", raw)
			}
		})
	}
	_, err := adapter.ProvideMetadata(context.Background(), pinterestInput(domain.CapabilitySummary))
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "capability_mismatch")

	snapshot := pinterestFixture(t)
	snapshot.OEmbedJSON = json.RawMessage(`{"provider_name":"Pinterest","title":"<script>alert(1)</script>"}`)
	unsafe := mustAdapter(t, &fixtureClient{snapshot: snapshot})
	_, err = unsafe.ProvideMetadata(context.Background(), pinterestInput(domain.CapabilityTitle))
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "unsafe_response")
}

func TestAcquireMediaIsDeterministicAndCapabilitySpecific(t *testing.T) {
	adapter := mustAdapter(t, &fixtureClient{snapshot: pinterestFixture(t)})
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilityGalleryManifest, domain.CapabilityOriginalMedia, domain.CapabilityThumbnailOrPoster} {
		t.Run(string(capability), func(t *testing.T) {
			request := provider.MediaRequest{Input: pinterestInput(capability)}
			first, err := adapter.AcquireMedia(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			second, err := adapter.AcquireMedia(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("same fixture produced different result\nfirst=%+v\nsecond=%+v", first, second)
			}
			if len(first.Manifest) != 1 || len(first.Observations) != 1 || first.Observations[0].Capability != capability {
				t.Fatalf("unexpected media result: %+v", first)
			}
			if err := first.Observations[0].Validate(); err != nil {
				t.Fatal(err)
			}
			var refs provider.ReferenceListValue
			if err := json.Unmarshal([]byte(first.Observations[0].ValueJSON), &refs); err != nil {
				t.Fatal(err)
			}
			switch capability {
			case domain.CapabilityGalleryManifest:
				if !reflect.DeepEqual(refs.MediaAssetIDs, []string{first.Manifest[0].Asset.ID}) || len(refs.AssetVariantIDs) != 0 {
					t.Fatalf("gallery refs = %+v", refs)
				}
			case domain.CapabilityOriginalMedia:
				if len(refs.AssetVariantIDs) != 1 || first.Manifest[0].Variants[0].Kind != domain.VariantOriginal || refs.AssetVariantIDs[0] != first.Manifest[0].Variants[0].ID {
					t.Fatalf("original refs = %+v manifest=%+v", refs, first.Manifest)
				}
			case domain.CapabilityThumbnailOrPoster:
				if len(refs.AssetVariantIDs) != 1 || first.Manifest[0].Variants[1].Kind != domain.VariantThumbnail || refs.AssetVariantIDs[0] != first.Manifest[0].Variants[1].ID {
					t.Fatalf("thumbnail refs = %+v manifest=%+v", refs, first.Manifest)
				}
			}
		})
	}
}

func TestFailedRepresentationDoesNotProvideCoverageButRemainsInManifest(t *testing.T) {
	snapshot := pinterestFixture(t)
	snapshot.Image.Original = &VariantCandidate{Failure: &VariantFailure{Code: "cdn_expired", Retryable: true}}
	adapter := mustAdapter(t, &fixtureClient{snapshot: snapshot})

	failed, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: pinterestInput(domain.CapabilityOriginalMedia)})
	assertAdapterError(t, err, domain.EnrichmentErrorRetryable, "media_temporarily_unavailable")
	if len(failed.Observations) != 0 || len(failed.Manifest) != 1 || failed.Manifest[0].Variants[0].AcquisitionState != domain.AcquisitionFailed ||
		failed.Manifest[0].Variants[0].Failure == nil || !failed.Manifest[0].Variants[0].Failure.Retryable {
		t.Fatalf("failure-only representation was not preserved honestly: %+v", failed)
	}
	gallery, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: pinterestInput(domain.CapabilityGalleryManifest)})
	if err != nil || len(gallery.Observations) != 1 || gallery.Manifest[0].Variants[0].AcquisitionState != domain.AcquisitionFailed {
		t.Fatalf("logical gallery should survive variant failure: result=%+v err=%v", gallery, err)
	}
	thumbnail, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: pinterestInput(domain.CapabilityThumbnailOrPoster)})
	if err != nil || len(thumbnail.Observations) != 1 {
		t.Fatalf("successful sibling did not survive: result=%+v err=%v", thumbnail, err)
	}
	permanentSnapshot := pinterestFixture(t)
	permanentSnapshot.Image.Original = &VariantCandidate{Failure: &VariantFailure{Code: "access_denied", Retryable: false}}
	_, err = mustAdapter(t, &fixtureClient{snapshot: permanentSnapshot}).AcquireMedia(context.Background(),
		provider.MediaRequest{Input: pinterestInput(domain.CapabilityOriginalMedia)})
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "media_unavailable")
}

func TestMediaAndOEmbedURLsAreProviderAllowlisted(t *testing.T) {
	badURLs := []string{
		"http://i.pinimg.com/pin.jpg",
		"https://user:pass@i.pinimg.com/pin.jpg",
		"https://i.pinimg.com:443/pin.jpg",
		"https://i.pinimg.com.evil.test/pin.jpg",
		"https://127.0.0.1/pin.jpg",
	}
	for _, badURL := range badURLs {
		t.Run(badURL, func(t *testing.T) {
			snapshot := pinterestFixture(t)
			snapshot.Image.Original.SourceURL = badURL
			adapter := mustAdapter(t, &fixtureClient{snapshot: snapshot})
			_, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: pinterestInput(domain.CapabilityOriginalMedia)})
			assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")
		})
	}
	snapshot := pinterestFixture(t)
	snapshot.OEmbedJSON = json.RawMessage(`{"provider_name":"Pinterest","provider_url":"https://www.pinterest.com/","thumbnail_url":"https://i.pinimg.com.evil.test/t.jpg"}`)
	adapter := mustAdapter(t, &fixtureClient{snapshot: snapshot})
	_, err := adapter.ProvideMetadata(context.Background(), pinterestInput(domain.CapabilityTitle))
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")
}

func TestRepresentationExpiryAndCandidateUnionAreHonest(t *testing.T) {
	snapshot := pinterestFixture(t)
	expires := snapshot.ObservedAt.Add(15 * time.Minute)
	snapshot.Image.Original.SourceExpiresAt = expires
	adapter := mustAdapter(t, &fixtureClient{snapshot: snapshot})
	result, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: pinterestInput(domain.CapabilityOriginalMedia)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 1 || !result.Observations[0].ExpiresAt.Equal(expires) {
		t.Fatalf("representation expiry not propagated: %+v", result.Observations)
	}

	ambiguous := pinterestFixture(t)
	ambiguous.Image.Original.Failure = &VariantFailure{Code: "cdn_expired", Retryable: true}
	_, err = mustAdapter(t, &fixtureClient{snapshot: ambiguous}).AcquireMedia(context.Background(),
		provider.MediaRequest{Input: pinterestInput(domain.CapabilityOriginalMedia)})
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")

	expired := pinterestFixture(t)
	expired.Image.Original.SourceExpiresAt = expired.ObservedAt.Add(-time.Second)
	_, err = mustAdapter(t, &fixtureClient{snapshot: expired}).AcquireMedia(context.Background(),
		provider.MediaRequest{Input: pinterestInput(domain.CapabilityOriginalMedia)})
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")
}

func TestStrictSnapshotSchemaAndClassifiedClientErrors(t *testing.T) {
	raw, err := os.ReadFile("testdata/pin.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("accepted trailing JSON value")
	}
	if _, err := DecodeSnapshot([]byte(`{"pin_id":"123456","unknown":true}`)); err == nil {
		t.Fatal("accepted unknown top-level field")
	}

	tests := []struct {
		kind      ClientFailureKind
		wantClass domain.EnrichmentErrorClass
		wantCode  string
	}{
		{FailureNotFound, domain.EnrichmentErrorPermanent, "not_found"},
		{FailureUnsupported, domain.EnrichmentErrorPermanent, "unsupported"},
		{FailureAuthentication, domain.EnrichmentErrorPermanent, "authentication_required"},
		{FailureRateLimited, domain.EnrichmentErrorRetryable, "rate_limited"},
		{FailureTransient, domain.EnrichmentErrorRetryable, "transient"},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			secret := "secret-provider-response"
			adapter := mustAdapter(t, &fixtureClient{err: &ClientError{Kind: test.kind, Cause: errors.New(secret)}})
			_, err := adapter.ProvideMetadata(context.Background(), pinterestInput(domain.CapabilityTitle))
			adapterErr := assertAdapterError(t, err, test.wantClass, test.wantCode)
			raw, marshalErr := json.Marshal(adapterErr)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(raw), secret) || strings.Contains(adapterErr.Error(), secret) {
				t.Fatalf("client cause leaked: json=%s error=%s", raw, adapterErr)
			}
		})
	}
}

func TestEnrichmentServiceAppendsProviderTitleWithoutReplacingBrowserTitle(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pinterest-enrichment.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mediaRepo := repository.NewMediaRepository(st.DB)
	blobs, err := blobstore.NewFileStore(filepath.Join(filepath.Dir(dbPath), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	captureSvc := service.NewCaptureService(repository.NewCaptureRepository(st.DB), service.NewMediaService(mediaRepo, blobs))

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "browser-capture-reader", "v1", "fixtures", "valid-capture-youtube.json"))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := capturecontract.DecodeCaptureEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	envelope.CaptureID = "01KPINTEREST00000000000000"
	envelope.Source.Provider = "pinterest"
	envelope.Source.ProviderItemID = "123456789012345678"
	envelope.Source.SourceItemKey = "pinterest:pin:123456789012345678"
	envelope.Source.SubmittedURL = "https://www.pinterest.com/pin/123456789012345678/"
	envelope.Source.CanonicalURL = envelope.Source.SubmittedURL
	envelope.Source.Canonicalizer = &capturecontract.Canonicalizer{Adapter: "browser-pinterest", Version: "1.0.0"}
	envelope.Document.Title = "Rich browser title"
	envelope.Media = []capturecontract.CaptureMediaItem{{
		ClientMediaID: "browser-pin-image", ProviderMediaID: "pin-media-001", Kind: "image", Role: "primary", Position: 0,
		SourceLocator: "https://i.pinimg.com/originals/aa/bb/cc/pin.jpg", DefaultCustody: "mirror",
		Variants: []capturecontract.CaptureAssetVariant{{ClientVariantID: "browser-original", Kind: "original",
			SourceURL: "https://i.pinimg.com/originals/aa/bb/cc/pin.jpg", MIMEType: "image/jpeg",
			TransferPreference: "browser_preferred", RequestedCustody: "mirror"}},
	}}
	envelope.Extraction.Adapter = "browser-pinterest"
	envelope.Extraction.AdapterVersion = "1.0.0"
	envelope.Extraction.ObservedCapabilities = []string{"title", "description", "body", "original_media"}
	manifest, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := captureSvc.AcceptManifest(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}

	enrichment := service.NewEnrichmentService(repository.NewEnrichmentRepository(st.DB), nil)
	coverage, _, replay, err := enrichment.RequestCapability(ctx, service.CapabilityReRequest{
		FragmentRevisionID: accepted.FragmentRevisionID, Capability: domain.CapabilityTitle,
		IdempotencyKey: "refresh-pinterest-title", RequestedBy: "local-user", Reason: "provider gap check",
	})
	if err != nil || replay || coverage.SelectedObservationID == "" {
		t.Fatalf("request title refresh: coverage=%+v replay=%v err=%v", coverage, replay, err)
	}
	adapter := mustAdapter(t, &fixtureClient{snapshot: pinterestFixture(t)})
	descriptor := adapter.Descriptor()
	descriptor.Capabilities = []domain.EnrichmentCapability{domain.CapabilityTitle}
	claim, found, err := enrichment.Claim(ctx, descriptor, "pinterest-worker", time.Minute)
	if err != nil || !found || claim.Job.Capability != domain.CapabilityTitle {
		t.Fatalf("claim title: claim=%+v found=%v err=%v", claim, found, err)
	}
	result, err := adapter.ProvideMetadata(ctx, service.ProviderInput(claim))
	if err != nil || len(result.Observations) != 1 {
		t.Fatalf("provide title: result=%+v err=%v", result, err)
	}
	resolved, err := enrichment.CompleteSuccess(ctx, claim, result.Observations[0])
	if err != nil {
		t.Fatal(err)
	}
	observations, err := repository.NewEnrichmentRepository(st.DB).ListObservations(ctx, accepted.FragmentRevisionID, domain.CapabilityTitle)
	if err != nil || len(observations) != 2 {
		t.Fatalf("title observations=%+v err=%v", observations, err)
	}
	selectedAttribution := domain.AttributionSource("")
	providerSeen := false
	for _, observation := range observations {
		if observation.ID == resolved.SelectedObservationID {
			selectedAttribution = observation.Attribution
		}
		providerSeen = providerSeen || observation.Attribution == domain.AttributionProvider
	}
	if !providerSeen || selectedAttribution != domain.AttributionSourceMaterial {
		t.Fatalf("provider did not append or replaced browser selection: selected=%s observations=%+v", selectedAttribution, observations)
	}
	_, replayJobs, replay, err := enrichment.RequestCapability(ctx, service.CapabilityReRequest{
		FragmentRevisionID: accepted.FragmentRevisionID, Capability: domain.CapabilityTitle,
		IdempotencyKey: "refresh-pinterest-title", RequestedBy: "local-user", Reason: "provider gap check",
	})
	if err != nil || !replay || len(replayJobs) != 0 {
		t.Fatalf("exact request replay duplicated work: jobs=%+v replay=%v err=%v", replayJobs, replay, err)
	}
	afterReplay, err := repository.NewEnrichmentRepository(st.DB).ListObservations(ctx, accepted.FragmentRevisionID, domain.CapabilityTitle)
	if err != nil || len(afterReplay) != 2 {
		t.Fatalf("exact replay duplicated observations: observations=%+v err=%v", afterReplay, err)
	}
}

func pinterestFixture(t *testing.T) Snapshot {
	t.Helper()
	raw, err := os.ReadFile("testdata/pin.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func pinterestInput(capability domain.EnrichmentCapability) provider.Input {
	return provider.Input{FragmentID: "fragment-pinterest", FragmentRevisionID: "revision-pinterest", Capability: capability,
		MaterialDigest: domain.DigestText("pinterest-material"), Source: domain.SourceIdentity{
			SourceRegistrationID: "provider-pinterest", Provider: "pinterest", ProviderItemID: "123456789012345678",
			SourceItemKey: "pinterest:pin:123456789012345678", SegmentKey: domain.DefaultSegmentKey,
			SubmittedURL:  "https://m.pinterest.com/pin/123456789012345678/?utm_source=share",
			CanonicalURL:  "https://www.pinterest.com/pin/123456789012345678/",
			SourceAdapter: domain.AdapterVersion{Adapter: "browser-pinterest", Version: "1.0.0"},
			Canonicalizer: domain.AdapterVersion{Adapter: "browser-pinterest", Version: "1.0.0"},
		}}
}

func mustAdapter(t *testing.T, client Client) *Adapter {
	t.Helper()
	adapter, err := New(client)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func assertAdapterError(t *testing.T, err error, class domain.EnrichmentErrorClass, code string) *provider.AdapterError {
	t.Helper()
	var adapterErr *provider.AdapterError
	if !errors.As(err, &adapterErr) {
		t.Fatalf("error = %T %v, want AdapterError", err, err)
	}
	if adapterErr.Class != class || adapterErr.Code != code {
		t.Fatalf("adapter error = %+v, want class=%s code=%s", adapterErr, class, code)
	}
	if err := adapterErr.Validate(); err != nil {
		t.Fatalf("invalid classified error: %v", err)
	}
	return adapterErr
}
