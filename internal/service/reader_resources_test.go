package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/blobstore"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestReaderArticleKeepsMarkdownCanonicalAndSanitizesRenderedHTML(t *testing.T) {
	content := "# Safe heading\n\n<script>alert(1)</script>\n<style>body{display:none}</style>\n" +
		"<iframe src=\"https://evil.example/embed\"></iframe>\n\n<img src=x onerror=alert(2)>\n\n" +
		"[unsafe](javascript:alert(3)) [safe](https://example.com/read)\n\n" +
		"<div class=\"oembed\" onclick=\"steal()\"><video src=\"https://evil.example/raw\"></video></div>"
	st, fragment := readerArticleFixture(t, content)
	defer st.Close()
	svc := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), readerBlobStore(t), t.TempDir())

	markdown, err := svc.Article(context.Background(), fragment.ID, fragment.Revision.ID, ArticleMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	if string(markdown.Body) != content || markdown.ContentType != "text/markdown; charset=utf-8" {
		t.Fatalf("canonical markdown changed: %+v %q", markdown, markdown.Body)
	}
	html, err := svc.Article(context.Background(), fragment.ID, fragment.Revision.ID, ArticleHTML)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(html.Body))
	for _, forbidden := range []string{"<script", "<style", "<iframe", "<img", "<video", "onclick", "onerror", "javascript:", "evil.example", "oembed"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("sanitized HTML retained %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "<h1>safe heading</h1>") || !strings.Contains(body, `href="https://example.com/read"`) {
		t.Fatalf("safe rendered content was lost: %s", body)
	}
	if markdown.Digest.Value == html.Digest.Value || markdown.Digest.Value != domain.DigestText(content) {
		t.Fatalf("representation digests are not exact: markdown=%+v html=%+v", markdown.Digest, html.Digest)
	}
}

func TestReaderArticleStripsMarkdownImagesWithoutOrphanMarkup(t *testing.T) {
	content := "Before\n\n[\n\n![profile picture](https://cdn.example/avatar.jpg)\n\n](https://social.example/profile)\n\n" +
		"After [useful link](https://example.com/read) and ![diagram](https://cdn.example/diagram.png)."
	st, fragment := readerArticleFixture(t, content)
	defer st.Close()
	svc := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), readerBlobStore(t), t.TempDir())

	resource, err := svc.Article(context.Background(), fragment.ID, fragment.Revision.ID, ArticleHTML)
	if err != nil {
		t.Fatal(err)
	}
	body := string(resource.Body)
	for _, forbidden := range []string{"<img", "cdn.example", "social.example", "<p>[</p>", "]("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("sanitized article retained Markdown image debris %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "<p>Before</p>") ||
		!strings.Contains(body, `<a href="https://example.com/read"`) ||
		!strings.Contains(body, "After") {
		t.Fatalf("sanitized article lost readable content: %s", body)
	}
}

func TestReaderArticleRejectsTamperedRevisionDigest(t *testing.T) {
	st, fragment := readerArticleFixture(t, "trusted")
	defer st.Close()
	if _, err := st.DB.Exec(`UPDATE fragment_revisions SET content = 'tampered' WHERE id = ?`, fragment.Revision.ID); err != nil {
		t.Fatal(err)
	}
	svc := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), readerBlobStore(t), t.TempDir())
	_, err := svc.Article(context.Background(), fragment.ID, fragment.Revision.ID, ArticleHTML)
	if !IsReaderResourceIntegrityFailure(err) {
		t.Fatalf("tampered revision error = %T %v", err, err)
	}
}

