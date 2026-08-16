package linkcontent

import (
	"html"
	"regexp"
	"strings"
)

var (
	metaTagPattern  = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	metaAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:][a-zA-Z0-9_:\-]*)\s*=\s*["']([^"']*)["']`)
	titleTagPattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	spacePattern    = regexp.MustCompile(`\s+`)
)

// ParseMetaTags extracts <meta property="..."|name="..." content="..."> pairs from raw
// HTML, keyed by lowercased property/name (e.g. "og:description", "twitter:title").
// This is the single shared implementation of the OG-tag/meta-description scraping logic
// used by both the local link-content backend and the manual intake enricher.
func ParseMetaTags(raw string) map[string]string {
	tags := metaTagPattern.FindAllString(raw, -1)
	out := make(map[string]string, len(tags))
	for _, tag := range tags {
		attrs := metaAttrPattern.FindAllStringSubmatch(tag, -1)
		if len(attrs) == 0 {
			continue
		}
		attrMap := map[string]string{}
		for _, attr := range attrs {
			if len(attr) < 3 {
				continue
			}
			attrMap[strings.ToLower(strings.TrimSpace(attr[1]))] = strings.TrimSpace(attr[2])
		}
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(attrMap["property"], attrMap["name"])))
		if key == "" {
			continue
		}
		content := strings.TrimSpace(attrMap["content"])
		if content == "" {
			continue
		}
		out[key] = content
	}
	return out
}

// HTMLTitle extracts and normalizes the contents of the first <title> tag in raw HTML.
func HTMLTitle(raw string) string {
	match := titleTagPattern.FindStringSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	return NormalizeText(match[1])
}

// NormalizeText unescapes HTML entities and collapses runs of whitespace to a single space.
func NormalizeText(text string) string {
	text = html.UnescapeString(text)
	return strings.TrimSpace(spacePattern.ReplaceAllString(text, " "))
}

// PreviewText trims text to at most limit characters, breaking cleanly and appending an
// ellipsis when truncation occurs.
func PreviewText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	if limit < 4 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
