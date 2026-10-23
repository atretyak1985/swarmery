package phaserun

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// Agent-declared stops, end to end through the REAL service: a run that ends
// `PHASE BLOCKED: protected-path` with a `kind: stop` block becomes a source=stop
// decision; the blocked re-run guard refuses the unchanged phase; the operator's
// answer writes the D-n line (and the allow: list), lifts the refusal, and the
// re-run continues on the phase's own branch with the decision in its prompt.

const stopReason = "The retry criterion needs pkg/export/writer_test.go, which the guard protects."

// stopPhaseDoc is phase 1's doc: a guard over tests that names its stop triggers.
const stopPhaseDoc = "# Phase 1 — Schema\nStatus: Pending\n\n## Acceptance Criteria\n\n- [ ] a\n- [ ] b\n\n## Guard\n\n" +
	"```yaml\nkind: guard\nprotected_paths:\n  - \"**/*_test.go\"\nstop_if:\n  - out-of-scope change\n  - protected path\n```\n\n## Completion Report\n"

// stopOutput is the executor's final reply: the stop block, then the ending.
func stopOutput(block string) string {
	return "I read the export writer.\n\n```yaml\n" + block + "```\n\nPHASE BLOCKED: protected-path"
}

const protectedPathStop = "kind: stop\ntrigger: protected-path\nreason: " + stopReason + "\npaths:\n  - pkg/export/writer_test.go\n" +
	"options:\n  - id: allow-add-to-scope\n    label: Let me edit the writer test\n    consequence: the test changes with the code\n" +
	"  - id: skip-test-change\n    label: Leave the test alone\n    consequence: criterion b stays open\n"

// stopService builds the service for phase p1 of the fixture: its doc is
// stopPhaseDoc, every Start mints a new session uuid, the worktree manager can
// continue an existing branch, and git answers every query with one tip.
func stopService(t *testing.T) (s *Service, r *stubRunner, wt *existingStubWt, taskID, p1 int64, docPath string) {
	t.Helper()
	db, taskID, p1, _ := fixture(t)
	docPath = phaseDocPath(t, db, p1)
	mustWriteDoc(t, docPath, stopPhaseDoc)
	r = &stubRunner{}
	wt = &existingStubWt{stubWt: &stubWt{}}
	s = newTestService(db, r, wt.stubWt)
	s.Wt = wt
	s.Git = quietGit{}
	n := 0
	s.UUID = func() string { n++; return fmt.Sprintf("uuid-stop-%d", n) }
	return s, r, wt, taskID, p1, docPath
}

// quietGit answers every ref query with stubBranchTip and every listing (the
// guard check's diff and untracked-file scan) with nothing: the run changed no
// protected file, so only the agent's own stop speaks.
type quietGit struct{}

func (quietGit) Run(_ string, args ...string) (string, error) {
	for _, a := range args {
		if a == "diff" || a == "ls-files" || a == "status" {
			return "", nil
		}
	}
	return stubBranchTip + "\n", nil
}

// blockWithStop runs p1 once; the executor replies with block and ends blocked.
func blockWithStop(t *testing.T, s *Service, r *stubRunner, p1 int64, block string) {
	t.Helper()
	r.mu.Lock()
	r.runFn = func(spec RunSpec) (*Run, error) {
		seedTranscript(t, s.DB, spec.SessionUUID, stopOutput(block))
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	r.mu.Unlock()
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state, _, _, runErr := phaseRow(t, s.DB, p1); state != "blocked" || runErr.String != "protected-path" {
		t.Fatalf("run_state/run_error = %q/%q, want blocked/protected-path (premise)", state, runErr.String)
	}
}

// answerAt is a moment after the blocked run ended (the service clock counts
// seconds up from testEpoch).
func answerAt(t *testing.T) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	return at.Add(time.Hour)
}

