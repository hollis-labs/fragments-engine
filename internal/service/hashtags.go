package service

import (
	"regexp"
	"strings"
)

// hashtagPattern matches inline hashtags such as "#link" or "#stack-explorer".
var hashtagPattern = regexp.MustCompile(`#([a-zA-Z0-9_-]+)`)

// ExtractHashtags scans content for inline "#tag" tokens and returns their
// values, lowercased and deduplicated case-insensitively, in order of first
// appearance. It never mutates or strips the hashtag text from content -- the
// caller is responsible for leaving the original content untouched; this is a
// read-only scan.
func ExtractHashtags(content string) []string {
	matches := hashtagPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		tag := strings.ToLower(m[1])
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

// urlPattern finds an http(s) URL anywhere in a string. It is intentionally
// simple: it only needs to locate a plausible enrichment target, not validate
// URL structure.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// trailingURLPunct is trimmed off the end of an extracted URL when it looks
// like sentence punctuation rather than part of the URL itself (e.g. the
// trailing "." in "check out https://example.com/foo.").
const trailingURLPunct = ".,;:!?)]}>\"'"

// ExtractFirstURL returns the first http(s):// URL found anywhere in content,
// along with true if one was found. It does not validate the URL -- it is
// meant only to identify a target for link enrichment when the URL is
// embedded mid-text (e.g. "check this out #link https://example.com/foo").
func ExtractFirstURL(content string) (string, bool) {
	match := urlPattern.FindString(content)
	if match == "" {
		return "", false
	}
	match = strings.TrimRight(match, trailingURLPunct)
	if match == "" {
		return "", false
	}
	return match, true
}

// containsTagFold reports whether tags contains target, compared
// case-insensitively.
func containsTagFold(tags []string, target string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, target) {
			return true
		}
	}
	return false
}
