package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// The two refusals base resolution and the blocked re-run guard add to
// POST …/run, asserted on the WIRE: status, discriminator, and the structured
// fields a client acts on. The decisions themselves are tested in
// internal/phaserun; what is pinned here is that they reach the client as
// themselves instead of falling through to the generic 500 arm.

// stackGitRepo is a temp repository on `main` with one commit. Real git, temp
// directory only.
func stackGitRepo(t *testing.T) (dir string, run func(args ...string) string) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()
	git := worktree.ExecGit{}
	run = func(args ...string) string {
		t.Helper()
		out, err := git.Run(dir, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	stackCommit(t, dir, run, "init")
	return dir, run
}

var stackCommitSeq int

// stackCommit adds one new file on the current branch.
func stackCommit(t *testing.T, dir string, run func(args ...string) string, msg string) {
	t.Helper()
	stackCommitSeq++
	name := "f" + strconv.Itoa(stackCommitSeq) + ".txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", name)
	run("commit", "-q", "-m", msg)
}

// TestRunPhaseDepsUnmerged: phase 2 depends on two COMPLETE phases whose run
// branches diverged and were never merged. 409, discriminated as deps-unmerged,
// naming both branches and the base they were measured against — and nothing
// spawned.
func TestRunPhaseDepsUnmerged(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, p2 := fixturePhaseIDs(t, db, taskID)
	repo, git := stackGitRepo(t)

	const branchA, branchB = "swarm/phase-9001", "swarm/phase-9002"
	for _, b := range []string{branchA, branchB} {
		git("checkout", "-q", "-b", b, "main")
		stackCommit(t, repo, git, b+" work")
		git("checkout", "-q", "main")
	}

	mustExecStack(t, db, `UPDATE projects SET path=? WHERE id=1`, repo)
	// Both dependencies are complete by the dependency gate's own measure.
	mustExecStack(t, db, `UPDATE epic_phases SET checkboxes_done=2, run_state='done', run_branch=? WHERE id=?`, branchA, p1)
	mustExecStack(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done, run_state, run_branch)
		VALUES (?, 3, 'Phase 3 — API', '/plan/phase-3-api.md', '[]', 1, 1, 'done', ?)`, taskID, branchB)
	mustExecStack(t, db, `UPDATE epic_phases SET depends_on='[1,3]' WHERE id=?`, p2)

	runner := &phaseStubRunner{}
	svc := attachPhaseRun(t, db, runner, true)
	svc.Git = worktree.ExecGit{}

	resp := postPhase(t, phaseRunURL(srv, taskID, p2))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error    string   `json:"error"`
		Code     string   `json:"code"`
		Message  string   `json:"message"`
		Branches []string `json:"branches"`
		Base     string   `json:"base"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "deps-unmerged" || body.Code != "deps-unmerged" {
		t.Errorf("error=%q code=%q, want deps-unmerged in both", body.Error, body.Code)
	}
	if !reflect.DeepEqual(body.Branches, []string{branchA, branchB}) {
		t.Errorf("branches = %v, want both branch names [%s %s]", body.Branches, branchA, branchB)
	}
	if body.Base != "main" {
		t.Errorf("base = %q, want main", body.Base)
	}
	// The sentence a toast shows names what to merge, where.
	for _, want := range []string{branchA, branchB, "main"} {
		if !strings.Contains(body.Message, want) {
			t.Errorf("message %q does not name %s", body.Message, want)
		}
	}
	if n := len(runner.dispatchedSpecs()); n != 0 {
		t.Errorf("dispatched %d run(s), want 0 — a refused run must not spawn", n)
	}
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p2).Scan(&state); err != nil || state != "idle" {
		t.Errorf("run_state = %q (%v), want idle — nothing may be stamped", state, err)
	}
}

// blockingPhaseRunner is an executor that ends its turn with the blocked sentinel,
// written where the completion loop reads it: the session's last assistant turn.
type blockingPhaseRunner struct {
	db     *sql.DB
	reason string
	runs   int
}

