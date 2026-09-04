package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

const (
	DefaultSegmentKey        = "root"
	DefaultContentFormat     = "markdown"
	DefaultAdapterVersion    = "1.0.0"
	DefaultNormalizerAdapter = "fe.material"
)

// NormalizedMaterial is the source-owned payload covered by a revision digest.
// Metadata and user state are retained on their respective records but are not
// revision identity. Media remains ordered by the producing source adapter.
type NormalizedMaterial struct {
	Title              string                    `json:"title"`
	Description        string                    `json:"description"`
	Content            string                    `json:"content"`
	ContentFormat      string                    `json:"content_format"`
	OrderedMedia       []RevisionMediaDescriptor `json:"ordered_media"`
	OrderedMediaDigest string                    `json:"ordered_media_digest"`
}

type RevisionMediaDescriptor struct {
	Kind         string `json:"kind"`
	Role         string `json:"role"`
	Name         string `json:"name"`
	MIMEType     string `json:"mime_type"`
	SourcePath   string `json:"source_path"`
	ExternalURL  string `json:"external_url"`
	Source       string `json:"source"`
	SourceItemID string `json:"source_item_id"`
}

func NormalizeMaterial(title, description, content, contentFormat string, attachments []PipelineAttachment) NormalizedMaterial {
	media := make([]RevisionMediaDescriptor, 0, len(attachments))
	for _, item := range attachments {
		media = append(media, RevisionMediaDescriptor{
			Kind:         strings.ToLower(strings.TrimSpace(item.Kind)),
			Role:         strings.ToLower(strings.TrimSpace(item.Role)),
			Name:         strings.TrimSpace(item.Name),
			MIMEType:     strings.ToLower(strings.TrimSpace(item.MIMEType)),
			SourcePath:   strings.TrimSpace(item.SourcePath),
			ExternalURL:  strings.TrimSpace(item.ExternalURL),
			Source:       strings.TrimSpace(item.Source),
			SourceItemID: strings.TrimSpace(item.SourceItemID),
		})
	}
	if strings.TrimSpace(contentFormat) == "" {
		contentFormat = DefaultContentFormat
	}
	material := NormalizedMaterial{
		Title:       normalizeNewlines(title),
		Description: normalizeNewlines(description),
		// Preserve leading/trailing source bytes. Line-ending normalization is
		// deterministic, while trimming title, description, or body would
		// destroy source-owned material during legacy backfill.
		Content:       normalizeNewlines(content),
		ContentFormat: strings.TrimSpace(strings.ToLower(contentFormat)),
		OrderedMedia:  media,
	}
	material.OrderedMediaDigest = digestJSON(media)
	return material
}

func normalizeNewlines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func (m NormalizedMaterial) Digest() string {
	return digestJSON(struct {
		Title              string                    `json:"title"`
		Description        string                    `json:"description"`
		Content            string                    `json:"content"`
		ContentFormat      string                    `json:"content_format"`
		OrderedMedia       []RevisionMediaDescriptor `json:"ordered_media"`
		OrderedMediaDigest string                    `json:"ordered_media_digest"`
	}{m.Title, m.Description, m.Content, m.ContentFormat, m.OrderedMedia, m.OrderedMediaDigest})
}

