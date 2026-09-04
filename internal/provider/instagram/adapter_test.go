package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
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
	tests := []struct {
		name     string
		raw      string
		wantID   string
		wantKind PostKind
		wantErr  bool
	}{
		{name: "post", raw: "https://www.instagram.com/p/DExample01/", wantID: "DExample01", wantKind: Post},
		{name: "mobile post", raw: "http://m.instagram.com/p/DExample01?utm_source=share#caption", wantID: "DExample01", wantKind: Post},
		{name: "reel", raw: "https://instagram.com/reel/ReelID_01/", wantID: "ReelID_01", wantKind: Reel},
		{name: "reels alias", raw: "https://www.instagr.am/reels/ReelID_01", wantID: "ReelID_01", wantKind: Reel},
		{name: "spoofed host", raw: "https://instagram.com.evil.test/p/DExample01", wantErr: true},
		{name: "userinfo", raw: "https://instagram.com@evil.test/p/DExample01", wantErr: true},
		{name: "port", raw: "https://instagram.com:443/p/DExample01", wantErr: true},
		{name: "profile", raw: "https://instagram.com/maker_account", wantErr: true},
		{name: "extra path", raw: "https://instagram.com/p/DExample01/edit", wantErr: true},
		{name: "encoded id", raw: "https://instagram.com/p/%44Example01", wantErr: true},
		{name: "invalid id", raw: "https://instagram.com/p/x", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerID, kind, err := ParseURL(test.raw)
			if (err != nil) != test.wantErr || providerID != test.wantID || kind != test.wantKind {
				t.Fatalf("ParseURL(%q) = id %q kind %q err %v", test.raw, providerID, kind, err)
			}
		})
	}
}

func TestDescriptorsDeclarePublicAndCredentialReferenceProfiles(t *testing.T) {
	client := &fixtureClient{snapshot: instagramFixture(t)}
	public := mustAdapter(t, client, PublicAccess()).Descriptor()
	if err := public.Validate(); err != nil {
		t.Fatal(err)
	}
	if public.Adapter != AdapterName || public.Version != AdapterVersion || public.NetworkClass != provider.NetworkPublicRead ||
		len(public.CredentialReferences) != 0 || public.InputSchema.ID != "fe.provider.instagram.input" || public.OutputSchema.ID != "fe.provider.instagram.result" ||
		!reflect.DeepEqual(public.Effects, []provider.ExternalEffect{provider.EffectNetworkRead}) {
		t.Fatalf("unexpected public descriptor: %+v", public)
	}
	ref := provider.CredentialReference{Name: "instagram-api-token", Purpose: "metadata-read"}
	authenticated := mustAdapter(t, client, AuthenticatedAccess(ref)).Descriptor()
	if err := authenticated.Validate(); err != nil {
		t.Fatal(err)
	}
	if authenticated.NetworkClass != provider.NetworkAuthenticated || !reflect.DeepEqual(authenticated.CredentialReferences, []provider.CredentialReference{ref}) {
		t.Fatalf("unexpected authenticated descriptor: %+v", authenticated)
	}
	raw, err := json.Marshal(authenticated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-token") || !strings.Contains(string(raw), "instagram-api-token") {
		t.Fatalf("descriptor did not preserve reference-only boundary: %s", raw)
	}
	if _, err := New(client, AuthenticatedAccess(provider.CredentialReference{Name: "token=super-secret-token", Purpose: "metadata-read"})); err == nil {
		t.Fatal("accepted credential-like value instead of opaque reference name")
	}
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilityDescription, domain.CapabilityGalleryManifest,
		domain.CapabilityOriginalMedia, domain.CapabilityThumbnailOrPoster, domain.CapabilityTags, domain.CapabilityEntities} {
		if !public.Supports(capability, "instagram", "gallery", []domain.MediaKind{domain.MediaImage, domain.MediaVideo}) {
			t.Fatalf("descriptor does not support %s", capability)
		}
	}
}

