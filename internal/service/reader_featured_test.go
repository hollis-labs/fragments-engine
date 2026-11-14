package service

import (
	"strings"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func featuredMedia(assetID, kind, role string, position int) capturecontract.ReaderMediaItem {
	return capturecontract.ReaderMediaItem{MediaAssetID: assetID, Kind: kind,
		Attachment: capturecontract.AttachmentRef{AttachmentID: assetID + "-ref", MediaAssetID: assetID, Role: role, Position: position}}
}

func featuredRevision(bodyRunes int) domain.FragmentRevision {
	revision := testReaderRevision("revision")
	revision.Content = strings.Repeat("a", bodyRunes)
	return revision
}

func TestReaderRendererKeepsArticlesWithImagesAsArticles(t *testing.T) {
	heroAndInline := []capturecontract.ReaderMediaItem{
		featuredMedia("inline-1", "image", "inline", 0),
		featuredMedia("hero-1", "image", "hero", 1),
	}
	if got := readerRenderer(featuredRevision(3000), heroAndInline); got != "article" {
		t.Fatalf("3000-char body with two images renderer=%q, want article", got)
	}
	id, count := readerFeaturedImage(heroAndInline)
	if id != "hero-1" || count != 2 {
		t.Fatalf("featured=%q count=%d, want hero-1 and 2 (hero wins over earlier position)", id, count)
	}

	three := []capturecontract.ReaderMediaItem{
		featuredMedia("a", "image", "gallery_item", 0),
		featuredMedia("b", "image", "gallery_item", 1),
		featuredMedia("c", "image", "gallery_item", 2),
	}
	if got := readerRenderer(featuredRevision(100), three); got != "gallery" {
		t.Fatalf("100-char body with three images renderer=%q, want gallery", got)
	}

	video := []capturecontract.ReaderMediaItem{
		featuredMedia("video-1", "video", "primary", 0),
		featuredMedia("poster-1", "image", "inline", 1),
		featuredMedia("poster-2", "image", "inline", 2),
	}
	for _, runes := range []int{0, 100, 3000} {
		if got := readerRenderer(featuredRevision(runes), video); got != "video" {
			t.Fatalf("primary video with %d-char body renderer=%q, want video", runes, got)
		}
	}

	if got := readerRenderer(featuredRevision(3000), nil); got != "article" {
		t.Fatalf("body without images renderer=%q, want article", got)
	}
	if id, count := readerFeaturedImage(nil); id != "" || count != 0 {
		t.Fatalf("no images: featured=%q count=%d", id, count)
	}
}

func TestReaderBodyThresholdIsTrimmedCharacters(t *testing.T) {
	two := []capturecontract.ReaderMediaItem{featuredMedia("a", "image", "inline", 0), featuredMedia("b", "image", "inline", 1)}
	revision := featuredRevision(279)
	if got := readerRenderer(revision, two); got != "gallery" {
		t.Fatalf("279 chars renderer=%q, want gallery", got)
	}
	revision.Content = "  \n" + strings.Repeat("é", 280) + "\n  "
	if got := readerRenderer(revision, two); got != "article" {
		t.Fatalf("280 characters (multibyte, padded) renderer=%q, want article", got)
	}
	revision.Content = strings.Repeat(" ", 400) + "short"
	if got := readerRenderer(revision, two); got != "gallery" {
		t.Fatalf("whitespace padding must not count as body, renderer=%q", got)
	}
}

func TestReaderFeaturedImageFallsBackToFirstByPositionAndIgnoresAuxiliary(t *testing.T) {
	media := []capturecontract.ReaderMediaItem{
		featuredMedia("poster", "image", "poster", 0),
		featuredMedia("late", "image", "inline", 5),
		featuredMedia("early", "image", "gallery_item", 2),
		featuredMedia("clip", "video", "inline", 1),
	}
	id, count := readerFeaturedImage(media)
	if id != "early" || count != 2 {
		t.Fatalf("featured=%q count=%d, want early and 2", id, count)
	}
}

func TestReaderFeaturedImageCountsAnAssetOnce(t *testing.T) {
	media := []capturecontract.ReaderMediaItem{
		featuredMedia("same", "image", "inline", 0),
		featuredMedia("same", "image", "hero", 1),
		featuredMedia("other", "image", "inline", 2),
	}
	id, count := readerFeaturedImage(media)
	if id != "same" || count != 2 {
		t.Fatalf("featured=%q count=%d, want same and 2 (one asset attached twice is one image)", id, count)
	}
	if id, count := readerFeaturedImage(media[:2]); id != "same" || count != 1 {
		t.Fatalf("one asset twice: featured=%q count=%d, want same and 1", id, count)
	}
}
