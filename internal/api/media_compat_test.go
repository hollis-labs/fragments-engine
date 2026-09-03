package api

import (
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestLegacyAttachmentMediaPathCompatibility(t *testing.T) {
	item := domain.FragmentAttachment{
		SourcePath:         "/source/original.png",
		StoragePath:        "/published/original.png",
		PreviewStoragePath: "/published/preview.jpg",
	}
	if got := attachmentMediaPath(item, "preview"); got != item.PreviewStoragePath {
		t.Fatalf("preview path = %q", got)
	}
	if got := attachmentMediaPath(item, "original"); got != item.StoragePath {
		t.Fatalf("original path = %q", got)
	}
	item.PreviewStoragePath = ""
	if got := attachmentMediaPath(item, "preview"); got != item.StoragePath {
		t.Fatalf("preview fallback path = %q", got)
	}
	item.StoragePath = ""
	if got := attachmentMediaPath(item, "original"); got != item.SourcePath {
		t.Fatalf("source fallback path = %q", got)
	}
	if got := attachmentMediaPath(item, "arbitrary"); got != "" {
		t.Fatalf("unsafe variant selector returned %q", got)
	}
}
