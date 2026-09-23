package phasediag

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// Blocker kinds, in the order Diagnose emits them (most actionable first).
const (
	KindDepIncomplete     = "dep-incomplete"
	KindDepUnmerged       = "dep-unmerged"
	KindBranchBlocksRetry = "branch-blocks-retry"
	KindBranchDirty       = "branch-dirty"

	// KindVerifyFailed: the phase opted into post-run verification
	// (`**Verify:** strict`) and the read-only verifier returned FAIL. It is a
	// BLOCKER, deliberately, and not a new run outcome: decision D5 makes the verdict
	// an INPUT to the diagnosis, while the ticked checkboxes remain the single truth
	// about progress. A `completed` phase whose verification failed still reads
	// `completed` — with this blocker beside it saying why that is not the whole story.
	//
	// The blocker surface is exactly the data-carrying advisory channel this package
	// already has (dep-unmerged, branch-dirty, …), which is why the verdict needed no
	// new vocabulary anywhere. Only `fail` is reported: pass is the silent normal, and
	// inconclusive means the verifier could not conclude — an absence of evidence, not
	// something standing in the operator's way.
	KindVerifyFailed = "verify-failed"

	KindNoCriteria = "no-criteria"

	// KindUnverified: the phase opted into verification, its criteria are all
	// ticked, and no verdict ever landed (or the verifier could not conclude). The
	// work may be perfectly fine — nobody knows, which is the point. Distinct from
	// KindVerifyFailed, which is a grade AGAINST the work; this one is the ABSENCE
	// of a grade, and it used to be silent: the phase read as done.
	KindUnverified = "unverified"

	// KindUnreported: the work landed and (where asked for) was confirmed, but the
	// record of it did not — an empty or placeholder Completion Report, or no
	// lesson recorded for the plan. Separate from KindUnverified because the
	// remedy is different: write down what happened, rather than re-run a grader.
	KindUnreported = "unreported"

	// KindNoLesson: the plan carries no lesson in the retro store. ADVISORY —
	// it never blocks completion; see the emission site for why.
	KindNoLesson = "no-lesson"

	// KindOwnWorktree: the phase's branch holds commits AND is checked out at the
	// worktree this daemon created for that very run. That state needs no action —
	// a retry warm-reuses the worktree and continues the work — and the one thing
	// KindBranchDirty advises, deleting the branch, is exactly what git refuses on
	// a live checkout (409 ErrBranchCheckedOut). So it is its own kind, with no
	// delete affordance, and KindBranchDirty is reserved for a leftover that is
	// genuinely not ours.
	KindOwnWorktree = "own-worktree"

	// KindOrphanBranch: a swarm/phase-<id> branch whose id matches no phase row at
	// all — work stranded under a previous id generation (rows used to be deleted
	// and re-inserted on every plan rescan, minting new ids; see the applyEpics
	// upsert). Only reported when the branch holds commits: an empty orphan is
	// litter the next run reclaims automatically, not lost work.
	//
	// Emitted LAST, after no-criteria: it is a plan-level fact about the repo
	// rather than a property of the phase being diagnosed, so it must not crowd
	// out the phase's own blockers.
	KindOrphanBranch = "orphan-branch"
)

// verdictFail is the one verification verdict this package reacts to. Spelled here
// rather than imported from internal/verify: phasediag is read-only over a column,
// and taking a dependency on the verifier to compare a string would invert the
// direction the seams run in (verify already reaches for the run engines, not the
// other way round). The value is the column's, pinned by TestVerifyFailedBlocker.
const verdictFail = "fail"

// maxOrphans caps the named orphan blockers one diagnosis emits. A repo littered
// with old run branches would otherwise flood the modal and bury the phase's own
// blockers; the overflow is reported as a single "+N more" line instead.
const maxOrphans = 5

// Blocker is one reason the phase did not progress, or one thing standing between
// the user and a successful retry. Summary is rendered verbatim by the UI.
//
// Branch/CommitsAhead carry the branch-dirty facts as DATA, not only as prose. The
// UI offers a `git branch -D` off this blocker, and a confirmation for a destructive
// action must name what it destroys from the same source that proved it — not by
// parsing Summary, and not by rebuilding "swarm/phase-<id>" client-side, which would
// go on naming a branch after the server's naming rule moved. Empty/0 on every other
// kind.
type Blocker struct {
	Kind         string `json:"kind"`
	Summary      string `json:"summary"`
	Detail       string `json:"detail"`
	Branch       string `json:"branch,omitempty"`
	CommitsAhead int    `json:"commitsAhead,omitempty"`
}

