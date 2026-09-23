package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors — errors.Is-able. The dispatcher (Phase 3) maps these to
// dispatch_error messages on the task row.
var (
	// ErrBranchBusy: swarm/<taskID> is checked out in a worktree at a DIFFERENT
	// path than the one we would hand this task. A live checkout owns the branch,
	// so the remedy is to find and release that worktree. No silent rename (Fusion
	// lesson) — the caller must resolve the conflict.
	//
	// Strictly the checked-out case: a branch that merely EXISTS is ErrBranchExists.
	// The two used to share this sentinel, and the message sent operators hunting for
	// a worktree that was not there — `git worktree list` showed nothing holding it.
	ErrBranchBusy = errors.New("worktree: task branch is busy in another worktree")
	// ErrBranchExists: swarm/<taskID> exists as a ref but NO worktree has it checked
	// out, so `git worktree add -b` collides on the name alone. Nothing has to be
	// released; the branch is either an empty leftover (ReclaimEmptyBranch deletes it
	// automatically) or it holds commits, which is a decision for the operator —
	// merge them or discard them. Different remedy from ErrBranchBusy, hence a
	// different sentinel.
	ErrBranchExists = errors.New("worktree: task branch already exists")
	// ErrPathOccupied: the worktree path is taken by a directory git does not know
	// about, and it could not be freed. Git reports this as `fatal: '<path>' already
	// exists` — the SAME "already exists" substring it uses for a taken branch name,
	// which is why the two used to collapse into ErrBranchExists. That mis-diagnosis
	// cost a real run (2026-07-30): the operator kept deleting a branch on the
	// daemon's advice while the actual blocker was a leftover directory, and since
	// `git worktree add -b` mints the branch BEFORE it validates the path, every
	// retry recreated the very branch it then blamed. Distinct blocker, distinct
	// remedy (free the path), distinct sentinel.
	ErrPathOccupied = errors.New("worktree: worktree path is occupied by a directory git does not track")
	// ErrRepoRootRefused: the computed worktree path equals or contains repoRoot
	// (or vice versa). A runtime invariant — never hand a task the repo root.
	ErrRepoRootRefused = errors.New("worktree: refusing to use a path inside the repo root")
	// ErrNotARepo: repoRoot is not a git repository / git could not operate on it.
	ErrNotARepo = errors.New("worktree: repoRoot is not a git repository")
	// ErrBranchCheckedOut: the branch is checked out in a live worktree. Reclaiming it
	// would yank a running task's checkout, so the caller must resolve it.
	ErrBranchCheckedOut = errors.New("worktree: branch is checked out in a worktree")
	// ErrBranchIsHead: refusing to reclaim the repo's currently checked-out branch. A
	// guard, not an expected condition — swarm/<taskID> is never the base branch.
	ErrBranchIsHead = errors.New("worktree: refusing to reclaim the repo's HEAD branch")
	// ErrRefusedBranch: the branch is outside the swarm/ namespace this package
	// owns. Reclaim and delete both end in `git branch -D`; refusing anything we did
	// not create closes the class at the boundary instead of trusting every caller
	// to build the name correctly (a `dev` fully contained in the current HEAD counts
	// as zero commits ahead and would otherwise be deleted).
	ErrRefusedBranch = errors.New("worktree: refusing to operate on a branch outside swarm/")
	// ErrDetachedHead: the repo has no checked-out branch, so there is no base to
	// measure a run branch against. Acquire tolerates this (it only needs a start
	// SHA, and resolveStartPoint falls back to HEAD's); reclaim must not, because
	// its answer authorizes a `branch -D`. A detached HEAD sitting ON the run
	// branch's own tip makes that fallback report 0 commits ahead, and the branch —
	// the only ref holding those commits — is then deleted. No base, no count.
	ErrDetachedHead = errors.New("worktree: refusing to reclaim a branch while the repo is on a detached HEAD")
)

// staleLockAge is how old a .git/worktrees/*/index.lock must be before the
// pre-acquisition sweep deletes it (Fusion FN-6988: a crashed run leaves a lock
// that blocks all future acquisitions).
const staleLockAge = 10 * time.Minute

// DefaultRoot is the base directory for worktrees when Manager.Root is empty.
const DefaultRoot = ".swarmery/worktrees"

