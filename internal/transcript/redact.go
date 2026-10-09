// Package transcript owns the only persistence boundary for transcript sources.
// Its destination is local and owner-private; it has no delivery or recall port.
package transcript

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

const redacted = "[REDACTED]"
const maxRecordBytes = 16 << 20

var ErrPrivateStoreRequired = errors.New("transcript: an owner-private store is required")
var ErrHistoricalDispositionRequired = errors.New("transcript: legacy shared material requires explicit historical-data disposition before backfill")
var ErrAttachments = errors.New("transcript: attachment persistence is not supported")

var secretKey = regexp.MustCompile(`(?i)^(?:.*[_-])?(?:api[_-]?key|access[_-]?key|secret(?:[_-]?key)?|password|passwd|token|access[_-]?token|refresh[_-]?token|authorization|cookie|credential|client[_-]?secret|private[_-]?key)s?$`)
var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]*PRIVATE KEY|OPENSSH PRIVATE KEY)-----.*?(?:-----END (?:[A-Z0-9 ]*PRIVATE KEY|OPENSSH PRIVATE KEY)-----|$)`),
	regexp.MustCompile(`(?im)\b(?:authorization|cookie|set-cookie)\s*:\s*[^\r\n]+`),
	regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`\b(?:sk-(?:proj-|ant-)?[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9]{12,}|github_pat_[A-Za-z0-9_]{12,}|xox[baprs]-[A-Za-z0-9-]{8,}|(?:AKIA|ASIA)[A-Z0-9]{16}|AIza[A-Za-z0-9_-]{35})\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	// Include quoted JSON/YAML assignments and shell environment assignments.
	regexp.MustCompile(`(?i)["']?\b(?:[A-Z0-9]+[_-])*(?:api[_-]?key|access[_-]?key|secret(?:[_-]?key)?|password|passwd|(?:access[_-]?|refresh[_-]?)?token|client[_-]?secret|private[_-]?key)["']?\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;&<>]+)`),
	regexp.MustCompile(`(?i)https?://[^\s/@]+:[^\s/@]+@`),
}

// Redact is deterministic and local. It covers recognizable credentials, not
// every possible secret. Owner-only storage is still required after redaction.
func Redact(text string) string {
	for _, pattern := range patterns {
		text = pattern.ReplaceAllString(text, redacted)
	}
	return text
}

// sanitize detaches and redacts every persisted string, including provenance
// and nested metadata. Opaque metadata and attachments fail closed rather than
// encoding unsanitized bytes or consulting an external analysis provider.
func sanitize(in domain.PipelineFragment) (domain.PipelineFragment, []byte, error) {
	if len(in.Attachments) != 0 {
		return domain.PipelineFragment{}, nil, ErrAttachments
	}
	if err := validateMetadata(in.Metadata, 0); err != nil {
		return domain.PipelineFragment{}, nil, err
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > maxRecordBytes {
		return domain.PipelineFragment{}, nil, errors.New("transcript: invalid or oversized record")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return domain.PipelineFragment{}, nil, errors.New("transcript: invalid record")
	}
	value = redactValue(value)
	clean, err := json.Marshal(value)
	if err != nil {
		return domain.PipelineFragment{}, nil, errors.New("transcript: cannot encode redacted record")
	}
	var out domain.PipelineFragment
	if err := json.Unmarshal(clean, &out); err != nil {
		return domain.PipelineFragment{}, nil, errors.New("transcript: cannot decode redacted record")
	}
	return out, clean, nil
}

func redactValue(value any) any {
	switch v := value.(type) {
	case string:
		return Redact(v)
	case []any:
		for i := range v {
			v[i] = redactValue(v[i])
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if secretKey.MatchString(key) {
				out[Redact(key)] = redacted
			} else {
				out[Redact(key)] = redactValue(item)
			}
		}
		return out
	default:
		return value
	}
}

func validateMetadata(value any, depth int) error {
	if depth > 32 {
		return errors.New("transcript: metadata nesting limit exceeded")
	}
	switch v := value.(type) {
	case nil, string, bool, float64, float32, int, int64, int32, uint, uint64, uint32, json.Number:
		return nil
	case []string:
		return nil
	case []any:
		for _, child := range v {
			if err := validateMetadata(child, depth+1); err != nil {
				return err
			}
		}
	case map[string]string:
		return nil
	case map[string]any:
		for _, child := range v {
			if err := validateMetadata(child, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("transcript: opaque metadata is not supported")
	}
	return nil
}

func valid(in domain.PipelineFragment) bool {
	return strings.TrimSpace(in.Source) != "" && strings.TrimSpace(in.SourceID) != "" && strings.TrimSpace(in.Content) != ""
}