func TestReaderMediaVerifiesCASBytesMIMEAndDigest(t *testing.T) {
	payload := testPNGBytes()
	expected := domain.ContentDigest{Algorithm: "sha256", Value: domain.DigestText(string(payload))}
	st, blobs, fragment, variant := readerMediaFixture(t, domain.MediaImage, domain.AssetVariant{
		VariantIdentity: "original", Kind: domain.VariantOriginal, MIMEType: "image/png",
		ExpectedDigest: expected, Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending,
	})
	defer st.Close()
	media := NewMediaService(repository.NewMediaRepository(st.DB), blobs)
	stored, err := media.StoreVariantContent(context.Background(), variant.ID, expected, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, t.TempDir())
	resource, err := svc.OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resource.Close()
	if resource.ContentType != "image/png" || resource.Digest != expected || resource.ByteSize != int64(len(payload)) || resource.Legacy {
		t.Fatalf("unexpected verified media: %+v", resource)
	}
	read, err := os.ReadFile(filepath.Join(blobs.Root(), filepath.FromSlash("sha256/"+expected.Value[:2]+"/"+expected.Value)))
	if err != nil || !bytes.Equal(read, payload) {
		t.Fatalf("stored payload mismatch: %v", err)
	}

	path, err := blobs.Resolve("sha256/" + expected.Value[:2] + "/" + expected.Value)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), payload...)
	tampered[len(tampered)-1] ^= 0xff
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = svc.OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, stored.ID)
	if !IsReaderResourceIntegrityFailure(err) {
		t.Fatalf("tampered CAS error = %T %v", err, err)
	}
}

func TestReaderMediaRejectsActiveOrMismatchedMIME(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    domain.MediaKind
		variant domain.AssetVariant
		payload []byte
	}{
		{"svg poster", domain.MediaImage, domain.AssetVariant{VariantIdentity: "poster", Kind: domain.VariantPoster, MIMEType: "image/svg+xml", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending}, []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"html transcript", domain.MediaVideo, domain.AssetVariant{VariantIdentity: "transcript", Kind: domain.VariantTranscript, MIMEType: "text/html", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending}, []byte(`<html><script>alert(1)</script></html>`)},
		{"declared jpeg png bytes", domain.MediaImage, domain.AssetVariant{VariantIdentity: "original", Kind: domain.VariantOriginal, MIMEType: "image/jpeg", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending}, testPNGBytes()},
		{"malformed declaration", domain.MediaImage, domain.AssetVariant{VariantIdentity: "original", Kind: domain.VariantOriginal, MIMEType: "not-a-mime", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending}, testPNGBytes()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := domain.ContentDigest{Algorithm: "sha256", Value: domain.DigestText(string(tc.payload))}
			tc.variant.ExpectedDigest = expected
			st, blobs, fragment, variant := readerMediaFixture(t, tc.kind, tc.variant)
			defer st.Close()
			stored, err := NewMediaService(repository.NewMediaRepository(st.DB), blobs).StoreVariantContent(context.Background(), variant.ID, expected, bytes.NewReader(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, t.TempDir()).OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, stored.ID)
			var unavailable *ReaderResourceUnavailableError
			if !errors.As(err, &unavailable) || unavailable.Reason != "unsafe_or_mismatched_mime" {
				t.Fatalf("unsafe MIME error = %T %v", err, err)
			}
		})
	}
}

func TestReaderMediaSafeMIMEAllowlistMatchesSniffedBytes(t *testing.T) {
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'm', 'p', '4', '1', 'i', 's', 'o', 'm'}
	tests := []struct {
		name     string
		asset    domain.MediaKind
		variant  domain.AssetVariantKind
		declared string
		bytes    []byte
		want     string
	}{
		{"jpeg", domain.MediaImage, domain.VariantOriginal, "image/jpeg", []byte("\xff\xd8\xff\xe0jpeg"), "image/jpeg"},
		{"png", domain.MediaImage, domain.VariantThumbnail, "image/png", testPNGBytes(), "image/png"},
		{"gif", domain.MediaImage, domain.VariantPreview, "image/gif", []byte("GIF89aimage"), "image/gif"},
		{"webp", domain.MediaImage, domain.VariantPoster, "image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP"), "image/webp"},
		{"mp4", domain.MediaVideo, domain.VariantOriginal, "video/mp4", mp4, "video/mp4"},
		{"webm", domain.MediaVideo, domain.VariantOriginal, "video/webm", []byte("\x1a\x45\xdf\xa3webm"), "video/webm"},
		{"mp3", domain.MediaAudio, domain.VariantAudio, "audio/mpeg", []byte("ID3\x04\x00\x00mp3"), "audio/mpeg"},
		{"wav", domain.MediaAudio, domain.VariantOriginal, "audio/wav", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), "audio/wav"},
		{"vtt", domain.MediaVideo, domain.VariantSubtitles, "text/vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\ntext"), "text/vtt"},
		{"srt", domain.MediaVideo, domain.VariantTranscript, "application/x-subrip", []byte("1\n00:00:00,000 --> 00:00:01,000\ntext"), "application/x-subrip"},
		{"plain", domain.MediaTimedText, domain.VariantOriginal, "text/plain", []byte("plain transcript"), "text/plain"},
		{"markdown", domain.MediaDocument, domain.VariantOriginal, "text/markdown", []byte("# document\n\nbody"), "text/markdown"},
		{"pdf", domain.MediaDocument, domain.VariantOriginal, "application/pdf", []byte("%PDF-1.7\nbody"), "application/pdf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeReaderMediaType(domain.MediaAsset{Kind: tc.asset}, domain.AssetVariant{Kind: tc.variant, MIMEType: tc.declared}, tc.bytes)
			if err != nil || got != tc.want {
				t.Fatalf("safe MIME = %q, %v; want %q (sniffed %q)", got, err, tc.want, http.DetectContentType(tc.bytes))
			}
		})
	}
}

