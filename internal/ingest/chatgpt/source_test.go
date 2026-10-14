package chatgpt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func TestSourceCollect(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "chatgpt-export"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}

	fragments, err := Source{}.Collect(context.Background(), config.IngestConfig{
		Name:   "chatgpt-test",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
	})
	if err != nil {
		t.Fatalf("collect chatgpt export: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(fragments))
	}
	item := fragments[0]
	if item.Source != "chatgpt" || item.SourceType != "chat" {
		t.Fatalf("unexpected source fields: %+v", item)
	}
	if item.SourceID != "conv-123" {
		t.Fatalf("unexpected source id: %s", item.SourceID)
	}
	if item.Title != "ChatGPT chat: Roadmap planning" {
		t.Fatalf("unexpected title: %s", item.Title)
	}
	if !strings.Contains(item.Content, "## user") || !strings.Contains(item.Content, "## assistant") {
		t.Fatalf("expected user and assistant blocks: %s", item.Content)
	}
	if !strings.Contains(item.Content, "deterministic ingest and search") {
		t.Fatalf("expected assistant content: %s", item.Content)
	}
	if item.Metadata["user_email"] != "tester@example.com" {
		t.Fatalf("expected user email metadata, got %#v", item.Metadata["user_email"])
	}
	if len(item.Attachments) != 0 {
		t.Fatalf("private transcript path must not emit attachments, got %d", len(item.Attachments))
	}
	if item.Metadata["attachment_count"] != 2 {
		t.Fatalf("expected attachment_count=2, got %#v", item.Metadata["attachment_count"])
	}
	if item.Metadata["url_refs_skipped"] != 1 {
		t.Fatalf("expected url_refs_skipped=1, got %#v", item.Metadata["url_refs_skipped"])
	}
	refs, ok := item.Metadata["attachment_refs"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("expected 2 attachment refs, got %#v", item.Metadata["attachment_refs"])
	}
	first := refs[0].(map[string]any)
	if first["kind"] != "image" || first["mime"] != "image/png" {
		t.Fatalf("unexpected first ref: %#v", first)
	}
	sum := sha256.Sum256([]byte("diagram.png"))
	if first["name_sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("name hash mismatch: %#v", first["name_sha256"])
	}
	for _, ref := range refs {
		for key, value := range ref.(map[string]any) {
			if text, ok := value.(string); ok && (strings.Contains(text, "diagram.png") || strings.Contains(text, "roadmap-note.md") || strings.Contains(text, "/") && key != "mime") {
				t.Fatalf("ref %q leaks a name or path", key)
			}
		}
	}
}

