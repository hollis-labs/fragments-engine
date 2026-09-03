package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MediaKind describes the renderer-neutral kind of a logical media object.
// Providers remain provenance and never become media kinds.
type MediaKind string

const (
	MediaImage     MediaKind = "image"
	MediaVideo     MediaKind = "video"
	MediaAudio     MediaKind = "audio"
	MediaDocument  MediaKind = "document"
	MediaTimedText MediaKind = "timed_text"
	MediaOther     MediaKind = "other"
)

func (k MediaKind) Valid() bool {
	switch k {
	case MediaImage, MediaVideo, MediaAudio, MediaDocument, MediaTimedText, MediaOther:
		return true
	default:
		return false
	}
}

type AssetVariantKind string

const (
	VariantOriginal   AssetVariantKind = "original"
	VariantPreview    AssetVariantKind = "preview"
	VariantThumbnail  AssetVariantKind = "thumbnail"
	VariantPoster     AssetVariantKind = "poster"
	VariantAudio      AssetVariantKind = "audio"
	VariantSubtitles  AssetVariantKind = "subtitles"
	VariantTranscript AssetVariantKind = "transcript"
)

func (k AssetVariantKind) Valid() bool {
	switch k {
	case VariantOriginal, VariantPreview, VariantThumbnail, VariantPoster,
		VariantAudio, VariantSubtitles, VariantTranscript:
		return true
	default:
		return false
	}
}

type AttachmentRole string

const (
	AttachmentPrimary     AttachmentRole = "primary"
	AttachmentGalleryItem AttachmentRole = "gallery_item"
	AttachmentHero        AttachmentRole = "hero"
	AttachmentInline      AttachmentRole = "inline"
	AttachmentPoster      AttachmentRole = "poster"
	AttachmentTranscript  AttachmentRole = "transcript"
	AttachmentOther       AttachmentRole = "other"
)

func (r AttachmentRole) Valid() bool {
	switch r {
	case AttachmentPrimary, AttachmentGalleryItem, AttachmentHero,
		AttachmentInline, AttachmentPoster, AttachmentTranscript, AttachmentOther:
		return true
	default:
		return false
	}
}

type CustodyMode string

const (
	CustodyReference CustodyMode = "reference"
	CustodyCache     CustodyMode = "cache"
	CustodyMirror    CustodyMode = "mirror"
	CustodyAdopted   CustodyMode = "adopted"
)

func (c CustodyMode) Valid() bool {
	switch c {
	case CustodyReference, CustodyCache, CustodyMirror, CustodyAdopted:
		return true
	default:
		return false
	}
}

type AcquisitionState string

const (
	AcquisitionPending       AcquisitionState = "pending"
	AcquisitionAvailable     AcquisitionState = "available"
	AcquisitionReferenceOnly AcquisitionState = "reference_only"
	AcquisitionFailed        AcquisitionState = "failed"
)

func (s AcquisitionState) Valid() bool {
	switch s {
	case AcquisitionPending, AcquisitionAvailable, AcquisitionReferenceOnly, AcquisitionFailed:
		return true
	default:
		return false
	}
}

type RetentionPolicy string

const (
	RetentionIndefinite RetentionPolicy = "indefinite"
	RetentionCache      RetentionPolicy = "cache"
	RetentionExternal   RetentionPolicy = "external"
)

// ContentDigest is an integrity identity, currently restricted to SHA-256 by
// the v1 capture contract.
type ContentDigest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

var sha256Hex = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (d ContentDigest) Normalized() (ContentDigest, error) {
	d.Algorithm = strings.ToLower(strings.TrimSpace(d.Algorithm))
	d.Value = strings.ToLower(strings.TrimSpace(d.Value))
	if d.Algorithm == "sha-256" {
		d.Algorithm = "sha256"
	}
	if d.Algorithm != "sha256" || !sha256Hex.MatchString(d.Value) {
		return ContentDigest{}, fmt.Errorf("digest must be sha256 with 64 lowercase hexadecimal characters")
	}
	return d, nil
}

func (d ContentDigest) Empty() bool {
	return strings.TrimSpace(d.Algorithm) == "" && strings.TrimSpace(d.Value) == ""
}

