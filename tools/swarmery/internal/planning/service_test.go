package planning

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ── test doubles ──

// stubRunner records specs and returns a canned Run. When block is non-nil the
// Start call waits on it (so a test can observe the in-flight state before the
// run completes and releases the slot). runFn overrides the returned Run.
type stubRunner struct {
	mu    sync.Mutex
	specs []RunSpec
	block chan struct{}
	runFn func(spec RunSpec) (*Run, error)
	// startErr forces Start to return an error (process never ran).
	startErr error
}

func (s *stubRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	block := s.block
	fn := s.runFn
	startErr := s.startErr
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done(): // Cancel() aborts the run
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: -1, TimedOut: true}, nil
		}
	}
	if startErr != nil {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: -1}, startErr
	}
	if fn != nil {
		return fn(spec)
	}
	return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

func (s *stubRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.specs)
}

func (s *stubRunner) lastSpec() RunSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) == 0 {
		return RunSpec{}
	}
	return s.specs[len(s.specs)-1]
}

// ── harness ──

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "planning.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

// newInlineService builds a Service whose async spawn runs INLINE (Go seam) and
// whose UUID is deterministic, for synchronous no-block tests.
func newInlineService(t *testing.T, db *sql.DB, r Runner) *Service {
	t.Helper()
	s := NewService(db, r)
	s.Go = func(fn func()) { fn() }
	var n int
	s.UUID = func() string { n++; return "uuid-planning" }
	_ = n
	return s
}

// ── tests ──

func TestStart_HappyPath_InlineRun(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newInlineService(t, db, r)

	var notified int
	s.Notify = func(int64) { notified++ }

	uuid, err := s.Start(1, "add a widget")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if uuid != "uuid-planning" {
		t.Errorf("uuid = %q, want uuid-planning", uuid)
	}
	if r.count() != 1 {
		t.Errorf("runner called %d times, want 1", r.count())
	}
	// Inline Go seam ⇒ the run completed and the slot released before Start returns.
	if s.Snapshot(1).Active {
		t.Error("expected no active run after inline completion")
	}
	// The spawned spec carried the prompt + cwd + uuid.
	spec := r.lastSpec()
	if spec.Cwd != "/repo/p" {
		t.Errorf("spec.Cwd = %q, want /repo/p", spec.Cwd)
	}
	if spec.SessionUUID != "uuid-planning" {
		t.Errorf("spec.SessionUUID = %q", spec.SessionUUID)
	}
	if !contains(spec.Prompt, "add a widget") {
		t.Error("spec.Prompt missing the idea")
	}
	// Notify fires at both edges (start + finish).
	if notified < 2 {
		t.Errorf("Notify fired %d times, want >= 2 (start + finish)", notified)
	}
}

