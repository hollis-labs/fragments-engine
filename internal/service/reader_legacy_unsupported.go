//go:build !darwin && !linux

package service

import (
	"fmt"
	"os"
)

func openLegacyReaderFile(_, _ string) (*os.File, error) {
	return nil, fmt.Errorf("legacy media serving is disabled on this platform")
}