// MediaAsset is the stable, logical provider/source object. SourceMediaKey is
// the adapter-observed key (for browser capture, client_media_id). Identity
// precedence is provider media ID, then a position-independent source locator,
// then the client/source key as a compatibility fallback.
type MediaAsset struct {
	ID                   string      `json:"id"`
	IdentityKey          string      `json:"identity_key"`
	SourceRegistrationID string      `json:"source_registration_id"`
	Provider             string      `json:"provider"`
	ProviderMediaID      string      `json:"provider_media_id,omitempty"`
	SourceMediaKey       string      `json:"source_media_key"`
	SourceLocator        string      `json:"source_locator,omitempty"`
	Kind                 MediaKind   `json:"kind"`
	Width                int         `json:"width,omitempty"`
	Height               int         `json:"height,omitempty"`
	DurationSeconds      float64     `json:"duration_seconds,omitempty"`
	PageCount            int         `json:"page_count,omitempty"`
	AltText              string      `json:"alt_text,omitempty"`
	SourceAuthority      string      `json:"source_authority"`
	DefaultCustody       CustodyMode `json:"default_custody"`
	MetadataJSON         string      `json:"metadata_json,omitempty"`
	LegacyAttachmentID   string      `json:"legacy_attachment_id,omitempty"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
}

type AssetFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// AssetVariant owns acquisition and custody independently from every sibling
// representation of the logical MediaAsset.
type AssetVariant struct {
	ID                string           `json:"id"`
	MediaAssetID      string           `json:"media_asset_id"`
	VariantIdentity   string           `json:"variant_identity"`
	Kind              AssetVariantKind `json:"kind"`
	SourceURL         string           `json:"source_url,omitempty"`
	SourceExpiresAt   time.Time        `json:"source_expires_at,omitempty"`
	SourcePath        string           `json:"source_path,omitempty"`
	MIMEType          string           `json:"mime_type,omitempty"`
	Width             int              `json:"width,omitempty"`
	Height            int              `json:"height,omitempty"`
	DurationSeconds   float64          `json:"duration_seconds,omitempty"`
	ByteSize          int64            `json:"byte_size,omitempty"`
	ExpectedDigest    ContentDigest    `json:"expected_digest,omitempty"`
	Digest            ContentDigest    `json:"digest,omitempty"`
	BlobDigest        string           `json:"blob_digest,omitempty"`
	Custody           CustodyMode      `json:"custody"`
	AcquisitionState  AcquisitionState `json:"acquisition_state"`
	Failure           *AssetFailure    `json:"failure,omitempty"`
	Retention         RetentionPolicy  `json:"retention"`
	MetadataJSON      string           `json:"metadata_json,omitempty"`
	LegacyStoragePath string           `json:"legacy_storage_path,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// AttachmentRef is immutable placement of a logical asset in one immutable
// source revision. Position is zero-based and is the sole ordering key.
type AttachmentRef struct {
	ID                 string         `json:"id"`
	FragmentRevisionID string         `json:"fragment_revision_id"`
	MediaAssetID       string         `json:"media_asset_id"`
	Role               AttachmentRole `json:"role"`
	Position           int            `json:"position"`
	Caption            string         `json:"caption,omitempty"`
	SourceContext      string         `json:"source_context,omitempty"`
	LegacyFragmentID   string         `json:"legacy_fragment_id,omitempty"`
	LegacyAttachmentID string         `json:"legacy_attachment_id,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
}

type MediaManifestItem struct {
	Asset      MediaAsset     `json:"asset"`
	Variants   []AssetVariant `json:"variants"`
	Attachment AttachmentRef  `json:"attachment"`
}

type MediaBlob struct {
	Digest        ContentDigest   `json:"digest"`
	StorageHandle string          `json:"storage_handle"`
	ByteSize      int64           `json:"byte_size"`
	Retention     RetentionPolicy `json:"retention"`
	CreatedAt     time.Time       `json:"created_at"`
	VerifiedAt    time.Time       `json:"verified_at"`
}

func StableMediaAssetID(sourceRegistrationID, provider, providerMediaID, sourceMediaKey, sourceLocator string) (string, string) {
	preferred := strings.TrimSpace(providerMediaID)
	if preferred == "" {
		preferred = strings.TrimSpace(sourceLocator)
	}
	if preferred == "" {
		preferred = strings.TrimSpace(sourceMediaKey)
	}
	key := strings.Join([]string{
		strings.TrimSpace(sourceRegistrationID),
		strings.ToLower(strings.TrimSpace(provider)),
		preferred,
	}, "\n")
	identityKey := DigestText(key)
	return DigestText("media-asset\n" + identityKey), identityKey
}

func StableAssetVariantID(mediaAssetID, variantIdentity string) string {
	return DigestText("asset-variant\n" + strings.TrimSpace(mediaAssetID) + "\n" + strings.TrimSpace(variantIdentity))
}

func StableAttachmentRefID(fragmentRevisionID string, position int) string {
	return DigestText(fmt.Sprintf("attachment-ref\n%s\n%d", strings.TrimSpace(fragmentRevisionID), position))
}