// Manager owns worktree lifecycle for a set of tasks. Git is the (mockable)
// git boundary; Root is the base dir under which per-project/per-task worktrees
// are created.
type Manager struct {
	Git  Git
	Root string // base dir; default: <home>/.swarmery/worktrees (see resolveRoot)
	// now is injected in tests to make the stale-lock age check deterministic;
	// nil means time.Now.
	now func() time.Time
}

// Acquired describes a worktree handed to a task.
type Acquired struct {
	Path       string // <Root>/<projectSlug>/<taskID>
	Branch     string // "swarm/<taskID>"
	StartPoint string // resolved SHA the worktree was pinned to
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// resolveRoot returns Root or the default under $HOME.
func (m *Manager) resolveRoot() (string, error) {
	if m.Root != "" {
		return m.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("worktree: resolve home for default root: %w", err)
	}
	return filepath.Join(home, DefaultRoot), nil
}

// Path is the checkout Acquire derives for (projectSlug, taskID): the
// <root>/<slug>/<taskID> join, computed WITHOUT touching git or the filesystem.
// Acquire itself calls it, so there is exactly one derivation of this layout.
//
// It is exported for the caller that has to find a worktree it never acquired.
// Adoption (a run that outlived the daemon) holds no worktree.Acquired, and the
// plan doc LENT into that checkout is the only copy carrying the orphan's ticks
// and its Completion Report — so the adoption path has to name the path to copy
// it back. Re-spelling the join at the call site would be a second copy of this
// package's layout, which is the drift runcore's branch helpers exist to prevent
// on the branch side.
//
// It answers for a path that may not exist: whether anything is there is the
// caller's question, not this one's.
func (m *Manager) Path(projectSlug, taskID string) (string, error) {
	root, err := m.resolveRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, projectSlug, taskID), nil
}

// swarmPrefix namespaces every branch this package creates. It is also the
// boundary guard: reclaim and delete refuse anything outside it (ErrRefusedBranch).
const swarmPrefix = "swarm/"

// branchName is the deterministic branch for a task.
func branchName(taskID string) string { return swarmPrefix + taskID }

// taskIDForBranch is branchName's exact inverse: "" when branch is not one of
// ours, so callers get the namespace check and the id in one step.
//
// A remainder containing "/" is refused too, because this inverse is what
// authorizes ownsWorktreePath's single-component reasoning: that check compares
// filepath.Base(p) against the WHOLE task id and requires Dir(Dir(p)) == Root, so
// a multi-component id fails both silently — the leftover is then read as a
// foreign checkout and the C1 crash-leftover warm reuse quietly stops working for
// it. phaserun ("phase-<id>") and planrun ("plan-<id>") are single-component by
// construction, but dispatch passes an unvalidated ExternalID straight through;
// asserting the precondition where the inverse is computed makes that reuse
// provably safe instead of incidentally safe.
func taskIDForBranch(branch string) string {
	if !strings.HasPrefix(branch, swarmPrefix) {
		return ""
	}
	id := strings.TrimPrefix(branch, swarmPrefix)
	if strings.Contains(id, "/") {
		return ""
	}
	return id
}

