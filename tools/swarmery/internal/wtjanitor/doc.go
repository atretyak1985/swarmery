// Package wtjanitor removes agent worktrees nobody else will.
//
// Two producers create worktrees in a repository and neither sweeps on a
// schedule: the Claude Code harness makes <repo>/.claude/worktrees/agent-<hex>
// for an isolated subagent (and keeps it whenever it is dirty, because it
// cannot know the work was committed onto some other branch), and this daemon
// makes ~/.swarmery/worktrees/<project>/<task> for dispatch/verify/phaserun
// (internal/worktree), reclaimed only when a LATER task happens to want the
// same path. Residue therefore accumulates until a human notices.
//
// The janitor is the out-of-band sweeper. It discovers worktrees by asking git
// (`git worktree list --porcelain`), so both roots are found by one mechanism,
// and it decides per worktree:
//
//   - skip          — a veto fired; nothing was touched
//   - keep-unmerged — the branch carries commits reachable from no other ref
//   - redundant     — clean, or every dirty path's blob is already in git
//   - salvage       — holds content found nowhere in git
//
// # Scope: agent worktrees only
//
// Ownership is decided before any veto or verdict (owner.go). The janitor owns
// <repo>/.claude/worktrees/agent-<hex> and anything strictly below the daemon's
// worktree root, and nothing else. Every other worktree git reports — an
// operator's feature checkout above all, even one under .claude/worktrees with
// its branch pushed — is listed as Foreign so its branch still counts as
// checked out, and is otherwise invisible: not observed, not classified, not
// journalled, never removed. Clean with no commits of its own is "redundant"
// by content and can still be somebody's working copy.
//
// # Veto order is load-bearing
//
// Main checkout, then a `git worktree lock`, then live process/session, then a
// fresh index.lock, then the idle floor — each is checked before anything about
// the CONTENT is considered. A locked worktree, like the main checkout, is not
// even observed.
// A worktree someone paused in five minutes ago must not be removed just
// because its files happen to be committed elsewhere, and a live one must not
// even be inspected. Reordering these is a safety regression, not a style
// choice.
//
// # The invariant
//
// Removal is unreachable except through VerdictRedundant — proven by blob
// identity, never by name, mtime or similarity — or through a SUCCESSFUL
// salvage commit. A salvage that fails degrades the verdict and leaves the
// worktree alone. Nothing this package does can be the reason work was lost.
//
// # Operator-approved removal of keep-unmerged worktrees (force.go)
//
// keep-unmerged is never removed by a sweep. Service.ForceUnmerged is the one
// other path to removal, and it is operator-triggered ONLY: it is reachable
// from the worktree-cleanup apply action (POST
// /api/worktree-cleanup/{id}/apply, `swarmery wtcleanup apply`), after the
// operator ticked the paths on a Needs-you card that showed a proof the
// branch's content is already in main (internal/wtcleanup). Nothing on a timer
// calls it, and Sweep's verdicts and veto order are untouched by it.
//
// The same invariant holds there: per path it re-checks ownership, the
// `git worktree lock` and liveness at the moment of destruction, then writes a
// salvage branch salvage/<branch>-<ts> that reaches every commit of the branch
// and every uncommitted file, and only after that salvage SUCCEEDED removes the
// checkout. Each attempt is journalled with verdict force-unmerged.
//
// # Plugin install records
//
// A worktree where Claude Code installed or updated a plugin at project scope
// leaves a record keyed by its path in <config-dir>/plugins/installed_plugins.json,
// and removing the worktree does not remove the record. With Service.Plugins
// set, every sweep ends by pruning the records whose path is agent-owned (the
// same rule as above) AND no longer exists, across ~/.claude and every
// ~/.claude-* account dir (plugins.go). The file is rewritten atomically and
// only if Claude Code has not changed it since it was read.
package wtjanitor
