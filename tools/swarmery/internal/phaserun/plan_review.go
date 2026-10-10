package phaserun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runsettings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// The plan branch review (decision D4, step 2). Each phase of a plan runs on its
// own run branch and is reviewed alone (review.go). Nothing looks at the SEAMS:
// what phase N hands over against what phase M expects. When a phase run is
// stamped `done` and that completes the plan — every phase's doc says `Status:
// done` or has all its criteria ticked — the same reviewer reads every phase's
// diff together. It runs once per set of branch tips.
//
// One plan review per plan is in flight at a time (claimPlanReview). A trigger
// that arrives while one runs only records that the tips may have moved; when the
// running review finishes, the tips are read again and reviewed if they differ
// from the set just reviewed. Two reviews of one plan never share a throwaway
// worktree, and the final set of tips is the one reviewed last.
//
// The review is ADVISORY. Its result is a phase_reviews row with scope='plan' and
// phase_id NULL, which the Inbox shows. It blocks nothing, re-runs nothing and
// writes nothing into any doc.
//
// The dedupe key — sha256 of the sorted "<run_branch>@<tip>" lines — is stored in
// phase_reviews.run_session_uuid, prefixed planReviewKeyPrefix. For scope='phase'
// that column names the run that was reviewed; for scope='plan' it names the set
// of runs. The prefix keeps it from ever reading as a session uuid. No new column.
// The key is written only for a settled outcome: the reviewer's own verdict
// (pass, fail, or an inconclusive it produced) or the file bound. A transient
// failure (no worktree, no fingerprint, a reviewer that never started) is recorded
// with an empty key, so the next trigger over the same tips reviews again.

const (
	// planReviewMaxFiles bounds the changed files the plan review takes in, summed
	// over every phase's range. A larger plan is recorded as not-verifiable and no
	// reviewer is spawned: past this size the prompt would hold only a fraction of
	// the change, and a verdict on that fraction would claim more than it read.
	planReviewMaxFiles = 300
	// planReviewKeyPrefix marks a branch-set key in phase_reviews.run_session_uuid.
	planReviewKeyPrefix = "branchset:"
	// planReviewTaskPrefix names the throwaway worktree:
	// swarm/planreview-<taskID>-<key[:12]>. "plan-<id>" belongs to planrun; this
	// one must never collide with it. The key part keeps two reviews of one plan
	// from ever sharing a worktree, even if the per-plan guard were bypassed.
	planReviewTaskPrefix = "planreview-"
	// planReviewNameKeyLen is how much of the key's hex the worktree name carries.
	planReviewNameKeyLen = 12
	// planReviewReadmeCap bounds the plan README in the prompt.
	planReviewReadmeCap = 32 << 10
)

// planSeamFocus tells the reviewer what differs from a phase review. reviewerRole
// still sets the role, the lenses and the verdict contract.
const planSeamFocus = `THIS IS A PLAN BRANCH REVIEW, not a phase review. Every phase of the plan below has finished, each on its own run branch, and each was judged alone. Your job is what nobody has checked yet: the seams between phases. Focus on what phase N hands over and what phase M expects: contracts, function signatures, migrations (numbering, columns, defaults), types and DTO fields, config keys, event names. A finding is a mismatch between two phases, or a phase that relies on something no phase delivered. Do not repeat findings that sit inside a single phase unless they break another phase.

Your cwd is a clean checkout of the integration base, NOT of the run branches: the phases' changes exist only in the diffs below. Read the base tree for context. The plan README stands in for the phase document.`

// planBranch is one phase's run branch as the plan review sees it.
type planBranch struct {
	PhaseID    int64
	Seq        int
	Name       string
	Branch     string
	StartPoint string // run_start_point; "" when the run recorded none
	RepoRoot   string // the repository this phase ran in
	Tip        string // the branch's commit now; "" when it cannot be read (deleted, merged away)
}

