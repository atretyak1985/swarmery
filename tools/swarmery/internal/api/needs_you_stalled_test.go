package api

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stalledFixture plants a workspace plan with three phases on top of
// needsYouServerDB's fixture and ledger rows for them, as the detector's tick
// would have written: phase 1 silent (executor, no session ingested), phase 2
// held by permission request 101 (session 1's pending approval), and a phase of
// an archived plan.
func stalledFixture(t *testing.T, db *sql.DB) (silent, held int64) {
	t.Helper()
	now := time.Now().UTC()
	ago := func(d time.Duration) string { return now.Add(-d).Format(needsYouTSFormat) }
	exec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
		return res
	}
	task := func(ext string, archived any) int64 {
		res := exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id, archived_at)
			VALUES (1, 'Stall plan', 'goal', 'running', ?, 'workspace', ?, ?)`, ago(96*time.Hour), ext, archived)
		id, _ := res.LastInsertId()
		return id
	}
	phase := func(taskID int64, seq int) int64 {
		res := exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (?, ?, ?, ?)`,
			taskID, seq, "Phase "+strconv.Itoa(seq), "/ws/"+strconv.FormatInt(taskID, 10)+"/phase-"+strconv.Itoa(seq)+".md")
		id, _ := res.LastInsertId()
		return id
	}
	notice := func(phaseID int64, reason, stage, uuid string, req any, since time.Duration) {
		exec(`INSERT INTO stall_notices (phase_id, reason, stage, session_uuid, request_id, since, first_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, phaseID, reason, stage, uuid, req, ago(since), ago(time.Minute))
	}

	plan := task("2026-10-22-stall-plan", nil)
	silent = phase(plan, 1)
	held = phase(plan, 2)
	notice(silent, "no-turns", "executor", "exec-not-ingested", nil, 25*time.Minute)
	notice(held, "permission-pending", "verifier", "uuid-a-0000-1111", 101, 60*time.Minute)

	old := task("2026-09-01-archived", ago(time.Hour))
	notice(phase(old, 1), "no-turns", "executor", "", nil, 3*time.Hour)
	return silent, held
}

func TestNeedsYouStalledPhase(t *testing.T) {
	srv, db := needsYouServerDB(t)
	silent, held := stalledFixture(t, db)

	resp := getNeedsYou(t, srv.URL+"/api/needs-you")
	var stalled []needsYouItem
	var approval *needsYouItem
	for i, it := range resp.Items {
		switch {
		case it.Kind == needsYouStalledPhase:
			stalled = append(stalled, it)
		case it.Kind == needsYouApproval && it.RequestID != nil && *it.RequestID == 101:
			approval = &resp.Items[i]
		}
	}

	// The silent phase is its own item; the held one is NOT (merged below); the
	// archived plan's is skipped.
	if len(stalled) != 1 {
		t.Fatalf("stalled_phase items = %d, want 1: %+v", len(stalled), stalled)
	}
	it := stalled[0]
	if it.Phase == nil || it.Phase.PhaseID != silent || it.Phase.PlanExternalID != "2026-10-22-stall-plan" {
		t.Fatalf("phase = %+v, want the silent phase of the stall plan", it.Phase)
	}
	if it.Stall == nil || it.Stall.Stage != "executor" || it.Stall.Reason != "no-turns" || it.Stall.SinceMinutes != 25 {
		t.Errorf("stall = %+v, want executor/no-turns/25", it.Stall)
	}
	if it.SessionName != "Stall plan · Phase 1: Phase 1" {
		t.Errorf("sessionName = %q", it.SessionName)
	}
	if it.Preview != "executor — no new turn for 25 min" {
		t.Errorf("preview = %q", it.Preview)
	}
	if it.SessionID != 0 || it.SessionUUID != "exec-not-ingested" {
		t.Errorf("session = %d/%q, want 0/the parked uuid (not ingested yet)", it.SessionID, it.SessionUUID)
	}
	if it.BlockingSeconds < 25*60 || it.BlockingSeconds > 26*60 {
		t.Errorf("blockingSeconds = %d, want ~25 min", it.BlockingSeconds)
	}

	// permission-pending merged into the approval of request 101: one row, which
	// keeps the approve/deny action and gains the phase («open phase»).
	if approval == nil {
		t.Fatal("approval #101 vanished from the queue")
	}
	if approval.Phase == nil || approval.Phase.PhaseID != held {
		t.Fatalf("approval #101 phase = %+v, want the held phase %d", approval.Phase, held)
	}
	if approval.Stall == nil || approval.Stall.Reason != "permission-pending" || approval.Stall.Stage != "verifier" {
		t.Errorf("approval #101 stall = %+v, want verifier/permission-pending", approval.Stall)
	}
	if approval.SessionID != 1 {
		t.Errorf("merged approval session = %d, want 1 (the approval's own)", approval.SessionID)
	}
}

func TestNeedsYouStalledPhaseProjectScope(t *testing.T) {
	srv, db := needsYouServerDB(t)
	stalledFixture(t, db)
	resp := getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-beta")
	for _, it := range resp.Items {
		if it.Kind == needsYouStalledPhase {
			t.Errorf("project=beta lists alpha's stalled phase: %+v", it)
		}
	}
}

func TestNeedsYouMergeStallsIntoApprovals(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	ph := func(n int64) *needsYouPhase { return &needsYouPhase{PhaseID: n} }
	st := func(reason string) *needsYouStall { return &needsYouStall{Stage: "executor", Reason: reason} }
	items := dedupeNeedsYou([]needsYouItem{
		{Kind: needsYouApproval, SessionID: 1, RequestID: id(7)},
		{Kind: needsYouStalledPhase, RequestID: id(7), Phase: ph(70), Stall: st("permission-pending")},
		{Kind: needsYouQuestion, SessionID: 2, RequestID: id(8)},
		{Kind: needsYouStalledPhase, RequestID: id(8), Phase: ph(80), Stall: st("permission-pending")},
		// Its request is no longer listed: stays a stalled_phase item.
		{Kind: needsYouStalledPhase, RequestID: id(9), Phase: ph(90), Stall: st("permission-pending")},
		// Not a permission stall: never merged, even when a request id rides along.
		{Kind: needsYouStalledPhase, Phase: ph(60), Stall: st("no-turns")},
	})
	var got []string
	for _, it := range items {
		s := it.Kind
		if it.Phase != nil {
			s += "@" + strconv.FormatInt(it.Phase.PhaseID, 10)
		}
		got = append(got, s)
	}
	want := "approval@70 question@80 stalled_phase@90 stalled_phase@60"
	if strings.Join(got, " ") != want {
		t.Errorf("merge = %s\nwant    %s", strings.Join(got, " "), want)
	}
}

// stalled_phase ranks just below approval among same-instant blockers.
func TestNeedsYouStalledPhaseRank(t *testing.T) {
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	id := func(n int64) *int64 { return &n }
	items := []needsYouItem{
		{Kind: needsYouQuestion, RequestID: id(2), at: at},
		{Kind: needsYouStalledPhase, Phase: &needsYouPhase{PhaseID: 5}, at: at},
		{Kind: needsYouApproval, RequestID: id(9), at: at},
	}
	sortNeedsYou(items)
	var got []string
	for _, it := range items {
		got = append(got, it.Kind)
	}
	if want := "approval stalled_phase question"; strings.Join(got, " ") != want {
		t.Errorf("order = %s, want %s", strings.Join(got, " "), want)
	}
}
