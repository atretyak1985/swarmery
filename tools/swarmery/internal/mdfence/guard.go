package mdfence

import (
	"fmt"
	"strconv"
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
//	allow:
//	  - "pkg/export/writer_test.go"
//	```
//
// ProtectedPaths are globs naming FILES the phase run must not edit; StopIf are
// the conditions under which the run stops and reports instead of carrying on.
// Allow is optional: paths (or globs) the operator let back into scope by
// answering a guard decision `allow-add-to-scope` (AddGuardAllow) — a protected
// path that also matches an allow entry may be edited.
type Guard struct {
	ProtectedPaths []string
	StopIf         []string
	Allow          []string
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
	if n, ok := keys[GuardAllowKey]; ok {
		if g.Allow, perr = stringList(b, GuardAllowKey, n); perr != nil {
			return Guard{}, perr
		}
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

// GuardAllowKey is the guard block's optional exclusion list.
const GuardAllowKey = "allow"

// AddGuardAllow returns doc with paths added to its guard block's `allow:` list:
// the existing entries first, then every new one not already there, each written
// as a double-quoted scalar. The rest of the document — the block's other keys
// and comments included — is left byte for byte as it was.
//
// The guard must parse: an absent block has nothing to widen and an unparsable
// one cannot be edited safely, so both return the ParseError.
func AddGuardAllow(doc string, paths []string) (string, *ParseError) {
	g, perr := ParseGuard(doc)
	if perr != nil {
		return "", perr
	}
	merged := append([]string(nil), g.Allow...)
	seen := make(map[string]bool, len(merged))
	for _, p := range merged {
		seen[p] = true
	}
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			merged = append(merged, p)
		}
	}
	b := kindBlocks(doc, GuardKind)[0]
	lines := strings.Split(doc, "\n")
	body := strings.Split(b.Content, "\n")
	closing := b.Start + 1 + len(body) // the closing fence's index; == len(lines) when unclosed

	// Drop the block's own `allow:` key — its line and everything indented
	// under it — then append the merged list at the end of the block.
	kept := make([]string, 0, len(body)+len(merged)+1)
	inAllow := false
	for _, l := range body {
		top := l != "" && l[0] != ' ' && l[0] != '\t' && l[0] != '#' && l[0] != '-'
		if top {
			inAllow = strings.HasPrefix(l, GuardAllowKey+":")
		}
		if !inAllow {
			kept = append(kept, l)
		}
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	kept = append(kept, GuardAllowKey+":")
	for _, p := range merged {
		kept = append(kept, "  - "+strconv.Quote(p))
	}
	out := make([]string, 0, len(lines)+len(merged)+1)
	out = append(out, lines[:b.Start+1]...)
	out = append(out, kept...)
	if closing < len(lines) {
		out = append(out, lines[closing:]...)
	}
	return strings.Join(out, "\n"), nil
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