func TestReaderMediaDoesNotClaimUnsupportedOggOrAVIFSniffing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		asset    domain.MediaKind
		declared string
		bytes    []byte
	}{
		{"ogg", domain.MediaAudio, "audio/ogg", []byte("OggS\x00stream")},
		{"avif", domain.MediaImage, "image/avif", []byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00avifmif1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := safeReaderMediaType(domain.MediaAsset{Kind: tc.asset}, domain.AssetVariant{Kind: domain.VariantOriginal, MIMEType: tc.declared}, tc.bytes); err == nil {
				t.Fatalf("unsupported %s was claimed as %q (sniffed %q)", tc.name, got, http.DetectContentType(tc.bytes))
			}
		})
	}
}

func TestReaderMediaServesVerifiedTranscriptWithoutSourceURLFallback(t *testing.T) {
	payload := []byte("WEBVTT\n\n00:00.000 --> 00:02.000\nTranscript text\n")
	expected := domain.ContentDigest{Algorithm: "sha256", Value: domain.DigestText(string(payload))}
	st, blobs, fragment, variant := readerMediaFixture(t, domain.MediaVideo, domain.AssetVariant{
		VariantIdentity: "captions:en", Kind: domain.VariantTranscript,
		SourceURL: "https://provider.invalid/expiring-captions", MIMEType: "text/vtt",
		ExpectedDigest: expected, Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending,
	})
	defer st.Close()
	stored, err := NewMediaService(repository.NewMediaRepository(st.DB), blobs).StoreVariantContent(context.Background(), variant.ID, expected, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resource, err := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, t.TempDir()).OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resource.Close()
	if resource.ContentType != "text/vtt" || resource.Digest != expected {
		t.Fatalf("transcript resource = %+v", resource)
	}
	read, err := io.ReadAll(resource.File)
	if err != nil || !bytes.Equal(read, payload) {
		t.Fatalf("transcript bytes = %q, %v", read, err)
	}
}

