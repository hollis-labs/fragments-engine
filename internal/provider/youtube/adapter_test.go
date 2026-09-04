package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

const testVideoID = "3RmtNXqnreI"

type fixtureClient struct {
	video         Video
	videoErr      error
	transcript    Transcript
	transcriptErr error
	videoCalls    int
	textCalls     int
}

func (f *fixtureClient) LookupVideo(_ context.Context, _ string) (Video, error) {
	f.videoCalls++
	return f.video, f.videoErr
}

func (f *fixtureClient) LookupTranscript(_ context.Context, _ string, _ []string) (Transcript, error) {
	f.textCalls++
	return f.transcript, f.transcriptErr
}

func TestParseVideoURLSupportsCanonicalYouTubeForms(t *testing.T) {
	for _, raw := range []string{
		"https://www.youtube.com/watch?v=" + testVideoID,
		"https://youtube.com/watch?v=" + testVideoID + "&t=15",
		"https://m.youtube.com/watch?v=" + testVideoID,
		"https://music.youtube.com/watch?v=" + testVideoID,
		"https://youtu.be/" + testVideoID + "?si=share",
		"https://www.youtube.com/shorts/" + testVideoID,
		"https://www.youtube.com/embed/" + testVideoID,
		"https://www.youtube.com/live/" + testVideoID,
		"https://www.youtube-nocookie.com/embed/" + testVideoID,
	} {
		id, err := ParseVideoURL(raw)
		if err != nil || id != testVideoID {
			t.Errorf("ParseVideoURL(%q) = %q, %v", raw, id, err)
		}
	}
	canonical, err := CanonicalWatchURL(testVideoID)
	if err != nil || canonical != "https://www.youtube.com/watch?v="+testVideoID {
		t.Fatalf("canonical URL = %q, %v", canonical, err)
	}
}

func TestParseVideoURLRejectsSpoofingAndAmbiguousIDs(t *testing.T) {
	for _, raw := range []string{
		"https://youtube.com.evil.example/watch?v=" + testVideoID,
		"https://youtube.com@evil.example/watch?v=" + testVideoID,
		"https://user@youtube.com/watch?v=" + testVideoID,
		"https://youtube.com:444/watch?v=" + testVideoID,
		"javascript://youtube.com/watch?v=" + testVideoID,
		"https://youtube.com/watch?v=" + testVideoID + "&v=AAAAAAAAAAA",
		"https://youtube.com/shorts/" + testVideoID + "?v=AAAAAAAAAAA",
		"https://youtube.com/channel/" + testVideoID,
		"https://youtu.be/" + testVideoID + "/extra",
		"https://youtu.be//" + testVideoID,
		"https://youtube.com//shorts/" + testVideoID,
		"https://youtube.com/watch?v=too-short",
	} {
		if _, err := ParseVideoURL(raw); err == nil {
			t.Errorf("unsafe YouTube URL accepted: %q", raw)
		}
	}
}

