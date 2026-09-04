package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

const browserCaptureRegistration = "browser-capture"

// InvalidCaptureRequestError distinguishes application-level request semantics
// from persistence and projection failures. Transports may safely expose these
// errors as client validation failures.
type InvalidCaptureRequestError struct {
	Err error
}

func (e *InvalidCaptureRequestError) Error() string { return e.Err.Error() }
func (e *InvalidCaptureRequestError) Unwrap() error { return e.Err }

func IsInvalidCaptureRequest(err error) bool {
	var target *InvalidCaptureRequestError
	return errors.As(err, &target)
}

func invalidCaptureRequest(err error) error {
	if err == nil {
		return nil
	}
	return &InvalidCaptureRequestError{Err: err}
}

// CaptureService is the application boundary for the v1 manifest-first
// protocol. It accepts canonical contract bytes at the edge, then delegates one
// atomic domain write to CaptureRepository.
type CaptureService struct {
	captures *repository.CaptureRepository
	media    *MediaService
	now      func() time.Time
}

func NewCaptureService(captures *repository.CaptureRepository, media *MediaService) *CaptureService {
	return &CaptureService{captures: captures, media: media, now: time.Now}
}

func (s *CaptureService) AcceptManifest(ctx context.Context, raw []byte) (capturecontract.CaptureManifestResponse, error) {
	if s == nil || s.captures == nil {
		return capturecontract.CaptureManifestResponse{}, fmt.Errorf("accept capture manifest: capture repository is required")
	}
	envelope, err := capturecontract.DecodeCaptureEnvelope(raw)
	if err != nil {
		return capturecontract.CaptureManifestResponse{}, err
	}
	canonicalEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return capturecontract.CaptureManifestResponse{}, fmt.Errorf("encode capture manifest: %w", err)
	}
	now := s.now().UTC()
	fragment, media, bindings, err := buildCaptureManifest(envelope, now)
	if err != nil {
		return capturecontract.CaptureManifestResponse{}, invalidCaptureRequest(err)
	}
	pageContext, _ := json.Marshal(envelope.PageContext)
	if envelope.PageContext == nil {
		pageContext = []byte(`{}`)
	}
	extraction, _ := json.Marshal(envelope.Extraction)
	warnings, _ := json.Marshal(envelope.Extraction.Warnings)
	followUp, _ := json.Marshal(map[string]any{
		"provider":                   envelope.Source.Provider,
		"observed_capabilities":      envelope.Extraction.ObservedCapabilities,
		"extraction_adapter":         envelope.Extraction.Adapter,
		"extraction_adapter_version": envelope.Extraction.AdapterVersion,
	})
	annotations := make([]domain.CaptureAnnotation, 0, len(envelope.Annotations))
	for _, item := range envelope.Annotations {
		annotation := domain.CaptureAnnotation{
			ID: item.AnnotationID, Kind: domain.CaptureAnnotationKind(item.Kind),
			Text: item.Text, ActorID: envelope.PrincipalID, CapturedAt: envelope.CapturedAt,
		}
		if item.Selector != nil {
			annotation.Selector = &domain.TextQuoteSelector{Exact: item.Selector.Exact, Prefix: item.Selector.Prefix, Suffix: item.Selector.Suffix}
		}
		if item.Position != nil {
			annotation.Position = &domain.DocumentPosition{BlockAnchor: item.Position.BlockAnchor, StartOffset: item.Position.StartOffset, EndOffset: item.Position.EndOffset}
		}
		annotations = append(annotations, annotation)
	}
	tags := make([]domain.AttributedTag, 0, len(envelope.Tags))
	for _, value := range envelope.Tags {
		tags = append(tags, domain.AttributedTag{Value: value, Source: domain.AttributionUser,
			Producer: domain.AdapterVersion{Adapter: envelope.Client.Kind, Version: envelope.Client.Version}})
	}
	descriptions := make([]domain.DescriptionObservation, 0, 1)
	if envelope.Document.Description != "" {
		descriptions = append(descriptions, domain.DescriptionObservation{
			Value: envelope.Document.Description, Source: domain.AttributionSourceMaterial,
			Producer: domain.AdapterVersion{Adapter: envelope.Extraction.Adapter, Version: envelope.Extraction.AdapterVersion},
		})
	}
	contextService := NewCaptureContextService(s.captures)
	contextService.now = s.now
	write, err := contextService.normalize(CaptureContextRequest{
		Fragment: fragment, CaptureID: envelope.CaptureID, IdempotencyKey: envelope.CaptureID,
		ProtocolPayloadDigest: domain.DigestText(string(canonicalEnvelope)), PrincipalID: envelope.PrincipalID,
		ActorID: envelope.PrincipalID, Client: domain.CaptureClient{Kind: envelope.Client.Kind, Version: envelope.Client.Version},
		CapturedAt: envelope.CapturedAt, SubmittedURL: envelope.Source.SubmittedURL,
		PageContextJSON: string(pageContext), ExtractionAdapter: domain.AdapterVersion{Adapter: envelope.Extraction.Adapter, Version: envelope.Extraction.AdapterVersion},
		ExtractionJSON: string(extraction), Completion: domain.CaptureAccepting,
		WarningsJSON: string(warnings), Annotations: annotations, Tags: tags, Descriptions: descriptions,
	})
	if err != nil {
		return capturecontract.CaptureManifestResponse{}, invalidCaptureRequest(err)
	}
	write.Media = media
	write.AssetBindings = bindings
	write.EnrichmentObservations, write.CapabilityCoverage = buildInitialCaptureEnrichment(envelope, fragment, media, now)
	write.FollowUpKind = "capture_enrichment"
	write.FollowUpPayloadJSON = string(followUp)
	write.BuildAcceptanceSnapshot = func(accepted domain.CaptureAcceptance) (string, error) {
		response := projectManifestResponse(envelope, accepted)
		encoded, err := json.Marshal(response)
		if err != nil {
			return "", err
		}
		if err := capturecontract.ValidateJSON(capturecontract.SchemaCaptureResponse, encoded); err != nil {
			return "", err
		}
		return string(encoded), nil
	}
	accepted, err := s.captures.Accept(ctx, write)
	if err != nil {
		return capturecontract.CaptureManifestResponse{}, err
	}
	var response capturecontract.CaptureManifestResponse
	if err := json.Unmarshal([]byte(accepted.Attempt.AcceptanceResultJSON), &response); err != nil {
		return capturecontract.CaptureManifestResponse{}, fmt.Errorf("decode capture acceptance snapshot: %w", err)
	}
	response.IdempotentReplay = accepted.IdempotentReplay
	encoded, _ := json.Marshal(response)
	if err := capturecontract.ValidateJSON(capturecontract.SchemaCaptureResponse, encoded); err != nil {
		return capturecontract.CaptureManifestResponse{}, fmt.Errorf("validate stored capture response: %w", err)
	}
	return response, nil
}