func TestSourceCollect_RefusesRawArchiveCopy(t *testing.T) {
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "chatgpt-export", "export-001"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	sourceRoot := filepath.Join(t.TempDir(), "download-export")
	if err := os.MkdirAll(sourceRoot, 0o750); err != nil {
		t.Fatalf("mkdir source root: %v", err)
	}
	for _, name := range []string{"conversations-000.json", "user.json"} {
		raw, err := os.ReadFile(filepath.Join(fixtureRoot, name))
		if err != nil {
			t.Fatalf("read fixture file %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(sourceRoot, name), raw, 0o600); err != nil {
			t.Fatalf("write source file %s: %v", name, err)
		}
	}

	archiveRoot := filepath.Join(t.TempDir(), "must-remain-absent")
	fragments, err := Source{}.Collect(context.Background(), config.IngestConfig{
		Name:   "chatgpt-archive-test",
		Kind:   kind,
		Source: config.IngestSource{Root: sourceRoot},
		Rules: map[string]any{
			"archive_root":         archiveRoot,
			"copy_text_exports":    true,
			"delete_copied_source": false,
		},
	})

	if err == nil || len(fragments) != 0 {
		t.Fatal("raw archive copy was not refused")
	}
	if _, err := os.Stat(archiveRoot); !os.IsNotExist(err) {
		t.Fatal("raw archive destination was touched")
	}
	if _, err := os.Stat(filepath.Join(sourceRoot, "conversations-000.json")); err != nil {
		t.Fatal("source was modified")
	}

}

func TestValidateArchivePolicy(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "downloads", "chatgpt-export")
	if err := os.MkdirAll(sourceRoot, 0o750); err != nil {
		t.Fatalf("mkdir source root: %v", err)
	}

	_, err := ValidateArchivePolicy(sourceRoot, config.ChatGPTExportRules{
		ArchiveRoot:     filepath.Join(t.TempDir(), "random-root"),
		CopyTextExports: true,
	})
	if err == nil || !strings.Contains(err.Error(), "archive_root must live under") {
		t.Fatalf("expected canonical archive root error, got %v", err)
	}

	canonicalRoot := config.ExpandHome("~/Documents/corpus/ai-chat-logs/chatgpt/logs")
	_, err = ValidateArchivePolicy(filepath.Join(canonicalRoot, "export-123"), config.ChatGPTExportRules{
		ArchiveRoot:     canonicalRoot,
		CopyTextExports: true,
	})
	if err == nil || !strings.Contains(err.Error(), "source.root must not be inside archive_root") {
		t.Fatalf("expected nested source error, got %v", err)
	}
}

func TestExtractTextParts(t *testing.T) {
	cases := []struct {
		name        string
		content     exportContent
		wantText    string
		wantNonText int
	}{
		{"text", exportContent{ContentType: "text", Parts: []any{"hello", " ", "world"}}, "hello\nworld", 0},
		{"multimodal keeps text and counts non-text", exportContent{ContentType: "multimodal_text", Parts: []any{
			map[string]any{"content_type": "image_asset_pointer"}, "caption", map[string]any{"content_type": "image_asset_pointer"},
		}}, "caption", 2},
		{"multimodal with only non-text", exportContent{ContentType: "multimodal_text", Parts: []any{map[string]any{"x": 1}}}, "", 1},
		{"nil part is not counted", exportContent{ContentType: "multimodal_text", Parts: []any{nil, "t"}}, "t", 0},
		{"thoughts ignored", exportContent{ContentType: "thoughts", Parts: []any{"secret chain"}}, "", 0},
		{"reasoning_recap ignored", exportContent{ContentType: "reasoning_recap", Parts: []any{"recap"}}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, nonText := extractTextParts(tc.content)
			if text != tc.wantText || nonText != tc.wantNonText {
				t.Fatalf("got (%q, %d), want (%q, %d)", text, nonText, tc.wantText, tc.wantNonText)
			}
		})
	}
}

func TestBuildConversationFragmentRecordsNonTextParts(t *testing.T) {
	conv := exportConversation{
		ID:          "conv-mm",
		CurrentNode: "n2",
		Mapping: map[string]conversationNode{
			"n1": {ID: "n1", Message: exportMessage{ID: "m1", Author: exportAuthor{Role: "user"}, Content: exportContent{ContentType: "multimodal_text", Parts: []any{"look", map[string]any{"content_type": "image_asset_pointer"}}}}},
			"n2": {ID: "n2", Parent: "n1", Message: exportMessage{ID: "m2", Author: exportAuthor{Role: "assistant"}, Content: exportContent{ContentType: "text", Parts: []any{"ok"}}}},
		},
	}
	frag, ok := buildConversationFragment(conv, "f.json", "dir", exportUserMeta{}, &collectStats{})
	if !ok {
		t.Fatal("expected a fragment")
	}
	if !strings.Contains(frag.Content, "look") {
		t.Fatalf("multimodal text part missing: %q", frag.Content)
	}
	if frag.Metadata["non_text_parts_skipped"] != 1 {
		t.Fatalf("expected non_text_parts_skipped=1, got %#v", frag.Metadata["non_text_parts_skipped"])
	}
}
