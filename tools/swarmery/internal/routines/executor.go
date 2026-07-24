package routines

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// defaultStepTimeout bounds a command/ai-prompt step when it sets no override
// and the whole-run timeout is larger (belt-and-braces so one wedged step can't
// consume the entire run budget silently).
const defaultStepTimeout = 5 * time.Minute

// detailCap bounds the per-run detail JSON stored in routine_runs.detail (the
// migration comment promises <=8KB).
const detailCap = 8 * 1024

// stepOutputCap bounds one step's captured output inside the detail JSON so a
// chatty command can't blow the whole-run detail budget on its own.
const stepOutputCap = 1024

// execRun runs one routine end-to-end: acquire a global-cap slot, insert a
// 'running' run row, execute the steps sequentially under the whole-run timeout,
// then stamp the terminal status + detail. The caller (fireCron/Trigger) already
// holds the per-routine single-flight lock and releases it. All failures are
// captured on the run row; nothing panics out (spawn wraps this in recover).
func (s *Service) execRun(ctx context.Context, r Routine, trigger string) {
	// Global concurrency cap. tick()/Trigger already checked activeCount, but the
	// semaphore is the hard gate that also serializes when many routines fire in
	// the same tick.
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	runID, err := s.startRun(r.ID, trigger)
	if err != nil {
		return // could not even record the run; nothing else to do
	}

	whole := time.Duration(r.TimeoutSec) * time.Second
	if whole <= 0 {
		whole = 15 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, whole)
	defer cancel()

	projectPath, err := s.projectPath(r.ProjectID)
	if err != nil {
		s.finishRun(runID, "failed", encodeDetail([]StepResult{{
			Name: "(setup)", Type: "", Status: "failed", Error: err.Error(),
		}}))
		return
	}

	results, status := s.runSteps(runCtx, r, projectPath)
	s.finishRun(runID, status, encodeDetail(results))
}

// runSteps executes the steps sequentially and returns the per-step results plus
// the aggregate run status. Rules:
//   - A whole-run timeout (runCtx cancelled) stops the run with status 'timeout'.
//   - A step that fails or per-step-times-out with continueOnFailure=false stops
//     the run with status 'failed'.
//   - A failing step with continueOnFailure=true is recorded, the run is marked
//     'failed' overall, but subsequent steps still execute.
//   - All steps ok → status 'ok'.
func (s *Service) runSteps(runCtx context.Context, r Routine, projectPath string) ([]StepResult, string) {
	results := make([]StepResult, 0, len(r.Steps))
	status := "ok"
	for _, step := range r.Steps {
		// Whole-run timeout tripped between steps → record the remaining step as
		// timed out and stop.
		if runCtx.Err() != nil {
			results = append(results, StepResult{Name: step.Name, Type: step.Type,
				Status: "timeout", Error: "whole-run timeout exceeded"})
			return results, "timeout"
		}

		res := s.execStep(runCtx, r, projectPath, step)
		results = append(results, res)
		if res.Status == "ok" {
			continue
		}

		// A non-ok step. If the whole-run deadline fired, the run is a timeout
		// regardless of continueOnFailure.
		if runCtx.Err() != nil {
			return results, "timeout"
		}
		// Otherwise this step failed (or hit only its per-step timeout). Honor
		// continueOnFailure: stop unless the step opted to continue.
		status = "failed"
		if !step.ContinueOnFailure {
			return results, status
		}
	}
	return results, status
}

