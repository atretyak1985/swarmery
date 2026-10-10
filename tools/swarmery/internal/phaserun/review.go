package phaserun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runsettings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// The review stage (decision D4): a phase doc carrying `**Review:** on` gets a
// second, read-only headless session after its run settles done|partial and before
// the verifier grades it. The reviewer reads the phase doc and the run's diff and
// answers with code-reviewer's contract (findings + one VERDICT line). A FAIL is
// written into the doc as `## Review findings` and starts ONE fix re-run; a second
// FAIL is recorded and nothing more. Every review is a row in phase_reviews.
//
// Read-only-ness is established twice, independently, because `claude` may change
// its tool set between versions: on the argv (Edit, Write, MultiEdit, NotebookEdit
// AND Bash denied — reviewDeniedTools on top of verify's read-only set, one flag)
// and after the fact (the worktree fingerprinted before and after; any change is
// an inconclusive `reviewer-mutated-tree` and the tree is restored).

// Review classes. Like verify's, an inconclusive detail is "<class>: <detail>".
const (
	// ClassReviewerMutatedTree — the worktree differed after the review from what
	// it was before it. The verdict is void whatever it said, and the tree has been
	// restored so the verifier grades what the executor left.
	ClassReviewerMutatedTree = "reviewer-mutated-tree"
	classReviewerNotStarted  = "reviewer-did-not-start"
	classReviewerTimedOut    = "reviewer-timed-out"
	classReviewerNoVerdict   = "reviewer-produced-no-verdict"
	classReviewCouldNot      = "could-not-conclude"
	classReviewUnverifiable  = "not-verifiable"
)

// reviewDeniedTools are denied on top of verify's read-only set: the verifier may
// run checks, a reviewer only reads (code-reviewer.md: "no write tools, no shell").
var reviewDeniedTools = []string{"Bash"}

// reviewModel is the reviewer's model: code-reviewer's own (opus), pinned to the
// verifier's full ID. "" would inherit the account default, which is not the
// agent's model and may cost twice as much.
const reviewModel = verify.DefaultModel

const (
	// reviewFindingsCap bounds phase_reviews.findings (the migration's 64 KB).
	reviewFindingsCap = 64 << 10
	// reviewDiffMaxBytes bounds the diff inlined into the prompt. The prompt is an
	// argv element, and ARG_MAX (1 MB on macOS) covers the whole argv + env.
	reviewDiffMaxBytes = 256 << 10
	// maxHashedUntracked bounds how many untracked files the fingerprint reads.
	maxHashedUntracked = 2000
	// maxHashedFileBytes: a larger untracked file is fingerprinted by size+mtime.
	maxHashedFileBytes = 4 << 20
)

// reviewerRole is code-reviewer.md's role and output contract
// (plugins/core/agents/code-reviewer.md, "Role" + "Output" + "Bounds"), restated
// for a headless run: the daemon cannot read the plugin cache, and the verdict line
// is the one verify.ParseVerdict already reads.
const reviewerRole = `You are the fleet's independent code reviewer, running headless and READ-ONLY in the git worktree of a finished plan-phase run (your cwd). You read code and judge it; you never fix it. You have Read, Glob and Grep; every write tool and the shell are denied, and the worktree is fingerprinted before and after you run — any change to it voids your verdict.

Review the change below against the phase document it was meant to implement. Lenses:
- Correctness — will this break? Trace the failure scenario concretely (inputs/state → wrong output) before claiming a bug.
- Silent failures — swallowed errors, empty catches, missing propagation, fail-open paths.
- Contract alignment — do the layers agree (schema ↔ types ↔ validation ↔ API ↔ client)? Name the exact mismatch.
- Plan conformance — compare what shipped against the phase document: scope drift, criteria ticked but not met, unapproved dependencies.
- Quality — only findings a maintainer would act on; style nits without consequence are noise.

Output: list only problems you would block the merge for — for each, file:line, one sentence on why it is wrong, and how to show it fails. Rank them by severity (P0 blocking / P1 must-fix). Zero findings is a legitimate result; say so plainly. At most one extra line for a non-blocking note, marked as non-blocking. When a verdict would need a build or test run, say so — the verifier runs after you. Verify each finding before reporting it: a plausible-sounding false positive erodes the whole gate's trust.

End with exactly one final line, nothing after it:
VERDICT: PASS | FAIL | INCONCLUSIVE
FAIL when any P0/P1 stands; INCONCLUSIVE only when you genuinely could not assess (missing files, no diff) — name what was missing.`

