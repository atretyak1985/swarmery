package routines

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEnabledEnv(t *testing.T) {
	cases := map[string]bool{
		"":      true, // unset ⇒ enabled
		"1":     true,
		"true":  true,
		"0":     false,
		"false": false,
		"off":   false,
		"OFF":   false,
		" 0 ":   false,
	}
	for v, want := range cases {
		t.Setenv("SWARMERY_ROUTINES", v)
		if got := Enabled(); got != want {
			t.Errorf("Enabled() with SWARMERY_ROUTINES=%q = %v, want %v", v, got, want)
		}
	}
}

func TestParseCronErrors(t *testing.T) {
	if _, err := ParseCron(""); err == nil {
		t.Error("blank cron should error")
	}
	if _, err := ParseCron("* * *"); err == nil {
		t.Error("3-field cron should error (want 5-field)")
	}
	if _, err := ParseCron("*/15 * * * *"); err != nil {
		t.Errorf("valid cron rejected: %v", err)
	}
}

func TestValidateStepSlice(t *testing.T) {
	if _, err := ValidateStepSlice(nil); err == nil {
		t.Error("nil slice should error")
	}
	got, err := ValidateStepSlice([]Step{{Type: StepCommand, Name: "c", Command: "ls"}})
	if err != nil || len(got) != 1 {
		t.Errorf("valid slice: got=%v err=%v", got, err)
	}
}

func TestEncodeDetailTruncatesManySteps(t *testing.T) {
	// Build results whose combined JSON far exceeds detailCap even without
	// outputs, forcing the hard [:detailCap] slice branch.
	results := make([]StepResult, 2000)
	for i := range results {
		results[i] = StepResult{Name: "step-with-a-longish-name", Type: StepCommand, Status: "ok",
			Output: "some output text that will be stripped in the first pass"}
	}
	got := encodeDetail(results)
	if len(got) > detailCap {
		t.Errorf("encodeDetail did not cap: %d > %d", len(got), detailCap)
	}
}

func TestRunErrorFormatting(t *testing.T) {
	// Pure error-type behavior (the real ClaudeRunner path that constructs these).
	to := &timeoutError{stderr: "some tail"}
	if to.Error() == "" || !strings.Contains(to.Error(), "timed out") {
		t.Errorf("timeoutError.Error() = %q", to.Error())
	}
	if isTimeout(to) != true {
		t.Error("isTimeout(timeoutError) should be true")
	}
	re := &runError{err: errors.New("exit 1"), stderr: "boom"}
	if !strings.Contains(re.Error(), "exit 1") || !strings.Contains(re.Error(), "boom") {
		t.Errorf("runError.Error() = %q", re.Error())
	}
	if isTimeout(re) {
		t.Error("isTimeout(runError) should be false")
	}
	// Empty-stderr variants.
	if (&timeoutError{}).Error() != "timed out" {
		t.Error("timeoutError with no stderr")
	}
	if (&runError{err: errors.New("x")}).Error() != "x" {
		t.Error("runError with no stderr")
	}
}

// TestStartSchedulerInitialTickThenStop verifies StartScheduler runs an initial
// pass immediately (firing a due routine) and returns promptly when ctx is
// cancelled, without waiting a full TickInterval.
func TestStartSchedulerInitialTickThenStop(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "boot", CronExpr: "* * * * *", Enabled: true,
		CatchUp: "run_one", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	forceNextRun(t, s, r.ID, clk.now())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.StartScheduler(ctx) // synchronous Go seam ⇒ the initial tick runs inline
		close(done)
	}()
	// The initial pass already ran the due routine (Go seam is synchronous).
	waitUntil(t, func() bool { return countRuns(t, s, r.ID) == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartScheduler did not return after cancel")
	}
}
