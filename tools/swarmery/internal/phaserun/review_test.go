package phaserun

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// fakeReviewer is a verify.Runner standing in for the reviewer session. It records
// every spec, answers outputs[i] on the i-th call (the last one repeats), and runs
// onRun first — which is how a test plays a reviewer that touches the worktree.
type fakeReviewer struct {
	mu      sync.Mutex
	specs   []verify.RunSpec
	outputs []string
	onRun   func(spec verify.RunSpec)
	// answer, when set, replaces outputs: the reply is chosen from the spec, which
	// is how a test tells a phase review from a plan review whatever their order.
	answer func(spec verify.RunSpec) string
}

func (f *fakeReviewer) Run(_ context.Context, spec verify.RunSpec) (*verify.Run, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	i := len(f.specs) - 1
	out := "VERDICT: PASS"
	if len(f.outputs) > 0 {
		if i >= len(f.outputs) {
			i = len(f.outputs) - 1
		}
		out = f.outputs[i]
	}
	hook, answer := f.onRun, f.answer
	f.mu.Unlock()
	if hook != nil {
		hook(spec)
	}
	if answer != nil {
		out = answer(spec)
	}
	return &verify.Run{Output: out}, nil
}

func (f *fakeReviewer) calls() []verify.RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]verify.RunSpec(nil), f.specs...)
}

func setReviewMode(t *testing.T, db *sql.DB, phaseID int64, mode string) {
	t.Helper()
	mustExec(t, db, `UPDATE epic_phases SET review_mode=? WHERE id=?`, mode, phaseID)
}

