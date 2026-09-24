package improve

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
)

// Exec is the git/gh + filesystem boundary of the apply pipeline. Real code
// shells out and touches disk (OSExec); tests fake it so the whole pipeline —
// worktree isolation, guardrails, PR creation — runs deterministically with no
// process spawned and no repo touched.
type Exec interface {
	// Run executes name+args in dir (empty = inherit) and returns combined
	// stdout+stderr. A non-zero exit is a non-nil error whose message includes
	// the captured output.
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	MkdirTemp() (string, error)
	RemoveAll(path string) error
}

// maxChangedLines caps the total (added+deleted) lines a single improvement PR
// may touch — the third guardrail. An agent rewrite that balloons past this is
// almost certainly off the rails and must go through a human, not the loop.
const maxChangedLines = 120

// corePack is the vendor-neutral plugin whose version the marketplace manifest
// tracks; a bump there is mirrored into .claude-plugin/marketplace.json.
const corePack = "core"

// gateErr names a guardrail in the failure it produces, so the failed row's
// error column always identifies which gate rejected the diff.
type gateErr struct {
	gate string
	msg  string
}

func (e *gateErr) Error() string { return e.gate + ": " + e.msg }

// Apply runs the phase-4 apply/PR pipeline for one APPROVED proposal. It never
// returns a pipeline error to the caller: outcomes land on the row —
// approved→applied (with pr_url) on success, approved→failed (with the failing
// gate named in error) on a guardrail/sha rejection, or a left-approved row
// with the error stored when gh is missing/unauthenticated (idempotent
// re-run). A non-nil return is reserved for pre-flight problems (bad id, DB
// error) the API surfaces as 4xx/5xx.
func (s *Service) Apply(ctx context.Context, proposalID int64) error {
	var agent, agentPath, targetKind, targetPath, baseSHA, diff, createdAt, status string
	err := s.DB.QueryRow(`
		SELECT agent, agent_path, target_kind, target_path, base_sha256, diff, created_at, status
		  FROM agent_change_proposals WHERE id = ?`, proposalID).
		Scan(&agent, &agentPath, &targetKind, &targetPath, &baseSHA, &diff, &createdAt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProposalNotFound
	}
	if err != nil {
		return err
	}
	if status != "approved" {
		return fmt.Errorf("proposal %d is %s, not approved", proposalID, status)
	}

	// The scope gate needs the target file as a repo-relative path. Since
	// generation resolves the source from origin/main, the stored path is already
	// repo-relative (plugins/<pack>/agents/<name>.md, or
	// plugins/<pack>/skills/<name>/SKILL.md for a skill proposal); repoRel
	// normalizes it and blocks any escape.
	stored := agentPath
	if targetKind == TargetSkill {
		stored = targetPath
	}
	relTarget, err := repoRel(s.Repo, stored)
	if err != nil {
		return s.markFailed(proposalID, err)
	}
	// The stored target must be a shape the loop is allowed to edit at all. A
	// needs_target row (empty path) or a hand-edited row pointing at
	// .github/workflows/x.yml dies here, before any git op — the path-scope gate
	// inside the worktree would also catch it, but only after fetching and
	// creating a branch.
	if !isEditableTarget(relTarget) {
		return s.markFailed(proposalID, &gateErr{gate: "path scope",
			msg: fmt.Sprintf("%q is not an agent definition or a SKILL.md", relTarget)})
	}

	// Step 2: the diff was generated against the target's origin/main content; if
	// origin/main drifted since (a merge landed), the patch context is stale —
	// fail before any worktree. Re-read the CURRENT origin/main content through
	// the same Exec boundary generation used, so generate-time and approve-time
	// are both origin/main-based (no working-tree drift).
	curContent, err := s.Exec.Run(context.Background(), s.Repo, "git", "show", "origin/main:"+relTarget)
	if err != nil {
		return s.markFailed(proposalID, fmt.Errorf("re-read target at origin/main: %w", err))
	}
	sum := sha256.Sum256([]byte(curContent))
	if hex.EncodeToString(sum[:]) != baseSHA {
		return s.markFailed(proposalID, errors.New("agent changed since proposal (sha256 mismatch)"))
	}

	branch, err := branchName(targetKind, agent, relTarget, createdAt)
	if err != nil {
		return s.markFailed(proposalID, err)
	}

	if ghErr := s.runApply(ctx, proposalID, targetKind, agent, relTarget, diff, branch); ghErr != nil {
		// gh outage: leave the proposal approved so the dashboard can re-run.
		log.Printf("warn: improve: apply %d (%s %s): PR step failed, left approved: %v",
			proposalID, targetKind, agent, ghErr)
		_, uerr := s.DB.Exec(
			`UPDATE agent_change_proposals SET error = ? WHERE id = ? AND status = 'approved'`,
			ghErr.Error(), proposalID)
		return uerr
	}
	return nil
}

