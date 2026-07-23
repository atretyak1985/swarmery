// Package githead resolves a repository's current HEAD commit without
// executing git: symbolic ref → loose ref file → packed-refs, plus the
// worktree case where .git is a "gitdir:" pointer file. Any parse failure
// degrades to ok=false — callers omit staleness info rather than guess.
package githead

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`) // sha1 or sha256

// Resolve returns the commit hash HEAD points at for the repo rooted at dir.
func Resolve(dir string) (string, bool) {
	gitDir := filepath.Join(dir, ".git")
	if fi, err := os.Stat(gitDir); err != nil {
		return "", false
	} else if !fi.IsDir() {
		b, err := os.ReadFile(gitDir) // worktree/submodule: "gitdir: <path>"
		if err != nil {
			return "", false
		}
		p := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		if p == "" {
			return "", false
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		gitDir = p
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	h := strings.TrimSpace(string(head))
	if shaRe.MatchString(h) {
		return h, true // detached
	}
	ref, ok := strings.CutPrefix(h, "ref: ")
	if !ok {
		return "", false
	}
	ref = strings.TrimSpace(ref)
	if b, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		s := strings.TrimSpace(string(b))
		if shaRe.MatchString(s) {
			return s, true
		}
		return "", false
	}
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(packed), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if sha, name, ok := strings.Cut(line, " "); ok && name == ref && shaRe.MatchString(sha) {
			return sha, true
		}
	}
	return "", false
}