func TestCanonicalIdentityRequiresAllCandidatesToAgreeAndIsAttributed(t *testing.T) {
	now := time.Date(2026, 9, 3, 16, 0, 0, 0, time.UTC)
	adapter := testAdapter(t, &fixtureClient{}, Options{Now: func() time.Time { return now }})
	input := testInput(domain.CapabilityEntities)
	result, err := adapter.ResolveCanonicalIdentity(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity.ProviderItemID != testVideoID || result.Identity.SourceItemKey != "youtube:"+testVideoID ||
		result.Identity.CanonicalURL != "https://www.youtube.com/watch?v="+testVideoID ||
		result.Identity.SubmittedURL != input.Source.SubmittedURL {
		t.Fatalf("canonical identity lost identity or provenance: %+v", result.Identity)
	}
	if result.Observation.Capability != domain.CapabilityEntities || result.Observation.Attribution != domain.AttributionProvider || !result.Observation.ObservedAt.Equal(now) {
		t.Fatalf("identity result is not attributed: %+v", result.Observation)
	}
	spoofed := input
	spoofed.Source.CanonicalURL = "https://youtu.be/AAAAAAAAAAA"
	if _, err := adapter.ResolveCanonicalIdentity(context.Background(), spoofed); err == nil {
		t.Fatal("disagreeing canonical URL and provider/source key were accepted")
	}
	wrongCapability := input
	wrongCapability.Capability = domain.CapabilityTitle
	if _, err := adapter.ResolveCanonicalIdentity(context.Background(), wrongCapability); err == nil {
		t.Fatal("identity emitted an entities observation for a title claim")
	}
	sourceKeyOnly := input
	sourceKeyOnly.Source.ProviderItemID = ""
	sourceKeyOnly.Source.SourceItemKey = "https://youtu.be/" + testVideoID
	strengthened, err := adapter.ResolveCanonicalIdentity(context.Background(), sourceKeyOnly)
	if err != nil {
		t.Fatal(err)
	}
	if strengthened.Identity.ProviderItemID != testVideoID || strengthened.Identity.SourceItemKey != sourceKeyOnly.Source.SourceItemKey {
		t.Fatalf("canonical identity rewrote stable fragment source key: before=%q after=%+v", sourceKeyOnly.Source.SourceItemKey, strengthened.Identity)
	}
}

func TestMetadataIsStrictlyPerCapabilityWithTypedChapters(t *testing.T) {
	client := &fixtureClient{video: Video{
		ID: testVideoID, Title: "Safe title", Description: "<script>unrelated bad description</script>",
		ChannelID: "UC_fixture", ChannelName: "Fixture Channel", Tags: []string{"Go", "testing", "go"},
		DurationSeconds: 60, Chapters: []Chapter{{Title: "Intro", StartSeconds: 0}, {Title: "Deep dive", StartSeconds: 12.5}},
	}}
	adapter := testAdapter(t, client, Options{})
	title, err := adapter.ProvideMetadata(context.Background(), testInput(domain.CapabilityTitle))
	if err != nil || len(title.Observations) != 1 || title.Observations[0].Capability != domain.CapabilityTitle {
		t.Fatalf("title-only completion = %+v, %v", title, err)
	}
	// Unsafe data in an unrelated field must not fail or leak into a title job.
	if strings.Contains(title.Observations[0].ValueJSON, "script") {
		t.Fatalf("unclaimed description leaked into title result: %s", title.Observations[0].ValueJSON)
	}
	if _, err := adapter.ProvideMetadata(context.Background(), testInput(domain.CapabilityDescription)); err == nil {
		t.Fatal("unsafe description was accepted on its own capability")
	}
	tags, err := adapter.ProvideMetadata(context.Background(), testInput(domain.CapabilityTags))
	if err != nil || len(tags.Observations) != 1 || tags.Observations[0].ValueJSON != `{"values":["Go","testing"]}` {
		t.Fatalf("typed tags = %+v, %v", tags, err)
	}
	entitiesResult, err := adapter.ProvideMetadata(context.Background(), testInput(domain.CapabilityEntities))
	if err != nil {
		t.Fatal(err)
	}
	var entities provider.EntityListValue
	if err := json.Unmarshal([]byte(entitiesResult.Observations[0].ValueJSON), &entities); err != nil {
		t.Fatal(err)
	}
	want := []provider.EntityValue{
		{Kind: "youtube_channel_id", Value: "UC_fixture"},
		{Kind: "youtube_channel", Value: "Fixture Channel"},
		{Kind: "youtube_chapter", Value: "0.000\tIntro"},
		{Kind: "youtube_chapter", Value: "12.500\tDeep dive"},
	}
	if !reflect.DeepEqual(entities.Entities, want) {
		t.Fatalf("typed chapter entities = %+v, want %+v", entities.Entities, want)
	}
	client.video.Chapters = []Chapter{{Title: "later", StartSeconds: 10}, {Title: "duplicate/earlier", StartSeconds: 10}}
	if _, err := adapter.ProvideMetadata(context.Background(), testInput(domain.CapabilityEntities)); err == nil {
		t.Fatal("duplicate/non-increasing chapter times were accepted")
	}
	client.video.Chapters = []Chapter{{Title: "not exactly representable", StartSeconds: 1.0001}}
	if _, err := adapter.ProvideMetadata(context.Background(), testInput(domain.CapabilityEntities)); err == nil {
		t.Fatal("chapter time outside the stable millisecond representation was accepted")
	}
}

func TestProviderResultsCannotChangeResolvedVideoIdentity(t *testing.T) {
	adapter := testAdapter(t, &fixtureClient{
		video: Video{ID: "AAAAAAAAAAA", Title: "different video"},
		transcript: Transcript{VideoID: "AAAAAAAAAAA", TrackID: "en", Kind: domain.VariantTranscript,
			Language: "en", Format: "plain_text", Text: "different transcript"},
	}, Options{})
	if _, err := adapter.ProvideMetadata(context.Background(), testInput(domain.CapabilityTitle)); err == nil {
		t.Fatal("metadata from a different provider video was accepted")
	}
	if _, err := adapter.ProvideTranscript(context.Background(), testInput(domain.CapabilityTranscript)); err == nil {
		t.Fatal("transcript from a different provider video was accepted")
	}
}

func TestMediaCompletionDerivesAssetIdentityAndPreservesCustodyDefaults(t *testing.T) {
	expires := time.Date(2026, 9, 3, 18, 0, 0, 0, time.UTC)
	client := &fixtureClient{video: Video{ID: testVideoID, DurationSeconds: 180, Posters: []Poster{
		{ID: "default", URL: "https://i.ytimg.com/vi/" + testVideoID + "/default.jpg", MIMEType: "image/jpeg", Width: 120, Height: 90},
		{ID: "maxresdefault", URL: "https://i.ytimg.com/vi/" + testVideoID + "/maxresdefault.jpg", SourceExpiresAt: expires, MIMEType: "image/jpeg", Width: 1280, Height: 720},
	}}}
	adapter := testAdapter(t, client, Options{})
	poster, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: testInput(domain.CapabilityThumbnailOrPoster)})
	if err != nil {
		t.Fatal(err)
	}
	wantAssetID, _ := domain.StableMediaAssetID("browser-capture", "youtube", testVideoID, "youtube:"+testVideoID, "https://www.youtube.com/watch?v="+testVideoID)
	if len(poster.Manifest) != 1 || poster.Manifest[0].Asset.ID != wantAssetID || len(poster.Manifest[0].Variants) != 2 {
		t.Fatalf("poster manifest = %+v", poster.Manifest)
	}
	for _, variant := range poster.Manifest[0].Variants {
		if variant.Kind != domain.VariantPoster || variant.Custody != domain.CustodyMirror ||
			variant.AcquisitionState != domain.AcquisitionPending || variant.Retention != domain.RetentionIndefinite {
			t.Fatalf("poster custody/state = %+v", variant)
		}
	}
	if poster.Manifest[0].Variants[0].Width != 1280 || !poster.Manifest[0].Variants[0].SourceExpiresAt.Equal(expires) {
		t.Fatalf("posters are not deterministic largest-first: %+v", poster.Manifest[0].Variants)
	}
	referencePoster, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{
		Input: testInput(domain.CapabilityThumbnailOrPoster), RequestedCustody: domain.CustodyReference,
	})
	if err != nil || referencePoster.Manifest[0].Variants[0].AcquisitionState != domain.AcquisitionReferenceOnly ||
		referencePoster.Manifest[0].Variants[0].Retention != domain.RetentionExternal {
		t.Fatalf("reference poster custody = %+v, %v", referencePoster, err)
	}
	if _, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{
		Input: testInput(domain.CapabilityThumbnailOrPoster), MediaAssetID: "arbitrary-asset",
	}); err == nil {
		t.Fatal("provider media was attached to a caller-spoofed asset ID")
	}
	sourceOnly := testInput(domain.CapabilityThumbnailOrPoster)
	sourceOnly.Source.ProviderItemID = ""
	sourceOnly.Source.SourceItemKey = "https://youtu.be/" + testVideoID
	sourceOnlyPoster, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: sourceOnly})
	if err != nil || sourceOnlyPoster.Manifest[0].Asset.ID != wantAssetID {
		t.Fatalf("source-only completion did not derive the stable provider asset: %+v, %v", sourceOnlyPoster, err)
	}
	original, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: testInput(domain.CapabilityOriginalMedia)})
	if err != nil {
		t.Fatal(err)
	}
	variant := original.Manifest[0].Variants[0]
	if variant.Kind != domain.VariantOriginal || variant.Custody != domain.CustodyReference || variant.AcquisitionState != domain.AcquisitionReferenceOnly {
		t.Fatalf("YouTube original was not reference-only: %+v", variant)
	}
	_, err = adapter.AcquireMedia(context.Background(), provider.MediaRequest{
		Input: testInput(domain.CapabilityOriginalMedia), RequestedCustody: domain.CustodyMirror,
	})
	if err == nil {
		t.Fatal("automatic original-video mirroring was accepted")
	}
	client.videoErr = errors.New("metadata API unavailable")
	original, err = adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: testInput(domain.CapabilityOriginalMedia)})
	if err != nil || len(original.Manifest) != 1 {
		t.Fatalf("reference-only original incorrectly depended on metadata capability: %+v, %v", original, err)
	}
}

