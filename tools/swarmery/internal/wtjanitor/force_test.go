package wtjanitor

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// forceFixture is a real repository with one daemon-owned worktree on
// swarm/phase-7 carrying a commit no other ref has (the keep-unmerged case) and
// an uncommitted file, plus a store that knows the repository as a project.
type forceFixture struct {
	repo, daemonRoot, wtPath string
	run                      func(args ...string) string
	db                       *sql.DB
	commit                   string
	now                      time.Time
}

func newForceFixture(t *testing.T) forceFixture {
	t.Helper()
	repo, run := testRepo(t)
	daemonRoot := t.TempDir()
	wtPath := filepath.Join(daemonRoot, "proj", "phase-7")
	run("worktree", "add", "-q", "-b", "swarm/phase-7", wtPath)
	write(t, filepath.Join(wtPath, "work.md"), "committed on the run branch only\n")
	gitIn(t, wtPath, "add", "work.md")
	gitIn(t, wtPath, "commit", "-qm", "phase work")
	commit := gitIn(t, wtPath, "rev-parse", "HEAD")
	write(t, filepath.Join(wtPath, "scratch.md"), "never committed\n")

	db, err := store.Open(filepath.Join(t.TempDir(), "force.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (3, ?, 'proj', 'Proj', '2026-10-01T00:00:00Z')`, repo); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return forceFixture{repo: repo, daemonRoot: daemonRoot, wtPath: wtPath, run: run, db: db, commit: commit,
		now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
}

func (f forceFixture) service(rem Remover, live Liveness) *Service {
	return &Service{DB: f.db, Git: RepoGit{}, Live: live, DaemonRoot: f.daemonRoot, Remover: rem,
		now: func() time.Time { return f.now }}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return out
}

type busyLive struct{}

func (busyLive) Busy(string) (bool, error) { return true, nil }

// The contract: a salvage branch is written BEFORE the removal, it reaches the
// branch's unmerged commit AND the uncommitted file, and the journal records
// force-unmerged with that branch.
func TestForceUnmerged_WritesSalvageBranchThenRemoves(t *testing.T) {
	f := newForceFixture(t)
	s := f.service(NewRealRemover(&worktree.Manager{Git: worktree.ExecGit{}}), idleLive{})

	res := s.ForceUnmerged(f.repo, []string{f.wtPath})
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1", len(res))
	}
	r := res[0]
	if r.Outcome != ForceRemoved {
		t.Fatalf("outcome = %q (%s), want removed", r.Outcome, r.Reason)
	}
	if want := "salvage/swarm-phase-7-20261022T120000Z"; r.SalvageBranch != want {
		t.Errorf("salvage branch = %q, want %q", r.SalvageBranch, want)
	}
	if _, err := os.Stat(f.wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still present: %v", err)
	}
	// The unmerged commit is reachable from the salvage ref…
	if _, err := run(f.repo, "merge-base", "--is-ancestor", f.commit, r.SalvageBranch); err != nil {
		t.Errorf("unmerged commit %s not reachable from %s: %v", f.commit, r.SalvageBranch, err)
	}
	// …and so is the file that was never committed.
	if out := gitIn(t, f.repo, "cat-file", "-p", r.SalvageBranch+":scratch.md"); !strings.Contains(out, "never committed") {
		t.Errorf("salvaged scratch.md = %q", out)
	}
	// The owned run branch went with the checkout: the salvage ref holds its commits.
	if out := f.run("branch", "--list", "swarm/phase-7"); out != "" {
		t.Errorf("run branch still present: %q", out)
	}

	var verdict, reason, salvage string
	var removed int
	var project sql.NullInt64
	if err := f.db.QueryRow(`SELECT verdict, reason, COALESCE(salvage_branch, ''), removed, project_id FROM worktree_sweeps`).
		Scan(&verdict, &reason, &salvage, &removed, &project); err != nil {
		t.Fatalf("journal row: %v", err)
	}
	if verdict != string(VerdictForceUnmerged) || removed != 1 || salvage != r.SalvageBranch {
		t.Errorf("journal = %s removed=%d salvage=%q, want force-unmerged removed=1 salvage=%q", verdict, removed, salvage, r.SalvageBranch)
	}
	if !project.Valid || project.Int64 != 3 {
		t.Errorf("journal project_id = %v, want 3", project)
	}
}

// A path that vanished between propose and apply is reported, not an error,
// and leaves no journal row.
func TestForceUnmerged_GonePathIsReported(t *testing.T) {
	f := newForceFixture(t)
	rem := &recordingRemover{}
	s := f.service(rem, idleLive{})

	res := s.ForceUnmerged(f.repo, []string{filepath.Join(f.daemonRoot, "proj", "phase-404")})
	if res[0].Outcome != ForceGone {
		t.Errorf("outcome = %q, want gone", res[0].Outcome)
	}
	if rem.calls() != 0 {
		t.Errorf("remover called %d times for a gone path", rem.calls())
	}
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM worktree_sweeps`).Scan(&n)
	if n != 0 {
		t.Errorf("journal rows = %d, want 0 for a gone path", n)
	}
}

// Vetoes hold even on an operator click: live, locked and foreign checkouts are
// refused and nothing reaches the remover — not even a salvage ref is written.
func TestForceUnmerged_VetoesRefuseAndTouchNothing(t *testing.T) {
	f := newForceFixture(t)

	rem := &recordingRemover{}
	if r := f.service(rem, busyLive{}).ForceUnmerged(f.repo, []string{f.wtPath})[0]; r.Outcome != ForceRefused {
		t.Errorf("live: outcome = %q, want refused", r.Outcome)
	}

	f.run("worktree", "lock", f.wtPath)
	if r := f.service(rem, idleLive{}).ForceUnmerged(f.repo, []string{f.wtPath})[0]; r.Outcome != ForceRefused {
		t.Errorf("locked: outcome = %q, want refused", r.Outcome)
	}
	f.run("worktree", "unlock", f.wtPath)

	foreign := filepath.Join(t.TempDir(), "feature-checkout")
	f.run("worktree", "add", "-q", "-b", "feat/mine", foreign)
	if r := f.service(rem, idleLive{}).ForceUnmerged(f.repo, []string{foreign})[0]; r.Outcome != ForceRefused {
		t.Errorf("foreign: outcome = %q, want refused", r.Outcome)
	}
	if r := f.service(rem, idleLive{}).ForceUnmerged(f.repo, []string{f.repo})[0]; r.Outcome != ForceRefused {
		t.Errorf("main checkout: outcome = %q, want refused", r.Outcome)
	}

	if rem.calls() != 0 {
		t.Errorf("remover called %d times; every veto must stop before it", rem.calls())
	}
	if out := f.run("branch", "--list", "salvage/*"); out != "" {
		t.Errorf("salvage refs written under a veto: %q", out)
	}
}

// No salvage, no removal: when the rescue ref cannot be written the worktree
// stays, the remover is never asked, and the failure is journalled.
func TestForceUnmerged_FailedSalvageKeepsTheWorktree(t *testing.T) {
	f := newForceFixture(t)
	// A ref one level BELOW the salvage name makes update-ref fail (D/F conflict).
	blocker := ForceSalvageBranchName(f.wtPath, "swarm/phase-7", f.now) + "/x"
	f.run("branch", blocker)
	rem := &recordingRemover{}

	r := f.service(rem, idleLive{}).ForceUnmerged(f.repo, []string{f.wtPath})[0]
	if r.Outcome != ForceFailed {
		t.Fatalf("outcome = %q, want failed", r.Outcome)
	}
	if rem.calls() != 0 {
		t.Errorf("remover called %d times after a failed salvage", rem.calls())
	}
	if _, err := os.Stat(f.wtPath); err != nil {
		t.Errorf("worktree gone after a failed salvage: %v", err)
	}
	rows := journalRows(t, f.db)
	if len(rows) != 1 || rows[0]["verdict"] != string(VerdictForceUnmerged) || rows[0]["removed"] != 0 || rows[0]["error"] == "" {
		t.Errorf("journal = %v, want one force-unmerged row, removed=0, error set", rows)
	}
}

func TestForceSalvageBranchName(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 8, 7, 0, time.UTC)
	cases := map[[2]string]string{
		{"/x/phase-7", "swarm/phase-7"}:          "salvage/swarm-phase-7-20261022T090807Z",
		{"/x/agent-ab12", "worktree-agent-ab12"}: "salvage/worktree-agent-ab12-20261022T090807Z",
		{"/x/agent-cd34", ""}:                    "salvage/agent-cd34-20261022T090807Z",
	}
	for in, want := range cases {
		if got := ForceSalvageBranchName(in[0], in[1], now); got != want {
			t.Errorf("ForceSalvageBranchName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