func TestStopDecisionEndToEnd(t *testing.T) {
	s, r, wt, taskID, p1, docPath := stopService(t)
	blockWithStop(t, s, r, p1, protectedPathStop)

	// The decision: source stop, the block's options, trigger/reason kept.
	ds, err := store.ListTaskDecisions(s.DB, taskID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("decisions = %+v, %v; want one", ds, err)
	}
	d := ds[0]
	if d.Source != store.DecisionSourceStop || d.Status != store.DecisionUndecided || d.DecisionID != "D-1" ||
		d.PhaseID == nil || *d.PhaseID != p1 || d.Question != stopReason {
		t.Fatalf("decision = %+v", d)
	}
	if !reflect.DeepEqual(d.OptionIDs, []string{"allow-add-to-scope", "skip-test-change"}) {
		t.Fatalf("options = %q", d.OptionIDs)
	}
	st := StopOf(d)
	if st == nil || st.Trigger != mdfence.StopTriggerProtectedPath || st.Reason != stopReason ||
		!reflect.DeepEqual(st.Paths, []string{"pkg/export/writer_test.go"}) || len(st.Flags) != 0 ||
		st.OptionLabel("skip-test-change") != "Leave the test alone" {
		t.Fatalf("stop payload = %+v", st)
	}

	// Unanswered, the unchanged phase is refused by the blocked re-run guard.
	if _, err := s.Start(p1, "", ""); !errors.Is(err, ErrBlockedUnchanged) {
		t.Fatalf("re-run before the answer: %v, want ErrBlockedUnchanged", err)
	}

	// The answer: the D-n line, the allow: list.
	before := readFileT(t, docPath)
	got, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: "allow-add-to-scope"}, answerAt(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DecisionDecided || got.Answer != "allow-add-to-scope" {
		t.Fatalf("decided = %+v", got)
	}
	after := readFileT(t, docPath)
	if line := "- **D-1** (stop: protected-path) — " + stopReason + " → allow-add-to-scope"; !strings.Contains(after, "## Decisions\n\n"+line+"\n") {
		t.Fatalf("doc lacks %q:\n%s", line, after)
	}
	g, perr := mdfence.ParseGuard(after)
	if perr != nil || !reflect.DeepEqual(g.Allow, []string{"pkg/export/writer_test.go"}) {
		t.Fatalf("guard allow = %q, %v", g.Allow, perr)
	}
	if blockedFingerprint("b", 0, nil, before) == blockedFingerprint("b", 0, nil, after) {
		t.Fatal("the answer left the blocked fingerprint unchanged")
	}

	// The re-run: admitted (the fingerprint moved), continuing on the branch that
	// holds the stopped run's partial commit, with the decision in its prompt.
	wt.stubWt.mu.Lock()
	wt.stubWt.reclaimAhead = 1
	wt.stubWt.mu.Unlock()
	r.mu.Lock()
	r.runFn = func(spec RunSpec) (*Run, error) {
		tickEveryPhaseDoc(s.DB)
		seedTranscript(t, s.DB, spec.SessionUUID, "Edited the writer test.\n\nPHASE DONE")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	r.mu.Unlock()
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("re-run after the answer: %v", err)
	}
	if len(wt.existing) != 1 {
		t.Errorf("AcquireExisting calls = %d, want 1 — the re-run must continue on its own branch", len(wt.existing))
	}
	r.mu.Lock()
	prompt := r.specs[len(r.specs)-1].Prompt
	r.mu.Unlock()
	for _, want := range []string{
		"- Decision D-1: allow-add-to-scope (Let me edit the writer test) — answers your earlier stop (protected-path): " + stopReason,
		"STOP TRIGGERS",
		"- out-of-scope change\n- protected path\n",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("re-run prompt lacks %q", want)
		}
	}
	if state, _, _, _ := phaseRow(t, s.DB, p1); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
}

