// Package wtcleanup narrows the worktree janitor's keep-unmerged pile to the
// worktrees an operator can remove with a clear conscience, and carries that
// decision to them and back.
//
// The janitor (internal/wtjanitor) never removes a keep-unmerged worktree: its
// branch carries commits no other ref has, and by itself it cannot tell an
// abandoned run from work in progress. Most of that pile is neither — it is the
// leftover checkout of a phase or board card that already finished, whose
// content reached main through a squash merge (so no ref "contains" it). This
// package finds exactly those:
//
//  1. Candidates reads the newest journal row per path from worktree_sweeps and
//     keeps the keep-unmerged ones still registered with git;
//  2. it resolves each to its owner — the phase run (swarm/phase-<id>, or a
//     stamped epic_phases.run_branch), the plan run (swarm/plan-<id>) or the
//     board card (tasks.branch / tasks.worktree_path / swarm/<external-id>) —
//     and drops every worktree whose owner is unknown, still open or active;
//  3. for the closed ones only (the git work is bounded by that), it computes a
//     PROOF that the branch's content is already in main: the phase's landing
//     is merged, or a merge of the branch into main changes nothing
//     (`git merge-tree --write-tree` + `git diff --stat`, objects written to a
//     scratch directory so the repository is never written).
//
// A candidate without proof is counted, never offered for deletion. Proposals
// (proposals.go) store the result for the Needs-you card; removal happens only
// when the operator applies them, through wtjanitor.Service.ForceUnmerged,
// which writes a salvage branch first.
package wtcleanup

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

// Proof kinds, strongest first.
const (
	ProofLanding     = "landing"      // the phase's landing is merged
	ProofContentDiff = "content-diff" // merging the branch into main changes nothing
	ProofNone        = "none"         // closed owner, content not proven in main
)

// Skip reasons: a keep-unmerged worktree that is not a candidate at all.
const (
	SkipGone         = "gone"          // no longer a worktree of the repository
	SkipLocked       = "locked"        // `git worktree lock` — the operator's own "keep"
	SkipUnknownOwner = "unknown-owner" // no phase, plan or card owns the branch
	SkipOpenOwner    = "open-owner"    // its phase or card is not closed
	SkipActive       = "active"        // a run is in flight, or a process/session is inside
	SkipRepoError    = "repo-error"    // git could not list the repository's worktrees this pass
)

// JournalWindow bounds how far back the sweep journal is read. The janitor
// journals a present keep-unmerged worktree on every tick, so an older newest
// row means the path left git's list long ago — git is re-checked anyway.
const JournalWindow = 30 * 24 * time.Hour

// Proof is why a candidate's content is (or is not shown to be) in main.
type Proof struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Candidate is one keep-unmerged worktree whose owner is closed.
type Candidate struct {
	RepoRoot    string `json:"repoRoot"`
	ProjectID   int64  `json:"projectId"`
	ProjectSlug string `json:"projectSlug"`
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	PhaseID     int64  `json:"phaseId,omitempty"`
	TaskID      int64  `json:"taskId,omitempty"`
	// Owner is a human label: "<plan> · Phase <seq>: <name>", "<plan> (plan run)"
	// or "<external-id>: <title>".
	Owner string `json:"owner"`
	Proof Proof  `json:"proof"`
}

// Skipped is a keep-unmerged worktree that is not a candidate, and why.
type Skipped struct {
	RepoRoot    string `json:"repoRoot"`
	ProjectSlug string `json:"projectSlug"`
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	Reason      string `json:"reason"`
}

// Scan is one pass over the journal.
type Scan struct {
	GeneratedAt string      `json:"generatedAt"`
	Candidates  []Candidate `json:"candidates"`
	Skipped     []Skipped   `json:"skipped"`
}

// Git is the read side Candidates needs. ExecGit is the real one.
type Git interface {
	// Worktrees enumerates the repository's worktrees (path, branch, lock, main).
	Worktrees(repoRoot string) ([]wtjanitor.Worktree, error)
	// ContentInMain reports whether merging branch into the repository's main
	// line changes nothing, with a one-line detail either way.
	ContentInMain(repoRoot, branch string) (bool, string, error)
	// Dirty counts the checkout's modified and untracked paths.
	Dirty(worktreePath string) (int, error)
}