// execStep dispatches one step by type and returns its result. Per-step timeout
// (step.TimeoutSec, else defaultStepTimeout, else the whole-run ctx) is layered
// on the run ctx.
func (s *Service) execStep(ctx context.Context, r Routine, projectPath string, step Step) StepResult {
	res := StepResult{Name: step.Name, Type: step.Type}
	stepCtx, cancel := s.stepContext(ctx, step)
	defer cancel()

	switch step.Type {
	case StepCommand:
		out, err := s.runCommand(stepCtx, projectPath, step.Command)
		res.Output = clip(out, stepOutputCap)
		if err != nil {
			res.Status = classify(stepCtx, err)
			res.Error = err.Error()
			return res
		}
		res.Status = "ok"
	case StepAIPrompt:
		if s.Runner == nil {
			res.Status = "failed"
			res.Error = "no runner configured"
			return res
		}
		out, err := s.Runner.Run(stepCtx, projectPath, step.Prompt, step.Model)
		res.Output = clip(out, stepOutputCap)
		if err != nil {
			res.Status = classify(stepCtx, err)
			res.Error = err.Error()
			return res
		}
		res.Status = "ok"
	case StepCreateTask:
		if s.Tasks == nil {
			res.Status = "failed"
			res.Error = "no task creator configured"
			return res
		}
		if !r.ProjectID.Valid {
			res.Status = "failed"
			res.Error = "create-task requires a project-scoped routine"
			return res
		}
		col := step.BoardColumn
		if col == "" {
			col = "triage"
		}
		cardID, err := s.Tasks.CreateTask(r.ProjectID.Int64, step.TaskTitle, step.TaskPrompt, col)
		if err != nil {
			res.Status = "failed"
			res.Error = err.Error()
			return res
		}
		res.Status = "ok"
		res.Output = "created task " + cardID
	default:
		res.Status = "failed"
		res.Error = "unknown step type " + step.Type
	}
	return res
}

// stepContext layers the per-step timeout onto the run ctx. A step override of 0
// falls back to defaultStepTimeout — but never longer than the run ctx, whose
// deadline still bounds everything.
func (s *Service) stepContext(ctx context.Context, step Step) (context.Context, context.CancelFunc) {
	d := time.Duration(step.TimeoutSec) * time.Second
	if d <= 0 {
		d = defaultStepTimeout
	}
	return context.WithTimeout(ctx, d)
}

// runCommand executes a shell command with cwd = project path (global → daemon
// cwd, i.e. cwd=""). `sh -c` so the stored command can be a normal one-liner
// (curl … | jq, &&-chains). Combined stdout+stderr is returned as the output.
func (s *Service) runCommand(ctx context.Context, cwd, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd
	// Run in a dedicated process group so a timeout kills the whole tree, not just
	// the `sh` parent. Without this, exec.CommandContext SIGKILLs only sh; an
	// orphaned child (e.g. `sleep`) keeps the stdout/stderr pipe's write end open
	// and CombinedOutput blocks until it exits on its own — so the deadline would
	// fire but the call would not return until the child finished naturally.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid → signal the entire process group (sh + descendants).
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Backstop: if a descendant still holds the pipe after the group kill, stop
	// waiting on I/O shortly after cancellation instead of hanging on Wait.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return string(out), &timeoutError{stderr: tail(string(out), stderrTailBytes)}
		}
		return string(out), fmt.Errorf("command failed: %w", err)
	}
	return string(out), nil
}

// classify maps a step error to a result status: 'timeout' when the ctx deadline
// fired or the error is a timeoutError, else 'failed'.
func classify(ctx context.Context, err error) string {
	if ctx.Err() == context.DeadlineExceeded || isTimeout(err) {
		return "timeout"
	}
	return "failed"
}

// clip trims s to <= n bytes (from the tail, which carries the actionable end of
// command output / model replies).
func clip(s string, n int) string { return tail(s, n) }

// encodeDetail renders the per-step results as JSON, hard-capped at detailCap by
// dropping earlier step outputs if the whole payload is too large (rare; only a
// pathological many-step routine hits it).
func encodeDetail(results []StepResult) string {
	b, err := json.Marshal(results)
	if err != nil {
		return `[{"status":"failed","error":"detail encode error"}]`
	}
	if len(b) <= detailCap {
		return string(b)
	}
	// Too big: strip outputs (keep status/name/error) and re-encode.
	for i := range results {
		results[i].Output = ""
	}
	b, _ = json.Marshal(results)
	if len(b) > detailCap {
		return string(b[:detailCap])
	}
	return string(b)
}