// runApply performs the worktree-scoped part of the pipeline. A *gateErr or
// sha/git failure is converted to a failed row here and returns nil; a PR-step
// (gh) failure is returned so Apply can keep the proposal approved. targetPath is
// the repo-relative path of the target file (an agent definition or a SKILL.md)
// — the only path the model's diff is permitted to touch (the path-scope gate).
func (s *Service) runApply(ctx context.Context, id int64, targetKind, agent, targetPath, diff, branch string) (ghErr error) {
	tmp, err := s.Exec.MkdirTemp()
	if err != nil {
		return s.failWrap(id, err)
	}
	defer func() {
		// Always remove the worktree — registration first (so a later add on the
		// same branch is clean), then the dir.
		if _, rmErr := s.Exec.Run(ctx, s.Repo, "git", "worktree", "remove", "--force", tmp); rmErr != nil {
			log.Printf("warn: improve: worktree remove %s: %v", tmp, rmErr)
		}
		if rmErr := s.Exec.RemoveAll(tmp); rmErr != nil {
			log.Printf("warn: improve: rm %s: %v", tmp, rmErr)
		}
	}()

	// Step 3: fetch + isolated worktree on the generated branch.
	if out, err := s.Exec.Run(ctx, s.Repo, "git", "fetch", "origin", "main"); err != nil {
		return s.failWrap(id, fmt.Errorf("git fetch: %v (%s)", err, out))
	}
	if out, err := s.Exec.Run(ctx, s.Repo, "git", "worktree", "add", "-f", tmp, "origin/main", "-b", branch); err != nil {
		return s.failWrap(id, fmt.Errorf("git worktree add: %v (%s)", err, out))
	}

	// Step 3 cont.: write + apply the patch.
	patch := filepath.Join(tmp, ".improve.patch")
	if err := s.Exec.WriteFile(patch, []byte(diff)); err != nil {
		return s.failWrap(id, err)
	}
	if out, err := s.Exec.Run(ctx, tmp, "git", "apply", "--check", ".improve.patch"); err != nil {
		return s.failWrap(id, &gateErr{gate: "git apply", msg: fmt.Sprintf("patch does not apply cleanly: %v (%s)", err, out)})
	}
	if out, err := s.Exec.Run(ctx, tmp, "git", "apply", ".improve.patch"); err != nil {
		return s.failWrap(id, &gateErr{gate: "git apply", msg: fmt.Sprintf("%v (%s)", err, out)})
	}
	_ = s.Exec.RemoveAll(patch)

	// Step 3b: STAGE-BEFORE-GATE. The gate must reflect exactly what WILL be
	// committed — including files the model's diff CREATED. A create is UNTRACKED
	// after `git apply`, so `git diff HEAD` (worktree vs HEAD, tracked-only) omits
	// it; the gate would pass and the later `git add -A` would sneak
	// .github/workflows/evil.yml into the committed+pushed PR (the P0 bypass), and
	// the injected file's lines would also dodge the ≤120 cap. Staging the full
	// content NOW (`git add -A`) puts every create/edit/rename into the index, and
	// the gate then reads the STAGED diff (`--cached`) — the exact tree that will
	// be committed.
	if out, err := s.Exec.Run(ctx, tmp, "git", "add", "-A"); err != nil {
		return s.failWrap(id, fmt.Errorf("git add: %v (%s)", err, out))
	}

	// HARD path-scope gate. git apply already blocks ../ traversal and symlink
	// escape, but a diff can still create/modify sibling top-level paths
	// (.github/workflows/*, scripts/*, .gitleaks.toml) that every downstream gate
	// misses. Compute the changed-path list from the STAGED diff and reject if
	// ANYTHING outside the target agent file was touched. The semver bump happens
	// AFTER this gate and re-stages its two manifests just before commit, so at
	// gate time the ONLY allowed changed path is the agent file itself.
	changed, total, err := s.changedPaths(ctx, tmp)
	if err != nil {
		return s.failWrap(id, err)
	}
	if err := checkPathScope(changed, targetPath); err != nil {
		return s.failWrap(id, err)
	}

	// Step 4: guardrails, in order (reusing the changed-path list from above).
	if err := s.guardrails(ctx, tmp, changed, total); err != nil {
		return s.failWrap(id, err)
	}

	// Step 5: the diff lives inside exactly one plugin pack ⇒ patch-bump that
	// pack's manifest (and, for core, the marketplace metadata in lockstep). This
	// writes the manifest files AFTER the gate; the re-stage below adds them to
	// the index so they land in the commit but were absent at gate time.
	if pack, ok := packOnly(changed); ok {
		if err := s.bumpPackSemver(tmp, pack); err != nil {
			return s.failWrap(id, err)
		}
	}

	// Step 6: re-stage so the post-gate manifest bump is included, then commit +
	// push (force-with-lease tolerates an idempotent re-run on the same generated
	// branch after a gh outage). The scope gate already ran against the staged
	// tree; this second `git add -A` can only add the two manifests bumpCoreSemver
	// wrote (the agent file is already staged, and the gate proved nothing else
	// was touched).
	if out, err := s.Exec.Run(ctx, tmp, "git", "add", "-A"); err != nil {
		return s.failWrap(id, fmt.Errorf("git add: %v (%s)", err, out))
	}
	label, commitMsg, title := prLabels(targetKind, agent, targetPath)
	if out, err := s.Exec.Run(ctx, tmp, "git", "commit", "-m", commitMsg); err != nil {
		return s.failWrap(id, fmt.Errorf("git commit: %v (%s)", err, out))
	}
	if out, err := s.Exec.Run(ctx, tmp, "git", "push", "--force-with-lease", "-u", "origin", branch); err != nil {
		// A push failure is treated like a gh outage — recoverable, stays approved.
		return fmt.Errorf("git push: %v (%s)", err, out)
	}

	// Step 7: open the PR.
	body := prBody(targetKind, label, targetPath)
	out, err := s.Exec.Run(ctx, tmp, "gh", "pr", "create",
		"--head", branch, "--title", title, "--body", body)
	if err != nil {
		return fmt.Errorf("gh pr create: %v (%s)", err, out)
	}
	prURL := firstURL(out)
	if prURL == "" {
		return fmt.Errorf("gh pr create returned no PR URL: %q", out)
	}

	// Step 7 cont.: applied + pr_url. Guarded on status so a concurrent
	// reject/re-run cannot be silently overwritten.
	now := fmtTS(time.Now())
	if _, err := s.DB.Exec(`
		UPDATE agent_change_proposals
		   SET status = 'applied', pr_url = ?, error = NULL, decided_at = COALESCE(decided_at, ?)
		 WHERE id = ? AND status = 'approved'`, prURL, now, id); err != nil {
		return fmt.Errorf("record applied: %v", err)
	}
	return nil
}

