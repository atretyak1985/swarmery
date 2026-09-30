package ingest

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const (
	orgDisabledText = "Your organization has disabled Claude subscription access for Claude Code."
	apiErrorLine    = "API Error: 529 Overloaded"
)

// failureTranscript writes a transcript under <tmp>/.claude-work/projects/ whose
// one assistant record carries text at ts. flagged controls isApiErrorMessage.
// Returns (file, projectsRoot); the account it resolves to is "work".
func failureTranscript(t *testing.T, session, text, ts string, flagged bool) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".claude-work", "projects")
	dir := filepath.Join(root, "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	flag := ""
	if flagged {
		flag = `"isApiErrorMessage":true,`
	}
	lines := []string{
		`{"type":"user","uuid":"u-` + session + `","timestamp":"` + ts + `","sessionId":"` + session + `","cwd":"/p","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","uuid":"a-` + session + `",` + flag + `"timestamp":"` + ts + `","sessionId":"` + session + `","cwd":"/p","message":{"id":"msg-` + session + `","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":` + string(quoted) + `}]}}`,
	}
	f := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, root
}

// breakerAt pins tripBreaker's clock for one test.
func breakerAt(t *testing.T, now time.Time) {
	t.Helper()
	prev := breakerClock
	breakerClock = func() time.Time { return now }
	t.Cleanup(func() { breakerClock = prev })
}

func workBreaker(t *testing.T, db *sql.DB) (store.AccountBreaker, bool) {
	t.Helper()
	b, ok, err := store.GetAccountBreaker(db, "work")
	if err != nil {
		t.Fatalf("get breaker: %v", err)
	}
	return b, ok
}

func workAlerts(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM config_lint_findings WHERE target = 'account:work' AND rule = ? AND resolved_at IS NULL`,
		store.AccountBreakerRule).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var breakerNoon = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// TestFreshTranscriptTripsBreaker: a flagged API-error record whose text is an
// auth failure and whose timestamp is minutes old opens the account's breaker —
// inside the tail's own transaction, alert included — and stores no message
// text. A second tail of the same file changes nothing.
func TestFreshTranscriptTripsBreaker(t *testing.T) {
	db := testDB(t)
	breakerAt(t, breakerNoon)
	f, root := failureTranscript(t, "sess-org", orgDisabledText, "2026-09-30T11:55:00.000Z", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	b, ok := workBreaker(t, db)
	if !ok || !b.IsOpen() {
		t.Fatalf("breaker = %+v ok=%v, want open", b, ok)
	}
	want := store.AccountBreaker{
		Account: "work", State: store.BreakerOpen, Kind: store.BreakerKindAuth,
		Reason: claudeprobe.ReasonAccessRefused, OpenedAt: "2026-09-30T12:00:00Z",
		Source: store.BreakerSourceTranscript,
	}
	if b != want {
		t.Errorf("breaker = %+v, want %+v", b, want)
	}
	if n := workAlerts(t, db); n != 1 {
		t.Errorf("open alerts = %d, want 1", n)
	}
	var msg string
	if err := db.QueryRow(`SELECT message FROM config_lint_findings WHERE target = 'account:work'`).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if msg != store.AccountBreakerMessage(store.BreakerKindAuth) || strings.Contains(msg, "organization") {
		t.Errorf("finding message = %q, want the fixed sentence and no CLI text", msg)
	}

	// Re-tail from scratch, five minutes on: same opening, same single alert.
	breakerAt(t, breakerNoon.Add(5*time.Minute))
	if _, err := db.Exec(`DELETE FROM file_offsets`); err != nil {
		t.Fatal(err)
	}
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if again, _ := workBreaker(t, db); again != want {
		t.Errorf("re-tail moved the opening: %+v", again)
	}
	if n := workAlerts(t, db); n != 1 {
		t.Errorf("open alerts after a re-tail = %d, want 1", n)
	}
}

// TestFreshQuotaTranscriptTripsBreaker: a fresh usage-limit record opens a quota
// breaker with a reset time, beside the limit-hit row it already recorded.
func TestFreshQuotaTranscriptTripsBreaker(t *testing.T) {
	db := testDB(t)
	breakerAt(t, breakerNoon)
	f, root := failureTranscript(t, "sess-limit-fresh", limitText, "2026-09-30T11:59:30Z", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	b, ok := workBreaker(t, db)
	if !ok || !b.IsOpen() || b.Kind != store.BreakerKindQuota || b.Reason != claudeprobe.ReasonRateLimited ||
		b.ResetsAt != "2026-09-30T13:00:00Z" {
		t.Errorf("breaker = %+v ok=%v, want an open quota breaker resetting at now + 1h", b, ok)
	}
	if n := len(limitRows(t, db)); n != 1 {
		t.Errorf("limit-hit rows = %d, want 1 — the detector still records the hit", n)
	}
}

// TestStaleTranscriptDoesNotTrip: the recency bound. A backfill or a re-tail
// walks old transcripts; a failure record older than ten minutes — or stamped
// further ahead than that — says nothing about the account NOW and must not
// pause it. Neither does an unflagged record, an API error, or a transcript
// read with no account context.
func TestStaleTranscriptDoesNotTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		text    string
		ts      string
		flagged bool
		rooted  bool
	}{
		{"auth record from two weeks ago", orgDisabledText, "2026-09-14T10:00:05Z", true, true},
		{"auth record eleven minutes old", orgDisabledText, "2026-09-30T11:49:00Z", true, true},
		{"quota record eleven minutes old", limitText, "2026-09-30T11:48:59Z", true, true},
		{"record stamped eleven minutes ahead", orgDisabledText, "2026-09-30T12:11:00Z", true, true},
		{"record with an unusable timestamp", orgDisabledText, "yesterday", true, true},
		{"fresh but unflagged — prose quoting the line", orgDisabledText, "2026-09-30T11:59:00Z", false, true},
		{"fresh API error", apiErrorLine, "2026-09-30T11:59:00Z", true, true},
		{"fresh prose that merely mentions a failure", "The CLI printed: Not logged in · Please run /login", "2026-09-30T11:59:00Z", true, true},
		{"fresh auth record with no account context", orgDisabledText, "2026-09-30T11:59:00Z", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			breakerAt(t, breakerNoon)
			f, root := failureTranscript(t, "sess-stale", tc.text, tc.ts, tc.flagged)
			if !tc.rooted {
				root = ""
			}
			if _, err := fileFrom(db, f, root); err != nil {
				t.Fatal(err)
			}
			if all, err := store.ListAccountBreakers(db, false); err != nil || len(all) != 0 {
				t.Errorf("breaker rows = %+v (%v), want none", all, err)
			}
			if n := workAlerts(t, db); n != 0 {
				t.Errorf("open alerts = %d, want 0", n)
			}
		})
	}

	// The bound is inclusive at exactly ten minutes.
	t.Run("exactly ten minutes old still trips", func(t *testing.T) {
		db := testDB(t)
		breakerAt(t, breakerNoon)
		f, root := failureTranscript(t, "sess-edge", orgDisabledText, "2026-09-30T11:50:00Z", true)
		if _, err := fileFrom(db, f, root); err != nil {
			t.Fatal(err)
		}
		if b, ok := workBreaker(t, db); !ok || !b.IsOpen() {
			t.Errorf("breaker = %+v ok=%v, want open at the edge of the bound", b, ok)
		}
	})
}
