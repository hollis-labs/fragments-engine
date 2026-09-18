package ingest

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// templateContext is what a destination's config-authored argument/body
// template sees. Fragment fields reach a template as DATA, substituted by
// value -- an agent-authored title or content containing "{{...}}"-looking
// text renders as that literal text, it never executes as template syntax.
// Only the template STRING ITSELF is ever parsed, and that string always
// comes from operator-authored destination config (added through
// `route destination-add`/API/MCP), never from fragment content. Content
// choosing its own destination/transport shape is exactly the line
// CW-20260917-0005 exists to hold.
type templateContext struct {
	Fragment domain.Fragment
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// truncate is pipe-friendly: {{.Fragment.Title | truncate 160}}
		"truncate": func(max int, s string) string { return truncateRunes(s, max) },
		"markdown": func(fragment domain.Fragment) string { return renderFragmentMarkdown(fragment) },
	}
}

// renderArgumentTemplate lets a destination's config ("arguments" for mcp,
// "body" for api) reference fragment fields declaratively instead of
// requiring a new Go provider function per destination -- the same value
// Cairn's declarative bootdir layouts get from not needing a second code
// path per agent harness (see CW-20260917-0003). Every string leaf in tmpl
// is parsed and executed as a text/template against
// templateContext{Fragment: fragment}; maps and slices are walked
// recursively; every other value (numbers, bools) passes through unchanged.
func renderArgumentTemplate(tmpl any, fragment domain.Fragment) (any, error) {
	switch v := tmpl.(type) {
	case string:
		return renderArgumentString(v, fragment)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, value := range v {
			rendered, err := renderArgumentTemplate(value, fragment)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			rendered, err := renderArgumentTemplate(value, fragment)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return v, nil
	}
}

func renderArgumentString(raw string, fragment domain.Fragment) (string, error) {
	if !strings.Contains(raw, "{{") {
		// The common case is a plain string with no template markup at all;
		// skip the parse for it.
		return raw, nil
	}
	tmpl, err := template.New("argument").Funcs(templateFuncs()).Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", raw, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateContext{Fragment: fragment}); err != nil {
		return "", fmt.Errorf("execute template %q: %w", raw, err)
	}
	return buf.String(), nil
}
