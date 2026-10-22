package cachecold

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "cachecold.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, name, first_seen, last_activity)
		VALUES (1, '/work/p', '-work-p', 'p', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

type sess struct {
	uuid, status, account, procState, entrypoint string
}

func addSession(t *testing.T, db *sql.DB, s sess) int64 {
	t.Helper()
	if s.status == "" {
		s.status = "idle"
	}
	var proc any
	if s.procState != "" {
		proc = s.procState
	}
	r, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at, status, account, proc_state, entrypoint)
		VALUES (1, ?, '2026-10-22T08:00:00Z', ?, ?, ?, ?)`, s.uuid, s.status, s.account, proc, s.entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

// addTurn inserts a main-thread turn ending at ended with the given footprint
// (all of it as cache_read, the warm shape).
func addTurn(t *testing.T, db *sql.DB, sessionID int64, seq int, role string, ended time.Time, footprint int64) {
	t.Helper()
	var tin, tcr, tcw any
	if role == "assistant" {
		tin, tcr, tcw = int64(10), footprint-1_010, int64(1_000)
	}
	ts := ended.UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, ended_at, tokens_in, tokens_cache_read, tokens_cache_write)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sessionID, seq, role, ts, ts, tin, tcr, tcw); err != nil {
		t.Fatal(err)
	}
}

func uuids(cs []Cold) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.SessionUUID)
	}
	return out
}

func TestCandidates(t *testing.T) {
	db := openDB(t)
	now := t0
	cfg := testConfig()
	cfg.KindOf = func(account string) Kind {
		if account == "console" {
			return KindAPI
		}
		return KindSubscription
	}

	// The prompt's three cases.
	sub70 := addSession(t, db, sess{uuid: "sub-70", procState: "running"})
	addTurn(t, db, sub70, 0, "assistant", now.Add(-70*time.Minute), 200_000)
	sub30 := addSession(t, db, sess{uuid: "sub-30"})
	addTurn(t, db, sub30, 0, "assistant", now.Add(-30*time.Minute), 200_000)
	api6 := addSession(t, db, sess{uuid: "api-6", account: "console"})
	addTurn(t, db, api6, 0, "assistant", now.Add(-6*time.Minute), 180_000)

	// Never candidates.
	ended := addSession(t, db, sess{uuid: "ended", status: "completed"})
	addTurn(t, db, ended, 0, "assistant", now.Add(-3*time.Hour), 300_000)
	dead := addSession(t, db, sess{uuid: "dead", procState: "dead"})
	addTurn(t, db, dead, 0, "assistant", now.Add(-3*time.Hour), 300_000)
	headless := addSession(t, db, sess{uuid: "headless", entrypoint: "sdk-cli"})
	addTurn(t, db, headless, 0, "assistant", now.Add(-3*time.Hour), 300_000)
	thin := addSession(t, db, sess{uuid: "thin"})
	addTurn(t, db, thin, 0, "assistant", now.Add(-3*time.Hour), 100_000)
	inflight := addSession(t, db, sess{uuid: "inflight"})
	addTurn(t, db, inflight, 0, "assistant", now.Add(-3*time.Hour), 300_000)
	addTurn(t, db, inflight, 1, "user", now.Add(-10*time.Second), 0)

	got, err := Candidates(db, now, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sub-70", "api-6"} // footprint DESC
	if g := uuids(got); len(g) != len(want) || g[0] != want[0] || g[1] != want[1] {
		t.Fatalf("Candidates = %v, want %v", g, want)
	}
	c := got[0]
	if c.SessionID != sub70 || c.Kind != KindSubscription || c.IdleMinutes != 70 || c.Footprint != 200_000 {
		t.Errorf("sub-70 = %+v", c)
	}
	if !c.Since.Equal(now.Add(-10 * time.Minute)) {
		t.Errorf("sub-70 since = %s, want last end + 60 min", c.Since)
	}
	if got[1].Kind != KindAPI || got[1].IdleMinutes != 6 {
		t.Errorf("api-6 = %+v", got[1])
	}
}

func TestCandidatesIgnoresSubagentTurns(t *testing.T) {
	db := openDB(t)
	id := addSession(t, db, sess{uuid: "s"})
	addTurn(t, db, id, 0, "assistant", t0.Add(-2*time.Hour), 200_000)
	// A subagent turn is newer, but it runs on its own cache.
	ts := t0.Add(-time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, ended_at, tokens_in, agent_name)
		VALUES (?, 1, 'assistant', ?, ?, 50000, 'Explore')`, id, ts, ts); err != nil {
		t.Fatal(err)
	}
	got, err := Candidates(db, t0, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IdleMinutes != 120 {
		t.Fatalf("Candidates = %+v, want the main thread idle 120 min", got)
	}
}

func TestCandidatesSkipsSyntheticStub(t *testing.T) {
	db := openDB(t)
	id := addSession(t, db, sess{uuid: "s"})
	addTurn(t, db, id, 0, "assistant", t0.Add(-70*time.Minute), 200_000)
	// A zero-usage `<synthetic>` stub is newer, but it is no request.
	ts := t0.Add(-time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, model, started_at, ended_at, tokens_in, tokens_cache_read, tokens_cache_write)
		VALUES (?, 1, 'assistant', '<synthetic>', ?, ?, 0, 0, 0)`, id, ts, ts); err != nil {
		t.Fatal(err)
	}
	got, err := Candidates(db, t0, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IdleMinutes != 70 || got[0].Footprint != 200_000 {
		t.Fatalf("Candidates = %+v, want the real turn idle 70 min", got)
	}
}
