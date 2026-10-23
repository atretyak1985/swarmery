package wtjanitor

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
)

// VerdictForceUnmerged is the journal verdict of an operator-approved removal
// of a keep-unmerged worktree (ForceUnmerged). Sweep never produces it.
const VerdictForceUnmerged Verdict = "force-unmerged"

// Force outcomes, one per requested path.
const (
	// ForceRemoved: salvage branch written, then the checkout removed.
	ForceRemoved = "removed"
	// ForceGone: the path is no longer a worktree of this repository (removed
	// by someone else since it was proposed). Reported, not an error.
	ForceGone = "gone"
	// ForceRefused: a veto held — main checkout, not an agent worktree, a
	// `git worktree lock`, or live. Nothing was touched.
	ForceRefused = "refused"
	// ForceFailed: salvage or removal failed. The worktree is still there.
	ForceFailed = "failed"
)

// ForceResult is what ForceUnmerged did with ONE requested path.
type ForceResult struct {
	Path          string
	Branch        string
	Outcome       string // removed | gone | refused | failed
	SalvageBranch string
	// Reason explains a refusal or a failure; "" on removed / gone.
	Reason string
}

// worktreeLister is the one Git read ForceUnmerged needs beyond Inspector.
// RepoGit implements it; a stubbed Inspector without it falls back to RepoGit.
type worktreeLister interface {
	List(repoRoot string) ([]Worktree, error)
}

// ForceUnmerged removes keep-unmerged worktrees the OPERATOR approved, one
// salvage branch each. It is reachable only from the worktree-cleanup apply
// path (POST /api/worktree-cleanup/{id}/apply, `swarmery wtcleanup apply`) —
// never from Sweep, whose verdicts and veto order it does not touch.
//
// Per path, in this order, every failure returning before the removal:
//
//  1. the path must still be a worktree of repoRoot (else ForceGone);
//  2. vetoes: main checkout, not agent-owned (owner.go), `git worktree lock`,
//     live process or session — each refuses and touches nothing;
//  3. salvage: everything in the checkout, uncommitted files included, is
//     committed onto refs/heads/salvage/<branch>-<ts> on top of its HEAD. A
//     salvage that fails ends the path as ForceFailed — no salvage, no removal;
//  4. removal: the checkout, and its branch when the janitor owns that
//     namespace (the salvage ref now holds every commit the branch had).
//
// Every attempted path is journalled in worktree_sweeps with verdict
// force-unmerged; a gone path is not (there was nothing to decide about).
func (s *Service) ForceUnmerged(repoRoot string, paths []string) []ForceResult {
	out := make([]ForceResult, 0, len(paths))
	var lister worktreeLister = RepoGit{}
	if l, ok := s.Git.(worktreeLister); ok {
		lister = l
	}
	wts, lerr := lister.List(repoRoot)
	if lerr != nil {
		for _, p := range paths {
			out = append(out, ForceResult{Path: p, Outcome: ForceFailed, Reason: "list worktrees: " + lerr.Error()})
		}
		return out
	}
	r := repo{id: s.projectID(repoRoot), path: repoRoot}
	now := s.clock()
	for _, p := range paths {
		out = append(out, s.forceOne(r, wts, p, now))
	}
	if s.Remover != nil {
		// Bookkeeping only: the removals above already happened.
		if perr := s.Remover.Prune(repoRoot); perr != nil {
			log.Printf("wtjanitor: prune %s: %v", repoRoot, perr)
		}
	}
	return out
}

func (s *Service) forceOne(r repo, wts []Worktree, path string, now time.Time) ForceResult {
	res := ForceResult{Path: path}
	wt, found := findWorktree(wts, path)
	if !found || !dirExists(wt.Path) {
		res.Outcome = ForceGone
		return res
	}
	res.Branch = wt.Branch

	refuse := func(reason string) ForceResult {
		res.Outcome, res.Reason = ForceRefused, reason
		s.journal(r, wt, Decision{VerdictForceUnmerged, "refused: " + reason}, "", false, nil)
		return res
	}
	switch {
	case wt.IsMain:
		// Not journalled, like Sweep: the main checkout is never a decision.
		res.Outcome, res.Reason = ForceRefused, "main checkout"
		return res
	case !agentOwned(r.path, s.DaemonRoot, wt.Path):
		res.Outcome, res.Reason = ForceRefused, "not an agent worktree"
		return res
	case wt.Locked:
		return refuse("locked (git worktree lock)")
	}
	if s.Live != nil {
		busy, berr := s.Live.Busy(wt.Path)
		if berr != nil {
			return refuse("liveness unknown: " + berr.Error())
		}
		if busy {
			return refuse("live process or session")
		}
	}

	branch, serr := salvageTo(r.path, wt.Path, wt.Branch, ForceSalvageBranchName(wt.Path, wt.Branch, now), now)
	if serr != nil {
		res.Outcome, res.Reason = ForceFailed, serr.Error()
		s.journal(r, wt, Decision{VerdictForceUnmerged, "salvage failed, worktree kept"}, "", false, serr)
		return res
	}
	res.SalvageBranch = branch

	// The salvage ref now reaches every commit the branch had, so deleting an
	// owned branch loses nothing; a branch outside the janitor's namespace is
	// left alone (RemoveWorktree would refuse it after the directory is gone).
	deleteBranch := wt.Branch != "" && ownsBranch(wt.Branch)
	reason := "operator-approved removal of a keep-unmerged worktree"
	if s.Remover == nil {
		res.Outcome, res.Reason = ForceFailed, "no remover configured"
		s.journal(r, wt, Decision{VerdictForceUnmerged, reason}, branch, false, fmt.Errorf("no remover configured"))
		return res
	}
	if rerr := s.Remover.RemoveWorktree(r.path, wt.Path, wt.Branch, deleteBranch); rerr != nil {
		res.Outcome, res.Reason = ForceFailed, rerr.Error()
		s.journal(r, wt, Decision{VerdictForceUnmerged, reason}, branch, false, rerr)
		return res
	}
	res.Outcome = ForceRemoved
	s.journal(r, wt, Decision{VerdictForceUnmerged, reason}, branch, true, nil)
	return res
}

// findWorktree matches path against git's list in every spelling (git prints
// /private/var/… on macOS where a caller may hold /var/…).
func findWorktree(wts []Worktree, path string) (Worktree, bool) {
	want := spellings(path)
	for _, wt := range wts {
		for _, a := range spellings(wt.Path) {
			for _, b := range want {
				if a == b {
					return wt, true
				}
			}
		}
	}
	return Worktree{}, false
}

// ForceSalvageBranchName is salvage/<branch>-<ts>, the branch flattened
// (slashes → dashes) so the rescue never nests under another namespace's
// prefix; a detached checkout uses its directory name. The timestamp is to the
// second: unlike Sweep's per-day name, a forced removal must never overwrite an
// earlier rescue.
func ForceSalvageBranchName(worktreePath, branch string, now time.Time) string {
	name := strings.TrimSpace(branch)
	if name == "" {
		name = filepath.Base(worktreePath)
	}
	name = strings.ReplaceAll(name, "/", "-")
	return "salvage/" + name + "-" + now.UTC().Format("20060102T150405Z")
}

// projectID resolves the journal's project_id; 0 (journalled as NULL) when the
// repository is not a known project.
func (s *Service) projectID(repoRoot string) int64 {
	if s.DB == nil {
		return 0
	}
	var id int64
	for _, p := range spellings(repoRoot) {
		if err := s.DB.QueryRow(`SELECT id FROM projects WHERE path = ?`, p).Scan(&id); err == nil {
			return id
		}
	}
	return 0
}
