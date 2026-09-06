package api

import (
	"database/sql"
	"testing"
)

// Pending-approval counting for the board's "needs me" filter (board redesign v2
// phase 5). permission_requests carries no task_id, so every case here is really
// a claim about WHICH session link is the right one — see pendingApprovalPairs.

// seedSession inserts a sessions row with a known uuid.
func seedSession(t *testing.T, db *sql.DB, id int64, uuid string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, session_uuid, status, started_at)
		 VALUES (?, 1, ?, 'running', '2026-09-01T00:00:00Z')`, id, uuid); err != nil {
		t.Fatal(err)
	}
}

// seedApproval inserts one permission_requests row in the given status.
func seedApproval(t *testing.T, db *sql.DB, id, sessionID int64, status string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO permission_requests (id, session_id, tool_name, request_json, status, requested_at)
		 VALUES (?, ?, 'Bash', '{}', ?, '2026-09-01T00:01:00Z')`, id, sessionID, status); err != nil {
		t.Fatal(err)
	}
}

func linkTaskSession(t *testing.T, db *sql.DB, taskID, sessionID int64, source string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO task_sessions (task_id, session_id, link_source, confidence) VALUES (?, ?, ?, 1.0)`,
		taskID, sessionID, source); err != nil {
		t.Fatal(err)
	}
}

// boardCountByID reads the whole board and returns one card's pendingApprovalCount,
// so every assertion below goes through the SAME path the SPA does.
func boardCountByID(t *testing.T, url string, id int64) int {
	t.Helper()
	var list []boardTaskDTO
	getJSON(t, url+"/api/board/tasks?projectId=1", &list)
	for _, d := range list {
		if d.ID == id {
			return d.PendingApprovalCount
		}
	}
	t.Fatalf("card %d missing from the board list", id)
	return -1
}

// TestPendingApprovalCountsThroughTheRunSession covers each way a card can reach
// an unanswered approval, and each way it must NOT.
func TestPendingApprovalCountsThroughTheRunSession(t *testing.T) {
	srv, db := testServerWithDB(t)
	defer srv.Close()

	// A card whose stage has exited: the explicit link is reconciled.
	linked := createdBoardTask(t, srv.URL, "linked")
	seedSession(t, db, 101, "uuid-linked")
	linkTaskSession(t, db, linked, 101, "explicit")
	seedApproval(t, db, 1001, 101, "pending")

	// A card mid-run: dispatch parked the uuid before spawning, and linkSession
	// has NOT run because the process has not exited — which is precisely the
	// state a card is in while it waits on an approval. Counting only through
	// task_sessions would report 0 for this card forever.
	live := createdBoardTask(t, srv.URL, "live")
	seedSession(t, db, 102, "uuid-live")
	if _, err := db.Exec(`UPDATE tasks SET dispatch_session_uuid = 'uuid-live' WHERE id = ?`, live); err != nil {
		t.Fatal(err)
	}
	seedApproval(t, db, 1002, 102, "pending")

	// A multi-stage playbook: tasks.session_id is COALESCE-pinned to stage 1, and
	// the approval is pending in stage 2. This is the case that rules
	// tasks.session_id out as the join column.
	staged := createdBoardTask(t, srv.URL, "staged")
	seedSession(t, db, 103, "uuid-stage1")
	seedSession(t, db, 104, "uuid-stage2")
	linkTaskSession(t, db, staged, 103, "explicit")
	linkTaskSession(t, db, staged, 104, "explicit")
	if _, err := db.Exec(
		`UPDATE tasks SET session_id = 103, dispatch_session_uuid = 'uuid-stage1' WHERE id = ?`, staged); err != nil {
		t.Fatal(err)
	}
	seedApproval(t, db, 1003, 104, "pending")

	// A heuristic link is a cwd/time guess (96.3% of live links are). Counting it
	// would charge someone else's unanswered approval to this card.
	guessed := createdBoardTask(t, srv.URL, "guessed")
	seedSession(t, db, 105, "uuid-guessed")
	linkTaskSession(t, db, guessed, 105, "heuristic")
	seedApproval(t, db, 1005, 105, "pending")

	// Resolved requests are not waiting on anybody.
	resolved := createdBoardTask(t, srv.URL, "resolved")
	seedSession(t, db, 106, "uuid-resolved")
	linkTaskSession(t, db, resolved, 106, "explicit")
	seedApproval(t, db, 1006, 106, "approved")
	seedApproval(t, db, 1007, 106, "denied")
	seedApproval(t, db, 1008, 106, "expired")

	// A card captured FROM a live session: origin_session_id is provenance, not a
	// run. That session's approvals belong to whoever is sitting in it.
	captured := createdBoardTask(t, srv.URL, "captured")
	seedSession(t, db, 107, "uuid-capture")
	if _, err := db.Exec(`UPDATE tasks SET origin_session_id = 107 WHERE id = ?`, captured); err != nil {
		t.Fatal(err)
	}
	seedApproval(t, db, 1009, 107, "pending")

	// Never dispatched, no session of any kind.
	idle := createdBoardTask(t, srv.URL, "idle")

	for _, tc := range []struct {
		name string
		id   int64
		want int
	}{
		{"explicit link", linked, 1},
		{"live run, uuid parked only", live, 1},
		{"stage 2 of a multi-stage run", staged, 1},
		{"heuristic link", guessed, 0},
		{"only resolved requests", resolved, 0},
		{"capture session, not a run", captured, 0},
		{"never dispatched", idle, 0},
	} {
		if got := boardCountByID(t, srv.URL, tc.id); got != tc.want {
			t.Errorf("%s: pendingApprovalCount = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestPendingApprovalCountsEachRequestOnce: once a live run's transcript is
// ingested, stage 1 satisfies BOTH arms of the union. The request is still one
// request, and the count the filter reads must say so.
func TestPendingApprovalCountsEachRequestOnce(t *testing.T) {
	srv, db := testServerWithDB(t)
	defer srv.Close()

	id := createdBoardTask(t, srv.URL, "both arms")
	seedSession(t, db, 201, "uuid-both")
	linkTaskSession(t, db, id, 201, "explicit")
	if _, err := db.Exec(`UPDATE tasks SET dispatch_session_uuid = 'uuid-both' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	seedApproval(t, db, 2001, 201, "pending")

	if got := boardCountByID(t, srv.URL, id); got != 1 {
		t.Errorf("pendingApprovalCount = %d, want 1 — the union must dedupe on the request", got)
	}

	// Two distinct requests on the same session are two.
	seedApproval(t, db, 2002, 201, "pending")
	if got := boardCountByID(t, srv.URL, id); got != 2 {
		t.Errorf("pendingApprovalCount = %d, want 2", got)
	}
}

// TestPendingApprovalCountOnSingleCardRead: the POST/PATCH response and the
// task_updated WS payload replace the client's row wholesale, so the single-card
// read must carry the same number as the list — otherwise any edit would blank
// the filter's fourth disjunct until the next reconcile.
func TestPendingApprovalCountOnSingleCardRead(t *testing.T) {
	srv, db := testServerWithDB(t)
	defer srv.Close()

	id := createdBoardTask(t, srv.URL, "one card")
	seedSession(t, db, 301, "uuid-one")
	linkTaskSession(t, db, id, 301, "explicit")
	seedApproval(t, db, 3001, 301, "pending")

	h := &Handler{DB: db}
	d, err := h.boardTaskByID(id)
	if err != nil || d == nil {
		t.Fatalf("boardTaskByID = %v, %v", d, err)
	}
	if d.PendingApprovalCount != 1 {
		t.Errorf("single-card pendingApprovalCount = %d, want 1", d.PendingApprovalCount)
	}
	if got := boardCountByID(t, srv.URL, id); got != d.PendingApprovalCount {
		t.Errorf("list says %d, single-card read says %d — the two reads must agree", got, d.PendingApprovalCount)
	}
}
