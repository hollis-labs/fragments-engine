package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

const (
	maxReaderCollectionName        = 200
	maxReaderCollectionDescription = 2000
)

var readerCollectionKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

type readerCollectionProjection interface {
	ListCollection(context.Context, string, string, int, int) (repository.ReaderSnapshot, error)
}

// ReaderCollectionService owns principal-scoped collections: named, ordered
// sets of fragments. Every method takes the principal and never reaches
// another principal's collection; see docs/reader-collections.md.
type ReaderCollectionService struct {
	repository *repository.ReaderCollectionRepository
	projection readerCollectionProjection
	now        func() time.Time
}

func NewReaderCollectionService(repo *repository.ReaderCollectionRepository, projection readerCollectionProjection) *ReaderCollectionService {
	return &ReaderCollectionService{repository: repo, projection: projection, now: time.Now}
}

func (s *ReaderCollectionService) Create(ctx context.Context, principalID string, request capturecontract.ReaderCollectionCreate) (capturecontract.ReaderCollection, error) {
	principalID, err := s.principal(principalID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	name, err := readerCollectionName(request.Name)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	description, err := readerCollectionDescription(request.Description)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	kind := domain.ReaderCollectionDefaultKind
	if strings.TrimSpace(request.Kind) != "" {
		if kind, err = readerCollectionKind(request.Kind); err != nil {
			return capturecontract.ReaderCollection{}, err
		}
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return capturecontract.ReaderCollection{}, fmt.Errorf("create reader collection ID: %w", err)
	}
	created, err := s.repository.Create(ctx, domain.ReaderCollection{ID: "rc-" + hex.EncodeToString(raw[:]),
		PrincipalID: principalID, Name: name, Description: description, Kind: kind, CreatedAt: s.now().UTC()})
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	return projectReaderCollection(created), nil
}

func (s *ReaderCollectionService) List(ctx context.Context, principalID string) (capturecontract.ReaderCollectionList, error) {
	principalID, err := s.principal(principalID)
	if err != nil {
		return capturecontract.ReaderCollectionList{}, err
	}
	collections, err := s.repository.List(ctx, principalID)
	if err != nil {
		return capturecontract.ReaderCollectionList{}, err
	}
	out := capturecontract.ReaderCollectionList{SchemaVersion: capturecontract.ReaderCollectionListVersion,
		Collections: make([]capturecontract.ReaderCollection, 0, len(collections))}
	for _, collection := range collections {
		out.Collections = append(out.Collections, projectReaderCollection(collection))
	}
	return out, nil
}

func (s *ReaderCollectionService) Get(ctx context.Context, principalID, collectionID string) (capturecontract.ReaderCollection, error) {
	principalID, collectionID, err := s.target(principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	collection, err := s.repository.Get(ctx, principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	return projectReaderCollection(collection), nil
}

func (s *ReaderCollectionService) Update(ctx context.Context, principalID, collectionID string, request capturecontract.ReaderCollectionUpdate) (capturecontract.ReaderCollection, error) {
	principalID, collectionID, err := s.target(principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	if err := readerCollectionExpected(request.ExpectedRevision); err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	var patch repository.ReaderCollectionPatch
	if request.Name != nil {
		name, err := readerCollectionName(*request.Name)
		if err != nil {
			return capturecontract.ReaderCollection{}, err
		}
		patch.Name = &name
	}
	if request.Description != nil {
		description, err := readerCollectionDescription(*request.Description)
		if err != nil {
			return capturecontract.ReaderCollection{}, err
		}
		patch.Description = &description
	}
	if request.Kind != nil {
		kind, err := readerCollectionKind(*request.Kind)
		if err != nil {
			return capturecontract.ReaderCollection{}, err
		}
		patch.Kind = &kind
	}
	if patch.Name == nil && patch.Description == nil && patch.Kind == nil {
		return capturecontract.ReaderCollection{}, &ReaderRequestError{Field: "collection", Reason: "name, description or kind is required"}
	}
	updated, err := s.repository.Update(ctx, principalID, collectionID, request.ExpectedRevision, patch, s.now().UTC())
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	return projectReaderCollection(updated), nil
}

// Delete removes the collection and its membership. Fragments are untouched.
func (s *ReaderCollectionService) Delete(ctx context.Context, principalID, collectionID string, expectedRevision int) error {
	principalID, collectionID, err := s.target(principalID, collectionID)
	if err != nil {
		return err
	}
	if err := readerCollectionExpected(expectedRevision); err != nil {
		return err
	}
	return s.repository.Delete(ctx, principalID, collectionID, expectedRevision)
}

func (s *ReaderCollectionService) AddItem(ctx context.Context, principalID, collectionID string, request capturecontract.ReaderCollectionAddItem) (capturecontract.ReaderCollection, error) {
	principalID, collectionID, err := s.target(principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	if err := readerCollectionExpected(request.ExpectedRevision); err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	fragmentID, err := readerCollectionFragment(request.FragmentID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	if request.Position != nil && *request.Position < 0 {
		return capturecontract.ReaderCollection{}, &ReaderRequestError{Field: "position", Reason: "must not be negative"}
	}
	updated, err := s.repository.AddItem(ctx, principalID, collectionID, request.ExpectedRevision, fragmentID, request.Position, s.now().UTC())
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	return projectReaderCollection(updated), nil
}

func (s *ReaderCollectionService) RemoveItem(ctx context.Context, principalID, collectionID, fragmentID string, expectedRevision int) (capturecontract.ReaderCollection, error) {
	principalID, collectionID, err := s.target(principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	if err := readerCollectionExpected(expectedRevision); err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	if fragmentID, err = readerCollectionFragment(fragmentID); err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	updated, err := s.repository.RemoveItem(ctx, principalID, collectionID, expectedRevision, fragmentID, s.now().UTC())
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	return projectReaderCollection(updated), nil
}

func (s *ReaderCollectionService) Reorder(ctx context.Context, principalID, collectionID string, request capturecontract.ReaderCollectionOrder) (capturecontract.ReaderCollection, error) {
	principalID, collectionID, err := s.target(principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	if err := readerCollectionExpected(request.ExpectedRevision); err != nil {
		return capturecontract.ReaderCollection{}, err
	}
	if len(request.FragmentIDs) > repository.MaxReaderCollectionItems {
		return capturecontract.ReaderCollection{}, &ReaderRequestError{Field: "fragment_ids", Reason: fmt.Sprintf("must name at most %d items", repository.MaxReaderCollectionItems)}
	}
	fragmentIDs := make([]string, 0, len(request.FragmentIDs))
	for _, raw := range request.FragmentIDs {
		fragmentID, err := readerCollectionFragment(raw)
		if err != nil {
			return capturecontract.ReaderCollection{}, err
		}
		fragmentIDs = append(fragmentIDs, fragmentID)
	}
	updated, err := s.repository.Reorder(ctx, principalID, collectionID, request.ExpectedRevision, fragmentIDs, s.now().UTC())
	if err != nil {
		var order *repository.ReaderCollectionOrderError
		if errors.As(err, &order) {
			return capturecontract.ReaderCollection{}, &ReaderRequestError{Field: "fragment_ids", Reason: order.Reason}
		}
		return capturecontract.ReaderCollection{}, err
	}
	return projectReaderCollection(updated), nil
}

// ListItems returns one page of the collection's members as Reader items in
// position order, with the collection as it is now.
func (s *ReaderCollectionService) ListItems(ctx context.Context, principalID, collectionID, cursor string) (capturecontract.ReaderCollectionItems, error) {
	principalID, collectionID, err := s.target(principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollectionItems{}, err
	}
	if s.projection == nil {
		return capturecontract.ReaderCollectionItems{}, fmt.Errorf("reader collection projection is unavailable")
	}
	after, err := decodeReaderCollectionCursor(collectionID, cursor)
	if err != nil {
		return capturecontract.ReaderCollectionItems{}, err
	}
	collection, err := s.repository.Get(ctx, principalID, collectionID)
	if err != nil {
		return capturecontract.ReaderCollectionItems{}, err
	}
	snapshot, err := s.projection.ListCollection(ctx, principalID, collectionID, after, readerPageSize+1)
	if err != nil {
		return capturecontract.ReaderCollectionItems{}, fmt.Errorf("list reader collection items: %w", err)
	}
	hasNext := len(snapshot.Bases) > readerPageSize
	if hasNext {
		snapshot.Bases = snapshot.Bases[:readerPageSize]
	}
	out := capturecontract.ReaderCollectionItems{SchemaVersion: capturecontract.ReaderCollectionItemsVersion,
		Collection: projectReaderCollection(collection), Items: make([]capturecontract.ReaderItem, 0, len(snapshot.Bases))}
	for _, base := range snapshot.Bases {
		item, err := projectReaderItem(base, snapshot, principalID)
		if err != nil {
			return capturecontract.ReaderCollectionItems{}, err
		}
		out.Items = append(out.Items, item)
	}
	if hasNext {
		out.NextCursor, err = encodeReaderCollectionCursor(collectionID, snapshot.Bases[len(snapshot.Bases)-1].CollectionPosition)
		if err != nil {
			return capturecontract.ReaderCollectionItems{}, err
		}
	}
	if err := validateReaderContract(capturecontract.SchemaReaderCollectionItems, out); err != nil {
		return capturecontract.ReaderCollectionItems{}, err
	}
	return out, nil
}

func (s *ReaderCollectionService) principal(principalID string) (string, error) {
	if s == nil || s.repository == nil {
		return "", fmt.Errorf("reader collection service is unavailable")
	}
	return readerPrincipal(principalID)
}

func (s *ReaderCollectionService) target(principalID, collectionID string) (string, string, error) {
	principalID, err := s.principal(principalID)
	if err != nil {
		return "", "", err
	}
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" || len(collectionID) > 255 {
		return "", "", &ReaderRequestError{Field: "collection_id", Reason: "must be a non-empty identifier of at most 255 characters"}
	}
	return principalID, collectionID, nil
}

func projectReaderCollection(collection domain.ReaderCollection) capturecontract.ReaderCollection {
	return capturecontract.ReaderCollection{SchemaVersion: capturecontract.ReaderCollectionVersion,
		CollectionID: collection.ID, Name: collection.Name, Description: collection.Description,
		Kind: collection.Kind, ItemCount: collection.ItemCount, Revision: collection.Revision,
		CreatedAt: collection.CreatedAt.UTC(), UpdatedAt: collection.UpdatedAt.UTC()}
}

func projectReaderCollectionRefs(refs []domain.ReaderCollectionRef) []capturecontract.ReaderCollectionRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]capturecontract.ReaderCollectionRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, capturecontract.ReaderCollectionRef{CollectionID: ref.ID, Name: ref.Name, Kind: ref.Kind})
	}
	return out
}

func readerCollectionName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxReaderCollectionName {
		return "", &ReaderRequestError{Field: "name", Reason: fmt.Sprintf("must contain 1 to %d characters", maxReaderCollectionName)}
	}
	return value, nil
}

func readerCollectionDescription(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxReaderCollectionDescription {
		return "", &ReaderRequestError{Field: "description", Reason: fmt.Sprintf("must contain at most %d characters", maxReaderCollectionDescription)}
	}
	return value, nil
}

func readerCollectionKind(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !readerCollectionKindPattern.MatchString(value) {
		return "", &ReaderRequestError{Field: "kind", Reason: "must be a lowercase slug of at most 32 characters, such as playlist, reading_list or research"}
	}
	return value, nil
}

func readerCollectionExpected(value int) error {
	if value < 0 {
		return &ReaderRequestError{Field: "expected_revision", Reason: "must not be negative"}
	}
	return nil
}

func readerCollectionFragment(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 {
		return "", &ReaderRequestError{Field: "fragment_id", Reason: "must be a non-empty identifier of at most 255 characters"}
	}
	return value, nil
}

type readerCollectionCursorEnvelope struct {
	Version      int    `json:"v"`
	CollectionID string `json:"collection"`
	After        int    `json:"after"`
}

func encodeReaderCollectionCursor(collectionID string, after int) (string, error) {
	raw, err := json.Marshal(readerCollectionCursorEnvelope{Version: 1, CollectionID: collectionID, After: after})
	if err != nil {
		return "", fmt.Errorf("encode reader collection cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeReaderCollectionCursor returns the position to list after; an empty
// cursor starts before the first member.
func decodeReaderCollectionCursor(collectionID, encoded string) (int, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return -1, nil
	}
	if len(encoded) > 2048 {
		return 0, &ReaderRequestError{Field: "cursor", Reason: "is too long"}
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, &ReaderRequestError{Field: "cursor", Reason: "is not a valid opaque cursor"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor readerCollectionCursorEnvelope
	if err := decoder.Decode(&cursor); err != nil {
		return 0, &ReaderRequestError{Field: "cursor", Reason: "is not a valid opaque cursor"}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return 0, &ReaderRequestError{Field: "cursor", Reason: "contains trailing data"}
	}
	if cursor.Version != 1 || cursor.CollectionID != collectionID || cursor.After < 0 {
		return 0, &ReaderRequestError{Field: "cursor", Reason: "does not belong to this collection"}
	}
	return cursor.After, nil
}
