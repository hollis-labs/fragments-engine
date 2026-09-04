// Package blobstore provides the Fragments Engine content-addressed custody
// boundary. Callers persist only opaque storage handles, never arbitrary paths.
package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

type DigestMismatchError struct {
	Expected domain.ContentDigest
	Actual   domain.ContentDigest
}

func (e *DigestMismatchError) Error() string {
	return fmt.Sprintf("media digest mismatch: expected %s, got %s", e.Expected.Value, e.Actual.Value)
}

type PutResult struct {
	Blob    domain.MediaBlob
	Created bool
}

type FileStore struct {
	root string
	now  func() time.Time
}

func NewFileStore(root string) (*FileStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("blob store root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve blob store root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("create blob store root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve blob store root symlinks: %w", err)
	}
	return &FileStore{root: filepath.Clean(realRoot), now: time.Now}, nil
}

func (s *FileStore) Root() string { return s.root }

// Put streams bytes once into a private temporary file while computing their
// digest. Only verified bytes are hard-linked into the immutable final path;
// competing writers of identical bytes converge on the same file.
func (s *FileStore) Put(ctx context.Context, expected domain.ContentDigest, src io.Reader, retention domain.RetentionPolicy) (PutResult, error) {
	if src == nil {
		return PutResult{}, fmt.Errorf("put blob: source reader is required")
	}
	if !expected.Empty() {
		var err error
		expected, err = expected.Normalized()
		if err != nil {
			return PutResult{}, fmt.Errorf("put blob expected %w", err)
		}
	}
	if retention != domain.RetentionIndefinite && retention != domain.RetentionCache {
		return PutResult{}, fmt.Errorf("put blob: invalid retention %q", retention)
	}
	tempDir := filepath.Join(s.root, ".tmp")
	if err := ensureStoreDirectory(s.root, ".tmp"); err != nil {
		return PutResult{}, fmt.Errorf("create blob staging directory: %w", err)
	}
	temp, err := os.CreateTemp(tempDir, "incoming-*")
	if err != nil {
		return PutResult{}, fmt.Errorf("create blob staging file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	defer temp.Close()
	if err := temp.Chmod(0o600); err != nil {
		return PutResult{}, fmt.Errorf("secure blob staging file: %w", err)
	}

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, h), &contextReader{ctx: ctx, reader: src})
	if err != nil {
		return PutResult{}, fmt.Errorf("stream blob: %w", err)
	}
	actual := domain.ContentDigest{Algorithm: "sha256", Value: hex.EncodeToString(h.Sum(nil))}
	if !expected.Empty() && expected.Value != actual.Value {
		return PutResult{}, &DigestMismatchError{Expected: expected, Actual: actual}
	}
	if err := temp.Sync(); err != nil {
		return PutResult{}, fmt.Errorf("sync blob staging file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return PutResult{}, fmt.Errorf("close blob staging file: %w", err)
	}

	handle := storageHandle(actual.Value)
	finalPath, err := s.Resolve(handle)
	if err != nil {
		return PutResult{}, err
	}
	if err := ensureStoreDirectory(s.root, "sha256", prefix(actual.Value)); err != nil {
		return PutResult{}, fmt.Errorf("create blob digest directory: %w", err)
	}
	created := false
	if err := os.Link(tempPath, finalPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return PutResult{}, fmt.Errorf("atomically install blob: %w", err)
		}
		if err := verifyFile(finalPath, actual, size); err != nil {
			return PutResult{}, fmt.Errorf("verify reused blob: %w", err)
		}
	} else {
		created = true
	}
	stamp := s.now().UTC()
	return PutResult{Blob: domain.MediaBlob{
		Digest: actual, StorageHandle: handle, ByteSize: size,
		Retention: retention, CreatedAt: stamp, VerifiedAt: stamp,
	}, Created: created}, nil
}

func (s *FileStore) Open(handle string) (*os.File, error) {
	path, err := s.Resolve(handle)
	if err != nil {
		return nil, err
	}
	if err := requireRegularFile(path); err != nil {
		return nil, err
	}
	return os.Open(path)
}

// Remove is used only to compensate a failed database commit. It accepts the
// store-generated handle shape and cannot address a path outside the root.
func (s *FileStore) Remove(handle string) error {
	path, err := s.Resolve(handle)
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove symlink at blob storage handle")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *FileStore) Resolve(handle string) (string, error) {
	handle = filepath.ToSlash(strings.TrimSpace(handle))
	parts := strings.Split(handle, "/")
	if len(parts) != 3 || parts[0] != "sha256" || len(parts[1]) != 2 || parts[1] != prefix(parts[2]) {
		return "", fmt.Errorf("invalid blob storage handle")
	}
	if _, err := (domain.ContentDigest{Algorithm: "sha256", Value: parts[2]}).Normalized(); err != nil {
		return "", fmt.Errorf("invalid blob storage handle: %w", err)
	}
	path := filepath.Clean(filepath.Join(s.root, filepath.FromSlash(handle)))
	rel, err := filepath.Rel(s.root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("blob storage handle escapes root")
	}
	if err := validateExistingDirectories(s.root, parts[:2]...); err != nil {
		return "", err
	}
	return path, nil
}

func storageHandle(digest string) string {
	return "sha256/" + prefix(digest) + "/" + digest
}

func prefix(value string) string {
	if len(value) < 2 {
		return ""
	}
	return value[:2]
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

func verifyFile(path string, expected domain.ContentDigest, expectedSize int64) error {
	if err := requireRegularFile(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if size != expectedSize || hex.EncodeToString(h.Sum(nil)) != expected.Value {
		return fmt.Errorf("existing content does not match its digest path")
	}
	return nil
}

func ensureStoreDirectory(root string, parts ...string) error {
	current := root
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("blob store path component is not a real directory")
		}
	}
	return nil
}

func validateExistingDirectories(root string, parts ...string) error {
	current := root
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("blob store path component is not a real directory")
		}
	}
	return nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("blob storage handle does not name a regular file")
	}
	return nil
}