func buildCaptureManifest(envelope capturecontract.CaptureEnvelope, now time.Time) (domain.Fragment, []domain.MediaManifestItem, []domain.CaptureAssetBinding, error) {
	registration := strings.TrimSpace(envelope.Source.SourceRegistrationID)
	if registration == "" {
		registration = browserCaptureRegistration
	}
	media, bindings, mediaMaterial, err := normalizeCaptureMedia(envelope, registration, now)
	if err != nil {
		return domain.Fragment{}, nil, nil, err
	}
	identity := domain.SourceIdentity{
		SourceRegistrationID: registration, Provider: strings.ToLower(strings.TrimSpace(envelope.Source.Provider)),
		ProviderItemID: strings.TrimSpace(envelope.Source.ProviderItemID), SourceItemKey: strings.TrimSpace(envelope.Source.SourceItemKey),
		SourceLocator: strings.TrimSpace(envelope.Source.SourceLocator), SegmentKey: strings.TrimSpace(envelope.Source.SegmentKey),
		SubmittedURL: strings.TrimSpace(envelope.Source.SubmittedURL), CanonicalURL: strings.TrimSpace(envelope.Source.CanonicalURL),
		SourceAdapter: domain.AdapterVersion{Adapter: envelope.Extraction.Adapter, Version: envelope.Extraction.AdapterVersion},
	}
	if envelope.Source.Canonicalizer != nil {
		identity.Canonicalizer = domain.AdapterVersion{Adapter: envelope.Source.Canonicalizer.Adapter, Version: envelope.Source.Canonicalizer.Version}
	} else {
		identity.Canonicalizer = identity.SourceAdapter
	}
	if identity.SourceRegistrationID == "" || identity.SourceItemKey == "" || identity.SegmentKey == "" {
		return domain.Fragment{}, nil, nil, fmt.Errorf("accept capture manifest: stable source identity is incomplete")
	}
	content, format := "", domain.DefaultContentFormat
	if envelope.Document.Content != nil {
		content = normalizeCaptureText(envelope.Document.Content.Body)
		format = strings.ToLower(strings.TrimSpace(envelope.Document.Content.Format))
	}
	title := normalizeCaptureText(envelope.Document.Title)
	if title == "" {
		title = firstCaptureText(envelope.Document.Description, envelope.Source.CanonicalURL, envelope.Source.SourceItemKey)
	}
	description := normalizeCaptureText(envelope.Document.Description)
	orderedMediaRaw, _ := json.Marshal(mediaMaterial)
	orderedMediaDigest := domain.DigestText(string(orderedMediaRaw))
	documentMetadata := map[string]any{
		"byline": envelope.Document.Byline, "published_at": envelope.Document.PublishedAt,
		"language": envelope.Document.Language, "extensions": envelope.Document.Extensions,
	}
	metadataRaw, _ := json.Marshal(documentMetadata)
	materialRaw, _ := json.Marshal(struct {
		Title              string                             `json:"title"`
		Description        string                             `json:"description"`
		Content            string                             `json:"content"`
		ContentFormat      string                             `json:"content_format"`
		Byline             string                             `json:"byline"`
		PublishedAt        *time.Time                         `json:"published_at,omitempty"`
		Language           string                             `json:"language"`
		Extensions         capturecontract.ProviderExtensions `json:"extensions,omitempty"`
		OrderedMedia       json.RawMessage                    `json:"ordered_media"`
		OrderedMediaDigest string                             `json:"ordered_media_digest"`
	}{title, description, content, format, envelope.Document.Byline,
		envelope.Document.PublishedAt, envelope.Document.Language,
		envelope.Document.Extensions, orderedMediaRaw, orderedMediaDigest})
	fragmentID := domain.StableFragmentID(identity)
	createdAt := envelope.CapturedAt.UTC()
	fragment := domain.Fragment{
		ID: fragmentID, Source: "browser", SourceType: captureRenderer(envelope, media),
		SourceID: identity.SourceItemKey, SourceIdentity: identity, Title: title,
		Content: content, ContentHash: domain.DigestText(content), CreatedAt: createdAt,
		IngestedAt: now, Status: domain.FragmentStatusInbox, MetadataJSON: string(metadataRaw),
		IngestName: registration,
		Revision: domain.FragmentRevision{
			FragmentID: fragmentID, MaterialDigest: domain.DigestText(string(materialRaw)),
			ContentDigest: domain.DigestText(content), Title: title, Description: description,
			Content: content, ContentFormat: format, OrderedMediaDigest: orderedMediaDigest,
			MetadataJSON: string(metadataRaw), Normalizer: domain.AdapterVersion{Adapter: "fe.capture", Version: "1.0.0"},
			ObservedAt: createdAt, CommittedAt: now,
		},
	}
	return fragment, media, bindings, nil
}

