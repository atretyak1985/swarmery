package decide

import (
	"strings"
	"testing"
)

// Every task type the model may answer is defined in the prompt that offers
// it, and a plan engine run's prompt defines no `planning`.
func TestTaskTypePromptDefinesEveryOption(t *testing.T) {
	if len(d2TaskTypeDefs) != len(TaskTypes) {
		t.Errorf("%d definitions for %d task types", len(d2TaskTypeDefs), len(TaskTypes))
	}
	for _, o := range TaskTypes {
		def := d2TaskTypeDefs[o]
		if def == "" {
			t.Errorf("task type %q has no definition", o)
			continue
		}
		if !strings.Contains(d2TaskTypePrompt, o+" = "+def) {
			t.Errorf("the prompt does not define %q: %q", o, d2TaskTypePrompt)
		}
		if want := o != "planning"; strings.Contains(d2RunTaskTypePrompt, o+" = ") != want {
			t.Errorf("run prompt defines %q = %v, want %v: %q", o, !want, want, d2RunTaskTypePrompt)
		}
	}
	if !strings.HasPrefix(d2TaskTypePrompt, "What kind of task was this coding session?") {
		t.Errorf("prompt lost its question: %q", d2TaskTypePrompt)
	}
	if !strings.HasSuffix(d2RunTaskTypePrompt, d2RunTaskTypeSuffix) {
		t.Errorf("run prompt lost its closing sentence: %q", d2RunTaskTypePrompt)
	}
	// The label evidence the definitions rest on: a handoff file and a how-to
	// block are docs, chores are ops, a probe is other.
	for o, word := range map[string]string{"docs": "session handoff file", "ops": "pull requests", "review": "judged existing work", "other": "rare"} {
		if !strings.Contains(d2TaskTypeDefs[o], word) {
			t.Errorf("%s definition lost %q: %q", o, word, d2TaskTypeDefs[o])
		}
	}
}

// The task-type question reads the head of the first user turn when the title
// does not already say it; the outcome and failure questions never do.
func TestTaskTypeOpeningLine(t *testing.T) {
	db := openDB(t)

	// A handoff session: titled after the work it hands off.
	handoff := "You are writing a session HANDOFF file.\n\nA developer   will /clear their overloaded session. " + strings.Repeat("More. ", 80)
	seedEmptySession(t, db, "s-handoff", "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, "s-handoff", 1, "user", "", "", handoff)
	seedTurn(t, db, "s-handoff", 2, "assistant", testModel, "end_turn", "# Handoff\n\nDone.")
	qs := d2QuestionsFor(t, db, "s-handoff", false)
	task := qs[QD2TaskType]
	line, rest, ok := strings.Cut(task.Input, "\n")
	if !ok || !strings.HasPrefix(line, "first request: You are writing a session HANDOFF file. A developer will /clear their overloaded session. More.") {
		t.Fatalf("task-type input does not open with the first request on one line:\n%s", task.Input)
	}
	if got := len(strings.TrimPrefix(line, "first request: ")); got > d2OpeningBytes {
		t.Errorf("first request is %d bytes, cap %d", got, d2OpeningBytes)
	}
	if !strings.HasPrefix(rest, "title: fix the parser\n") {
		t.Errorf("the digest must follow the first request unchanged:\n%s", task.Input)
	}
	if qs[QD2Outcome].Input != rest || qs[QD2Failure].Input != rest {
		t.Errorf("outcome and failure must read the digest alone:\noutcome:\n%s\nfailure:\n%s", qs[QD2Outcome].Input, qs[QD2Failure].Input)
	}

	// A title cut from the first prompt already says it: no line.
	seedEmptySession(t, db, "s-echo", "2026-09-20T11:00:00.000Z", "")
	mustExec(t, db, `UPDATE sessions SET title = 'Write the how-to block for the item below.  ## The contract' WHERE session_uuid = 's-echo'`)
	seedTurn(t, db, "s-echo", 1, "user", "", "", "Write the how-to block for the item below.\n\n## The contract\n\nThree headings.")
	seedTurn(t, db, "s-echo", 2, "assistant", testModel, "end_turn", "# How to use")
	if in := d2QuestionsFor(t, db, "s-echo", false)[QD2TaskType].Input; strings.Contains(in, "first request:") || !strings.HasPrefix(in, "title: ") {
		t.Errorf("a title that repeats the first request must not quote it again:\n%s", in)
	}

	// A plan engine run is named by its phase, not by the engine's prompt.
	seedEmptySession(t, db, "s-run", "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, "s-run", 1, "user", "", "", PhaseRunPromptHead+", headlessly.\n\nPHASE DOCUMENT (plan/phase-1-x.md):\n# Phase 1 — X\n\n## Goal\n\nDo X.\n")
	seedTurn(t, db, "s-run", 2, "assistant", testModel, "end_turn", "PHASE DONE")
	if in := d2QuestionsFor(t, db, "s-run", false)[QD2TaskType].Input; strings.Contains(in, "first request:") || !strings.Contains(in, "phase: Phase 1 — X\n") {
		t.Errorf("a run must name its phase and quote no first request:\n%s", in)
	}

	// A session with no user turn has nothing to quote.
	seedSession(t, db, "s-assistant-only", "2026-09-20T11:00:00.000Z", "")
	if in := d2QuestionsFor(t, db, "s-assistant-only", false)[QD2TaskType].Input; strings.Contains(in, "first request:") {
		t.Errorf("no user turn, yet a first request was quoted:\n%s", in)
	}
}

// The failure prompt keeps `none` for a session that finished what it was
// asked, names the blocked ending and the no-answer ending, and keeps the
// account and API clauses word for word.
func TestFailurePromptReservesNone(t *testing.T) {
	for _, want := range []string{
		"none if it shipped",
		"a final answer that finished what was asked, with no error",
		"a last message that reports the work BLOCKED is never none",
		"blocked-on-operator = the session ended waiting on the operator",
		"other = it stopped with no final answer (final answer: no)",
		"auth = the CLI was not logged in or its login expired; " +
			"quota = a usage or spend limit stopped the session; " +
			"api-error = the API was unreachable, overloaded or dropped the response.",
	} {
		if !strings.Contains(d2FailurePrompt, want) {
			t.Errorf("failure prompt lacks %q:\n%s", want, d2FailurePrompt)
		}
	}
	// Every cause the prompt defines is one the model may answer.
	for _, clause := range strings.Split(d2FailurePrompt, "; ") {
		name, _, ok := strings.Cut(clause, " = ")
		if !ok || strings.ContainsAny(name, " (") {
			continue
		}
		found := false
		for _, c := range FailureCauses {
			found = found || c == name
		}
		if !found {
			t.Errorf("failure prompt defines %q, which is not a failure cause", name)
		}
	}
}