func TestIdentityCanonicalizesPostAndReelWithTypedEntities(t *testing.T) {
	snapshot := instagramFixture(t)
	client := &fixtureClient{snapshot: snapshot}
	adapter := mustAdapter(t, client, PublicAccess())
	result, err := adapter.ResolveCanonicalIdentity(context.Background(), instagramInput(domain.CapabilityEntities))
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity.ProviderItemID != snapshot.ProviderID || result.Identity.SourceItemKey != "instagram:p:"+snapshot.ProviderID ||
		result.Identity.CanonicalURL != "https://www.instagram.com/p/"+snapshot.ProviderID+"/" {
		t.Fatalf("post identity = %+v", result.Identity)
	}
	var entities provider.EntityListValue
	if err := json.Unmarshal([]byte(result.Observation.ValueJSON), &entities); err != nil {
		t.Fatal(err)
	}
	if len(entities.Entities) != 1 || entities.Entities[0] != (provider.EntityValue{Kind: "instagram_post", Value: snapshot.ProviderID}) {
		t.Fatalf("identity entities = %+v", entities)
	}

	reelSnapshot := snapshot
	reelSnapshot.ProviderID = "ReelID_01"
	reelSnapshot.Kind = Reel
	reelSnapshot.CanonicalURL = "https://www.instagram.com/reels/ReelID_01/"
	reelClient := &fixtureClient{snapshot: reelSnapshot}
	reelAdapter := mustAdapter(t, reelClient, PublicAccess())
	reelInput := instagramInput(domain.CapabilityEntities)
	reelInput.Source.ProviderItemID = "ReelID_01"
	reelInput.Source.SourceItemKey = "instagram:reel:ReelID_01"
	reelInput.Source.SubmittedURL = "https://www.instagr.am/reels/ReelID_01/?share=1"
	reelInput.Source.CanonicalURL = "https://www.instagram.com/reel/ReelID_01/"
	reelResult, err := reelAdapter.ResolveCanonicalIdentity(context.Background(), reelInput)
	if err != nil {
		t.Fatal(err)
	}
	if reelResult.Identity.SourceItemKey != "instagram:reel:ReelID_01" || reelResult.Identity.CanonicalURL != "https://www.instagram.com/reel/ReelID_01/" ||
		!strings.Contains(reelResult.Observation.ValueJSON, "instagram_reel") {
		t.Fatalf("reel identity = %+v observation=%s", reelResult.Identity, reelResult.Observation.ValueJSON)
	}
}

func TestMetadataHonorsSingleClaimAndDropsRawOEmbed(t *testing.T) {
	adapter := mustAdapter(t, &fixtureClient{snapshot: instagramFixture(t)}, PublicAccess())
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilityDescription, domain.CapabilityTags, domain.CapabilityEntities} {
		t.Run(string(capability), func(t *testing.T) {
			result, err := adapter.ProvideMetadata(context.Background(), instagramInput(capability))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Observations) != 1 || result.Observations[0].Capability != capability {
				t.Fatalf("cross-capability or missing result: %+v", result.Observations)
			}
			if err := result.Observations[0].Validate(); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(string(raw)), "<script") || strings.Contains(string(raw), "must-not-cross") || strings.Contains(string(raw), `"html"`) {
				t.Fatalf("raw oEmbed crossed boundary: %s", raw)
			}
		})
	}
	_, err := adapter.ProvideMetadata(context.Background(), instagramInput(domain.CapabilitySummary))
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "capability_mismatch")

	snapshot := instagramFixture(t)
	snapshot.OEmbedJSON = json.RawMessage(`{"provider_name":"Instagram","title":"<iframe src='https://evil.test'></iframe>"}`)
	unsafe := mustAdapter(t, &fixtureClient{snapshot: snapshot}, PublicAccess())
	_, err = unsafe.ProvideMetadata(context.Background(), instagramInput(domain.CapabilityDescription))
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "unsafe_response")
}