func (r *blockingPhaseRunner) Start(_ context.Context, spec phaserun.RunSpec) (*phaserun.Run, error) {
	r.runs++
	var sid int64
	err := r.db.QueryRow(`SELECT id FROM sessions WHERE session_uuid=?`, spec.SessionUUID).Scan(&sid)
	if err == sql.ErrNoRows {
		res, ierr := r.db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at)
			VALUES (1, ?, '2026-09-30T09:00:00Z')`, spec.SessionUUID)
		if ierr != nil {
			return nil, ierr
		}
		sid, _ = res.LastInsertId()
	} else if err != nil {
		return nil, err
	}
	if _, err := r.db.Exec(`INSERT INTO turns (session_id, seq, role, text, started_at)
		VALUES (?, (SELECT COALESCE(MAX(seq),0)+1 FROM turns WHERE session_id=?), 'assistant', ?, '2026-09-30T09:00:00Z')`,
		sid, sid, "I read the code.\n\nPHASE BLOCKED: "+r.reason); err != nil {
		return nil, err
	}
	return &phaserun.Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

// TestRunPhaseBlockedUnchanged: a run blocks; the same POST again is refused as
// blocked-unchanged with the reason and when it lapses; the same POST with
// {"force": true} runs.
func TestRunPhaseBlockedUnchanged(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	const reason = "this worktree is based on a commit from before PR #108"
	runner := &blockingPhaseRunner{db: db, reason: reason}
	attachPhaseRun(t, db, runner, true)
	url := phaseRunURL(srv, taskID, p1)

	if resp := postPhase(t, url); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first run status = %d, want 202", resp.StatusCode)
	}
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state); err != nil || state != "blocked" {
		t.Fatalf("run_state = %q (%v), want blocked (test premise)", state, err)
	}

	resp := postPhase(t, url)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-run status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error      string `json:"error"`
		Code       string `json:"code"`
		Message    string `json:"message"`
		Reason     string `json:"reason"`
		Since      string `json:"since"`
		RetryAfter string `json:"retryAfter"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "blocked-unchanged" || body.Code != "blocked-unchanged" {
		t.Errorf("error=%q code=%q, want blocked-unchanged in both", body.Error, body.Code)
	}
	if body.Reason != reason {
		t.Errorf("reason = %q, want the blocked run's reason", body.Reason)
	}
	since, err := time.Parse(time.RFC3339, body.Since)
	if err != nil {
		t.Fatalf("since = %q: %v", body.Since, err)
	}
	retry, err := time.Parse(time.RFC3339, body.RetryAfter)
	if err != nil {
		t.Fatalf("retryAfter = %q: %v", body.RetryAfter, err)
	}
	if got := retry.Sub(since); got != 24*time.Hour {
		t.Errorf("retryAfter - since = %s, want the default 24h cooldown", got)
	}
	if !strings.Contains(body.Message, reason) {
		t.Errorf("message %q does not carry the reason", body.Message)
	}
	if runner.runs != 1 {
		t.Errorf("runs = %d, want 1 — the refused re-run must not spawn", runner.runs)
	}

	// `force: false` is the same as no force at all.
	if resp := postPhaseBody(t, url, `{"force": false}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("force=false status = %d, want 409", resp.StatusCode)
	}
	// A force that is not a boolean is a malformed request, not a forced run.
	if resp := postPhaseBody(t, url, `{"force": "yes"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("force=\"yes\" status = %d, want 400", resp.StatusCode)
	}
	if runner.runs != 1 {
		t.Errorf("runs = %d, want still 1", runner.runs)
	}

	// The operator says run it anyway.
	if resp := postPhaseBody(t, url, `{"force": true}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forced re-run status = %d, want 202", resp.StatusCode)
	}
	if runner.runs != 2 {
		t.Errorf("runs = %d, want the forced run spawned (2)", runner.runs)
	}
}

func mustExecStack(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}