// Candidates scans the journal and returns every keep-unmerged worktree whose
// owner is closed, with its proof, plus every one it skipped and why. live may
// be nil (no process/session check — tests, or a read-only dry run that cannot
// see the machine's processes).
func Candidates(db *sql.DB, g Git, live wtjanitor.Liveness) (Scan, error) {
	now := time.Now().UTC()
	scan := Scan{GeneratedAt: now.Format(time.RFC3339), Candidates: []Candidate{}, Skipped: []Skipped{}}
	rows, err := db.Query(`
		SELECT w.path, p.id, p.path, p.slug
		  FROM worktree_sweeps w
		  JOIN projects p ON p.id = w.project_id
		 WHERE w.id IN (SELECT MAX(id) FROM worktree_sweeps WHERE ts >= ? GROUP BY path)
		   AND w.verdict = ? AND p.archived = 0
		 ORDER BY p.path, w.path`,
		now.Add(-JournalWindow).Format(time.RFC3339), string(wtjanitor.VerdictKeepUnmerged))
	if err != nil {
		return scan, fmt.Errorf("wtcleanup: read journal: %w", err)
	}
	type entry struct {
		path, repoRoot, slug string
		projectID            int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.path, &e.projectID, &e.repoRoot, &e.slug); err != nil {
			rows.Close()
			return scan, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return scan, err
	}

	listed := map[string][]wtjanitor.Worktree{}
	unreadable := map[string]bool{}
	for _, e := range entries {
		wts, ok := listed[e.repoRoot]
		if !ok && !unreadable[e.repoRoot] {
			if wts, err = g.Worktrees(e.repoRoot); err != nil {
				unreadable[e.repoRoot] = true
			}
			listed[e.repoRoot] = wts
		}
		skip := func(branch, reason string) {
			scan.Skipped = append(scan.Skipped, Skipped{RepoRoot: e.repoRoot, ProjectSlug: e.slug, Path: e.path, Branch: branch, Reason: reason})
		}
		if unreadable[e.repoRoot] {
			skip("", SkipRepoError) // not "gone": nothing is known about it this pass
			continue
		}
		wt, found := findWorktree(wts, e.path)
		if !found || wt.IsMain || !dirExists(wt.Path) {
			skip("", SkipGone)
			continue
		}
		if wt.Locked {
			skip(wt.Branch, SkipLocked)
			continue
		}
		own, known, oerr := resolveOwner(db, e.projectID, wt.Branch, wt.Path)
		if oerr != nil {
			return scan, oerr
		}
		switch {
		case !known:
			skip(wt.Branch, SkipUnknownOwner)
			continue
		case own.active:
			skip(wt.Branch, SkipActive)
			continue
		case !own.closed:
			skip(wt.Branch, SkipOpenOwner)
			continue
		}
		if live != nil {
			if busy, lerr := live.Busy(wt.Path); lerr != nil || busy {
				skip(wt.Branch, SkipActive) // unknown liveness counts as live
				continue
			}
		}
		scan.Candidates = append(scan.Candidates, Candidate{
			RepoRoot: e.repoRoot, ProjectID: e.projectID, ProjectSlug: e.slug,
			Path: wt.Path, Branch: wt.Branch, PhaseID: own.phaseID, TaskID: own.taskID,
			Owner: own.label, Proof: proveOne(g, e.repoRoot, wt, own),
		})
	}
	return scan, nil
}