// AgentMessage is the executor's own last word, for the cases the daemon cannot
// prove. Text is the newest assistant turn of the run's session, truncated on a
// rune boundary.
type AgentMessage struct {
	SessionUUID string `json:"sessionUuid"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated"`
}

// Diagnosis is the full answer for one phase.
type Diagnosis struct {
	PhaseID       int64  `json:"phaseId"`
	Seq           int    `json:"seq"`
	Name          string `json:"name"`
	RunOutcome    string `json:"runOutcome"`
	CriteriaTotal int    `json:"criteriaTotal"`
	// CriteriaBefore is nil when run_checkboxes_before is NULL — the run's baseline
	// was never measured. A plain int would serialise that as 0 and let the UI render
	// a "0 → N" delta nobody measured, which is the exact claim the NULL policy in
	// OutcomeFromRow exists to refuse.
	CriteriaBefore *int          `json:"criteriaBefore"`
	CriteriaAfter  int           `json:"criteriaAfter"`
	RunStartedAt   *string       `json:"runStartedAt"`
	RunEndedAt     *string       `json:"runEndedAt"`
	RunError       *string       `json:"runError"`
	Blockers       []Blocker     `json:"blockers"`
	AgentMessage   *AgentMessage `json:"agentMessage"`
	// VerifyVerdict / VerifyDetail are the last verification's answer for this phase
	// (pass | fail | inconclusive), null when the phase was never graded — the default,
	// since verification is opt-in per doc. Exposed ALONGSIDE RunOutcome, never folded
	// into it: the outcome stays checkbox-derived (D5), and a fail additionally raises
	// the verify-failed blocker.
	VerifyVerdict *string `json:"verifyVerdict"`
	VerifyDetail  *string `json:"verifyDetail"`
}

// ErrPhaseNotFound: no epic_phases row for the id (mapped to 404 by the api layer).
var ErrPhaseNotFound = errors.New("phasediag: phase not found")

// OwnCheckout is the seam that tells a leftover run branch apart from a run whose
// own worktree is still checked out on it — satisfied by *worktree.Manager. A nil
// OwnCheckout means "cannot tell", and the diagnosis then reports the blocking
// reading (branch-dirty), which is the safe direction: it over-warns rather than
// telling a user that a branch which really does block a retry needs no action.
type OwnCheckout interface {
	OwnCheckoutOf(repoRoot, branch string) (string, bool)
}

// maxAgentText caps the executor excerpt shown in the modal.
const maxAgentText = 1200

// maxSubjects caps the commit subjects listed in a branch-dirty detail — enough
// to recognise the work, not a full log dump.
const maxSubjects = 20

// phaseRow is one epic_phases row joined to its project, as both the subject of a
// diagnosis and (partially) its dependencies.
type phaseRow struct {
	ID       int64
	Seq      int
	Name     string
	DocPath  string
	Total    int
	Done     int
	Complete bool // ticked criteria are the ONLY completion proof (see phaserun.depSatisfied)
}

// branchName is the deterministic run branch for a phase id — the same name
// phaserun hands worktree.Acquire ("phase-<id>" ⇒ swarm/phase-<id>).
func branchName(phaseID int64) string {
	return runcore.PhaseBranch(phaseID)
}

