package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// TestE2EAdmissionAgainstRealRepo exercises the FULL admission path against a
// real git repo with the real worktree.Manager (real git, no stub) but a stub
// Runner (no real claude spawn — the sentinel is injected as an ingested turn).
// Opt-in: set SWARMERY_E2E=1. It proves the daemon-side contract Phase 4/6 rely
// on: real swarm/<id> branch + worktree on disk, explicit session link, prompt
// carrying the Swarm-Task-Id trailer, sentinel routing, and real worktree
// teardown on done — without burning API tokens on a real headless session.
func TestE2EAdmissionAgainstRealRepo(t *testing.T) {
	if os.Getenv("SWARMERY_E2E") == "" {
		t.Skip("set SWARMERY_E2E=1 to run the real-repo integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// A scratch repo with one commit.
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@t.co")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "commit", "--allow-empty", "-qm", "root")

	db, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,?, 'p','2026-01-01T00:00:00Z')`, repo); err != nil {
		t.Fatal(err)
	}

	wtRoot := t.TempDir()
	mgr := &worktree.Manager{Git: worktree.ExecGit{}, Root: wtRoot}

	var capturedPrompt string
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		capturedPrompt = spec.Prompt
		// The worktree must exist on disk at spawn time.
		if _, err := os.Stat(spec.Cwd); err != nil {
			t.Errorf("worktree cwd %s does not exist at spawn: %v", spec.Cwd, err)
		}
		// Inject the transcript the ingest pipeline would land, with a done sentinel.
		ingestSession(t, db, spec.SessionUUID, "NO-OP: nothing to change on HEAD")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}

	s := NewService(db, Config{MaxConcurrent: 2, MaxWorktrees: 4, RunTimeout: time.Minute, Enabled: true}, r, mgr)
	s.Go = func(fn func()) { fn() } // inline for a synchronous assertion

	id := insertTask(t, db, "T-e2e01", taskOpts{fileScope: `["src/x"]`})

	// Before admission, capture branch existence.
	s.Schedule()

	// 1) The prompt carried the exact trailer + branch.
	if !strings.Contains(capturedPrompt, worktree.Trailer("T-e2e01")) {
		t.Errorf("prompt missing trailer %q", worktree.Trailer("T-e2e01"))
	}
	if !strings.Contains(capturedPrompt, "swarm/T-e2e01") {
		t.Error("prompt missing branch name")
	}

	// 2) The branch was really created in the repo.
	out := runGit(t, repo, "branch", "--list", "swarm/T-e2e01")
	if !strings.Contains(out, "swarm/T-e2e01") {
		t.Errorf("swarm/T-e2e01 branch not created; git branch = %q", out)
	}

	// 3) Sentinel routed to done + result_note, worktree torn down, branch kept.
	if col := column(t, db, id); col != "done" {
		t.Errorf("column = %q, want done", col)
	}
	if note := taskField(t, db, id, "result_note"); !strings.HasPrefix(note.String, "NO-OP:") {
		t.Errorf("result_note = %q", note.String)
	}
	// Worktree directory removed…
	if _, err := os.Stat(filepath.Join(wtRoot, "p", "T-e2e01")); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be removed on done; stat err = %v", err)
	}
	// …but the branch (with its commits) kept.
	out = runGit(t, repo, "branch", "--list", "swarm/T-e2e01")
	if !strings.Contains(out, "swarm/T-e2e01") {
		t.Error("branch should be KEPT after worktree removal (verification needs it)")
	}

	// 4) Explicit link recorded.
	var linkSrc string
	if err := db.QueryRow(`SELECT link_source FROM task_sessions WHERE task_id=?`, id).Scan(&linkSrc); err != nil || linkSrc != "explicit" {
		t.Errorf("explicit link missing: src=%q err=%v", linkSrc, err)
	}
	t.Logf("E2E OK: real branch swarm/T-e2e01 created+kept, worktree torn down, explicit link, trailer in prompt")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
