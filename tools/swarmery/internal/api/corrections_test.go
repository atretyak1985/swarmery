package api

// memory-engineering phase 3 — the operator correction ledger over HTTP: each
// of the six operator endpoints writes exactly one operator_corrections row
// (and the two "nothing actually changed" cases write none), and
// GET /api/corrections reads the window back as groups + rows.

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/corrections"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrev"
)

// ledgerRows returns the ledger rows of one source, newest first.
func ledgerRows(t *testing.T, db *sql.DB, source string) []corrections.Row {
	t.Helper()
	rows, err := db.Query(`SELECT ref, COALESCE(project_id, -1), before, after, reason, norm_key
		FROM operator_corrections WHERE source = ? ORDER BY id DESC`, source)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []corrections.Row
	for rows.Next() {
		var r corrections.Row
		var pid int64
		if err := rows.Scan(&r.Ref, &pid, &r.Before, &r.After, &r.Reason, &r.NormKey); err != nil {
			t.Fatal(err)
		}
		if pid >= 0 {
			r.ProjectID = &pid
		}
		r.Source = source
		out = append(out, r)
	}
	return out
}

func ledgerCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_corrections`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Lesson edit / dismiss / retire: one row each; an edit that changes neither
// the title nor the guidance (area globs only, or the same title again)
// writes none; a refused edit writes none.
func TestCorrectionLedgerLessons(t *testing.T) {
	srv, db := testServerWithDB(t)
	ins := func(uuid, title, norm string) int64 {
		res, err := db.Exec(`INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title,
			guidance, area_globs, evidence_json, status, created_at, updated_at)
			VALUES (?, 1, 1, ?, ?, 'Do it.', 'internal/**', '["divergence:1"]', 'candidate', 'now', 'now')`, uuid, title, norm)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	a := ins("a", "Pin the model", "pin model")
	b := ins("b", "Other", "other")
	url := func(id int64, action string) string {
		u := srv.URL + "/api/lessons/" + itoa64(id)
		if action != "" {
			u += "/" + action
		}
		return u
	}
	do := func(method, u, body string, want int) {
		t.Helper()
		if r := decisionReq(t, method, u, body); r.StatusCode != want {
			t.Fatalf("%s %s %s = %d, want %d", method, u, body, r.StatusCode, want)
		}
	}

	// No textual change: area globs only, then the same title again.
	do(http.MethodPatch, url(a, ""), `{"areaGlobs":["internal/api/**"]}`, 200)
	do(http.MethodPatch, url(a, ""), `{"title":"Pin the model"}`, 200)
	if n := ledgerCount(t, db); n != 0 {
		t.Fatalf("rows after no-change edits = %d, want 0", n)
	}
	// A refused edit (guidance with a newline) writes none either.
	do(http.MethodPatch, url(a, ""), `{"guidance":"one\ntwo"}`, 400)

	do(http.MethodPatch, url(a, ""), `{"title":"Pin the model in every run","guidance":"Always."}`, 200)
	edits := ledgerRows(t, db, corrections.SourceLessonEdit)
	if len(edits) != 1 {
		t.Fatalf("lesson_edit rows = %d, want 1", len(edits))
	}
	if e := edits[0]; e.Ref != "lesson:"+itoa64(a) || e.Before != "Pin the model\nDo it." ||
		e.After != "Pin the model in every run\nAlways." || e.NormKey != "pin model every run always" || e.ProjectID != nil {
		t.Errorf("lesson_edit row = %+v", e)
	}

	do(http.MethodPost, url(b, "dismiss"), `{"reason":"one-off"}`, 200)
	dis := ledgerRows(t, db, corrections.SourceLessonDismiss)
	if len(dis) != 1 || dis[0].Ref != "lesson:"+itoa64(b) || dis[0].Before != "Other" ||
		dis[0].Reason != "one-off" || dis[0].NormKey != "one off" {
		t.Errorf("lesson_dismiss rows = %+v", dis)
	}
	// A repeat is a 409 and writes nothing.
	do(http.MethodPost, url(b, "dismiss"), `{"reason":"again"}`, 409)

	do(http.MethodPost, url(a, "accept"), ``, 200)
	do(http.MethodPost, url(a, "retire"), `{"reason":"superseded by the skill"}`, 200)
	ret := ledgerRows(t, db, corrections.SourceLessonRetire)
	if len(ret) != 1 || ret[0].Ref != "lesson:"+itoa64(a) || ret[0].Before != "Pin the model in every run" ||
		ret[0].NormKey != "superseded by skill" {
		t.Errorf("lesson_retire rows = %+v", ret)
	}
	if n := ledgerCount(t, db); n != 3 {
		t.Fatalf("total rows = %d, want 3", n)
	}
}

// Undoing an applied triage verdict writes one row: before = the verdict's
// value, after = its prior, the item's project carried over; the 409 repeat
// writes none.
func TestCorrectionLedgerTriageUndo(t *testing.T) {
	srv, svc, _ := serverWithTriage(t)
	if resp, body := doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", map[string]any{}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start = %d %s", resp.StatusCode, body)
	}
	resp, body := doRoutineReq(t, http.MethodGet, srv+"/api/triage/verdicts", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verdicts = %d %s", resp.StatusCode, body)
	}
	var out struct {
		Items []struct {
			ID    int64  `json:"id"`
			Value string `json:"value"`
		} `json:"items"`
	}
	decodeInto(t, body, &out)
	if len(out.Items) != 1 {
		t.Fatalf("verdicts = %+v", out.Items)
	}
	vid := out.Items[0].ID
	if resp, body := doRoutineReq(t, http.MethodPost, fmt.Sprintf("%s/api/triage/verdicts/%d/undo", srv, vid), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("undo = %d %s", resp.StatusCode, body)
	}
	rows := ledgerRows(t, svc.DB, corrections.SourceTriageUndo)
	if len(rows) != 1 {
		t.Fatalf("triage_undo rows = %d, want 1", len(rows))
	}
	if r := rows[0]; r.Ref != fmt.Sprintf("verdict:%d", vid) || r.Before != "noise" || r.After != `{"state":"open"}` ||
		r.Reason != "undo friction/noise" || r.NormKey != "undo friction noise" || r.ProjectID != nil {
		t.Errorf("triage_undo row = %+v", r)
	}
	if resp, _ := doRoutineReq(t, http.MethodPost, fmt.Sprintf("%s/api/triage/verdicts/%d/undo", srv, vid), nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second undo = %d", resp.StatusCode)
	}
	if n := ledgerCount(t, svc.DB); n != 1 {
		t.Fatalf("rows after the refused undo = %d, want 1", n)
	}
}

// Rejecting a staged revision writes one row with the note as the reason and
// the revision's purpose as `before`; the 409 repeat writes none.
func TestCorrectionLedgerRevisionReject(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	id := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "a.md", Action: planrev.ActionCreate, Proposed: "x"},
	})
	resp := postRevJSON(t, srv.URL+"/api/revisions/"+itoa(id)+"/reject", `{"note":"wrong direction"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d", resp.StatusCode)
	}
	rows := ledgerRows(t, db, corrections.SourceRevisionReject)
	if len(rows) != 1 {
		t.Fatalf("revision_reject rows = %d, want 1", len(rows))
	}
	if r := rows[0]; r.Ref != "revision:"+itoa(id) || r.Before != "tighten the plan" || r.Reason != "wrong direction" ||
		r.NormKey != "wrong direction" {
		t.Errorf("revision_reject row = %+v", r)
	}
	resp = postRevJSON(t, srv.URL+"/api/revisions/"+itoa(id)+"/reject", `{}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-reject = %d, want 409", resp.StatusCode)
	}
	if n := ledgerCount(t, db); n != 1 {
		t.Fatalf("rows after the refused reject = %d, want 1", n)
	}
}

// Ground truth equal to the agent's answer is a label (no row); a different
// truth is a correction (one row keyed on question + answer -> truth); an
// off-option value is refused and writes none.
func TestCorrectionLedgerTruthDisagree(t *testing.T) {
	srv, db := testServerWithDB(t)
	for i, answer := range []string{"refactor", "refactor"} {
		if _, err := db.Exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, error, backend, created_at)
			VALUES ('d2.task_type', 's', 's-c', 'h', ?, 0.8, '', 'local', ?)`, answer, fmt.Sprintf("2026-10-07T10:00:0%dZ", i)); err != nil {
			t.Fatal(err)
		}
	}
	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/1/ground-truth", `{"value":"refactor"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("agreeing truth = %d, want 204", r.StatusCode)
	}
	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/2/ground-truth", `{"value":"a-typo"}`); r.StatusCode != 400 {
		t.Fatalf("off-option truth = %d, want 400", r.StatusCode)
	}
	if n := ledgerCount(t, db); n != 0 {
		t.Fatalf("rows after an agreeing + a refused truth = %d, want 0", n)
	}
	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/2/ground-truth", `{"value":"bugfix"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("disagreeing truth = %d, want 204", r.StatusCode)
	}
	rows := ledgerRows(t, db, corrections.SourceTruthDisagree)
	if len(rows) != 1 {
		t.Fatalf("truth_disagree rows = %d, want 1", len(rows))
	}
	if r := rows[0]; r.Ref != "decision:2" || r.Before != "refactor" || r.After != "bugfix" ||
		r.Reason != "d2.task_type: refactor -> bugfix" || r.NormKey != "d2 task type refactor bugfix" {
		t.Errorf("truth_disagree row = %+v", r)
	}
}

