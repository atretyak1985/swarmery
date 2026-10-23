package wtcleanup

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

// Decisions stored in worktree_cleanup_proposals.decided.
const (
	DecidedRemoved = "removed"
	DecidedGone    = "gone"
)

// Apply outcomes beyond wtjanitor's removed / gone / refused / failed.
const (
	OutcomeAlready       = "already-decided" // the proposal was decided before; nothing done
	OutcomeNotProposed   = "not-proposed"    // no proposal for this path in this repository
	OutcomeNoProof       = "no-proof"        // proof_kind none: counted, never removed
	OutcomeBranchChanged = "branch-changed"  // the checkout holds another branch than proposed
	OutcomeHeadChanged   = "head-changed"    // the checkout's HEAD moved since the proof was computed
	OutcomeOwnerActive   = "owner-active"    // the owner is running again, reopened, or no longer known
	OutcomeRepoError     = "repo-error"      // git could not list the repository's worktrees: nothing done
)

// ErrNotFound: the proposal id named in an apply does not exist.
var ErrNotFound = errors.New("wtcleanup: no such proposal")

// ProposeSummary is one repository's outcome of Propose.
type ProposeSummary struct {
	RepoRoot     string `json:"repoRoot"`
	ProjectSlug  string `json:"projectSlug"`
	WithProof    int    `json:"withProof"`    // open proposals the card lists
	WithoutProof int    `json:"withoutProof"` // open proposals only counted
	Added        int    `json:"added"`
	Refreshed    int    `json:"refreshed"`
	Dropped      int    `json:"dropped"` // open proposals no longer candidates
}