// proveOne computes the proof for a closed candidate: landing first (no git
// work), else the content diff. Uncommitted paths are named in the detail —
// they are not part of either proof, and ForceUnmerged salvages them anyway.
func proveOne(g Git, repoRoot string, wt wtjanitor.Worktree, own owner) Proof {
	var p Proof
	switch {
	case own.landing == "merged":
		p = Proof{Kind: ProofLanding, Detail: "phase landing merged"}
		if own.prURL != "" {
			p.Detail += " · " + own.prURL
		}
	case wt.Branch == "":
		p = Proof{Kind: ProofNone, Detail: "detached HEAD"}
	default:
		ok, detail, err := g.ContentInMain(repoRoot, wt.Branch)
		switch {
		case err != nil:
			p = Proof{Kind: ProofNone, Detail: "content diff failed: " + err.Error()}
		case ok:
			p = Proof{Kind: ProofContentDiff, Detail: detail}
		default:
			p = Proof{Kind: ProofNone, Detail: detail}
		}
	}
	if n, err := g.Dirty(wt.Path); err == nil && n > 0 {
		p.Detail += fmt.Sprintf(" · %d uncommitted path(s), salvaged on removal", n)
	}
	return p
}

/* ---------- owner resolution ---------- */

// owner is what the database says about the run or card behind a branch.
type owner struct {
	phaseID, taskID int64
	label           string
	closed, active  bool
	landing, prURL  string
}

// resolveOwner maps a worktree to its phase run, plan run or board card.
// known=false means nothing owns it (a harness subagent's branch, a hand-made
// one) — such a worktree is never a candidate.
func resolveOwner(db *sql.DB, projectID int64, branch, path string) (owner, bool, error) {
	if branch == "" {
		return owner{}, false, nil
	}
	if o, ok, err := phaseOwner(db, branch); err != nil || ok {
		return o, ok, err
	}
	if digits, ok := strings.CutPrefix(branch, runcore.PlanBranchPrefix); ok {
		if id, err := strconv.ParseInt(digits, 10, 64); err == nil {
			return planOwner(db, id)
		}
	}
	return taskOwner(db, projectID, branch, path)
}

// phaseOwner: the stamped run_branch first (a re-keyed phase ran under an older
// id), then the deterministic swarm/phase-<id>.
func phaseOwner(db *sql.DB, branch string) (owner, bool, error) {
	const cols = `SELECT e.id, e.workspace_task_id, e.seq, e.name, COALESCE(e.doc_status, ''),
		       COALESCE(e.run_state, ''), COALESCE(e.landing_state, 'none'), COALESCE(e.pr_url, ''),
		       COALESCE(t.title, ''), COALESCE(t.status, ''), COALESCE(t.board_column, ''),
		       t.archived_at IS NOT NULL
		  FROM epic_phases e LEFT JOIN tasks t ON t.id = e.workspace_task_id `
	row := db.QueryRow(cols+`WHERE e.run_branch = ? ORDER BY e.id DESC LIMIT 1`, branch)
	o, ok, err := scanPhase(row)
	if err != nil || ok {
		return o, ok, err
	}
	if _, isPhase := runcore.PhaseTaskNameFromBranch(branch); !isPhase {
		return owner{}, false, nil
	}
	id, perr := strconv.ParseInt(strings.TrimPrefix(branch, runcore.PhaseBranchPrefix), 10, 64)
	if perr != nil {
		return owner{}, false, nil
	}
	return scanPhase(db.QueryRow(cols+`WHERE e.id = ?`, id))
}

func scanPhase(row *sql.Row) (owner, bool, error) {
	var o owner
	var seq int
	var name, docStatus, runState, planTitle, taskStatus, column string
	var archived bool
	err := row.Scan(&o.phaseID, &o.taskID, &seq, &name, &docStatus, &runState, &o.landing, &o.prURL,
		&planTitle, &taskStatus, &column, &archived)
	if err == sql.ErrNoRows {
		return owner{}, false, nil
	}
	if err != nil {
		return owner{}, false, fmt.Errorf("wtcleanup: phase owner: %w", err)
	}
	o.label = "Phase " + strconv.Itoa(seq) + ": " + name
	if planTitle != "" {
		o.label = planTitle + " · " + o.label
	}
	o.active = runState == "running"
	o.closed = docStatus == "done" || docStatus == "cancelled" || o.landing == "merged" ||
		taskClosed(taskStatus, column, archived)
	return o, true, nil
}

