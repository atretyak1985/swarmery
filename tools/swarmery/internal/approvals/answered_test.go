// A question answered in the terminal leaves the dashboard at once. The claims:
//
//  1. Once ingest records the call's result (tool_call status set), Sweep
//     resolves the pending row resolved_elsewhere via 'terminal' and wakes the
//     shim — the input ingest stored carries tool_use_id, the hook's does not.
//  2. A call still waiting for its result, a different call, an event from
//     before the lookback, or a newer identical call still waiting all leave
//     the row pending.
package approvals

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// seedToolCall stores a tool_call event the way ingest does: the input with
// the call's tool_use_id added, status NULL until the result arrives.
func seedToolCall(t *testing.T, db *sql.DB, sessionID int64, tool string, input string, ts time.Time, status string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	m["tool_use_id"] = json.RawMessage(`"toolu_test"`)
	payload, _ := json.Marshal(map[string]any{"input": m})
	var st any
	if status != "" {
		st = status
	}
	if _, err := db.Exec(
		`INSERT INTO events (session_id, ts, type, tool_name, status, payload) VALUES (?, ?, 'tool_call', ?, ?, ?)`,
		sessionID, ts.UTC().Format(tsFormat), tool, st, string(payload)); err != nil {
		t.Fatal(err)
	}
}

func TestSweepResolvesQuestionAnsweredInTerminal(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-ask")
	now := time.Now()
	svc := New(db, nil, Options{Now: func() time.Time { return now }})
	id, ch, _, err := svc.Open(askHookInput(t, "uuid-ask"))
	if err != nil {
		t.Fatal(err)
	}
	input := `{"questions":` + askQuestionsJSON + `}`

	seedToolCall(t, db, sid, "AskUserQuestion", input, now.Add(-3*time.Second), "")
	svc.Sweep()
	if got := requestStatus(t, db, id); got != StatusPending {
		t.Fatalf("resolved before the result: status = %q", got)
	}

	if _, err := db.Exec(`UPDATE events SET status = 'ok' WHERE session_id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	svc.Sweep()
	if got := requestStatus(t, db, id); got != StatusResolvedElsewhere {
		t.Fatalf("status = %q, want resolved_elsewhere", got)
	}
	select {
	case d := <-ch:
		if d.Status != StatusResolvedElsewhere || d.Via != "terminal" {
			t.Errorf("waiter got %+v, want resolved_elsewhere via terminal", d)
		}
	default:
		t.Fatal("waiter not woken")
	}
	if got := sessionStatus(t, db, sid); got == "waiting_approval" {
		t.Error("session stuck in waiting_approval")
	}
}

func TestSweepLeavesUnrelatedCallsPending(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, db *sql.DB, sid int64, now time.Time)
	}{
		{"another command answered", func(t *testing.T, db *sql.DB, sid int64, now time.Time) {
			seedToolCall(t, db, sid, "Bash", `{"command":"pwd","description":"d"}`, now, "ok")
		}},
		{"same command before the lookback", func(t *testing.T, db *sql.DB, sid int64, now time.Time) {
			seedToolCall(t, db, sid, "Bash", `{"command":"ls","description":"d"}`, now.Add(-answeredLookback-time.Second), "ok")
		}},
		{"a newer identical call still waits", func(t *testing.T, db *sql.DB, sid int64, now time.Time) {
			seedToolCall(t, db, sid, "Bash", `{"command":"ls","description":"d"}`, now.Add(-time.Minute), "ok")
			seedToolCall(t, db, sid, "Bash", `{"command":"ls","description":"d"}`, now.Add(-time.Second), "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			sid := seedSession(t, db, "uuid-bash")
			now := time.Now()
			svc := New(db, nil, Options{Now: func() time.Time { return now }})
			id, _, _, err := svc.Open(hookInput(t, "uuid-bash", "Bash", "ls"))
			if err != nil {
				t.Fatal(err)
			}
			tc.seed(t, db, sid, now)
			svc.Sweep()
			if got := requestStatus(t, db, id); got != StatusPending {
				t.Fatalf("status = %q, want pending", got)
			}
		})
	}
}