// Propose stores a scan as the open proposals the Needs-you card is derived
// from. Per repository, in one transaction: a candidate already proposed is
// refreshed in place (same id, same proposed_at — the card's identity and age
// stay stable), a new one is inserted, and an open proposal that is no longer a
// candidate is dropped. Decided rows are history and never touched. A
// repository with open proposals but no candidates this pass loses them all.
func Propose(db *sql.DB, scan Scan, now time.Time) ([]ProposeSummary, error) {
	byRepo := map[string][]Candidate{}
	for _, c := range scan.Candidates {
		byRepo[c.RepoRoot] = append(byRepo[c.RepoRoot], c)
	}
	rows, err := db.Query(`SELECT DISTINCT repo_root FROM worktree_cleanup_proposals WHERE decided IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("wtcleanup: open proposals: %w", err)
	}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := byRepo[r]; !ok {
			byRepo[r] = nil
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	out := make([]ProposeSummary, 0, len(repos))
	for _, r := range repos {
		sum, err := proposeRepo(db, r, byRepo[r], now)
		if err != nil {
			return out, err
		}
		out = append(out, sum)
	}
	return out, nil
}

func proposeRepo(db *sql.DB, repoRoot string, cands []Candidate, now time.Time) (ProposeSummary, error) {
	sum := ProposeSummary{RepoRoot: repoRoot}
	tx, err := db.Begin()
	if err != nil {
		return sum, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	open := map[string]int64{}
	rows, err := tx.Query(`SELECT id, path FROM worktree_cleanup_proposals WHERE repo_root = ? AND decided IS NULL`, repoRoot)
	if err != nil {
		return sum, err
	}
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return sum, err
		}
		open[path] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return sum, err
	}

	ts := now.UTC().Format(time.RFC3339)
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		sum.ProjectSlug = c.ProjectSlug
		if c.Proof.Kind == ProofNone {
			sum.WithoutProof++
		} else {
			sum.WithProof++
		}
		if id, ok := open[c.Path]; ok {
			if _, err := tx.Exec(`UPDATE worktree_cleanup_proposals
				   SET project_id = ?, branch = ?, head_sha = ?, phase_id = ?, task_id = ?, proof_kind = ?, proof_detail = ?
				 WHERE id = ?`,
				nullID(c.ProjectID), c.Branch, c.HeadSHA, nullID(c.PhaseID), nullID(c.TaskID), c.Proof.Kind, c.Proof.Detail, id); err != nil {
				return sum, fmt.Errorf("wtcleanup: refresh %s: %w", c.Path, err)
			}
			sum.Refreshed++
			continue
		}
		if _, err := tx.Exec(`INSERT INTO worktree_cleanup_proposals
			   (repo_root, project_id, path, branch, head_sha, phase_id, task_id, proof_kind, proof_detail, proposed_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repoRoot, nullID(c.ProjectID), c.Path, c.Branch, c.HeadSHA, nullID(c.PhaseID), nullID(c.TaskID),
			c.Proof.Kind, c.Proof.Detail, ts); err != nil {
			return sum, fmt.Errorf("wtcleanup: propose %s: %w", c.Path, err)
		}
		sum.Added++
	}
	for path, id := range open {
		if seen[path] {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM worktree_cleanup_proposals WHERE id = ? AND decided IS NULL`, id); err != nil {
			return sum, fmt.Errorf("wtcleanup: drop %s: %w", path, err)
		}
		sum.Dropped++
	}
	return sum, tx.Commit()
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

/* ---------- apply ---------- */

// Forcer is the destructive boundary Apply hands the approved paths to —
// wtjanitor.Service in production, a recorder in tests.
type Forcer interface {
	ForceUnmerged(repoRoot string, paths []string) []wtjanitor.ForceResult
}

// PathOutcome is what Apply did with one requested path.
type PathOutcome struct {
	Path          string `json:"path"`
	Outcome       string `json:"outcome"`
	SalvageBranch string `json:"salvageBranch,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// ApplyResult answers one apply.
type ApplyResult struct {
	ID       int64         `json:"id"`
	RepoRoot string        `json:"repoRoot"`
	Paths    []PathOutcome `json:"paths"`
}

// applyMu serialises applies in this process: two clicks on the same card must
// not hand one path to ForceUnmerged twice.
var applyMu sync.Mutex

// Apply removes the operator-selected paths of the repository that proposal id
// belongs to. Only open proposals WITH a proof reach the Forcer; everything
// else is answered without touching git:
//
//   - a path already decided → already-decided (idempotent per proposal);
//   - a path never proposed in this repository → not-proposed;
//   - a proof-less proposal → no-proof;
//   - the repository's worktrees cannot be listed → repo-error, for every pick
//     (fail closed: nothing reaches the Forcer);
//   - a checkout now on another branch than proposed → branch-changed;
//   - an owner that is running again, reopened or no longer known → owner-active
//     (a phase re-run reuses the same path AND branch, so only the owner's state
//     can tell a re-acquired checkout apart);
//   - a checkout whose HEAD is not the one the proof was computed for →
//     head-changed.
//
// Removed and gone paths are decided; refused and failed ones stay open with
// the reason in `error`, so the same card can retry them.
func Apply(db *sql.DB, g Git, f Forcer, id int64, paths []string, now time.Time) (ApplyResult, error) {
	applyMu.Lock()
	defer applyMu.Unlock()

	res := ApplyResult{ID: id, Paths: []PathOutcome{}}
	if err := db.QueryRow(`SELECT repo_root FROM worktree_cleanup_proposals WHERE id = ?`, id).Scan(&res.RepoRoot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return res, ErrNotFound
		}
		return res, err
	}

	type pick struct {
		idx       int
		rowID     int64
		projectID int64
		branch    string
		head      string
	}
	var picks []pick
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out := PathOutcome{Path: p}
		var rowID, projectID int64
		var branch, head, proof string
		var decided sql.NullString
		err := db.QueryRow(`SELECT id, COALESCE(project_id, 0), branch, head_sha, proof_kind, decided FROM worktree_cleanup_proposals
			 WHERE repo_root = ? AND path = ?
			 ORDER BY decided IS NULL DESC, id DESC LIMIT 1`, res.RepoRoot, p).Scan(&rowID, &projectID, &branch, &head, &proof, &decided)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out.Outcome = OutcomeNotProposed
		case err != nil:
			return res, err
		case decided.Valid:
			out.Outcome, out.Reason = OutcomeAlready, decided.String
		case proof == ProofNone:
			out.Outcome, out.Reason = OutcomeNoProof, "no proof the content is in main"
		default:
			picks = append(picks, pick{idx: len(res.Paths), rowID: rowID, projectID: projectID, branch: branch, head: head})
		}
		res.Paths = append(res.Paths, out)
	}
	if len(picks) == 0 {
		return res, nil
	}

	// Everything below re-checks, at click time, what the scan saw. Without the
	// worktree list none of it can be checked, so nothing is removed.
	wts, err := g.Worktrees(res.RepoRoot)
	if err != nil {
		for _, pk := range picks {
			res.Paths[pk.idx].Outcome = OutcomeRepoError
			res.Paths[pk.idx].Reason = "cannot list worktrees: " + err.Error()
		}
		return res, nil
	}
	kept := picks[:0]
	for _, pk := range picks {
		out := &res.Paths[pk.idx]
		wt, found := findWorktree(wts, out.Path)
		if !found {
			kept = append(kept, pk) // gone since: the Forcer reports it and the row is decided gone
			continue
		}
		// The proof was computed for the proposed branch. A checkout that now
		// holds another one (the path reused by a later run) is not what the
		// operator saw.
		if wt.Branch != pk.branch {
			out.Outcome, out.Reason = OutcomeBranchChanged, fmt.Sprintf("proposed %q, now %q", pk.branch, wt.Branch)
			continue
		}
		// Same path, same branch is exactly what a phase re-run looks like: only
		// the owner's state tells a re-acquired checkout apart.
		own, known, oerr := resolveOwner(db, pk.projectID, wt.Branch, wt.Path)
		if oerr != nil {
			return res, oerr
		}
		if reason := ownerVeto(own, known); reason != "" {
			out.Outcome, out.Reason = OutcomeOwnerActive, reason
			continue
		}
		// The proof is for one commit; a checkout that moved since carries
		// commits nobody proved are in main.
		head, herr := g.Head(wt.Path)
		switch {
		case herr != nil:
			out.Outcome, out.Reason = OutcomeHeadChanged, "HEAD unreadable: "+herr.Error()
			continue
		case pk.head == "" || head != pk.head:
			out.Outcome, out.Reason = OutcomeHeadChanged, fmt.Sprintf("proposed at %s, now %s", shortSHA(pk.head), shortSHA(head))
			continue
		}
		kept = append(kept, pk)
	}
	picks = kept
	if len(picks) == 0 {
		return res, nil
	}

	targets := make([]string, len(picks))
	for i, pk := range picks {
		targets[i] = res.Paths[pk.idx].Path
	}
	forced := f.ForceUnmerged(res.RepoRoot, targets)
	ts := now.UTC().Format(time.RFC3339)
	for i, pk := range picks {
		out := &res.Paths[pk.idx]
		if i >= len(forced) {
			out.Outcome, out.Reason = wtjanitor.ForceFailed, "no result from the janitor"
			continue
		}
		fr := forced[i]
		out.Outcome, out.SalvageBranch, out.Reason = fr.Outcome, fr.SalvageBranch, fr.Reason
		var err error
		switch fr.Outcome {
		case wtjanitor.ForceRemoved:
			_, err = db.Exec(`UPDATE worktree_cleanup_proposals SET decided = ?, decided_at = ?, salvage_branch = ?, error = NULL
				 WHERE id = ? AND decided IS NULL`, DecidedRemoved, ts, nullStr(fr.SalvageBranch), pk.rowID)
		case wtjanitor.ForceGone:
			_, err = db.Exec(`UPDATE worktree_cleanup_proposals SET decided = ?, decided_at = ?, error = NULL
				 WHERE id = ? AND decided IS NULL`, DecidedGone, ts, pk.rowID)
		default:
			_, err = db.Exec(`UPDATE worktree_cleanup_proposals SET error = ? WHERE id = ? AND decided IS NULL`,
				fr.Outcome+": "+fr.Reason, pk.rowID)
		}
		if err != nil {
			return res, fmt.Errorf("wtcleanup: record %s: %w", out.Path, err)
		}
	}
	return res, nil
}

// ownerVeto is why an owner no longer allows removal ("" when it does): the
// same closed-and-idle rule Candidates applied at scan time.
func ownerVeto(own owner, known bool) string {
	switch {
	case !known:
		return "no phase, plan or card owns the branch any more"
	case own.active:
		return "a run is in flight for " + own.label
	case !own.closed:
		return own.label + " is open again"
	}
	return ""
}

func shortSHA(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
