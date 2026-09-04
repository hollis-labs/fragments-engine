// Package youtube implements FE's narrow provider contracts without leaking
// YouTube-specific records into the core fragment or media domain.
package youtube

import (
	"context"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/provider"
)

// Client is the injectable provider boundary. A production client may use an
// official API and configured credential resolver; tests use deterministic
// fixtures. No credential bytes or provider response maps cross this boundary.
type Client interface {
	LookupVideo(context.Context, string) (Video, error)
	LookupTranscript(context.Context, string, []string) (Transcript, error)
}

type Video struct {
	ID              string
	Title           string
	Description     string
	ChannelID       string
	ChannelName     string
	Tags            []string
	DurationSeconds float64
	Chapters        []Chapter
	Posters         []Poster
}

type Chapter struct {
	Title        string
	StartSeconds float64
}

type Poster struct {
	ID              string
	URL             string
	SourceExpiresAt time.Time
	MIMEType        string
	Width           int
	Height          int
}

type Transcript struct {
	VideoID         string
	TrackID         string
	Kind            domain.AssetVariantKind
	Language        string
	Format          string
	Text            string
	SourceURL       string
	SourceExpiresAt time.Time
}

type Options struct {
	Version              string
	PosterCustody        domain.CustodyMode
	TranscriptCustody    domain.CustodyMode
	PreferredLanguages   []string
	NetworkClass         provider.NetworkClass
	CredentialReferences []provider.CredentialReference
	Now                  func() time.Time
}

type TranscriptAcquisition struct {
	Asset   domain.MediaAsset
	Result  provider.TranscriptResult
	Content []byte
}
