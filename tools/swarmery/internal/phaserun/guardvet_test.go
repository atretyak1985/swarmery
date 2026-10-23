package phaserun

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// The executor is untrusted and controls every file name in its worktree and
// the contents of guard.log. These tests hold the rule that no answer releases
// more than the exact literal files the operator was shown.

// An allow entry without wildcards releases exactly one path in the post-run
// check, as it does in the hook — never the directory it might also name (git
// reads a literal exclude pathspec as a directory prefix).
func TestProtectedChangesLiteralAllowIsOneFile(t *testing.T) {
	g := newGuardTrap(t)
	g.write("scripts/tests/lib/x.sh", "echo base\n")
	g.run("add", "-A")
	g.run("commit", "-qm", "lib")
	base := g.tip("HEAD")
	g.write("scripts/tests/lib/x.sh", "echo changed\n")
	globs := []string{"scripts/tests/**/*"}
	for _, c := range []struct {
		allow []string
		want  []string
	}{
		{[]string{"scripts/tests/lib"}, []string{"scripts/tests/lib/x.sh"}},
		{[]string{"scripts/tests"}, []string{"scripts/tests/lib/x.sh"}},
		{[]string{"scripts/tests/lib/x.sh"}, []string{}},
		{[]string{"SCRIPTS/Tests/lib/X.sh"}, []string{}}, // case ignored, as the hook does
		{[]string{"scripts/tests/lib/*.sh"}, []string{}}, // a hand-written glob keeps glob reading
		{[]string{"scripts/tests/lib/"}, []string{}},     // a hand-written directory glob too
		{[]string{"scripts/tests/lib/x.sh.bak"}, []string{"scripts/tests/lib/x.sh"}},
	} {
		got, derr := protectedChanges(worktree.ExecGit{}, g.dir, base, globs, c.allow)
		if derr != "" || !reflect.DeepEqual(got, c.want) {
			t.Errorf("allow %q: protectedChanges = %q (%s), want %q", c.allow, got, derr, c.want)
		}
	}
}

// File names and guard.log fields are vetted like an agent's paths before they
// can be stored as releasable, named in the question or written to `allow:`:
// a changed file literally named `**/*_test.go`, a forged guard.log path `**/*`
// and a guard.log path that is a directory never get there.
func TestGuardCheckVetsReleasablePaths(t *testing.T) {
	g := newGuardTrap(t)
	g.write("**/*_test.go", "package trap\n")             // a file name that is a glob
	g.write("pkg/ok_test.go", "package pkg\n")            // the one legitimate change
	g.write("lib_test.go/readme", "not a test\n")         // a directory named like a test file
	g.write("cases_test.go/inner_test.go", "package x\n") // a directory holding a changed test
	g.write(GuardLogRel, "2026-10-22T18:00:00Z\tBash\t**/*\tprotected_paths \"**/*_test.go\"\n"+
		"2026-10-22T18:00:01Z\tBash\tlib_test.go\tprotected_paths \"**/*_test.go\"\n"+
		"2026-10-22T18:00:02Z\tBash\tcases_test.go\tprotected_paths \"**/*_test.go\"\n"+
		"2026-10-22T18:00:03Z\tEdit\tpkg/ok_test.go\tprotected_paths \"**/*_test.go\"\n")
	res := g.check()
	if !slices.Contains(res.ProtectedChanged, "**/*_test.go") {
		t.Fatalf("premise: the glob-named file is a protected change: %q (%s)", res.ProtectedChanged, res.DiffError)
	}
	want := []string{"cases_test.go/inner_test.go", "pkg/ok_test.go"}
	if !reflect.DeepEqual(res.ReleasablePaths, want) || !res.PathsRejected {
		t.Fatalf("releasable = %q rejected=%v, want %q and rejected", res.ReleasablePaths, res.PathsRejected, want)
	}
	q := guardQuestion(res)
	for _, bad := range []string{"**", "lib_test.go,", "cases_test.go,"} {
		if strings.Contains(q, bad) {
			t.Errorf("question names %q: %s", bad, q)
		}
	}
	if !strings.Contains(q, "cases_test.go/inner_test.go, pkg/ok_test.go (and 4 paths that cannot be allowed — not a literal file under a protected glob") {
		t.Errorf("question = %s", q)
	}

	// Answered allow, the guard gains only the vetted files.
	s, _, _, taskID, p1, docPath := stopService(t)
	if _, _, err := store.UpsertStopDecision(s.DB, taskID, p1, q, GuardOptions, guardJSON(res), s.ts()); err != nil {
		t.Fatal(err)
	}
	if paths, rejected := DecisionAllowPaths(guardJSON(res)); !reflect.DeepEqual(paths, want) || !rejected {
		t.Fatalf("DecisionAllowPaths = %q, %v", paths, rejected)
	}
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: GuardOptionAllow}, answerAt(t)); err != nil {
		t.Fatal(err)
	}
	if gd, perr := mdfence.ParseGuard(readFileT(t, docPath)); perr != nil || !reflect.DeepEqual(gd.Allow, want) {
		t.Fatalf("allow = %q, %v; want %q", gd.Allow, perr, want)
	}
}