// planBranchKey is the dedupe key for one set of branch tips: the sha256 of the
// sorted "<branch>@<tip>" lines. A moved tip (a fix commit, a re-run) is a new key
// and so a new review. The same tips are the same key, whatever phase stamped.
func planBranchKey(branches []planBranch) string {
	lines := make([]string, 0, len(branches))
	for _, b := range branches {
		lines = append(lines, b.Branch+"@"+b.Tip)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return planReviewKeyPrefix + hex.EncodeToString(sum[:])
}

// maybePlanReview is stamp's hook. It spawns the plan branch review check and
// returns. The check and the review run in their own goroutine with their own
// context, never inside the stamping run's single-flight slot. A daemon without a
// wired reviewer (Review nil) does nothing, like the phase review stage.
func (s *Service) maybePlanReview(phaseID int64, docPath string) {
	if s.Review == nil {
		return
	}
	s.spawn(func() { s.planBranchReview(phaseID, docPath) })
}

// planReviewTrigger is one `done` stamp that may start a plan review: the phase
// that stamped and its doc on disk.
type planReviewTrigger struct {
	phaseID int64
	docPath string
}

// planReviewFlight is a plan's in-flight plan review. pending is the latest
// trigger that arrived while it ran (nil: none); the running review takes it up
// instead of releasing the slot.
type planReviewFlight struct {
	pending *planReviewTrigger
}

// planReviewSubject is what a plan review would review now: the plan, its run
// branches with their tips, and their key.
type planReviewSubject struct {
	taskID   int64
	branches []planBranch
	key      string
}

// planBranchReview runs the plan branch review of phaseID's plan when the plan is
// complete and its current set of branch tips has not been reviewed yet. When a
// review of the plan is already running it only leaves the trigger behind, and
// the running review takes it up when it finishes (planBranchReviewLoop).
// Best-effort and silent when there is nothing to do.
func (s *Service) planBranchReview(phaseID int64, docPath string) {
	sub, ok := s.planReviewSubjectOf(phaseID, docPath)
	if !ok {
		return
	}
	if !s.claimPlanReview(sub.taskID, sub.key, planReviewTrigger{phaseID: phaseID, docPath: docPath}) {
		return
	}
	s.planBranchReviewLoop(phaseID, sub)
}

// planBranchReviewLoop holds the plan's review slot. It reviews sub, then takes
// up whatever trigger arrived meanwhile: it reads the tips again and reviews them
// when they differ from the set just reviewed and are not recorded yet. It
// releases the slot only when no trigger is left, atomically in nextPlanReview, so
// a trigger can never fall between the last check and the release.
func (s *Service) planBranchReviewLoop(phaseID int64, sub planReviewSubject) {
	for {
		started := s.ts()
		out, settled := s.planReview(sub.taskID, phaseID, sub.key, sub.branches)
		key := ""
		if settled {
			key = sub.key
		}
		s.recordPlanReview(sub.taskID, key, started, out)
		log.Printf("phaserun: task=%d plan branch review %s over %d branch(es) %s", sub.taskID, out.verdict, len(sub.branches), out.detail)
		s.notify(sub.taskID)

		reviewed := sub.key
		for {
			next, more := s.nextPlanReview(sub.taskID)
			if !more {
				return
			}
			n, ok := s.planReviewSubjectOf(next.phaseID, next.docPath)
			// The tips just reviewed ⇒ nothing new. That holds after a transient
			// failure too: the slot does not retry in a loop; the next trigger
			// after the release does.
			if !ok || n.taskID != sub.taskID || n.key == reviewed || s.planReviewRecorded(n.taskID, n.key) {
				continue
			}
			log.Printf("phaserun: task=%d plan branch tips moved during the review — reviewing the new set", sub.taskID)
			phaseID, sub = next.phaseID, n
			break
		}
	}
}

// planReviewSubjectOf resolves phaseID's plan and, when the plan is complete, its
// run branches and their key. ok is false when there is nothing to review: the
// plan is unfinished, no phase ran on a branch, or the store could not be read.
func (s *Service) planReviewSubjectOf(phaseID int64, docPath string) (planReviewSubject, bool) {
	var sub planReviewSubject
	if err := s.DB.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, phaseID).Scan(&sub.taskID); err != nil {
		log.Printf("warning: phaserun: plan review phase=%d: epic unreadable: %v", phaseID, err)
		return sub, false
	}
	complete, err := s.planComplete(sub.taskID, phaseID, docPath)
	if err != nil {
		log.Printf("warning: phaserun: plan review task=%d: completion unreadable: %v", sub.taskID, err)
		return sub, false
	}
	if !complete {
		return sub, false
	}
	sub.branches, err = s.planBranches(sub.taskID)
	if err != nil {
		log.Printf("warning: phaserun: plan review task=%d: run branches unreadable: %v", sub.taskID, err)
		return sub, false
	}
	if len(sub.branches) == 0 {
		return sub, false // no phase ran on a branch: nothing to diff
	}
	sub.key = planBranchKey(sub.branches)
	return sub, true
}

