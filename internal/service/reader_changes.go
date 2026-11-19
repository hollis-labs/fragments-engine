package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

const (
	readerChangesDefaultLimit = 50
	readerChangesMaxLimit     = 200
)

type readerChangeRepository interface {
	ListChanges(context.Context, string, repository.ReaderChangeCursor, int64, int) (repository.ReaderSnapshot, error)
}

type ReaderChangesRequest struct {
	PrincipalID string
	// After is the opaque cursor from an earlier response; empty starts from
	// the beginning of the feed.
	After string
	// Limit is the page size; zero means the default.
	Limit int
}

// Changes returns the Reader items changed since a cursor, oldest change
// first, each once and at its current state. See docs/reader-change-feed.md.
func (s *ReaderService) Changes(ctx context.Context, request ReaderChangesRequest) (capturecontract.ReaderChangeList, error) {
	if s == nil || s.repository == nil {
		return capturecontract.ReaderChangeList{}, fmt.Errorf("reader service is unavailable")
	}
	changes, ok := s.repository.(readerChangeRepository)
	if !ok {
		return capturecontract.ReaderChangeList{}, fmt.Errorf("reader change feed is unavailable")
	}
	principalID, err := readerPrincipal(request.PrincipalID)
	if err != nil {
		return capturecontract.ReaderChangeList{}, err
	}
	limit := request.Limit
	if limit == 0 {
		limit = readerChangesDefaultLimit
	}
	if limit < 1 || limit > readerChangesMaxLimit {
		return capturecontract.ReaderChangeList{}, &ReaderRequestError{Field: "limit", Reason: fmt.Sprintf("must be between 1 and %d", readerChangesMaxLimit)}
	}
	cursor, err := decodeReaderChangeCursor(principalID, request.After)
	if err != nil {
		return capturecontract.ReaderChangeList{}, err
	}
	snapshot, err := changes.ListChanges(ctx, principalID,
		repository.ReaderChangeCursor{Seq: cursor.Seq, FragmentID: cursor.FragmentID}, cursor.Base, limit+1)
	if err != nil {
		return capturecontract.ReaderChangeList{}, fmt.Errorf("list reader changes: %w", err)
	}
	caughtUp := len(snapshot.Bases) <= limit
	if !caughtUp {
		snapshot.Bases = snapshot.Bases[:limit]
	}
	out := capturecontract.ReaderChangeList{SchemaVersion: capturecontract.ReaderChangesVersion,
		Items: make([]capturecontract.ReaderChange, 0, len(snapshot.Bases)), CaughtUp: caughtUp}
	for _, base := range snapshot.Bases {
		item, err := projectReaderItem(base, snapshot, principalID)
		if err != nil {
			return capturecontract.ReaderChangeList{}, err
		}
		kinds := snapshot.ChangeKinds[base.FragmentID]
		if kinds == nil {
			kinds = []string{}
		}
		out.Items = append(out.Items, capturecontract.ReaderChange{FragmentID: base.FragmentID,
			ChangeSeq: base.ChangeSeq, Kinds: kinds, Item: item})
	}
	next := cursor
	if len(snapshot.Bases) > 0 {
		last := snapshot.Bases[len(snapshot.Bases)-1]
		next.Seq, next.FragmentID = last.ChangeSeq, last.FragmentID
	}
	if caughtUp {
		// Every change at or below the sequence read before the page has been
		// returned or is not a Reader item, so the cursor moves past all of it
		// and the next walk reports kinds from here.
		if snapshot.ChangeSequence > next.Seq {
			next.Seq = snapshot.ChangeSequence
		}
		next.FragmentID = ""
		next.Base = next.Seq
	}
	if out.NextCursor, err = encodeReaderChangeCursor(principalID, next); err != nil {
		return capturecontract.ReaderChangeList{}, err
	}
	if err := validateReaderContract(capturecontract.SchemaReaderChanges, out); err != nil {
		return capturecontract.ReaderChangeList{}, err
	}
	return out, nil
}

// readerChangeCursor is the decoded feed cursor. Seq and FragmentID are the
// position; Base is the sequence the client was last caught up at, which is
// where the kinds of a change are counted from.
type readerChangeCursor struct {
	Seq        int64
	FragmentID string
	Base       int64
}

type readerChangeCursorEnvelope struct {
	Version    int    `json:"v"`
	Principal  string `json:"p"`
	Seq        int64  `json:"seq"`
	FragmentID string `json:"f,omitempty"`
	Base       int64  `json:"base"`
}

// readerChangePrincipalKey binds a cursor to its principal without carrying
// the principal ID in it.
func readerChangePrincipalKey(principalID string) string {
	sum := sha256.Sum256([]byte(principalID))
	return hex.EncodeToString(sum[:6])
}

func encodeReaderChangeCursor(principalID string, cursor readerChangeCursor) (string, error) {
	raw, err := json.Marshal(readerChangeCursorEnvelope{Version: 1, Principal: readerChangePrincipalKey(principalID),
		Seq: cursor.Seq, FragmentID: cursor.FragmentID, Base: cursor.Base})
	if err != nil {
		return "", fmt.Errorf("encode reader change cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeReaderChangeCursor(principalID, encoded string) (readerChangeCursor, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return readerChangeCursor{}, nil
	}
	invalid := func(reason string) (readerChangeCursor, error) {
		return readerChangeCursor{}, &ReaderRequestError{Field: "after", Reason: reason}
	}
	if len(encoded) > 2048 {
		return invalid("is too long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return invalid("is not a valid opaque cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor readerChangeCursorEnvelope
	if err := decoder.Decode(&cursor); err != nil {
		return invalid("is not a valid opaque cursor")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalid("contains trailing data")
	}
	if cursor.Version != 1 || cursor.Seq < 0 || cursor.Base < 0 || cursor.Base > cursor.Seq || len(cursor.FragmentID) > 255 {
		return invalid("is not a valid opaque cursor")
	}
	if cursor.Principal != readerChangePrincipalKey(principalID) {
		return invalid("was issued to a different principal")
	}
	return readerChangeCursor{Seq: cursor.Seq, FragmentID: cursor.FragmentID, Base: cursor.Base}, nil
}
