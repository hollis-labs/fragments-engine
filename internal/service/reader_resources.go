package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type ArticleFormat string

const (
	ArticleHTML     ArticleFormat = "html"
	ArticleMarkdown ArticleFormat = "markdown"
)

type ReaderBlobStore interface {
	Open(string) (*os.File, error)
}

type ReaderArticleResource struct {
	CanonicalFragmentID string
	Revision            domain.FragmentRevision
	Format              ArticleFormat
	ContentType         string
	Body                []byte
	Digest              domain.ContentDigest
}

type ReaderMediaResource struct {
	CanonicalFragmentID string
	RevisionID          string
	Asset               domain.MediaAsset
	Variant             domain.AssetVariant
	File                *os.File
	ContentType         string
	Digest              domain.ContentDigest
	ByteSize            int64
	ModifiedAt          time.Time
	Legacy              bool
}

func (r *ReaderMediaResource) Close() error {
	if r == nil || r.File == nil {
		return nil
	}
	return r.File.Close()
}

// ReaderResourceUnavailableError describes a known partial or deliberately
// non-servable media state. Its Reason is a stable server classification, not
// source/provider text.
type ReaderResourceUnavailableError struct {
	State     domain.AcquisitionState
	Reason    string
	Retryable bool
}

func (e *ReaderResourceUnavailableError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("reader resource unavailable: %s (%s)", e.State, e.Reason)
}

type ReaderResourceIntegrityError struct {
	Reason string
}

func (e *ReaderResourceIntegrityError) Error() string {
	if e == nil {
		return ""
	}
	return "reader resource failed integrity verification: " + e.Reason
}

func IsReaderResourceUnavailable(err error) bool {
	var target *ReaderResourceUnavailableError
	return errors.As(err, &target)
}

func IsReaderResourceIntegrityFailure(err error) bool {
	var target *ReaderResourceIntegrityError
	return errors.As(err, &target)
}

type ReaderResourceService struct {
	repo       *repository.ReaderResourceRepository
	blobs      ReaderBlobStore
	legacyRoot string
}

func NewReaderResourceService(repo *repository.ReaderResourceRepository, blobs ReaderBlobStore, legacyRoot string) *ReaderResourceService {
	return &ReaderResourceService{repo: repo, blobs: blobs, legacyRoot: strings.TrimSpace(legacyRoot)}
}

func (s *ReaderResourceService) Article(ctx context.Context, fragmentID, revisionID string, format ArticleFormat) (ReaderArticleResource, error) {
	if s == nil || s.repo == nil {
		return ReaderArticleResource{}, fmt.Errorf("reader article: repository is required")
	}
	owned, err := s.repo.GetRevision(ctx, fragmentID, revisionID)
	if err != nil {
		return ReaderArticleResource{}, err
	}
	revision := owned.Revision
	sourceDigest := domain.DigestText(revision.Content)
	if revision.ContentDigest != "" && !strings.EqualFold(revision.ContentDigest, sourceDigest) {
		return ReaderArticleResource{}, &ReaderResourceIntegrityError{Reason: "revision content digest mismatch"}
	}
	resource := ReaderArticleResource{
		CanonicalFragmentID: owned.CanonicalFragmentID,
		Revision:            revision,
		Format:              format,
	}
	switch format {
	case ArticleMarkdown:
		resource.ContentType = "text/markdown; charset=utf-8"
		resource.Body = []byte(revision.Content)
	case ArticleHTML:
		body, err := renderSanitizedArticle(revision.Content, revision.ContentFormat)
		if err != nil {
			return ReaderArticleResource{}, err
		}
		resource.ContentType = "text/html; charset=utf-8"
		resource.Body = body
	default:
		return ReaderArticleResource{}, fmt.Errorf("unsupported article format %q", format)
	}
	resource.Digest = domain.ContentDigest{Algorithm: "sha256", Value: domain.DigestText(string(resource.Body))}
	return resource, nil
}

