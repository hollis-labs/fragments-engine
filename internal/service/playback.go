package service

import (
	"fmt"
	"math"
	"strings"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/provider"
)

// TrustedYouTubePlaybackSpec is the sole constructor for Reader provider
// playback. It accepts a provider identity, never a source URL or provider
// markup, and delegates the closed allowlist/ID check to the provider contract.
func TrustedYouTubePlaybackSpec(providerName, providerItemID string, startSeconds float64) (capturecontract.PlaybackSpec, error) {
	if math.IsNaN(startSeconds) || math.IsInf(startSeconds, 0) {
		return capturecontract.PlaybackSpec{}, fmt.Errorf("construct trusted playback: start must be finite")
	}
	spec := provider.PlaybackSpec{
		Kind:           provider.PlaybackProviderEmbed,
		Provider:       strings.ToLower(strings.TrimSpace(providerName)),
		ProviderItemID: strings.TrimSpace(providerItemID),
		StartSeconds:   startSeconds,
	}
	if err := provider.ValidatePlaybackSpec(spec); err != nil {
		return capturecontract.PlaybackSpec{}, fmt.Errorf("construct trusted playback: %w", err)
	}
	start := spec.StartSeconds
	return capturecontract.PlaybackSpec{
		Kind:           string(spec.Kind),
		Provider:       spec.Provider,
		ProviderItemID: spec.ProviderItemID,
		StartSeconds:   &start,
	}, nil
}