// Diagnose builds the diagnosis for one phase. git may be nil, and the project may
// have no filesystem path — branch-derived blockers are then omitted rather than
// guessed; criteria and dependency blockers still render.
func Diagnose(db *sql.DB, git worktree.Git, own OwnCheckout, phaseID int64) (Diagnosis, error) {
	var (
		d                Diagnosis
		taskID           int64
		depsJSON         string
		runState         string
		uuid             sql.NullString
		startedAt        sql.NullString
		endedAt          sql.NullString
		runErr           sql.NullString
		before           sql.NullInt64
		after            sql.NullInt64
		docPath          string
		projPath         sql.NullString
		total, don       int
		verifyMode       sql.NullString
		verdict          sql.NullString
		vDetail          sql.NullString
		completionReport sql.NullString
		lessonRecorded   bool
	)
	err := db.QueryRow(`
		SELECT e.workspace_task_id, e.seq, e.name, e.doc_path, e.depends_on,
		       e.checkboxes_total, e.checkboxes_done, e.run_state, e.run_session_uuid,
		       e.run_started_at, e.run_ended_at, e.run_error, e.run_checkboxes_before,
		       e.run_checkboxes_after, e.verify_mode, e.verify_verdict, e.verify_detail,
		       COALESCE(e.completion_report,''),
		       EXISTS (SELECT 1 FROM retro_lessons l
		                 JOIN task_retros r ON r.id = l.retro_id
		                WHERE r.task_id = e.workspace_task_id),
		       p.path
		  FROM epic_phases e
		  JOIN tasks t ON t.id = e.workspace_task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE e.id = ?`, phaseID).Scan(
		&taskID, &d.Seq, &d.Name, &docPath, &depsJSON,
		&total, &don, &runState, &uuid,
		&startedAt, &endedAt, &runErr, &before, &after,
		&verifyMode, &verdict, &vDetail, &completionReport, &lessonRecorded, &projPath)
	if errors.Is(err, sql.ErrNoRows) {
		return Diagnosis{}, ErrPhaseNotFound
	}
	if err != nil {
		return Diagnosis{}, err
	}

	d.PhaseID = phaseID
	d.CriteriaTotal = total
	// The run's right edge is the count stamped at exit (0042) when it exists;
	// otherwise the LIVE count, which may have moved since — the wsingest rescan
	// and TickPhaseChecklist both write checkboxes_done.
	d.CriteriaAfter = don
	if after.Valid {
		d.CriteriaAfter = int(after.Int64)
	}
	// A NULL baseline is reported as such (nil), not as a fabricated 0 — see the
	// field comment. The outcome derivation applies the same NULL policy via
	// OutcomeFromRow, the one code path the api layer's phase DTO also uses.
	if before.Valid {
		b := int(before.Int64)
		d.CriteriaBefore = &b
	}
	d.RunStartedAt = nullStr(startedAt)
	d.RunEndedAt = nullStr(endedAt)
	d.RunError = nullStr(runErr)
	// OutcomeFromRow takes NO verdict argument, on purpose: §5.4 considered
	// `completed-unverified` and rejected it. The outcome is checkbox-derived and the
	// verdict travels beside it, so the one derivation both this modal and the list
	// chip go through stays exactly as wide as it was.
	d.RunOutcome = OutcomeFromRow(runState, total, don, before, after)
	d.VerifyVerdict = nullStr(verdict)
	d.VerifyDetail = nullStr(vDetail)
	d.Blockers = []Blocker{} // always a JSON array, never null

	// The base branch is the one signal every branch-derived blocker is relative
	// to. No repo path, no git seam, or a detached HEAD ⇒ we cannot name a base,
	// so those blockers are skipped entirely rather than guessed against a
	// hard-coded "main".
	repoRoot := projPath.String
	base := ""
	if repoRoot != "" && git != nil {
		base = baseBranch(git, repoRoot)
	}
	branchesKnown := base != ""

	deps, err := loadDeps(db, taskID, depsJSON)
	if err != nil {
		return Diagnosis{}, err
	}

	// 1. dep-incomplete — a dependency cannot prove it is done. A dependency with
	// NO criteria is equally unprovable (same rule as phaserun.depSatisfied), but
	// "is only 0/0 complete" implies a count that came up short; the truth is that
	// nothing was ever countable. Same Kind — the UI renders kinds, and a sixth one
	// would widen a contract other phases consume — different sentence.
	for _, dep := range deps {
		if dep.Complete {
			continue
		}
		summary := fmt.Sprintf("Phase %d is only %d/%d complete", dep.Seq, dep.Done, dep.Total)
		if dep.Total == 0 {
			summary = fmt.Sprintf(
				"Phase %d has no acceptance-criteria checkboxes, so its completion cannot be proven", dep.Seq)
		}
		d.Blockers = append(d.Blockers, Blocker{
			Kind:    KindDepIncomplete,
			Summary: summary,
			Detail:  fmt.Sprintf("phase %d — %s", dep.Seq, filepath.Base(dep.DocPath)),
		})
	}

	// 2. dep-unmerged — the dependency IS ticked, but its code never reached base.
	// This is the incident's real cause: the executor read a green dependency and
	// found none of its code in the tree it was given.
	if branchesKnown {
		for _, dep := range deps {
			if !dep.Complete {
				continue
			}
			branch := branchName(dep.ID)
			exists, ahead, _ := branchAhead(git, repoRoot, base, branch)
			if exists && ahead > 0 {
				d.Blockers = append(d.Blockers, Blocker{
					Kind: KindDepUnmerged,
					Summary: fmt.Sprintf("Phase %d is ticked, but its code is on %s — %s not merged into %s",
						dep.Seq, branch, plural(ahead, "commit"), base),
					Detail: fmt.Sprintf("%s → %s ahead, %s", branch, plural(ahead, "commit"), againstBase(base)),
				})
			}
		}
	}

	// 3/4. The phase's OWN leftover branch — mutually exclusive states: an empty
	// one is noise the next retry cleans up, a non-empty one either holds work a
	// retry would collide with, or is still checked out at the run's own worktree
	// (own-worktree), where a retry simply continues it.
	if branchesKnown {
		branch := branchName(phaseID)
		exists, ahead, subjects := branchAhead(git, repoRoot, base, branch)
		ownPath := ""
		if exists && ahead > 0 && own != nil {
			if p, ok := own.OwnCheckoutOf(repoRoot, branch); ok {
				ownPath = p
			}
		}
		switch {
		case exists && ahead == 0:
			d.Blockers = append(d.Blockers, Blocker{
				Kind:    KindBranchBlocksRetry,
				Summary: fmt.Sprintf("Leftover branch %s will be cleaned up automatically on retry", branch),
				Detail:  fmt.Sprintf("%s → 0 commits ahead, %s", branch, againstBase(base)),
			})
		case exists && ownPath != "":
			// No Branch field: this state must offer no delete affordance, because
			// `git branch -D` on a live checkout is refused (409) — the blocker would
			// otherwise advise the one action that cannot work.
			d.Blockers = append(d.Blockers, Blocker{
				Kind: KindOwnWorktree,
				Summary: fmt.Sprintf(
					"A previous run's worktree is still checked out on %s; retrying continues it", branch),
				Detail: fmt.Sprintf("%s → %s ahead, checked out at %s, %s",
					branch, plural(ahead, "commit"), ownPath, againstBase(base)),
			})
		case exists:
			d.Blockers = append(d.Blockers, Blocker{
				Kind: KindBranchDirty,
				Summary: fmt.Sprintf("Branch %s holds %s — merge or delete it before retrying",
					branch, plural(ahead, "commit")),
				// The base note goes LAST: the subjects are the answer to "what is on
				// this branch", newest first, and the qualifier belongs after them.
				Detail: strings.Join(append(subjects, againstBase(base)), "\n"),
				// The same two facts as data, for the delete confirmation.
				Branch:       branch,
				CommitsAhead: ahead,
			})
		}
	}

	// 5. verify-failed — the phase asked to be graded and the grade came back FAIL.
	// A phase-level fact about the work itself, so it sits with the phase's own
	// blockers, after the branch states (which stand between the operator and a retry)
	// and before no-criteria (a statement about measurability, not about the work).
	//
	// Only `fail`: pass is the silent normal, and inconclusive is an absence of
	// evidence rather than something in the way.
	if verdict.String == verdictFail && verdictIsCurrent(db, phaseID, verifyMode.String, startedAt) {
		d.Blockers = append(d.Blockers, Blocker{
			Kind:    KindVerifyFailed,
			Summary: "Verification of this phase's work returned FAIL — the criteria it ticked were not confirmed",
			// The verifier's own reasons, verbatim: the whole value of the blocker is
			// carrying WHY, and a summary alone would send the operator to the logs.
			Detail: strings.TrimSpace(vDetail.String),
		})
	}

	// 5b. unverified — the completion gate refuses this phase for want of a grade it
	// asked for. Emitted from phasegate.Check rather than from a local rule, so the
	// modal, the Plans list and the dependency gate cannot disagree about the same
	// row. `fail` is deliberately NOT routed here: decision D5 keeps a failed grade
	// as completed work with a verify-failed blocker beside it, and 5 above says so.
	gate := phasegate.Check(phasegate.Input{
		CriteriaDone:     d.CriteriaAfter,
		CriteriaTotal:    total,
		VerifyMode:       verifyMode.String,
		VerifyVerdict:    verdict.String,
		CompletionReport: completionReport.String,
		Ran:              endedAt.Valid,
		ClosureRequired:  phasegate.ClosureGateEnabled(),
	})
	switch gate.State {
	case phasegate.StateUnverified:
		d.Blockers = append(d.Blockers, Blocker{
			Kind:    KindUnverified,
			Summary: "Every acceptance criterion is ticked, but this phase has no verification result — it is unverified, not done",
			Detail:  strings.Join(gate.Reasons, "\n"),
		})
	case phasegate.StateUnreported:
		d.Blockers = append(d.Blockers, Blocker{
			Kind:    KindUnreported,
			Summary: "The work is done and unrecorded — a phase closes with a Completion Report and a lesson, not just with ticked boxes",
			Detail:  strings.Join(gate.Reasons, "\n"),
		})
	}

	// 5c. no-lesson — ADVISORY, and deliberately not part of the completion gate.
	// The retrospective that carries a lesson is written after the work, so
	// requiring one to call the work done can never be satisfied in sequence; and
	// in the live store exactly one plan in eighty had a lesson recorded, so
	// gating on it measured an unused convention rather than a fleet that skips
	// lessons. It is surfaced where it is useful — beside the finished work,
	// where the operator can still write it — and it blocks nothing.
	if !lessonRecorded && phasegate.CriteriaMetForDisplay(d.CriteriaAfter, total) {
		d.Blockers = append(d.Blockers, Blocker{
			Kind:    KindNoLesson,
			Summary: "This plan has no recorded lesson — the next retrospective will have nothing to learn from it",
			Detail: "Add a `### Lesson N: <title>` entry under `## Lessons Learned` in the task's " +
				"phases/09-retrospective.md, which is where the retro flow already reads lessons from.",
		})
	}

	// 6. no-criteria — nothing to measure, so no run of this phase can ever be
	// proven to have achieved anything.
	if total == 0 {
		d.Blockers = append(d.Blockers, Blocker{
			Kind:    KindNoCriteria,
			Summary: "This phase doc has no acceptance-criteria checkboxes, so progress cannot be measured",
			Detail:  docPath,
		})
	}

	// 7. orphan-branch — LAST, because it is a fact about the repo rather than
	// about this phase. See KindOrphanBranch.
	if branchesKnown {
		orphans, err := orphanBranches(db, git, repoRoot, base)
		if err != nil {
			return Diagnosis{}, err
		}
		d.Blockers = append(d.Blockers, orphans...)
	}

	d.AgentMessage = agentMessage(db, uuid.String)
	return d, nil
}