// changedPaths runs the numstat diff of the STAGED tree in the worktree and
// parses it into the changed-path list + total (added+deleted) line count. It is
// called once, right after `git add -N .`, so both the path-scope gate and the
// guardrails reuse the same file list (a single git invocation).
//
// The command is the single source of truth for BOTH the scope gate and the
// ≤120-line cap. Three flags close the P0 bypass and its rename/quoting cousins:
//   - --cached surfaces staged untracked CREATES (with `git add -N .` beforehand),
//     so a newly created evil file is an add row the scope gate catches instead of
//     an invisible untracked file that `git add -A` later sneaks into the commit.
//   - --no-renames forces a rename to split into a delete row + an add row, so the
//     created DESTINATION path is its own entry the allowed-set check sees (never
//     an `old => new` token that would parse to an ambiguous path).
//   - core.quotepath=false keeps non-ASCII paths verbatim instead of arriving as
//     "\303\251…" quoted strings that would dodge the exact string compare.
func (s *Service) changedPaths(ctx context.Context, tmp string) ([]string, int, error) {
	// "add\tdel\tpath" per file, one path column per row under --no-renames.
	numstat, err := s.Exec.Run(ctx, tmp,
		"git", "-c", "core.quotepath=false", "diff", "--cached", "--numstat", "--no-renames", "HEAD")
	if err != nil {
		return nil, 0, &gateErr{gate: "changed lines", msg: fmt.Sprintf("git diff: %v (%s)", err, numstat)}
	}
	changed, total, err := parseNumstat(numstat)
	if err != nil {
		return nil, 0, &gateErr{gate: "changed lines", msg: err.Error()}
	}
	return changed, total, nil
}

