package sourceutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var defaultDocExts = map[string]struct{}{
	".c":        {},
	".cc":       {},
	".cpp":      {},
	".css":      {},
	".go":       {},
	".h":        {},
	".hpp":      {},
	".html":     {},
	".java":     {},
	".js":       {},
	".json":     {},
	".jsx":      {},
	".md":       {},
	".markdown": {},
	".mdx":      {},
	".mjs":      {},
	".py":       {},
	".rb":       {},
	".rst":      {},
	".rs":       {},
	".sh":       {},
	".sql":      {},
	".svg":      {},
	".toml":     {},
	".ts":       {},
	".tsx":      {},
	".txt":      {},
	".xml":      {},
	".yaml":     {},
	".yml":      {},
}

func NormalizePath(path string) string {
	path = filepath.ToSlash(path)
	path = strings.TrimSpace(path)
	return strings.TrimPrefix(path, "./")
}

func FileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ParseFrontmatter splits a leading "---\n...\n---\n" (or "...\n") YAML
// frontmatter block off the front of body, returning the decoded frontmatter
// map and the remaining body. It returns (nil, body) unchanged when body has
// no frontmatter block, or when the block fails to decode as a YAML mapping,
// so callers can always treat the return as "best-effort metadata, body is
// authoritative."
func ParseFrontmatter(body string) (map[string]any, string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, "---\n") {
		return nil, body
	}
	rest := strings.TrimPrefix(body, "---\n")
	endIdx := strings.Index(rest, "\n---\n")
	tokenLen := len("\n---\n")
	if endIdx == -1 {
		endIdx = strings.Index(rest, "\n...\n")
		tokenLen = len("\n...\n")
	}
	if endIdx == -1 {
		return nil, body
	}
	var frontmatter map[string]any
	if err := yaml.NewDecoder(bytes.NewBufferString(rest[:endIdx])).Decode(&frontmatter); err != nil || len(frontmatter) == 0 {
		return nil, body
	}
	return frontmatter, rest[endIdx+tokenLen:]
}

func SortedKeys[V any](in map[string]V) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ValidateGlobs(patterns []string) error {
	for _, pattern := range patterns {
		if _, err := compileGlob(pattern); err != nil {
			return err
		}
	}
	return nil
}

func MatchAnyGlob(path string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return false, nil
	}
	for _, pattern := range patterns {
		matched, err := MatchGlob(path, pattern)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func MatchGlob(path, pattern string) (bool, error) {
	re, err := compileGlob(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(NormalizePath(path)), nil
}

func ShouldIncludePath(path string, include, exclude []string) (bool, error) {
	normalized := NormalizePath(path)
	if len(include) == 0 {
		if !IsDefaultDocPath(normalized) {
			return false, nil
		}
	} else {
		matched, err := MatchAnyGlob(normalized, include)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
	}
	excluded, err := MatchAnyGlob(normalized, exclude)
	if err != nil {
		return false, err
	}
	return !excluded, nil
}

func IsDefaultDocPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := defaultDocExts[ext]
	return ok
}

func ParseTimeBound(now time.Time, raw string, isSince bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if duration, err := time.ParseDuration(raw); err == nil {
		if isSince {
			return now.Add(-duration).UTC().Format(time.RFC3339), nil
		}
		return now.Add(duration).UTC().Format(time.RFC3339), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("invalid time bound %q", raw)
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	pattern = NormalizePath(pattern)
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					out.WriteString(`(?:.*/)?`)
					i += 2
					continue
				}
				out.WriteString(".*")
				i++
				continue
			}
			out.WriteString(`[^/]*`)
		case '?':
			out.WriteString(`[^/]`)
		default:
			out.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	out.WriteString("$")
	re, err := regexp.Compile(out.String())
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %w", pattern, err)
	}
	return re, nil
}
