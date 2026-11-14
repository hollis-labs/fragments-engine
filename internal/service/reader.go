package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/hollis-labs/fragments-engine/internal/provider"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

const readerPageSize = 50

type readerProjectionRepository interface {
	List(context.Context, repository.ReaderPageRequest) (repository.ReaderSnapshot, error)
	Get(context.Context, string, string, string) (repository.ReaderSnapshot, error)
}

type ReaderService struct {
	repository readerProjectionRepository
}

func NewReaderService(repository readerProjectionRepository) *ReaderService {
	return &ReaderService{repository: repository}
}

type ReaderListRequest struct {
	Scope       string
	Cursor      string
	PrincipalID string
	// Optional filters. Renderers are any-of; Tags are all-of. Since and
	// Until are RFC 3339 and bound capture time inclusively.
	Renderers []string
	Tags      []string
	State     string
	Provider  string
	Since     string
	Until     string
	Query     string
	// Later, Archived and Starred are "true" or "false" when filtering on a
	// flag. Without Archived, archived items are left out of inbox and library.
	Later    string
	Archived string
	Starred  string
}

// readerFilterScanChunk is how many candidate rows a filtered page reads per
// repository call while looking for renderer/tag matches, and
// readerFilterScanMax bounds the total scanned for one page.
const (
	readerFilterScanChunk = 100
	readerFilterScanMax   = 5000
)

var (
	readerListRenderers = map[string]bool{"article": true, "image": true, "gallery": true,
		"video": true, "audio": true, "document": true, "text": true}
	readerListStates = map[string]bool{"unread": true, "in_progress": true, "read": true}
)

type readerListFilter struct {
	renderers   map[string]bool
	tags        []string
	repoFilter  repository.ReaderListFilter
	fingerprint string
}

func (f readerListFilter) postFilters() bool { return len(f.renderers) > 0 || len(f.tags) > 0 }

func parseReaderListFilter(request ReaderListRequest) (readerListFilter, error) {
	var out readerListFilter
	var renderers []string
	for _, raw := range request.Renderers {
		for _, value := range strings.Split(raw, ",") {
			value = strings.ToLower(strings.TrimSpace(value))
			if !readerListRenderers[value] {
				return out, &ReaderRequestError{Field: "renderer", Reason: "must be one of article, image, gallery, video, audio, document, text"}
			}
			if out.renderers == nil {
				out.renderers = map[string]bool{}
			}
			if !out.renderers[value] {
				out.renderers[value] = true
				renderers = append(renderers, value)
			}
		}
	}
	sort.Strings(renderers)
	seenTag := map[string]bool{}
	for _, raw := range request.Tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" || len(tag) > 255 {
			return out, &ReaderRequestError{Field: "tag", Reason: "must be a non-empty value of at most 255 characters"}
		}
		if !seenTag[tag] {
			seenTag[tag] = true
			out.tags = append(out.tags, tag)
		}
	}
	sort.Strings(out.tags)
	if state := strings.ToLower(strings.TrimSpace(request.State)); request.State != "" {
		if !readerListStates[state] {
			return out, &ReaderRequestError{Field: "state", Reason: "must be unread, in_progress, or read"}
		}
		out.repoFilter.State = state
	}
	if request.Provider != "" {
		provider := strings.ToLower(strings.TrimSpace(request.Provider))
		if provider == "" || len(provider) > 255 {
			return out, &ReaderRequestError{Field: "provider", Reason: "must be a non-empty value of at most 255 characters"}
		}
		out.repoFilter.Provider = provider
	}
	parseBound := func(field, raw string) (*time.Time, error) {
		if raw == "" {
			return nil, nil
		}
		value, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
		if err != nil {
			return nil, &ReaderRequestError{Field: field, Reason: "must be an RFC 3339 timestamp"}
		}
		value = value.UTC()
		return &value, nil
	}
	var err error
	if out.repoFilter.Since, err = parseBound("since", request.Since); err != nil {
		return out, err
	}
	if out.repoFilter.Until, err = parseBound("until", request.Until); err != nil {
		return out, err
	}
	if out.repoFilter.Since != nil && out.repoFilter.Until != nil && out.repoFilter.Since.After(*out.repoFilter.Until) {
		return out, &ReaderRequestError{Field: "since", Reason: "must not be after until"}
	}
	if request.Query != "" {
		terms := strings.Fields(request.Query)
		if len(terms) == 0 || len(request.Query) > 512 {
			return out, &ReaderRequestError{Field: "q", Reason: "must contain search text of at most 512 characters"}
		}
		// Same quoting as the legacy keyword search: terms cannot inject FTS syntax.
		for i, term := range terms {
			terms[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
		}
		out.repoFilter.Match = strings.Join(terms, " ")
	}
	for _, flag := range []struct {
		field, raw string
		dst        **bool
	}{{"later", request.Later, &out.repoFilter.Later}, {"archived", request.Archived, &out.repoFilter.Archived},
		{"starred", request.Starred, &out.repoFilter.Starred}} {
		if flag.raw == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(flag.raw)) {
		case "true":
			value := true
			*flag.dst = &value
		case "false":
			value := false
			*flag.dst = &value
		default:
			return out, &ReaderRequestError{Field: flag.field, Reason: "must be true or false"}
		}
	}
	var parts []string
	add := func(key, value string) {
		if value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	add("renderer", strings.Join(renderers, ","))
	add("tag", strings.Join(out.tags, "\x1f"))
	add("state", out.repoFilter.State)
	add("provider", out.repoFilter.Provider)
	if out.repoFilter.Since != nil {
		add("since", out.repoFilter.Since.Format(time.RFC3339Nano))
	}
	if out.repoFilter.Until != nil {
		add("until", out.repoFilter.Until.Format(time.RFC3339Nano))
	}
	add("q", out.repoFilter.Match)
	for _, flag := range []struct {
		key  string
		want *bool
	}{{"later", out.repoFilter.Later}, {"archived", out.repoFilter.Archived}, {"starred", out.repoFilter.Starred}} {
		if flag.want != nil {
			add(flag.key, fmt.Sprint(*flag.want))
		}
	}
	if len(parts) > 0 {
		sum := sha256.Sum256([]byte(strings.Join(parts, "\x1e")))
		out.fingerprint = hex.EncodeToString(sum[:8])
	}
	return out, nil
}