type captureMediaMaterial struct {
	LogicalIdentity string                   `json:"logical_identity"`
	Kind            string                   `json:"kind"`
	Role            string                   `json:"role"`
	Position        int                      `json:"position"`
	Width           *int                     `json:"width,omitempty"`
	Height          *int                     `json:"height,omitempty"`
	DurationSeconds *float64                 `json:"duration_seconds,omitempty"`
	PageCount       *int                     `json:"page_count,omitempty"`
	AltText         string                   `json:"alt_text"`
	Caption         string                   `json:"caption"`
	SourceContext   string                   `json:"source_context"`
	Variants        []captureVariantMaterial `json:"variants"`
}

type captureVariantMaterial struct {
	Kind            string                  `json:"kind"`
	MIMEType        string                  `json:"mime_type"`
	Width           *int                    `json:"width,omitempty"`
	Height          *int                    `json:"height,omitempty"`
	DurationSeconds *float64                `json:"duration_seconds,omitempty"`
	ByteSize        *int64                  `json:"byte_size,omitempty"`
	Digest          *capturecontract.Digest `json:"digest,omitempty"`
}

func normalizeCaptureMedia(envelope capturecontract.CaptureEnvelope, registration string, now time.Time) ([]domain.MediaManifestItem, []domain.CaptureAssetBinding, []captureMediaMaterial, error) {
	items := append([]capturecontract.CaptureMediaItem(nil), envelope.Media...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
	media := make([]domain.MediaManifestItem, 0, len(items))
	bindings := make([]domain.CaptureAssetBinding, 0)
	material := make([]captureMediaMaterial, 0, len(items))
	seenMedia := make(map[string]struct{}, len(items))
	seenVariants := make(map[string]struct{})
	for index, item := range items {
		if item.Position != index {
			return nil, nil, nil, fmt.Errorf("accept capture manifest: media positions must be contiguous and zero-based (want %d, got %d)", index, item.Position)
		}
		if _, exists := seenMedia[item.ClientMediaID]; exists {
			return nil, nil, nil, fmt.Errorf("accept capture manifest: duplicate client media ID %q", item.ClientMediaID)
		}
		seenMedia[item.ClientMediaID] = struct{}{}
		locator := strings.TrimSpace(item.SourceLocator)
		if locator == "" {
			locator = stableObservedMediaLocator(item.Variants)
		}
		logicalIdentity := strings.TrimSpace(item.ProviderMediaID)
		if logicalIdentity == "" {
			logicalIdentity = locator
		}
		if logicalIdentity == "" {
			return nil, nil, nil, fmt.Errorf("accept capture manifest: media %q requires provider_media_id, source_locator, or a stable observed source URL", item.ClientMediaID)
		}
		defaultCustody := domain.CustodyMode(item.DefaultCustody)
		if defaultCustody == "" {
			defaultCustody = domain.CustodyReference
		}
		metadataRaw, _ := json.Marshal(item.Extensions)
		asset := domain.MediaAsset{
			SourceRegistrationID: registration, Provider: strings.ToLower(envelope.Source.Provider),
			ProviderMediaID: item.ProviderMediaID, SourceMediaKey: item.ClientMediaID,
			SourceLocator: locator, Kind: domain.MediaKind(item.Kind),
			Width: intValue(item.Width), Height: intValue(item.Height), DurationSeconds: floatValue(item.DurationSeconds),
			PageCount: intValue(item.PageCount), AltText: item.AltText,
			SourceAuthority: envelope.Extraction.Adapter, DefaultCustody: defaultCustody,
			MetadataJSON: string(metadataRaw), CreatedAt: now,
		}
		variants := make([]domain.AssetVariant, 0, len(item.Variants))
		variantMaterial := make([]captureVariantMaterial, 0, len(item.Variants))
		semanticVariants := make([]captureVariantMaterial, len(item.Variants))
		semanticVariantKeys := make([]string, len(item.Variants))
		semanticVariantCounts := make(map[string]int, len(item.Variants))
		for variantIndex, variant := range item.Variants {
			semanticVariants[variantIndex] = captureVariantMaterial{
				Kind: variant.Kind, MIMEType: strings.ToLower(variant.MIMEType), Width: variant.Width,
				Height: variant.Height, DurationSeconds: variant.DurationSeconds,
				ByteSize: variant.ByteSize, Digest: variant.Digest,
			}
			raw, _ := json.Marshal(semanticVariants[variantIndex])
			semanticVariantKeys[variantIndex] = string(raw)
			semanticVariantCounts[string(raw)]++
		}
		seenLogicalVariants := make(map[string]struct{}, len(item.Variants))
		for variantIndex, variant := range item.Variants {
			if _, exists := seenVariants[variant.ClientVariantID]; exists {
				return nil, nil, nil, fmt.Errorf("accept capture manifest: duplicate client variant ID %q", variant.ClientVariantID)
			}
			seenVariants[variant.ClientVariantID] = struct{}{}
			custody := domain.CustodyMode(variant.RequestedCustody)
			action := domain.AssetInstructionAction("")
			reason := ""
			if envelope.Source.Provider == "youtube" && item.Kind == "video" && variant.Kind == "original" {
				custody = domain.CustodyReference
				action = domain.AssetReferenceOnly
				reason = "youtube original video remains reference-only by default"
			} else if variant.TransferPreference == "reference_only" || custody == domain.CustodyReference {
				custody = domain.CustodyReference
				action = domain.AssetReferenceOnly
			} else if variant.TransferPreference == "browser_preferred" {
				action = domain.AssetRequestUpload
			} else if variant.TransferPreference == "server_preferred" && variant.SourceURL != "" {
				action = domain.AssetServerAcquire
			} else {
				action = domain.AssetRejected
				reason = "server acquisition requires an observed source URL"
			}
			state := domain.AcquisitionPending
			if action == domain.AssetReferenceOnly {
				state = domain.AcquisitionReferenceOnly
			} else if action == domain.AssetRejected {
				state = domain.AcquisitionFailed
			}
			expected := domain.ContentDigest{}
			if variant.Digest != nil {
				expected = domain.ContentDigest{Algorithm: variant.Digest.Algorithm, Value: variant.Digest.Value}
			}
			materialVariant := semanticVariants[variantIndex]
			identityMaterial := semanticVariantKeys[variantIndex]
			if semanticVariantCounts[identityMaterial] > 1 {
				locator := stableObservedURL(variant.SourceURL)
				if locator == "" {
					return nil, nil, nil, fmt.Errorf("accept capture manifest: same-shaped variants on media %q require distinct stable source URLs", item.ClientMediaID)
				}
				identityMaterial += "\n" + locator
			}
			variantIdentity := domain.DigestText("capture-asset-variant\n" + identityMaterial)
			if _, duplicate := seenLogicalVariants[variantIdentity]; duplicate {
				return nil, nil, nil, fmt.Errorf("accept capture manifest: media %q has duplicate logical variant semantics", item.ClientMediaID)
			}
			seenLogicalVariants[variantIdentity] = struct{}{}
			variantMetadata, _ := json.Marshal(variant.Extensions)
			v := domain.AssetVariant{
				VariantIdentity: variantIdentity, Kind: domain.AssetVariantKind(variant.Kind),
				SourceURL: variant.SourceURL, MIMEType: variant.MIMEType,
				Width: intValue(variant.Width), Height: intValue(variant.Height), DurationSeconds: floatValue(variant.DurationSeconds),
				ByteSize: int64Value(variant.ByteSize), ExpectedDigest: expected,
				Custody: custody, AcquisitionState: state, MetadataJSON: string(variantMetadata), CreatedAt: now,
			}
			if variant.SourceExpiresAt != nil {
				v.SourceExpiresAt = variant.SourceExpiresAt.UTC()
			}
			if action == domain.AssetRejected {
				v.Failure = &domain.AssetFailure{Code: "capture.variant_rejected", Message: reason, Retryable: false}
			}
			variants = append(variants, v)
			bindings = append(bindings, domain.CaptureAssetBinding{
				ClientVariantID: variant.ClientVariantID, Action: action,
				ExpectedDigest: expected, Reason: reason, MediaPosition: item.Position,
				VariantIdentity: variantIdentity,
			})
			variantMaterial = append(variantMaterial, materialVariant)
		}
		sort.Slice(variantMaterial, func(i, j int) bool {
			left, _ := json.Marshal(variantMaterial[i])
			right, _ := json.Marshal(variantMaterial[j])
			return string(left) < string(right)
		})
		media = append(media, domain.MediaManifestItem{Asset: asset, Variants: variants, Attachment: domain.AttachmentRef{
			Role: domain.AttachmentRole(item.Role), Position: item.Position,
			Caption: item.Caption, SourceContext: item.SourceContext, CreatedAt: now,
		}})
		material = append(material, captureMediaMaterial{
			LogicalIdentity: logicalIdentity, Kind: item.Kind, Role: item.Role,
			Position: item.Position, Width: item.Width, Height: item.Height,
			DurationSeconds: item.DurationSeconds, PageCount: item.PageCount,
			AltText: item.AltText, Caption: item.Caption, SourceContext: item.SourceContext,
			Variants: variantMaterial,
		})
	}
	return media, bindings, material, nil
}

func stableObservedMediaLocator(variants []capturecontract.CaptureAssetVariant) string {
	type candidate struct {
		priority int
		locator  string
	}
	candidates := make([]candidate, 0, len(variants))
	for _, variant := range variants {
		locator := stableObservedURL(variant.SourceURL)
		if locator == "" {
			continue
		}
		priority := 1
		if variant.Kind == "original" {
			priority = 0
		}
		candidates = append(candidates, candidate{priority: priority, locator: locator})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].locator < candidates[j].locator
	})
	if len(candidates) != 0 {
		return candidates[0].locator
	}
	return ""
}