func renderSanitizedArticle(source, format string) ([]byte, error) {
	var rendered bytes.Buffer
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "markdown":
		md := goldmark.New(goldmark.WithExtensions(extension.GFM))
		if err := md.Convert([]byte(source), &rendered); err != nil {
			return nil, fmt.Errorf("render reader article markdown: %w", err)
		}
	case "plain_text", "text":
		rendered.WriteString("<pre>")
		rendered.WriteString(html.EscapeString(source))
		rendered.WriteString("</pre>")
	default:
		return nil, &ReaderResourceUnavailableError{State: domain.AcquisitionAvailable, Reason: "unsupported_content_format"}
	}

	policy := bluemonday.NewPolicy()
	policy.AllowElements(
		"p", "br", "hr", "blockquote", "pre", "code", "em", "strong", "del",
		"h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li",
		"table", "thead", "tbody", "tr", "th", "td", "a",
	)
	policy.AllowAttrs("href", "title").OnElements("a")
	policy.AllowAttrs("start").Matching(bluemonday.Integer).OnElements("ol")
	policy.AllowAttrs("colspan", "rowspan").Matching(bluemonday.Integer).OnElements("th", "td")
	policy.AllowURLSchemes("http", "https", "mailto")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy.SanitizeBytes(rendered.Bytes()), nil
}

func (s *ReaderResourceService) OpenMedia(ctx context.Context, fragmentID, revisionID, variantID string) (ReaderMediaResource, error) {
	if s == nil || s.repo == nil || s.blobs == nil {
		return ReaderMediaResource{}, fmt.Errorf("reader media: repository and blob store are required")
	}
	owned, err := s.repo.GetVariant(ctx, fragmentID, revisionID, variantID)
	if err != nil {
		return ReaderMediaResource{}, err
	}
	variant := owned.Variant
	if variant.AcquisitionState != domain.AcquisitionAvailable || variant.Custody == domain.CustodyReference {
		state := variant.AcquisitionState
		if variant.Custody == domain.CustodyReference {
			state = domain.AcquisitionReferenceOnly
		}
		return ReaderMediaResource{}, &ReaderResourceUnavailableError{
			State: state, Reason: "acquisition_" + string(state), Retryable: state == domain.AcquisitionPending || (state == domain.AcquisitionFailed && variant.Failure != nil && variant.Failure.Retryable),
		}
	}

	var file *os.File
	legacy := false
	if owned.Blob != nil {
		file, err = s.blobs.Open(owned.Blob.StorageHandle)
		if err != nil {
			return ReaderMediaResource{}, &ReaderResourceIntegrityError{Reason: "content-addressed blob cannot be opened"}
		}
	} else if variant.Custody == domain.CustodyAdopted && strings.TrimSpace(variant.LegacyStoragePath) != "" {
		file, err = openLegacyReaderFile(s.legacyRoot, variant.LegacyStoragePath)
		if err != nil {
			return ReaderMediaResource{}, &ReaderResourceUnavailableError{State: variant.AcquisitionState, Reason: "legacy_path_not_authorized"}
		}
		legacy = true
	} else {
		return ReaderMediaResource{}, &ReaderResourceUnavailableError{State: variant.AcquisitionState, Reason: "verified_bytes_unavailable"}
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ReaderMediaResource{}, &ReaderResourceIntegrityError{Reason: "media is not a regular file"}
	}
	if owned.Blob != nil && owned.Blob.ByteSize != info.Size() {
		return ReaderMediaResource{}, &ReaderResourceIntegrityError{Reason: "blob size mismatch"}
	}
	if variant.ByteSize > 0 && variant.ByteSize != info.Size() {
		return ReaderMediaResource{}, &ReaderResourceIntegrityError{Reason: "variant size mismatch"}
	}
	actual, err := hashReaderFile(ctx, file)
	if err != nil {
		return ReaderMediaResource{}, err
	}
	for _, expected := range []string{variant.ExpectedDigest.Value, variant.Digest.Value, variant.BlobDigest} {
		if expected != "" && !strings.EqualFold(expected, actual.Value) {
			return ReaderMediaResource{}, &ReaderResourceIntegrityError{Reason: "media digest mismatch"}
		}
	}
	if owned.Blob != nil && !strings.EqualFold(owned.Blob.Digest.Value, actual.Value) {
		return ReaderMediaResource{}, &ReaderResourceIntegrityError{Reason: "blob digest mismatch"}
	}
	sample := make([]byte, 512)
	n, readErr := io.ReadFull(file, sample)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return ReaderMediaResource{}, fmt.Errorf("sniff reader media: %w", readErr)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ReaderMediaResource{}, fmt.Errorf("rewind reader media: %w", err)
	}
	contentType, err := safeReaderMediaType(owned.Asset, variant, sample[:n])
	if err != nil {
		return ReaderMediaResource{}, &ReaderResourceUnavailableError{State: variant.AcquisitionState, Reason: "unsafe_or_mismatched_mime"}
	}
	closeOnError = false
	return ReaderMediaResource{
		CanonicalFragmentID: owned.CanonicalFragmentID,
		RevisionID:          owned.RevisionID,
		Asset:               owned.Asset,
		Variant:             variant,
		File:                file,
		ContentType:         contentType,
		Digest:              actual,
		ByteSize:            info.Size(),
		ModifiedAt:          info.ModTime().UTC(),
		Legacy:              legacy,
	}, nil
}