// planOwner: a whole-plan run's checkout. Closed when the plan's card is, or
// every one of its phases is done.
func planOwner(db *sql.DB, taskID int64) (owner, bool, error) {
	var title, status, column string
	var archived bool
	err := db.QueryRow(`SELECT COALESCE(title, ''), COALESCE(status, ''), COALESCE(board_column, ''), archived_at IS NOT NULL
		  FROM tasks WHERE id = ?`, taskID).Scan(&title, &status, &column, &archived)
	if err == sql.ErrNoRows {
		return owner{}, false, nil
	}
	if err != nil {
		return owner{}, false, fmt.Errorf("wtcleanup: plan owner: %w", err)
	}
	o := owner{taskID: taskID, label: strings.TrimSpace(title + " (plan run)")}
	var running int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs WHERE workspace_task_id = ? AND run_state = 'running'`, taskID).
		Scan(&running); err != nil {
		return owner{}, false, fmt.Errorf("wtcleanup: plan run state: %w", err)
	}
	o.active = running > 0
	var phases, done int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(doc_status IN ('done', 'cancelled') OR landing_state = 'merged'), 0)
		  FROM epic_phases WHERE workspace_task_id = ?`, taskID).Scan(&phases, &done); err != nil {
		return owner{}, false, fmt.Errorf("wtcleanup: plan phases: %w", err)
	}
	o.closed = taskClosed(status, column, archived) || (phases > 0 && done == phases)
	return o, true, nil
}

// taskOwner: a board card's dispatch checkout, matched by its recorded branch
// or path, else by swarm/<external-id>. The newest card wins (an external id
// can repeat across re-imports).
func taskOwner(db *sql.DB, projectID int64, branch, path string) (owner, bool, error) {
	ext := strings.TrimPrefix(branch, runcore.BranchPrefix)
	var o owner
	var extID, title, status, column string
	var archived bool
	err := db.QueryRow(`SELECT id, COALESCE(external_id, ''), COALESCE(title, ''), COALESCE(status, ''),
		       COALESCE(board_column, ''), archived_at IS NOT NULL
		  FROM tasks
		 WHERE project_id = ? AND source = 'queue'
		   AND (branch = ? OR worktree_path = ? OR (? <> ? AND external_id = ?))
		 ORDER BY id DESC LIMIT 1`, projectID, branch, path, ext, branch, ext).
		Scan(&o.taskID, &extID, &title, &status, &column, &archived)
	if err == sql.ErrNoRows {
		return owner{}, false, nil
	}
	if err != nil {
		return owner{}, false, fmt.Errorf("wtcleanup: task owner: %w", err)
	}
	o.label = strings.TrimSpace(extID + ": " + title)
	o.active = column == "in_progress"
	o.closed = taskClosed(status, column, archived)
	return o, true, nil
}

// taskClosed: a card done or cancelled, in the done/archived column, or archived.
func taskClosed(status, column string, archived bool) bool {
	return archived || status == "done" || status == "cancelled" || column == "done" || column == "archived"
}

/* ---------- git ---------- */

// ExecGit implements Git with the git binary. Every call runs with optional
// locks off, and the content diff writes its objects to a scratch directory:
// nothing here writes to the repository.
type ExecGit struct{}

func (ExecGit) Worktrees(repoRoot string) ([]wtjanitor.Worktree, error) {
	return wtjanitor.RepoGit{}.List(repoRoot)
}