// checkPathScope is the HARD apply-scope gate. It rejects (gate "path scope") if
// any changed path is outside the single target file. The gate runs against the
// STAGED (intent-to-add) tree BEFORE the semver bump writes-and-stages its
// manifests, so the ONLY path the model's diff is allowed to have touched is the
// target itself — anything else (a sibling CI workflow, a renamed-away
// destination, a manifest the model tried to edit directly) is a bypass attempt
// and must be rejected. The bump's own manifest writes are added to the commit
// afterward by our code, never present at gate time.
//
// Phase 5 widens WHICH file may be the target (an agent definition or a
// SKILL.md) and changes NOTHING about this gate's semantics — deliberately. A
// skill that references sibling resources/*.md still cannot have them edited by
// the loop: exactly one path, and it is the target.
func checkPathScope(changed []string, targetPath string) error {
	for _, p := range changed {
		if p != targetPath {
			return &gateErr{gate: "path scope",
				msg: fmt.Sprintf("diff touches %s outside the target file %s", p, targetPath)}
		}
	}
	return nil
}

// repoRel normalizes the stored agent_path to a repo-relative slash path for
// the scope gate. Since generation now resolves the source from origin/main,
// agent_path is ALREADY repo-relative (plugins/<pack>/agents/<name>.md); a
// relative path is cleaned + slash-normalized and validated to not escape the
// repo (filepath.Rel would misbehave with a relative second arg, so we do NOT
// call it here). An absolute path (legacy rows generated before this change) is
// still made relative to the repo root, rejecting any escape.
func repoRel(repo, agentPath string) (string, error) {
	if !filepath.IsAbs(agentPath) {
		clean := filepath.ToSlash(filepath.Clean(agentPath))
		if clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
			return "", fmt.Errorf("agent path %q escapes repo %q", agentPath, repo)
		}
		return clean, nil
	}
	rel, err := filepath.Rel(repo, agentPath)
	if err != nil {
		return "", fmt.Errorf("agent path %q not under repo %q: %w", agentPath, repo, err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("agent path %q escapes repo %q", agentPath, repo)
	}
	return filepath.ToSlash(rel), nil
}