// A stored record is never trusted: a guard row (or a stop merged onto one)
// carrying raw ProtectedChanged / guard.log paths and no stored ReleasablePaths
// — a row from before they existed, or one edited at rest — is vetted when shown
// and when answered.
func TestDecisionAllowPathsVetsRawRecords(t *testing.T) {
	guard := GuardResult{
		HasGuard: true, ProtectedPaths: []string{"**/*_test.go"},
		ProtectedChanged: []string{"**/*_test.go", "pkg/ok_test.go", "pkg/dir_test.go/a_test.go"},
		GuardBlocks:      []GuardLogLine{{Tool: "Bash", Path: "**/*"}, {Tool: "Bash", Path: "pkg/dir_test.go"}},
	}
	raw := guardJSON(guard)
	var legacy map[string]any
	_ = json.Unmarshal([]byte(raw), &legacy)
	delete(legacy, "releasablePaths") // a row from before the field
	b, _ := json.Marshal(legacy)
	want := []string{"pkg/ok_test.go", "pkg/dir_test.go/a_test.go"}
	if paths, rejected := DecisionAllowPaths(string(b)); !reflect.DeepEqual(paths, want) || !rejected {
		t.Fatalf("guard row: DecisionAllowPaths = %q, %v; want %q, rejected", paths, rejected, want)
	}
	// A forged releasablePaths is re-vetted too.
	guard.ReleasablePaths = []string{"**/*", "pkg/ok_test.go", "README.md"}
	if paths, _ := DecisionAllowPaths(guardJSON(guard)); !reflect.DeepEqual(paths, []string{"pkg/ok_test.go"}) {
		t.Fatalf("forged releasable: %q", paths)
	}
	stop := guardJSONWithStop(t, guard, &StopPayload{Trigger: mdfence.StopTriggerProtectedPath, Reason: "r",
		Options: []mdfence.StopOption{{ID: GuardOptionAllow}}, Paths: []string{"pkg/dir_test.go", "pkg/new_test.go"}})
	if paths, rejected := DecisionAllowPaths(stop); !reflect.DeepEqual(paths, []string{"pkg/ok_test.go", "pkg/new_test.go"}) || !rejected {
		t.Fatalf("stop row: DecisionAllowPaths = %q, %v", paths, rejected)
	}
}

// vetAllowPaths' directory probe: a path that is a directory on disk (any
// case the index knows) or the parent of any other path is never kept.
func TestVetAllowPathsRejectsDirectories(t *testing.T) {
	g := newGuardTrap(t)
	g.write("scripts/tests/lib/x.sh", "echo\n")
	g.run("add", "-A")
	g.run("commit", "-qm", "lib")
	probe := worktreeDirProbe(worktree.ExecGit{}, g.dir)
	protected := []string{"scripts/tests/**/*", "scripts/tests/*"}
	kept, rejected := vetAllowPaths([]string{"scripts/tests/lib", "SCRIPTS/Tests/LIB", "scripts/tests/lib/x.sh"}, protected, nil, probe)
	if !reflect.DeepEqual(kept, []string{"scripts/tests/lib/x.sh"}) || !rejected {
		t.Fatalf("kept = %q rejected=%v", kept, rejected)
	}
	// Without the worktree, a path that is the parent of another is still dropped.
	kept, _ = vetAllowPaths([]string{"scripts/tests/lib"}, protected, []string{"Scripts/Tests/Lib/y.sh"}, nil)
	if len(kept) != 0 {
		t.Fatalf("parent of a changed path kept: %q", kept)
	}
	if !probe("scripts/tests/lib") || probe("scripts/tests/lib/x.sh") || probe("scripts/tests/lib/gone.sh") {
		t.Fatalf("probe misreads %s", filepath.Join(g.dir, "scripts", "tests", "lib"))
	}
}