func reviewFixRound(t *testing.T, db *sql.DB, phaseID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT review_fix_round FROM epic_phases WHERE id=?`, phaseID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type reviewRow struct {
	verdict, detail, findings, treeBefore, treeAfter string
	fixRound                                         int
}

func reviewRows(t *testing.T, db *sql.DB, phaseID int64) []reviewRow {
	t.Helper()
	rows, err := db.Query(`SELECT verdict, detail, findings, tree_before, tree_after, fix_round
		FROM phase_reviews WHERE scope='phase' AND phase_id=? ORDER BY id`, phaseID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []reviewRow
	for rows.Next() {
		var r reviewRow
		if err := rows.Scan(&r.verdict, &r.detail, &r.findings, &r.treeBefore, &r.treeAfter, &r.fixRound); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// gitRepo makes a real git checkout with one commit and returns it with the
// commit's SHA — what the fingerprint and the restore act on.
func gitRepo(t *testing.T) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base = git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "the run's work")
	return dir, base
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// reviewFixture is a phase with **Review:** on and the loaded info reviewRun gets.
func reviewFixture(t *testing.T) (*Service, *sql.DB, int64, phaseInfo) {
	t.Helper()
	db, _, p1, _ := fixture(t)
	setReviewMode(t, db, p1, "on")
	s := newTestService(db, &stubRunner{}, &stubWt{})
	info, err := s.loadPhase(p1)
	if err != nil {
		t.Fatal(err)
	}
	if info.ReviewMode != "on" {
		t.Fatalf("info.ReviewMode = %q, want on (loadPhase must read review_mode)", info.ReviewMode)
	}
	return s, db, p1, info
}

// (a) The reviewer is spawned through verify.ClaudeRunner with ONE --disallowedTools
// flag denying the edit tools, Bash, and curl/wget/http to the daemon's API on the
// service's port. The real runner spawns a stand-in `claude` that records its argv
// outside the worktree (so recording it is not a mutation).
func TestReviewSpawnArgsDenyEditToolsAndBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude PATH shim is POSIX-only")
	}
	t.Setenv("SWARMERY_VERIFY_PERMISSION_MODE", "")
	t.Setenv("SWARMERY_PERMISSION_MODE", "")
	bin := t.TempDir()
	argsOut := filepath.Join(t.TempDir(), "args.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$REVIEW_ARGS_OUT\"\necho '- no blocking findings'\necho 'VERDICT: PASS'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REVIEW_ARGS_OUT", argsOut)

	s, db, p1, info := reviewFixture(t)
	s.Review = verify.ClaudeRunner{}
	s.DaemonPort = 8080
	dir, base := gitRepo(t)

	verdict, fix := s.reviewRun(p1, info, worktree.Acquired{Path: dir, Branch: "swarm/x", StartPoint: base}, "done")
	if verdict != "pass" || fix {
		t.Fatalf("reviewRun = (%q, %v), want (pass, false); rows %+v", verdict, fix, reviewRows(t, db, p1))
	}
	raw, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("the stand-in claude never ran: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	flags := 0
	list := ""
	for i, a := range argv {
		if a == "--disallowedTools" {
			flags++
			if i+1 < len(argv) {
				list = argv[i+1]
			}
		}
	}
	if flags != 1 {
		t.Fatalf("argv carries %d --disallowedTools flags, want exactly 1:\n%s", flags, raw)
	}
	const wantList = "Edit,Write,MultiEdit,NotebookEdit,Bash," +
		"Bash(curl *127.0.0.1:8080*),Bash(curl *localhost:8080*)," +
		"Bash(wget *127.0.0.1:8080*),Bash(wget *localhost:8080*)," +
		"Bash(http *127.0.0.1:8080*),Bash(http *localhost:8080*)"
	if list != wantList {
		t.Errorf("--disallowedTools %q, want %q", list, wantList)
	}
	if !strings.Contains(string(raw), verify.DaemonAPINotice(8080)) {
		t.Errorf("the review prompt lacks the daemon-API rule for :8080:\n%s", raw)
	}
	// The prompt carries the contract and the run's diff.
	if !strings.Contains(string(raw), "VERDICT: PASS | FAIL | INCONCLUSIVE") || !strings.Contains(string(raw), "func main() {}") {
		t.Errorf("the review prompt lacks the verdict contract or the run's diff:\n%s", raw)
	}
	if !strings.Contains(string(raw), "--model\n"+verify.DefaultModel) {
		t.Errorf("the reviewer is not pinned to %s:\n%s", verify.DefaultModel, raw)
	}
}

// (b) A reviewer that changes the worktree — an uncommitted edit, which HEAD's tree
// hash alone would not see, plus a new file — voids its own verdict: inconclusive,
// class reviewer-mutated-tree, and the tree is restored for the verifier.
func TestReviewMutatedTreeIsInconclusiveAndRestored(t *testing.T) {
	s, db, p1, info := reviewFixture(t)
	dir, base := gitRepo(t)
	s.Review = &fakeReviewer{
		outputs: []string{"VERDICT: PASS"},
		onRun: func(verify.RunSpec) {
			_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // edited by the reviewer\n"), 0o644)
			_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("scratch\n"), 0o644)
		},
	}

	verdict, fix := s.reviewRun(p1, info, worktree.Acquired{Path: dir, StartPoint: base}, "done")
	if verdict != "inconclusive" || fix {
		t.Fatalf("reviewRun = (%q, %v), want (inconclusive, false)", verdict, fix)
	}
	rows := reviewRows(t, db, p1)
	if len(rows) != 1 {
		t.Fatalf("phase_reviews rows = %d, want 1", len(rows))
	}
	if !strings.HasPrefix(rows[0].detail, ClassReviewerMutatedTree) {
		t.Errorf("detail = %q, want the %s class", rows[0].detail, ClassReviewerMutatedTree)
	}
	if rows[0].treeBefore == "" || rows[0].treeBefore == rows[0].treeAfter {
		t.Errorf("tree_before=%q tree_after=%q, want two different fingerprints", rows[0].treeBefore, rows[0].treeAfter)
	}
	if st := gitStatus(t, dir); st != "" {
		t.Errorf("worktree not restored after the reviewer mutated it:\n%s", st)
	}
}

// A worktree that was ALREADY dirty and that the reviewer leaves alone is not a
// mutation: the fingerprint is stable across the review.
func TestReviewDirtyButUntouchedTreeIsNotAMutation(t *testing.T) {
	s, _, p1, info := reviewFixture(t)
	dir, base := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "left-by-executor.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Review = &fakeReviewer{outputs: []string{"- fine\nVERDICT: PASS"}}

	if verdict, _ := s.reviewRun(p1, info, worktree.Acquired{Path: dir, StartPoint: base}, "partial"); verdict != "pass" {
		t.Fatalf("verdict = %q, want pass", verdict)
	}
}

// startReviewed runs phase p1 through Start with the review stage wired to a fake
// reviewer, a constant fingerprint (the harness worktree is not a checkout) and a
// stub verifier.
func startReviewed(t *testing.T, outputs ...string) (*Service, *sql.DB, int64, *stubRunner, *fakeReviewer, *stubVerifier) {
	t.Helper()
	db, _, p1, _ := fixture(t)
	setReviewMode(t, db, p1, "on")
	setVerifyMode(t, db, p1, "normal")
	runner := &stubRunner{}
	s := newTestService(db, runner, &stubWt{})
	rv := &fakeReviewer{outputs: outputs}
	s.Review = rv
	s.treeFingerprint = func(string) (string, error) { return "tree-1", nil }
	v := &stubVerifier{}
	s.Verify = v
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s, db, p1, runner, rv, v
}

// (c) FAIL ⇒ `## Review findings` in the doc and exactly ONE Returned re-run, whose
// prompt points at the findings; the re-run's FAIL is recorded and starts nothing.
// The verifier grades both runs.
func TestReviewFailStartsExactlyOneFixRerun(t *testing.T) {
	const fail = "- P0 internal/x.go:12 — the error is swallowed.\nVERDICT: FAIL"
	_, db, p1, runner, rv, v := startReviewed(t, fail)

	if n := runner.specCount(); n != 2 {
		t.Fatalf("executor runs = %d, want 2 (the run + one fix re-run)", n)
	}
	runner.mu.Lock()
	first, second := runner.specs[0], runner.specs[1]
	runner.mu.Unlock()
	if first.Returned {
		t.Error("the first run is marked Returned")
	}
	if !second.Returned || !strings.Contains(second.Prompt, ReviewFixNote) {
		t.Errorf("the re-run is not a Returned run pointed at the review findings (Returned=%v)", second.Returned)
	}
	if strings.Contains(second.Prompt, ReturnedNote) {
		t.Error("the review fix re-run carries the OPERATOR feedback note")
	}
	if n := len(rv.calls()); n != 2 {
		t.Errorf("reviews = %d, want 2", n)
	}
	if n := len(v.calls()); n != 2 {
		t.Errorf("verifier calls = %d, want 2 — the verifier runs after the review either way", n)
	}

	doc, _, _, _ := phaseDocAndState(t, db, p1)
	if c := strings.Count(doc, "## Review findings ("); c != 1 {
		t.Errorf("doc carries %d `## Review findings` sections, want 1:\n%s", c, doc)
	}
	if !strings.Contains(doc, "> - P0 internal/x.go:12 — the error is swallowed.") {
		t.Errorf("the findings are not quoted into the doc:\n%s", doc)
	}
	rows := reviewRows(t, db, p1)
	if len(rows) != 2 || rows[0].fixRound != 0 || rows[1].fixRound != 1 || rows[0].verdict != "fail" || rows[1].verdict != "fail" {
		t.Errorf("phase_reviews = %+v, want two fail rows at fix rounds 0 and 1", rows)
	}
	if got := reviewFixRound(t, db, p1); got != 1 {
		t.Errorf("review_fix_round = %d, want 1 after the fix re-run", got)
	}
	if rows[0].findings == "" {
		t.Error("the review row lost the findings")
	}
	// (a) at the service seam: the spec carries Bash and the daemon-API rules (no
	// port set ⇒ the default) on top of the read-only set, merged into one flag by
	// the runner.
	spec := rv.calls()[0]
	if got, want := strings.Join(verify.ToolDenyArgs(spec.DisallowedTools), " "),
		"--disallowedTools Edit,Write,MultiEdit,NotebookEdit,Bash,"+strings.Join(verify.DaemonDenyPatterns(verify.DefaultDaemonPort), ","); got != want {
		t.Errorf("reviewer denial = %q, want %q", got, want)
	}
	if !strings.Contains(spec.Prompt, verify.DaemonAPINotice(verify.DefaultDaemonPort)) {
		t.Errorf("the review prompt lacks the daemon-API rule:\n%s", spec.Prompt)
	}
	if want := "/wt/p/" + runcore.PhaseTaskName(p1); spec.Cwd != want {
		t.Errorf("reviewer cwd = %q, want the run's worktree %q", spec.Cwd, want)
	}
	if spec.Model != verify.DefaultModel {
		t.Errorf("reviewer model = %q, want %q (the agent's opus, not the account default)", spec.Model, verify.DefaultModel)
	}
}

// A PASS on the fix re-run closes the cycle: review_fix_round back to 0, still one
// re-run in total.
func TestReviewPassAfterFixResetsRound(t *testing.T) {
	_, db, p1, runner, _, _ := startReviewed(t, "- P1 a.go:1 — wrong.\nVERDICT: FAIL", "- none\nVERDICT: PASS")
	if n := runner.specCount(); n != 2 {
		t.Fatalf("executor runs = %d, want 2", n)
	}
	if got := reviewFixRound(t, db, p1); got != 0 {
		t.Errorf("review_fix_round = %d after a pass, want 0", got)
	}
}

// A new run that is not the review's fix re-run opens with review_fix_round = 0.
func TestReviewFixRoundResetOnPlainRun(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET review_fix_round=1 WHERE id=?`, p1)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := reviewFixRound(t, db, p1); got != 0 {
		t.Errorf("review_fix_round = %d after a plain run, want 0", got)
	}
}

// (d) `**Review:** off` (the default) ⇒ the reviewer is never spawned; neither is it
// for a run that did not end cleanly.
func TestReviewSkippedWhenOffOrRunFailed(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	rv := &fakeReviewer{}
	s.Review = rv
	s.treeFingerprint = func(string) (string, error) { return "tree-1", nil }
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := len(rv.calls()); n != 0 {
		t.Errorf("reviewer called %d time(s) for a doc that never asked", n)
	}

	db2, _, q1, _ := fixture(t)
	setReviewMode(t, db2, q1, "on")
	s2 := newTestService(db2, &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 2, Stderr: "boom"}, nil
	}}, &stubWt{})
	rv2 := &fakeReviewer{}
	s2.Review = rv2
	if _, err := s2.Start(q1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := len(rv2.calls()); n != 0 {
		t.Errorf("reviewer called %d time(s) on a failed run", n)
	}
}

// (e) review → verify → worktree removal, mirroring TestVerifyRunsBeforeWorktreeRemoval.
func TestReviewRunsBeforeVerify(t *testing.T) {
	db, _, p1, _ := fixture(t)
	setReviewMode(t, db, p1, "on")
	setVerifyMode(t, db, p1, "strict")
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	s.treeFingerprint = func(string) (string, error) { return "tree-1", nil }

	var order []string
	s.Review = &fakeReviewer{onRun: func(verify.RunSpec) { order = append(order, "review") }}
	s.Verify = &stubVerifier{onVerify: func() { order = append(order, "verify") }}
	wt.onRemove = func() { order = append(order, "remove") }

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if strings.Join(order, ",") != "review,verify,remove" {
		t.Fatalf("call order = %v, want [review verify remove]", order)
	}
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("slot still held after the run goroutine returned")
	}
}

// A reviewer that panics is an inconclusive `reviewer-did-not-start` review, and
// the rest of the run's exit path — verifier, worktree removal, slot release —
// still runs.
func TestReviewPanicIsInconclusiveAndTheExitPathContinues(t *testing.T) {
	db, _, p1, _ := fixture(t)
	setReviewMode(t, db, p1, "on")
	setVerifyMode(t, db, p1, "strict")
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	s.treeFingerprint = func(string) (string, error) { return "tree-1", nil }

	var order []string
	s.Review = &fakeReviewer{onRun: func(verify.RunSpec) { panic("reviewer exploded") }}
	s.Verify = &stubVerifier{onVerify: func() { order = append(order, "verify") }}
	wt.onRemove = func() { order = append(order, "remove") }

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if strings.Join(order, ",") != "verify,remove" {
		t.Fatalf("call order after a reviewer panic = %v, want [verify remove]", order)
	}
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("slot still held after a reviewer panic")
	}
	rows := reviewRows(t, db, p1)
	if len(rows) != 1 || rows[0].verdict != "inconclusive" ||
		!strings.HasPrefix(rows[0].detail, "reviewer-did-not-start: ") {
		t.Fatalf("phase_reviews = %+v, want one inconclusive reviewer-did-not-start row", rows)
	}
	if got := reviewFixRound(t, db, p1); got != 0 {
		t.Errorf("review_fix_round = %d after a panicked review, want 0 (no fix re-run)", got)
	}
}

func phaseDocAndState(t *testing.T, db *sql.DB, id int64) (doc, state string, uuid, runErr sql.NullString) {
	t.Helper()
	var path string
	if err := db.QueryRow(`SELECT doc_path, run_state, run_session_uuid, run_error FROM epic_phases WHERE id=?`, id).
		Scan(&path, &state, &uuid, &runErr); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), state, uuid, runErr
}