func (f readerListFilter) matches(item capturecontract.ReaderItem) bool {
	if len(f.renderers) > 0 && !f.renderers[item.Renderer] {
		return false
	}
	if len(f.tags) > 0 {
		have := make(map[string]bool, len(item.Tags.Combined))
		for _, tag := range item.Tags.Combined {
			have[strings.ToLower(tag)] = true
		}
		for _, tag := range f.tags {
			if !have[tag] {
				return false
			}
		}
	}
	return true
}

type ReaderGetRequest struct {
	FragmentID  string
	RevisionID  string
	PrincipalID string
}

type ReaderRequestError struct {
	Field  string
	Reason string
}

func (e *ReaderRequestError) Error() string {
	return fmt.Sprintf("invalid reader %s: %s", e.Field, e.Reason)
}

func IsInvalidReaderRequest(err error) bool {
	var target *ReaderRequestError
	return errors.As(err, &target)
}

func (s *ReaderService) List(ctx context.Context, request ReaderListRequest) (capturecontract.ReaderItemList, error) {
	if s == nil || s.repository == nil {
		return capturecontract.ReaderItemList{}, fmt.Errorf("reader service is unavailable")
	}
	request.Scope = strings.ToLower(strings.TrimSpace(request.Scope))
	if request.Scope != "inbox" && request.Scope != "library" && request.Scope != "all" {
		return capturecontract.ReaderItemList{}, &ReaderRequestError{Field: "scope", Reason: "must be inbox, library, or all"}
	}
	principalID, err := readerPrincipal(request.PrincipalID)
	if err != nil {
		return capturecontract.ReaderItemList{}, err
	}
	filter, err := parseReaderListFilter(request)
	if err != nil {
		return capturecontract.ReaderItemList{}, err
	}
	cursor, err := decodeReaderCursorFiltered(request.Scope, filter.fingerprint, request.Cursor)
	if err != nil {
		return capturecontract.ReaderItemList{}, err
	}
	var items []capturecontract.ReaderItem
	var nextAt time.Time
	var nextID string
	if !filter.postFilters() {
		snapshot, err := s.repository.List(ctx, repository.ReaderPageRequest{
			Scope: request.Scope, Cursor: cursor, Limit: readerPageSize + 1, PrincipalID: principalID,
			Filter: filter.repoFilter,
		})
		if err != nil {
			return capturecontract.ReaderItemList{}, fmt.Errorf("list reader items: %w", err)
		}
		hasNext := len(snapshot.Bases) > readerPageSize
		if hasNext {
			snapshot.Bases = snapshot.Bases[:readerPageSize]
		}
		items = make([]capturecontract.ReaderItem, 0, len(snapshot.Bases))
		for _, base := range snapshot.Bases {
			item, err := projectReaderItem(base, snapshot, principalID)
			if err != nil {
				return capturecontract.ReaderItemList{}, err
			}
			items = append(items, item)
		}
		if hasNext {
			last := snapshot.Bases[len(snapshot.Bases)-1]
			nextAt, nextID = last.SortAt, last.FragmentID
		}
	} else {
		items, nextAt, nextID, err = s.listPostFiltered(ctx, request.Scope, principalID, cursor, filter)
		if err != nil {
			return capturecontract.ReaderItemList{}, err
		}
	}
	out := capturecontract.ReaderItemList{SchemaVersion: capturecontract.ReaderListVersion,
		Scope: request.Scope, Items: items}
	if nextID != "" {
		out.NextCursor, err = encodeReaderCursorFiltered(request.Scope, filter.fingerprint, nextAt, nextID)
		if err != nil {
			return capturecontract.ReaderItemList{}, err
		}
	}
	if err := validateReaderContract(capturecontract.SchemaReaderList, out); err != nil {
		return capturecontract.ReaderItemList{}, err
	}
	return out, nil
}

