package routines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// runInline executes a routine synchronously (the Go seam is synchronous) and
// returns its single run row + decoded step results.
func runInline(t *testing.T, s *Service, r Routine, trigger string) (Run, []StepResult) {
	t.Helper()
	s.execRun(context.Background(), r, trigger)
	runs, err := s.Runs(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("no run recorded")
	}
	run := runs[0]
	var results []StepResult
	if run.Detail.Valid {
		if err := json.Unmarshal([]byte(run.Detail.String), &results); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
	}
	return run, results
}

func TestExecCommandStepOK(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "cmd", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "echo", Command: "echo hello-routines"})})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "ok" {
		t.Errorf("run status = %q, want ok", run.Status)
	}
	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("results = %+v", results)
	}
	if !strings.Contains(results[0].Output, "hello-routines") {
		t.Errorf("output missing command stdout: %q", results[0].Output)
	}
}

func TestExecCommandStepFails(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "cmd", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "false", Command: "exit 3"})})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
	if results[0].Status != "failed" || results[0].Error == "" {
		t.Errorf("step result = %+v", results[0])
	}
}

func TestExecAIPromptStep(t *testing.T) {
	sr := &stubRunner{out: "model said hi"}
	s, _ := newTestSvc(t, sr, &stubTasks{})
	seedProject(t, s.DB, 1, "/tmp/proj")
	r := mustCreate(t, s, CreateParams{
		ProjectID: sql.NullInt64{Int64: 1, Valid: true},
		Name:      "ai", TimeoutSec: 60,
		Steps: mkSteps(Step{Type: StepAIPrompt, Name: "ask", Prompt: "do X", Model: "sonnet"})})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "ok" {
		t.Errorf("run status = %q, want ok", run.Status)
	}
	if sr.callCount() != 1 {
		t.Fatalf("runner called %d times, want 1", sr.callCount())
	}
	if sr.calls[0].cwd != "/tmp/proj" {
		t.Errorf("ai-prompt cwd = %q, want /tmp/proj", sr.calls[0].cwd)
	}
	if sr.calls[0].model != "sonnet" {
		t.Errorf("ai-prompt model = %q, want sonnet", sr.calls[0].model)
	}
	if !strings.Contains(results[0].Output, "model said hi") {
		t.Errorf("ai-prompt output not captured: %q", results[0].Output)
	}
}

func TestExecAIPromptError(t *testing.T) {
	sr := &stubRunner{err: errors.New("boom")}
	s, _ := newTestSvc(t, sr, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "ai", TimeoutSec: 60,
		Steps: mkSteps(Step{Type: StepAIPrompt, Name: "ask", Prompt: "do X"})})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
	if results[0].Status != "failed" {
		t.Errorf("step status = %q, want failed", results[0].Status)
	}
}

func TestExecCreateTaskStep(t *testing.T) {
	st := &stubTasks{id: "T-newcrd"}
	s, _ := newTestSvc(t, &stubRunner{}, st)
	seedProject(t, s.DB, 1, "/tmp/proj")
	r := mustCreate(t, s, CreateParams{
		ProjectID: sql.NullInt64{Int64: 1, Valid: true},
		Name:      "mk", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCreateTask, Name: "card", TaskTitle: "Fix", TaskPrompt: "Do the fix", BoardColumn: "todo"})})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "ok" {
		t.Errorf("run status = %q, want ok", run.Status)
	}
	if st.callCount() != 1 {
		t.Fatalf("task creator called %d times, want 1", st.callCount())
	}
	c := st.calls[0]
	if c.projectID != 1 || c.title != "Fix" || c.col != "todo" {
		t.Errorf("create-task call = %+v", c)
	}
	if !strings.Contains(results[0].Output, "T-newcrd") {
		t.Errorf("create-task output missing card id: %q", results[0].Output)
	}
}

func TestExecCreateTaskRequiresProject(t *testing.T) {
	st := &stubTasks{}
	s, _ := newTestSvc(t, &stubRunner{}, st)
	// Global routine (no project) + create-task step ⇒ fails.
	r := mustCreate(t, s, CreateParams{Name: "mk", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCreateTask, Name: "card", TaskTitle: "Fix", TaskPrompt: "Body"})})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
	if st.callCount() != 0 {
		t.Errorf("task creator should not be called for a global routine")
	}
	if !strings.Contains(results[0].Error, "project-scoped") {
		t.Errorf("error should mention project scope: %q", results[0].Error)
	}
}