// orphanBranches lists swarm/phase-<id> branches whose id matches no epic_phases
// row AND that hold commits ahead of base.
//
// Scoping: the id must be absent from epic_phases ENTIRELY, not merely absent from
// the epic being diagnosed. Phase ids are global across epics, so an epic-scoped
// check would make every plan report every other plan's live run branches — noise
// that would bury the one branch that actually is lost work.
//
// Empty orphans are omitted: 0 commits ahead of base means there is nothing to
// lose, and the next run's ReclaimEmptyBranch deletes them automatically.
//
// Like every other branch-derived rule here, a caller with base == "" (detached
// HEAD, nil git, pathless project) never reaches this function, and ANY git error
// degrades to "no orphans" rather than failing the diagnosis.
func orphanBranches(db *sql.DB, git worktree.Git, repoRoot, base string) ([]Blocker, error) {
	out, err := git.Run(repoRoot, "branch", "--list", runcore.PhaseBranchPrefix+"*")
	if err != nil {
		return nil, nil
	}

	ids := make([]int64, 0, 8)
	for _, line := range strings.Split(out, "\n") {
		// `git branch --list` marks the current branch "* " and a branch checked
		// out in another worktree "+ ", and indents the rest by two spaces.
		name := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "*+"))
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, runcore.PhaseBranchPrefix) {
			continue
		}
		id, convErr := strconv.ParseInt(strings.TrimPrefix(name, runcore.PhaseBranchPrefix), 10, 64)
		if convErr != nil {
			continue // swarm/phase-<not a number> is not a run branch of ours
		}
		// Only the CANONICAL spelling is ours. The blocker below reports branchName(id)
		// rather than the listed line, so a hand-made swarm/phase-007 would otherwise be
		// reported as swarm/phase-7 — a branch that may not exist, whose cleanup button
		// would then act on the wrong name (and which the orphan route now refuses).
		if name != branchName(id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var (
		blockers []Blocker
		overflow int
	)
	for _, id := range ids {
		var one int
		queryErr := db.QueryRow(`SELECT 1 FROM epic_phases WHERE id = ?`, id).Scan(&one)
		if queryErr == nil {
			continue // the id IS a live phase row (possibly in another epic) — not orphaned
		}
		if !errors.Is(queryErr, sql.ErrNoRows) {
			return nil, queryErr
		}
		branch := branchName(id)
		exists, ahead, _ := branchAhead(git, repoRoot, base, branch)
		if !exists || ahead == 0 {
			continue // gone, unreadable, or empty litter
		}
		if len(blockers) >= maxOrphans {
			overflow++
			continue
		}
		blockers = append(blockers, Blocker{
			Kind: KindOrphanBranch,
			Summary: fmt.Sprintf(
				"%s holds %s but matches no phase in this plan — work stranded before the ids were stabilised",
				branch, plural(ahead, "commit")),
			Detail: fmt.Sprintf("%s → %s ahead, %s; merge or delete it",
				branch, plural(ahead, "commit"), againstBase(base)),
			// Carried as data so the cleanup action names the branch the server
			// proved, not one the client rebuilt from a phase id — an orphan's id
			// matches no row, so there is no phase to derive it from.
			Branch:       branch,
			CommitsAhead: ahead,
		})
	}
	if overflow > 0 {
		// No Branch field: the overflow line is a count, not a delete target, and
		// a delete affordance keyed on Branch must not appear for it.
		noun := "branches"
		if overflow == 1 {
			noun = "branch"
		}
		blockers = append(blockers, Blocker{
			Kind:    KindOrphanBranch,
			Summary: fmt.Sprintf("+%d more orphaned run %s in this repo hold commits", overflow, noun),
			Detail: fmt.Sprintf("listed %d of %d; %s",
				maxOrphans, maxOrphans+overflow, againstBase(base)),
		})
	}
	return blockers, nil
}

