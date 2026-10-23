package wtjanitor

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Salvage commits everything in a doomed worktree onto
// refs/heads/salvage/<name>-<date> and returns the branch name.
//
// `git add -A` here mutates ONLY this worktree's own index — each worktree has
// its own — and the worktree is about to be deleted, so there is nothing to
// preserve. The repository's main checkout, its index and its HEAD are never
// touched: the ref is created with update-ref, not by checking anything out.
// Parallel sessions in the same repository therefore see nothing but a new ref
// appearing, which is the whole reason this is plumbing and not a commit.
//
// Every failure returns an error and NO branch, and the caller must treat that
// as "do not remove". A salvage that fails is the one thing that turns a
// removal back into a keep.
func Salvage(repoRoot, worktreePath, origBranch string, now time.Time) (string, error) {
	return salvageTo(repoRoot, worktreePath, origBranch, SalvageBranchName(worktreePath, now), now)
}

// salvageTo is Salvage with the rescue branch named by the caller: Sweep's
// per-day name, or ForceUnmerged's per-second one.
func salvageTo(repoRoot, worktreePath, origBranch, branch string, now time.Time) (string, error) {
	if _, err := run(worktreePath, "add", "-A"); err != nil {
		return "", fmt.Errorf("salvage: stage: %w", err)
	}
	tree, err := run(worktreePath, "write-tree")
	if err != nil {
		return "", fmt.Errorf("salvage: write-tree: %w", err)
	}
	head, err := run(worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("salvage: rev-parse HEAD: %w", err)
	}
	msg := fmt.Sprintf(
		"chore(salvage): rescue %s\n\nSwept by the swarmery worktree janitor at %s.\nOrigin path: %s\nOrigin branch: %s\n",
		filepath.Base(worktreePath), now.UTC().Format(time.RFC3339), worktreePath, orDetached(origBranch))
	commit, err := run(worktreePath, "commit-tree", tree, "-p", head, "-m", msg)
	if err != nil {
		return "", fmt.Errorf("salvage: commit-tree: %w", err)
	}
	// The empty old value makes update-ref refuse when the ref already exists:
	// an earlier rescue under the same name is never overwritten — the salvage
	// fails, and a failed salvage keeps the worktree.
	if _, err := run(repoRoot, "update-ref", "refs/heads/"+branch, commit, ""); err != nil {
		return "", fmt.Errorf("salvage: update-ref %s: %w", branch, err)
	}
	return branch, nil
}

// SalvageBranchName is deterministic per (worktree, day) so retries do not
// litter the branch list. salvageTo never overwrites an existing ref, so a
// same-day retry after a salvaged-but-not-removed sweep keeps the worktree
// until the next day's name is free.
func SalvageBranchName(worktreePath string, now time.Time) string {
	return "salvage/" + filepath.Base(worktreePath) + "-" + now.UTC().Format("20060102")
}

func orDetached(b string) string {
	if strings.TrimSpace(b) == "" {
		return "(detached)"
	}
	return b
}