// seedBlockedStop runs p1 to a blocked end and stores a stop decision of
// trigger on top of guard, the way raiseStopDecision does.
func seedBlockedStop(t *testing.T, s *Service, r *stubRunner, p1 int64, trigger string, guard GuardResult) {
	t.Helper()
	blockWithStop(t, s, r, p1, "kind: stop\ntrigger: "+trigger+"\nreason: r\n"+
		"options:\n  - id: keep-going\n    label: Keep going\n  - id: skip-it\n    label: Skip it\n")
	st := &mdfence.Stop{Trigger: trigger, Reason: "r",
		Options: []mdfence.StopOption{{ID: "keep-going", Label: "Keep going"}, {ID: "skip-it", Label: "Skip it"}}}
	info, err := s.loadPhase(p1)
	if err != nil {
		t.Fatal(err)
	}
	s.raiseStopDecision(p1, info, st, guard, nil)
}

// A stop on a run whose guard check flagged it (a protected edit) lets the
// re-run build on that branch only when answered allow-add-to-scope: any other
// answer leaves the protected edit unaccepted. The merged card still offers the
// guard's reject, whatever the agent's trigger was.
func TestStopOnFlaggedGuardContinuesOnlyOnAllow(t *testing.T) {
	flagged := GuardResult{HasGuard: true, ProtectedPaths: []string{"**/*_test.go"}, ProtectedChanged: []string{"a_test.go"},
		ReleasablePaths: []string{"a_test.go"}}
	for _, c := range []struct {
		name, trigger string
		guard         GuardResult
		ans           GuardAnswer
		want          bool
	}{
		{"flagged, agent option", mdfence.StopTriggerProtectedPath, flagged, GuardAnswer{Option: "skip-it"}, false},
		{"flagged, agent option with note", mdfence.StopTriggerAccessError, flagged, GuardAnswer{Option: "keep-going", Note: "x"}, false},
		{"flagged, reject", mdfence.StopTriggerAccessError, flagged, GuardAnswer{Option: GuardOptionReject}, false},
		{"flagged, own words", mdfence.StopTriggerOutOfScope, flagged, GuardAnswer{Option: mdfence.StopOptionOther, Note: "allow-add-to-scope"}, false},
		{"flagged, allow", mdfence.StopTriggerAccessError, flagged, GuardAnswer{Option: GuardOptionAllow}, true},
		{"not flagged, agent option", mdfence.StopTriggerAccessError, GuardResult{}, GuardAnswer{Option: "skip-it"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r, _, taskID, p1, docPath := stopService(t)
			seedBlockedStop(t, s, r, p1, c.trigger, c.guard)
			d, err := store.GetTaskDecision(s.DB, taskID, "D-1")
			if err != nil {
				t.Fatal(err)
			}
			if c.guard.Flagged() && (!slices.Contains(d.OptionIDs, GuardOptionAllow) || !slices.Contains(d.OptionIDs, GuardOptionReject)) {
				t.Fatalf("a stop on a flagged run must offer the guard's allow and reject: %q", d.OptionIDs)
			}
			if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", c.ans, answerAt(t)); err != nil {
				t.Fatal(err)
			}
			info, err := s.loadPhase(p1)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.stopAnsweredSinceBlock(p1, info); got != c.want {
				t.Fatalf("stopAnsweredSinceBlock = %v, want %v", got, c.want)
			}
			gd, _ := mdfence.ParseGuard(readFileT(t, docPath))
			if allowed := len(gd.Allow) > 0; allowed != (c.ans.Option == GuardOptionAllow) {
				t.Fatalf("allow list = %q after answer %q", gd.Allow, c.ans.Option)
			}
		})
	}
}

// The reserved guard ids carry the daemon's label and consequence, so an agent
// cannot dress its allow option up as "leave it alone".
func TestStopOptionsReservedIDsCarryDaemonLabels(t *testing.T) {
	got := stopOptions([]mdfence.StopOption{
		{ID: GuardOptionAllow, Label: "Leave the test alone", Consequence: "nothing is released"},
		{ID: "skip", Label: "Skip"},
	}, GuardResult{})
	want := []mdfence.StopOption{stopGuardAllowOption, {ID: "skip", Label: "Skip"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stopOptions = %+v, want %+v", got, want)
	}
}
