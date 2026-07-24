package routines

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Service owns the routines subsystem: durable store access, the 60s scheduler,
// and the step executor. Every process/side-effect boundary is an interface so
// tests run against stubs; the clock and async seams make scheduling
// deterministic without sleeping on real cron.
type Service struct {
	DB      *sql.DB
	Runner  Runner      // ai-prompt boundary
	Tasks   TaskCreator // create-task boundary (nil ⇒ create-task steps error)
	Enabled bool        // kill-switch (SWARMERY_ROUTINES); false ⇒ ticker never admits

	now func() time.Time // clock seam
	Go  func(func())     // async-spawn seam (nil ⇒ real `go`), mirrors dispatch.Go

	sem chan struct{} // global concurrency cap (MaxConcurrent)

	scheduling atomic.Bool // re-entrance guard: overlapping tick+trigger passes coalesce

	mu     sync.Mutex          // guards active
	active map[string]struct{} // routine ids with a live run (per-routine single-flight)
}

// NewService builds a routines service. The caller wires DB, Runner
// (ClaudeRunner), Tasks (an api-layer adapter), and the kill-switch; now/Go
// default to production impls.
func NewService(db *sql.DB, r Runner, tasks TaskCreator, enabled bool) *Service {
	return &Service{
		DB: db, Runner: r, Tasks: tasks, Enabled: enabled,
		now:    time.Now,
		sem:    make(chan struct{}, MaxConcurrent),
		active: make(map[string]struct{}),
	}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) spawn(fn func()) {
	wrapped := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: routines: goroutine panic recovered: %v", r)
			}
		}()
		fn()
	}
	if s.Go != nil {
		s.Go(wrapped)
		return
	}
	go wrapped()
}

// ── active-run tracking (per-routine single-flight) ──

func (s *Service) tryAcquire(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, live := s.active[id]; live {
		return false
	}
	s.active[id] = struct{}{}
	return true
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

func (s *Service) activeCount() int {
	s.mu.Lock()
	n := len(s.active)
	s.mu.Unlock()
	return n
}

// ── scheduler loop ──

// StartScheduler runs an initial pass then ticks every TickInterval until ctx is
// done. The daemon runs it in a goroutine. Mirrors dispatch.StartScheduler.
func (s *Service) StartScheduler(ctx context.Context) {
	s.tick()
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick runs one admission pass: find due routines and fire each (respecting the
// global cap + per-routine single-flight). Re-entrance-guarded so an overlapping
// pass (a slow tick) returns immediately.
func (s *Service) tick() {
	if !s.Enabled {
		return
	}
	if !s.scheduling.CompareAndSwap(false, true) {
		return
	}
	defer s.scheduling.Store(false)

	now := s.clock()
	due, err := s.due(now)
	if err != nil {
		log.Printf("error: routines: load due: %v", err)
		return
	}
	for _, r := range due {
		// Global cap: stop scanning once every lane is busy.
		if s.activeCount() >= MaxConcurrent {
			return
		}
		s.fireCron(r, now)
	}
}

// fireCron handles one due routine under the cron trigger, applying the catch-up
// policy, then spawns the run. Single-flight: if a run for this routine is
// already live, skip this pass (the next tick retries).
func (s *Service) fireCron(r Routine, now time.Time) {
	// catch_up='skip' with >1 missed slot: drop the missed slots WITHOUT running,
	// just advance next_run_at past now. "Missed" = the slot after next_run_at is
	// still <= now (i.e. at least two slots are due).
	if r.CatchUp == "skip" && r.NextRunAt.Valid {
		if slot, ok := NextRun(r.CronExpr, parseTS(r.NextRunAt.String)); ok && !slot.After(now) {
			// Two or more slots are overdue → skip straight to the next future slot.
			if err := s.advanceOnly(r.ID, r.CronExpr, now); err != nil {
				log.Printf("error: routines: advance skip %s: %v", r.ID, err)
			}
			return
		}
	}

	if !s.tryAcquire(r.ID) {
		return // a run is already live for this routine
	}
	// Advance the schedule NOW (before the run) so a long run does not re-fire on
	// the next tick, and so 'run_one' fires exactly once for a backlog.
	if err := s.advanceSchedule(r.ID, r.CronExpr, now); err != nil {
		log.Printf("error: routines: advance %s: %v", r.ID, err)
	}
	s.spawn(func() {
		defer s.release(r.ID)
		s.execRun(context.Background(), r, "cron")
	})
}

// Trigger fires a routine on demand (manual or webhook). It bypasses the cron
// due-query and catch-up policy but honors the global cap + single-flight.
// Returns false when the routine is already running or the cap is full (the
// caller maps that to a 202-with-note or 429; here we simply report started).
func (s *Service) Trigger(id, trigger string) (started bool, err error) {
	r, err := s.Get(id)
	if err != nil {
		return false, err
	}
	if s.activeCount() >= MaxConcurrent {
		return false, nil
	}
	if !s.tryAcquire(id) {
		return false, nil
	}
	s.spawn(func() {
		defer s.release(id)
		s.execRun(context.Background(), r, trigger)
	})
	return true, nil
}

// parseTS parses one of our RFC3339 timestamps; zero time on error (a malformed
// stored timestamp then reads as "epoch", which is always <= now, so the routine
// simply fires — fail-safe toward running rather than silently stalling).
func parseTS(s string) time.Time {
	t, err := time.Parse(tsFormat, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func decodeSteps(raw string) ([]Step, error) {
	if raw == "" {
		return []Step{}, nil
	}
	var steps []Step
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil, err
	}
	if steps == nil {
		steps = []Step{}
	}
	return steps, nil
}

func logHealed(n int64) {
	log.Printf("swarmery routines: healed %d interrupted run(s) to failed", n)
}
