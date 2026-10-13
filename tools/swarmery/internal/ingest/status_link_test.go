package ingest

import (
	"testing"
	"time"
)

// TestRecomputeStatusesLinkOverride: a fresh link_seen_at (younger than
// 2×AwaitAfter) takes the row off the awaiting_reply timer and lets the mod's
// link_status decide; a stale or absent one leaves the timer path unchanged.
//
// The base fixture is the detector's positive case (cli, running, end_turn,
// quiet 4m) — on the timer alone it moves to awaiting_reply.
func TestRecomputeStatusesLinkOverride(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	aw := DefaultThresholds().AwaitAfter // 3m → fresh window 6m

	base := awaitFixture{status: "idle", entrypoint: "cli", procState: "running",
		quiet: 4 * time.Minute, stopReason: "end_turn"}
	for _, tc := range []struct {
		name       string
		f          awaitFixture
		linkStatus any           // nil = NULL
		seenAgo    time.Duration // < 0 = link_seen_at NULL
		want       string
	}{
		{"no link → timer awaits", base, nil, -1, StatusAwaitingReply},
		{"fresh working keeps it active, not awaiting", base, "working", time.Minute, "active"},
		{"fresh working just inside 2×AwaitAfter", base, "working", 2*aw - time.Second, "active"},
		{"stale working (2×AwaitAfter) falls back to the timer", base, "working", 2 * aw, StatusAwaitingReply},
		{"stale working (10m) falls back to the timer", base, "working", 10 * time.Minute, StatusAwaitingReply},
		{"fresh waiting awaits before the timer would", func() awaitFixture {
			f := base
			f.status, f.quiet = "active", 30*time.Second
			return f
		}(), "waiting", 10 * time.Second, StatusAwaitingReply},
		{"fresh waiting on a headless run keeps the timer status", func() awaitFixture {
			f := base
			f.entrypoint = "sdk-cli"
			return f
		}(), "waiting", time.Minute, "idle"},
		{"fresh done completes", base, "done", time.Minute, "completed"},
		{"fresh link without a status skips the detector only", base, nil, time.Minute, "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			id := seedAwaitSession(t, db, "s-link", tc.f, now)
			var seen any
			if tc.seenAgo >= 0 {
				seen = now.Add(-tc.seenAgo).UTC().Format("2006-01-02T15:04:05.000Z")
			}
			if _, err := db.Exec(`UPDATE sessions SET link_status = ?, link_seen_at = ? WHERE id = ?`,
				tc.linkStatus, seen, id); err != nil {
				t.Fatal(err)
			}
			changes, err := RecomputeStatuses(db, Thresholds{}, now)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
			moved := tc.f.status != tc.want
			if reported := len(changes) == 1 && changes[0].ID == id && changes[0].Status == tc.want; reported != moved {
				t.Errorf("changes = %+v, want a reported transition: %v", changes, moved)
			}
		})
	}
}

func TestLinkFreshUnparseable(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	id := seedAwaitSession(t, db, "s-garbage", awaitFixture{status: "idle", entrypoint: "cli",
		procState: "running", quiet: 4 * time.Minute, stopReason: "end_turn"}, now)
	if _, err := db.Exec(`UPDATE sessions SET link_status = 'working', link_seen_at = 'not-a-time' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := RecomputeStatuses(db, Thresholds{}, now); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != StatusAwaitingReply {
		t.Errorf("unparseable link_seen_at: status = %q, want the timer's %q", got, StatusAwaitingReply)
	}
}
