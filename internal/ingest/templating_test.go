package ingest

import (
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestRenderArgumentTemplate_ScalarsPassThroughUnchanged(t *testing.T) {
	rendered, err := renderArgumentTemplate(map[string]any{
		"kind":    "attention",
		"count":   3,
		"enabled": true,
	}, testFragment())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := rendered.(map[string]any)
	if m["kind"] != "attention" || m["count"] != 3 || m["enabled"] != true {
		t.Fatalf("expected non-template values passed through unchanged, got %#v", m)
	}
}

func TestRenderArgumentTemplate_InterpolatesFragmentFieldsAndFuncs(t *testing.T) {
	fragment := testFragment()
	rendered, err := renderArgumentTemplate(map[string]any{
		"idempotency_key": "fe:{{.Fragment.ID}}",
		"title":           "{{.Fragment.Title | truncate 5}}",
		"source": map[string]any{
			"application_id": "fragments-engine",
			"agent_id":       "{{.Fragment.Source}}",
		},
		"tags": []any{"static", "{{.Fragment.SourceType}}"},
	}, fragment)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := rendered.(map[string]any)
	if m["idempotency_key"] != "fe:"+fragment.ID {
		t.Fatalf("unexpected idempotency_key: %#v", m["idempotency_key"])
	}
	if m["title"] != "Claud" {
		t.Fatalf("expected truncated title, got %#v", m["title"])
	}
	source := m["source"].(map[string]any)
	if source["agent_id"] != fragment.Source {
		t.Fatalf("expected nested map field rendered, got %#v", source)
	}
	tags := m["tags"].([]any)
	if tags[1] != fragment.SourceType {
		t.Fatalf("expected slice element rendered, got %#v", tags)
	}
}

func TestRenderArgumentTemplate_MarkdownFunc(t *testing.T) {
	fragment := testFragment()
	rendered, err := renderArgumentTemplate(map[string]any{
		"details_markdown": "{{markdown .Fragment}}",
	}, fragment)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := rendered.(map[string]any)
	got, _ := m["details_markdown"].(string)
	if got != renderFragmentMarkdown(fragment) {
		t.Fatalf("expected markdown func to match renderFragmentMarkdown, got %q", got)
	}
}

// TestRenderArgumentTemplate_FragmentContentIsDataNeverTemplateSyntax is the
// direct guardrail test for CW-20260917-0005: only the operator-authored
// template STRING (the destination config) is ever parsed by text/template.
// Fragment content containing "{{...}}"-looking text must render as that
// literal text, never execute -- an agent-authored draft must not be able to
// smuggle template directives into a destination it doesn't control.
func TestRenderArgumentTemplate_FragmentContentIsDataNeverTemplateSyntax(t *testing.T) {
	fragment := testFragment()
	fragment.Content = `Please review {{.Fragment.ID}} and {{markdown .Fragment}} literally.`
	rendered, err := renderArgumentTemplate(map[string]any{
		"details_markdown": "{{.Fragment.Content}}",
	}, fragment)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := rendered.(map[string]any)
	if m["details_markdown"] != fragment.Content {
		t.Fatalf("expected fragment content substituted verbatim as data, got %#v", m["details_markdown"])
	}
}

func TestRenderFragmentMarkdown_PublicationPathAndSourceFilePath(t *testing.T) {
	fragment := testFragment()
	fragment.MetadataJSON = `{"publication_path":"docs/reports/2026-audit.md","source_file_path":"/tmp/agent-scratch/audit.md"}`
	rendered := renderFragmentMarkdown(fragment)
	if !strings.Contains(rendered, "publication_path: docs/reports/2026-audit.md\n") {
		t.Fatalf("expected publication_path in rendered header, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "source_file_path: /tmp/agent-scratch/audit.md\n") {
		t.Fatalf("expected source_file_path in rendered header, got:\n%s", rendered)
	}
}

func TestRenderFragmentMarkdown_NoMetadata_OmitsOptionalHeaderLines(t *testing.T) {
	fragment := testFragment()
	rendered := renderFragmentMarkdown(fragment)
	if strings.Contains(rendered, "publication_path:") || strings.Contains(rendered, "source_file_path:") {
		t.Fatalf("expected no optional header lines without metadata, got:\n%s", rendered)
	}
}

func TestRenderArgumentTemplate_PlainStringsSkipParsing(t *testing.T) {
	rendered, err := renderArgumentTemplate("no braces here", domain.Fragment{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rendered != "no braces here" {
		t.Fatalf("expected plain string unchanged, got %#v", rendered)
	}
}

func TestRenderArgumentTemplate_InvalidTemplateErrors(t *testing.T) {
	_, err := renderArgumentTemplate(map[string]any{"bad": "{{.Fragment.Nope("}, testFragment())
	if err == nil {
		t.Fatal("expected an error for malformed template syntax")
	}
}