// loadDeps resolves depends_on (JSON seq numbers) to sibling phase rows, ascending
// by seq. Garbage JSON ⇒ no dependencies (the epics.go decodeIntList posture), and
// a seq with no sibling row is skipped: unknowable, not a blocker.
func loadDeps(db *sql.DB, taskID int64, depsJSON string) ([]phaseRow, error) {
	var seqs []int
	if err := json.Unmarshal([]byte(depsJSON), &seqs); err != nil {
		return nil, nil
	}
	sort.Ints(seqs)

	out := make([]phaseRow, 0, len(seqs))
	for _, seq := range seqs {
		var r phaseRow
		err := db.QueryRow(`
			SELECT id, seq, name, doc_path, checkboxes_total, checkboxes_done
			  FROM epic_phases
			 WHERE workspace_task_id = ? AND seq = ?
			 ORDER BY id LIMIT 1`, taskID, seq).Scan(
			&r.ID, &r.Seq, &r.Name, &r.DocPath, &r.Total, &r.Done)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// Completion is proven by ticked criteria only — never by run_state. A
		// zero-criteria dependency can never prove it, so it counts as incomplete.
		r.Complete = r.Total > 0 && r.Done >= r.Total
		out = append(out, r)
	}
	return out, nil
}

// baseBranch returns the repo's current branch name — the same signal
// worktree.Manager.resolveStartPoint pins acquisitions to, so a diagnosis can
// never disagree with an acquisition about what "base" means. Detached HEAD or a
// git failure returns "", and every branch-derived blocker is then skipped.
func baseBranch(git worktree.Git, repoRoot string) string {
	out, err := git.Run(repoRoot, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// againstBase spells out what a commit count was compared with. baseBranch is
// deliberately the repo's CURRENT checkout (so a diagnosis can never disagree with
// worktree.Acquire about "base"), which means a user sitting on a feature branch
// sees a dependency merged into dev but not into that branch as dep-unmerged, and
// branch-blocks-retry / branch-dirty flip under the same skew. The base choice
// stays; every blocker derived from it says which branch it measured against, so
// base skew is recognisable instead of looking like a real problem.
func againstBase(base string) string {
	return "measured against the currently checked-out branch " + base
}

// branchAhead reports whether refs/heads/<branch> exists and how many commits it
// holds beyond base, plus their subjects (newest first, capped at maxSubjects) for
// the branch-dirty detail.
//
// ANY git error degrades to exists=false: a diagnosis explaining why a run failed
// must never itself fail because git was unhappy.
func branchAhead(git worktree.Git, repoRoot, base, branch string) (exists bool, ahead int, subjects []string) {
	ref := "refs/heads/" + branch
	if _, err := git.Run(repoRoot, "show-ref", "--verify", "--quiet", ref); err != nil {
		return false, 0, nil
	}
	out, err := git.Run(repoRoot, "rev-list", "--count", base+".."+ref)
	if err != nil {
		return false, 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return false, 0, nil
	}
	if n == 0 {
		return true, 0, nil
	}
	log, err := git.Run(repoRoot, "log", "--format=%s",
		"--max-count="+strconv.Itoa(maxSubjects), base+".."+ref)
	if err != nil {
		return false, 0, nil
	}
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			subjects = append(subjects, s)
		}
	}
	return true, n, subjects
}

