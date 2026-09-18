package config

import "gopkg.in/yaml.v3"

// mergeYAML rewrites original with cfg's values while preserving everything
// about the original document that cfg doesn't actually change: comments,
// key order, indentation style, and any key cfg's schema doesn't model.
//
// This ports Cairn's config-merge principle (install/merge.go) from JSON to
// YAML: "a key it declares carries its value; every other key stands,
// wherever it sits." Save() used to round-trip through a full
// yaml.Marshal(&cfg), which always re-encodes every struct field from
// scratch -- destroying comments, reordering keys to struct-field order, and
// writing out zero-value defaults that were never present in the source
// file. That is the reason fragments.yaml has to be seeded once from
// fragments.example.yaml and never pointed back at the template (see
// scripts/seed-config.sh): the first ingest CRUD write already clobbered it.
//
// If original isn't a YAML mapping document (empty, malformed, or genuinely
// not present), there's nothing byte-preserving to do -- fall back to a
// plain marshal of cfg.
func mergeYAML(original []byte, cfg Config) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(original, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return yaml.Marshal(&cfg)
	}

	var desired yaml.Node
	if err := desired.Encode(&cfg); err != nil {
		return nil, err
	}
	if desired.Kind != yaml.MappingNode {
		return yaml.Marshal(&cfg)
	}

	mergeMappingNode(doc.Content[0], &desired)
	return yaml.Marshal(&doc)
}

// mergeMappingNode applies dst := merge(dst, src) in place, recursively.
// Every key src declares carries src's value; every key dst has that src
// does not declare stands untouched, at its original position. A surviving
// key's own comments are never touched -- only the value node's content is
// replaced, and only when the value actually differs in shape from a plain
// scalar swap.
func mergeMappingNode(dst, src *yaml.Node) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, val := src.Content[i], src.Content[i+1]
		j := findMappingKey(dst, key.Value)
		if j == -1 {
			dst.Content = append(dst.Content, key, val)
			continue
		}
		dstVal := dst.Content[j+1]
		switch {
		case dstVal.Kind == yaml.MappingNode && val.Kind == yaml.MappingNode:
			mergeMappingNode(dstVal, val)
		case dstVal.Kind == yaml.SequenceNode && val.Kind == yaml.SequenceNode:
			mergeSequenceNode(dstVal, val)
		default:
			replaceNodeValue(dstVal, val)
		}
	}
}

// mergeSequenceNode merges a sequence of mappings by their "name" field when
// both sides carry one (e.g. Config.Ingests): a src item whose name matches
// a dst item merges into that item's existing node, keeping its comments; a
// src item with no match is a new item, appended; a dst item absent from src
// was removed, and is dropped. A sequence with no "name" field to key on
// (e.g. a plain string list such as an ingest's include/exclude globs) has
// no identity to match by, so it is replaced wholesale by src -- FE has no
// per-scalar comments worth preserving in those lists today.
func mergeSequenceNode(dst, src *yaml.Node) {
	used := make([]bool, len(dst.Content))
	merged := make([]*yaml.Node, 0, len(src.Content))
	for _, srcItem := range src.Content {
		name := mappingIdentityValue(srcItem)
		matched := -1
		if name != "" {
			for i, dstItem := range dst.Content {
				if !used[i] && mappingIdentityValue(dstItem) == name {
					matched = i
					break
				}
			}
		}
		if matched == -1 {
			merged = append(merged, srcItem)
			continue
		}
		used[matched] = true
		dstItem := dst.Content[matched]
		if dstItem.Kind == yaml.MappingNode && srcItem.Kind == yaml.MappingNode {
			mergeMappingNode(dstItem, srcItem)
			merged = append(merged, dstItem)
		} else {
			merged = append(merged, srcItem)
		}
	}
	dst.Content = merged
}

func findMappingKey(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func mappingIdentityValue(node *yaml.Node) string {
	if node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "name" && node.Content[i+1].Kind == yaml.ScalarNode {
			return node.Content[i+1].Value
		}
	}
	return ""
}

// replaceNodeValue overwrites dst's content with src's, keeping dst's own
// comments -- the "value changed shape" fallback for anything not handled by
// the mapping/sequence recursion above: a scalar, or a kind mismatch (e.g.
// a field's type changed across a schema revision).
func replaceNodeValue(dst, src *yaml.Node) {
	dst.Kind = src.Kind
	dst.Tag = src.Tag
	dst.Value = src.Value
	dst.Style = src.Style
	dst.Anchor = src.Anchor
	dst.Content = src.Content
}