// Acquire creates (or safely reuses) a worktree for taskID pinned to an
// explicit start point. It enforces invariants 1–6 (see the phase doc):
//  1. explicit startPoint — never ambient HEAD;
//  2. repo-root guard — runtime, not caller-trusted;
//  3. a branch conflict fails loudly — ErrBranchBusy when another worktree has it
//     checked out, ErrBranchExists when the name is merely taken;
//  4. reuse-or-reset — reuse only a branch-matched worktree, recreate on any
//     proven mismatch, never destroy on a transient probe failure;
//  5. stale-lock sweep before acquisition;
//  6. (trailer format lives in trailer.go).
func (m *Manager) Acquire(repoRoot, projectSlug, taskID string) (Acquired, error) {
	path, err := m.Path(projectSlug, taskID)
	if err != nil {
		return Acquired{}, err
	}
	branch := branchName(taskID)

	// Invariant 2: repo-root guard (runtime). Evaluate symlinks on both sides so
	// a symlinked Root that resolves inside the repo is still caught.
	if err := guardRepoRoot(repoRoot, path); err != nil {
		return Acquired{}, err
	}

	// repoRoot must be a git repo — resolve the default-branch tip up front so a
	// non-repo fails fast with ErrNotARepo (invariant 1 needs this SHA anyway).
	startSHA, err := m.resolveStartPoint(repoRoot)
	if err != nil {
		return Acquired{}, err
	}

	// Invariant 5: stale-lock sweep + prune before touching worktrees.
	m.sweepStaleLocks(repoRoot)
	// prune is best-effort: a failure here should not abort acquisition, but a
	// non-repo would already have failed above.
	_, _ = m.Git.Run(repoRoot, "worktree", "prune")

	// Invariant 4 + 3: decide reuse / reclaim / conflict from the worktree list.
	list, listErr := m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
	if listErr != nil {
		// Transient probe failure: one retry, then error — NEVER destroy on a
		// flaky signal (Fusion lines 185-191).
		list, listErr = m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
		if listErr != nil {
			return Acquired{}, fmt.Errorf("worktree: list worktrees: %w", listErr)
		}
	}
	entries := parseWorktreeList(list)

	// Invariant 3: the branch is checked out in some OTHER path → busy.
	if other, ok := entries.pathForBranch(branch); ok && !samePath(other, path) {
		return Acquired{}, fmt.Errorf("%w: %s is on %s", ErrBranchBusy, other, branch)
	}

	// Invariant 4: our path already registered?
	if reg, ok := entries.byPath(path); ok {
		if reg.branch == branch {
			// Branch-matched → warm reuse as-is. Still worth a sync pass: the
			// project's untracked .claude/ files may have changed (or been added)
			// since this worktree was first acquired.
			syncUntrackedConfig(repoRoot, path)
			// Same reasoning for the dependency tree: a warm worktree may predate
			// the source checkout's first install, or have had its link removed by
			// a run that reinstalled.
			lendDependencies(repoRoot, path)
			return Acquired{Path: path, Branch: branch, StartPoint: startSHA}, nil
		}
		// Foreign branch / detached at our path → reclaim in place: remove then
		// recreate below.
		if _, err := m.Git.Run(repoRoot, "worktree", "remove", "--force", path); err != nil {
			return Acquired{}, fmt.Errorf("worktree: reclaim %s: %w", path, err)
		}
	} else if dirExists(path) {
		// Path exists on disk but is not a registered worktree (crash leftover,
		// archive→restore). Reclaim: prune already ran; force-remove clears any
		// stale registration, then recreate.
		_, _ = m.Git.Run(repoRoot, "worktree", "remove", "--force", path)
		// That call is a no-op when NOTHING is registered for the path — git refuses
		// it outright ("is not a working tree"), so the directory survives and the
		// `worktree add` below dies on the path instead. Free the name here, where
		// the state is known, rather than letting git's ambiguous message decide.
		if dirExists(path) {
			if err := m.freeOrphanPath(path, taskID); err != nil {
				return Acquired{}, err
			}
		}
	}

	// Invariant 1: create pinned to the explicit start SHA. Never a bare
	// `git worktree add <path>` (ambient HEAD may sit on a sibling task branch).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Acquired{}, fmt.Errorf("worktree: mkdir base %s: %w", filepath.Dir(path), err)
	}
	// `git worktree add -b` creates the branch ref BEFORE it validates the target
	// path, so a failure below can leave behind a branch nothing holds — one more
	// per retry, each of them then reported as the blocker. Whether the ref is ours
	// to roll back is only knowable BEFORE the add.
	branchExisted, branchProbeErr := m.branchExists(repoRoot, branch)

	if out, err := m.Git.Run(repoRoot, "worktree", "add", "-b", branch, path, startSHA); err != nil {
		// Roll back a ref this failed add just minted — never one that predates it
		// (that branch may hold the only copy of a crashed run's commits).
		if !branchExisted && branchProbeErr == nil {
			if minted, probeErr := m.branchExists(repoRoot, branch); probeErr == nil && minted {
				_, _ = m.Git.Run(repoRoot, "branch", "-D", branch)
			}
		}
		// Git says "already exists" for two unrelated blockers. Discriminate on the
		// half of each message that is NOT shared, most specific first.
		switch {
		case strings.Contains(out, "a branch named"):
			// The branch exists but is not checked out anywhere. It is NOT busy — the
			// list probe above already proved no worktree holds it — so it gets its own
			// sentinel and a message that does not send the reader looking for a
			// checkout that does not exist.
			return Acquired{}, fmt.Errorf("%w: %s (no worktree holds it — merge or delete it)", ErrBranchExists, branch)
		case strings.Contains(out, "already exists"):
			// The target path. freeOrphanPath above handles the leftover we own, so
			// reaching here means something else holds the name (a file, a foreign
			// directory, a permission wall) — say so instead of blaming the branch.
			return Acquired{}, fmt.Errorf("%w: %s", ErrPathOccupied, path)
		}
		return Acquired{}, fmt.Errorf("worktree: add %s: %w", path, err)
	}
	// The worktree now has whatever repoRoot had COMMITTED — lend it whatever
	// repoRoot has UNCOMMITTED too (issue #192), before handing it to a caller:
	// the untracked .claude/ config, and the gitignored dependency tree without
	// which the run's own verification command cannot execute.
	syncUntrackedConfig(repoRoot, path)
	lendDependencies(repoRoot, path)
	return Acquired{Path: path, Branch: branch, StartPoint: startSHA}, nil
}