func DigestText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// NormalizeSourceIdentity applies compatibility defaults for pre-v1 producers.
// Explicit adapter output always wins; the metadata fallbacks exist so a legacy
// row and the same item re-ingested after migration resolve to the same key.
func NormalizeSourceIdentity(in PipelineFragment, ingestName string) SourceIdentity {
	identity := in.SourceIdentity
	identity.SourceRegistrationID = firstNonEmpty(identity.SourceRegistrationID, ingestName, in.Source)
	identity.Provider = firstNonEmpty(identity.Provider, sourceProvider(in.Source))
	identity.SourceItemKey = firstNonEmpty(identity.SourceItemKey, in.SourceID)
	identity.SegmentKey = firstNonEmpty(identity.SegmentKey, DefaultSegmentKey)

	if value := metadataString(in.Metadata, "source_url"); identity.SubmittedURL == "" && isHTTPURL(value) {
		identity.SubmittedURL = value
	}
	if value := metadataString(in.Metadata, "url"); identity.SubmittedURL == "" && isHTTPURL(value) {
		identity.SubmittedURL = value
	}
	if value := metadataString(in.Metadata, "final_url"); identity.CanonicalURL == "" && isHTTPURL(value) {
		identity.CanonicalURL = value
	}
	if identity.CanonicalURL == "" {
		if isHTTPURL(in.SourceID) {
			identity.CanonicalURL = in.SourceID
		} else if identity.SubmittedURL != "" {
			identity.CanonicalURL = identity.SubmittedURL
		}
	}
	if identity.SubmittedURL == "" && identity.CanonicalURL != "" {
		identity.SubmittedURL = identity.CanonicalURL
	}

	if videoID := metadataString(in.Metadata, "video_id"); videoID != "" {
		identity.Provider = "youtube"
		identity.ProviderItemID = firstNonEmpty(identity.ProviderItemID, videoID)
		if in.SourceIdentity.SourceItemKey == "" {
			identity.SourceItemKey = "youtube:" + videoID
		}
	}
	if pinID := metadataString(in.Metadata, "pin_id"); pinID != "" {
		identity.Provider = "pinterest"
		identity.ProviderItemID = firstNonEmpty(identity.ProviderItemID, pinID)
		if in.SourceIdentity.SourceItemKey == "" {
			identity.SourceItemKey = "pinterest:" + pinID
		}
	}
	if metadataString(in.Metadata, "ingest_mode") == "changed_doc" {
		repoName := metadataString(in.Metadata, "repo_name")
		relativePath := metadataString(in.Metadata, "relative_path")
		if in.SourceIdentity.SourceItemKey == "" && repoName != "" && relativePath != "" {
			identity.SourceItemKey = repoName + ":" + relativePath
		}
	}
	// Manual URL intake historically used a body hash as SourceID. The URL is
	// the actual source-item key and permits later material to become a revision.
	if in.SourceIdentity.SourceItemKey == "" && strings.TrimSpace(in.Source) == "manual" && identity.CanonicalURL != "" {
		identity.SourceItemKey = identity.CanonicalURL
	}
	if strings.TrimSpace(identity.SourceLocator) == "" && identity.CanonicalURL == "" {
		identity.SourceLocator = strings.TrimSpace(in.SourceID)
	}

	identity.SourceAdapter.Adapter = firstNonEmpty(identity.SourceAdapter.Adapter, ingestName, "legacy-ingest")
	identity.SourceAdapter.Version = firstNonEmpty(identity.SourceAdapter.Version, DefaultAdapterVersion)
	identity.Canonicalizer.Adapter = firstNonEmpty(identity.Canonicalizer.Adapter, identity.SourceAdapter.Adapter)
	identity.Canonicalizer.Version = firstNonEmpty(identity.Canonicalizer.Version, identity.SourceAdapter.Version)

	identity.SourceRegistrationID = strings.TrimSpace(identity.SourceRegistrationID)
	identity.SourceItemKey = strings.TrimSpace(identity.SourceItemKey)
	identity.SegmentKey = strings.TrimSpace(identity.SegmentKey)
	identity.Provider = sanitizeProvider(identity.Provider)
	identity.ProviderItemID = strings.TrimSpace(identity.ProviderItemID)
	identity.SourceLocator = strings.TrimSpace(identity.SourceLocator)
	identity.SubmittedURL = strings.TrimSpace(identity.SubmittedURL)
	identity.CanonicalURL = strings.TrimSpace(identity.CanonicalURL)
	return identity
}

func StableFragmentID(identity SourceIdentity) string {
	return DigestText(identity.SourceRegistrationID + "\n" + identity.SourceItemKey + "\n" + identity.SegmentKey)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

var nonProviderCharacter = regexp.MustCompile(`[^a-z0-9_-]+`)

func sourceProvider(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.HasPrefix(source, "git://") {
		return "git"
	}
	if source == "url" || strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return "web"
	}
	return source
}

func sanitizeProvider(provider string) string {
	provider = nonProviderCharacter.ReplaceAllString(strings.ToLower(strings.TrimSpace(provider)), "-")
	provider = strings.Trim(provider, "-_")
	if provider == "" || provider[0] < 'a' || provider[0] > 'z' {
		return "unknown"
	}
	return provider
}

func digestJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		// Every value accepted here is made only from JSON primitive fields.
		panic(err)
	}
	return DigestText(string(raw))
}
