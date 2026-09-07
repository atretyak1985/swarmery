package ingest

import "sync"

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
var pendingTerminal sync.Map // session_uuid (string) -> SessionTerminal

// ParkPendingTerminal records t for sessionUUID until a matching session row
// is created. A later call for the same uuid overwrites the earlier one —
// only the most recent SessionStart for a given uuid should ever win.
func ParkPendingTerminal(sessionUUID string, t SessionTerminal) {
	if sessionUUID == "" || t.isEmpty() {
		return
	}
	pendingTerminal.Store(sessionUUID, t)
}

// popPendingTerminal removes and returns the parked terminal identity for
// sessionUUID, if any. Called once, right after a session row is minted.
func popPendingTerminal(sessionUUID string) (SessionTerminal, bool) {
	v, ok := pendingTerminal.LoadAndDelete(sessionUUID)
	if !ok {
		return SessionTerminal{}, false
	}
	return v.(SessionTerminal), true
}