// orphanSuffix marks a leftover directory freeOrphanPath moved aside. The leaf of
// such a path is never a bare taskID, so a later Acquire cannot mistake a
// quarantined leftover for the worktree it owns.
const orphanSuffix = ".orphaned-"

// freeOrphanPath frees the worktree path when a directory git does not track sits
// on it — the state an external `git worktree prune` leaves when the directory
// outlives its registration, and the one that made a retry a permanent dead end on
// 2026-07-30.
//
// An EMPTY leftover is deleted: there is nothing to preserve and moving it aside
// would litter Root for ever. A NON-EMPTY one is renamed to
// `<path>.orphaned-<UTC stamp>`, because in that incident the sole file in the dead
// directory was an uncommitted test — freeing the path must never mean destroying
// the work. Refuses with ErrPathOccupied for anything outside the
// <Root>/<slug>/<taskID> shape this manager owns: this function deletes or moves a
// directory, and the namespace guard is what keeps that from ever aiming at one we
// did not create.
func (m *Manager) freeOrphanPath(path, taskID string) error {
	if !m.ownsWorktreePath(path, taskID) {
		return fmt.Errorf("%w: %s (outside the worktree root this manager owns)", ErrPathOccupied, path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrPathOccupied, path, err)
	}
	if len(entries) == 0 {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrPathOccupied, path, err)
		}
		return nil
	}
	stamp := m.clock().UTC().Format("20060102-150405")
	dest := path + orphanSuffix + stamp
	// Two frees inside one second must not collide — and a bounded search keeps a
	// pathological Root from turning this into an unbounded loop.
	for i := 2; dirExists(dest); i++ {
		if i > 9 {
			return fmt.Errorf("%w: %s (no free quarantine name next to it)", ErrPathOccupied, path)
		}
		dest = fmt.Sprintf("%s%s%s-%d", path, orphanSuffix, stamp, i)
	}
	if err := os.Rename(path, dest); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrPathOccupied, path, err)
	}
	return nil
}

// clearUntrackedLeftover completes a teardown whose `git worktree remove` failed.
// It succeeds for exactly one failure — the one that is not a real problem: git has
// nothing registered for the path, so it could not have removed it, and whatever is
// on disk is a leftover this manager owns. Anything git DOES track is left alone
// and the caller's original error stands; deleting a live registration behind git's
// back would strand .git/worktrees metadata.
func (m *Manager) clearUntrackedLeftover(repoRoot string, a Acquired) error {
	taskID := taskIDForBranch(a.Branch)
	if taskID == "" {
		return fmt.Errorf("%w: %q cannot prove ownership of %s", ErrRefusedBranch, a.Branch, a.Path)
	}
	list, err := m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("worktree: list worktrees: %w", err)
	}
	if _, registered := parseWorktreeList(list).byPath(a.Path); registered {
		return fmt.Errorf("worktree: %s is still registered — git's removal failure is real", a.Path)
	}
	if !dirExists(a.Path) {
		return nil // git had nothing to remove and nothing is left behind
	}
	return m.freeOrphanPath(a.Path, taskID)
}

