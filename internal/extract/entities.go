package extract

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

var modelPattern = regexp.MustCompile(`\b(?:gpt-[a-z0-9._-]+|llama[a-z0-9._:-]*|claude[a-z0-9._:-]*|qwen[a-z0-9._:-]*|deepseek[a-z0-9._:-]*|codestral[a-z0-9._:-]*|starcoder[a-z0-9._:-]*)\b`)

var knownTools = []string{
	"claude code",
	"ollama",
	"sqlite",
	"vanta",
	"carrier",
	"nanite",
	"nil",
	"chatgpt",
	"mcp",
}

const minSharedEntityConfidence = 0.8

func FromFragment(fragment domain.Fragment) []domain.FragmentEntity {
	entities := make([]domain.FragmentEntity, 0, 8)
	added := map[string]struct{}{}

	add := func(kind, value, source string, confidence float64) {
		value = normalizeValue(value)
		if value == "" {
			return
		}
		if confidence <= 0 {
			return
		}
		key := kind + "\n" + value + "\n" + source
		if _, ok := added[key]; ok {
			return
		}
		added[key] = struct{}{}
		entities = append(entities, domain.FragmentEntity{
			Kind:       kind,
			Value:      value,
			Source:     source,
			Confidence: confidence,
		})
	}

	meta := metadata(fragment.MetadataJSON)
	switch fragment.Source {
	case "claude":
		collectClaudeEntities(add, meta)
	}
	collectTextEntities(add, fragment)

	slices.SortFunc(entities, func(a, b domain.FragmentEntity) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if a.Value != b.Value {
			return strings.Compare(a.Value, b.Value)
		}
		return strings.Compare(a.Source, b.Source)
	})
	return entities
}

// Kinds returns the full set of entity kinds FromFragment can produce.
// Callers that replace FromFragment's output (e.g. RecallStage, via
// repository.EntityRepository.ReplaceFragmentEntitiesByKind) use this to
// scope their delete+reinsert to just these kinds, so they don't disturb
// entities owned by other ingest stages (e.g. directive tagging, manual
// tags) for the same fragment.
func Kinds() []string {
	return []string{"workspace", "repo", "model", "tool"}
}

func SharedByKind(left, right []domain.FragmentEntity) map[string][]string {
	leftByKind := map[string]map[string]struct{}{}
	rightByKind := map[string]map[string]struct{}{}

	for _, item := range left {
		if item.Confidence < minSharedEntityConfidence {
			continue
		}
		if leftByKind[item.Kind] == nil {
			leftByKind[item.Kind] = map[string]struct{}{}
		}
		leftByKind[item.Kind][item.Value] = struct{}{}
	}
	for _, item := range right {
		if item.Confidence < minSharedEntityConfidence {
			continue
		}
		if rightByKind[item.Kind] == nil {
			rightByKind[item.Kind] = map[string]struct{}{}
		}
		rightByKind[item.Kind][item.Value] = struct{}{}
	}

	out := map[string][]string{}
	for kind, leftValues := range leftByKind {
		rightValues := rightByKind[kind]
		if len(rightValues) == 0 {
			continue
		}
		shared := make([]string, 0, 2)
		for value := range leftValues {
			if _, ok := rightValues[value]; ok {
				shared = append(shared, value)
			}
		}
		if len(shared) == 0 {
			continue
		}
		slices.Sort(shared)
		out[kind] = shared
	}
	return out
}

func collectClaudeEntities(add func(string, string, string, float64), meta map[string]any) {
	cwd := stringValue(meta["cwd"])
	sourceFile := stringValue(meta["source_file"])

	if cwd != "" {
		add("workspace", filepath.Base(strings.TrimRight(cwd, "/")), "metadata.cwd", 0.92)
	}
	if sourceFile != "" {
		projectDir := filepath.Base(filepath.Dir(sourceFile))
		add("repo", projectDir, "metadata.source_file", 0.95)
	}
}

func collectTextEntities(add func(string, string, string, float64), fragment domain.Fragment) {
	text := strings.ToLower(fragment.Title + "\n" + fragment.Summary + "\n" + fragment.Content)
	for _, match := range modelPattern.FindAllString(text, -1) {
		if confidence := modelConfidence(match); confidence > 0 {
			add("model", match, "content.pattern", confidence)
		}
	}
	for _, tool := range knownTools {
		if strings.Contains(text, tool) {
			add("tool", tool, "content.keyword", toolConfidence(tool))
		}
	}
}

func modelConfidence(match string) float64 {
	match = normalizeValue(match)
	if match == "" {
		return 0
	}
	if _, blocked := blockedModels[match]; blocked {
		return 0
	}
	if _, ok := strongModels[match]; ok {
		return 0.9
	}
	if strings.ContainsAny(match, "0123456789") {
		return 0.88
	}
	if strings.Contains(match, "-") || strings.Contains(match, ".") || strings.Contains(match, ":") {
		return 0.84
	}
	return 0
}

func toolConfidence(tool string) float64 {
	if _, ok := strongTools[tool]; ok {
		return 0.84
	}
	return 0.8
}

func metadata(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func normalizeValue(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.Trim(raw, "/")
	if raw == "" || raw == "." {
		return ""
	}
	raw = strings.NewReplacer("_", "-", " ", "-").Replace(raw)
	return raw
}

var blockedModels = map[string]struct{}{
	"claude": {},
	"llama":  {},
}

var strongModels = map[string]struct{}{
	"claude-3-5-sonnet": {},
	"claude-3-7-sonnet": {},
	"claude-4-opus":     {},
	"claude-4-sonnet":   {},
	"gpt-4o":            {},
	"gpt-4.1":           {},
	"llama3.1":          {},
	"nomic-embed-text":  {},
}

var strongTools = map[string]struct{}{
	"ollama":  {},
	"sqlite":  {},
	"vanta":   {},
	"carrier": {},
	"nanite":  {},
	"nil":     {},
}
