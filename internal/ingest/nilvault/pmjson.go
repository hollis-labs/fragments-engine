package nilvault

import (
	"encoding/json"
	"strconv"
	"strings"
)

// pmNode is the on-wire shape of a TipTap/ProseMirror node, as stored in
// Nil's todos.notes_doc column. It deliberately mirrors Nil's own
// ingest.Node shape (see ingest/doc.go in Nil's repo: {type, attrs, content,
// marks, text}) closely enough to decode the same JSON, but is NOT a code
// dependency on Nil's package -- this is FE's own, intentionally minimal,
// read-only walker.
type pmNode struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []pmNode       `json:"content,omitempty"`
	Marks   []pmMark       `json:"marks,omitempty"`
	Text    string         `json:"text,omitempty"`
}

type pmMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// convertNotesDoc converts a notes_doc PM-JSON string into readable plain
// text, and separately collects the target IDs of any wikilink nodes
// encountered while walking (per-vault todo IDs, as strings). It only
// understands TipTap's standard default node types (paragraph, heading,
// bulletList/orderedList/listItem, codeBlock, blockquote, horizontalRule,
// hardBreak, text, wikilink) -- anything else degrades gracefully rather
// than erroring, since a future Nil plugin could introduce custom node
// types: nodes with a "text" field contribute that text, nodes with
// "content" are recursed into, and anything else is silently skipped.
//
// If raw isn't valid JSON at all (unexpected, but not impossible for a
// foreign store read directly off disk), the raw string is returned as-is
// rather than dropping the item's content entirely.
func convertNotesDoc(raw string) (string, []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var root pmNode
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return raw, nil
	}
	var linked []string
	blocks := renderBlocks(root.Content, &linked)
	return strings.TrimSpace(strings.Join(blocks, "\n\n")), dedupStrings(linked)
}

func renderBlocks(nodes []pmNode, linked *[]string) []string {
	blocks := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if block := renderBlock(n, linked); strings.TrimSpace(block) != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func renderBlock(n pmNode, linked *[]string) string {
	switch n.Type {
	case "paragraph", "heading":
		text := renderInline(n.Content, linked)
		if n.Type == "heading" {
			level := attrInt(n.Attrs, "level", 1)
			text = strings.Repeat("#", level) + " " + text
		}
		return text
	case "codeBlock":
		return renderInline(n.Content, linked)
	case "blockquote":
		inner := renderBlocks(n.Content, linked)
		var quoted []string
		for _, block := range inner {
			for _, line := range strings.Split(block, "\n") {
				quoted = append(quoted, "> "+line)
			}
		}
		return strings.Join(quoted, "\n")
	case "bulletList":
		return renderList(n.Content, linked, false)
	case "orderedList":
		return renderList(n.Content, linked, true)
	case "listItem":
		// listItem should normally only be reached via renderList, but
		// degrade sensibly if walked directly (e.g. malformed doc).
		return strings.Join(renderBlocks(n.Content, linked), "\n")
	case "horizontalRule":
		return "---"
	default:
		// Unrecognized node type: degrade gracefully rather than error.
		if n.Text != "" {
			return n.Text
		}
		if len(n.Content) > 0 {
			return strings.Join(renderBlocks(n.Content, linked), "\n\n")
		}
		return ""
	}
}

func renderList(items []pmNode, linked *[]string, ordered bool) string {
	lines := make([]string, 0, len(items))
	for i, item := range items {
		marker := "- "
		if ordered {
			marker = strconv.Itoa(i+1) + ". "
		}
		itemBlocks := renderBlocks(item.Content, linked)
		if len(itemBlocks) == 0 {
			continue
		}
		lines = append(lines, marker+itemBlocks[0])
		for _, extra := range itemBlocks[1:] {
			for _, line := range strings.Split(extra, "\n") {
				lines = append(lines, "  "+line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// renderInline walks inline content (text, hardBreak, wikilink, and marked
// text) into a single string. Unrecognized inline node types degrade the
// same way as renderBlock: use n.Text if present, else recurse into
// n.Content, else skip.
func renderInline(nodes []pmNode, linked *[]string) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(n.Text)
		case "hardBreak":
			b.WriteString("\n")
		case "wikilink":
			label, _ := n.Attrs["label"].(string)
			if label == "" {
				label = "wikilink"
			}
			b.WriteString(label)
			if id := wikilinkTargetID(n.Attrs); id != "" {
				*linked = append(*linked, id)
			}
		default:
			if n.Text != "" {
				b.WriteString(n.Text)
			} else if len(n.Content) > 0 {
				b.WriteString(renderInline(n.Content, linked))
			}
		}
	}
	return b.String()
}

// wikilinkTargetID extracts a wikilink node's target todo ID from its attrs
// map as a string. Nil's html.go encodes it as attrs["id"], written as a
// JSON number when produced by MarshalDoc -- which decodes here as
// float64 -- but a string form is tolerated too, defensively.
func wikilinkTargetID(attrs map[string]any) string {
	v, ok := attrs["id"]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case string:
		return strings.TrimSpace(t)
	default:
		return ""
	}
}

func attrInt(attrs map[string]any, key string, fallback int) int {
	v, ok := attrs[key]
	if !ok {
		return fallback
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	default:
		return fallback
	}
}

func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