// planComplete reports whether every phase of the plan is finished: its doc says
// `Status: done` (doc_status) or all its criteria are ticked
// (phasediag.CriteriaMet), and no other phase has a run in flight: a sibling
// still running may yet move its tip, and its own `done` stamp is the trigger
// that sees the final set. The stamping phase is read from its doc on disk, which
// was copied back before stamp. Its epic_phases counts wait for the next
// wsingest scan.
func (s *Service) planComplete(taskID, phaseID int64, docPath string) (bool, error) {
	rows, err := s.DB.Query(`
		SELECT id, checkboxes_done, checkboxes_total, COALESCE(doc_status, ''), COALESCE(run_state, '')
		  FROM epic_phases WHERE workspace_task_id = ?`, taskID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id          int64
			done, total int
			status      string
			runState    string
		)
		if err := rows.Scan(&id, &done, &total, &status, &runState); err != nil {
			return false, err
		}
		n++
		if id == phaseID {
			if c, ok := criteriaInDoc(docPath); ok {
				done, total = c.Done, c.Total
			}
		} else if runState == "running" {
			return false, nil
		}
		if status != "done" && !phasediag.CriteriaMet(done, total) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// planBranches lists the plan's phases that ran on a branch, in seq order, each
// with its repository and its tip read now.
func (s *Service) planBranches(taskID int64) ([]planBranch, error) {
	rows, err := s.DB.Query(`
		SELECT id, seq, COALESCE(name, ''), run_branch, COALESCE(run_start_point, '')
		  FROM epic_phases
		 WHERE workspace_task_id = ? AND run_branch IS NOT NULL AND run_branch <> ''
		 ORDER BY seq, id`, taskID)
	if err != nil {
		return nil, err
	}
	var out []planBranch
	for rows.Next() {
		var b planBranch
		if err := rows.Scan(&b.PhaseID, &b.Seq, &b.Name, &b.Branch, &b.StartPoint); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		info, err := s.loadPhase(out[i].PhaseID)
		if err != nil {
			return nil, fmt.Errorf("phase %d: %w", out[i].PhaseID, err)
		}
		// A phase whose repo cannot be resolved keeps RepoRoot "" and Tip "": it
		// still counts in the key, and its prompt header says why it has no diff.
		if root, err := s.runRoot(info); err == nil {
			out[i].RepoRoot = root
			out[i].Tip = s.planBranchTip(root, out[i].Branch)
		}
	}
	return out, nil
}

// planBranchTip is the commit refs/heads/<branch> names, or "" when it cannot be
// read. Like branchTip, but through reviewGit: the plan review must read tips
// even where no Git seam is wired.
func (s *Service) planBranchTip(repoRoot, branch string) string {
	if repoRoot == "" || branch == "" || strings.HasPrefix(branch, "-") {
		return ""
	}
	out, err := s.reviewGit().Run(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// claimPlanReview takes the plan's review slot for key, or reports that the review
// may not start. While a review of the plan is in flight the trigger is left
// behind as pending, the latest one winning, for the running review to take up
// (nextPlanReview) whatever its key. A key already recorded for the plan is not
// claimed.
func (s *Service) claimPlanReview(taskID int64, key string, trig planReviewTrigger) bool {
	s.planReviewMu.Lock()
	defer s.planReviewMu.Unlock()
	if f, ok := s.planReviewsInFlight[taskID]; ok {
		f.pending = &trig
		return false
	}
	if s.planReviewRecorded(taskID, key) {
		return false
	}
	if s.planReviewsInFlight == nil {
		s.planReviewsInFlight = map[int64]*planReviewFlight{}
	}
	s.planReviewsInFlight[taskID] = &planReviewFlight{}
	return true
}

// nextPlanReview is called by the slot's holder after each review. With a pending
// trigger it hands that trigger over and the slot stays held; without one it
// releases the slot. One lock covers both, so a trigger either lands before the
// release (and is handed over) or claims a free slot after it.
func (s *Service) nextPlanReview(taskID int64) (planReviewTrigger, bool) {
	s.planReviewMu.Lock()
	defer s.planReviewMu.Unlock()
	f, ok := s.planReviewsInFlight[taskID]
	if !ok || f.pending == nil {
		delete(s.planReviewsInFlight, taskID)
		return planReviewTrigger{}, false
	}
	next := *f.pending
	f.pending = nil
	return next, true
}

// planReviewRecorded reports whether key is already recorded for the plan. A
// failed read reports true: not starting is the safe side, and the next trigger
// asks again.
func (s *Service) planReviewRecorded(taskID int64, key string) bool {
	var n int
	if err := s.DB.QueryRow(`
		SELECT COUNT(*) FROM phase_reviews
		 WHERE scope = 'plan' AND workspace_task_id = ? AND run_session_uuid = ?`,
		strconv.FormatInt(taskID, 10), key).Scan(&n); err != nil {
		log.Printf("warning: phaserun: plan review task=%d: dedupe read failed, not starting: %v", taskID, err)
		return true
	}
	return n > 0
}

// planReviewName is the throwaway worktree's task name for one review:
// planreview-<taskID>-<the first planReviewNameKeyLen hex chars of key>.
func planReviewName(taskID int64, key string) string {
	h := strings.TrimPrefix(key, planReviewKeyPrefix)
	if len(h) > planReviewNameKeyLen {
		h = h[:planReviewNameKeyLen]
	}
	return planReviewTaskPrefix + strconv.FormatInt(taskID, 10) + "-" + h
}

// planReview reviews the branch set under key. Within the file bound it spawns the
// reviewer in a throwaway worktree on the integration base, fingerprinted before
// and after. It removes the worktree and its branch afterwards in every case.
// settled reports whether the outcome may dedupe key: true for the reviewer's own
// outcome and for the file bound, false for a transient failure that a later
// trigger over the same tips should retry.
func (s *Service) planReview(taskID, phaseID int64, key string, branches []planBranch) (out reviewOutcome, settled bool) {
	inconclusive := func(o reviewOutcome, class, detail string) reviewOutcome {
		o.verdict, o.detail = string(verify.VerdictInconclusive), class+": "+detail
		return o
	}
	var o reviewOutcome
	total, sections := s.planDiffs(branches)
	if total > planReviewMaxFiles {
		return inconclusive(o, classReviewUnverifiable, strconv.Itoa(total)+" files"), true
	}

	info, err := s.loadPhase(phaseID)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "the stamping phase is unreadable: "+err.Error()), false
	}
	repoRoot, err := s.runRoot(info)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "the plan's repository cannot be resolved: "+err.Error()), false
	}
	var title string
	_ = s.DB.QueryRow(`SELECT COALESCE(title, '') FROM tasks WHERE id = ?`, taskID).Scan(&title)

	name := planReviewName(taskID, key)
	base := s.integrationBase(repoRoot)
	// A crashed earlier review may have left its commit-less branch behind, and
	// AcquireAt would then refuse the name. It is measured against the same base
	// it was cut from. Best-effort: a branch holding commits is left alone, and
	// the acquire below reports the conflict.
	if _, err := s.reclaimEmptyBranch(repoRoot, "swarm/"+name, base); err != nil {
		log.Printf("warning: phaserun: plan review task=%d: reclaim swarm/%s: %v", taskID, name, err)
	}
	acq, err := s.acquire(repoRoot, info.ProjectSlug, name, base)
	if err != nil {
		return inconclusive(o, classReviewerNotStarted, "could not acquire a review worktree: "+err.Error()), false
	}
	defer func() {
		if s.Wt == nil {
			return
		}
		if err := s.Wt.Remove(repoRoot, acq, false /* throwaway: drop the branch too */); err != nil {
			log.Printf("warning: phaserun: plan review task=%d: remove worktree %s: %v", taskID, acq.Path, err)
		}
	}()

	o.treeBefore, err = s.worktreeFingerprint(acq.Path)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "could not fingerprint the review worktree before the review: "+err.Error()), false
	}
	o.sessionUUID = s.UUID()
	resolution := claudeacct.Resolve(info.ProjectPath)
	spec := verify.RunSpec{
		Prompt:          planReviewPrompt(title, planReadme(info.DocPath), sections),
		SessionUUID:     o.sessionUUID,
		Cwd:             acq.Path,
		Model:           reviewModel,
		Resolution:      resolution,
		SettingsFile:    runsettings.Compose("review", resolution, runsettings.Inputs{}),
		DisallowedTools: reviewDeniedTools,
	}
	log.Printf("phaserun: task=%d plan branch review in worktree=%q (%d files over %d branch(es))",
		taskID, acq.Path, total, len(branches))
	run, rerr := s.Review.Run(context.Background(), spec)

	o.treeAfter, err = s.worktreeFingerprint(acq.Path)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "could not fingerprint the review worktree after the review: "+err.Error()), false
	}
	if run != nil {
		o.findings = capFindings(run.Output)
	}
	if o.treeAfter != o.treeBefore {
		// No restore: the worktree is removed in the defer above.
		return inconclusive(o, ClassReviewerMutatedTree, o.treeBefore+"→"+o.treeAfter), true
	}
	o = concludeReview(o, run, rerr)
	// A reviewer that never started produced no outcome of its own.
	return o, !strings.HasPrefix(o.detail, classReviewerNotStarted+":")
}

