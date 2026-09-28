package lessons

import (
	"context"
	"errors"
	"testing"
	"time"
)

func generationRow(t *testing.T, g *Generator, uuid string) (state, msg string) {
	t.Helper()
	if err := g.DB.QueryRow(`SELECT state, error FROM lesson_generations WHERE source_phase_run = ?`, uuid).
		Scan(&state, &msg); err != nil {
		t.Fatalf("generation row %s: %v", uuid, err)
	}
	return state, msg
}

func claimRunning(t *testing.T, g *Generator, phaseID int64, uuid string, at time.Time) {
	t.Helper()
	mustExec(t, g.DB, `INSERT INTO lesson_generations (source_phase_run, phase_id, state, model, created_at)
		VALUES (?, ?, 'running', 'm', ?)`, uuid, phaseID, at.UTC().Format(time.RFC3339))
}

// A generation orphaned in 'running' by a restart is failed at startup and
// then retried once by the next scoring pass; a retry interrupted again is final.
func TestRecoverInterruptedRunningIsRetriedOnce(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, runUUID, 0.9, report)
	r := &fakeRunner{out: lessonJSON(phaseID)}
	g := newGen(db, r)
	claimRunning(t, g, phaseID, runUUID, fixedNow.Add(-11*time.Minute))

	n, err := g.Recover()
	if err != nil || n != 1 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	if state, msg := generationRow(t, g, runUUID); state != "failed" || msg != errInterrupted {
		t.Fatalf("after recover: %s %q", state, msg)
	}

	out, err := g.Generate(context.Background(), phaseID, runUUID)
	if err != nil || out.Skipped != "" || out.Inserted != 1 || r.calls != 1 {
		t.Fatalf("retry = %+v %v calls=%d", out, err, r.calls)
	}
	if state, msg := generationRow(t, g, runUUID); state != "done" || msg != "" {
		t.Fatalf("after retry: %s %q", state, msg)
	}
	again, err := g.Generate(context.Background(), phaseID, runUUID)
	if err != nil || again.Skipped == "" || r.calls != 1 {
		t.Fatalf("a done retry must stay done: %+v %v calls=%d", again, err, r.calls)
	}
}

func TestRecoverInterruptedRetryIsNotRetriedAgain(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, runUUID, 0.9, report)
	g := newGen(db, nil)
	claimRunning(t, g, phaseID, runUUID, fixedNow.Add(-time.Hour))
	if _, err := g.Recover(); err != nil {
		t.Fatal(err)
	}
	// The retry claim; the model call observes the row, then the process
	// "dies" — the row is put back to what the claim alone wrote.
	spy := &claimSpy{g: g, uuid: runUUID}
	g.Runner = spy
	_, _ = g.Generate(context.Background(), phaseID, runUUID)
	if spy.state != "running" || spy.msg != errRetrying {
		t.Fatalf("retry claim: %s %q", spy.state, spy.msg)
	}
	mustExec(t, db, `UPDATE lesson_generations SET state = 'running', error = ?, finished_at = NULL
		WHERE source_phase_run = ?`, spy.msg, runUUID)

	r := &fakeRunner{out: lessonJSON(phaseID)}
	later := newGen(db, r)
	later.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	if n, err := later.Recover(); err != nil || n != 1 {
		t.Fatalf("second recover = %d, %v", n, err)
	}
	if state, msg := generationRow(t, later, runUUID); state != "failed" || msg != errRetryInterrupted {
		t.Fatalf("after second recover: %s %q", state, msg)
	}
	out, err := later.Generate(context.Background(), phaseID, runUUID)
	if err != nil || out.Skipped == "" || r.calls != 0 {
		t.Fatalf("an interrupted retry must not retry again: %+v %v calls=%d", out, err, r.calls)
	}
}

// claimSpy records the generation row as the model call sees it.
type claimSpy struct {
	g          *Generator
	uuid       string
	state, msg string
}

func (s *claimSpy) Run(context.Context, string) (string, error) {
	_ = s.g.DB.QueryRow(`SELECT state, error FROM lesson_generations WHERE source_phase_run = ?`, s.uuid).
		Scan(&s.state, &s.msg)
	return "", errors.New("killed")
}

// A generation under ten minutes old may still be in flight: left alone.
func TestStaleRunningLeavesFreshRunning(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, runUUID, 0.9, report)
	r := &fakeRunner{out: lessonJSON(phaseID)}
	g := newGen(db, r)
	claimRunning(t, g, phaseID, runUUID, fixedNow.Add(-9*time.Minute))
	if n, err := g.Recover(); err != nil || n != 0 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	if state, _ := generationRow(t, g, runUUID); state != "running" {
		t.Fatalf("fresh row = %s", state)
	}
	if out, _ := g.Generate(context.Background(), phaseID, runUUID); out.Skipped == "" || r.calls != 0 {
		t.Fatalf("a running claim must not be taken over: %+v calls=%d", out, r.calls)
	}
}

// A genuine model failure keeps the deliberate no-retry.
func TestInterruptedRetryNotForRealFailure(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, runUUID, 0.9, report)
	if _, err := newGen(db, &fakeRunner{err: errors.New("boom")}).Generate(context.Background(), phaseID, runUUID); err == nil {
		t.Fatal("runner error must fail the generation")
	}
	r := &fakeRunner{out: lessonJSON(phaseID)}
	g := newGen(db, r)
	g.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	if n, err := g.Recover(); err != nil || n != 0 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	out, err := g.Generate(context.Background(), phaseID, runUUID)
	if err != nil || out.Skipped == "" || r.calls != 0 {
		t.Fatalf("a real failure must not be retried: %+v %v calls=%d", out, err, r.calls)
	}
	if state, msg := generationRow(t, g, runUUID); state != "failed" || msg == errInterrupted {
		t.Fatalf("real failure row = %s %q", state, msg)
	}
}
