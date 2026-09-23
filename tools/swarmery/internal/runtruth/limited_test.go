package runtruth

import (
	"database/sql"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

var limited = claudeprobe.Result{Status: claudeprobe.StatusLimited, Reason: claudeprobe.ReasonRateLimited}

func countHits(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM account_limit_hits`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A limited verdict is not a login verdict: the stored ready row is
// byte-identical afterwards, exactly one hit row lands, and a second limited
// inside the debounce window inserts nothing. unknown still writes nothing.
func TestRecordLimitedNeverTouchesVerdict(t *testing.T) {
	rec, db, now := newRecorder(t)
	if err := store.PutAccountRunnable(db, "nabu-org", "ready", "", "probe", time.Unix(1764000000, 0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _ := mustGet(t, db, "nabu-org")

	rec.Record("nabu-org", limited)
	after, ok := mustGet(t, db, "nabu-org")
	if !ok || after != before {
		t.Errorf("verdict changed by a limited run: %+v -> %+v", before, after)
	}
	if n := countHits(t, db); n != 1 {
		t.Fatalf("hit rows = %d, want 1", n)
	}
	var acct, src, recUUID string
	if err := db.QueryRow(`SELECT account, source, record_uuid FROM account_limit_hits`).Scan(&acct, &src, &recUUID); err != nil {
		t.Fatal(err)
	}
	if acct != "nabu-org" || src != "run" || recUUID != "" {
		t.Errorf("hit row = %s/%s/%q, want nabu-org/run/''", acct, src, recUUID)
	}

	*now = now.Add(debounceWindow / 2)
	rec.Record("nabu-org", limited)
	if n := countHits(t, db); n != 1 {
		t.Errorf("second limited inside debounce inserted: %d rows", n)
	}

	// A login verdict right after a limit hit is NOT suppressed by it.
	rec.Record("nabu-org", noLogin)
	if row, _ := mustGet(t, db, "nabu-org"); row.Status != "no-login" {
		t.Errorf("no-login after a limit hit was debounced away: %+v", row)
	}

	*now = now.Add(debounceWindow)
	rec.Record("", limited)
	if n := countHits(t, db); n != 2 {
		t.Errorf("default-account limit after the window: %d rows, want 2", n)
	}

	rec.Record("other", claudeprobe.Result{Status: claudeprobe.StatusUnknown})
	if n := countHits(t, db); n != 2 {
		t.Error("unknown wrote a hit row")
	}
	if _, ok := mustGet(t, db, "other"); ok {
		t.Error("unknown wrote a verdict")
	}
}

// A store failure is logged, never a panic, and does not arm the debounce.
func TestRecordLimitedStoreError(t *testing.T) {
	rec, db, _ := newRecorder(t)
	db.Close()
	rec.Record("x", limited)
	if _, ok := rec.lastHit["x"]; ok {
		t.Error("a failed write armed the debounce")
	}
}