// resolveStartPoint returns the SHA of the repo's DEFAULT-branch tip, resolved
// in repoRoot's context — NOT the ambient checkout HEAD (invariant 1). If the
// repo sits on another branch during recovery, we still pin to the default
// branch tip. Falls back to HEAD only when the default branch cannot be
// determined (bare repo edge cases).
func (m *Manager) resolveStartPoint(repoRoot string) (string, error) {
	// Determine the default branch name from the current symbolic HEAD; if the
	// checkout is detached or the ref is missing, fall back to HEAD's SHA.
	def, err := m.Git.Run(repoRoot, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		// Not a repo at all? rev-parse will confirm; otherwise detached HEAD.
		sha, headErr := m.Git.Run(repoRoot, "rev-parse", "HEAD")
		if headErr != nil {
			return "", fmt.Errorf("%w: %v", ErrNotARepo, headErr)
		}
		return strings.TrimSpace(sha), nil
	}
	def = strings.TrimSpace(def)
	sha, err := m.Git.Run(repoRoot, "rev-parse", "refs/heads/"+def)
	if err != nil {
		return "", fmt.Errorf("worktree: resolve tip of %s: %w", def, err)
	}
	return strings.TrimSpace(sha), nil
}

// Remove tears down an acquired worktree. keepBranch preserves swarm/<taskID>
// (e.g. so its commits remain reachable for merge); otherwise the branch is
// deleted too.
func (m *Manager) Remove(repoRoot string, a Acquired, keepBranch bool) error {
	if _, err := m.Git.Run(repoRoot, "worktree", "remove", "--force", a.Path); err != nil {
		// A teardown that leaves the directory behind is what makes the NEXT Acquire
		// fail — git refuses `worktree remove` on a path it does not track, and the
		// leftover then collides with `worktree add`. That is how one externally
		// pruned worktree cost a plan every retry it had (2026-07-30). Finish the job
		// when git had nothing to remove, deciding from state rather than from git's
		// message; a genuine failure still surfaces, now with both causes attached.
		if freeErr := m.clearUntrackedLeftover(repoRoot, a); freeErr != nil {
			return fmt.Errorf("worktree: remove %s: %w", a.Path, errors.Join(err, freeErr))
		}
	}
	if !keepBranch && a.Branch != "" {
		// The last `git branch -D` in this package that does NOT go through
		// checkBranchReclaimable. Acquired is an exported struct any caller can
		// construct, and a.Branch is trusted verbatim, so the namespace class is
		// closed here at the boundary too rather than by assuming every caller
		// built the name through Acquire.
		if taskIDForBranch(a.Branch) == "" {
			return fmt.Errorf("%w: %s", ErrRefusedBranch, a.Branch)
		}
		if _, err := m.Git.Run(repoRoot, "branch", "-D", a.Branch); err != nil {
			return fmt.Errorf("worktree: delete branch %s: %w", a.Branch, err)
		}
	}
	return nil
}

// branchExists probes refs/heads/<branch>, distinguishing "the ref is not there"
// from "git could not answer".
//
// `rev-parse --verify --quiet` exits non-zero with NO output when a ref simply
// does not exist, and prints a diagnostic ("fatal: not a git repository", a lock
// error, a permission error) when git itself is unhappy. `show-ref --verify
// --quiet` cannot tell the two apart, and collapsing both to "missing" made
// DeleteBranch return nil for a delete that never happened — DeleteRunBranch then
// logged success and the UI cleared a dirty-branch banner on a no-op.
func (m *Manager) branchExists(repoRoot, branch string) (bool, error) {
	out, err := m.Git.Run(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if strings.TrimSpace(out) == "" {
		return false, nil // the ref is absent — nothing to do, not an error
	}
	return false, fmt.Errorf("worktree: probe branch %s: %w: %s", branch, err, strings.TrimSpace(out))
}

// checkBranchReclaimable reports whether branch exists and is safe to delete:
// it must live in the swarm/ namespace, must not be the repo's HEAD branch, and
// must not be checked out in a worktree. A missing branch is (false, nil) —
// nothing to do, not an error; a probe git could not answer is an error.
//
// reuseOwn relaxes the last check for the ONE checkout that is not a conflict:
// the worktree this manager itself would hand the branch's own task
// (<Root>/<projectSlug>/<taskID>, see ownsWorktreePath). A daemon crash leaves
// exactly that — registered, on its own branch, un-prunable because the directory
// survives — and Acquire recovers it by warm reuse (invariant 4). Reporting it as
// ErrBranchCheckedOut made phaserun.Start bail before it ever reached that
// Acquire, so no phase could be retried after a restart. With reuseOwn the branch
// is reported as nothing-to-reclaim (false, nil): there is no name to free, and
// no `branch -D` is attempted — git refuses that on a live checkout anyway.
func (m *Manager) checkBranchReclaimable(repoRoot, branch string, reuseOwn bool) (bool, error) {
	taskID := taskIDForBranch(branch)
	if taskID == "" {
		return false, fmt.Errorf("%w: %s is not a %s<taskID> run branch", ErrRefusedBranch, branch, swarmPrefix)
	}
	exists, err := m.branchExists(repoRoot, branch)
	if err != nil || !exists {
		return false, err
	}
	if head, err := m.Git.Run(repoRoot, "symbolic-ref", "--short", "HEAD"); err == nil {
		if strings.TrimSpace(head) == branch {
			return false, fmt.Errorf("%w: %s", ErrBranchIsHead, branch)
		}
	}
	// A stale registration must not look like a live checkout — prune first (the
	// same best-effort posture Acquire uses).
	_, _ = m.Git.Run(repoRoot, "worktree", "prune")
	list, err := m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("worktree: list worktrees: %w", err)
	}
	if path, ok := parseWorktreeList(list).pathForBranch(branch); ok {
		if reuseOwn && m.ownsWorktreePath(path, taskID) {
			return false, nil // our own leftover — Acquire warm-reuses it
		}
		return false, fmt.Errorf("%w: %s is on %s", ErrBranchCheckedOut, path, branch)
	}
	return true, nil
}

