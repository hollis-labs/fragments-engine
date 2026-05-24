package service

import (
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ffs"
)

type PinterestCorpusWriter struct {
	root string
}

func NewPinterestCorpusWriter(root string) *PinterestCorpusWriter {
	return &PinterestCorpusWriter{root: strings.TrimSpace(root)}
}

func (w *PinterestCorpusWriter) Write(detail domain.FragmentDetail) (string, error) {
	if w == nil || strings.TrimSpace(w.root) == "" {
		return "", nil
	}
	result, err := ffs.WritePinterestPinBundle(w.root, detail)
	if err != nil {
		return "", err
	}
	return result.FragmentPath, nil
}
