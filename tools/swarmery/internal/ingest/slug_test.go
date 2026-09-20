package ingest

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeproj"
)

// TestSlugForPathIsTheDBSlugNotTheClaudeDirName pins the divergence between the
// two slugs on purpose, so the next reader who notices it does not "unify" them
// in the wrong direction.
//
// SlugForPath fills the projects.slug column and appears in URLs: it is project
// identity, and encoding '.' would rewrite that identity for every project
// living under a dot-directory. claudeproj.Slug is the directory name Claude
// Code itself writes under ~/.claude/projects: it is a LOOKUP key, and it must
// match what is on disk or the lookup silently finds nothing.
func TestSlugForPathIsTheDBSlugNotTheClaudeDirName(t *testing.T) {
	// A dot-free path is where the two agree — which is exactly why the
	// divergence went unnoticed until a dotted project turned up.
	const plain = "/Users/dev/src/acme"
	if got, want := SlugForPath(plain), claudeproj.Slug(plain); got != want {
		t.Fatalf("for a dot-free path the two encodings must agree: %q vs %q", got, want)
	}

	// A dotted path is where they must NOT agree. If this ever fails, someone
	// changed SlugForPath — read the comment on it before going further.
	const dotted = "/Users/dev/.local/src/acme"
	dbSlug := SlugForPath(dotted)
	if dbSlug != "-Users-dev-.local-src-acme" {
		t.Fatalf("SlugForPath(%q) = %q — the DB slug encodes '/' only", dotted, dbSlug)
	}
	dirName := claudeproj.Slug(dotted)
	if dirName != "-Users-dev--local-src-acme" {
		t.Fatalf("claudeproj.Slug(%q) = %q — the Claude Code dir name encodes '.' too", dotted, dirName)
	}
	if dbSlug == dirName {
		t.Fatal("the DB slug and the ~/.claude/projects directory name collapsed into one encoding")
	}
}