// agentMessage returns the executor's newest assistant prose for the run's
// session, nil when there is no uuid or nothing ingested yet. turns.text holds
// text blocks only — ingest drops thinking blocks (migration 0005) — so this is
// the executor's own explanation, never extended thinking.
func agentMessage(db *sql.DB, uuid string) *AgentMessage {
	if uuid == "" {
		return nil
	}
	var text sql.NullString
	err := db.QueryRow(`
		SELECT tr.text
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid=? AND tr.role='assistant' AND tr.text IS NOT NULL
		 ORDER BY tr.seq DESC LIMIT 1`, uuid).Scan(&text)
	if err != nil || !text.Valid {
		return nil
	}
	trimmed := strings.TrimSpace(text.String)
	if trimmed == "" {
		return nil
	}
	msg := &AgentMessage{SessionUUID: uuid, Text: trimmed}
	// Cut on a RUNE boundary: a byte-wise slice would split a multi-byte rune and
	// render as a replacement char in the modal.
	if r := []rune(trimmed); len(r) > maxAgentText {
		msg.Text = string(r[:maxAgentText])
		msg.Truncated = true
	}
	return msg
}

// plural renders "1 commit" / "N commits".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// nullStr converts a NULL-able column to an omitted (nil) JSON value.
func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// verdictIsCurrent reports whether a stored FAIL verdict belongs on the phase's
// blocker list. A doc that asked for verification is graded on every run, so its
// verdict is always current. A doc whose verify mode is off only ever gets a grade
// from the opt-in surprise auto-verify (learning loop phase 13.5), and epic_phases
// keeps that verdict across runs — so for such a doc the FAIL counts only when a
// verification of this phase started during the current run. A later clean run
// that was not surprising (and so was not graded) must not inherit the old FAIL.
func verdictIsCurrent(db *sql.DB, phaseID int64, verifyMode string, runStartedAt sql.NullString) bool {
	if verifyMode != "" && verifyMode != "off" {
		return true
	}
	if !runStartedAt.Valid || runStartedAt.String == "" {
		return true
	}
	started, err := parseStamp(runStartedAt.String)
	if err != nil {
		return true
	}
	var last sql.NullString
	if err := db.QueryRow(`SELECT MAX(started_at) FROM verification_runs WHERE target_key = ?`,
		"phase:"+strconv.FormatInt(phaseID, 10)).Scan(&last); err != nil || !last.Valid {
		// No verification row at all (the verdict came from somewhere else, e.g. an
		// older build): keep today's behaviour rather than hide a FAIL.
		return err != nil || !last.Valid
	}
	// verification_runs stamps milliseconds and run_started_at whole seconds, so
	// compare as times — as strings "…:00.5Z" would sort before "…:00Z". MAX() over
	// same-width RFC3339 millisecond stamps is safe because every row shares a format.
	v, err := parseStamp(last.String)
	if err != nil {
		return true
	}
	return !v.Before(started)
}

func parseStamp(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05", s)
}