func TestTranscriptAcquisitionIsIndependentAndTranscriptFirstCanUseBlobLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 3, 17, 0, 0, 0, time.UTC)
	client := &fixtureClient{
		video: Video{ID: testVideoID, Posters: []Poster{{ID: "poster", URL: "https://i.ytimg.com/poster.jpg", MIMEType: "image/jpeg", Width: 640, Height: 360}}},
		transcript: Transcript{VideoID: testVideoID, TrackID: "en-manual", Kind: domain.VariantTranscript,
			Language: "en", Format: "plain_text", Text: "first line\nsecond line", SourceURL: "https://captions.youtube.example/en",
			SourceExpiresAt: now.Add(time.Hour)},
	}
	adapter := testAdapter(t, client, Options{Now: func() time.Time { return now }})
	acquisition, err := adapter.AcquireTranscript(context.Background(), testInput(domain.CapabilityTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if acquisition.Asset.Kind != domain.MediaVideo || acquisition.Result.Variant.Custody != domain.CustodyMirror ||
		acquisition.Result.Variant.AcquisitionState != domain.AcquisitionPending || acquisition.Result.Variant.ExpectedDigest.Value != domain.DigestText(string(acquisition.Content)) ||
		!acquisition.Result.Variant.SourceExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("transcript acquisition result = %+v", acquisition)
	}
	if acquisition.Result.Observation.Capability != domain.CapabilityTranscript || !strings.Contains(acquisition.Result.Observation.ValueJSON, "first line") {
		t.Fatalf("searchable transcript observation missing: %+v", acquisition.Result.Observation)
	}
	referenceAdapter := testAdapter(t, client, Options{TranscriptCustody: domain.CustodyReference})
	referenceAcquisition, err := referenceAdapter.AcquireTranscript(context.Background(), testInput(domain.CapabilityTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if referenceAcquisition.Result.Variant.Custody != domain.CustodyReference ||
		referenceAcquisition.Result.Variant.AcquisitionState != domain.AcquisitionReferenceOnly ||
		referenceAcquisition.Result.Variant.Retention != domain.RetentionExternal {
		t.Fatalf("reference transcript custody = %+v", referenceAcquisition.Result.Variant)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "youtube.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mediaRepo := repository.NewMediaRepository(st.DB)
	blobs, err := blobstore.NewFileStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	completion := service.NewProviderMediaCompletion(mediaRepo, service.NewMediaService(mediaRepo, blobs))
	if _, err := completion.Complete(context.Background(), acquisition.Asset, acquisition.Result.Variant, bytes.NewBufferString("corrupted transcript")); err == nil {
		t.Fatal("transcript content that disagreed with the provider digest was accepted")
	}
	var preStoreBlobs int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_blobs`).Scan(&preStoreBlobs); err != nil {
		t.Fatal(err)
	}
	pending, err := mediaRepo.GetVariant(context.Background(), acquisition.Result.Variant.ID)
	if err != nil || preStoreBlobs != 0 || pending.AcquisitionState != domain.AcquisitionPending || pending.BlobDigest != "" {
		t.Fatalf("failed transcript verification left durable blob state: blobs=%d variant=%+v err=%v", preStoreBlobs, pending, err)
	}
	storedVariant, err := completion.Complete(context.Background(), acquisition.Asset, acquisition.Result.Variant, bytes.NewReader(acquisition.Content))
	if err != nil {
		t.Fatal(err)
	}
	if storedVariant.AcquisitionState != domain.AcquisitionAvailable || storedVariant.BlobDigest == "" {
		t.Fatalf("transcript-first completion did not store verified bytes: %+v", storedVariant)
	}
	var assets, variants, blobsCount int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM asset_variants`).Scan(&variants); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_blobs`).Scan(&blobsCount); err != nil {
		t.Fatal(err)
	}
	if assets != 1 || variants != 1 || blobsCount != 1 {
		t.Fatalf("transcript-first composition rows assets=%d variants=%d blobs=%d", assets, variants, blobsCount)
	}
	var observations int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM enrichment_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 0 {
		t.Fatalf("provider media completion bypassed fenced observation publication: %d rows", observations)
	}
	replayed, err := completion.Complete(context.Background(), acquisition.Asset, acquisition.Result.Variant, bytes.NewReader(acquisition.Content))
	if err != nil || replayed.ID != storedVariant.ID || replayed.BlobDigest != storedVariant.BlobDigest {
		t.Fatalf("exact transcript completion replay diverged: %+v, %v", replayed, err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM asset_variants`).Scan(&variants); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM media_blobs`).Scan(&blobsCount); err != nil {
		t.Fatal(err)
	}
	if assets != 1 || variants != 1 || blobsCount != 1 {
		t.Fatalf("transcript replay duplicated rows assets=%d variants=%d blobs=%d", assets, variants, blobsCount)
	}

	client.transcriptErr = errors.New("fixture outage with untrusted response details")
	if _, err := adapter.ProvideTranscript(context.Background(), testInput(domain.CapabilityTranscript)); err == nil {
		t.Fatal("transcript client failure was ignored")
	} else {
		var classified *provider.AdapterError
		if !errors.As(err, &classified) || classified.Class != domain.EnrichmentErrorRetryable || strings.Contains(classified.Message, "untrusted") {
			t.Fatalf("transcript failure was not safely classified: %v", err)
		}
	}
	// A transcript failure does not poison poster capability execution.
	poster, err := adapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: testInput(domain.CapabilityThumbnailOrPoster)})
	if err != nil || len(poster.Manifest[0].Variants) != 1 {
		t.Fatalf("poster capability failed with transcript sibling: %+v, %v", poster, err)
	}
	posterFailureClient := &fixtureClient{
		video: Video{ID: testVideoID},
		transcript: Transcript{VideoID: testVideoID, TrackID: "en", Kind: domain.VariantTranscript,
			Language: "en", Format: "plain_text", Text: "transcript survives poster failure"},
	}
	posterFailureAdapter := testAdapter(t, posterFailureClient, Options{})
	if _, err := posterFailureAdapter.AcquireMedia(context.Background(), provider.MediaRequest{Input: testInput(domain.CapabilityThumbnailOrPoster)}); err == nil {
		t.Fatal("missing poster candidates unexpectedly succeeded")
	}
	if transcriptResult, err := posterFailureAdapter.ProvideTranscript(context.Background(), testInput(domain.CapabilityTranscript)); err != nil || transcriptResult.Observation.Capability != domain.CapabilityTranscript {
		t.Fatalf("transcript capability was poisoned by poster failure: %+v, %v", transcriptResult, err)
	}
}

func TestPlaybackIsAllowlistedProviderIDOnlyAndCapabilityScoped(t *testing.T) {
	adapter := testAdapter(t, &fixtureClient{}, Options{})
	result, err := adapter.ProvidePlayback(context.Background(), testInput(domain.CapabilityEntities))
	if err != nil {
		t.Fatal(err)
	}
	if result.Spec.Kind != provider.PlaybackProviderEmbed || result.Spec.Provider != "youtube" || result.Spec.ProviderItemID != testVideoID {
		t.Fatalf("playback spec = %+v", result.Spec)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(strings.ToLower(string(raw)), "html") || strings.Contains(string(raw), "iframe") {
		t.Fatalf("playback exposed provider markup: %s", raw)
	}
	if _, err := adapter.ProvidePlayback(context.Background(), testInput(domain.CapabilityTitle)); err == nil {
		t.Fatal("playback emitted entities for a title claim")
	}
}

func TestBrowserProvidedFactsStaySelectedWhileMissingYouTubeCapabilitiesFill(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "browser-capture-reader", "v1", "fixtures", "valid-capture-youtube.json"))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := capturecontract.DecodeCaptureEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	envelope.Tags = []string{}
	raw, _ = json.Marshal(envelope)
	st, err := store.Open(filepath.Join(t.TempDir(), "coverage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mediaRepo := repository.NewMediaRepository(st.DB)
	blobs, err := blobstore.NewFileStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	captureService := service.NewCaptureService(repository.NewCaptureRepository(st.DB), service.NewMediaService(mediaRepo, blobs))
	accepted, err := captureService.AcceptManifest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	enrichmentRepo := repository.NewEnrichmentRepository(st.DB)
	enrichment := service.NewEnrichmentService(enrichmentRepo, nil)
	plan, err := enrichment.PlanCapture(context.Background(), envelope.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) == 0 {
		t.Fatal("missing capabilities were not planned")
	}
	client := &fixtureClient{
		video:      Video{ID: testVideoID, ChannelID: "UC_fixture", ChannelName: "Fixture Channel", Tags: []string{"provider-tag"}, Chapters: []Chapter{{Title: "Intro", StartSeconds: 0}}},
		transcript: Transcript{VideoID: testVideoID, TrackID: "en", Kind: domain.VariantTranscript, Language: "en", Format: "plain_text", Text: "searchable provider transcript"},
	}
	adapter := testAdapter(t, client, Options{})
	mediaCompletion := service.NewProviderMediaCompletion(mediaRepo, service.NewMediaService(mediaRepo, blobs))
	claims := make(map[domain.EnrichmentCapability]domain.EnrichmentClaim)
	for len(claims) < 3 {
		claim, found, err := enrichment.Claim(context.Background(), adapter.Descriptor(), "youtube-worker", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if claim.Job.Capability == domain.CapabilityTitle || claim.Job.Capability == domain.CapabilityBody {
			t.Fatalf("browser-provided capability was planned for provider overwrite: %s", claim.Job.Capability)
		}
		claims[claim.Job.Capability] = claim
	}
	wantCapabilities := []domain.EnrichmentCapability{domain.CapabilityEntities, domain.CapabilityTags, domain.CapabilityTranscript}
	for _, capability := range wantCapabilities {
		claim, ok := claims[capability]
		if !ok {
			t.Fatalf("missing YouTube capability %s was not claimable; got %v", capability, sortedCapabilities(claims))
		}
		var observation provider.ObservationDraft
		switch capability {
		case domain.CapabilityTranscript:
			acquisition, err := adapter.AcquireTranscript(context.Background(), service.ProviderInput(claim))
			if err != nil {
				t.Fatal(err)
			}
			stored, err := mediaCompletion.Complete(context.Background(), acquisition.Asset, acquisition.Result.Variant, bytes.NewReader(acquisition.Content))
			if err != nil || stored.AcquisitionState != domain.AcquisitionAvailable {
				t.Fatalf("transcript bytes were not stored before observation publication: %+v, %v", stored, err)
			}
			observation = acquisition.Result.Observation
		default:
			result, err := adapter.ProvideMetadata(context.Background(), service.ProviderInput(claim))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Observations) != 1 || result.Observations[0].Capability != capability {
				t.Fatalf("%s claim returned unrelated observations: %+v", capability, result)
			}
			observation = result.Observations[0]
		}
		coverage, err := enrichment.CompleteSuccess(context.Background(), claim, observation)
		if err != nil || coverage.State != domain.CoverageProvided {
			t.Fatalf("complete %s = %+v, %v", capability, coverage, err)
		}
	}
	for _, capability := range []domain.EnrichmentCapability{domain.CapabilityTitle, domain.CapabilityBody} {
		observations, err := enrichmentRepo.ListObservations(context.Background(), accepted.FragmentRevisionID, capability)
		if err != nil || len(observations) != 1 || observations[0].Attribution != domain.AttributionSourceMaterial {
			t.Fatalf("browser %s observation was changed: %+v, %v", capability, observations, err)
		}
	}
	var highlightText string
	if err := st.DB.QueryRow(`SELECT text FROM capture_annotations WHERE capture_id = ?`, envelope.CaptureID).Scan(&highlightText); err != nil {
		t.Fatal(err)
	}
	if highlightText != "Useful page text." {
		t.Fatalf("provider completion changed browser highlight source context: %q", highlightText)
	}
	providerTags, err := enrichmentRepo.ListObservations(context.Background(), accepted.FragmentRevisionID, domain.CapabilityTags)
	if err != nil || len(providerTags) != 1 || providerTags[0].Producer.Adapter != adapterName ||
		providerTags[0].Producer.Version != adapter.Descriptor().Version || providerTags[0].InputMaterialDigest == "" {
		t.Fatalf("provider result lost descriptor/input attribution: %+v, %v", providerTags, err)
	}
}

func testAdapter(t *testing.T, client Client, options Options) *Adapter {
	t.Helper()
	adapter, err := New(client, options)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func testInput(capability domain.EnrichmentCapability) provider.Input {
	return provider.Input{
		FragmentID: "fragment", FragmentRevisionID: "revision", Capability: capability,
		MaterialDigest: domain.DigestText("material"),
		Source: domain.SourceIdentity{
			SourceRegistrationID: "browser-capture", Provider: "youtube", ProviderItemID: testVideoID,
			SourceItemKey: "youtube:" + testVideoID, SourceLocator: "https://youtu.be/" + testVideoID,
			SegmentKey: "root", SubmittedURL: "https://www.youtube.com/shorts/" + testVideoID,
			CanonicalURL:  "https://www.youtube.com/watch?v=" + testVideoID,
			SourceAdapter: domain.AdapterVersion{Adapter: "youtube", Version: "1.0.0"},
			Canonicalizer: domain.AdapterVersion{Adapter: "youtube", Version: "1.0.0"},
		},
	}
}

func sortedCapabilities(claims map[domain.EnrichmentCapability]domain.EnrichmentClaim) []string {
	result := make([]string, 0, len(claims))
	for capability := range claims {
		result = append(result, string(capability))
	}
	sort.Strings(result)
	return result
}
