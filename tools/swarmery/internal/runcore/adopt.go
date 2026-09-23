package runcore

// Adoption — a run that outlived the daemon.
//
// Every engine spawns in its own process group (see spawner.go / procgroup), so
// launchd stopping the daemon no longer takes the executor down with it: it keeps
// editing its worktree, keeps committing, keeps ticking checkboxes. That breaks the
// original premise of every startup heal ("we just started, so nothing can be
// running"): stamping such a row 'failed / daemon restart' makes the dashboard
// claim a process is dead while it is visibly still producing work — and, worse,
// frees the single-flight slot, so a Retry spawns a SECOND executor into the same
// worktree.
//
// So startup PROBES each 'running' row instead of assuming. A row whose process is
// still there is re-adopted: the state stays 'running', the slot is held, Stop
// keeps working (it kills the orphan's process group), and a watcher performs the
// terminal write once the pid finally goes away. Only rows with no live process are
// healed.
//
// What adoption cannot recover is the exit status: the orphan is not our child, so
// there is no wait() to read. Each engine decides what to write instead — which is
// why the terminal action is the engine's, not this file's (dispatch writes nothing
// at all: it already owns an evidence-based reclaim path through procwatch +
// HealDeadProcess, and a second reclaim policy would be one more thing to keep in
// step).

import (
	"database/sql"
	"log"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procfind"
)

// AdoptPollInterval is how often an adopted run's pid is probed. Coarse on
// purpose: runs are minutes-to-hours long, and the only cost of noticing an exit a
// few seconds late is a few seconds of a stale chip.
const AdoptPollInterval = 5 * time.Second

// AdoptedExitNote is what an engine stamps when an adopted run's process
// disappears. The exit status is genuinely unknown — the orphan is not our child,
// so there is no wait() to read — and the note must survive so nobody later reads
// a clean exit into a run we never actually reaped.
//
// It is a NOTE, not a verdict: the state beside it is decided from the same
// evidence the normal exit path uses (ticked criteria + the transcript's ending),
// because "the daemon restarted" is not a reason for the phase's thesis to stop
// holding.
const AdoptedExitNote = "adopted after a daemon restart — exit status unknown"

// AdoptedNotContinuedNote explains the one thing an adopted run CANNOT get: a
// continuation. The continuation counter is in-memory and died with the previous
// daemon, so resuming from here could push a run past MaxContinuations — the cap
// is a money bound and must hold unconditionally. A survivor that would have been
// nudged therefore settles `partial` and says so.
const AdoptedNotContinuedNote = "adopted after daemon restart; not continued"

// Candidate is one row left 'running' by a previous daemon.
type Candidate struct {
	ID   int64
	UUID string // the run's session uuid — what a process can be matched by
}

// Tracked is what an engine tells runcore so the adoption LOOP can be shared while
// each engine keeps its own policy.
//
// The three methods are the three things that genuinely differ: which table holds
// 'running' rows, what a Stop must do to the orphan, and what to write when it
// finally exits. Everything else — the empty-uuid skip, the process match, holding
// the slot, the poll, the log lines, the did-something-else-already-close-this-out
// check — is identical in all three engines and lives here.
type Tracked interface {
	// Engine is the slot namespace and the log label ("dispatch", "phaserun", …).
	Engine() string

	// ScanRunning lists the engine's 'running' rows.
	ScanRunning() ([]Candidate, error)

	// Adopt prepares ONE survivor, having matched it to pid, and returns the hooks
	// runcore will call at the right moments.
	//
	// ok=false skips the candidate, leaving it to the heal sweep. That is the right
	// trade for a row we cannot load: an adopted run we could never stamp on exit
	// would be stuck 'running' for ever, which is worse than a wrong 'failed' the
	// operator can retry.
	Adopt(c Candidate, pid int) (AdoptHooks, bool)
}

// AdoptHooks are one adopted run's engine-owned actions. They are closures rather
// than further interface methods so an engine that had to load a row to adopt it
// (phaserun needs the doc path and the workspace task id) carries that data forward
// instead of re-reading it — and so an engine that needs none of them (dispatch
// writes no terminal state: procwatch + HealDeadProcess already own its reclaim)
// simply leaves them nil.
type AdoptHooks struct {
	// Kill is what a Stop does to a run this process did not spawn. It is WRAPPED,
	// not called, by runcore: the "was it cancelled?" bookkeeping every engine needs
	// is done once, here.
	Kill func()
	// Adopted fires once the slot is held — publish the live-run edge here, not
	// before, so a duplicate that gets refused never announces itself.
	Adopted func()
	// Ended performs the terminal write once the pid is gone AND the slot was still
	// held.
	Ended func(cancelled bool)
}