// guardrails runs the three gates in order over the pre-computed changed-path
// list (from changedPaths). Any gate failure is a *gateErr naming it.
func (s *Service) guardrails(ctx context.Context, tmp string, changed []string, total int) error {
	// 1) neutrality scan.
	out, err := s.Exec.Run(ctx, tmp, "bash", "scripts/scan-flavor.sh")
	if err != nil {
		return &gateErr{gate: "scan-flavor", msg: fmt.Sprintf("scan errored: %v (%s)", err, out)}
	}
	if !strings.Contains(out, "✓ clean") {
		return &gateErr{gate: "scan-flavor", msg: "neutrality scan not clean"}
	}

	// 2) frontmatter on every changed agent definition OR skill definition file.
	// Both carry the same contract — a leading `---` with name: and description:
	// inside the first 15 lines — so one predicate covers the CI agent gate and
	// the SKILL.md shape a skill proposal must not break.
	for _, p := range changed {
		if !isEditableTarget(p) {
			continue
		}
		content, rerr := s.Exec.ReadFile(filepath.Join(tmp, p))
		if rerr != nil {
			return &gateErr{gate: "frontmatter", msg: fmt.Sprintf("cannot read %s: %v", p, rerr)}
		}
		if !frontmatterOK(string(content)) {
			return &gateErr{gate: "frontmatter", msg: fmt.Sprintf("%s missing name/description in the first 15 lines", p)}
		}
	}

	// 3) diff-size cap.
	if total > maxChangedLines {
		return &gateErr{gate: "changed lines",
			msg: fmt.Sprintf("%d changed lines exceeds the %d-line cap", total, maxChangedLines)}
	}
	return nil
}

// markFailed flips the proposal to failed with the given error text and returns
// nil (the outcome is the row). A guarded write keeps it from clobbering a
// concurrent reject.
func (s *Service) markFailed(id int64, cause error) error {
	log.Printf("warn: improve: apply %d failed: %v", id, cause)
	_, err := s.DB.Exec(`
		UPDATE agent_change_proposals
		   SET status = 'failed', error = ?, decided_at = COALESCE(decided_at, ?)
		 WHERE id = ? AND status = 'approved'`, cause.Error(), fmtTS(time.Now()), id)
	return err
}

// failWrap is markFailed adapted to runApply's ghErr return shape: it records
// the failed row and returns nil (so Apply does NOT also store a gh error).
func (s *Service) failWrap(id int64, cause error) error {
	_ = s.markFailed(id, cause)
	return nil
}

// isAgentFile matches the CI frontmatter glob plugins/*/agents/*.md.
func isAgentFile(p string) bool {
	if !strings.HasPrefix(p, "plugins/") || !strings.HasSuffix(p, ".md") {
		return false
	}
	parts := strings.Split(p, "/")
	return len(parts) == 4 && parts[2] == "agents"
}

// parseNumstat parses `git diff --numstat` output into (paths, totalChanged).
// Binary files report "-\t-\tpath"; those add 0 to the total but still count as
// changed paths. Callers pass --no-renames, so every row is exactly
// "added\tdeleted\tpath" with a single path column (no `old => new` rename
// token), and core.quotepath=false keeps that path verbatim rather than quoting
// non-ASCII bytes.
//
// The grammar itself — TAB split (never whitespace: a whitespace split would
// truncate `plugins/a/x.md y.md` to its last token, so two rows could both parse
// to the target path and smuggle an extra file past checkPathScope), fail-CLOSED
// on a row with fewer than three columns — lives in internal/gitstat, shared with
// the phase-run actuals that measure a run branch with the same command.
func parseNumstat(out string) (paths []string, total int, err error) {
	files, err := gitstat.ParseNumstat(out)
	if err != nil {
		return nil, 0, err
	}
	added, removed := gitstat.Totals(files)
	if len(files) == 0 {
		return nil, 0, nil
	}
	return gitstat.Paths(files), added + removed, nil
}

// branchName derives the deterministic branch {kind}-improve/{slug}-{yyyymmdd}
// from the proposal's created_at (RFC-ish "2006-01-02T15:04:05.000Z"), so the
// branch is reproducible on a re-run and independent of wall-clock in tests.
//
// An agent row's slug is its registry key. A SKILL row's `agent` column holds
// the lesson identity — a whole sentence — so the slug comes from the target
// path's skill name instead; a 60-character branch named after a retro sentence
// is not a ref anyone can read.
func branchName(targetKind, agent, targetPath, createdAt string) (string, error) {
	prefix, slug := "agent-improve/", branchSlug(agent)
	if targetKind == TargetSkill {
		prefix = "skill-improve/"
		if name := skillNameOf(targetPath); name != "" {
			slug = branchSlug(name)
		}
	}
	day, err := createdDay(createdAt)
	if err != nil {
		return "", err
	}
	return prefix + slug + "-" + day, nil
}

