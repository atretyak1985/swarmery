package claudeacct

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wtPath is <home>/.swarmery/worktrees/<slug-of-src>/<task>.
func wtPath(home, src, task string) string {
	return filepath.Join(home, ".swarmery", "worktrees", strings.ReplaceAll(src, "/", "-"), task)
}

// linkWorktree writes the pairing `git worktree add` leaves behind: <wt>/.git
// naming <src>/.git/worktrees/<name>, and that admin dir's gitdir file pointing
// back at <wt>/.git.
func linkWorktree(t *testing.T, src, wt, name string) {
	t.Helper()
	admin := filepath.Join(src, ".git", "worktrees", name)
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(wt, ".git")+"\n")
}

// The primary mapping: the worktree's own .git file names the source checkout,
// unambiguously, even where the slug would not decode.
func TestSourceCheckout_GitdirMapping(t *testing.T) {
	home := fakeHome(t)
	src := filepath.Join(home, "projects", "acme", "tools", "sprint-report")
	// A decoy that makes the slug ambiguous: "tools/sprint/report" also exists.
	mkdirs(t, src, filepath.Join(home, "projects", "acme", "tools", "sprint", "report"))
	wt := wtPath(home, src, "phase-9")
	linkWorktree(t, src, wt, "phase-9")

	if got := sourceCheckout(wt); got != src {
		t.Fatalf("sourceCheckout(worktree) = %q, want %q", got, src)
	}
	if got, want := sourceCheckout(filepath.Join(wt, "a", "b")), filepath.Join(src, "a", "b"); got != want {
		t.Fatalf("sourceCheckout(worktree/a/b) = %q, want %q", got, want)
	}
	// A gitdir naming a directory that does not exist maps to nothing — and the
	// slug fallback is then ambiguous, so still nothing.
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: /nowhere/at/all/.git/worktrees/phase-9\n")
	if got := sourceCheckout(wt); got != "" {
		t.Fatalf("sourceCheckout with a dangling gitdir and an ambiguous slug = %q, want \"\"", got)
	}
	// A .git with no gitdir line and no worktrees separator.
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+src+"\n")
	if got := sourceFromGitFile(wt); got != "" {
		t.Fatalf("sourceFromGitFile without the worktrees separator = %q, want \"\"", got)
	}
}

// S3: a .git FILE is just bytes in the worktree. A gitdir line naming a
// checkout git does not pair with this worktree is not believed — no .git
// directory there, no back-pointer, or a back-pointer naming another worktree —
// and the mapping falls back to the (here unambiguous) slug, never to the
// directory the forged line named.
func TestSourceFromGitFile_RequiresGitsBackPointer(t *testing.T) {
	home := fakeHome(t)
	src := filepath.Join(home, "projects", "acme", "repo")
	victim := filepath.Join(home, "projects", "victim")
	mkdirs(t, src, victim)
	wt := wtPath(home, src, "phase-3")
	linkWorktree(t, src, wt, "phase-3")
	if got := sourceFromGitFile(wt); got != src {
		t.Fatalf("paired worktree: sourceFromGitFile = %q, want %q", got, src)
	}

	// 1. The named checkout has no .git directory at all.
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(victim, ".git", "worktrees", "phase-3")+"\n")
	if got := sourceFromGitFile(wt); got != "" {
		t.Fatalf("gitdir naming a checkout with no .git dir = %q, want \"\"", got)
	}
	// 2. A .git directory, but no admin dir / gitdir back-pointer for <n>.
	mkdirs(t, filepath.Join(victim, ".git"))
	if got := sourceFromGitFile(wt); got != "" {
		t.Fatalf("gitdir with no back-pointer = %q, want \"\"", got)
	}
	// 3. A back-pointer that names a DIFFERENT worktree's .git.
	writeFile(t, filepath.Join(victim, ".git", "worktrees", "phase-3", "gitdir"), filepath.Join(home, "elsewhere", ".git")+"\n")
	if got := sourceFromGitFile(wt); got != "" {
		t.Fatalf("gitdir whose back-pointer names another worktree = %q, want \"\"", got)
	}
	// Resolution falls back to the slug — the real source — not the victim.
	if got := sourceCheckout(wt); got != src {
		t.Fatalf("sourceCheckout after a forged .git = %q, want the slug's %q", got, src)
	}
	// 4. A traversal in the worktree name is refused outright.
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(src, ".git", "worktrees")+"/../x\n")
	if got := sourceFromGitFile(wt); got != "" {
		t.Fatalf("gitdir with a traversing name = %q, want \"\"", got)
	}
	// 5. A back-pointer reached through a symlinked spelling still pairs.
	link := filepath.Join(home, "wt-link")
	if err := os.Symlink(wt, link); err != nil {
		t.Fatal(err)
	}
	linkWorktree(t, src, wt, "phase-3")
	writeFile(t, filepath.Join(src, ".git", "worktrees", "phase-3", "gitdir"), filepath.Join(link, ".git")+"\n")
	if got := sourceFromGitFile(wt); got != src {
		t.Fatalf("back-pointer via a symlinked spelling = %q, want %q", got, src)
	}
}

// No .git at all: the slug fallback decodes when exactly one candidate exists.
func TestSourceCheckout_AbsentGitFallsBackToAnUnambiguousSlug(t *testing.T) {
	home := fakeHome(t)
	src := filepath.Join(home, "projects", "acme", "tools", "sprint-report")
	mkdirs(t, src)
	wt := wtPath(home, src, "phase-1")
	mkdirs(t, wt)
	if got := sourceCheckout(wt); got != src {
		t.Fatalf("sourceCheckout via slug = %q, want %q", got, src)
	}
}

// An ambiguous slug is REFUSED: a guessed estate is worse than none.
func TestSourceCheckout_AmbiguousSlugRefused(t *testing.T) {
	home := fakeHome(t)
	src := filepath.Join(home, "projects", "acme", "tools", "sprint-report")
	mkdirs(t, src, filepath.Join(home, "projects", "acme", "tools", "sprint", "report"))
	wt := wtPath(home, src, "phase-2")
	mkdirs(t, wt)
	if got := sourceCheckout(wt); got != "" {
		t.Fatalf("sourceCheckout with two decodings = %q, want \"\"", got)
	}
	// And a slug with more separators than the bound is refused unsearched.
	if got := sourceFromSlug(strings.Repeat("-a", maxSlugDashes+1)); got != "" {
		t.Fatalf("sourceFromSlug over the bound = %q", got)
	}
	if got := sourceFromSlug("no-leading-dash"); got != "" {
		t.Fatalf("sourceFromSlug of a relative slug = %q", got)
	}
}

// Anything that is not a worktree maps to "".
func TestSourceCheckout_NonWorktreePath(t *testing.T) {
	home := fakeHome(t)
	for _, p := range []string{
		filepath.Join(home, "projects", "acme"),
		filepath.Join(home, ".swarmery"),
		filepath.Join(home, ".swarmery", "worktrees"),
		filepath.Join(home, ".swarmery", "worktrees", "-only-a-slug"),
		filepath.Join(home, ".swarmery", "worktrees-not"),
		"/elsewhere/entirely",
	} {
		if got := sourceCheckout(p); got != "" {
			t.Errorf("sourceCheckout(%q) = %q, want \"\"", p, got)
		}
	}
}
