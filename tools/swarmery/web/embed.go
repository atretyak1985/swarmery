// Package web embeds the built SPA (vite output in dist/).
// Run `make build` to produce a real bundle; the committed .gitkeep only
// keeps the embed pattern valid on a fresh clone.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built SPA as a filesystem rooted at dist/.
func Dist() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