// integrationBase is the start ref of the throwaway review worktree: the
// remote's default branch when the repo has one, otherwise "" (the repo's current
// branch tip, Acquire's own default).
func (s *Service) integrationBase(repoRoot string) string {
	g := s.reviewGit()
	for _, ref := range []string{"origin/HEAD", "origin/main"} {
		if _, err := g.Run(repoRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref
		}
	}
	return ""
}

// planDiffs counts every phase's changed files (git diff --name-only
// <run_start_point>...<run_branch>) and renders the per-phase diff sections of the
// prompt. The total decides the file bound. The sections share one byte budget
// (reviewDiffMaxBytes, the prompt being an argv element). A phase with no start
// point, no readable tip or a failing diff gets a header saying so and counts no
// files.
func (s *Service) planDiffs(branches []planBranch) (total int, sections string) {
	g := s.reviewGit()
	budget := reviewDiffMaxBytes
	var b strings.Builder
	for _, br := range branches {
		fmt.Fprintf(&b, "\n=== PHASE %d — %s · branch %s", br.Seq, br.Name, br.Branch)
		if br.Tip != "" {
			fmt.Fprintf(&b, " @ %s", shortSHA(br.Tip))
		}
		b.WriteString(" ===\n")
		switch {
		case br.RepoRoot == "":
			b.WriteString("DIFF: unavailable — the phase's repository could not be resolved.\n")
			continue
		case br.Tip == "":
			b.WriteString("DIFF: unavailable — the branch no longer exists (deleted or merged away).\n")
			continue
		case strings.TrimSpace(br.StartPoint) == "" || strings.HasPrefix(br.StartPoint, "-"):
			b.WriteString("DIFF: unavailable — the run recorded no start point, so there is no honest base.\n")
			continue
		}
		rng := br.StartPoint + "..." + br.Branch
		names, err := g.Run(br.RepoRoot, "diff", "--name-only", rng)
		if err != nil {
			fmt.Fprintf(&b, "DIFF: unavailable — `git diff --name-only %s` failed: %v\n", rng, err)
			continue
		}
		n := 0
		for _, l := range strings.Split(names, "\n") {
			if strings.TrimSpace(l) != "" {
				n++
			}
		}
		total += n
		fmt.Fprintf(&b, "Range %s in %s: %d file(s).\n", rng, br.RepoRoot, n)
		if n == 0 || total > planReviewMaxFiles {
			continue // past the bound nothing is spawned, so nothing more is read
		}
		if stat, err := g.Run(br.RepoRoot, "diff", "--stat", rng); err == nil {
			fmt.Fprintf(&b, "%s\n", strings.TrimRight(stat, "\n"))
		}
		if budget <= 0 {
			b.WriteString("FULL DIFF: omitted — the prompt's diff budget is spent; judge this phase from its stat.\n")
			continue
		}
		full, err := g.Run(br.RepoRoot, "diff", rng)
		if err != nil {
			fmt.Fprintf(&b, "FULL DIFF: unavailable — %v\n", err)
			continue
		}
		if len(full) > budget {
			full = full[:budget] + "\n[… diff truncated: the prompt's " + strconv.Itoa(reviewDiffMaxBytes>>10) + " KB budget is spent]"
		}
		budget -= len(full)
		fmt.Fprintf(&b, "FULL DIFF:\n----------------------------------------\n%s\n----------------------------------------\n",
			strings.TrimRight(full, "\n"))
	}
	return total, b.String()
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// planReadme returns the plan's README (the dir above the phase doc holds
// plan/README.md beside the phase docs), capped; "" when there is none.
func planReadme(docPath string) string {
	if docPath == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(docPath), "README.md"))
	if err != nil {
		return ""
	}
	if len(b) > planReviewReadmeCap {
		return string(b[:planReviewReadmeCap]) + "\n[… README truncated]"
	}
	return string(b)
}