// Free text is recorded as option `other`; a flagged block (one option, an
// unknown trigger) still raises the decision; unknown options are refused.
func TestStopDecisionFreeTextAndFlags(t *testing.T) {
	s, r, _, taskID, p1, docPath := stopService(t)
	blockWithStop(t, s, r, p1, "kind: stop\ntrigger: database-down\nreason: the staging login is refused\n"+
		"options:\n  - id: retry-later\n    label: Retry later\n    consequence: the phase waits\n")
	d, err := store.GetTaskDecision(s.DB, taskID, "D-1")
	if err != nil {
		t.Fatal(err)
	}
	st := StopOf(d)
	if st == nil || !reflect.DeepEqual(st.Flags, []string{mdfence.LintStopTriggerUnknown, mdfence.LintStopOptionsCount}) {
		t.Fatalf("stop = %+v, want both lints", st)
	}
	for _, bad := range []GuardAnswer{{Option: "reject"}, {Option: mdfence.StopOptionOther}, {}} {
		if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", bad, answerAt(t)); !errors.Is(err, ErrDecisionBadOption) {
			t.Errorf("answer %+v: %v, want ErrDecisionBadOption", bad, err)
		}
	}
	got, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1",
		GuardAnswer{Option: mdfence.StopOptionOther, Note: "use the\nread-only replica"}, answerAt(t))
	if err != nil || got.Answer != "other: use the read-only replica" {
		t.Fatalf("free text = %+v, %v", got, err)
	}
	doc := readFileT(t, docPath)
	if line := "- **D-1** (stop: database-down) — the staging login is refused → other: use the read-only replica"; !strings.Contains(doc, line) {
		t.Fatalf("doc lacks %q:\n%s", line, doc)
	}
	if g, _ := mdfence.ParseGuard(doc); len(g.Allow) != 0 {
		t.Errorf("a non-protected-path answer widened the guard: %q", g.Allow)
	}
}

// TestBlockedStopAnsweredLiftsCooldown (blocked.go): inside the cooldown the
// blocked phase is refused while its stop is open, and admitted the moment the
// stop is answered — the D-n line is the change. Picking the other option
// (no allow: list) is enough on its own.
func TestBlockedStopAnsweredLiftsCooldown(t *testing.T) {
	s, r, _, taskID, p1, _ := stopService(t)
	blockWithStop(t, s, r, p1, protectedPathStop)
	if _, err := s.Start(p1, "", ""); !errors.Is(err, ErrBlockedUnchanged) {
		t.Fatalf("open stop: %v, want ErrBlockedUnchanged", err)
	}
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: "skip-test-change", Note: "later"}, answerAt(t)); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.runFn = func(spec RunSpec) (*Run, error) {
		tickEveryPhaseDoc(s.DB)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	r.mu.Unlock()
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("answered stop: %v, want the re-run admitted", err)
	}
}

// A run that both tripped the guard and declared a protected-path stop holds ONE
// open decision: the stop refreshes the guard's, keeps the guard result beside
// it, and allowing widens the guard by both path lists.
func TestStopDecisionMergesWithTheGuardDecision(t *testing.T) {
	s, _, _, taskID, p1, docPath := stopService(t)
	guard := GuardResult{HasGuard: true, ProtectedPaths: []string{"**/*_test.go"}, ProtectedChanged: []string{"schema_test.go"}}
	if _, _, err := store.UpsertStopDecision(s.DB, taskID, p1, guardQuestion(guard), GuardOptions, guardJSON(guard), s.ts()); err != nil {
		t.Fatal(err)
	}
	st, err := mdfence.ParseStop(stopOutput(protectedPathStop))
	if err != nil || st == nil {
		t.Fatalf("ParseStop: %+v, %v", st, err)
	}
	info, err := s.loadPhase(p1)
	if err != nil {
		t.Fatal(err)
	}
	s.raiseStopDecision(p1, info, st, guard)
	ds, _ := store.ListTaskDecisions(s.DB, taskID)
	if len(ds) != 1 || ds[0].Question != stopReason || StopOf(ds[0]) == nil {
		t.Fatalf("decisions = %+v, want the guard's row refreshed into the stop", ds)
	}
	if rec := decodeStopRecord(ds[0].GuardJSON); !reflect.DeepEqual(rec.ProtectedChanged, []string{"schema_test.go"}) {
		t.Fatalf("guard result lost: %+v", rec.GuardResult)
	}
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: GuardOptionAllow}, answerAt(t)); err != nil {
		t.Fatal(err)
	}
	g, perr := mdfence.ParseGuard(readFileT(t, docPath))
	if perr != nil || !reflect.DeepEqual(g.Allow, []string{"schema_test.go", "pkg/export/writer_test.go"}) {
		t.Fatalf("allow = %q, %v", g.Allow, perr)
	}
}