// ownsWorktreePath reports whether p is the worktree THIS manager creates for
// taskID: <Root>/<projectSlug>/<taskID>, the same shape Acquire computes.
//
// The projectSlug component is deliberately not pinned. Only Acquire knows which
// slug a given caller will pass, and threading it down would change
// ReclaimEmptyBranch/DeleteBranch — hence dispatch.WorktreeManager — for no
// safety: a leftover under a DIFFERENT slug is one Acquire will reject as
// ErrBranchBusy a moment later (its pathForBranch check compares against the slug
// it was actually given), so tolerating it here loses nothing and still fails
// loudly. Anything outside Root, or whose leaf is another task, stays a conflict.
func (m *Manager) ownsWorktreePath(p, taskID string) bool {
	root, err := m.resolveRoot()
	if err != nil {
		return false
	}
	p = filepath.Clean(p)
	if filepath.Base(p) != taskID {
		return false
	}
	return samePath(filepath.Dir(filepath.Dir(p)), root)
}

// OwnCheckoutOf reports the worktree path holding branch when that worktree is
// one THIS manager created for the branch's own task (<Root>/<slug>/<taskID>),
// and ok=false otherwise — no such checkout, a foreign one, or git unreadable.
//
// It exists for the READ-ONLY diagnosis side. A branch with commits means two very
// different things: a crash leftover checked out at the run's own path retries
// fine (Acquire warm-reuses it) and cannot be deleted (`git branch -D` refuses a
// live checkout), whereas the same branch anywhere else genuinely blocks a retry.
// Telling the user to "merge or delete it" is wrong advice for the first case, and
// the distinction already lives here in ownsWorktreePath — so diagnosis borrows it
// instead of re-deriving this package's path layout somewhere else, where the two
// copies would drift the first time the layout changes.
//
// Read-only and best-effort by construction: it prunes nothing, deletes nothing,
// and any git failure is reported as "not ours" so a diagnosis can never fail
// because git was unhappy.
func (m *Manager) OwnCheckoutOf(repoRoot, branch string) (string, bool) {
	taskID := taskIDForBranch(branch)
	if taskID == "" {
		return "", false
	}
	list, err := m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false
	}
	path, ok := parseWorktreeList(list).pathForBranch(branch)
	if !ok || !m.ownsWorktreePath(path, taskID) {
		return "", false
	}
	return path, true
}

