package wtcleanup

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

// fixture is a real repository plus a store that knows it as project 1, with
// helpers to grow worktrees, phases, cards and journal rows.
type fixture struct {
	t          *testing.T
	repo, root string // root: where the worktrees live (the daemon's root in production)
	db         *sql.DB
	plan       int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	f := &fixture{t: t, repo: t.TempDir(), root: t.TempDir()}
	f.git(f.repo, "init", "-q", "-b", "main")
	f.git(f.repo, "config", "user.email", "test@example.com")
	f.git(f.repo, "config", "user.name", "Test")
	f.write(f.repo, "README.md", "hello\n")
	f.git(f.repo, "add", "README.md")
	f.git(f.repo, "commit", "-qm", "init")

	db, err := store.Open(filepath.Join(t.TempDir(), "wtc.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	f.db = db
	f.exec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, ?, 'proj', 'Proj', '2026-10-01T00:00:00Z')`, f.repo)
	f.plan = f.insert(`INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id)
		VALUES (1, 'Cleanup plan', 'goal', 'running', '2026-10-01T00:00:00Z', 'workspace', '2026-10-01-cleanup')`)
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(dir, name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("exec: %v\n%s", err, q)
	}
}

func (f *fixture) insert(q string, args ...any) int64 {
	f.t.Helper()
	res, err := f.db.Exec(q, args...)
	if err != nil {
		f.t.Fatalf("insert: %v\n%s", err, q)
	}
	id, _ := res.LastInsertId()
	return id
}

// worktree adds a checkout on branch with one commit of its own (file → body)
// and journals it keep-unmerged.
func (f *fixture) worktree(name, branch, file, body string) string {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	f.git(f.repo, "worktree", "add", "-q", "-b", branch, path)
	f.write(path, file, body)
	f.git(path, "add", file)
	f.git(path, "commit", "-qm", "work on "+branch)
	f.journal(path, branch, "keep-unmerged")
	return path
}

func (f *fixture) journal(path, branch, verdict string) {
	f.t.Helper()
	f.exec(`INSERT INTO worktree_sweeps (ts, project_id, path, branch, verdict, reason) VALUES (?, 1, ?, ?, ?, 'test')`,
		time.Now().UTC().Format(time.RFC3339), path, branch, verdict)
}

// phase inserts a phase of the fixture plan whose run branch is swarm/phase-<id>.
func (f *fixture) phase(seq int, docStatus, runState, landing string) int64 {
	f.t.Helper()
	id := f.insert(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, doc_status, run_state, landing_state)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, f.plan, seq, "Step "+string(rune('A'+seq)), "/ws/phase-"+string(rune('0'+seq))+".md",
		docStatus, runState, landing)
	f.exec(`UPDATE epic_phases SET run_branch = 'swarm/phase-' || id WHERE id = ?`, id)
	return id
}

// squashIntoMain lands branch's content on main as ONE new commit, the way a
// squash merge on the host does — no ref ends up containing the branch's commits.
func (f *fixture) squashIntoMain(branch string) {
	f.t.Helper()
	f.git(f.repo, "merge", "--squash", branch)
	f.git(f.repo, "commit", "-qm", "squash "+branch)
}

func byPath(s Scan) (map[string]Candidate, map[string]string) {
	cands := map[string]Candidate{}
	for _, c := range s.Candidates {
		cands[c.Path] = c
	}
	skipped := map[string]string{}
	for _, sk := range s.Skipped {
		skipped[sk.Path] = sk.Reason
	}
	return cands, skipped
}

func canon(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

// The three proof outcomes the phase is about, on one real repository.
func TestCandidates_ProofKinds(t *testing.T) {
	f := newFixture(t)

	// (a) squash-merged: the content is in main though no ref contains the commit.
	squashed := f.phase(1, "done", "done", "none")
	pa := f.worktree("phase-a", "swarm/phase-"+itoa(squashed), "a.md", "landed by squash\n")
	f.squashIntoMain("swarm/phase-" + itoa(squashed))

	// (b) landing merged: the landing record is the proof, no git work needed.
	landed := f.phase(2, "done", "done", "merged")
	pb := f.worktree("phase-b", "swarm/phase-"+itoa(landed), "b.md", "pr merged\n")

	// (c) real unmerged work under a closed phase: no proof, counted only.
	unmerged := f.phase(3, "done", "done", "none")
	pc := f.worktree("phase-c", "swarm/phase-"+itoa(unmerged), "c.md", "only here\n")

	objectsBefore := f.git(f.repo, "count-objects")
	scan, err := Candidates(f.db, ExecGit{}, nil)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	cands, skipped := byPath(scan)
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none", skipped)
	}
	want := map[string]string{pa: ProofContentDiff, pb: ProofLanding, pc: ProofNone}
	for path, kind := range want {
		c, ok := cands[canon(t, path)]
		if !ok {
			c, ok = cands[path]
		}
		if !ok {
			t.Errorf("%s: not a candidate (got %v)", path, scan.Candidates)
			continue
		}
		if c.Proof.Kind != kind {
			t.Errorf("%s: proof = %s (%s), want %s", filepath.Base(path), c.Proof.Kind, c.Proof.Detail, kind)
		}
		if c.PhaseID == 0 || c.TaskID != f.plan || !strings.Contains(c.Owner, "Cleanup plan · Phase") {
			t.Errorf("%s: owner = phase %d task %d %q", filepath.Base(path), c.PhaseID, c.TaskID, c.Owner)
		}
	}

	sum := Summarize(scan)
	if len(sum) != 1 || sum[0].ByProof[ProofLanding] != 1 || sum[0].ByProof[ProofContentDiff] != 1 || sum[0].ByProof[ProofNone] != 1 {
		t.Errorf("summary = %+v, want one of each proof kind", sum)
	}
	// The content diff must not write to the repository: its merged trees go to
	// a scratch object directory.
	if after := f.git(f.repo, "count-objects"); after != objectsBefore {
		t.Errorf("repository objects changed during the scan: %q → %q", objectsBefore, after)
	}
}

// Only a CLOSED owner yields a candidate: open, active, unknown, locked, gone
// and a path whose newest journal row is no longer keep-unmerged are not.
func TestCandidates_OnlyClosedOwnersAreCandidates(t *testing.T) {
	f := newFixture(t)

	open := f.phase(1, "in_progress", "idle", "none")
	pOpen := f.worktree("phase-open", "swarm/phase-"+itoa(open), "o.md", "open\n")

	running := f.phase(2, "done", "running", "none")
	pActive := f.worktree("phase-running", "swarm/phase-"+itoa(running), "r.md", "running\n")

	agentDir := filepath.Join(f.repo, ".claude", "worktrees")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pAgent := filepath.Join(agentDir, "agent-abc123")
	f.git(f.repo, "worktree", "add", "-q", "-b", "worktree-agent-abc123", pAgent)
	f.write(pAgent, "x.md", "x\n")
	f.git(pAgent, "add", "x.md")
	f.git(pAgent, "commit", "-qm", "agent work")
	f.journal(pAgent, "worktree-agent-abc123", "keep-unmerged")

	lockedPhase := f.phase(3, "done", "done", "none")
	pLocked := f.worktree("phase-locked", "swarm/phase-"+itoa(lockedPhase), "l.md", "locked\n")
	f.git(f.repo, "worktree", "lock", pLocked)

	gone := filepath.Join(f.root, "phase-gone")
	f.journal(gone, "swarm/phase-999", "keep-unmerged")

	nowSkipped := f.phase(4, "done", "done", "none")
	pNowSkip := f.worktree("phase-nowskip", "swarm/phase-"+itoa(nowSkipped), "s.md", "s\n")
	f.journal(pNowSkip, "swarm/phase-"+itoa(nowSkipped), "skip") // newest row wins

	// A board card: archived, so closed; its content is in main.
	card := f.insert(`INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id, board_column)
		VALUES (1, 'Fix the thing', 'p', 'needs_review', '2026-10-01T00:00:00Z', 'queue', 'T-abc', 'archived')`)
	pCard := f.worktree("T-abc", "swarm/T-abc", "card.md", "card work\n")
	f.squashIntoMain("swarm/T-abc")

	scan, err := Candidates(f.db, ExecGit{}, nil)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	cands, skipped := byPath(scan)
	lookup := func(p string) string {
		if r, ok := skipped[canon(t, p)]; ok {
			return r
		}
		return skipped[p]
	}
	wantSkip := map[string]string{
		pOpen: SkipOpenOwner, pActive: SkipActive, pAgent: SkipUnknownOwner, pLocked: SkipLocked, gone: SkipGone,
	}
	for p, reason := range wantSkip {
		if got := lookup(p); got != reason {
			t.Errorf("%s: skip reason = %q, want %q", filepath.Base(p), got, reason)
		}
	}
	if r := lookup(pNowSkip); r != "" {
		t.Errorf("a path whose newest row is skip was considered (%s)", r)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %v, want only the card", scan.Candidates)
	}
	for _, c := range cands {
		if canon(t, c.Path) != canon(t, pCard) || c.TaskID != card || c.PhaseID != 0 {
			t.Errorf("candidate = %+v, want the card %d at %s", c, card, pCard)
		}
		if c.Proof.Kind != ProofContentDiff || !strings.HasPrefix(c.Owner, "T-abc: ") {
			t.Errorf("card candidate proof/owner = %s %q", c.Proof.Kind, c.Owner)
		}
	}
}

// Liveness is the last veto: a closed owner with a process inside is active.
func TestCandidates_LiveWorktreeIsActive(t *testing.T) {
	f := newFixture(t)
	ph := f.phase(1, "done", "done", "merged")
	p := f.worktree("phase-live", "swarm/phase-"+itoa(ph), "l.md", "l\n")

	scan, err := Candidates(f.db, ExecGit{}, alwaysBusy{})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(scan.Candidates) != 0 {
		t.Errorf("candidates = %v, want none while live", scan.Candidates)
	}
	_, skipped := byPath(scan)
	if r := skipped[canon(t, p)] + skipped[p]; r != SkipActive {
		t.Errorf("skip reason = %q, want active", r)
	}
}

// Uncommitted files are not part of any proof; the detail names them.
func TestCandidates_DetailNamesUncommittedPaths(t *testing.T) {
	f := newFixture(t)
	ph := f.phase(1, "done", "done", "merged")
	p := f.worktree("phase-dirty", "swarm/phase-"+itoa(ph), "d.md", "d\n")
	f.write(p, "scratch.md", "uncommitted\n")

	scan, err := Candidates(f.db, ExecGit{}, nil)
	if err != nil || len(scan.Candidates) != 1 {
		t.Fatalf("Candidates = %v, %v", scan.Candidates, err)
	}
	if d := scan.Candidates[0].Proof.Detail; !strings.Contains(d, "1 uncommitted path(s)") {
		t.Errorf("detail = %q, want the uncommitted count", d)
	}
}

// A repository git cannot list is a repo-error for this pass, never "gone":
// nothing is known about its worktrees.
func TestCandidates_UnreadableRepoIsNotGone(t *testing.T) {
	f := newFixture(t)
	ph := f.phase(1, "done", "done", "merged")
	f.worktree("phase-x", "swarm/phase-"+itoa(ph), "x.md", "x\n")

	scan, err := Candidates(f.db, failingGit{}, nil)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(scan.Candidates) != 0 || len(scan.Skipped) != 1 || scan.Skipped[0].Reason != SkipRepoError {
		t.Errorf("scan = %+v, want one repo-error skip", scan)
	}
}

type failingGit struct{ ExecGit }

func (failingGit) Worktrees(string) ([]wtjanitor.Worktree, error) {
	return nil, os.ErrPermission
}

type alwaysBusy struct{}

func (alwaysBusy) Busy(string) (bool, error) { return true, nil }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
