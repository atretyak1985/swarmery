package mdfence

import (
	"fmt"
	"strings"
)

// GuardKind is the `kind:` value of a phase doc's guard block.
const GuardKind = "guard"

// Guard is a phase doc's `## Guard` block, written by the planner:
//
//	```yaml
//	kind: guard
//	protected_paths:
//	  - ".github/workflows/*.yml"
//	  - "**/*_test.go"
//	stop_if:
//	  - out-of-scope change
//	  - protected path
//	```
//
// ProtectedPaths are globs naming FILES the phase run must not edit; StopIf are
// the conditions under which the run stops and reports instead of carrying on.
type Guard struct {
	ProtectedPaths []string
	StopIf         []string
	// Warnings are lints that do not make the block unparsable — today only a
	// glob that names a directory instead of files (D22). The glob is still in
	// ProtectedPaths: dropping it would silently unprotect what the planner meant.
	Warnings []string
	// Line is the 1-based document line of the block's opening fence.
	Line int
}

// ParseGuard reads the first `kind: guard` yaml block of doc.
//
// It returns an ErrAbsent ParseError when the doc carries no such block, and an
// ErrUnparsable one — with the document line and the reason — when the block is
// there but is not valid yaml, is missing `protected_paths` or `stop_if`, or holds
// anything but a list of strings under either. Unknown keys are ignored. A second
// guard block is ignored with a warning: the first one is the phase's guard.
func ParseGuard(doc string) (Guard, *ParseError) {
	blocks := kindBlocks(doc, GuardKind)
	if len(blocks) == 0 {
		return Guard{}, &ParseError{Kind: ErrAbsent, Reason: "the doc carries no `kind: guard` block"}
	}
	b := blocks[0]
	m, perr := decodeMapping(b)
	if perr != nil {
		return Guard{}, perr
	}
	keys := mappingKeys(m)
	g := Guard{Line: b.Start + 1}
	for _, k := range []string{"protected_paths", "stop_if"} {
		if _, ok := keys[k]; !ok {
			return Guard{}, &ParseError{Kind: ErrUnparsable, Line: docLine(b, 1), Reason: "missing key " + k}
		}
	}
	if g.ProtectedPaths, perr = stringList(b, "protected_paths", keys["protected_paths"]); perr != nil {
		return Guard{}, perr
	}
	if g.StopIf, perr = stringList(b, "stop_if", keys["stop_if"]); perr != nil {
		return Guard{}, perr
	}
	for _, p := range g.ProtectedPaths {
		if NamesDirectory(p) {
			g.Warnings = append(g.Warnings, fmt.Sprintf(
				"protected path %q names a directory, not files — write the file pattern (e.g. %q)", p, fileGlobFor(p)))
		}
	}
	for _, extra := range blocks[1:] {
		g.Warnings = append(g.Warnings, fmt.Sprintf("a second `kind: guard` block at line %d is ignored", extra.Start+1))
	}
	return g, nil
}

// NamesDirectory reports whether a protected-path glob names a directory rather
// than files: it ends with "/" or its last segment is "**" ("scripts/tests/**").
// Globs name FILES (D22) — "scripts/tests/**/*" or ".github/workflows/*.yml".
func NamesDirectory(glob string) bool {
	g := strings.TrimSpace(glob)
	if g == "" {
		return false
	}
	if strings.HasSuffix(g, "/") {
		return true
	}
	last := g[strings.LastIndex(g, "/")+1:]
	return last == "**"
}

// fileGlobFor is the file-naming spelling of a directory glob, for the warning.
func fileGlobFor(glob string) string {
	g := strings.TrimSuffix(strings.TrimSpace(glob), "/")
	g = strings.TrimSuffix(g, "/**")
	if g == "**" || g == "" {
		return "**/*"
	}
	return g + "/**/*"
}