func TestStart_SingleFlight_409(t *testing.T) {
	db := testDB(t)
	// A blocking runner keeps the first run in flight so the second Start sees it.
	r := &stubRunner{block: make(chan struct{})}
	s := NewService(db, r) // real `go` so the first Start's goroutine parks on block
	s.UUID = func() string { return "uuid-1" }

	if _, err := s.Start(1, "first idea"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Wait until the run's goroutine has actually ENTERED the runner (count==1),
	// not merely until Active is set — Start marks active synchronously but spawns
	// the runner call on a goroutine, so a slower scheduler (CI under -race) can
	// leave the runner un-entered while Active is already true, racing the count
	// assertion below.
	waitFor(t, func() bool { return r.count() == 1 })

	_, err := s.Start(1, "second idea")
	if !errors.Is(err, ErrActive) {
		t.Fatalf("second Start err = %v, want ErrActive", err)
	}
	if r.count() != 1 {
		t.Errorf("runner called %d times, want 1 (second rejected before spawn)", r.count())
	}

	// Release the first run; the slot frees.
	close(r.block)
	waitFor(t, func() bool { return !s.Snapshot(1).Active })
}

func TestStart_UnknownProject(t *testing.T) {
	db := testDB(t)
	s := newInlineService(t, db, &stubRunner{})
	if _, err := s.Start(999, "idea"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestStart_PathlessProject(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2,'','q','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s := newInlineService(t, db, &stubRunner{})
	if _, err := s.Start(2, "idea"); !errors.Is(err, ErrNoPath) {
		t.Fatalf("err = %v, want ErrNoPath", err)
	}
}

func TestSnapshot_ActiveResolvesSessionID(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{block: make(chan struct{})}
	s := NewService(db, r)
	s.UUID = func() string { return "uuid-live" }

	if _, err := s.Start(1, "idea"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool { return s.Snapshot(1).Active })

	// Before the sessions row exists, SessionID is nil but the uuid + startedAt are present.
	st := s.Snapshot(1)
	if !st.Active || st.SessionUUID != "uuid-live" || st.StartedAt == nil {
		t.Fatalf("snapshot = %+v, want active with uuid + startedAt", st)
	}
	if st.SessionID != nil {
		t.Errorf("SessionID = %v, want nil before ingest mints the row", *st.SessionID)
	}

	// Simulate ingest minting the sessions row for the planner uuid.
	if _, err := db.Exec(
		`INSERT INTO sessions(id, project_id, session_uuid, status, started_at, source)
		 VALUES(42,1,'uuid-live','active','2026-01-01T00:00:00Z','jsonl')`); err != nil {
		t.Fatal(err)
	}
	st = s.Snapshot(1)
	if st.SessionID == nil || *st.SessionID != 42 {
		t.Fatalf("SessionID = %v, want 42 after row minted", st.SessionID)
	}

	close(r.block)
	waitFor(t, func() bool { return !s.Snapshot(1).Active })
}

func TestSnapshot_Idle(t *testing.T) {
	db := testDB(t)
	s := newInlineService(t, db, &stubRunner{})
	st := s.Snapshot(1)
	if st.Active || st.SessionUUID != "" || st.SessionID != nil || st.StartedAt != nil {
		t.Fatalf("idle snapshot = %+v, want zero", st)
	}
}

func TestCancel(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{block: make(chan struct{})}
	s := NewService(db, r)
	s.UUID = func() string { return "uuid-c" }

	if _, err := s.Start(1, "idea"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool { return s.Snapshot(1).Active })

	if !s.Cancel(1) {
		t.Fatal("Cancel returned false for an active run")
	}
	// Cancelling the ctx unblocks the stub runner's select → run finishes → slot frees.
	waitFor(t, func() bool { return !s.Snapshot(1).Active })

	// Cancelling with nothing in flight returns false.
	if s.Cancel(1) {
		t.Error("Cancel returned true with no active run")
	}
}

func TestRunAndHandle_OutcomeBranchesDoNotPanic(t *testing.T) {
	// Exercise the exit-log branches (start error, timeout, nonzero, clean) — each
	// must release the slot regardless.
	cases := []struct {
		name string
		run  func(spec RunSpec) (*Run, error)
		err  error
	}{
		{"clean", func(s RunSpec) (*Run, error) { return &Run{SessionUUID: s.SessionUUID, ExitCode: 0}, nil }, nil},
		{"nonzero", func(s RunSpec) (*Run, error) {
			return &Run{SessionUUID: s.SessionUUID, ExitCode: 1, Stderr: "boom"}, nil
		}, nil},
		{"timeout", func(s RunSpec) (*Run, error) {
			return &Run{SessionUUID: s.SessionUUID, ExitCode: -1, TimedOut: true}, nil
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := testDB(t)
			r := &stubRunner{runFn: c.run}
			s := newInlineService(t, db, r)
			if _, err := s.Start(1, "idea"); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if s.Snapshot(1).Active {
				t.Error("slot not released after run")
			}
		})
	}

	t.Run("start-error", func(t *testing.T) {
		db := testDB(t)
		r := &stubRunner{startErr: errors.New("fork failed")}
		s := newInlineService(t, db, r)
		if _, err := s.Start(1, "idea"); err != nil {
			t.Fatalf("Start (the spawn itself succeeds; the runner error is handled in the goroutine): %v", err)
		}
		if s.Snapshot(1).Active {
			t.Error("slot not released after start error")
		}
	})
}

// waitFor polls cond up to ~2s (the run goroutines are real here).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