func TestCarouselOrderAndStableIdentity(t *testing.T) {
	snapshot := instagramFixture(t)
	adapter := mustAdapter(t, &fixtureClient{snapshot: snapshot}, PublicAccess())
	request := provider.MediaRequest{Input: instagramInput(domain.CapabilityGalleryManifest)}
	first, err := adapter.AcquireMedia(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.AcquireMedia(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same carousel fixture was not deterministic")
	}
	if len(first.Manifest) != 3 || len(first.Observations) != 1 || first.Observations[0].Capability != domain.CapabilityGalleryManifest {
		t.Fatalf("gallery result = %+v", first)
	}
	var refs provider.ReferenceListValue
	if err := json.Unmarshal([]byte(first.Observations[0].ValueJSON), &refs); err != nil {
		t.Fatal(err)
	}
	for index, item := range first.Manifest {
		if item.Attachment.Position != index || refs.MediaAssetIDs[index] != item.Asset.ID || item.Asset.ProviderMediaID != snapshot.Items[index].MediaID {
			t.Fatalf("position %d lost ordered provider identity: item=%+v refs=%+v", index, item, refs)
		}
	}
	if first.Manifest[2].Variants[0].AcquisitionState != domain.AcquisitionFailed || first.Manifest[2].Variants[0].Failure == nil ||
		first.Manifest[2].Variants[0].Failure.Message != "Instagram provider reported original variant unavailable" {
		t.Fatalf("partial failed item was not preserved safely: %+v", first.Manifest[2])
	}

	reordered := instagramFixture(t)
	reordered.Items[0], reordered.Items[1] = reordered.Items[1], reordered.Items[0]
	reorderedResult, err := mustAdapter(t, &fixtureClient{snapshot: reordered}, PublicAccess()).AcquireMedia(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	stable := map[string]string{}
	for _, item := range first.Manifest {
		stable[item.Asset.ProviderMediaID] = item.Asset.ID
	}
	for index, item := range reorderedResult.Manifest {
		if stable[item.Asset.ProviderMediaID] != item.Asset.ID || item.Attachment.Position != index {
			t.Fatalf("carousel reorder changed asset identity: %+v", item)
		}
	}
	if reorderedResult.Manifest[0].Asset.ProviderMediaID != "ig-video-bravo" || reorderedResult.Manifest[1].Asset.ProviderMediaID != "ig-image-alpha" {
		t.Fatalf("provider order not preserved: %+v", reorderedResult.Manifest)
	}
}

func TestRepresentationCapabilitiesExcludeFailedSiblings(t *testing.T) {
	adapter := mustAdapter(t, &fixtureClient{snapshot: instagramFixture(t)}, PublicAccess())
	tests := []struct {
		capability domain.EnrichmentCapability
		wantKinds  []domain.AssetVariantKind
	}{
		{domain.CapabilityOriginalMedia, []domain.AssetVariantKind{domain.VariantOriginal, domain.VariantOriginal}},
		{domain.CapabilityThumbnailOrPoster, []domain.AssetVariantKind{domain.VariantThumbnail, domain.VariantPoster}},
	}
	for _, test := range tests {
		t.Run(string(test.capability), func(t *testing.T) {
			result, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: instagramInput(test.capability)})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Manifest) != 3 || result.Manifest[2].Variants[0].AcquisitionState != domain.AcquisitionFailed || len(result.Observations) != 1 {
				t.Fatalf("partial media result = %+v", result)
			}
			var refs provider.ReferenceListValue
			if err := json.Unmarshal([]byte(result.Observations[0].ValueJSON), &refs); err != nil {
				t.Fatal(err)
			}
			if len(refs.AssetVariantIDs) != len(test.wantKinds) {
				t.Fatalf("failed variant was referenced: %+v", refs)
			}
			for index, ref := range refs.AssetVariantIDs {
				found := false
				for _, item := range result.Manifest {
					for _, variant := range item.Variants {
						if variant.ID == ref && variant.Kind == test.wantKinds[index] && variant.AcquisitionState != domain.AcquisitionFailed {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("reference %q is not a usable %s: %+v", ref, test.wantKinds[index], result.Manifest)
				}
			}
		})
	}

	allFailed := instagramFixture(t)
	allFailed.Items[0].Original = &VariantCandidate{Failure: &VariantFailure{Code: "cdn_expired", Retryable: true}}
	allFailed.Items[1].Original = &VariantCandidate{Failure: &VariantFailure{Code: "auth_required", Retryable: false}}
	failedResult, err := mustAdapter(t, &fixtureClient{snapshot: allFailed}, PublicAccess()).AcquireMedia(context.Background(),
		provider.MediaRequest{Input: instagramInput(domain.CapabilityOriginalMedia)})
	assertAdapterError(t, err, domain.EnrichmentErrorRetryable, "media_temporarily_unavailable")
	if len(failedResult.Observations) != 0 || len(failedResult.Manifest) != 3 {
		t.Fatalf("failure-only result invented coverage or dropped partial manifest: %+v", failedResult)
	}
	for _, item := range failedResult.Manifest {
		for _, variant := range item.Variants {
			if variant.Kind == domain.VariantOriginal && variant.AcquisitionState != domain.AcquisitionFailed {
				t.Fatalf("unexpected usable original: %+v", variant)
			}
		}
	}
}

func TestMediaFiltersAndURLsStayTypedAndAllowlisted(t *testing.T) {
	adapter := mustAdapter(t, &fixtureClient{snapshot: instagramFixture(t)}, PublicAccess())
	all, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: instagramInput(domain.CapabilityOriginalMedia)})
	if err != nil {
		t.Fatal(err)
	}
	selectedID := all.Manifest[1].Asset.ID
	filtered, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: instagramInput(domain.CapabilityOriginalMedia),
		MediaAssetID: selectedID, VariantKind: domain.VariantOriginal})
	if err != nil || len(filtered.Manifest) != 1 || filtered.Manifest[0].Asset.ID != selectedID || len(filtered.Manifest[0].Variants) != 1 {
		t.Fatalf("filtered media result=%+v err=%v", filtered, err)
	}

	badURLs := []string{
		"http://scontent.cdninstagram.com/a.jpg",
		"https://user:pass@scontent.cdninstagram.com/a.jpg",
		"https://scontent.cdninstagram.com:443/a.jpg",
		"https://cdninstagram.com.evil.test/a.jpg",
		"https://127.0.0.1/a.jpg",
	}
	for _, badURL := range badURLs {
		t.Run(badURL, func(t *testing.T) {
			snapshot := instagramFixture(t)
			snapshot.Items[0].Original.SourceURL = badURL
			bad := mustAdapter(t, &fixtureClient{snapshot: snapshot}, PublicAccess())
			_, err := bad.AcquireMedia(context.Background(), provider.MediaRequest{Input: instagramInput(domain.CapabilityOriginalMedia)})
			assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")
		})
	}
	snapshot := instagramFixture(t)
	snapshot.OEmbedJSON = json.RawMessage(`{"provider_name":"Instagram","provider_url":"https://www.instagram.com/","thumbnail_url":"https://cdninstagram.com.evil.test/t.jpg"}`)
	badOEmbed := mustAdapter(t, &fixtureClient{snapshot: snapshot}, PublicAccess())
	_, err = badOEmbed.ProvideMetadata(context.Background(), instagramInput(domain.CapabilityDescription))
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")
}

