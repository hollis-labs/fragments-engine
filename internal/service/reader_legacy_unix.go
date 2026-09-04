//go:build darwin || linux

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openLegacyReaderFile anchors traversal at an already-open configured root
// and opens every descendant with O_NOFOLLOW. Intermediate path components
// therefore cannot be swapped to symlinks between validation and the final
// open. O_NONBLOCK prevents a malicious special file from blocking before the
// regular-file check.
func openLegacyReaderFile(root, storedPath string) (*os.File, error) {
	root = strings.TrimSpace(root)
	storedPath = strings.TrimSpace(storedPath)
	if root == "" || storedPath == "" {
		return nil, fmt.Errorf("legacy media root and path are required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, err
	}
	candidate := storedPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("legacy media path escapes configured root")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, filepath.Separator) {
			return nil, fmt.Errorf("legacy media path contains an invalid component")
		}
	}

	dirFD, err := unix.Open(rootReal, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open configured legacy media root: %w", err)
	}
	for _, part := range parts[:len(parts)-1] {
		nextFD, openErr := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(dirFD)
		if openErr != nil {
			return nil, fmt.Errorf("open legacy media directory: %w", openErr)
		}
		dirFD = nextFD
	}
	fileFD, openErr := unix.Openat(dirFD, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	_ = unix.Close(dirFD)
	if openErr != nil {
		return nil, fmt.Errorf("open legacy media file: %w", openErr)
	}
	file := os.NewFile(uintptr(fileFD), candidate)
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, fmt.Errorf("open legacy media file descriptor")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("legacy media path is not a regular file")
	}
	return file, nil
}
