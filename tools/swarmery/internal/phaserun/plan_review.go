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
// The review is ADVISORY. Its result is a phase_reviews row with scope='plan' and
// phase_id NULL, which the Inbox shows. It blocks nothing, re-runs nothing and
// writes nothing into any doc.
//
// The dedupe key — sha256 of the sorted "<run_branch>@<tip>" lines — is stored in
// phase_reviews.run_session_uuid, prefixed planReviewKeyPrefix. For scope='phase'
// that column names the run that was reviewed; for scope='plan' it names the set
// of runs. The prefix keeps it from ever reading as a session uuid. No new column.

const (
	// planReviewMaxFiles bounds the changed files the plan review takes in, summed
	// over every phase's range. A larger plan is recorded as not-verifiable and no
	// reviewer is spawned: past this size the prompt would hold only a fraction of
	// the change, and a verdict on that fraction would claim more than it read.
	planReviewMaxFiles = 300
	// planReviewKeyPrefix marks a branch-set key in phase_reviews.run_session_uuid.
	planReviewKeyPrefix = "branchset:"
	// planReviewTaskPrefix names the throwaway worktree: swarm/planreview-<taskID>.
	// "plan-<id>" belongs to planrun; this one must never collide with it.
	planReviewTaskPrefix = "planreview-"
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

// planBranchReview runs the plan branch review of phaseID's plan when the plan is
// complete and its current set of branch tips has not been reviewed yet.
// Best-effort and silent when there is nothing to do. Every failure after the
// claim is recorded as an inconclusive row.
func (s *Service) planBranchReview(phaseID int64, docPath string) {
	var taskID int64
	if err := s.DB.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, phaseID).Scan(&taskID); err != nil {
		log.Printf("warning: phaserun: plan review phase=%d: epic unreadable: %v", phaseID, err)
		return
	}
	complete, err := s.planComplete(taskID, phaseID, docPath)
	if err != nil {
		log.Printf("warning: phaserun: plan review task=%d: completion unreadable: %v", taskID, err)
		return
	}
	if !complete {
		return
	}
	branches, err := s.planBranches(taskID)
	if err != nil {
		log.Printf("warning: phaserun: plan review task=%d: run branches unreadable: %v", taskID, err)
		return
	}
	if len(branches) == 0 {
		return // no phase ran on a branch: nothing to diff
	}
	key := planBranchKey(branches)
	if !s.claimPlanReview(taskID, key) {
		return
	}
	defer s.releasePlanReview(key)

	started := s.ts()
	out := s.planReview(taskID, phaseID, branches)
	s.recordPlanReview(taskID, key, started, out)
	log.Printf("phaserun: task=%d plan branch review %s over %d branch(es) %s", taskID, out.verdict, len(branches), out.detail)
	s.notify(taskID)
}

// planComplete reports whether every phase of the plan is finished: its doc says
// `Status: done` (doc_status) or all its criteria are ticked
// (phasediag.CriteriaMet). The stamping phase is read from its doc on disk, which
// was copied back before stamp. Its epic_phases counts wait for the next
// wsingest scan.
func (s *Service) planComplete(taskID, phaseID int64, docPath string) (bool, error) {
	rows, err := s.DB.Query(`
		SELECT id, checkboxes_done, checkboxes_total, COALESCE(doc_status, '')
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
		)
		if err := rows.Scan(&id, &done, &total, &status); err != nil {
			return false, err
		}
		n++
		if id == phaseID {
			if c, ok := criteriaInDoc(docPath); ok {
				done, total = c.Done, c.Total
			}
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

// claimPlanReview takes the key for this process, or reports that it is taken:
// in flight here, or already recorded for the plan.
func (s *Service) claimPlanReview(taskID int64, key string) bool {
	s.planReviewMu.Lock()
	defer s.planReviewMu.Unlock()
	if s.planReviewsInFlight[key] {
		return false
	}
	var n int
	if err := s.DB.QueryRow(`
		SELECT COUNT(*) FROM phase_reviews
		 WHERE scope = 'plan' AND workspace_task_id = ? AND run_session_uuid = ?`,
		strconv.FormatInt(taskID, 10), key).Scan(&n); err != nil {
		log.Printf("warning: phaserun: plan review task=%d: dedupe read failed, not starting: %v", taskID, err)
		return false
	}
	if n > 0 {
		return false
	}
	if s.planReviewsInFlight == nil {
		s.planReviewsInFlight = map[string]bool{}
	}
	s.planReviewsInFlight[key] = true
	return true
}

func (s *Service) releasePlanReview(key string) {
	s.planReviewMu.Lock()
	defer s.planReviewMu.Unlock()
	delete(s.planReviewsInFlight, key)
}

// planReview reviews the branch set. Within the file bound it spawns the reviewer
// in a throwaway worktree on the integration base, fingerprinted before and
// after. It removes the worktree and its branch afterwards in every case.
func (s *Service) planReview(taskID, phaseID int64, branches []planBranch) reviewOutcome {
	inconclusive := func(o reviewOutcome, class, detail string) reviewOutcome {
		o.verdict, o.detail = string(verify.VerdictInconclusive), class+": "+detail
		return o
	}
	var o reviewOutcome
	total, sections := s.planDiffs(branches)
	if total > planReviewMaxFiles {
		return inconclusive(o, classReviewUnverifiable, strconv.Itoa(total)+" files")
	}

	info, err := s.loadPhase(phaseID)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "the stamping phase is unreadable: "+err.Error())
	}
	repoRoot, err := s.runRoot(info)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "the plan's repository cannot be resolved: "+err.Error())
	}
	var title string
	_ = s.DB.QueryRow(`SELECT COALESCE(title, '') FROM tasks WHERE id = ?`, taskID).Scan(&title)

	name := planReviewTaskPrefix + strconv.FormatInt(taskID, 10)
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
		return inconclusive(o, classReviewerNotStarted, "could not acquire a review worktree: "+err.Error())
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
		return inconclusive(o, classReviewUnverifiable, "could not fingerprint the review worktree before the review: "+err.Error())
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
		return inconclusive(o, classReviewUnverifiable, "could not fingerprint the review worktree after the review: "+err.Error())
	}
	if run != nil {
		o.findings = capFindings(run.Output)
	}
	if o.treeAfter != o.treeBefore {
		// No restore: the worktree is removed in the defer above.
		return inconclusive(o, ClassReviewerMutatedTree, o.treeBefore+"→"+o.treeAfter)
	}
	return concludeReview(o, run, rerr)
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
// Until a row is written the key is not deduped, so a lost write means the next
// `done` stamp reviews again.
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
