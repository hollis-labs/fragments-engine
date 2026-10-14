package transcript

import (
	"errors"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// Sources on the private path (chatgpt_export among them) must not hand the
// store attachments; the sanitizer stays fail-closed if one does.
func TestSanitizeRefusesFragmentsCarryingAttachments(t *testing.T) {
	for _, source := range []string{"chatgpt", "claude", "codex"} {
		in := domain.PipelineFragment{
			Source:      source,
			SourceID:    "conv",
			Content:     "safe",
			Attachments: []domain.PipelineAttachment{{Name: "doc.pdf", SourcePath: "/tmp/doc.pdf"}},
		}
		if _, _, err := sanitize(in); !errors.Is(err, ErrAttachments) {
			t.Fatalf("%s: sanitize accepted attachments: %v", source, err)
		}
	}
	in := domain.PipelineFragment{Source: "chatgpt", SourceID: "conv", Content: "safe"}
	if _, _, err := sanitize(in); err != nil {
		t.Fatalf("sanitize rejected an attachment-free fragment: %v", err)
	}
}
