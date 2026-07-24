package dispatch

import (
	"path"
	"strings"
)

// pathsOverlap reports whether two declared file scopes conflict, i.e. the two
// tasks must NOT run concurrently (Fusion scheduler.ts overlap gate, DESIGN.md
// §4.4). A scope is a list of path prefixes/globs relative to the repo root.
//
// Overlap rules:
//   - Empty scope means "undeclared" → Fusion treats it as GLOBAL: an empty
//     scope conflicts with ANY non-empty scope, and two empty scopes conflict
//     with each other. This is the conservative choice — a task that doesn't
//     say what it touches is assumed to touch everything. (The per-project
//     narrowing is applied by the caller, which only ever compares scopes of
//     tasks in the same project.)
//   - Two non-empty scopes conflict iff any entry of one overlaps any entry of
//     the other (entryOverlap): one is a prefix of the other, or a glob in one
//     matches a path in the other.
func pathsOverlap(a, b []string) bool {
	ca, cb := cleanScope(a), cleanScope(b)
	if len(ca) == 0 || len(cb) == 0 {
		// Undeclared scope on either side ⇒ global ⇒ always conflicts.
		return true
	}
	for _, pa := range ca {
		for _, pb := range cb {
			if entryOverlap(pa, pb) {
				return true
			}
		}
	}
	return false
}

// cleanScope normalizes a scope: trims blanks, drops empties, and cleans each
// path (collapsing "./", trailing slashes) so prefix comparison is stable.
func cleanScope(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		out = append(out, normalizePath(x))
	}
	return out
}

// normalizePath cleans a path/glob for comparison. It preserves glob
// metacharacters (path.Clean leaves *, ?, [] intact) while collapsing "." and
// redundant separators and stripping any trailing slash.
func normalizePath(p string) string {
	p = strings.TrimPrefix(p, "./")
	p = path.Clean(p)
	return strings.TrimSuffix(p, "/")
}

// entryOverlap reports whether two individual scope entries conflict. If either
// contains a glob metacharacter, the two are treated as overlapping when either
// matches the other's literal directory prefix (a glob is a superset of some
// paths, so we conservatively conflict on any glob-vs-prefix match). Otherwise
// it is a pure directory-prefix test in both directions.
func entryOverlap(a, b string) bool {
	if a == b {
		return true
	}
	aGlob, bGlob := isGlob(a), isGlob(b)
	switch {
	case aGlob && bGlob:
		// Two globs: conservatively conflict if their non-glob roots are
		// prefix-related (e.g. "src/**/*.ts" vs "src/api/*.go").
		return prefixRelated(globRoot(a), globRoot(b))
	case aGlob:
		return globMatches(a, b)
	case bGlob:
		return globMatches(b, a)
	default:
		return prefixRelated(a, b)
	}
}

// prefixRelated reports whether one path is a directory-prefix of the other
// (or equal). "src/api" relates to "src/api/handlers.go" and to "src/api", but
// NOT to "src/apiv2" — the boundary must fall on a path separator.
func prefixRelated(a, b string) bool {
	if a == b {
		return true
	}
	return isDirPrefix(a, b) || isDirPrefix(b, a)
}

// isDirPrefix reports whether parent is a directory-prefix of child: child
// equals parent, or child starts with parent + "/". "" is a prefix of anything
// (the repo root), matching the global-scope intuition.
func isDirPrefix(parent, child string) bool {
	if parent == "" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

// globMatches reports whether glob pattern g overlaps literal path p. It matches
// with path.Match at the full path, and — because path.Match does not let a
// single "*" cross separators — also treats a match against p's own directory
// prefixes as an overlap (so "src/*" conflicts with "src/api/x.go" via the
// "src" root), and treats the glob's literal root being a prefix of p (or vice
// versa) as an overlap.
func globMatches(g, p string) bool {
	if ok, _ := path.Match(g, p); ok {
		return true
	}
	root := globRoot(g)
	return prefixRelated(root, p)
}

// isGlob reports whether s contains a path.Match metacharacter.
func isGlob(s string) bool { return strings.ContainsAny(s, "*?[") }

// globRoot returns the leading path segments of a glob up to (not including)
// the first segment that contains a metacharacter. "src/api/*.go" → "src/api";
// "**/x" → "" (root). Used to reduce a glob to the directory subtree it can
// touch for conservative prefix comparison.
func globRoot(g string) string {
	segs := strings.Split(g, "/")
	keep := make([]string, 0, len(segs))
	for _, s := range segs {
		if isGlob(s) {
			break
		}
		keep = append(keep, s)
	}
	return strings.Join(keep, "/")
}