// reclaimBase resolves the tip a reclaim measures against: the repo's CHECKED-OUT
// branch, via symbolic-ref and nothing else.
//
// It deliberately does NOT call resolveStartPoint, whose HEAD fallback is
// acquire-oriented — the right answer for pinning a new worktree, the wrong one for
// authorizing a delete. A repo detached at swarm/<taskID>'s own tip resolves that
// fallback to the branch's own SHA, `rev-list --count <thatSHA>..refs/heads/<branch>`
// answers 0 ("empty"), and the only ref holding those commits is force-deleted.
// phasediag.baseBranch already refuses to guess a base on a detached HEAD and drops
// every branch-derived blocker; the destructive path must be at least as careful as
// the read-only one, so it refuses too (ErrDetachedHead). No base, no count, no delete.
func (m *Manager) reclaimBase(repoRoot string) (string, error) {
	def, err := m.Git.Run(repoRoot, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrDetachedHead, strings.TrimSpace(def))
	}
	def = strings.TrimSpace(def)
	if def == "" {
		return "", fmt.Errorf("%w: symbolic-ref answered with an empty branch name", ErrDetachedHead)
	}
	sha, err := m.Git.Run(repoRoot, "rev-parse", "refs/heads/"+def)
	if err != nil {
		return "", fmt.Errorf("worktree: resolve tip of %s: %w", def, err)
	}
	return strings.TrimSpace(sha), nil
}

// ReclaimEmptyBranch deletes branch when it exists and holds no commits ahead of the
// repo's base branch, so a re-run can re-acquire the deterministic name swarm/<taskID>
// instead of dying on ErrBranchExists. The base is the repo's checked-out branch
// (reclaimBase) — the same signal Acquire pins to whenever there IS one, and an
// ErrDetachedHead refusal when there is not.
//
// Returns the number of commits ahead when the branch HAS work: the branch is left
// untouched and the caller must not destroy it. Returns 0 when the branch was deleted,
// never existed, or is still checked out in THIS task's own worktree — the crash
// leftover Acquire recovers by warm reuse, where there is nothing to reclaim and the
// commits (if any) are continued rather than reported as blocking.
func (m *Manager) ReclaimEmptyBranch(repoRoot, branch string) (int, error) {
	exists, err := m.checkBranchReclaimable(repoRoot, branch, true /* reuseOwn */)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil // nothing to reclaim
	}
	base, err := m.reclaimBase(repoRoot)
	if err != nil {
		return 0, err
	}
	out, err := m.Git.Run(repoRoot, "rev-list", "--count", base+"..refs/heads/"+branch)
	if err != nil {
		return 0, fmt.Errorf("worktree: count commits on %s: %w", branch, err)
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("worktree: unparseable commit count %q for %s: %w", strings.TrimSpace(out), branch, err)
	}
	// A negative count is not a count. Atoi accepts "-1" happily and `ahead > 0` is
	// false for it, so without this guard nonsense output authorizes a `branch -D`
	// — the single outcome an unusable count must never buy.
	if ahead < 0 {
		return 0, fmt.Errorf("worktree: negative commit count %d for %s", ahead, branch)
	}
	if ahead > 0 {
		// The branch carries work — never destroyed implicitly; the caller decides.
		return ahead, nil
	}
	if _, err := m.Git.Run(repoRoot, "branch", "-D", branch); err != nil {
		return 0, fmt.Errorf("worktree: delete branch %s: %w", branch, err)
	}
	return 0, nil
}

// DeleteBranch force-deletes branch (git branch -D), refusing while it is checked
// out in ANY worktree (including one of ours) or is the repo's HEAD branch, and
// refusing anything outside the swarm/ namespace. Unlike ReclaimEmptyBranch this
// DOES discard commits — it exists only for an explicit user decision.
//
// reuseOwn is deliberately false: this call ends in a `git branch -D`, which git
// itself refuses on a live checkout, so tolerating our own leftover would only
// trade a sentinel naming the holding worktree for a raw git failure. The user's
// way out is to retry the phase (Acquire warm-reuses and the run's teardown
// removes the worktree) or remove that worktree by hand.
//
// existed reports whether a branch was actually deleted. Deleting is idempotent —
// a missing branch is (false, nil), NOT an error — so an error alone cannot tell
// a real deletion from a no-op, and every caller with only `err` to read claimed
// "deleted" for both. The probe that answers this already runs inside
// checkBranchReclaimable, so reporting it costs nothing and removes the duplicate
// rev-parse callers were doing to reconstruct it.
func (m *Manager) DeleteBranch(repoRoot, branch string) (existed bool, err error) {
	exists, err := m.checkBranchReclaimable(repoRoot, branch, false /* reuseOwn */)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil // already gone — deleting is idempotent
	}
	if _, err := m.Git.Run(repoRoot, "branch", "-D", branch); err != nil {
		return false, fmt.Errorf("worktree: delete branch %s: %w", branch, err)
	}
	return true, nil
}

