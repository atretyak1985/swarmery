package ingest

import (
	"testing"
	"time"
)

// TestParkPendingTerminalReclaimsStaleEntries proves the eviction rule
// documented at ParkPendingTerminal: a uuid that is parked but never reaches
// popPendingTerminal (e.g. ErrNoSessionEvidence, or a project ingest that
// never tails) does not camp in pendingTerminal forever — a later
// ParkPendingTerminal call, once the entry is older than pendingTerminalTTL,
// sweeps it away.
func TestParkPendingTerminalReclaimsStaleEntries(t *testing.T) {
	restore := nowFn
	defer func() { nowFn = restore }()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return base }

	ParkPendingTerminal("stale-uuid", SessionTerminal{Program: "iTerm.app"})

	// Advance well past pendingTerminalTTL and park an unrelated uuid — that
	// call's sweep must reclaim "stale-uuid" as a side effect.
	nowFn = func() time.Time { return base.Add(pendingTerminalTTL + time.Minute) }
	ParkPendingTerminal("other-uuid", SessionTerminal{Program: "Terminal.app"})

	if _, ok := popPendingTerminal("stale-uuid"); ok {
		t.Error("stale-uuid: parked entry older than pendingTerminalTTL was not reclaimed by the sweep")
	}

	// Clean up the uuid this test parked so it doesn't leak into other tests
	// sharing the package-level pendingTerminal map.
	popPendingTerminal("other-uuid")
}

// TestParkPendingTerminalKeepsFreshEntries proves the sweep does not evict an
// entry that has not yet crossed pendingTerminalTTL — only stale ones.
func TestParkPendingTerminalKeepsFreshEntries(t *testing.T) {
	restore := nowFn
	defer func() { nowFn = restore }()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return base }

	ParkPendingTerminal("fresh-uuid", SessionTerminal{Program: "iTerm.app"})

	// Advance, but stay under pendingTerminalTTL, and trigger a sweep via
	// another Park call — "fresh-uuid" must still be there.
	nowFn = func() time.Time { return base.Add(pendingTerminalTTL - time.Minute) }
	ParkPendingTerminal("other-fresh-uuid", SessionTerminal{Program: "Terminal.app"})

	term, ok := popPendingTerminal("fresh-uuid")
	if !ok {
		t.Fatal("fresh-uuid: entry younger than pendingTerminalTTL was incorrectly reclaimed by the sweep")
	}
	if term.Program != "iTerm.app" {
		t.Errorf("fresh-uuid: Program = %q, want %q", term.Program, "iTerm.app")
	}

	popPendingTerminal("other-fresh-uuid")
}