// ContentInMain merges branch into each main ref that exists — the remote's
// default branch, then the local one — and reports true when the merged tree
// equals that main's tree: the branch carries nothing main does not already
// have (the squash-merge case, where no ref contains the branch's commits).
// A conflicted merge is "not proven", never an error.
func (ExecGit) ContentInMain(repoRoot, branch string) (bool, string, error) {
	common, err := gitOut(repoRoot, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false, "", fmt.Errorf("git common dir: %w", err)
	}
	scratch, err := os.MkdirTemp("", "wtcleanup-objects-*")
	if err != nil {
		return false, "", err
	}
	defer os.RemoveAll(scratch)
	// New objects (the merged tree) land in scratch; every read falls through
	// to the repository's own object store as an alternate.
	env := []string{"GIT_OBJECT_DIRECTORY=" + scratch, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(common, "objects")}

	mains := mainRefs(repoRoot)
	if len(mains) == 0 {
		return false, "no main branch found", nil
	}
	var notes []string
	for _, m := range mains {
		tree, merr := gitOut(repoRoot, env, "merge-tree", "--write-tree", m, branch)
		if merr != nil {
			notes = append(notes, "conflicts with "+m)
			continue
		}
		tree, _, _ = strings.Cut(tree, "\n") // the first line is the tree id
		stat, derr := gitOut(repoRoot, env, "diff", "--stat", m, tree)
		if derr != nil {
			return false, "", fmt.Errorf("diff %s %s: %w", m, tree, derr)
		}
		if stat == "" {
			return true, "merging into " + m + " changes nothing", nil
		}
		notes = append(notes, m+": "+lastLine(stat))
	}
	return false, strings.Join(notes, "; "), nil
}

// mainRefs lists the main line refs to test against, remote first (a squash
// merge lands there before anyone pulls), existing ones only. master is a
// fallback for a repository with no main at all.
func mainRefs(repoRoot string) []string {
	def := "main"
	if head, err := gitOut(repoRoot, nil, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && head != "" {
		def = strings.TrimPrefix(head, "origin/")
	}
	existing := func(names ...string) []string {
		var out []string
		for _, n := range names {
			if _, err := gitOut(repoRoot, nil, "rev-parse", "--verify", "--quiet", n+"^{commit}"); err == nil {
				out = append(out, n)
			}
		}
		return out
	}
	if out := existing("origin/"+def, def); len(out) > 0 {
		return out
	}
	return existing("origin/master", "master")
}

func (ExecGit) Dirty(worktreePath string) (int, error) {
	out, err := gitOutRaw(worktreePath, nil, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) >= 4 {
			n++
		}
	}
	return n, nil
}

func gitOut(dir string, env []string, args ...string) (string, error) {
	out, err := gitOutRaw(dir, env, args...)
	return strings.TrimSpace(out), err
}

func gitOutRaw(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_OPTIONAL_LOCKS=0"), env...)
	out, err := cmd.Output()
	return string(out), err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

/* ---------- paths ---------- */

// findWorktree matches path against git's list in every spelling (git prints
// /private/var/… on macOS where the journal may hold /var/…).
func findWorktree(wts []wtjanitor.Worktree, path string) (wtjanitor.Worktree, bool) {
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
	return wtjanitor.Worktree{}, false
}

func spellings(p string) []string {
	c := filepath.Clean(p)
	out := []string{c}
	if r, err := filepath.EvalSymlinks(c); err == nil && r != c {
		out = append(out, r)
	}
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

/* ---------- summaries ---------- */

// RepoSummary counts one repository's scan: candidates by proof kind, and the
// skipped worktrees by reason.
type RepoSummary struct {
	RepoRoot    string         `json:"repoRoot"`
	ProjectSlug string         `json:"projectSlug"`
	ByProof     map[string]int `json:"byProof"`
	Skipped     map[string]int `json:"skipped"`
}

// Summarize groups a scan per repository, sorted by path.
func Summarize(s Scan) []RepoSummary {
	by := map[string]*RepoSummary{}
	get := func(repo string) *RepoSummary {
		if r, ok := by[repo]; ok {
			return r
		}
		r := &RepoSummary{RepoRoot: repo, ByProof: map[string]int{ProofLanding: 0, ProofContentDiff: 0, ProofNone: 0},
			Skipped: map[string]int{}}
		by[repo] = r
		return r
	}
	for _, c := range s.Candidates {
		r := get(c.RepoRoot)
		r.ProjectSlug = c.ProjectSlug
		r.ByProof[c.Proof.Kind]++
	}
	for _, sk := range s.Skipped {
		r := get(sk.RepoRoot)
		r.ProjectSlug = sk.ProjectSlug
		r.Skipped[sk.Reason]++
	}
	out := make([]RepoSummary, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoRoot < out[j].RepoRoot })
	return out
}