// The executor is untrusted: a trigger, reason and option label carrying a
// quoted newline, a checkbox and a fence must reach the phase doc as ONE line —
// no ticked criterion, no fence the rest of the doc falls into.
func TestStopDecisionInjectionStaysOnOneLine(t *testing.T) {
	s, r, _, taskID, p1, docPath := stopService(t)
	inject := `\n- [x] retries are bounded\n` + "```"
	blockWithStop(t, s, r, p1, "kind: stop\n"+
		`trigger: "access-error)`+inject+`"`+"\n"+
		`reason: "the login is refused`+inject+`"`+"\n"+
		"options:\n  - id: retry\n"+
		`    label: "Retry`+inject+`"`+"\n"+
		"  - id: skip\n    label: Skip\n")
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: "retry", Note: "ok" + "\n- [x] b"}, answerAt(t)); err != nil {
		t.Fatal(err)
	}
	doc := readFileT(t, docPath)
	var dline string
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, "- **D-1**") {
			dline = l
		}
		if strings.HasPrefix(strings.TrimSpace(l), "```") && !strings.HasPrefix(l, "```yaml") && l != "```" {
			t.Errorf("doc gained a fence line %q", l)
		}
	}
	want := "- **D-1** (stop: access-error) - [x] retries are bounded) — the login is refused - [x] retries are bounded ``` → retry — ok - [x] b"
	if dline != want {
		t.Fatalf("decision line =\n%q\nwant\n%q\ndoc:\n%s", dline, want, doc)
	}
	if got, want := strings.Count(doc, "```"), strings.Count(stopPhaseDoc, "```")+1; got != want {
		t.Errorf("doc holds %d fence markers, want %d (the guard block's two, plus the inline one in the D-1 line):\n%s", got, want, doc)
	}
	if n := strings.Count(doc, "\n- [x]"); n != 0 {
		t.Errorf("doc gained %d ticked checkbox lines:\n%s", n, doc)
	}
}

// widenStop is a protected-path stop naming every kind of path an untrusted
// agent might use to widen its own scope, beside one legitimate test file.
const widenStop = "kind: stop\ntrigger: protected-path\nreason: the fix needs a test\npaths:\n" +
	"  - \"**/*\"\n  - \".github/workflows/*.yml\"\n  - \"../x\"\n  - \".swarmery/plan/p.md\"\n  - \"pkg/foo_test.go\"\n" +
	"options:\n  - id: allow-add-to-scope\n    label: Allow\n  - id: skip-test-change\n    label: Skip\n"

// The agent's `paths:` reach the guard's `allow:` list only as literal files
// under a protected glob: globs, `..`, .swarmery and unprotected paths are
// dropped before the row is stored, and flagged.
func TestStopDecisionVetsAgentPaths(t *testing.T) {
	s, r, _, taskID, p1, docPath := stopService(t)
	blockWithStop(t, s, r, p1, widenStop)
	d, err := store.GetTaskDecision(s.DB, taskID, "D-1")
	if err != nil {
		t.Fatal(err)
	}
	st := StopOf(d)
	if st == nil || !reflect.DeepEqual(st.Paths, []string{"pkg/foo_test.go"}) ||
		!reflect.DeepEqual(st.Flags, []string{mdfence.LintStopPathsRejected}) {
		t.Fatalf("stop payload = %+v, want only pkg/foo_test.go and stop_paths_rejected", st)
	}
	if got := StopAllowPaths(d.GuardJSON); !reflect.DeepEqual(got, []string{"pkg/foo_test.go"}) {
		t.Fatalf("StopAllowPaths = %q", got)
	}
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: GuardOptionAllow}, answerAt(t)); err != nil {
		t.Fatal(err)
	}
	g, perr := mdfence.ParseGuard(readFileT(t, docPath))
	if perr != nil || !reflect.DeepEqual(g.Allow, []string{"pkg/foo_test.go"}) {
		t.Fatalf("allow = %q, %v; want only pkg/foo_test.go", g.Allow, perr)
	}
}

// A row raised before the paths were vetted (its stored paths raw) is vetted
// again when it is answered: the stored payload is never trusted on its own.
func TestStopDecisionRevetsStoredPathsOnAnswer(t *testing.T) {
	s, _, _, taskID, p1, docPath := stopService(t)
	guard := GuardResult{HasGuard: true, ProtectedPaths: []string{"**/*_test.go"}}
	raw := guardJSONWithStop(t, guard, &StopPayload{
		Trigger: mdfence.StopTriggerProtectedPath, Reason: "r",
		Options: []mdfence.StopOption{{ID: GuardOptionAllow, Label: "Allow"}, {ID: "skip", Label: "Skip"}},
		Paths:   []string{"**/*", "pkg/foo_test.go", "README.md"},
	})
	if _, _, err := store.UpsertStopDecision(s.DB, taskID, p1, "r", []string{GuardOptionAllow, "skip"}, raw, s.ts()); err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: GuardOptionAllow}, answerAt(t)); err != nil {
		t.Fatal(err)
	}
	g, perr := mdfence.ParseGuard(readFileT(t, docPath))
	if perr != nil || !reflect.DeepEqual(g.Allow, []string{"pkg/foo_test.go"}) {
		t.Fatalf("allow = %q, %v; want only pkg/foo_test.go", g.Allow, perr)
	}
}

