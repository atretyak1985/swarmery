package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// permissionWaitClaude stands in for `claude` and behaves the way the real CLI
// did on 2026-10-02, in verification runs 6 and 8 (tasks 572 and 580). Both
// times the verifier's first Bash check outside the project allowlist raised a
// permission prompt. With no --permission-mode on the argv, nobody in a headless
// run could answer it. The approvals long-poll held it for 600s, the next prompt
// held another 600s, and the 15m hard timeout killed the run before it wrote a
// verdict. Run 5, the same afternoon, reached FAIL only because the operator
// happened to approve its four prompts from the dashboard within seconds.
//
// With a mode that never asks, the same check runs at once and the verdict is
// written. The sleep stands in for the 600s hold, and the runner's timeout is
// shrunk so the test keeps the same order (hold longer than timeout) in seconds.
//
// holdOutlivesTimeout is generous on purpose. On macOS the first exec of a
// freshly written script can take 5-15s (the exec policy check runs once per new
// file), so a tighter bound would time out before the stand-in even started, and
// the test would pass or fail for a reason unrelated to the prompt.
const permissionWaitClaude = `
mode=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--permission-mode" ]; then mode="$a"; fi
  prev="$a"
done
echo "$@" > "$PWD/args.txt"
if [ "$mode" = "bypassPermissions" ]; then
  echo "- ran the declared check: ok"
  echo "VERDICT: PASS"
  exit 0
fi
exec sleep 600
`

const holdOutlivesTimeout = 30 * time.Second

// verifyPermKnob is spelled out rather than taken from runner.go, so this file
// pins the knob's public name: renaming it would silently break operators'
// launchd plists.
const verifyPermKnob = "SWARMERY_VERIFY_PERMISSION_MODE"

// clearPermissionKnobs pins the resolution to the code default, so an
// operator's own SWARMERY_*_PERMISSION_MODE cannot change what these tests see.
func clearPermissionKnobs(t *testing.T) {
	t.Helper()
	t.Setenv(verifyPermKnob, "")
	t.Setenv("SWARMERY_PERMISSION_MODE", "")
}

// The regression: a verifier spawned with no permission decision waits on a
// prompt nobody answers and is stamped verifier-timed-out. It has to reach its
// verdict instead.
func TestInconclusiveTimedOutOnPermissionWait(t *testing.T) {
	clearPermissionKnobs(t)
	fakeClaudeRunner(t, permissionWaitClaude)

	db := testDB(t)
	s := newTestService(t, db, ClaudeRunner{Timeout: holdOutlivesTimeout}, stubTrees{hash: "tree-perm"})
	id := insertTask(t, db, taskOpts{worktree: t.TempDir()})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Fatalf("verdict = %q, detail %q; want pass: the verifier spawn must not wait on a permission prompt nobody can answer",
			got, detailOf(t, db, id))
	}
}

// Not asking must not mean "may edit": the verifier grades the worktree it runs
// in, so the edit tools stay denied on the argv itself, not only in the prompt.
func TestInconclusivePermissionWait_EditToolsStayDenied(t *testing.T) {
	clearPermissionKnobs(t)
	fakeClaudeRunner(t, permissionWaitClaude)
	cwd := t.TempDir()

	if _, err := (ClaudeRunner{Timeout: holdOutlivesTimeout}).Run(context.Background(),
		RunSpec{Prompt: "grade", SessionUUID: "u-perm", Cwd: cwd}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := readArgs(t, cwd)
	for _, want := range []string{
		"--permission-mode bypassPermissions",
		"--disallowedTools Edit,Write,MultiEdit,NotebookEdit",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}
}

// The escape hatch every other spawn site has: SWARMERY_VERIFY_PERMISSION_MODE=off
// omits the flag (the pre-fix argv), and the edit tools stay denied regardless.
func TestInconclusivePermissionWait_OffOmitsTheFlag(t *testing.T) {
	clearPermissionKnobs(t)
	t.Setenv(verifyPermKnob, "off")
	fakeClaudeRunner(t, `echo "$@" > "$PWD/args.txt"; exit 0`)
	cwd := t.TempDir()

	if _, err := (ClaudeRunner{}).Run(context.Background(),
		RunSpec{Prompt: "grade", SessionUUID: "u-off", Cwd: cwd}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := readArgs(t, cwd)
	if strings.Contains(argv, "--permission-mode") {
		t.Errorf("argv %q carries --permission-mode with %s=off", argv, verifyPermKnob)
	}
	if !strings.Contains(argv, "--disallowedTools Edit,Write,MultiEdit,NotebookEdit") {
		t.Errorf("argv %q lost the edit-tool denial with %s=off", argv, verifyPermKnob)
	}
}

func readArgs(t *testing.T, cwd string) string {
	t.Helper()
	out, err := os.ReadFile(filepath.Join(cwd, "args.txt"))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	return strings.TrimSpace(string(out))
}