// prLabels renders the three human-facing strings of the PR step: the display
// label of the target, the conventional-commit subject, and the PR title. The
// scope is the owning pack (plugins/<pack>/…) rather than a hardcoded `core`, so
// a domain pack's skill does not open a PR claiming to change core.
func prLabels(targetKind, agent, targetPath string) (label, commitMsg, title string) {
	scope := corePack
	if pack, ok := packOf(targetPath); ok {
		scope = pack
	}
	noun, label := "agent", agent
	if targetKind == TargetSkill {
		noun = "skill"
		if name := skillNameOf(targetPath); name != "" {
			label = name
		}
	}
	return label,
		fmt.Sprintf("feat(%s): improve %s %s (advisor-evidenced)", scope, label, noun),
		fmt.Sprintf("feat(%s): improve %s %s", scope, label, noun)
}

// createdDay extracts YYYYMMDD from the stored created_at timestamp.
func createdDay(createdAt string) (string, error) {
	for _, layout := range []string{tsFmt, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(layout, createdAt); err == nil {
			return t.UTC().Format("20060102"), nil
		}
	}
	if len(createdAt) >= 10 {
		if t, err := time.Parse("2006-01-02", createdAt[:10]); err == nil {
			return t.Format("20060102"), nil
		}
	}
	return "", fmt.Errorf("cannot parse created_at %q for branch date", createdAt)
}

// branchSlug reduces an agent key to a git-ref-safe slug (":" from "core:x"
// notation → "-", everything unusual → "-").
func branchSlug(agent string) string {
	var b strings.Builder
	for _, r := range agent {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// firstURL returns the first https URL found in s (gh pr create echoes the PR
// URL, sometimes amid other lines).
func firstURL(s string) string {
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "https://") {
			return strings.TrimSpace(f)
		}
	}
	return ""
}

// prBody renders the PR description: rationale/evidence pointer + the advisor
// verify-plan line the retro loop closes on. The two kinds cite DIFFERENT
// evidence and DIFFERENT verify metrics, because they are answers to different
// questions — an agent rewrite answers "this agent keeps failing", a skill edit
// answers "the fleet keeps re-learning this lesson" — and a reviewer who cannot
// tell which claim the PR is making cannot check it.
func prBody(targetKind, label, targetPath string) string {
	if targetKind == TargetSkill {
		return fmt.Sprintf(`Automated skill improvement generated by the swarmery retro self-improvement loop.

**Skill:** %s (`+"`%s`"+`)

This diff was produced by the advisor-evidenced rewriter from a recurring
retrospective lesson (R11: one lesson identity learned in ≥3 distinct tasks) and
passed the neutrality, frontmatter, path-scope, and diff-size guardrails.

**Verify plan:** advisor verifies lesson_task_count for this lesson falls over
the 14 days after merge; if the fleet keeps re-learning it, revert this PR.

Do not auto-merge — human review required.`, label, targetPath)
	}
	return fmt.Sprintf(`Automated agent improvement generated by the swarmery retro self-improvement loop.

**Agent:** %s

This diff was produced by the advisor-evidenced rewriter from real session
telemetry (scorecard regressions, ledger assessments, and transcript excerpts)
and passed the neutrality, frontmatter, and diff-size guardrails.

**Verify plan:** advisor verifies behavior_failed_run_share improves by ≥20%%
for %s 7 days after merge; if it does not regress-free, revert this PR.

Do not auto-merge — human review required.`, label, label)
}

// OSExec is the production Exec: real process spawns + filesystem.
type OSExec struct {
	// Timeout bounds one git/gh invocation (0 → osExecTimeout).
	Timeout time.Duration
}

// osExecTimeout bounds a single git/gh command.
const osExecTimeout = 3 * time.Minute

func (e OSExec) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = osExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (e OSExec) ReadFile(path string) ([]byte, error)     { return os.ReadFile(path) }
func (e OSExec) WriteFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }
func (e OSExec) RemoveAll(path string) error              { return os.RemoveAll(path) }
func (e OSExec) MkdirTemp() (string, error)               { return os.MkdirTemp("", "swarmery-improve-*") }