// planReviewPrompt renders the plan branch review prompt: the phase reviewer's
// role and contract, the seam focus, the plan README and the per-phase diffs.
func planReviewPrompt(title, readme, sections string) string {
	var b strings.Builder
	b.WriteString(reviewerRole)
	b.WriteString("\n\n")
	b.WriteString(planSeamFocus)
	b.WriteString("\n\nPLAN: ")
	b.WriteString(title)
	if strings.TrimSpace(readme) != "" {
		b.WriteString("\n\nPLAN README:\n----------------------------------------\n")
		b.WriteString(strings.TrimRight(readme, "\n"))
		b.WriteString("\n----------------------------------------\n")
	}
	b.WriteString("\n\nPHASE DIFFS (each phase's run branch against the commit its run started from):\n")
	b.WriteString(sections)
	return b.String()
}

// recordPlanReview writes the scope='plan' row. Best-effort like recordReview.
// key is "" for a transient outcome: the row is shown but dedupes nothing. Until a
// row carrying the key is written the key is not deduped, so a lost write means
// the next `done` stamp reviews again.
func (s *Service) recordPlanReview(taskID int64, key, started string, o reviewOutcome) {
	if _, err := s.DB.Exec(`
		INSERT INTO phase_reviews
			(scope, phase_id, workspace_task_id, session_uuid, run_session_uuid,
			 tree_before, tree_after, verdict, detail, findings, fix_round,
			 started_at, finished_at)
		VALUES ('plan', NULL, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		strconv.FormatInt(taskID, 10), o.sessionUUID, key,
		o.treeBefore, o.treeAfter, o.verdict, o.detail, o.findings,
		started, s.ts()); err != nil {
		log.Printf("error: phaserun: task=%d record plan review: %v", taskID, err)
	}
}