// Prune runs `git worktree prune` and sweeps stale index.lock files — the same
// recovery the acquisition path does, exposed for a periodic reaper.
func (m *Manager) Prune(repoRoot string) error {
	m.sweepStaleLocks(repoRoot)
	if _, err := m.Git.Run(repoRoot, "worktree", "prune"); err != nil {
		return fmt.Errorf("worktree: prune: %w", err)
	}
	return nil
}

// sweepStaleLocks deletes <repoRoot>/.git/worktrees/*/index.lock files older
// than staleLockAge (Fusion FN-6988). Best-effort: unreadable dirs are skipped.
func (m *Manager) sweepStaleLocks(repoRoot string) {
	base := filepath.Join(repoRoot, ".git", "worktrees")
	dirs, err := os.ReadDir(base)
	if err != nil {
		return // no worktrees registered yet, or .git is a file (submodule) — fine
	}
	cutoff := m.clock().Add(-staleLockAge)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		lock := filepath.Join(base, d.Name(), "index.lock")
		info, err := os.Stat(lock)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(lock)
		}
	}
}

// guardRepoRoot implements invariant 2: reject a worktree path that equals or
// nests with repoRoot in either direction, after resolving symlinks.
func guardRepoRoot(repoRoot, path string) error {
	rr := evalOrClean(repoRoot)
	p := evalOrClean(path)
	if samePath(rr, p) || isSubpath(rr, p) || isSubpath(p, rr) {
		return fmt.Errorf("%w: path=%s repoRoot=%s", ErrRepoRootRefused, path, repoRoot)
	}
	return nil
}

// evalOrClean resolves symlinks in p. The worktree path usually does not exist
// yet, so EvalSymlinks would fail on the whole path; instead we resolve the
// deepest EXISTING ancestor (so a symlinked repoRoot like macOS /var →
// /private/var is canonicalized consistently on both sides of the guard) and
// re-append the not-yet-created tail lexically.
func evalOrClean(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	dir := p
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached the root; nothing existed
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			// `dir` is the deepest non-existent component; its own basename
			// must be re-appended along with the accumulated tail, otherwise
			// the path collapses to the resolved ancestor and the repo-root
			// guard over-matches (e.g. repoRoot "/tmp/repo" → "/tmp").
			parts := append([]string{resolved, filepath.Base(dir)}, tail...)
			return filepath.Clean(filepath.Join(parts...))
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
	}
	return p
}

// isSubpath reports whether child is strictly inside parent.
func isSubpath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// samePath compares two paths after resolving symlinks in their deepest
// existing ancestor. `git worktree list` reports canonicalized paths (e.g.
// macOS /var → /private/var), while our computed paths are not resolved; a
// plain filepath.Clean compare would then miss a legitimate warm-reuse match
// and mis-fire ErrBranchBusy on the same task. evalOrClean canonicalizes both.
func samePath(a, b string) bool { return evalOrClean(a) == evalOrClean(b) }

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// --- git worktree list --porcelain parsing ---------------------------------

type worktreeEntry struct {
	path   string
	branch string // short branch name ("swarm/T-x"), empty if detached
}

type worktreeEntries []worktreeEntry

// parseWorktreeList parses `git worktree list --porcelain`. Each record is a
// "worktree <path>" line optionally followed by "branch refs/heads/<name>"
// (absent for a detached HEAD), records separated by blank lines.
func parseWorktreeList(out string) worktreeEntries {
	var entries worktreeEntries
	var cur *worktreeEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur != nil {
				entries = append(entries, *cur)
			}
			cur = &worktreeEntry{path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			if cur != nil {
				ref := strings.TrimPrefix(line, "branch ")
				cur.branch = strings.TrimPrefix(ref, "refs/heads/")
			}
		case line == "":
			if cur != nil {
				entries = append(entries, *cur)
				cur = nil
			}
		}
	}
	if cur != nil {
		entries = append(entries, *cur)
	}
	return entries
}

func (e worktreeEntries) byPath(path string) (worktreeEntry, bool) {
	for _, w := range e {
		if samePath(w.path, path) {
			return w, true
		}
	}
	return worktreeEntry{}, false
}

func (e worktreeEntries) pathForBranch(branch string) (string, bool) {
	for _, w := range e {
		if w.branch == branch {
			return w.path, true
		}
	}
	return "", false
}