// A blocked run's GUARD decision answered reject must not let the re-run build
// on the branch that holds the rejected protected edits; answered allow, it may
// — as an agent stop may, whatever its answer.
func TestStopAnsweredSinceBlockGuardReject(t *testing.T) {
	for _, c := range []struct {
		option string
		want   bool
	}{{GuardOptionReject, false}, {GuardOptionAllow, true}} {
		t.Run(c.option, func(t *testing.T) {
			s, r, _, taskID, p1, _ := stopService(t)
			r.mu.Lock()
			r.runFn = func(spec RunSpec) (*Run, error) {
				seedTranscript(t, s.DB, spec.SessionUUID, "Touched the test.\n\nPHASE BLOCKED: protected-path")
				return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
			}
			r.mu.Unlock()
			if _, err := s.Start(p1, "", ""); err != nil {
				t.Fatalf("Start: %v", err)
			}
			guard := GuardResult{HasGuard: true, ProtectedPaths: []string{"**/*_test.go"}, ProtectedChanged: []string{"a_test.go"}}
			if _, _, err := store.UpsertStopDecision(s.DB, taskID, p1, guardQuestion(guard), GuardOptions, guardJSON(guard), s.ts()); err != nil {
				t.Fatal(err)
			}
			if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: c.option}, answerAt(t)); err != nil {
				t.Fatal(err)
			}
			info, err := s.loadPhase(p1)
			if err != nil {
				t.Fatal(err)
			}
			if info.RunState != "blocked" {
				t.Fatalf("run_state = %q, want blocked (premise)", info.RunState)
			}
			if got := s.stopAnsweredSinceBlock(p1, info); got != c.want {
				t.Fatalf("stopAnsweredSinceBlock after a guard %s = %v, want %v", c.option, got, c.want)
			}
		})
	}
}

func guardJSONWithStop(t *testing.T, g GuardResult, st *StopPayload) string {
	t.Helper()
	b, err := json.Marshal(stopRecord{GuardResult: g, Stop: st})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGuardGlobMatch(t *testing.T) {
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"**/*_test.go", "pkg/foo_test.go", true},
		{"**/*_test.go", "foo_test.go", true},
		{"**/*_test.go", "pkg/a/b/Foo_TEST.go", true},
		{"**/*_test.go", "pkg/foo.go", false},
		{".github/workflows/*.yml", ".github/workflows/ci.yml", true},
		{".github/workflows/*.yml", ".github/workflows/sub/ci.yml", false},
		{"scripts/tests/", "scripts/tests/a/b.sh", true},
		{"scripts/tests/**", "scripts/tests", false},
		{"./docs/*.md", "docs/a.md", true},
		{"**", "anything/at/all", true},
		{"pkg/[", "pkg/[", false},
	} {
		if got := guardGlobMatch(c.glob, c.path); got != c.want {
			t.Errorf("guardGlobMatch(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

// A blocked run without a stop block, and a done run whose reply quotes one,
// raise nothing.
func TestStopStageIgnoresPlainBlocksAndDoneRuns(t *testing.T) {
	s, _, _, taskID, p1, _ := stopService(t)
	info, err := s.loadPhase(p1)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscript(t, s.DB, "plain", "PHASE BLOCKED: the dependency is not merged")
	s.stopStage(p1, info, "blocked", "plain", GuardResult{})
	seedTranscript(t, s.DB, "done", stopOutput(protectedPathStop)+"\n\nPHASE DONE")
	s.stopStage(p1, info, "done", "done", GuardResult{})
	if ds, _ := store.ListTaskDecisions(s.DB, taskID); len(ds) != 0 {
		t.Fatalf("decisions = %+v, want none", ds)
	}
}
