package routines

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// migratedTestDB opens a fully-migrated temp SQLite DB via store.Open (which
// runs Migrate) — the canonical helper the provision/advisor tests use.
func migratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "routines.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedProject inserts a project row so project-scoped routines resolve a path.
func seedProject(t *testing.T, db *sql.DB, id int64, path string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(?,?,?,?)`,
		id, path, "p"+itoa(id), "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// stubRunner records ai-prompt calls and returns a canned result or error.
type stubRunner struct {
	mu      sync.Mutex
	calls   []stubCall
	out     string
	err     error
	blockCh chan struct{} // when non-nil, Run blocks on it (to hold a slot)
}

type stubCall struct {
	cwd, prompt, model string
}

func (s *stubRunner) Run(ctx context.Context, cwd, prompt, model string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, stubCall{cwd, prompt, model})
	block := s.blockCh
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", &timeoutError{}
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "", &timeoutError{}
	}
	return s.out, s.err
}

func (s *stubRunner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// stubTasks records create-task calls and returns a canned card id or error.
type stubTasks struct {
	mu    sync.Mutex
	calls []stubTaskCall
	id    string
	err   error
}

type stubTaskCall struct {
	projectID          int64
	title, prompt, col string
}

func (s *stubTasks) CreateTask(projectID int64, title, prompt, column string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubTaskCall{projectID, title, prompt, column})
	if s.err != nil {
		return "", s.err
	}
	id := s.id
	if id == "" {
		id = "T-stub01"
	}
	return id, nil
}

func (s *stubTasks) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// newTestSvc builds a Service on a migrated DB with a fixed clock and a
// synchronous Go seam (runs spawned closures inline so tests are deterministic —
// no goroutines, no sleeping on real cron).
func newTestSvc(t *testing.T, r Runner, tasks TaskCreator) (*Service, *fakeClock) {
	t.Helper()
	db := migratedTestDB(t)
	clk := &fakeClock{t: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)}
	s := NewService(db, r, tasks, true)
	s.now = clk.now
	s.Go = func(fn func()) { fn() } // synchronous
	return s, clk
}

// fakeClock is a manually-advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// mkSteps is a terse constructor for a validated steps slice in tests.
func mkSteps(steps ...Step) []Step { return steps }

// mustCreate creates a routine or fails the test.
func mustCreate(t *testing.T, s *Service, p CreateParams) Routine {
	t.Helper()
	if p.CatchUp == "" {
		p.CatchUp = "skip"
	}
	if p.TimeoutSec == 0 {
		p.TimeoutSec = 900
	}
	r, err := s.Create(p)
	if err != nil {
		t.Fatalf("create routine: %v", err)
	}
	return r
}