// reviewPrompt renders the reviewer's prompt: the role, the phase doc as it stands
// now (with the executor's ticks), and the run's diff.
func reviewPrompt(title, doc, diff string) string {
	var b strings.Builder
	b.WriteString(reviewerRole)
	b.WriteString("\n\nPHASE: ")
	b.WriteString(title)
	b.WriteString("\n\nPHASE DOCUMENT:\n----------------------------------------\n")
	b.WriteString(strings.TrimRight(doc, "\n"))
	b.WriteString("\n----------------------------------------\n\n")
	b.WriteString(diff)
	return b.String()
}

// reviewGit is the git the review stage shells out to in the run's worktree: the
// service's seam when wired (the daemon wires worktree.Manager's), the real binary
// otherwise. Unlike base resolution, the review has no "unknown" fallback — a
// worktree it cannot read is a review it cannot vouch for.
func (s *Service) reviewGit() worktree.Git {
	if s.Git != nil {
		return s.Git
	}
	return worktree.ExecGit{}
}

// reviewDiff renders the diff section of the prompt: `git diff base...HEAD --stat`
// and the full diff of at most verify.DefaultMaxDiffFiles files, capped at
// reviewDiffMaxBytes. base is the run's recorded start point; without one there is
// no honest range, and the prompt says so instead of diffing the branch against
// itself.
func (s *Service) reviewDiff(dir, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "DIFF: unavailable — this run recorded no start point, so there is no honest base to diff against. Review the commits on HEAD that the phase document describes."
	}
	g := s.reviewGit()
	rng := base + "...HEAD"
	stat, err := g.Run(dir, "diff", "--stat", rng)
	if err != nil {
		return fmt.Sprintf("DIFF: unavailable — `git diff --stat %s` failed: %v", rng, err)
	}
	names, err := g.Run(dir, "diff", "--name-only", rng)
	if err != nil {
		return fmt.Sprintf("DIFF: unavailable — `git diff --name-only %s` failed: %v", rng, err)
	}
	var files []string
	for _, l := range strings.Split(names, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DIFF STAT (git diff --stat %s):\n%s\n", rng, strings.TrimRight(stat, "\n"))
	if len(files) == 0 {
		b.WriteString("\nThe run's range holds no changed files.\n")
		return b.String()
	}
	shown := files
	if len(shown) > verify.DefaultMaxDiffFiles {
		shown = shown[:verify.DefaultMaxDiffFiles]
		fmt.Fprintf(&b, "\nThe change spans %d files; the full diff below covers the first %d. Read the rest from the tree.\n",
			len(files), len(shown))
	}
	full, err := g.Run(dir, append([]string{"diff", rng, "--"}, shown...)...)
	if err != nil {
		fmt.Fprintf(&b, "\nFULL DIFF: unavailable — %v\n", err)
		return b.String()
	}
	if len(full) > reviewDiffMaxBytes {
		full = full[:reviewDiffMaxBytes] + "\n[… diff truncated at " + strconv.Itoa(reviewDiffMaxBytes>>10) + " KB; read the rest from the tree]"
	}
	fmt.Fprintf(&b, "\nFULL DIFF:\n----------------------------------------\n%s\n----------------------------------------\n",
		strings.TrimRight(full, "\n"))
	return b.String()
}

