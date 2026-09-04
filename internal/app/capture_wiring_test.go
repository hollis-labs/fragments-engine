package app

import (
	"path/filepath"
	"testing"
)

func TestCaptureBlobRootIsSiblingOfFilesystemDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data", "fragments.db")
	got, err := captureBlobRoot(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(dbPath), ".fragments-engine-blobs")
	if got != want {
		t.Fatalf("blob root = %q, want %q", got, want)
	}
}

func TestCaptureBlobRootRejectsNonFilesystemDatabase(t *testing.T) {
	for _, value := range []string{":memory:", "file::memory:?cache=shared", "file:fragments.db"} {
		if _, err := captureBlobRoot(value); err == nil {
			t.Fatalf("captureBlobRoot(%q) unexpectedly succeeded", value)
		}
	}
}