// GET /api/corrections folds the window into groups and lists the rows;
// window/limit are honoured, and an empty ledger is {groups:[], rows:[]}.
func TestCorrectionsEndpoint(t *testing.T) {
	srv, db := testServerWithDB(t)
	var empty struct {
		Groups []corrections.Group `json:"groups"`
		Rows   []corrections.Row   `json:"rows"`
	}
	getJSON(t, srv.URL+"/api/corrections", &empty)
	if empty.Groups == nil || empty.Rows == nil || len(empty.Groups)+len(empty.Rows) != 0 {
		t.Fatalf("empty ledger = %+v, want empty (non-null) arrays", empty)
	}

	ins := func(source, ref, reason, key, at string) {
		if _, err := db.Exec(`INSERT INTO operator_corrections (source, ref, before, after, reason, norm_key, created_at)
			VALUES (?, ?, 'b', 'a', ?, ?, ?)`, source, ref, reason, key, at); err != nil {
			t.Fatal(err)
		}
	}
	// Two inside the 14-day window under one key, one 30-day-old row under
	// another (outside 14d, inside 365d).
	ins("lesson_edit", "lesson:1", "pin the model", "pin model", "2099-01-01T00:00:00.000Z")
	ins("lesson_dismiss", "lesson:2", "Pin the model!", "pin model", "2099-01-02T00:00:00.000Z")
	ins("revision_reject", "revision:5", "wrong direction", "wrong direction",
		time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02T15:04:05.000Z"))

	var got struct {
		Groups []corrections.Group `json:"groups"`
		Rows   []corrections.Row   `json:"rows"`
	}
	getJSON(t, srv.URL+"/api/corrections?window=14d", &got)
	if len(got.Groups) != 1 || got.Groups[0].NormKey != "pin model" || got.Groups[0].Count != 2 ||
		len(got.Groups[0].Refs) != 2 || got.Groups[0].Sample != "Pin the model!" {
		t.Fatalf("groups = %+v", got.Groups)
	}
	if len(got.Rows) != 2 || got.Rows[0].Ref != "lesson:2" || got.Rows[1].Ref != "lesson:1" {
		t.Fatalf("rows = %+v", got.Rows)
	}
	getJSON(t, srv.URL+"/api/corrections?window=14d&limit=1", &got)
	if len(got.Rows) != 1 || len(got.Groups) != 1 {
		t.Fatalf("limit=1: rows = %d, groups = %d", len(got.Rows), len(got.Groups))
	}
	// A window wide enough reaches the old row; a bogus window is the default.
	getJSON(t, srv.URL+"/api/corrections?window=365d", &got)
	if len(got.Groups) != 2 || len(got.Rows) != 3 {
		t.Fatalf("365d: groups = %d, rows = %d; want 2/3", len(got.Groups), len(got.Rows))
	}
	getJSON(t, srv.URL+"/api/corrections?window=bogus", &got)
	if len(got.Rows) != 2 {
		t.Fatalf("bogus window: rows = %d, want the 14-day default's 2", len(got.Rows))
	}
}