// stableObservedURL retains URL components that can distinguish a logical
// source representation while removing common expiring authorization hints.
// A host-only URL is too weak to serve as media identity.
func stableObservedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	query := u.Query()
	for key := range query {
		if ephemeralURLParameter(key) {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	u.ForceQuery = false
	if (u.Path == "" || u.Path == "/") && u.RawQuery == "" {
		return ""
	}
	return u.String()
}

func ephemeralURLParameter(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if strings.HasPrefix(key, "x-amz-") || strings.HasPrefix(key, "x-goog-") {
		return true
	}
	switch key {
	case "access_token", "auth", "authorization", "exp", "expires", "expiry",
		"key-pair-id", "key_pair_id", "policy", "sig", "signature", "token":
		return true
	default:
		return false
	}
}

func projectManifestResponse(envelope capturecontract.CaptureEnvelope, accepted domain.CaptureAcceptance) capturecontract.CaptureManifestResponse {
	instructions := make([]capturecontract.AssetInstruction, 0, len(accepted.AssetBindings))
	for _, binding := range accepted.AssetBindings {
		instruction := capturecontract.AssetInstruction{ClientVariantID: binding.ClientVariantID,
			AssetVariantID: binding.AssetVariantID, Action: string(binding.Action)}
		switch binding.Action {
		case domain.AssetRequestUpload:
			instruction.UploadHref = "/v1/captures/" + url.PathEscape(accepted.Attempt.CaptureID) + "/assets/" + url.PathEscape(binding.ClientVariantID) + "/content"
			instruction.ExpectedDigest = contractDigest(binding.ExpectedDigest)
		case domain.AssetReuseBlob:
			instruction.Digest = contractDigest(binding.Digest)
		case domain.AssetRejected:
			instruction.AssetVariantID = ""
			instruction.Reason = binding.Reason
		}
		instructions = append(instructions, instruction)
	}
	warnings := append([]capturecontract.ContractWarning(nil), envelope.Extraction.Warnings...)
	if warnings == nil {
		warnings = []capturecontract.ContractWarning{}
	}
	return capturecontract.CaptureManifestResponse{
		SchemaVersion: capturecontract.CaptureResultVersion, CaptureID: accepted.Attempt.CaptureID,
		FragmentID: accepted.Fragment.ID, FragmentRevisionID: accepted.ObservedRevision.ID,
		CaptureAttemptID: accepted.Attempt.ID, Completion: string(accepted.Attempt.Completion),
		IdempotentReplay: false, AssetInstructions: instructions, Warnings: warnings,
		ReaderItem: projectOptimisticReader(envelope, accepted),
	}
}

func projectOptimisticReader(envelope capturecontract.CaptureEnvelope, accepted domain.CaptureAcceptance) capturecontract.ReaderItem {
	description := (*capturecontract.ResolvedText)(nil)
	if accepted.ObservedRevision.Description != "" {
		description = &capturecontract.ResolvedText{Value: accepted.ObservedRevision.Description, Source: "source"}
	}
	summary := accepted.ObservedRevision.Description
	if summary == "" {
		summary = boundedRunes(accepted.ObservedRevision.Content, 4000)
	}
	if summary == "" {
		summary = accepted.ObservedRevision.Title
	}
	full := accepted.ObservedRevision.Content != ""
	article := capturecontract.ReaderArticle{PreviewMarkdown: boundedRunes(accepted.ObservedRevision.Content, 4000), FullContentAvailable: full}
	if full {
		article.FullContentHref = "/v1/reader/items/" + url.PathEscape(accepted.Fragment.ID) + "/content?revision_id=" + url.QueryEscape(accepted.ObservedRevision.ID)
	}
	media := make([]capturecontract.ReaderMediaItem, 0, len(accepted.Media))
	acquisition := capturecontract.AcquisitionSummary{}
	for _, item := range accepted.Media {
		variants := make([]capturecontract.ReaderAssetVariant, 0, len(item.Variants))
		for _, variant := range item.Variants {
			out := capturecontract.ReaderAssetVariant{
				AssetVariantID: variant.ID, Kind: string(variant.Kind), Custody: string(variant.Custody),
				AcquisitionState: string(variant.AcquisitionState), MIMEType: variant.MIMEType,
				Width: positiveIntPointer(variant.Width), Height: positiveIntPointer(variant.Height),
				DurationSeconds: positiveFloatPointer(variant.DurationSeconds), ByteSize: nonzeroInt64Pointer(variant.ByteSize),
				Digest: contractDigest(variant.Digest), SourceURL: variant.SourceURL,
			}
			if variant.BlobDigest != "" {
				out.ContentHref = "/v1/media/variants/" + url.PathEscape(variant.ID) + "/content"
			}
			if variant.Failure != nil {
				out.Failure = &capturecontract.AssetFailure{Code: variant.Failure.Code, Message: variant.Failure.Message, Retryable: variant.Failure.Retryable}
			}
			switch variant.AcquisitionState {
			case domain.AcquisitionPending:
				acquisition.Pending++
			case domain.AcquisitionAvailable:
				acquisition.Available++
			case domain.AcquisitionReferenceOnly:
				acquisition.ReferenceOnly++
			case domain.AcquisitionFailed:
				acquisition.Failed++
			}
			variants = append(variants, out)
		}
		media = append(media, capturecontract.ReaderMediaItem{
			Attachment: capturecontract.AttachmentRef{AttachmentID: item.Attachment.ID,
				FragmentRevisionID: item.Attachment.FragmentRevisionID, MediaAssetID: item.Asset.ID,
				Role: string(item.Attachment.Role), Position: item.Attachment.Position,
				Caption: item.Attachment.Caption, SourceContext: item.Attachment.SourceContext},
			MediaAssetID: item.Asset.ID, ProviderMediaID: item.Asset.ProviderMediaID,
			Kind: string(item.Asset.Kind), AltText: item.Asset.AltText, Variants: variants,
		})
	}
	combined, attributed := projectTags(accepted.Tags)
	annotations := make([]capturecontract.ReaderAnnotation, 0, len(accepted.Annotations))
	for _, item := range accepted.Annotations {
		annotation := capturecontract.ReaderAnnotation{AnnotationID: item.ID, CaptureID: item.CaptureID,
			Kind: string(item.Kind), Text: item.Text, CapturedAt: item.CapturedAt}
		if item.Selector != nil {
			annotation.Selector = &capturecontract.TextQuoteSelector{Exact: item.Selector.Exact, Prefix: item.Selector.Prefix, Suffix: item.Selector.Suffix}
		}
		if item.Position != nil {
			annotation.Position = &capturecontract.DocumentPosition{BlockAnchor: item.Position.BlockAnchor, StartOffset: item.Position.StartOffset, EndOffset: item.Position.EndOffset}
		}
		annotations = append(annotations, annotation)
	}
	enrichment := projectCapabilityCoverage(accepted.Coverage)
	source := capturecontract.SourceIdentity{
		SourceRegistrationID: accepted.Fragment.SourceIdentity.SourceRegistrationID,
		SubmittedURL:         accepted.Fragment.SourceIdentity.SubmittedURL, CanonicalURL: accepted.Fragment.SourceIdentity.CanonicalURL,
		Provider: accepted.Fragment.SourceIdentity.Provider, ProviderItemID: accepted.Fragment.SourceIdentity.ProviderItemID,
		SourceItemKey: accepted.Fragment.SourceIdentity.SourceItemKey, SourceLocator: accepted.Fragment.SourceIdentity.SourceLocator,
		SegmentKey: accepted.Fragment.SourceIdentity.SegmentKey,
	}
	if accepted.Fragment.SourceIdentity.Canonicalizer.Adapter != "" {
		source.Canonicalizer = &capturecontract.Canonicalizer{Adapter: accepted.Fragment.SourceIdentity.Canonicalizer.Adapter, Version: accepted.Fragment.SourceIdentity.Canonicalizer.Version}
	}
	item := capturecontract.ReaderItem{
		SchemaVersion: capturecontract.ReaderItemVersion, FragmentID: accepted.Fragment.ID,
		FragmentRevisionID: accepted.ObservedRevision.ID, Revision: 0,
		Source: source, Renderer: captureRenderer(envelope, accepted.Media),
		Display: capturecontract.ReaderDisplay{Title: capturecontract.ResolvedText{Value: accepted.ObservedRevision.Title, Source: "source"},
			Description: description, Summary: capturecontract.ResolvedText{Value: summary, Source: "deterministic"}},
		Article: article, Media: media,
		Tags:        capturecontract.ReaderTags{Combined: combined, Attributed: attributed},
		Annotations: annotations, CaptureCount: accepted.Attempt.AcceptedCaptureCount,
		ReadingState: capturecontract.ReadingState{PrincipalID: accepted.Attempt.PrincipalID,
			FragmentID: accepted.Fragment.ID, State: "unread", Position: capturecontract.ReadingPosition{Kind: "none"}, Revision: 0},
		Operations: capturecontract.OperationalSummaries{
			Triage:          capturecontract.TriageSummary{CaseIDs: []string{}, UnresolvedCount: 0},
			Routing:         capturecontract.EffectSummary{State: "none", References: []string{}},
			Materialization: capturecontract.EffectSummary{State: "none", References: []string{}},
			Enrichment:      enrichment, Acquisition: acquisition,
		},
		Actions: []capturecontract.CommandCapability{},
	}
	item.Playback = readerYouTubePlayback(item.Renderer, accepted.Fragment.SourceIdentity)
	return item
}

func (s *CaptureService) StoreAssetContent(ctx context.Context, captureID, clientVariantID string, digest domain.ContentDigest, src io.Reader) (domain.AssetVariant, error) {
	if s == nil || s.captures == nil || s.media == nil {
		return domain.AssetVariant{}, fmt.Errorf("store capture asset: capture and media services are required")
	}
	binding, err := s.captures.GetAssetBinding(ctx, captureID, clientVariantID)
	if err != nil {
		return domain.AssetVariant{}, err
	}
	var lockedBinding domain.CaptureAssetBinding
	return s.media.StoreVariantContentAtomic(ctx, binding.AssetVariantID, digest, src,
		func(ctx context.Context, q repository.MediaWriteConn, variant domain.AssetVariant) error {
			var err error
			lockedBinding, err = s.captures.ValidateAssetUploadOn(ctx, q, captureID, clientVariantID, variant.ID)
			return err
		},
		func(ctx context.Context, q repository.MediaWriteConn, variant domain.AssetVariant) error {
			return s.captures.RecordUploadedOutcomeOn(ctx, q, lockedBinding, variant, s.now().UTC())
		})
}

func (s *CaptureService) Complete(ctx context.Context, captureID string, raw []byte) (capturecontract.CaptureStatus, error) {
	if s == nil || s.captures == nil {
		return capturecontract.CaptureStatus{}, fmt.Errorf("complete capture: capture repository is required")
	}
	request, err := capturecontract.DecodeCaptureCompletion(raw)
	if err != nil {
		return capturecontract.CaptureStatus{}, err
	}
	stateBefore, err := s.captures.GetProtocolState(ctx, captureID)
	if err != nil {
		return capturecontract.CaptureStatus{}, err
	}
	knownVariants := make(map[string]struct{}, len(stateBefore.Bindings))
	for _, binding := range stateBefore.Bindings {
		knownVariants[binding.ClientVariantID] = struct{}{}
	}
	reportedVariants := make(map[string]struct{}, len(request.Assets))
	for _, item := range request.Assets {
		if _, exists := knownVariants[item.ClientVariantID]; !exists {
			return capturecontract.CaptureStatus{}, invalidCaptureRequest(fmt.Errorf("complete capture: client variant %q does not belong to capture", item.ClientVariantID))
		}
		if _, duplicate := reportedVariants[item.ClientVariantID]; duplicate {
			return capturecontract.CaptureStatus{}, invalidCaptureRequest(fmt.Errorf("complete capture: duplicate client variant %q", item.ClientVariantID))
		}
		reportedVariants[item.ClientVariantID] = struct{}{}
	}
	canonical, _ := json.Marshal(request)
	warnings, _ := json.Marshal(request.Warnings)
	outcomes := make([]domain.CaptureVariantOutcome, 0, len(request.Assets))
	for _, item := range request.Assets {
		outcome := domain.CaptureVariantOutcome{CaptureID: captureID, ClientVariantID: item.ClientVariantID,
			Outcome: domain.CaptureAssetOutcome(item.Outcome), Reason: item.Reason}
		if item.Digest != nil {
			outcome.Digest = domain.ContentDigest{Algorithm: item.Digest.Algorithm, Value: item.Digest.Value}
		}
		if item.ByteSize != nil {
			outcome.ByteSize = *item.ByteSize
		}
		if item.Retryable != nil {
			outcome.Retryable = *item.Retryable
		}
		outcomes = append(outcomes, outcome)
	}
	write := repository.CaptureCompletionWrite{CaptureID: strings.TrimSpace(captureID), IdempotencyKey: request.IdempotencyKey,
		SemanticDigest: domain.DigestText(string(canonical)), ReportJSON: string(canonical),
		WarningsJSON: string(warnings), Outcomes: outcomes, UpdatedAt: s.now().UTC()}
	write.BuildStatusSnapshot = func(state repository.CaptureProtocolState) (string, error) {
		status := projectCaptureStatus(state)
		encoded, err := json.Marshal(status)
		if err != nil {
			return "", err
		}
		if err := capturecontract.ValidateJSON(capturecontract.SchemaCaptureStatus, encoded); err != nil {
			return "", err
		}
		return string(encoded), nil
	}
	state, err := s.captures.CompleteCapture(ctx, write)
	if err != nil {
		return capturecontract.CaptureStatus{}, err
	}
	if state.CompletionStatusJSON != "" {
		var status capturecontract.CaptureStatus
		if err := json.Unmarshal([]byte(state.CompletionStatusJSON), &status); err != nil {
			return capturecontract.CaptureStatus{}, fmt.Errorf("decode capture completion snapshot: %w", err)
		}
		return status, nil
	}
	return projectCaptureStatus(state), nil
}

func (s *CaptureService) GetStatus(ctx context.Context, captureID string) (capturecontract.CaptureStatus, error) {
	if s == nil || s.captures == nil {
		return capturecontract.CaptureStatus{}, fmt.Errorf("get capture status: capture repository is required")
	}
	state, err := s.captures.GetProtocolState(ctx, captureID)
	if err != nil {
		return capturecontract.CaptureStatus{}, err
	}
	return projectCaptureStatus(state), nil
}

func projectCaptureStatus(state repository.CaptureProtocolState) capturecontract.CaptureStatus {
	stored := make(map[string]domain.CaptureVariantOutcome, len(state.Outcomes))
	for _, item := range state.Outcomes {
		stored[item.ClientVariantID] = item
	}
	assets := make([]capturecontract.AssetOutcome, 0, len(state.Bindings))
	for _, binding := range state.Bindings {
		item, ok := stored[binding.ClientVariantID]
		if !ok {
			item = domain.CaptureVariantOutcome{ClientVariantID: binding.ClientVariantID, Outcome: domain.AssetOutcomeDeferred}
			if !binding.Digest.Empty() {
				item.Outcome = domain.AssetOutcomeAlreadyAvailable
				item.Digest = binding.Digest
			} else if binding.Action == domain.AssetRejected {
				item.Outcome = domain.AssetOutcomeNotAvailable
				item.Reason = binding.Reason
			}
		}
		outcome := capturecontract.AssetOutcome{ClientVariantID: item.ClientVariantID, Outcome: string(item.Outcome),
			Digest: contractDigest(item.Digest), Reason: item.Reason}
		if item.Outcome == domain.AssetOutcomeUploaded {
			size := item.ByteSize
			outcome.ByteSize = &size
		}
		if item.Outcome == domain.AssetOutcomeFailed || item.Outcome == domain.AssetOutcomeNotAvailable {
			retryable := item.Retryable
			outcome.Retryable = &retryable
		}
		assets = append(assets, outcome)
	}
	warnings := make([]capturecontract.ContractWarning, 0)
	_ = json.Unmarshal([]byte(state.Attempt.WarningsJSON), &warnings)
	if warnings == nil {
		warnings = []capturecontract.ContractWarning{}
	}
	return capturecontract.CaptureStatus{SchemaVersion: capturecontract.CaptureStatusVersion,
		CaptureID: state.Attempt.CaptureID, FragmentID: state.Attempt.FragmentID,
		FragmentRevisionID: state.Attempt.FragmentRevisionID, CaptureAttemptID: state.Attempt.ID,
		Completion: string(state.Attempt.Completion), Assets: assets,
		EnrichmentInProgress: state.EnrichmentInProgress, Warnings: warnings}
}

func projectTags(tags []domain.AttributedTag) ([]string, []capturecontract.AttributedTag) {
	combined := make([]string, 0)
	attributed := make([]capturecontract.AttributedTag, 0, len(tags))
	seen := make(map[string]struct{})
	for _, item := range tags {
		if _, ok := seen[item.NormalizedValue]; !ok {
			seen[item.NormalizedValue] = struct{}{}
			combined = append(combined, item.Value)
		}
		attributed = append(attributed, capturecontract.AttributedTag{Value: item.Value, Source: string(item.Source), ObservationID: item.ObservationID})
	}
	return combined, attributed
}

func captureRenderer(envelope capturecontract.CaptureEnvelope, media []domain.MediaManifestItem) string {
	if len(media) > 1 {
		return "gallery"
	}
	if len(media) == 1 {
		switch media[0].Asset.Kind {
		case domain.MediaImage:
			return "image"
		case domain.MediaVideo:
			return "video"
		case domain.MediaAudio:
			return "audio"
		case domain.MediaDocument:
			return "document"
		}
	}
	if envelope.Document.Content != nil && envelope.Document.Content.Format == "markdown" {
		return "article"
	}
	if envelope.Document.Title != "" || envelope.Document.Description != "" || envelope.Document.Content != nil {
		return "text"
	}
	return "unknown"
}

func contractDigest(value domain.ContentDigest) *capturecontract.Digest {
	if value.Empty() {
		return nil
	}
	return &capturecontract.Digest{Algorithm: "sha256", Value: value.Value}
}

func normalizeCaptureText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func firstCaptureText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return normalizeCaptureText(value)
		}
	}
	return "Untitled capture"
}

func boundedRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
func floatValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
func positiveIntPointer(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}
func positiveFloatPointer(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}
func nonzeroInt64Pointer(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}
