package wtjanitor

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// DefaultMinIdle is the age floor a worktree must clear before it is eligible.
const DefaultMinIdle = 30 * time.Minute

// Inspector is the read side of one repository: enumerate its worktrees and
// observe the facts Classify needs.
type Inspector interface {
	Git
	Inspect(repoRoot string, live Liveness, owned func(path string) bool) ([]Worktree, error)
}

// Remover is the destructive boundary — separated so tests can assert that a
// vetoed or failed-salvage worktree is NEVER passed to it.
//
// RemoveWorktree takes the branch and a flag rather than leaving the caller to
// pair it with DeleteBranch: the checkout and its branch die together or not at
// all, and splitting that across two calls is exactly how a half-removed state
// (directory gone, branch left, journal claiming nothing happened) gets built.
type Remover interface {
	RemoveWorktree(repoRoot, path, branch string, deleteBranch bool) error
	DeleteBranch(repoRoot, branch string) error
	Prune(repoRoot string) error
}

// ownedBranchPrefixes are the branch namespaces the janitor may delete: the
// dispatcher's own, and the harness's isolated-subagent branches.
//
// salvage/ is deliberately absent — a rescue branch must survive the sweeper
// that created it — and so is everything else. This mirrors worktree.Manager's
// ErrRefusedBranch discipline: close the class at the boundary rather than
// trusting each caller to have built the name correctly. Without it a branch
// like `dev`, fully contained in HEAD and therefore "zero commits of its own",
// would qualify for deletion.
var ownedBranchPrefixes = []string{runcore.BranchPrefix, "worktree-agent-"}

// ownsBranch reports whether the janitor is allowed to delete this branch.
func ownsBranch(branch string) bool {
	for _, p := range ownedBranchPrefixes {
		if strings.HasPrefix(branch, p) {
			return true
		}
	}
	return false
}

// RealRemover adapts worktree.Manager, whose API is shaped for the acquire
// path: Remove takes an Acquired value (Path/Branch/StartPoint) rather than a
// bare path, and DeleteBranch returns (existed bool, err error).
//
// Branch deletion is NOT delegated. Manager owns the swarm/ namespace only and
// refuses anything outside it (ErrRefusedBranch) — correctly, since it must not
// delete branches it did not create. The janitor's namespace is wider: it also
// owns the harness's worktree-agent-* branches, which Manager has never heard
// of. Routing those through Manager.Remove(keepBranch=false) removes the
// DIRECTORY, then fails the branch guard and returns an error — leaving the
// checkout gone, the branch behind, and the journal claiming nothing happened.
// So the worktree always goes through Manager (for its leftover-directory
// repair) with keepBranch=true, and the branch goes through the guard above.
type RealRemover struct{ M *worktree.Manager }

// NewRealRemover wraps the manager dispatch/verify/phaserun already share.
func NewRealRemover(m *worktree.Manager) RealRemover { return RealRemover{M: m} }

func (r RealRemover) RemoveWorktree(repoRoot, path, branch string, deleteBranch bool) error {
	// keepBranch=true always: Manager's own branch guard would refuse the
	// harness namespace, and the directory removal must not depend on it.
	if err := r.M.Remove(repoRoot, worktree.Acquired{Path: path, Branch: branch}, true); err != nil {
		return err
	}
	if !deleteBranch || branch == "" {
		return nil
	}
	return r.DeleteBranch(repoRoot, branch)
}

// DeleteBranch removes a branch the janitor owns. Deleting is idempotent: git
// exits non-zero for a branch that is already gone, and a caller that just
// removed one has nothing to do about that.
func (r RealRemover) DeleteBranch(repoRoot, branch string) error {
	if !ownsBranch(branch) {
		return fmt.Errorf("wtjanitor: refusing to delete branch outside %v: %s", ownedBranchPrefixes, branch)
	}
	// No "is it checked out" probe here: git refuses to delete a checked-out
	// branch by itself, and both callers have already established otherwise —
	// sweepOne only reaches this after removing that very worktree, sweepBranches
	// only considers branches no worktree holds.
	if _, err := run(repoRoot, "branch", "-D", branch); err != nil {
		return fmt.Errorf("wtjanitor: delete branch %s: %w", branch, err)
	}
	return nil
}