// listPostFiltered pages through SQL-filtered candidates in chunks, keeping
// the items whose projected renderer and tags match. When the scan budget runs
// out before a full page is found it returns what it has with a cursor at the
// last row scanned, so the client continues the same filtered set.
func (s *ReaderService) listPostFiltered(ctx context.Context, scope, principalID string, cursor *repository.ReaderPageCursor, filter readerListFilter) ([]capturecontract.ReaderItem, time.Time, string, error) {
	items := make([]capturecontract.ReaderItem, 0, readerPageSize)
	var lastKept, lastScanned repository.ReaderBase
	scanned := 0
	for {
		snapshot, err := s.repository.List(ctx, repository.ReaderPageRequest{
			Scope: scope, Cursor: cursor, Limit: readerFilterScanChunk, PrincipalID: principalID,
			Filter: filter.repoFilter,
		})
		if err != nil {
			return nil, time.Time{}, "", fmt.Errorf("list reader items: %w", err)
		}
		for _, base := range snapshot.Bases {
			item, err := projectReaderItem(base, snapshot, principalID)
			if err != nil {
				return nil, time.Time{}, "", err
			}
			scanned++
			lastScanned = base
			if !filter.matches(item) {
				continue
			}
			if len(items) == readerPageSize {
				return items, lastKept.SortAt, lastKept.FragmentID, nil
			}
			items = append(items, item)
			lastKept = base
		}
		if len(snapshot.Bases) < readerFilterScanChunk {
			return items, time.Time{}, "", nil
		}
		last := snapshot.Bases[len(snapshot.Bases)-1]
		cursor = &repository.ReaderPageCursor{SortAt: last.SortAt, FragmentID: last.FragmentID}
		if scanned >= readerFilterScanMax {
			return items, lastScanned.SortAt, lastScanned.FragmentID, nil
		}
	}
}

func (s *ReaderService) Get(ctx context.Context, request ReaderGetRequest) (capturecontract.ReaderItem, error) {
	if s == nil || s.repository == nil {
		return capturecontract.ReaderItem{}, fmt.Errorf("reader service is unavailable")
	}
	request.FragmentID, request.RevisionID = strings.TrimSpace(request.FragmentID), strings.TrimSpace(request.RevisionID)
	if request.FragmentID == "" || len(request.FragmentID) > 255 {
		return capturecontract.ReaderItem{}, &ReaderRequestError{Field: "fragment_id", Reason: "must be a non-empty identifier of at most 255 characters"}
	}
	if len(request.RevisionID) > 255 {
		return capturecontract.ReaderItem{}, &ReaderRequestError{Field: "revision_id", Reason: "must be at most 255 characters"}
	}
	principalID, err := readerPrincipal(request.PrincipalID)
	if err != nil {
		return capturecontract.ReaderItem{}, err
	}
	snapshot, err := s.repository.Get(ctx, request.FragmentID, request.RevisionID, principalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return capturecontract.ReaderItem{}, sql.ErrNoRows
		}
		return capturecontract.ReaderItem{}, fmt.Errorf("get reader item: %w", err)
	}
	if len(snapshot.Bases) != 1 {
		return capturecontract.ReaderItem{}, fmt.Errorf("get reader item: expected exactly one base projection")
	}
	item, err := projectReaderItem(snapshot.Bases[0], snapshot, principalID)
	if err != nil {
		return capturecontract.ReaderItem{}, err
	}
	if err := validateReaderContract(capturecontract.SchemaReaderItem, item); err != nil {
		return capturecontract.ReaderItem{}, err
	}
	return item, nil
}

// ProjectReaderItem is the command service's transport-neutral projection
// seam. Commands always return the same authoritative item shape as Reader
// detail, scoped to the principal that issued the mutation.
func (s *ReaderService) ProjectReaderItem(ctx context.Context, principalID, fragmentID string) (capturecontract.ReaderItem, error) {
	return s.Get(ctx, ReaderGetRequest{FragmentID: fragmentID, PrincipalID: principalID})
}

func readerPrincipal(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "local-user"
	}
	if len(value) > 255 {
		return "", &ReaderRequestError{Field: "principal_id", Reason: "must be at most 255 characters"}
	}
	return value, nil
}

type readerCursorEnvelope struct {
	Version    int    `json:"v"`
	Scope      string `json:"scope"`
	SortAt     string `json:"sort_at"`
	FragmentID string `json:"fragment_id"`
	// Filters fingerprints the list filters the cursor was issued for. It is
	// omitted for unfiltered lists so their cursors are unchanged.
	Filters string `json:"filters,omitempty"`
}