func TestRepresentationExpiryAndCandidateUnionAreHonest(t *testing.T) {
	snapshot := instagramFixture(t)
	firstExpiry := snapshot.ObservedAt.Add(20 * time.Minute)
	secondExpiry := snapshot.ObservedAt.Add(10 * time.Minute)
	snapshot.Items[0].Original.SourceExpiresAt = firstExpiry
	snapshot.Items[1].Original.SourceExpiresAt = secondExpiry
	result, err := mustAdapter(t, &fixtureClient{snapshot: snapshot}, PublicAccess()).AcquireMedia(context.Background(),
		provider.MediaRequest{Input: instagramInput(domain.CapabilityOriginalMedia)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 1 || !result.Observations[0].ExpiresAt.Equal(secondExpiry) {
		t.Fatalf("earliest representation expiry not propagated: %+v", result.Observations)
	}

	ambiguous := instagramFixture(t)
	ambiguous.Items[0].Original.Failure = &VariantFailure{Code: "cdn_expired", Retryable: true}
	_, err = mustAdapter(t, &fixtureClient{snapshot: ambiguous}, PublicAccess()).AcquireMedia(context.Background(),
		provider.MediaRequest{Input: instagramInput(domain.CapabilityOriginalMedia)})
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")

	expired := instagramFixture(t)
	expired.Items[0].Original.SourceExpiresAt = expired.ObservedAt.Add(-time.Second)
	_, err = mustAdapter(t, &fixtureClient{snapshot: expired}, PublicAccess()).AcquireMedia(context.Background(),
		provider.MediaRequest{Input: instagramInput(domain.CapabilityOriginalMedia)})
	assertAdapterError(t, err, domain.EnrichmentErrorPermanent, "invalid_response")
}

func TestStrictSnapshotSchemaAndClassifiedAccessFailures(t *testing.T) {
	raw, err := os.ReadFile("testdata/carousel.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("accepted trailing JSON value")
	}
	if _, err := DecodeSnapshot([]byte(`{"provider_id":"DExample01","unknown":true}`)); err == nil {
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
			secret := "secret-instagram-response"
			adapter := mustAdapter(t, &fixtureClient{err: &ClientError{Kind: test.kind, Cause: errors.New(secret)}}, PublicAccess())
			_, err := adapter.ProvideMetadata(context.Background(), instagramInput(domain.CapabilityDescription))
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

func instagramFixture(t *testing.T) Snapshot {
	t.Helper()
	raw, err := os.ReadFile("testdata/carousel.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func instagramInput(capability domain.EnrichmentCapability) provider.Input {
	return provider.Input{FragmentID: "fragment-instagram", FragmentRevisionID: "revision-instagram", Capability: capability,
		MaterialDigest: domain.DigestText("instagram-material"), Source: domain.SourceIdentity{
			SourceRegistrationID: "provider-instagram", Provider: "instagram", ProviderItemID: "DExample01",
			SourceItemKey: "instagram:p:DExample01", SegmentKey: domain.DefaultSegmentKey,
			SubmittedURL:  "https://m.instagram.com/p/DExample01/?utm_source=share",
			CanonicalURL:  "https://www.instagram.com/p/DExample01/",
			SourceAdapter: domain.AdapterVersion{Adapter: "browser-instagram", Version: "1.0.0"},
			Canonicalizer: domain.AdapterVersion{Adapter: "browser-instagram", Version: "1.0.0"},
		}}
}

func mustAdapter(t *testing.T, client Client, profile AccessProfile) *Adapter {
	t.Helper()
	adapter, err := New(client, profile)
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
