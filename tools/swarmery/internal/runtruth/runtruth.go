// Package runtruth turns real dispatched/verification runs into stored
// account-runnable verdicts. Dispatch and verify already start `claude` under
// the account's config dir, so a run that dies demanding a login is a free
// authoritative probe — this adapter is the one place that reads such an exit
// as evidence and persists it with source='run' (the D5 cadence decision:
// events plus run-truth, no background prober).
//
// The runners stay decoupled from the store: they expose an optional
// AccountVerdict hook, and cmd/swarmery wires it to Recorder.Record. This
// package is deliberately outside cmd/swarmery so the write rules are covered
// by the coverage gate.
package runtruth

import (
	"database/sql"
	"log"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// debounceWindow caps run-sourced verdict writes to one per account per
// minute, so a burst of failing dispatches does not hammer the store.
const debounceWindow = time.Minute

// Recorder persists run-sourced verdicts under the write rules below. Safe for
// concurrent use — dispatch and verify call the hook from their own goroutines.
type Recorder struct {
	db *sql.DB

	// now is the clock, a seam so the debounce is testable without sleeping.
	now func() time.Time

	mu        sync.Mutex
	lastWrite map[string]time.Time // account key → last successful verdict write
	lastHit   map[string]time.Time // account key → last limit-hit row written
}

// NewRecorder returns a Recorder writing through db.
func NewRecorder(db *sql.DB) *Recorder {
	return &Recorder{db: db, now: time.Now, lastWrite: map[string]time.Time{}, lastHit: map[string]time.Time{}}
}

// Record is the AccountVerdict hook body: it reads one finished run's
// classification as evidence about the account and writes the verdict store
// accordingly. account "" is the runners' spelling of the default account and
// is normalised to its registry key.
//
// The write rules — this is where correctness lives:
//
//   - Only a NEGATIVE verdict (no-login) is written unconditionally. A
//     successful run is weak evidence of readiness for the whole account (it
//     may have used a cached session), so ready overwrites nothing — EXCEPT a
//     stored no-login, which it clears back to ready: the operator logged in
//     outside the dashboard, and healing that false alarm is strictly better
//     than leaving it on screen.
//   - limited is NOT a login verdict and never reaches account_runnable: a
//     limited account is logged in, and storing 'limited' there would render a
//     working account as "unknown" the moment it hits a limit. It appends one
//     account_limit_hits row (source 'run', empty record_uuid) instead, under
//     the same debounceWindow — tracked in its own map, so a limit hit never
//     suppresses a following login verdict. The hook carries no session uuid,
//     so the row's session_uuid is empty; the transcript detector (internal/ingest)
//     records the same hit with its session and record uuid.
//   - unknown is never written. An ordinary task failure is not evidence about
//     the account, and mapping it to unknown would erase a good verdict.
//   - At most one write per account per debounceWindow.
//   - Nothing from the run's output reaches this function; only the account
//     key and the classified status are ever logged.
func (rec *Recorder) Record(account string, r claudeprobe.Result) {
	if account == "" {
		account = ingest.DefaultAccount
	}
	if r.Status == claudeprobe.StatusLimited {
		rec.recordLimit(account)
		return
	}
	if r.Status == claudeprobe.StatusUnknown {
		return
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	now := rec.now()
	if last, ok := rec.lastWrite[account]; ok && now.Sub(last) < debounceWindow {
		return
	}

	if r.Status == claudeprobe.StatusReady {
		// Ready writes only as recovery from a stored no-login (see above).
		stored, ok, err := store.GetAccountRunnable(rec.db, account)
		if err != nil {
			log.Printf("warning: runtruth: read verdict account=%s: %v", account, err)
			return
		}
		if !ok || stored.Status != string(claudeprobe.StatusNoLogin) {
			return
		}
	}

	if err := store.PutAccountRunnable(rec.db, account, string(r.Status), r.Reason, "run", now); err != nil {
		log.Printf("warning: runtruth: write verdict account=%s status=%s: %v", account, r.Status, err)
		return
	}
	rec.lastWrite[account] = now
}

// recordLimit appends one run-sourced limit hit for account, at most once per
// debounceWindow. It never touches account_runnable.
func (rec *Recorder) recordLimit(account string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	now := rec.now()
	if last, ok := rec.lastHit[account]; ok && now.Sub(last) < debounceWindow {
		return
	}
	if _, err := store.InsertAccountLimitHit(rec.db, store.LimitHit{
		Account:    account,
		ObservedAt: now.UTC().Format(time.RFC3339),
		Source:     "run",
	}); err != nil {
		log.Printf("warning: runtruth: write limit hit account=%s: %v", account, err)
		return
	}
	rec.lastHit[account] = now
}