func TestContinueOnFailureTrue(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "chain", TimeoutSec: 30,
		Steps: mkSteps(
			Step{Type: StepCommand, Name: "bad", Command: "exit 1", ContinueOnFailure: true},
			Step{Type: StepCommand, Name: "good", Command: "echo second-ran"},
		)})
	run, results := runInline(t, s, r, "manual")
	// Overall failed (a step failed), but the second step still executed.
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (second step must run)", len(results))
	}
	if results[0].Status != "failed" || results[1].Status != "ok" {
		t.Errorf("results = %+v", results)
	}
	if !strings.Contains(results[1].Output, "second-ran") {
		t.Errorf("second step did not run: %q", results[1].Output)
	}
}

func TestContinueOnFailureFalseStops(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "chain", TimeoutSec: 30,
		Steps: mkSteps(
			Step{Type: StepCommand, Name: "bad", Command: "exit 1"}, // continueOnFailure=false
			Step{Type: StepCommand, Name: "never", Command: "echo should-not-run"},
		)})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
	if len(results) != 1 {
		t.Errorf("results = %d, want 1 (second step must be skipped)", len(results))
	}
}

func TestPerStepTimeout(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	// Whole-run budget is generous; the STEP timeout (1s) trips on a 10s sleep.
	r := mustCreate(t, s, CreateParams{Name: "slow", TimeoutSec: 60,
		Steps: mkSteps(Step{Type: StepCommand, Name: "sleep", Command: "sleep 10", TimeoutSec: 1})})
	start := time.Now()
	run, results := runInline(t, s, r, "manual")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("per-step timeout did not fire promptly (took %s)", time.Since(start))
	}
	if results[0].Status != "timeout" {
		t.Errorf("step status = %q, want timeout", results[0].Status)
	}
	// A per-step timeout with continueOnFailure=false → run failed.
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
}

func TestWholeRunTimeout(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	// Whole-run budget is 1s; the first step sleeps 10s (its own step budget is
	// larger), so the run ctx deadline fires → run status 'timeout'.
	r := mustCreate(t, s, CreateParams{Name: "whole", TimeoutSec: 1,
		Steps: mkSteps(
			Step{Type: StepCommand, Name: "sleep", Command: "sleep 10", TimeoutSec: 30},
			Step{Type: StepCommand, Name: "after", Command: "echo after"},
		)})
	start := time.Now()
	run, results := runInline(t, s, r, "manual")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("whole-run timeout did not fire promptly (took %s)", time.Since(start))
	}
	if run.Status != "timeout" {
		t.Errorf("run status = %q, want timeout", run.Status)
	}
	if len(results) < 1 || results[0].Status != "timeout" {
		t.Errorf("first step should be timeout: %+v", results)
	}
}

func TestDetailTruncation(t *testing.T) {
	// A step whose output far exceeds the per-step cap is clipped, and the
	// overall detail stays under detailCap.
	big := strings.Repeat("x", 50_000)
	sr := &stubRunner{out: big}
	s, _ := newTestSvc(t, sr, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "big", TimeoutSec: 60,
		Steps: mkSteps(Step{Type: StepAIPrompt, Name: "ask", Prompt: "spew"})})
	run, results := runInline(t, s, r, "manual")
	if run.Status != "ok" {
		t.Errorf("run status = %q, want ok", run.Status)
	}
	if len(results[0].Output) > stepOutputCap {
		t.Errorf("output not clipped: %d > %d", len(results[0].Output), stepOutputCap)
	}
	if run.Detail.Valid && len(run.Detail.String) > detailCap {
		t.Errorf("detail exceeds cap: %d > %d", len(run.Detail.String), detailCap)
	}
}

func TestMissingRunnerAndTasks(t *testing.T) {
	// Service with nil Runner + nil Tasks: ai-prompt and create-task steps fail
	// cleanly rather than panicking.
	db := migratedTestDB(t)
	s := NewService(db, nil, nil, true)
	s.Go = func(fn func()) { fn() }
	seedProject(t, s.DB, 1, "/tmp/p")
	r := mustCreate(t, s, CreateParams{
		ProjectID: sql.NullInt64{Int64: 1, Valid: true}, Name: "x", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepAIPrompt, Name: "ask", Prompt: "p", ContinueOnFailure: true},
			Step{Type: StepCreateTask, Name: "mk", TaskTitle: "T", TaskPrompt: "B"})})
	_, results := runInline(t, s, r, "manual")
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Status != "failed" || !strings.Contains(results[0].Error, "runner") {
		t.Errorf("ai-prompt without runner: %+v", results[0])
	}
	if results[1].Status != "failed" || !strings.Contains(results[1].Error, "task creator") {
		t.Errorf("create-task without creator: %+v", results[1])
	}
}