func (r RealRemover) Prune(repoRoot string) error { return r.M.Prune(repoRoot) }

// Service runs sweeps. It owns no goroutine: the daemon's ticker calls Sweep.
type Service struct {
	DB   *sql.DB
	Git  Inspector
	Live Liveness
	// MinIdle is the age floor Classify enforces. A zero value means
	// DefaultMinIdle, never "no floor" — a zero floor would silently remove a
	// real safety guarantee.
	MinIdle time.Duration
	// OnlyRepo limits a sweep to one repository path ("" = every project).
	OnlyRepo string
	// DaemonRoot is worktree.Manager's root; checkouts under it are the
	// daemon's own and in scope. "" leaves only the harness's agent worktrees.
	DaemonRoot string
	Remover    Remover
	// now is a test seam.
	now func() time.Time
}

// Result counts one sweep for the caller's log line.
type Result struct {
	Inspected, Removed, Salvaged, Kept, Skipped, Errors int
}

type repo struct {
	id   int64
	path string
}

// Sweep processes every non-archived project once. dryRun decides everything and
// writes the journal but performs no destructive action.
func (s *Service) Sweep(dryRun bool) (Result, error) {
	var res Result
	repos, err := s.repos()
	if err != nil {
		return res, err
	}
	for _, r := range repos {
		owned := func(p string) bool { return agentOwned(r.path, s.DaemonRoot, p) }
		wts, ierr := s.Git.Inspect(r.path, s.Live, owned)
		if ierr != nil {
			log.Printf("wtjanitor: inspect %s: %v", r.path, ierr)
			res.Errors++
			continue
		}
		for _, wt := range wts {
			// Ownership is decided before anything else and is re-checked here
			// rather than trusted to the Inspector: a foreign worktree is never
			// classified, journalled or handed to the Remover. It stays in wts so
			// sweepBranches still sees its branch as checked out.
			if wt.Foreign || (!wt.IsMain && !owned(wt.Path)) {
				continue
			}
			res.Inspected++
			s.sweepOne(r, wt, dryRun, &res)
		}
		if !dryRun {
			s.sweepBranches(r, wts, &res)
			if perr := s.Remover.Prune(r.path); perr != nil {
				log.Printf("wtjanitor: prune %s: %v", r.path, perr)
			}
		}
	}
	return res, nil
}

// sweepOne decides and acts on ONE worktree. The order here IS the safety
// contract: classify, then re-check liveness, then salvage, and only then
// remove — with every failure path returning before the removal.
func (s *Service) sweepOne(r repo, wt Worktree, dryRun bool, res *Result) {
	now := s.clock()
	dec, err := Classify(r.path, wt, s.Git, now, s.minIdle())
	if err != nil {
		s.journal(r, wt, Decision{VerdictSkip, "classify failed"}, "", false, err)
		res.Errors++
		return
	}
	switch dec.Verdict {
	case VerdictSkip:
		res.Skipped++
		// The main checkout is skipped structurally, not judged: journalling it
		// would write one identical row per project per tick — 96 rows a day
		// saying nothing — and bury the decisions that matter. Every OTHER skip
		// (live, too young, lock held) IS informative and is recorded.
		if !wt.IsMain {
			s.journal(r, wt, dec, "", false, nil)
		}
		return
	case VerdictKeepUnmerged:
		res.Kept++
		s.journal(r, wt, dec, "", false, nil)
		return
	}
	if dryRun {
		s.journal(r, wt, dec, "", false, nil)
		return
	}

	// Classification and action are not atomic: a run may have started in the
	// meantime, and the whole point of the liveness veto is that it holds at the
	// moment of destruction, not at the moment of judgement.
	if fresh, lerr := s.Live.Busy(wt.Path); lerr == nil && fresh {
		res.Skipped++
		s.journal(r, wt, Decision{VerdictSkip, "became live before removal"}, "", false, nil)
		return
	}

	salvageBranch := ""
	if dec.Verdict == VerdictSalvage {
		b, serr := Salvage(r.path, wt.Path, wt.Branch, now)
		if serr != nil {
			// Salvage failed ⇒ removal is off the table. This is the single most
			// important branch in the package: it is what makes "the janitor
			// cannot be the reason work was lost" true rather than aspirational.
			res.Errors++
			s.journal(r, wt, Decision{VerdictKeepUnmerged, "salvage failed, worktree kept"}, "", false, serr)
			return
		}
		salvageBranch = b
		res.Salvaged++
	}

	// The branch goes with the worktree in ONE call, and only when it carries
	// nothing of its own. HasOwnCommits is already false here (that case
	// returned above); the check is belt-and-braces against a future reordering.
	deleteBranch := wt.Branch != "" && !wt.HasOwnCommits
	if rerr := s.Remover.RemoveWorktree(r.path, wt.Path, wt.Branch, deleteBranch); rerr != nil {
		res.Errors++
		s.journal(r, wt, dec, salvageBranch, false, rerr)
		return
	}
	res.Removed++
	s.journal(r, wt, dec, salvageBranch, true, nil)
}

