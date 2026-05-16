package sysop

import (
	"embed"
	"io/fs"
)

// distFS embeds the sysop production bundle from apps/sysop/dist.
//
//go:embed all:dist
var distFS embed.FS

func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