func TestReaderMediaLegacyPathRequiresConfiguredRootAndNoSymlink(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "preview.png")
	if err := os.WriteFile(inside, testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	st, blobs, fragment, variant := readerMediaFixture(t, domain.MediaImage, domain.AssetVariant{
		VariantIdentity: "legacy-preview", Kind: domain.VariantPreview, MIMEType: "image/png",
		Custody: domain.CustodyAdopted, AcquisitionState: domain.AcquisitionAvailable,
		LegacyStoragePath: inside,
	})
	defer st.Close()
	svc := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, root)
	resource, err := svc.OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, variant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !resource.Legacy {
		t.Fatal("legacy path did not retain mutable cache classification")
	}
	_ = resource.Close()

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, inside); err != nil {
		t.Fatal(err)
	}
	_, err = svc.OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, variant.ID)
	var unavailable *ReaderResourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != "legacy_path_not_authorized" {
		t.Fatalf("symlink escape error = %T %v", err, err)
	}

	outsideDir := t.TempDir()
	outsideNested := filepath.Join(outsideDir, "nested.png")
	if err := os.WriteFile(outsideNested, testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked-directory")
	if err := os.Symlink(outsideDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE asset_variants SET legacy_storage_path = ? WHERE id = ?`, filepath.Join(linkedDir, "nested.png"), variant.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, variant.ID)
	if !errors.As(err, &unavailable) || unavailable.Reason != "legacy_path_not_authorized" {
		t.Fatalf("intermediate symlink escape error = %T %v", err, err)
	}

	if _, err := st.DB.Exec(`UPDATE asset_variants SET legacy_storage_path = ? WHERE id = ?`, filepath.Join(root, "..", filepath.Base(outside)), variant.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, variant.ID)
	if !errors.As(err, &unavailable) {
		t.Fatalf("path traversal error = %T %v", err, err)
	}
}

func TestReaderMediaNeverReadsSourcePathAsCustody(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source-only.png")
	if err := os.WriteFile(sourcePath, testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	st, blobs, fragment, variant := readerMediaFixture(t, domain.MediaImage, domain.AssetVariant{
		VariantIdentity: "source-only", Kind: domain.VariantOriginal, MIMEType: "image/png",
		Custody: domain.CustodyAdopted, AcquisitionState: domain.AcquisitionAvailable,
		SourcePath: sourcePath,
	})
	defer st.Close()
	_, err := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, root).OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, variant.ID)
	var unavailable *ReaderResourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != "verified_bytes_unavailable" {
		t.Fatalf("source path was treated as custody: %T %v", err, err)
	}
}

func TestReaderMediaReportsIndependentPartialStates(t *testing.T) {
	for _, variant := range []domain.AssetVariant{
		{VariantIdentity: "pending", Kind: domain.VariantPoster, MIMEType: "image/png", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionPending},
		{VariantIdentity: "reference", Kind: domain.VariantOriginal, MIMEType: "video/mp4", SourceURL: "https://example.invalid/video", Custody: domain.CustodyReference, AcquisitionState: domain.AcquisitionReferenceOnly},
		{VariantIdentity: "failed", Kind: domain.VariantPoster, MIMEType: "image/png", Custody: domain.CustodyMirror, AcquisitionState: domain.AcquisitionFailed, Failure: &domain.AssetFailure{Code: "upstream", Message: "untrusted provider detail", Retryable: true}},
	} {
		t.Run(variant.VariantIdentity, func(t *testing.T) {
			st, blobs, fragment, stored := readerMediaFixture(t, domain.MediaVideo, variant)
			defer st.Close()
			_, err := NewReaderResourceService(repository.NewReaderResourceRepository(st.DB), blobs, t.TempDir()).OpenMedia(context.Background(), fragment.ID, fragment.Revision.ID, stored.ID)
			var unavailable *ReaderResourceUnavailableError
			if !errors.As(err, &unavailable) || unavailable.State != variant.AcquisitionState {
				t.Fatalf("partial state error = %T %v", err, err)
			}
			if strings.Contains(err.Error(), "untrusted provider detail") {
				t.Fatalf("provider failure leaked: %v", err)
			}
		})
	}
}

func TestTrustedYouTubePlaybackSpecRejectsSpoofingAndUntrustedKinds(t *testing.T) {
	spec, err := TrustedYouTubePlaybackSpec("YouTube", "3RmtNXqnreI", 12.5)
	if err != nil || spec.Kind != "provider_embed" || spec.Provider != "youtube" || spec.ProviderItemID != "3RmtNXqnreI" || spec.StartSeconds == nil || *spec.StartSeconds != 12.5 || spec.URL != "" {
		t.Fatalf("trusted playback = %+v, %v", spec, err)
	}
	for _, tc := range []struct {
		provider, id string
		start        float64
	}{
		{"youtube.com.evil.example", "3RmtNXqnreI", 0},
		{"youtube", "https://youtu.be/3RmtNXqnreI", 0},
		{"youtube", "3RmtNXqnreI<script>", 0},
		{"youtube", "short", 0},
		{"youtube", "3RmtNXqnreI", -1},
		{"youtube", "3RmtNXqnreI", math.NaN()},
		{"youtube", "3RmtNXqnreI", math.Inf(1)},
	} {
		if spec, err := TrustedYouTubePlaybackSpec(tc.provider, tc.id, tc.start); err == nil {
			t.Fatalf("untrusted playback accepted: %+v", spec)
		}
	}
}

func TestOptimisticReaderUsesContextualMediaHrefAndTrustedPlaybackConstructor(t *testing.T) {
	accepted := domain.CaptureAcceptance{
		Fragment:         domain.Fragment{ID: "fragment-reader", SourceIdentity: domain.SourceIdentity{Provider: "youtube", ProviderItemID: "3RmtNXqnreI"}},
		ObservedRevision: domain.FragmentRevision{ID: "revision-reader", FragmentID: "fragment-reader", Title: "Video"},
		Media: []domain.MediaManifestItem{{
			Asset: domain.MediaAsset{ID: "asset-reader", Kind: domain.MediaVideo},
			Variants: []domain.AssetVariant{{ID: "variant-reader", Kind: domain.VariantPoster, Custody: domain.CustodyMirror,
				AcquisitionState: domain.AcquisitionAvailable, BlobDigest: strings.Repeat("a", 64)}},
			Attachment: domain.AttachmentRef{ID: "attachment-reader", FragmentRevisionID: "revision-reader", MediaAssetID: "asset-reader", Role: domain.AttachmentPrimary},
		}},
	}
	item := projectOptimisticReader(capturecontract.CaptureEnvelope{}, accepted)
	if item.Playback == nil || item.Playback.ProviderItemID != "3RmtNXqnreI" {
		t.Fatalf("trusted playback missing: %+v", item.Playback)
	}
	wantHref := "/v1/media/variants/variant-reader/content?fragment_id=fragment-reader&revision_id=revision-reader"
	if got := item.Media[0].Variants[0].ContentHref; got != wantHref {
		t.Fatalf("contextual content href = %q, want %q", got, wantHref)
	}

	accepted.Fragment.SourceIdentity.Provider = "youtube.com.evil.example"
	accepted.Fragment.SourceIdentity.ProviderItemID = "3RmtNXqnreI"
	if spoofed := projectOptimisticReader(capturecontract.CaptureEnvelope{}, accepted); spoofed.Playback != nil {
		t.Fatalf("spoofed provider received playback: %+v", spoofed.Playback)
	}
	accepted.Fragment.SourceIdentity.Provider = "youtube"
	accepted.Fragment.SourceIdentity.ProviderItemID = "<script>x</script>"
	if malformed := projectOptimisticReader(capturecontract.CaptureEnvelope{}, accepted); malformed.Playback != nil {
		t.Fatalf("malformed provider ID received playback: %+v", malformed.Playback)
	}
}

func readerArticleFixture(t *testing.T, content string) (*store.Store, domain.Fragment) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "reader.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 18, 0, 0, 0, time.UTC)
	built, err := repository.BuildFragment(domain.PipelineFragment{
		Source: "url", SourceType: "article", SourceID: "https://example.com/reader",
		Title: "Reader", Content: content, CreatedAt: now,
	}, "reader-test", now)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	fragment, _, err := repository.NewFragmentRepository(st.DB).UpsertResolved(context.Background(), built)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, fragment
}

func readerMediaFixture(t *testing.T, assetKind domain.MediaKind, variant domain.AssetVariant) (*store.Store, *blobstore.FileStore, domain.Fragment, domain.AssetVariant) {
	t.Helper()
	st, fragment := readerArticleFixture(t, "reader media")
	blobs := readerBlobStore(t)
	manifest := domain.MediaManifestItem{
		Asset: domain.MediaAsset{
			SourceRegistrationID: "browser", Provider: "web", SourceMediaKey: "media",
			SourceLocator: "https://example.com/media", Kind: assetKind, SourceAuthority: "web",
			DefaultCustody: variant.Custody,
		},
		Variants:   []domain.AssetVariant{variant},
		Attachment: domain.AttachmentRef{Role: domain.AttachmentPrimary, Position: 0},
	}
	resolved, err := repository.NewMediaRepository(st.DB).UpsertManifest(context.Background(), fragment.Revision.ID, []domain.MediaManifestItem{manifest}, time.Now().UTC())
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, blobs, fragment, resolved[0].Variants[0]
}

func readerBlobStore(t *testing.T) *blobstore.FileStore {
	t.Helper()
	blobs, err := blobstore.NewFileStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return blobs
}

func testPNGBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
}
