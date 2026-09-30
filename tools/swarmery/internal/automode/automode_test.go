package automode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// The two refusals recorded on the live DB (2026-09-24 and 2026-09-28), as the
// transcript stores them in a tool call's result.
const (
	variantError = "Error: The server-side auto mode classifier gave no verdict (error), so auto mode cannot " +
		"determine the safety of Bash. This is a transient failure of the check, not a judgment about the action: " +
		"a later response may get a verdict."
	variantCutOff = "Error: The server-side auto mode classifier gave no verdict (the response ended before its " +
		"verdict arrived), so auto mode cannot determine the safety of Bash. This is a transient failure of the " +
		"check, not a judgment about the action: a later response may get a verdict."
)

// t0 is the instant the tests' bursts happen at.
var t0 = time.Date(2026, 9, 28, 6, 38, 0, 0, time.UTC)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	return db
}

// session registers a session row once; events reference it.
func session(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO sessions (id, project_id, session_uuid, status, started_at, source)
		 VALUES (?, 1, ?, 'active', '2026-09-28T00:00:00Z', 'jsonl')`,
		id, fmt.Sprintf("uuid-%d", id)); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

// toolCall inserts one tool_call event the way ingest stores it: ts in UTC with
// milliseconds, the tool's result inside the JSON payload.
func toolCall(t *testing.T, db *sql.DB, sessionID int64, at time.Time, status, result string) {
	t.Helper()
	session(t, db, sessionID)
	payload, err := json.Marshal(map[string]any{
		"input":  map[string]any{"command": "git status"},
		"result": result,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO events (session_id, ts, type, tool_name, status, payload) VALUES (?, ?, 'tool_call', 'Bash', ?, ?)`,
		sessionID, at.UTC().Format(tsLayout), status, string(payload)); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

