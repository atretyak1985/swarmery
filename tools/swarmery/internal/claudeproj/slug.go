// Package claudeproj names the per-project directories Claude Code keeps under
// `<home>/.claude/projects`. It is a leaf: standard library only, so every
// package that needs to FIND a project's transcripts or auto-memory can depend
// on it without dragging a dependency graph behind it.
//
// This is deliberately NOT the same encoding as `ingest.SlugForPath`. There are
// two slugs in this codebase and they answer two different questions:
//
//	claudeproj.Slug      — the on-disk directory name Claude Code itself chose.
//	                       Encodes '/' AND '.' to '-'. Use it to LOCATE files
//	                       under ~/.claude/projects.
//	ingest.SlugForPath   — the daemon's own project identity, stored in the
//	                       projects.slug column and used in URLs. Encodes '/'
//	                       only, on purpose; changing it would rewrite identity.
//
// They coincide for any path without a dot, which is why the divergence stayed
// invisible: a project at `/Users/dev/.local/src/acme` is the first place they
// disagree, and there the ingest encoding points at a directory that does not
// exist.
package claudeproj

import (
	"path/filepath"
	"strings"
)

// Slug encodes an absolute path the way Claude Code names the per-project
// directory under `<home>/.claude/projects`: every `/` and every `.` becomes
// `-`. The leading slash encodes too, which is why every slug opens with `-`,
// and why a dot-directory produces a doubled `--` (a path such as
// `<home>/.config/x` encodes to `-…--config-x`).
//
// Verified against the real slug directories on the probe machine: for all 39
// that carried a transcript, re-encoding the `cwd` the transcript itself
// records reproduced the directory name exactly, with no exceptions.
//
// The path is cleaned first, so a trailing separator does not leak a trailing
// `-`. An empty path returns an empty slug rather than the `-` that
// filepath.Clean's "." would otherwise produce.
//
// Everything that is not `/` or `.` passes through byte-for-byte — spaces,
// underscores, non-ASCII. That is not an oversight and it is not a guess we get
// to refine: the only evidence for this encoding is the set of directory names
// Claude Code actually wrote, so a character no observed name exercises must be
// left alone until a real slug shows otherwise.
func Slug(path string) string {
	if path == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '.' {
			return '-'
		}
		return r
	}, filepath.Clean(path))
}
