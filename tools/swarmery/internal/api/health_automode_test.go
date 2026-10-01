package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/automode"
)

// eventTSLayout is how ingest stores events.ts (UTC, milliseconds).
const eventTSLayout = "2006-01-02T15:04:05.000Z"

// noVerdictResult is the refusal a tool call gets when the auto mode permission
// check fails to answer, as the transcript records it.
const noVerdictResult = "Error: The server-side auto mode classifier gave no verdict (error), so auto mode " +
	"cannot determine the safety of Bash. This is a transient failure of the check, not a judgment about the action."

// seedNoVerdict inserts one refused tool call for sessionID at the given time.
// The session row is created on first use (project 1 of projectsTestServer).
func seedNoVerdict(t *testing.T, db *sql.DB, sessionID int64, at time.Time) string {
	t.Helper()
	execSQL(t, db, `INSERT OR IGNORE INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (?, 1, 'u-automode-' || ?, 'active', '2026-09-28T00:00:00Z', 'jsonl')`, sessionID, sessionID)
	payload, err := json.Marshal(map[string]any{
		"input":  map[string]any{"command": "git status"},
		"result": noVerdictResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := at.UTC().Format(eventTSLayout)
	execSQL(t, db, `INSERT INTO events (session_id, ts, type, tool_name, status, payload)
		VALUES (?, ?, 'tool_call', 'Bash', 'error', ?)`, sessionID, ts, string(payload))
	return ts
}

// healthBody GETs /api/health and returns the status and the raw body.
func healthBody(t *testing.T, srvURL string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(srvURL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// autoModeKeys decodes the health body and returns the autoModeClassifier
// object's raw members.
func autoModeKeys(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("decode health: %v\n%s", err, body)
	}
	raw, ok := top["autoModeClassifier"]
	if !ok {
		t.Fatalf("health has no autoModeClassifier field:\n%s", body)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("autoModeClassifier is not an object: %v\n%s", err, raw)
	}
	return obj
}

// TestHealthAutoModeField: /api/health carries autoModeClassifier with exactly
// noVerdictLastHour, sessionsLastHour, lastAt and alerting — counting the last
// hour only, and alerting while the auto_mode_no_verdict finding is open.
func TestHealthAutoModeField(t *testing.T) {
	srv, db := projectsTestServer(t)
	now := time.Now()
	seedNoVerdict(t, db, 10, now.Add(-2*time.Hour)) // outside the hour
	seedNoVerdict(t, db, 10, now.Add(-4*time.Minute))
	seedNoVerdict(t, db, 10, now.Add(-3*time.Minute))
	newest := seedNoVerdict(t, db, 12, now.Add(-2*time.Minute))
	// The ticker's evaluation: three refusals in ten minutes open the alert.
	if err := automode.Evaluate(db, now); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	status, body := healthBody(t, srv.URL)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", status, body)
	}
	obj := autoModeKeys(t, body)
	var keys []string
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"alerting", "lastAt", "noVerdictLastHour", "sessionsLastHour"}
	if len(keys) != len(want) {
		t.Fatalf("autoModeClassifier keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("autoModeClassifier keys = %v, want %v", keys, want)
		}
	}

	var resp healthDTO
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	got := resp.AutoModeClassifier
	if got.NoVerdictLastHour != 3 || got.SessionsLastHour != 2 {
		t.Errorf("last hour = %d refusals in %d sessions, want 3 in 2", got.NoVerdictLastHour, got.SessionsLastHour)
	}
	if got.LastAt == nil || *got.LastAt != newest {
		t.Errorf("lastAt = %v, want %q", got.LastAt, newest)
	}
	if !got.Alerting {
		t.Error("alerting = false, want true while the finding is open")
	}

	// The same finding is the Inbox alert: listed by GET /api/alerts as a bare
	// finding — no account, nothing to resume.
	alerts := listAlertsOK(t, srv)
	if len(alerts) != 1 {
		t.Fatalf("alerts = %+v, want exactly the auto mode one", alerts)
	}
	a := alerts[0]
	if a.Rule != automode.Rule || a.Target != automode.Target || a.Severity != "warn" ||
		a.Message != automode.Message(automode.Counts{Events: 3, Sessions: 2}) {
		t.Errorf("alert = %+v", a)
	}
	if a.Account != "" || a.Kind != "" || a.Reason != "" || a.OpenedAt != "" || a.ResetsAt != "" {
		t.Errorf("alert carries breaker fields it has no business with: %+v", a)
	}
}

// A machine that saw no refusal reports zeroes, and lastAt as an explicit null.
func TestHealthAutoModeFieldZeroWhenQuiet(t *testing.T) {
	srv, _ := projectsTestServer(t)
	status, body := healthBody(t, srv.URL)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", status, body)
	}
	obj := autoModeKeys(t, body)
	for k, want := range map[string]string{
		"noVerdictLastHour": "0", "sessionsLastHour": "0", "lastAt": "null", "alerting": "false",
	} {
		if got := string(obj[k]); got != want {
			t.Errorf("autoModeClassifier.%s = %s, want %s", k, got, want)
		}
	}
}

// The summary is computed at most once per autoModeCacheTTL: a refusal that
// lands inside the TTL shows up only after it.
func TestHealthAutoModeFieldIsCached(t *testing.T) {
	_, db := projectsTestServer(t)
	h := &Handler{DB: db}
	now := time.Now()
	seedNoVerdict(t, db, 10, now.Add(-5*time.Minute))

	if got := h.autoModeHealth(now); got.NoVerdictLastHour != 1 {
		t.Fatalf("first read = %+v, want 1 refusal", got)
	}
	seedNoVerdict(t, db, 10, now.Add(-time.Minute))
	if got := h.autoModeHealth(now.Add(autoModeCacheTTL - time.Second)); got.NoVerdictLastHour != 1 {
		t.Errorf("read inside the TTL = %+v, want the cached 1", got)
	}
	if got := h.autoModeHealth(now.Add(autoModeCacheTTL)); got.NoVerdictLastHour != 2 {
		t.Errorf("read after the TTL = %+v, want the recomputed 2", got)
	}
}

// TestHealthSurvivesQueryError: when the auto mode query cannot run, health
// still answers 200 with the field at its zero values.
func TestHealthSurvivesQueryError(t *testing.T) {
	srv, db := projectsTestServer(t)
	// Both reads behind the field now fail: the count (events) and the alert
	// state (config_lint_findings).
	execSQL(t, db, `ALTER TABLE events RENAME TO events_gone`)
	execSQL(t, db, `ALTER TABLE config_lint_findings RENAME TO config_lint_findings_gone`)
	if _, err := automode.Count(db, time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("the count still runs — this test would prove nothing")
	}
	if _, err := automode.Alerting(db); err == nil {
		t.Fatal("the alert read still runs — this test would prove nothing")
	}

	status, body := healthBody(t, srv.URL)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — health must not fail over this field\n%s", status, body)
	}
	obj := autoModeKeys(t, body)
	for k, want := range map[string]string{
		"noVerdictLastHour": "0", "sessionsLastHour": "0", "lastAt": "null", "alerting": "false",
	} {
		if got := string(obj[k]); got != want {
			t.Errorf("autoModeClassifier.%s = %s, want %s", k, got, want)
		}
	}
	var resp healthDTO
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" {
		t.Errorf("status field = %q, want ok", resp.Status)
	}
}
