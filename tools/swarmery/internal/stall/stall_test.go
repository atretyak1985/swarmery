package stall

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

// fixture is a migrated store with one project and one workspace plan.
type fixture struct {
	t      *testing.T
	db     *sql.DB
	taskID int64
	dir    string
	nextID int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "stall.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{t: t, db: db, dir: t.TempDir()}
	f.exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES(1, '/repo/p', 'p', '2026-01-01T00:00:00Z')`)
	res := f.exec(`INSERT INTO tasks(project_id, title, prompt, status, created_at, source, external_id)
		VALUES(1, 'The plan', 'goal', 'running', '2026-10-01T00:00:00Z', 'workspace', '2026-10-01-the-plan')`)
	f.taskID, _ = res.LastInsertId()
	return f
}

func (f *fixture) exec(q string, args ...any) sql.Result {
	f.t.Helper()
	res, err := f.db.Exec(q, args...)
	if err != nil {
		f.t.Fatalf("exec: %v\n%s", err, q)
	}
	return res
}

// phase plants a phase doc (with an optional `**Playbook:**`) and its row.
func (f *fixture) phase(seq int, playbook string) (id int64, docPath string) {
	f.t.Helper()
	docPath = filepath.Join(f.dir, "phase-"+string(rune('0'+seq))+".md")
	head := "# Phase\nStatus: In progress\n"
	if playbook != "" {
		head += "**Playbook:** " + playbook + "\n"
	}
	if err := os.WriteFile(docPath, []byte(head+"\n## Acceptance Criteria\n- [ ] x\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	res := f.exec(`INSERT INTO epic_phases(workspace_task_id, seq, name, doc_path) VALUES(?, ?, ?, ?)`,
		f.taskID, seq, "Phase "+string(rune('0'+seq)), docPath)
	id, _ = res.LastInsertId()
	return id, docPath
}

// session plants a sessions row; lastTurn > 0 adds one assistant turn ending then.
func (f *fixture) session(uuid, status string, lastTurn time.Duration) int64 {
	f.t.Helper()
	f.nextID++
	id := f.nextID
	f.exec(`INSERT INTO sessions(id, project_id, session_uuid, status, started_at, ended_at)
		VALUES(?, 1, ?, ?, ?, ?)`, id, uuid, status, ago(2*time.Hour), ago(lastTurn))
	if lastTurn > 0 {
		f.exec(`INSERT INTO turns(session_id, seq, role, started_at, ended_at) VALUES(?, 1, 'assistant', ?, ?)`,
			id, ago(lastTurn+time.Minute), ago(lastTurn))
	}
	return id
}

func (f *fixture) permission(sessionID int64, age time.Duration) int64 {
	f.t.Helper()
	res := f.exec(`INSERT INTO permission_requests(session_id, tool_name, request_json, status, requested_at)
		VALUES(?, 'Bash', '{}', 'pending', ?)`, sessionID, now.Add(-age).UTC().Format("2006-01-02T15:04:05.000Z"))
	id, _ := res.LastInsertId()
	return id
}

func (f *fixture) running(phaseID int64, uuid string, started time.Duration) {
	f.exec(`UPDATE epic_phases SET run_state='running', run_session_uuid=?, run_started_at=? WHERE id=?`,
		uuid, ago(started), phaseID)
}

// settled marks the executor finished (run ended `ago` ended) with a report.
func (f *fixture) settled(phaseID int64, runUUID string, ended time.Duration) {
	f.exec(`UPDATE epic_phases SET run_state='done', run_session_uuid=?, run_started_at=?, run_ended_at=?,
		completion_report='shipped' WHERE id=?`, runUUID, ago(3*time.Hour), ago(ended), phaseID)
}

func (f *fixture) reviewing(phaseID int64, uuid string, started time.Duration) {
	f.exec(`UPDATE epic_phases SET review_session_uuid=?, review_started_at=? WHERE id=?`, uuid, ago(started), phaseID)
}

func (f *fixture) verifying(phaseID int64, uuid string, started time.Duration) {
	f.exec(`INSERT INTO verification_runs(target_key, verify_session_uuid, status, started_at)
		VALUES('phase:' || ?, ?, 'running', ?)`, phaseID, uuid, ago(started))
}

func defaults(t *testing.T) Config {
	t.Helper()
	g, err := guards.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if g.StallMinutes != 20 || g.StallMinutesFor("long-tests") != 60 {
		t.Fatalf("shipped guards changed (stallMinutes=%d, long-tests=%d): re-read this test's arithmetic",
			g.StallMinutes, g.StallMinutesFor("long-tests"))
	}
	return Config{Guards: g}
}

func detectOne(t *testing.T, f *fixture) []Stalled {
	t.Helper()
	got, err := Detect(f.db, now, defaults(t))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	return got
}

// TestDetect is the phase's acceptance matrix, one fixture per row.
func TestDetect(t *testing.T) {
	type want struct {
		stalled bool
		stage   Stage
		reason  Reason
		minutes int
	}
	for _, tc := range []struct {
		name  string
		setup func(f *fixture) (requestID int64)
		want  want
	}{
		{
			name: "executor silent 25 min → stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.running(id, "exec-1", time.Hour)
				f.session("exec-1", "idle", 25*time.Minute)
				return 0
			},
			want: want{true, StageExecutor, ReasonNoTurns, 25},
		},
		{
			name: "executor silent 10 min → not stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.running(id, "exec-1", time.Hour)
				f.session("exec-1", "idle", 10*time.Minute)
				return 0
			},
		},
		{
			name: "executor never ingested, started 25 min ago → stalled from its start",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.running(id, "exec-1", 25*time.Minute)
				return 0
			},
			want: want{true, StageExecutor, ReasonNoTurns, 25},
		},
		{
			name: "executor ended without a Completion Report → stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.running(id, "exec-1", time.Hour)
				f.session("exec-1", "completed", 5*time.Minute)
				return 0
			},
			want: want{true, StageExecutor, ReasonEnded, 5},
		},
		{
			name: "executor ended inside the continuation grace → not stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.running(id, "exec-1", time.Hour)
				f.session("exec-1", "completed", time.Minute)
				return 0
			},
		},
		{
			name: "executor ended with its report written → the daemon's to settle, not stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.running(id, "exec-1", time.Hour)
				f.exec(`UPDATE epic_phases SET completion_report='shipped' WHERE id=?`, id)
				f.session("exec-1", "completed", 30*time.Minute)
				return 0
			},
		},
		{
			name: "reviewer ended without verdict → stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.settled(id, "exec-1", 40*time.Minute)
				f.reviewing(id, "rev-1", 35*time.Minute)
				f.session("rev-1", "completed", 30*time.Minute)
				return 0
			},
			want: want{true, StageReviewer, ReasonEnded, 30},
		},
		{
			name: "reviewer with its verdict recorded → not stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.settled(id, "exec-1", 40*time.Minute)
				f.reviewing(id, "rev-1", 35*time.Minute)
				f.session("rev-1", "completed", 30*time.Minute)
				f.exec(`INSERT INTO phase_reviews(scope, phase_id, workspace_task_id, session_uuid, verdict, started_at, finished_at)
					VALUES('phase', ?, '1', 'rev-1', 'pass', ?, ?)`, id, ago(35*time.Minute), ago(30*time.Minute))
				return 0
			},
		},
		{
			name: "reviewer stamp from before the current run → superseded, not stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.reviewing(id, "rev-old", 5*time.Hour)
				f.settled(id, "exec-2", 40*time.Minute)
				f.session("rev-old", "completed", 5*time.Hour)
				return 0
			},
		},
		{
			name: "verifier with a pending permission 25 min → stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.settled(id, "exec-1", time.Hour)
				f.verifying(id, "ver-1", 50*time.Minute)
				s := f.session("ver-1", "waiting_approval", 25*time.Minute)
				return f.permission(s, 25*time.Minute)
			},
			want: want{true, StageVerifier, ReasonPermissionPending, 25},
		},
		{
			name: "permission pending 5 min → not stalled, however quiet the transcript",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.running(id, "exec-1", time.Hour)
				s := f.session("exec-1", "waiting_approval", 25*time.Minute)
				f.permission(s, 5*time.Minute)
				return 0
			},
		},
		{
			name: "playbook long-tests silent 40 min → not stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "long-tests")
				f.running(id, "exec-1", 2*time.Hour)
				f.session("exec-1", "idle", 40*time.Minute)
				return 0
			},
		},
		{
			name: "playbook long-tests silent 65 min → stalled",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "long-tests")
				f.running(id, "exec-1", 2*time.Hour)
				f.session("exec-1", "idle", 65*time.Minute)
				return 0
			},
			want: want{true, StageExecutor, ReasonNoTurns, 65},
		},
		{
			name: "settled phase with nothing in flight → not a candidate",
			setup: func(f *fixture) int64 {
				id, _ := f.phase(1, "")
				f.settled(id, "exec-1", 3*time.Hour)
				f.session("exec-1", "completed", 3*time.Hour)
				return 0
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			reqID := tc.setup(f)
			got := detectOne(t, f)
			if !tc.want.stalled {
				if len(got) != 0 {
					t.Fatalf("want no stall, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want one stall, got %d: %+v", len(got), got)
			}
			st := got[0]
			if st.Stage != tc.want.stage || st.Reason != tc.want.reason {
				t.Errorf("stage/reason = %s/%s, want %s/%s", st.Stage, st.Reason, tc.want.stage, tc.want.reason)
			}
			if m := st.Minutes(now); m != tc.want.minutes {
				t.Errorf("minutes = %d, want %d", m, tc.want.minutes)
			}
			if st.RequestID != reqID {
				t.Errorf("requestID = %d, want %d", st.RequestID, reqID)
			}
			if st.TaskID != f.taskID || st.PlanTitle != "The plan" || st.Project != "p" {
				t.Errorf("plan context = %d %q %q", st.TaskID, st.PlanTitle, st.Project)
			}
		})
	}
}

// fakeNotifier records what the ledger asked to announce.
type fakeNotifier struct{ got []Stalled }

func (n *fakeNotifier) Emit(st Stalled) { n.got = append(n.got, st) }

// TestReconcileNotifiesOncePerPhaseReason: a standing stall announces once, a
// cleared one leaves the ledger, and the same stall recurring announces again.
func TestReconcileNotifiesOncePerPhaseReason(t *testing.T) {
	f := newFixture(t)
	id, _ := f.phase(1, "")
	f.running(id, "exec-1", time.Hour)
	sid := f.session("exec-1", "idle", 25*time.Minute)

	var n fakeNotifier
	var changed []int64
	tk := &Ticker{DB: f.db, Config: defaults(t), Emit: n.Emit,
		Changed: func(task int64) { changed = append(changed, task) },
		Now:     func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if _, err := tk.Tick(); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(n.got) != 1 || n.got[0].Reason != ReasonNoTurns {
		t.Fatalf("three ticks over one standing stall: want 1 event, got %+v", n.got)
	}
	if len(changed) != 1 || changed[0] != f.taskID {
		t.Errorf("Changed = %v, want one nudge for task %d", changed, f.taskID)
	}
	var notified sql.NullString
	if err := f.db.QueryRow(`SELECT notified_at FROM stall_notices WHERE phase_id=? AND reason='no-turns'`, id).
		Scan(&notified); err != nil || !notified.Valid {
		t.Fatalf("ledger row: notified_at=%v err=%v", notified, err)
	}

	// The executor speaks again: the condition clears and the row goes.
	f.exec(`INSERT INTO turns(session_id, seq, role, started_at, ended_at) VALUES(?, 2, 'assistant', ?, ?)`,
		sid, ago(time.Minute), ago(30*time.Second))
	res, err := tk.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleared != 1 {
		t.Fatalf("Cleared = %d, want 1", res.Cleared)
	}
	var rows int
	f.db.QueryRow(`SELECT COUNT(*) FROM stall_notices`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("ledger rows after clearing = %d, want 0", rows)
	}

	// Silent again, 25 minutes later: a recurrence is a new notice.
	later := now.Add(25 * time.Minute)
	tk.Now = func() time.Time { return later }
	if _, err := tk.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 2 {
		t.Fatalf("recurrence after clearing: want 2 events in total, got %d", len(n.got))
	}
}

// TestReconcileReasonChangeIsANewNotice: the same phase moving from silent to
// ended is a different (phase, reason) — the old row clears, the new one notifies.
func TestReconcileReasonChangeIsANewNotice(t *testing.T) {
	f := newFixture(t)
	id, _ := f.phase(1, "")
	f.running(id, "exec-1", time.Hour)
	f.session("exec-1", "idle", 25*time.Minute)

	var n fakeNotifier
	tk := &Ticker{DB: f.db, Config: defaults(t), Emit: n.Emit, Now: func() time.Time { return now }}
	if _, err := tk.Tick(); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE sessions SET status='completed' WHERE session_uuid='exec-1'`)
	res, err := tk.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleared != 1 || len(n.got) != 2 || n.got[1].Reason != ReasonEnded {
		t.Fatalf("reason change: cleared=%d events=%+v", res.Cleared, n.got)
	}
}