// openFindings returns the unresolved auto_mode_no_verdict rows' messages.
func openFindings(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT message FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`, Rule)
	if err != nil {
		t.Fatalf("query findings: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func allFindings(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM config_lint_findings WHERE rule = ?`, Rule).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCountMatchesBothVariants(t *testing.T) {
	db := testDB(t)
	toolCall(t, db, 1, t0, "error", variantError)
	toolCall(t, db, 1, t0.Add(3*time.Second), "error", variantError)
	toolCall(t, db, 2, t0.Add(5*time.Second+250*time.Millisecond), "error", variantCutOff)

	got, err := Count(db, t0.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got.Events != 3 || got.Sessions != 2 {
		t.Errorf("Count = %d events in %d sessions, want 3 in 2", got.Events, got.Sessions)
	}
	if want := "2026-09-28T06:38:05.250Z"; got.LastAt != want {
		t.Errorf("LastAt = %q, want %q (the newest matching row, as stored)", got.LastAt, want)
	}
}

func TestCountIgnoresOtherErrors(t *testing.T) {
	db := testDB(t)
	// A tool error that is not the classifier's.
	toolCall(t, db, 1, t0, "error", "Error: Exit code 1\nfatal: not a git repository")
	// An ordinary permission denial.
	toolCall(t, db, 1, t0.Add(time.Second), "denied", "Permission to use Bash has been denied.")
	// The sentence quoted by a call that SUCCEEDED (an agent reading a log).
	toolCall(t, db, 2, t0.Add(2*time.Second), "ok", variantError)
	// The right text on a row that is not a tool call.
	session(t, db, 3)
	if _, err := db.Exec(
		`INSERT INTO events (session_id, ts, type, status, payload) VALUES (3, ?, 'error', 'error', ?)`,
		t0.Format(tsLayout), `{"result":"`+variantError+`"}`); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	got, err := Count(db, t0.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got.Events != 0 || got.Sessions != 0 || got.LastAt != "" {
		t.Errorf("Count = %+v, want nothing: none of these is a no-verdict refusal", got)
	}
}

func TestCountRespectsWindow(t *testing.T) {
	db := testDB(t)
	since := t0
	toolCall(t, db, 1, since.Add(-time.Hour), "error", variantError)        // long before
	toolCall(t, db, 1, since.Add(-time.Millisecond), "error", variantError) // just before
	toolCall(t, db, 2, since, "error", variantError)                        // on the bound: in
	// Later in the bound's own second. A whole-second bound ("…00Z") would sort
	// after this row's "…00.896Z" and lose it.
	toolCall(t, db, 3, since.Add(896*time.Millisecond), "error", variantError)
	toolCall(t, db, 3, since.Add(9*time.Minute), "error", variantCutOff)

	got, err := Count(db, since)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got.Events != 3 || got.Sessions != 2 {
		t.Errorf("Count(since) = %d events in %d sessions, want 3 in 2", got.Events, got.Sessions)
	}
	// A bound past every row counts nothing.
	none, err := Count(db, since.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if none.Events != 0 || none.LastAt != "" {
		t.Errorf("Count(after everything) = %+v, want zero", none)
	}
}

// A burst seen by three consecutive ticks is ONE unresolved finding, and only
// one row was ever written for it.
func TestEvaluateRaisesOneAlert(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 5; i++ {
		toolCall(t, db, 1, t0.Add(time.Duration(i)*4*time.Second), "error", variantError)
	}
	for i := 0; i < 4; i++ {
		toolCall(t, db, 2, t0.Add(22*time.Second+time.Duration(i)*5*time.Second), "error", variantError)
	}

	clock := t0.Add(time.Minute)
	tk := &Ticker{DB: db, Now: func() time.Time { return clock }}
	for i := 0; i < 3; i++ {
		tk.Once()
		clock = clock.Add(DefaultInterval)
	}

	open := openFindings(t, db)
	if len(open) != 1 {
		t.Fatalf("unresolved findings = %d (%v), want exactly 1", len(open), open)
	}
	if n := allFindings(t, db); n != 1 {
		t.Errorf("finding rows = %d, want 1: a burst must not write a row per tick", n)
	}
	want := "9 permission checks got no verdict in the last 10 minutes across 2 sessions — " +
		"Claude Code's server-side classifier is failing; affected sessions pause until it recovers."
	if open[0] != want {
		t.Errorf("message = %q\nwant      %q", open[0], want)
	}
	var sev, target string
	if err := db.QueryRow(
		`SELECT severity, target FROM config_lint_findings WHERE rule = ?`, Rule).Scan(&sev, &target); err != nil {
		t.Fatal(err)
	}
	if sev != "warn" || target != Target {
		t.Errorf("finding = severity %q target %q, want warn / %q", sev, target, Target)
	}
	if alerting, err := Alerting(db); err != nil || !alerting {
		t.Errorf("Alerting = %v, %v; want true", alerting, err)
	}
}

func TestEvaluateBelowThresholdNoAlert(t *testing.T) {
	db := testDB(t)
	// Two refusals: the transient failure the message itself describes.
	toolCall(t, db, 1, t0, "error", variantError)
	toolCall(t, db, 2, t0.Add(30*time.Second), "error", variantCutOff)
	// And a third, but outside the ten-minute window of the evaluation below.
	toolCall(t, db, 3, t0.Add(-20*time.Minute), "error", variantError)

	if err := Evaluate(db, t0.Add(time.Minute)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if n := allFindings(t, db); n != 0 {
		t.Errorf("finding rows = %d, want 0 below the threshold of %d", n, DefaultAlertMin)
	}
	if alerting, err := Alerting(db); err != nil || alerting {
		t.Errorf("Alerting = %v, %v; want false", alerting, err)
	}

	// The threshold is the operator's to move.
	t.Setenv(EnvAlertMin, "2")
	if err := Evaluate(db, t0.Add(time.Minute)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if open := openFindings(t, db); len(open) != 1 || !strings.HasPrefix(open[0], "2 permission checks ") {
		t.Errorf("with %s=2: unresolved findings = %v, want one for 2 checks", EnvAlertMin, open)
	}
}

func TestEvaluateResolvesAfterQuiet(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 3; i++ {
		toolCall(t, db, 1, t0.Add(time.Duration(i)*time.Second), "error", variantError)
	}
	last := t0.Add(2 * time.Second)

	if err := Evaluate(db, t0.Add(time.Minute)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(openFindings(t, db)) != 1 {
		t.Fatal("the burst did not raise the alert")
	}

	// Past the alert window but inside the quiet one: the burst is tailing off,
	// the alert stays.
	if err := Evaluate(db, last.Add(15*time.Minute)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(openFindings(t, db)) != 1 {
		t.Error("the alert resolved after 15 minutes, want it kept until 30 quiet minutes")
	}

	// Thirty quiet minutes: resolved, and the row stays as history.
	if err := Evaluate(db, last.Add(QuietWindow+time.Second)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if open := openFindings(t, db); len(open) != 0 {
		t.Errorf("unresolved findings = %v, want none after %s without a refusal", open, QuietWindow)
	}
	if n := allFindings(t, db); n != 1 {
		t.Errorf("finding rows = %d, want the resolved one kept", n)
	}

	// A new burst after the resolve is a new alert — a second row.
	for i := 0; i < 3; i++ {
		toolCall(t, db, 2, t0.Add(2*time.Hour+time.Duration(i)*time.Second), "error", variantCutOff)
	}
	if err := Evaluate(db, t0.Add(2*time.Hour+time.Minute)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(openFindings(t, db)) != 1 || allFindings(t, db) != 2 {
		t.Errorf("after a second burst: %d unresolved of %d rows, want 1 of 2",
			len(openFindings(t, db)), allFindings(t, db))
	}
}

func TestAlertMinEnv(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", DefaultAlertMin},
		{"5", 5},
		{" 1 ", 1},
		{"0", DefaultAlertMin},
		{"-2", DefaultAlertMin},
		{"many", DefaultAlertMin},
	}
	for _, c := range cases {
		t.Setenv(EnvAlertMin, c.env)
		if got := AlertMin(); got != c.want {
			t.Errorf("AlertMin() with %s=%q = %d, want %d", EnvAlertMin, c.env, got, c.want)
		}
	}
}

func TestMessageSingular(t *testing.T) {
	got := Message(Counts{Events: 1, Sessions: 1})
	if !strings.HasPrefix(got, "1 permission check got no verdict in the last 10 minutes across 1 session — ") {
		t.Errorf("Message = %q", got)
	}
}

// Run evaluates at once, without waiting a full interval, and stops with its
// context.
func TestTickerRunEvaluatesImmediatelyAndStops(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 3; i++ {
		toolCall(t, db, 1, t0.Add(time.Duration(i)*time.Second), "error", variantError)
	}
	ctx, cancel := context.WithCancel(context.Background())
	tk := &Ticker{DB: db, Interval: 5 * time.Millisecond, Now: func() time.Time { return t0.Add(time.Minute) }}
	done := make(chan struct{})
	go func() {
		tk.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if alerting, _ := Alerting(db); alerting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not raise the alert")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	if n := allFindings(t, db); n != 1 {
		t.Errorf("finding rows = %d after several ticks, want 1", n)
	}
}

// A database that cannot answer leaves the alert untouched and does not panic.
func TestOnceSurvivesQueryError(t *testing.T) {
	db := testDB(t)
	db.Close()
	(&Ticker{DB: db}).Once()
	if _, err := Count(db, t0); err == nil {
		t.Error("Count on a closed DB returned no error")
	}
	if _, err := Alerting(db); err == nil {
		t.Error("Alerting on a closed DB returned no error")
	}
}
