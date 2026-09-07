package ingest

import (
	"sync"
	"time"
)

// SessionTerminal is the terminal identity the hookshim captured at
// SessionStart (docs/hooks-protocol.md), destined for the four term_* columns
// on sessions (migration 0068). Empty fields mean that source variable (or
// the PID-derived tty) was absent.
type SessionTerminal struct {
	Program  string
	FocusURL string
	BundleID string
	TTY      string
}

// isEmpty reports whether none of the four values were ever set.
func (t SessionTerminal) isEmpty() bool {
	return t.Program == "" && t.FocusURL == "" && t.BundleID == "" && t.TTY == ""
}

// pendingTerminal parks a SessionStart hook's terminal identity for a session
// whose row does not exist yet — the hook POST can beat the JSONL tail that
// mints it (internal/api hookSessionStart). Keyed by session_uuid; popped and
// applied the moment upsertProjectAndSession creates that row, so a session
// never loses its terminal to the race. Package-level because the hookshim's
// POST lands in internal/api, which already imports internal/ingest — the
// reverse import would cycle.
var pendingTerminal sync.Map // session_uuid (string) -> parkedTerminal

// nowFn is the clock ParkPendingTerminal and its sweep read — a package-level
// test seam (overridden in tests), since pendingTerminal is itself package
// state rather than a struct field.
var nowFn = time.Now

// pendingTerminalTTL bounds how long a parked terminal identity may wait for
// its session row before the sweep reclaims it. The normal exit is
// popPendingTerminal, called from the session-CREATE branch of
// upsertProjectAndSession — but a uuid that never reaches that branch (e.g.
// ErrNoSessionEvidence, or a project ingest that never tails) would otherwise
// camp in the map for the daemon's entire lifetime.
const pendingTerminalTTL = time.Hour

// parkedTerminal pairs a parked identity with the time it was parked, so the
// sweep can tell a stale entry from one still waiting on its session row.
type parkedTerminal struct {
	term     SessionTerminal
	parkedAt time.Time
}

// ParkPendingTerminal records t for sessionUUID until a matching session row
// is created. A later call for the same uuid overwrites the earlier one —
// only the most recent SessionStart for a given uuid should ever win.
//
// Eviction rule: every call first sweeps entries older than
// pendingTerminalTTL (1h) out of the map. There is no background goroutine —
// ParkPendingTerminal is the only writer, so piggybacking the sweep on it
// keeps the map self-bounding for a uuid that never reaches
// popPendingTerminal, using a lock-free sync.Map.Range.
func ParkPendingTerminal(sessionUUID string, t SessionTerminal) {
	if sessionUUID == "" || t.isEmpty() {
		return
	}
	sweepPendingTerminal()
	pendingTerminal.Store(sessionUUID, parkedTerminal{term: t, parkedAt: nowFn()})
}

// sweepPendingTerminal deletes every parked entry older than
// pendingTerminalTTL. Safe to call concurrently with Store/LoadAndDelete —
// sync.Map.Range tolerates mutation during iteration.
func sweepPendingTerminal() {
	cutoff := nowFn().Add(-pendingTerminalTTL)
	pendingTerminal.Range(func(key, value any) bool {
		if pt, ok := value.(parkedTerminal); ok && pt.parkedAt.Before(cutoff) {
			pendingTerminal.Delete(key)
		}
		return true
	})
}

// popPendingTerminal removes and returns the parked terminal identity for
// sessionUUID, if any. Called once, right after a session row is minted.
func popPendingTerminal(sessionUUID string) (SessionTerminal, bool) {
	v, ok := pendingTerminal.LoadAndDelete(sessionUUID)
	if !ok {
		return SessionTerminal{}, false
	}
	return v.(parkedTerminal).term, true
}
