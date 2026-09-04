package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestFileStoreDigestMismatchLeavesNoDurableFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	wrong := sha256.Sum256([]byte("different"))
	_, err = store.Put(context.Background(), domain.ContentDigest{Algorithm: "sha256", Value: hex.EncodeToString(wrong[:])}, bytes.NewBufferString("payload"), domain.RetentionIndefinite)
	var mismatch *DigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected DigestMismatchError, got %v", err)
	}
	assertNoRegularFiles(t, root)
}

func TestFileStoreConcurrentIdenticalContentReusesOneBlob(t *testing.T) {
	root := t.TempDir()
	first, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("shared immutable bytes")
	sum := sha256.Sum256(payload)
	expected := domain.ContentDigest{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])}
	type outcome struct {
		result PutResult
		err    error
	}
	results := make(chan outcome, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := first
			if i%2 == 1 {
				store = second
			}
			result, err := store.Put(context.Background(), expected, bytes.NewReader(payload), domain.RetentionIndefinite)
			results <- outcome{result, err}
		}(i)
	}
	wg.Wait()
	close(results)
	created := 0
	handle := ""
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if item.result.Created {
			created++
		}
		if handle == "" {
			handle = item.result.Blob.StorageHandle
		} else if handle != item.result.Blob.StorageHandle {
			t.Fatalf("writers did not converge: %q != %q", handle, item.result.Blob.StorageHandle)
		}
	}
	if created != 1 {
		t.Fatalf("created count = %d, want 1", created)
	}
	path, err := first.Resolve(handle)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("stored bytes = %q, %v", got, err)
	}
}

func TestFileStoreRejectsPathEscapeHandles(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range []string{"../../outside", "/tmp/outside", "sha256/aa/../../outside", "sha256/aa/not-a-digest"} {
		if _, err := store.Resolve(handle); err == nil {
			t.Fatalf("Resolve(%q) accepted an unsafe handle", handle)
		}
		if err := store.Remove(handle); err == nil {
			t.Fatalf("Remove(%q) accepted an unsafe handle", handle)
		}
	}
}

func TestFileStoreRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sha256")); err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(context.Background(), domain.ContentDigest{}, bytes.NewBufferString("must stay inside"), domain.RetentionIndefinite)
	if err == nil {
		t.Fatal("blob store followed a symlink outside its root")
	}
	assertNoRegularFiles(t, outside)
}

func assertNoRegularFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			t.Fatalf("unexpected durable file after failed put: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
