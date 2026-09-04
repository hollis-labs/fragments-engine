package youtube

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/provider"
)

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func CanonicalWatchURL(videoID string) (string, error) {
	videoID = strings.TrimSpace(videoID)
	if !videoIDPattern.MatchString(videoID) {
		return "", fmt.Errorf("invalid YouTube video ID")
	}
	return "https://www.youtube.com/watch?v=" + videoID, nil
}

// ParseVideoURL accepts only known ID-bearing YouTube URL shapes. Host suffix
// matching is intentionally avoided: youtube.com.evil.example is not YouTube.
func ParseVideoURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.User != nil || parsed.Port() != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("YouTube URL must be an absolute HTTP(S) URL without credentials or an explicit port")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	escapedPath := parsed.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/") {
		return "", fmt.Errorf("YouTube URL path is invalid")
	}
	path := strings.TrimPrefix(escapedPath, "/")
	if strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	if path == "" || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return "", fmt.Errorf("YouTube URL path is invalid")
	}
	segments := strings.Split(path, "/")
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("YouTube URL query is invalid")
	}
	var id string
	switch host {
	case "youtu.be", "www.youtu.be":
		if len(segments) != 1 {
			return "", fmt.Errorf("unsupported youtu.be URL path")
		}
		id, err = url.PathUnescape(segments[0])
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
		if path == "watch" {
			values := query["v"]
			if len(values) != 1 {
				return "", fmt.Errorf("YouTube watch URL requires one video ID")
			}
			id = values[0]
		} else if len(segments) == 2 && (segments[0] == "shorts" || segments[0] == "embed" || segments[0] == "live") {
			id, err = url.PathUnescape(segments[1])
		} else {
			return "", fmt.Errorf("unsupported YouTube URL path")
		}
	case "youtube-nocookie.com", "www.youtube-nocookie.com":
		if len(segments) != 2 || segments[0] != "embed" {
			return "", fmt.Errorf("unsupported youtube-nocookie URL path")
		}
		id, err = url.PathUnescape(segments[1])
	default:
		return "", fmt.Errorf("URL host is not an allowlisted YouTube host")
	}
	if err != nil || !videoIDPattern.MatchString(id) {
		return "", fmt.Errorf("YouTube URL contains an invalid video ID")
	}
	if path != "watch" {
		if values := query["v"]; len(values) > 0 && (len(values) != 1 || values[0] != id) {
			return "", fmt.Errorf("YouTube URL contains conflicting video IDs")
		}
	}
	return id, nil
}

func resolveInputVideoID(input provider.Input) (string, error) {
	source := input.Source
	var candidates []string
	add := func(label, candidate string) error {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return nil
		}
		if !videoIDPattern.MatchString(candidate) {
			return fmt.Errorf("%s contains an invalid YouTube video ID", label)
		}
		candidates = append(candidates, candidate)
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(source.Provider), "youtube") {
		return "", fmt.Errorf("source provider is not YouTube")
	}
	if err := add("provider item ID", source.ProviderItemID); err != nil {
		return "", err
	}
	key := strings.TrimSpace(source.SourceItemKey)
	if strings.HasPrefix(strings.ToLower(key), "youtube:") {
		key = key[len("youtube:"):]
	}
	if strings.Contains(key, "://") {
		id, err := ParseVideoURL(key)
		if err != nil {
			return "", fmt.Errorf("source item key: %w", err)
		}
		key = id
	}
	if err := add("source item key", key); err != nil {
		return "", err
	}
	for _, item := range []struct{ label, value string }{
		{"submitted URL", source.SubmittedURL},
		{"canonical URL", source.CanonicalURL},
		{"source locator", source.SourceLocator},
	} {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		candidate := strings.TrimSpace(item.value)
		if !strings.Contains(candidate, "://") {
			if strings.HasPrefix(strings.ToLower(candidate), "youtube:") {
				candidate = candidate[len("youtube:"):]
			}
			if err := add(item.label, candidate); err != nil {
				return "", err
			}
			continue
		}
		id, err := ParseVideoURL(candidate)
		if err != nil {
			return "", fmt.Errorf("%s: %w", item.label, err)
		}
		candidates = append(candidates, id)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("YouTube source has no video identity candidate")
	}
	for _, candidate := range candidates[1:] {
		if candidate != candidates[0] {
			return "", fmt.Errorf("YouTube source identity candidates disagree")
		}
	}
	return candidates[0], nil
}
