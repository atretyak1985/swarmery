package worktree

import (
	"fmt"
	"log"
	"strings"
)

// CheckComplete refuses a checkout that is about to be handed out — warm reuse,
// idempotent re-attach, a fresh add's post-check, or the verifier about to grade
// it — while tracked files are missing from it against its own HEAD.
//
// The leftover it exists for is a `git worktree remove --force` killed part-way
// (Manager.Remove under checkoutTimeout, git.go): git deletes the working tree in
// readdir order BEFORE it drops .git/worktrees/<id>, so the checkout stays
// registered on its run branch with its index intact and a scattered subset of
// files gone (some top-level directories intact, others missing entirely) — and
// warm reuse matches it by branch. A `worktree add` killed mid-checkout is the
// secondary source: a sorted-prefix cut-off, possibly before any index was written.
//
// The probe is `git diff-index --diff-filter=D HEAD` run in the checkout:
//   - against HEAD, not the run's start point, so deletions the executor
//     COMMITTED are its work and never trip it;
//   - diff-index rather than `ls-files --deleted` or `git status`, because a
//     checkout killed before it wrote its index lists nothing in the index to be
//     missing, and diff-index still reports every HEAD path as deleted;
//   - plumbing, so it never refreshes or rewrites the index of a checkout it is
//     only inspecting;
//   - newline-separated `--name-only` output, one path per line, no rename pairs.
//
// The price is that an UNCOMMITTED deletion (a crashed run's `git rm` or `rm`)
// is refused too. Refusing costs the operator one look; handing out a half-deleted
// tree costs a whole run on code that does not build. A probe git cannot answer is
// a refusal as well: it proves nothing about the tree. CheckComplete never removes
// or repairs anything — what is left may hold uncommitted work.
func (m *Manager) CheckComplete(path string) error {
	out, err := m.Git.Run(path, "diff-index", "--name-only", "--diff-filter=D", "HEAD", "--")
	if err != nil {
		return fmt.Errorf("%w: %s: completeness probe failed: %v", ErrIncompleteCheckout, path, err)
	}
	var missing []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	log.Printf("warning: worktree: refusing to hand out %s: %d tracked file(s) missing against HEAD (first: %s)",
		path, len(missing), missing[0])
	return fmt.Errorf("%w: %s has %d tracked file(s) missing against HEAD (first: %s) — an interrupted teardown "+
		"or checkout leaves this; remove it (`git worktree remove --force %s`, after saving anything worth keeping) "+
		"and run again", ErrIncompleteCheckout, path, len(missing), missing[0], path)
}