// worktreeFingerprint identifies the CONTENT of a worktree — committed and not.
//
// worktree.Manager.TreeHash alone is `rev-parse HEAD^{tree}`: the tree of the HEAD
// commit. An edit the reviewer did not commit (the only kind it could make) is
// invisible to it. So a clean worktree is fingerprinted by exactly that tree id,
// and a dirty one by the tree id plus a hash of `git status --porcelain`, the
// tracked changes against HEAD (`git diff HEAD --binary`) and the content of every
// untracked, non-ignored file. Equal before and after ⇒ the review changed nothing
// git can see.
func (s *Service) worktreeFingerprint(dir string) (string, error) {
	if s.treeFingerprint != nil {
		return s.treeFingerprint(dir)
	}
	g := s.reviewGit()
	out, err := g.Run(dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out)
	status, err := g.Run(dir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		return tree, nil
	}
	h := sha256.New()
	_, _ = io.WriteString(h, status)
	diff, err := g.Run(dir, "diff", "HEAD", "--binary")
	if err != nil {
		return "", err
	}
	_, _ = io.WriteString(h, diff)
	others, err := g.Run(dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	n := 0
	for _, rel := range strings.Split(others, "\x00") {
		if rel = strings.TrimSpace(rel); rel == "" {
			continue
		}
		if n++; n > maxHashedUntracked {
			break
		}
		hashUntracked(h, filepath.Join(dir, rel), rel)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return tree + "+dirty:" + sum[:16], nil
}

// hashUntracked folds one untracked path into h: a symlink by its target, a small
// regular file by its bytes, anything else (a big file, a directory, an unreadable
// path) by what Lstat says about it.
func hashUntracked(h io.Writer, abs, rel string) {
	_, _ = io.WriteString(h, "\x00"+rel+"\x00")
	fi, err := os.Lstat(abs)
	if err != nil {
		_, _ = io.WriteString(h, "missing")
		return
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, _ := os.Readlink(abs)
		_, _ = io.WriteString(h, "link:"+target)
	case fi.Mode().IsRegular() && fi.Size() <= maxHashedFileBytes:
		if b, err := os.ReadFile(abs); err == nil {
			_, _ = h.Write(b)
			return
		}
		fallthrough
	default:
		fmt.Fprintf(h, "%s:%d:%d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
	}
}

// restoreTree puts a worktree the reviewer changed back to HEAD: tracked files
// checked out, untracked files removed. Gitignored paths (the lent dependency
// symlinks) are left alone — `clean -fd` without -x does not touch them.
func (s *Service) restoreTree(dir string) error {
	g := s.reviewGit()
	if _, err := g.Run(dir, "checkout", "--", "."); err != nil {
		return err
	}
	_, err := g.Run(dir, "clean", "-fd")
	return err
}

// reviewOutcome is one review's record, the phase_reviews row in waiting.
type reviewOutcome struct {
	sessionUUID, treeBefore, treeAfter string
	verdict, detail, findings          string
}

// reviewRun is the review stage of a finished phase run. It returns the verdict
// ("" when no review ran) and whether a fix re-run must be started — which the
// CALLER does, after the run's slot is released (startReviewFix).
//
// Skipped (silently) unless the stage is wired, the doc opted in, the run ended
// done|partial (a run that stopped mid-edit is measuring the interruption) and the
// worktree is known. Every other failure is recorded as an inconclusive review and
// never changes the run's own outcome.
func (s *Service) reviewRun(phaseID int64, info phaseInfo, acq worktree.Acquired, endState string) (verdict string, fix bool) {
	if s.Review == nil || info.ReviewMode != wsingest.ReviewOn || acq.Path == "" ||
		(endState != "done" && endState != "partial") {
		return "", false
	}
	started := s.ts()
	round, runUUID := s.reviewState(phaseID)
	out := s.review(phaseID, info, acq)
	s.recordReview(phaseID, info, runUUID, round, started, out)
	log.Printf("phaserun: phase=%d review %s (fix round %d) %s", phaseID, out.verdict, round, out.detail)

	switch out.verdict {
	case string(verify.VerdictPass):
		// A pass closes the fix cycle: a later review may start a fix again.
		s.setReviewFixRound(phaseID, 0)
	case string(verify.VerdictFail):
		if round > 0 {
			// The one fix already ran and the review still fails: recorded above,
			// nothing more. The verifier still grades the run.
			return out.verdict, false
		}
		if err := wsingest.AppendReviewFindings(info.DocPath, out.findings, s.clock()); err != nil {
			// No findings in the doc ⇒ a re-run would not know what to fix.
			log.Printf("error: phaserun: phase=%d review FAIL but the findings could not be written into %s: %v — no fix re-run",
				phaseID, info.DocPath, err)
			return out.verdict, false
		}
		s.setReviewFixRound(phaseID, 1)
		return out.verdict, true
	}
	return out.verdict, false
}

// review runs the reviewer and classifies what came back. The fingerprint is taken
// on both sides of the spawn and compared FIRST: a mutated tree voids any verdict.
func (s *Service) review(phaseID int64, info phaseInfo, acq worktree.Acquired) reviewOutcome {
	inconclusive := func(o reviewOutcome, class, detail string) reviewOutcome {
		o.verdict, o.detail = string(verify.VerdictInconclusive), class+": "+detail
		return o
	}
	var o reviewOutcome
	doc, err := os.ReadFile(info.DocPath)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "the phase doc is unreadable: "+err.Error())
	}
	o.treeBefore, err = s.worktreeFingerprint(acq.Path)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "could not fingerprint the worktree before the review: "+err.Error())
	}

	o.sessionUUID = s.UUID()
	resolution := claudeacct.Resolve(info.ProjectPath)
	spec := verify.RunSpec{
		Prompt:      reviewPrompt(info.Name, string(doc), s.reviewDiff(acq.Path, acq.StartPoint)),
		SessionUUID: o.sessionUUID,
		Cwd:         acq.Path,
		Model:       reviewModel,
		// Account and estate come from the PROJECT path, never from the worktree,
		// exactly as the verifier resolves them.
		Resolution:      resolution,
		SettingsFile:    runsettings.Compose("review", resolution, runsettings.Inputs{}),
		DisallowedTools: reviewDeniedTools,
	}
	log.Printf("phaserun: phase=%d reviewing worktree=%q", phaseID, acq.Path)
	// context.Background(): the run's own context is already cancelled (the defer's
	// cancel() fired); the reviewer is a new run with its own timeout.
	run, rerr := s.Review.Run(context.Background(), spec)

	o.treeAfter, err = s.worktreeFingerprint(acq.Path)
	if err != nil {
		return inconclusive(o, classReviewUnverifiable, "could not fingerprint the worktree after the review: "+err.Error())
	}
	if run != nil {
		o.findings = capFindings(run.Output)
	}
	if o.treeAfter != o.treeBefore {
		detail := o.treeBefore + "→" + o.treeAfter
		if err := s.restoreTree(acq.Path); err != nil {
			detail += " (restoring the worktree failed: " + err.Error() + ")"
			log.Printf("error: phaserun: phase=%d reviewer mutated the worktree and it could not be restored: %v", phaseID, err)
		}
		return inconclusive(o, ClassReviewerMutatedTree, detail)
	}
	switch {
	case rerr != nil:
		return inconclusive(o, classReviewerNotStarted, rerr.Error())
	case run == nil:
		return inconclusive(o, classReviewerNotStarted, "the runner returned no run")
	case run.TimedOut:
		return inconclusive(o, classReviewerTimedOut, "killed by the hard timeout before it reported a verdict")
	}
	v, reasons := verify.ParseVerdict(run.Output)
	switch v {
	case verify.VerdictPass, verify.VerdictFail:
		o.verdict, o.detail = string(v), reasons
		return o
	}
	if !verify.HasVerdictLine(run.Output) {
		return inconclusive(o, classReviewerNoVerdict, fmt.Sprintf("the reviewer wrote %d bytes (exit code %d) but no VERDICT: line",
			len(run.Output), run.ExitCode))
	}
	return inconclusive(o, classReviewCouldNot, reasons)
}