func hashReaderFile(ctx context.Context, file *os.File) (domain.ContentDigest, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return domain.ContentDigest{}, fmt.Errorf("rewind reader media: %w", err)
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return domain.ContentDigest{}, err
		}
		n, err := file.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return domain.ContentDigest{}, fmt.Errorf("verify reader media: %w", err)
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return domain.ContentDigest{}, fmt.Errorf("rewind reader media: %w", err)
	}
	return domain.ContentDigest{Algorithm: "sha256", Value: hex.EncodeToString(h.Sum(nil))}, nil
}

func safeReaderMediaType(asset domain.MediaAsset, variant domain.AssetVariant, sample []byte) (string, error) {
	sniffed := normalizedMediaType(http.DetectContentType(sample))
	declared, err := declaredReaderMediaType(variant.MIMEType)
	if err != nil {
		return "", err
	}
	if activeReaderMIME(sniffed) || activeReaderMIME(declared) {
		return "", fmt.Errorf("active content MIME is not allowed")
	}
	if declared == "application/octet-stream" {
		declared = ""
	}
	if sniffed == "application/octet-stream" || sniffed == "" {
		return "", fmt.Errorf("media bytes have no recognized safe MIME")
	}
	selected := sniffed
	if declared != "" {
		if declared == sniffed || (sniffed == "text/plain" && isReaderTextType(declared)) {
			selected = declared
		} else {
			return "", fmt.Errorf("declared MIME does not match bytes")
		}
	}
	if !allowedReaderMediaType(asset.Kind, variant.Kind, selected) {
		return "", fmt.Errorf("MIME is not allowed for media kind")
	}
	return selected, nil
}

func normalizedMediaType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return canonicalReaderMediaType(strings.ToLower(mediaType))
}

func declaredReaderMediaType(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(mediaType, "/") {
		return "", fmt.Errorf("declared media MIME is invalid")
	}
	return canonicalReaderMediaType(strings.ToLower(mediaType)), nil
}

func canonicalReaderMediaType(value string) string {
	switch value {
	case "audio/wave", "audio/x-wav":
		return "audio/wav"
	default:
		return value
	}
}

func activeReaderMIME(value string) bool {
	if strings.HasSuffix(value, "+xml") {
		return true
	}
	switch value {
	case "image/svg+xml", "text/html", "application/xhtml+xml", "application/xml", "text/xml", "application/javascript", "text/javascript":
		return true
	default:
		return false
	}
}

func isReaderTextType(value string) bool {
	switch value {
	case "text/plain", "text/markdown", "text/vtt", "application/x-subrip":
		return true
	default:
		return false
	}
}

func allowedReaderMediaType(assetKind domain.MediaKind, variantKind domain.AssetVariantKind, value string) bool {
	raster := value == "image/jpeg" || value == "image/png" || value == "image/gif" || value == "image/webp"
	video := value == "video/mp4" || value == "video/webm"
	audio := value == "audio/mpeg" || value == "audio/wav"
	timedText := value == "text/plain" || value == "text/vtt" || value == "application/x-subrip"
	document := value == "application/pdf" || value == "text/plain" || value == "text/markdown"
	switch variantKind {
	case domain.VariantPreview, domain.VariantThumbnail, domain.VariantPoster:
		return raster
	case domain.VariantAudio:
		return audio
	case domain.VariantSubtitles, domain.VariantTranscript:
		return timedText
	case domain.VariantOriginal:
		switch assetKind {
		case domain.MediaImage:
			return raster
		case domain.MediaVideo:
			return video
		case domain.MediaAudio:
			return audio
		case domain.MediaDocument:
			return document
		case domain.MediaTimedText:
			return timedText
		default:
			return false
		}
	default:
		return false
	}
}
