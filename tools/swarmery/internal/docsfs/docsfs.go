// Package docsfs embeds a build-time snapshot of the swarmery repo docs
// (ONBOARDING.md, EXTENDING.md, NEUTRALITY.md) for the /api/docs endpoints.
//
// `make build` and `make dev` copy the files from ../../docs into content/
// (the `copy-docs` target); the copies are gitignored, and the committed
// .gitkeep keeps the `all:content` embed pattern valid when they are absent
// (e.g. a fresh clone or CI) — in that case /api/docs serves an empty list.
package docsfs

import (
	"embed"
	"io/fs"
)

//go:embed all:content
var content embed.FS

// Content returns the embedded docs as a filesystem rooted at content/.
func Content() (fs.FS, error) {
	return fs.Sub(content, "content")
}