// sweepBranches deletes residual local branches that no worktree checks out and
// that carry nothing of their own.
//
// salvage/* is deliberately absent from the prefix set: a rescue branch holds
// commits reachable from nothing else by construction, so it could never match
// the "no own commits" test anyway — but leaving it out makes it impossible for
// a future edit to that test to turn the janitor against its own rescues.
func (s *Service) sweepBranches(r repo, wts []Worktree, res *Result) {
	checkedOut := map[string]bool{}
	for _, wt := range wts {
		if wt.Branch != "" {
			checkedOut[wt.Branch] = true
		}
	}
	out, err := run(r.path, "branch", "--format=%(refname:short)")
	if err != nil {
		log.Printf("wtjanitor: list branches %s: %v", r.path, err)
		return
	}
	g, ok := s.Git.(interface {
		hasOwnCommits(repoRoot, branch string) (bool, error)
	})
	if !ok {
		return // stubbed Git in tests: branch sweeping is covered by its own test
	}
	for _, b := range strings.Split(out, "\n") {
		b = strings.TrimSpace(b)
		if b == "" || checkedOut[b] {
			continue
		}
		if !strings.HasPrefix(b, runcore.BranchPrefix) && !strings.HasPrefix(b, "worktree-agent-") {
			continue
		}
		own, oerr := g.hasOwnCommits(r.path, b)
		if oerr != nil || own {
			continue // unmerged work, or we could not prove otherwise
		}
		if derr := s.Remover.DeleteBranch(r.path, b); derr != nil {
			log.Printf("wtjanitor: delete branch %s: %v", b, derr)
			continue
		}
		res.Removed++
	}
}

func (s *Service) repos() ([]repo, error) {
	q := `SELECT id, path FROM projects WHERE archived = 0`
	args := []any{}
	if s.OnlyRepo != "" {
		q += ` AND path = ?`
		args = append(args, s.OnlyRepo)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repo
	for rows.Next() {
		var r repo
		if err := rows.Scan(&r.id, &r.path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// journal records one decision. A journal failure is logged, never returned: an
// unwritable audit row must not stop the sweep that already happened.
func (s *Service) journal(r repo, wt Worktree, dec Decision, salvageBranch string, removed bool, actErr error) {
	var branch, salvage, errStr any
	if wt.Branch != "" {
		branch = wt.Branch
	}
	if salvageBranch != "" {
		salvage = salvageBranch
	}
	if actErr != nil {
		errStr = actErr.Error()
	}
	if _, err := s.DB.Exec(
		`INSERT INTO worktree_sweeps (ts, project_id, path, branch, verdict, reason, salvage_branch, files, removed, error)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.clock().UTC().Format(time.RFC3339), r.id, wt.Path, branch, string(dec.Verdict), dec.Reason,
		salvage, len(wt.Dirty), boolInt(removed), errStr,
	); err != nil {
		log.Printf("wtjanitor: journal %s: %v", wt.Path, err)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) minIdle() time.Duration {
	if s.MinIdle > 0 {
		return s.MinIdle
	}
	return DefaultMinIdle
}