// Adopter is the shared adoption loop with its seams. Slots is where the adopted
// run's claim is held; the rest default to production behaviour when nil/zero.
type Adopter struct {
	Slots   *Slots
	Tracked Tracked

	// FindRun locates a live run process by its session uuid. nil ⇒ a ps scan.
	FindRun func(sessionUUID string) (int, bool)
	// ProcAlive reports whether a pid still exists. nil ⇒ a signal-0 probe.
	ProcAlive func(pid int) bool
	// Poll overrides AdoptPollInterval when > 0 (tests shrink it).
	Poll time.Duration
	// Go is the async-spawn seam (nil ⇒ real `go`), as on every Service.
	Go func(func())
}

// AdoptSurvivors probes every 'running' row and adopts the ones whose process is
// still alive, returning their ids so the caller's heal sweep can exclude them.
func (a Adopter) AdoptSurvivors() ([]int64, error) {
	candidates, err := a.Tracked.ScanRunning()
	if err != nil {
		return nil, err
	}
	var adopted []int64
	for _, c := range candidates {
		if strings.TrimSpace(c.UUID) == "" {
			continue // pre-uuid row, or one that never got that far: nothing to match against
		}
		pid, ok := a.findRun(c.UUID)
		if !ok {
			continue
		}
		if a.adopt(c, pid) {
			adopted = append(adopted, c.ID)
		}
	}
	return adopted, nil
}

// adopt re-attaches this process to a run it did not spawn: it holds the slot (so
// a Retry cannot start a rival executor in the same worktree), wires Stop to the
// engine's kill hook, and watches the pid until it exits.
//
// The worktree is deliberately NOT removed when an adopted run ends: this process
// never acquired it, so it holds no lock over it, and the deterministic path is
// warm-reused by the next Start anyway.
func (a Adopter) adopt(c Candidate, pid int) bool {
	engine := a.Tracked.Engine()
	hooks, ok := a.Tracked.Adopt(c, pid)
	if !ok {
		return false
	}

	// A Stop the operator pressed is a cancellation, not an unknown exit — that
	// distinction is the one piece of the outcome adoption CAN still know, and every
	// engine needed the same flag to record it.
	cancelled := &atomic.Bool{}
	// Slots.Adopt, not TryAcquire: the process is ALREADY running, so a full pool
	// must not stop us tracking it. Refusing would leave the slot free for a rival
	// Start in the same worktree — the exact failure this file exists to prevent.
	key := SlotKey(engine, c.ID)
	_, tracked := a.Slots.Adopt(key, c.UUID, func() {
		cancelled.Store(true)
		if hooks.Kill != nil {
			hooks.Kill()
		}
	})
	if !tracked {
		return false // already tracked — never adopt the same run twice
	}

	log.Printf("%s: adopted running id=%d uuid=%s pid=%d (survived a daemon restart)", engine, c.ID, c.UUID, pid)
	if hooks.Adopted != nil {
		hooks.Adopted()
	}
	Go(engine, a.Go, func() {
		for a.procAlive(pid) {
			time.Sleep(a.pollInterval())
		}
		// Release by KEY, and read its answer: what matters here is whether the run
		// was STILL tracked when the pid vanished. Something else closing it out first
		// (a Stop that already stamped, a rescan) means the terminal write below would
		// stamp over an outcome that is already recorded.
		if !a.Slots.Release(key) {
			return
		}
		log.Printf("%s: adopted run id=%d pid=%d ended", engine, c.ID, pid)
		if hooks.Ended != nil {
			hooks.Ended(cancelled.Load())
		}
	})
	return true
}

func (a Adopter) findRun(sessionUUID string) (int, bool) {
	if a.FindRun != nil {
		return a.FindRun(sessionUUID)
	}
	return procfind.BySessionUUID(sessionUUID)
}

func (a Adopter) procAlive(pid int) bool {
	if a.ProcAlive != nil {
		return a.ProcAlive(pid)
	}
	return syscall.Kill(pid, 0) == nil
}

func (a Adopter) pollInterval() time.Duration {
	if a.Poll > 0 {
		return a.Poll
	}
	return AdoptPollInterval
}

// ScanCandidates runs a two-column (id, uuid) query into Candidates. The three
// engines' scans differed only in the SELECT, and each had its own copy of this
// rows/Close/Err plumbing — including the easy-to-get-wrong part, closing the rows
// before returning a scan error.
func ScanCandidates(db *sql.DB, query string, args ...any) ([]Candidate, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.UUID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HealExcluding runs a heal UPDATE that must skip the ids just adopted.
//
// `NOT IN ()` is invalid SQL and `x NOT IN (NULL)` is never true, so the exclusion
// clause may only exist when there is something to exclude — a rule three engines
// each re-derived, in three hand-built placeholder loops. idColumn is the column
// holding the engine's row id in that UPDATE (`id`, `workspace_task_id`, …).
//
// args are the UPDATE's own placeholders, in order, ahead of the exclusion list.
// Returns the number of rows healed.
func HealExcluding(db *sql.DB, update, idColumn string, adopted []int64, args ...any) (int64, error) {
	q := update
	if len(adopted) > 0 {
		q += ` AND ` + idColumn + ` NOT IN (?` + strings.Repeat(",?", len(adopted)-1) + `)`
		for _, id := range adopted {
			args = append(args, id)
		}
	}
	res, err := db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