// capFindings trims the reviewer's output to reviewFindingsCap, keeping the TAIL —
// the findings closest to the verdict line.
func capFindings(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= reviewFindingsCap {
		return s
	}
	return s[len(s)-reviewFindingsCap:]
}

// reviewState reads the phase's fix round and the session of the run under review.
func (s *Service) reviewState(phaseID int64) (round int, runUUID string) {
	if err := s.DB.QueryRow(`SELECT review_fix_round, COALESCE(run_session_uuid, '') FROM epic_phases WHERE id = ?`,
		phaseID).Scan(&round, &runUUID); err != nil {
		log.Printf("warning: phaserun: phase=%d review state unreadable: %v", phaseID, err)
	}
	return round, runUUID
}

func (s *Service) setReviewFixRound(phaseID int64, round int) {
	if _, err := s.DB.Exec(`UPDATE epic_phases SET review_fix_round = ? WHERE id = ?`, round, phaseID); err != nil {
		log.Printf("error: phaserun: phase=%d review_fix_round=%d: %v", phaseID, round, err)
	}
}

// recordReview writes the phase_reviews row. Best-effort: a lost record must not
// change the run's outcome, so a failure is logged.
func (s *Service) recordReview(phaseID int64, info phaseInfo, runUUID string, round int, started string, o reviewOutcome) {
	if _, err := s.DB.Exec(`
		INSERT INTO phase_reviews
			(scope, phase_id, workspace_task_id, session_uuid, run_session_uuid,
			 tree_before, tree_after, verdict, detail, findings, fix_round,
			 started_at, finished_at)
		VALUES ('phase', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		phaseID, strconv.FormatInt(info.WorkspaceTaskID, 10), o.sessionUUID, runUUID,
		o.treeBefore, o.treeAfter, o.verdict, o.detail, o.findings, round,
		started, s.ts()); err != nil {
		log.Printf("error: phaserun: phase=%d record review: %v", phaseID, err)
	}
}

// startReviewFix starts the ONE fix re-run a FAIL review asked for. Called from
// the tail of runAndHandle's defer, after the slot was released. A refused start
// (no free slot, a quota gate, …) is logged: the findings are already in the doc,
// so the next start of the phase still sees them.
func (s *Service) startReviewFix(phaseID int64) {
	uuid, err := s.StartWith(phaseID, StartOptions{Returned: true, ReviewFix: true})
	if err != nil {
		log.Printf("warning: phaserun: phase=%d review fix re-run refused: %v — the findings stay in the doc", phaseID, err)
		return
	}
	log.Printf("phaserun: phase=%d review fix re-run started uuid=%s", phaseID, uuid)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
