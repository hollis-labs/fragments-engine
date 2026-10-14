package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// CollectionError is the only failure detail a transcript source may expose.
// The privacy boundary suppresses source errors because they can quote record
// content; this type carries just enough to locate a failure without it: a
// fixed error class, a hash of the source-relative path, and a line number and
// byte offset into that file. It never holds a path, a decoder message or any
// record text.
type CollectionError struct {
	Class    string
	PathHash string
	Line     int
	Offset   int64
}

func (e *CollectionError) Error() string {
	parts := []string{"class=" + e.Class}
	if e.PathHash != "" {
		parts = append(parts, "file="+e.PathHash)
	}
	if e.Line > 0 {
		parts = append(parts, fmt.Sprintf("line=%d", e.Line))
	}
	if e.Offset > 0 {
		parts = append(parts, fmt.Sprintf("offset=%d", e.Offset))
	}
	return strings.Join(parts, " ")
}

// HashPath returns a short stable digest of a source-relative path.
func HashPath(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:6])
}

// AttributeFile stamps the path hash on a CollectionError anywhere in err's
// chain. Other errors pass through unchanged.
func AttributeFile(err error, rel string) error {
	var ce *CollectionError
	if errors.As(err, &ce) && ce.PathHash == "" {
		ce.PathHash = HashPath(rel)
	}
	return err
}

type skipKey struct{}

// SkipCollector gathers inputs a source skipped without failing the run. It
// records only sanitized CollectionErrors, so a skip is always countable and
// attributable but never silent.
type SkipCollector struct {
	mu    sync.Mutex
	Skips []CollectionError
}

// WithSkipCollector returns a context whose NoteSkip calls land in c.
func WithSkipCollector(ctx context.Context, c *SkipCollector) context.Context {
	return context.WithValue(ctx, skipKey{}, c)
}

// NoteSkip records a per-file skip. Without a collector it is a no-op, which
// keeps a source usable outside the pipeline.
func NoteSkip(ctx context.Context, class, rel string) { NoteSkipAt(ctx, class, rel, 0, 0) }

// NoteSkipAt is NoteSkip with the position of the problem in the file: a line
// number and byte offset, so an operator can find it without the content.
func NoteSkipAt(ctx context.Context, class, rel string, line int, offset int64) {
	c, _ := ctx.Value(skipKey{}).(*SkipCollector)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.Skips = append(c.Skips, CollectionError{Class: class, PathHash: HashPath(rel), Line: line, Offset: offset})
	c.mu.Unlock()
}

// ByClass counts skips per class.
func (c *SkipCollector) ByClass() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for _, s := range c.Skips {
		out[s.Class]++
	}
	return out
}