// TestReconcileWithoutNotifier keeps the ledger honest when no webhook is set.
func TestReconcileWithoutNotifier(t *testing.T) {
	f := newFixture(t)
	id, _ := f.phase(1, "")
	f.running(id, "exec-1", time.Hour)
	f.session("exec-1", "idle", 25*time.Minute)
	st, err := Detect(f.db, now, defaults(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(f.db, now, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notified) != 1 {
		t.Fatalf("Notified = %d, want 1", len(res.Notified))
	}
	var notified sql.NullString
	f.db.QueryRow(`SELECT notified_at FROM stall_notices`).Scan(&notified)
	if notified.Valid {
		t.Errorf("notified_at = %q without a notifier, want NULL", notified.String)
	}
}

func TestDescribe(t *testing.T) {
	for _, tc := range []struct {
		stage  Stage
		reason Reason
		want   string
	}{
		{StageExecutor, ReasonNoTurns, "no new turn for 21 min"},
		{StageVerifier, ReasonPermissionPending, "a permission request has waited 21 min"},
		{StageExecutor, ReasonEnded, "the session ended without its Completion Report (21 min ago)"},
		{StageReviewer, ReasonEnded, "the session ended without a verdict (21 min ago)"},
		{StageVerifier, ReasonEnded, "the session ended without a verification record (21 min ago)"},
	} {
		if got := Describe(tc.stage, tc.reason, 21); got != tc.want {
			t.Errorf("Describe(%s, %s) = %q, want %q", tc.stage, tc.reason, got, tc.want)
		}
	}
}