func encodeReaderCursor(scope string, sortAt time.Time, fragmentID string) (string, error) {
	return encodeReaderCursorFiltered(scope, "", sortAt, fragmentID)
}

func encodeReaderCursorFiltered(scope, filters string, sortAt time.Time, fragmentID string) (string, error) {
	if sortAt.IsZero() || strings.TrimSpace(fragmentID) == "" {
		return "", fmt.Errorf("encode reader cursor: sort timestamp and fragment ID are required")
	}
	raw, err := json.Marshal(readerCursorEnvelope{Version: 1, Scope: scope,
		SortAt: sortAt.UTC().Format(time.RFC3339Nano), FragmentID: fragmentID, Filters: filters})
	if err != nil {
		return "", fmt.Errorf("encode reader cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeReaderCursor(scope, encoded string) (*repository.ReaderPageCursor, error) {
	return decodeReaderCursorFiltered(scope, "", encoded)
}

func decodeReaderCursorFiltered(scope, filters, encoded string) (*repository.ReaderPageCursor, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 2048 {
		return nil, &ReaderRequestError{Field: "cursor", Reason: "is too long"}
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, &ReaderRequestError{Field: "cursor", Reason: "is not a valid opaque cursor"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor readerCursorEnvelope
	if err := decoder.Decode(&cursor); err != nil {
		return nil, &ReaderRequestError{Field: "cursor", Reason: "is not a valid opaque cursor"}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &ReaderRequestError{Field: "cursor", Reason: "contains trailing data"}
	}
	sortAt, err := time.Parse(time.RFC3339Nano, cursor.SortAt)
	if err != nil || cursor.Version != 1 || cursor.Scope != scope || strings.TrimSpace(cursor.FragmentID) == "" || len(cursor.FragmentID) > 255 {
		return nil, &ReaderRequestError{Field: "cursor", Reason: "does not belong to this Reader scope"}
	}
	if cursor.Filters != filters {
		return nil, &ReaderRequestError{Field: "cursor", Reason: "was issued for different filters; restart the list without it when changing filters"}
	}
	return &repository.ReaderPageCursor{SortAt: sortAt.UTC(), FragmentID: cursor.FragmentID}, nil
}

func projectReaderItem(base repository.ReaderBase, snapshot repository.ReaderSnapshot, principalID string) (capturecontract.ReaderItem, error) {
	revision := base.FragmentRevision
	coverageRows := snapshot.Coverage[revision.ID]
	selected := make(map[domain.EnrichmentCapability]capturecontract.ResolvedText)
	for _, row := range coverageRows {
		if text, ok := selectedReaderText(row); ok {
			selected[row.Coverage.Capability] = text
		}
	}
	title, ok := selected[domain.CapabilityTitle]
	if !ok {
		title = capturecontract.ResolvedText{Value: revision.Title, Source: "source"}
	}
	var description *capturecontract.ResolvedText
	if value, ok := selected[domain.CapabilityDescription]; ok {
		description = &value
	} else if revision.Description != "" {
		value := capturecontract.ResolvedText{Value: revision.Description, Source: "source"}
		description = &value
	}
	summary, ok := selected[domain.CapabilitySummary]
	if !ok && description != nil {
		summary = *description
		ok = true
	}
	if !ok && revision.Content != "" {
		summary = capturecontract.ResolvedText{Value: boundedReaderRunes(revision.Content, 4000), Source: "deterministic"}
		ok = true
	}
	if !ok {
		summary = capturecontract.ResolvedText{Value: title.Value, Source: "deterministic"}
	}
	display := capturecontract.ReaderDisplay{Title: title, Description: description, Summary: summary}
	var metadata struct {
		Byline      string     `json:"byline"`
		PublishedAt *time.Time `json:"published_at"`
	}
	if json.Unmarshal([]byte(revision.MetadataJSON), &metadata) == nil {
		if metadata.Byline != "" {
			display.Byline = &capturecontract.ResolvedText{Value: metadata.Byline, Source: "source"}
		}
		if metadata.PublishedAt != nil && !metadata.PublishedAt.IsZero() {
			published := metadata.PublishedAt.UTC()
			display.PublishedAt = &published
		}
	}
	article := capturecontract.ReaderArticle{PreviewMarkdown: boundedReaderRunes(revision.Content, 4000),
		FullContentAvailable: revision.Content != ""}
	if article.FullContentAvailable {
		article.FullContentHref = "/v1/reader/items/" + url.PathEscape(base.FragmentID) +
			"/content?revision_id=" + url.QueryEscape(revision.ID)
	}
	media, acquisition := projectReaderMedia(base.FragmentID, revision.ID, snapshot.Media[revision.ID])
	renderer := readerRenderer(revision, media)
	combined, attributed := projectReaderTags(base.FragmentID, revision.ID, snapshot.Tags[base.FragmentID])
	annotations := projectReaderAnnotations(snapshot.Annotations[base.FragmentID])
	enrichment := make([]capturecontract.CapabilityCoverage, 0, len(coverageRows))
	for _, row := range coverageRows {
		enrichment = append(enrichment, capturecontract.CapabilityCoverage{Capability: string(row.Coverage.Capability),
			State: string(row.Coverage.State), ObservationID: row.Coverage.SelectedObservationID,
			Detail: row.Coverage.Detail})
	}
	routing, materialization := projectReaderEffects(snapshot.Effects[base.FragmentID])
	source := projectReaderSource(base.Source)
	readingState := capturecontract.ReadingState{PrincipalID: principalID, FragmentID: base.FragmentID,
		State: "unread", Position: capturecontract.ReadingPosition{Kind: "none"}, Revision: 0}
	if base.ReadingState != nil {
		readingState = projectReadingState(*base.ReadingState, principalID, base.FragmentID)
	}
	item := capturecontract.ReaderItem{
		SchemaVersion: capturecontract.ReaderItemVersion, FragmentID: base.FragmentID,
		FragmentRevisionID: revision.ID, Revision: base.AggregateRevision, Source: source,
		Renderer: renderer, Display: display, Article: article, Media: media,
		Tags:        capturecontract.ReaderTags{Combined: combined, Attributed: attributed},
		Annotations: annotations, CaptureCount: base.CaptureCount,
		ReadingState: readingState,
		Operations: capturecontract.OperationalSummaries{
			Triage:  capturecontract.TriageSummary{CaseIDs: []string{}, UnresolvedCount: boolInt(base.InInbox)},
			Routing: routing, Materialization: materialization, Enrichment: enrichment, Acquisition: acquisition,
		},
		Actions: readerCommandCapabilities(readingState.State, base.HasCaptureAttempt, media),
	}
	if base.CuratedNote != nil {
		item.CuratedNote = &capturecontract.CuratedNote{BodyMarkdown: base.CuratedNote.BodyMarkdown,
			Revision: base.CuratedNote.Revision, UpdatedAt: base.CuratedNote.UpdatedAt}
	}
	item.Playback = readerYouTubePlayback(renderer, base.Source)
	if err := validateReaderContract(capturecontract.SchemaReaderItem, item); err != nil {
		return capturecontract.ReaderItem{}, fmt.Errorf("project reader item %q: %w", base.FragmentID, err)
	}
	return item, nil
}

func projectReadingState(state domain.ReadingState, principalID, fragmentID string) capturecontract.ReadingState {
	return capturecontract.ReadingState{
		PrincipalID: principalID,
		FragmentID:  fragmentID,
		State:       string(state.State),
		Position: capturecontract.ReadingPosition{
			Kind:            string(state.Position.Kind),
			Progress:        state.Position.Progress,
			BlockAnchor:     state.Position.BlockAnchor,
			LocalOffset:     state.Position.LocalOffset,
			ElapsedSeconds:  state.Position.ElapsedSeconds,
			DurationSeconds: state.Position.DurationSeconds,
			ProviderMediaID: state.Position.ProviderMediaID,
			AttachmentID:    state.Position.AttachmentID,
			Index:           state.Position.Index,
			Page:            state.Position.Page,
		},
		LastOpenedAt: state.LastOpened,
		CompletedAt:  state.CompletedAt,
		Later:        state.LaterAt != nil, LaterAt: state.LaterAt,
		Archived: state.ArchivedAt != nil, ArchivedAt: state.ArchivedAt,
		Starred: state.StarredAt != nil, StarredAt: state.StarredAt,
		Revision: state.Revision,
	}
}

func readerCommandCapabilities(readingStatus string, hasCapture bool, media []capturecontract.ReaderMediaItem) []capturecontract.CommandCapability {
	schemaID, _ := capturecontract.SchemaID(capturecontract.SchemaReaderCommand)
	definitions := []struct {
		command    string
		definition string
		enabled    bool
	}{
		{"add_tag", "AddTag", true},
		{"remove_tag", "RemoveTag", true},
		{"append_capture_note", "AppendCaptureNote", hasCapture},
		{"update_curated_note", "UpdateCuratedNote", true},
		{"set_reading_progress", "SetReadingProgress", true},
		{"mark_read", "MarkRead", readingStatus != "read"},
		{"mark_unread", "MarkUnread", readingStatus != "unread"},
		{"request_asset_acquisition", "RequestAssetAcquisition", len(media) > 0},
		{"route", "Route", true},
		{"materialize", "Materialize", true},
		{"set_later", "SetLater", true},
		{"set_archived", "SetArchived", true},
		{"set_starred", "SetStarred", true},
	}
	out := make([]capturecontract.CommandCapability, 0, len(definitions))
	for _, definition := range definitions {
		if !definition.enabled {
			continue
		}
		out = append(out, capturecontract.CommandCapability{
			Command: definition.command, InputSchema: schemaID + "#/$defs/" + definition.definition,
			ExpectedRevisionRequired: true,
		})
	}
	return out
}

func selectedReaderText(row repository.ReaderCoverage) (capturecontract.ResolvedText, bool) {
	if row.Coverage.SelectedObservationID == "" || row.SelectedValueJSON == "" {
		return capturecontract.ResolvedText{}, false
	}
	switch row.Coverage.Capability {
	case domain.CapabilityTitle, domain.CapabilityDescription, domain.CapabilitySummary:
	default:
		return capturecontract.ResolvedText{}, false
	}
	if !row.SelectedAttribution.ValidDescriptionSource() ||
		provider.ValidateCapabilityValue(row.Coverage.Capability, []byte(row.SelectedValueJSON)) != nil {
		return capturecontract.ResolvedText{}, false
	}
	var value provider.TextValue
	if json.Unmarshal([]byte(row.SelectedValueJSON), &value) != nil {
		return capturecontract.ResolvedText{}, false
	}
	return capturecontract.ResolvedText{Value: value.Text, Source: string(row.SelectedAttribution),
		ObservationID: row.Coverage.SelectedObservationID}, true
}

func projectReaderSource(source domain.SourceIdentity) capturecontract.SourceIdentity {
	out := capturecontract.SourceIdentity{SourceRegistrationID: source.SourceRegistrationID,
		SubmittedURL: source.SubmittedURL, CanonicalURL: source.CanonicalURL,
		Provider: source.Provider, ProviderItemID: source.ProviderItemID,
		SourceItemKey: source.SourceItemKey, SourceLocator: source.SourceLocator,
		SegmentKey: source.SegmentKey}
	if source.Canonicalizer.Adapter != "" && source.Canonicalizer.Version != "" {
		out.Canonicalizer = &capturecontract.Canonicalizer{Adapter: source.Canonicalizer.Adapter,
			Version: source.Canonicalizer.Version}
	}
	return out
}

func projectReaderMedia(fragmentID, revisionID string, items []domain.MediaManifestItem) ([]capturecontract.ReaderMediaItem, capturecontract.AcquisitionSummary) {
	out := make([]capturecontract.ReaderMediaItem, 0, len(items))
	summary := capturecontract.AcquisitionSummary{}
	for _, item := range items {
		variants := make([]capturecontract.ReaderAssetVariant, 0, len(item.Variants))
		for _, variant := range item.Variants {
			projected := capturecontract.ReaderAssetVariant{AssetVariantID: variant.ID,
				Kind: string(variant.Kind), Custody: string(variant.Custody),
				AcquisitionState: string(variant.AcquisitionState), MIMEType: variant.MIMEType,
				Width: positiveIntPointer(variant.Width), Height: positiveIntPointer(variant.Height),
				DurationSeconds: positiveFloatPointer(variant.DurationSeconds),
				ByteSize:        readerByteSize(variant.ByteSize), Digest: contractDigest(variant.Digest)}
			if isSafeReaderURL(variant.SourceURL) {
				projected.SourceURL = variant.SourceURL
			}
			if variant.BlobDigest != "" || (variant.AcquisitionState == domain.AcquisitionAvailable && projected.SourceURL == "") {
				projected.ContentHref = readerMediaContentHref(fragmentID, revisionID, variant.ID)
			}
			if variant.Failure != nil {
				projected.Failure = &capturecontract.AssetFailure{Code: nonemptyReaderValue(variant.Failure.Code, "acquisition_failed"),
					Message: nonemptyReaderValue(variant.Failure.Message, "asset acquisition failed"), Retryable: variant.Failure.Retryable}
			} else if variant.AcquisitionState == domain.AcquisitionFailed {
				projected.Failure = &capturecontract.AssetFailure{Code: "acquisition_failed", Message: "asset acquisition failed", Retryable: false}
			}
			switch variant.AcquisitionState {
			case domain.AcquisitionPending:
				summary.Pending++
			case domain.AcquisitionAvailable:
				summary.Available++
			case domain.AcquisitionReferenceOnly:
				summary.ReferenceOnly++
			case domain.AcquisitionFailed:
				summary.Failed++
			}
			variants = append(variants, projected)
		}
		out = append(out, capturecontract.ReaderMediaItem{
			Attachment: capturecontract.AttachmentRef{AttachmentID: item.Attachment.ID,
				FragmentRevisionID: item.Attachment.FragmentRevisionID, MediaAssetID: item.Asset.ID,
				Role: string(item.Attachment.Role), Position: item.Attachment.Position,
				Caption: item.Attachment.Caption, SourceContext: item.Attachment.SourceContext},
			MediaAssetID: item.Asset.ID, ProviderMediaID: item.Asset.ProviderMediaID,
			Kind: string(item.Asset.Kind), AltText: item.Asset.AltText, Variants: variants,
		})
	}
	return out, summary
}

func readerMediaContentHref(fragmentID, revisionID, variantID string) string {
	query := url.Values{
		"fragment_id": {fragmentID},
		"revision_id": {revisionID},
	}
	return "/v1/media/variants/" + url.PathEscape(variantID) + "/content?" + query.Encode()
}

func readerRenderer(revision domain.FragmentRevision, media []capturecontract.ReaderMediaItem) string {
	renderableVisuals := 0
	for _, item := range media {
		if (item.Kind == "image" || item.Kind == "video") && readerRenderableRole(item.Attachment.Role) {
			renderableVisuals++
		}
	}
	if renderableVisuals > 1 {
		return "gallery"
	}
	for _, item := range media {
		if item.Kind == "video" && readerRenderableRole(item.Attachment.Role) {
			return "video"
		}
	}
	images := 0
	for _, item := range media {
		if item.Kind == "image" && readerRenderableRole(item.Attachment.Role) {
			images++
		}
	}
	if images == 1 {
		return "image"
	}
	for _, kind := range []string{"audio", "document"} {
		for _, item := range media {
			if item.Kind == kind && readerRenderableRole(item.Attachment.Role) {
				return kind
			}
		}
	}
	if strings.EqualFold(revision.ContentFormat, "markdown") && revision.Content != "" {
		return "article"
	}
	if revision.Title != "" || revision.Description != "" || revision.Content != "" {
		return "text"
	}
	return "unknown"
}

func readerRenderableRole(role string) bool {
	switch role {
	case "primary", "gallery_item", "hero", "inline":
		return true
	default:
		return false
	}
}

func projectReaderTags(fragmentID, revisionID string, facts []repository.ReaderTagFact) ([]string, []capturecontract.AttributedTag) {
	// One attributed entry per tag and source. Several observations of the same
	// tag from the same source (for example the capture's own tag row and the
	// enrichment record of the same tags) collapse into it: observation_id keeps
	// the tag-observation row's id when there is one, else the first, and
	// observation_ids lists them all once there are several.
	type attributedEntry struct {
		tag       capturecontract.AttributedTag
		preferred bool
		ids       []string
	}
	entries := make([]*attributedEntry, 0)
	entryIndex := map[string]*attributedEntry{}
	combinedOrder := make([]string, 0)
	combinedValues := map[string]string{}
	appendTag := func(value string, source domain.AttributionSource, observationID string, preferred bool) {
		value = strings.TrimSpace(value)
		if value == "" || !source.ValidTagSource() {
			return
		}
		normalized := strings.ToLower(value)
		if _, seen := combinedValues[normalized]; !seen {
			combinedOrder = append(combinedOrder, normalized)
		}
		combinedValues[normalized] = value
		key := normalized + "\x00" + string(source)
		entry, ok := entryIndex[key]
		if !ok {
			entry = &attributedEntry{tag: capturecontract.AttributedTag{Value: value, Source: string(source),
				ObservationID: observationID}, preferred: preferred}
			entryIndex[key] = entry
			entries = append(entries, entry)
		} else if preferred && !entry.preferred {
			entry.tag.ObservationID, entry.preferred = observationID, true
		} else if entry.tag.ObservationID == "" {
			entry.tag.ObservationID = observationID
		}
		if observationID == "" {
			return
		}
		for _, id := range entry.ids {
			if id == observationID {
				return
			}
		}
		entry.ids = append(entry.ids, observationID)
	}
	for _, fact := range facts {
		if fact.FragmentID != fragmentID || (fact.FragmentRevisionID != "" && fact.FragmentRevisionID != revisionID) {
			continue
		}
		if fact.OverlayAction != "" {
			normalized := strings.ToLower(strings.TrimSpace(fact.NormalizedValue))
			switch fact.OverlayAction {
			case "suppress":
				delete(combinedValues, normalized)
			case "add":
				appendTag(fact.Value, domain.AttributionUser, fact.ObservationID, true)
			}
			continue
		}
		if fact.ValueJSON == "" {
			appendTag(fact.Value, fact.Source, fact.ObservationID, true)
			continue
		}
		if provider.ValidateCapabilityValue(domain.CapabilityTags, []byte(fact.ValueJSON)) != nil {
			continue
		}
		var values provider.StringListValue
		if json.Unmarshal([]byte(fact.ValueJSON), &values) != nil {
			continue
		}
		for _, value := range values.Values {
			appendTag(value, fact.Source, fact.ObservationID, false)
		}
	}
	attributed := make([]capturecontract.AttributedTag, 0, len(entries))
	for _, entry := range entries {
		if len(entry.ids) > 1 {
			entry.tag.ObservationIDs = entry.ids
		}
		attributed = append(attributed, entry.tag)
	}
	combined := make([]string, 0, len(combinedValues))
	emitted := map[string]struct{}{}
	for _, normalized := range combinedOrder {
		value, ok := combinedValues[normalized]
		if !ok {
			continue
		}
		if _, seen := emitted[normalized]; seen {
			continue
		}
		combined = append(combined, value)
		emitted[normalized] = struct{}{}
	}
	return combined, attributed
}

func projectReaderAnnotations(items []domain.CaptureAnnotation) []capturecontract.ReaderAnnotation {
	out := make([]capturecontract.ReaderAnnotation, 0, len(items))
	for _, item := range items {
		projected := capturecontract.ReaderAnnotation{AnnotationID: item.ID, CaptureID: item.CaptureID,
			Kind: string(item.Kind), Text: item.Text, CapturedAt: item.CapturedAt}
		if item.Selector != nil {
			projected.Selector = &capturecontract.TextQuoteSelector{Exact: item.Selector.Exact,
				Prefix: item.Selector.Prefix, Suffix: item.Selector.Suffix}
		}
		if item.Position != nil {
			projected.Position = &capturecontract.DocumentPosition{BlockAnchor: item.Position.BlockAnchor,
				StartOffset: item.Position.StartOffset, EndOffset: item.Position.EndOffset}
		}
		out = append(out, projected)
	}
	return out
}

func readerYouTubePlayback(renderer string, source domain.SourceIdentity) *capturecontract.PlaybackSpec {
	if renderer != "video" {
		return nil
	}
	spec, err := TrustedYouTubePlaybackSpec(source.Provider, source.ProviderItemID, 0)
	if err != nil {
		return nil
	}
	return &spec
}

type effectState string

const (
	effectPending   effectState = "pending"
	effectSucceeded effectState = "succeeded"
	effectPartial   effectState = "partial"
	effectFailed    effectState = "failed"
)

func projectReaderEffects(items []repository.ReaderEffect) (capturecontract.EffectSummary, capturecontract.EffectSummary) {
	routing := map[string]effectState{}
	materialization := map[string]effectState{}
	for index, item := range items {
		ref := item.RouteID
		if ref == "" {
			ref = item.DestinationID
		}
		if ref == "" {
			ref = fmt.Sprintf("unreferenced-%d", index)
		}
		if item.CommandKind != "" {
			state := effectPending
			switch item.CommandState {
			case domain.ReaderCommandSucceeded:
				state = effectSucceeded
			case domain.ReaderCommandFailed:
				state = effectFailed
			case domain.ReaderCommandUncertain:
				state = effectPartial
			}
			if item.CommandKind == "materialize" {
				materialization[ref] = state
			} else if item.CommandKind == "route" {
				routing[ref] = state
			}
			continue
		}
		prefix := item.Reason
		if cut := strings.IndexByte(prefix, ':'); cut >= 0 {
			prefix = prefix[:cut]
		}
		if item.Decision == "materialize" || strings.HasPrefix(prefix, "materialize_") {
			switch prefix {
			case "materialize_destination", "materialize_route":
				materialization[ref] = effectSucceeded
			case "materialize_unsupported_callback", "materialize_destination_error", "materialize_error":
				materialization[ref] = effectFailed
			}
			continue
		}
		switch prefix {
		case "delivery_queued", "delivery_queued_by_design":
			routing[ref] = effectPending
		case "matched_auto_route", "manual_route_entity", "queued_delivery_success":
			routing[ref] = effectSucceeded
		case "destination_error", "manual_route_error", "queued_delivery_dead_letter":
			routing[ref] = effectFailed
		default:
			if item.Decision == "auto_route" || item.Decision == "manual_route" || item.Decision == "queued_route" {
				routing[ref] = effectSucceeded
			}
		}
	}
	return summarizeReaderEffects(routing), summarizeReaderEffects(materialization)
}

func summarizeReaderEffects(states map[string]effectState) capturecontract.EffectSummary {
	if len(states) == 0 {
		return capturecontract.EffectSummary{State: "none", References: []string{}}
	}
	references := make([]string, 0, len(states))
	counts := map[effectState]int{}
	for ref, state := range states {
		if !strings.HasPrefix(ref, "unreferenced-") {
			references = append(references, ref)
		}
		counts[state]++
	}
	sort.Strings(references)
	state := "partial"
	switch {
	case counts[effectPending] == len(states):
		state = "pending"
	case counts[effectSucceeded] == len(states):
		state = "succeeded"
	case counts[effectFailed] == len(states):
		state = "failed"
	}
	return capturecontract.EffectSummary{State: state, References: references}
}

func validateReaderContract(schema capturecontract.SchemaName, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode reader contract: %w", err)
	}
	if err := capturecontract.ValidateJSON(schema, raw); err != nil {
		return fmt.Errorf("validate reader contract: %w", err)
	}
	return nil
}

func boundedReaderRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func readerByteSize(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func isSafeReaderURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil
}

func nonemptyReaderValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
