package domain

import "testing"

func TestStableMediaAssetIDUsesPositionIndependentIdentityPrecedence(t *testing.T) {
	providerA, _ := StableMediaAssetID("capture", "youtube", "video-1", "client:0", "https://one.example/signed")
	providerB, _ := StableMediaAssetID("capture", "youtube", "video-1", "client:9", "https://two.example/rotated")
	if providerA != providerB {
		t.Fatal("provider media ID must outrank locator and positional client key")
	}

	locatorA, _ := StableMediaAssetID("capture", "web", "", "generic:image:0", "https://cdn.example/image.jpg")
	locatorB, _ := StableMediaAssetID("capture", "web", "", "generic:image:7", "https://cdn.example/image.jpg")
	if locatorA != locatorB {
		t.Fatal("stable source locator must outrank positional client key")
	}

	fallbackA, _ := StableMediaAssetID("capture", "web", "", "client-stable-a", "")
	fallbackB, _ := StableMediaAssetID("capture", "web", "", "client-stable-b", "")
	if fallbackA == fallbackB {
		t.Fatal("source/client key must distinguish assets when stronger identity is absent")
	}
}

func TestStableMediaAssetIDExcludesKindAndOrder(t *testing.T) {
	image, _ := StableMediaAssetID("capture", "provider", "media-1", "client:0", "")
	video, _ := StableMediaAssetID("capture", "provider", "media-1", "client:99", "")
	if image != video {
		t.Fatal("logical asset identity unexpectedly depends on classification or position")
	}
}
