package verify

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// phaseDocWithVerify is the shape the planner writes: a Verify block inside the
// copy-paste agent prompt, one command per bullet.
const phaseDocWithVerify = "# Phase 1 — x\n**Verify:** normal\n\n## Copy-paste Agent Prompt\n\n```\nDo the thing.\n\n" +
	"Verify (each is one command):\n" +
	"- ./run-checks.sh\n" +
	"- go -C /repo/p/tools/x test ./internal/... -timeout 30m\n" +
	"- `make -C /repo/p/tools/x test`\n" +
	"\nTICK CONTRACT: ...\n```\n\n## Acceptance Criteria\n\n- [x] checks pass\n"

func TestParseVerifyCommands(t *testing.T) {
	got, ok := ParseVerifyCommands(phaseDocWithVerify)
	want := []string{"./run-checks.sh", "go -C /repo/p/tools/x test ./internal/... -timeout 30m", "make -C /repo/p/tools/x test"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseVerifyCommands = (%q, %v), want (%q, true)", got, ok, want)
	}
	// The header row `**Verify:** normal` is a mode, not a block.
	if got, ok := ParseVerifyCommands("# P\n**Verify:** normal\n\nno block here\n"); ok || len(got) != 0 {
		t.Errorf("header row parsed as a block: (%q, %v)", got, ok)
	}
	// A command a prefix rule cannot carry makes the whole block unusable: a list
	// that silently misses one check turns that check into a denial.
	// Compound and prefixed shapes too: the CLI matches each subcommand of a
	// compound command against the allow rules separately, so a rule spelling
	// the whole line does not admit it (review I1).
	for _, bad := range []string{"- run (x)", "- a, b", "- cat <<EOF",
		"- cd tools/swarmery && go test ./...", "- make a || make b", "- go vet ./...; go test ./...",
		"- go test ./... | tee out", "- go test ./... &", "- echo $(pwd)", "- go test ./... > out.txt",
		"- go test < in", "- cd tools/swarmery", "- FOO=1 make test", "- GOFLAGS=-count=1 go test ./...",
		// Review round 2 (M2): continuations and wrapper prefixes.
		`- go test ./... \`, "- env FOO=1 go test ./...", "- time make test", "- sudo make install",
		"- nohup ./run-checks.sh", "- xargs go test", "- exec ./run-checks.sh", "- nice make test", "- command make test"} {
		if _, ok := ParseVerifyCommands("Verify:\n- ./ok.sh\n" + bad + "\n"); ok {
			t.Errorf("block with %q accepted", bad)
		}
	}
	if _, ok := ParseVerifyCommands("Verify:\n\nnothing listed\n"); ok {
		t.Error("an empty Verify block counted as declared commands")
	}
}

// A phase target that declares its Verify commands runs least-privilege: the
// runner gets an allow-list (read tools, read-only git, exactly those commands —
// also spelled against the worktree for a primary-checkout path), and the prompt
// says so, including that a denied check is INCONCLUSIVE, never FAIL.
func TestVerifyTarget_DeclaredCommandsBecomeTheAllowList(t *testing.T) {
	db := testDB(t)
	var spec RunSpec
	r := &stubRunner{outFn: func(s RunSpec) *Run { spec = s; return &Run{Output: "VERDICT: PASS"} }}
	s := newTestService(t, db, r, stubTrees{hash: "tree-lp"})

	if err := s.VerifyTarget(context.Background(), Target{
		Key: PhaseKey(41), WorktreePath: "/wt/p/phase-41", ProjectPath: "/repo/p",
		Title: "phase", Prompt: phaseDocWithVerify, Strictness: StrictnessNormal,
		Stamp: func(Verdict, string) error { return nil },
	}); err != nil {
		t.Fatalf("VerifyTarget: %v", err)
	}
	for _, want := range []string{
		"Read", "Grep", "Glob", "Bash(git diff*)", "Bash(git log*)", "Bash(git show*)", "Bash(git status*)",
		"Bash(./run-checks.sh*)",
		"Bash(go -C /repo/p/tools/x test ./internal/... -timeout 30m*)",
		"Bash(go -C /wt/p/phase-41/tools/x test ./internal/... -timeout 30m*)",
		"Bash(make -C /wt/p/phase-41/tools/x test*)",
	} {
		if !inList(spec.AllowedTools, want) {
			t.Errorf("allow-list %q lacks %q", spec.AllowedTools, want)
		}
	}
	if inList(spec.AllowedTools, "Bash") {
		t.Errorf("allow-list %q carries unrestricted Bash", spec.AllowedTools)
	}
	if !strings.Contains(spec.Prompt, "INCONCLUSIVE") || !strings.Contains(spec.Prompt, "./run-checks.sh") ||
		!strings.Contains(spec.Prompt, "RUN SCOPE") {
		t.Errorf("prompt does not carry the run-scope notice:\n%s", spec.Prompt)
	}
	// The daemon-API rules still ride along.
	if !inList(spec.DisallowedTools, DaemonDenyPatterns(0)[0]) {
		t.Errorf("deny list %q lost the daemon-API rules", spec.DisallowedTools)
	}
}

// No machine-readable Verify block (every board card today): the measured
// fallback — bypassPermissions plus the deny list — because an allow-list that
// misses a check the model picked would turn it into a denial.
func TestVerifyTarget_NoDeclaredCommandsKeepsTheFallback(t *testing.T) {
	db := testDB(t)
	var spec RunSpec
	r := &stubRunner{outFn: func(s RunSpec) *Run { spec = s; return &Run{Output: "VERDICT: PASS"} }}
	s := newTestService(t, db, r, stubTrees{hash: "tree-fb"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatalf("VerifyTask: %v", err)
	}
	if len(spec.AllowedTools) != 0 {
		t.Errorf("a task with no Verify block got an allow-list %q", spec.AllowedTools)
	}
	if strings.Contains(spec.Prompt, "RUN SCOPE") {
		t.Errorf("fallback prompt carries the run-scope notice:\n%s", spec.Prompt)
	}
}

// A Verify block written the way real phase docs often are — `cd <dir> && go
// test …` — cannot be carried by prefix rules, so the whole run keeps the
// fallback rather than running least-privilege and denying its own check.
func TestVerifyTarget_CompoundCommandKeepsTheFallback(t *testing.T) {
	db := testDB(t)
	var spec RunSpec
	r := &stubRunner{outFn: func(s RunSpec) *Run { spec = s; return &Run{Output: "VERDICT: PASS"} }}
	s := newTestService(t, db, r, stubTrees{hash: "tree-cmp"})
	doc := "Verify (each is one command):\n- ./run-checks.sh\n- cd tools/swarmery && go test ./internal/...\n"

	if err := s.VerifyTarget(context.Background(), Target{
		Key: PhaseKey(42), WorktreePath: "/wt/p/phase-42", ProjectPath: "/repo/p",
		Title: "phase", Prompt: doc, Strictness: StrictnessNormal,
		Stamp: func(Verdict, string) error { return nil },
	}); err != nil {
		t.Fatalf("VerifyTarget: %v", err)
	}
	if len(spec.AllowedTools) != 0 || strings.Contains(spec.Prompt, "RUN SCOPE") {
		t.Errorf("a compound Verify command still produced an allow-list %q / run-scope prompt", spec.AllowedTools)
	}
}

// At the argv: an allow-list means dontAsk (the read-only site default) and ONE
// --allowedTools flag; none means the bypass fallback, exactly as before.
func TestClaudeRunner_AllowListSelectsTheReadOnlyMode(t *testing.T) {
	clearPermissionKnobs(t)
	fakeClaudeRunner(t, `echo "$@" > "$PWD/args.txt"; exit 0`)

	cwd := t.TempDir()
	if _, err := (ClaudeRunner{}).Run(context.Background(), RunSpec{
		Prompt: "grade", SessionUUID: "u-lp", Cwd: cwd, AllowedTools: []string{"Read", "Grep", "Glob"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := readArgs(t, cwd)
	for _, want := range []string{"--permission-mode dontAsk", "--allowedTools Read,Grep,Glob", "--disallowedTools Edit,Write,MultiEdit,NotebookEdit"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}
	if strings.Contains(argv, "bypassPermissions") {
		t.Errorf("argv %q carries bypassPermissions with an allow-list", argv)
	}

	// A reviewer names its own knob: it wins over the verifier's.
	t.Setenv("SWARMERY_REVIEW_PERMISSION_MODE", "plan")
	t.Setenv(verifyPermKnob, "bypassPermissions")
	if _, err := (ClaudeRunner{}).Run(context.Background(), RunSpec{
		Prompt: "review", SessionUUID: "u-rv", Cwd: cwd, AllowedTools: []string{"Read"},
		PermissionEnv: "SWARMERY_REVIEW_PERMISSION_MODE",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if argv := readArgs(t, cwd); !strings.Contains(argv, "--permission-mode plan") {
		t.Errorf("reviewer argv %q ignores its own knob", argv)
	}
}

func inList(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
