package api

// Tests for POST /api/board/tasks/{id}/close-no-change (tasks_review.go) — the
// regression for "a card cannot close without a commit": a card that needed no
// change closes as done, and its micro-plan reads Done because the evidence is
// written into its Completion Report.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// seedMicroPlanCard mints a real micro-plan for the card and joins it through
// tasks.workspace_dir, returning the phase doc path.
func seedMicroPlanCard(t *testing.T, db *sql.DB, id int64, extID string) string {
	t.Helper()
	dir, err := taskdir.MintMicroPlan(t.TempDir(), "p", taskdir.Card{
		ExternalID: extID, Title: "review me", Prompt: "do the thing",
	}, time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET workspace_dir=? WHERE id=?`, dir, id); err != nil {
		t.Fatal(err)
	}
	return taskdir.PhaseDocPath(dir)
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCloseNoChange_ClosesTheCardAndTicksItsMicroPlan(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc1", reviewCard{})
	doc := seedMicroPlanCard(t, db, id, "T-nc1")

	resp, body := postReview(t, srv.URL, id, "close-no-change",
		`{"reason":"already_fixed","evidence":"HEAD already guards it at auth.go:42.\nSee commit abc123."}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if col := taskRow(t, db, id, "board_column"); col != "done" {
		t.Errorf("board_column = %q, want done", col)
	}
	if st := taskRow(t, db, id, "status"); st != "done" {
		t.Errorf("status = %q, want done", st)
	}
	note := taskRow(t, db, id, "result_note")
	if !strings.HasPrefix(note, "NO CHANGE (already_fixed)") {
		t.Errorf("result_note = %q, want the NO CHANGE (already_fixed) prefix", note)
	}
	if strings.Contains(note, "See commit") {
		t.Errorf("result_note = %q, want only the evidence's first line", note)
	}
	if taskRow(t, db, id, "finished_at") == "" {
		t.Error("finished_at not stamped")
	}
	if body["boardColumn"] != "done" {
		t.Errorf("response boardColumn = %v, want done", body["boardColumn"])
	}

	got := readFileString(t, doc)
	if strings.Contains(got, "- [ ]") || strings.Count(got, "- [x]") != 2 {
		t.Errorf("want the micro-plan fully ticked:\n%s", got)
	}
	report := got[strings.LastIndex(got, "## Completion Report"):]
	for _, want := range []string{
		"**Outcome: closed without a commit — already_fixed.**",
		"HEAD already guards it at auth.go:42.\nSee commit abc123.",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("Completion Report lacks %q:\n%s", want, got)
		}
	}
}

func TestCloseNoChange_BlankEvidenceIsRefusedAndChangesNothing(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc2", reviewCard{})
	doc := seedMicroPlanCard(t, db, id, "T-nc2")
	before := readFileString(t, doc)

	resp, body := postReview(t, srv.URL, id, "close-no-change", `{"reason":"duplicate","evidence":"   \n "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %v)", resp.StatusCode, body)
	}
	if body["error"] != "evidence is required" {
		t.Errorf("error = %v, want \"evidence is required\"", body["error"])
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("board_column = %q, want in_review (unchanged)", col)
	}
	if note := taskRow(t, db, id, "result_note"); note != "" {
		t.Errorf("result_note = %q, want empty (unchanged)", note)
	}
	if after := readFileString(t, doc); after != before {
		t.Errorf("the micro-plan changed on a refused close:\n%s", after)
	}
}

func TestCloseNoChange_RefusesBadInput(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc3", reviewCard{})

	for name, payload := range map[string]string{
		"unknown reason": `{"reason":"wontfix","evidence":"because"}`,
		"missing reason": `{"evidence":"because"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := postReview(t, srv.URL, id, "close-no-change", payload)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %v)", resp.StatusCode, body)
			}
			if _, ok := body["error"]; !ok {
				t.Errorf("body = %v, want an error field", body)
			}
		})
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_review" {
		t.Errorf("board_column = %q, want in_review (unchanged)", col)
	}
}

func TestCloseNoChange_RunningCardIsAConflict(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc4", reviewCard{BoardColumn: "in_progress", Status: "running"})
	doc := seedMicroPlanCard(t, db, id, "T-nc4")
	before := readFileString(t, doc)

	resp, body := postReview(t, srv.URL, id, "close-no-change", `{"reason":"not_a_defect","evidence":"works as designed"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %v)", resp.StatusCode, body)
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_progress" {
		t.Errorf("board_column = %q, want in_progress (unchanged)", col)
	}
	if after := readFileString(t, doc); after != before {
		t.Errorf("the micro-plan changed on a refused close:\n%s", after)
	}
}

func TestCloseNoChange_NonQueueTaskIsNotFound(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id)
		VALUES (1, 'ws plan', 'goal', 'running', '2026-10-25T00:00:00Z', 'workspace', '2026-10-25-ws-plan')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	resp, body := postReview(t, srv.URL, id, "close-no-change", `{"reason":"duplicate","evidence":"same as T-1"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %v)", resp.StatusCode, body)
	}
	if st := taskRow(t, db, id, "status"); st != "running" {
		t.Errorf("status = %q, want running (unchanged)", st)
	}
	// An unknown id is a 404 too.
	if resp, _ := postReview(t, srv.URL, 999999, "close-no-change", `{"reason":"duplicate","evidence":"x"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", resp.StatusCode)
	}
}

// postCloseNoChange calls the handler directly, so a test can set Handler seams.
func postCloseNoChange(h *Handler, id int64, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/board/tasks/"+strconv.FormatInt(id, 10)+"/close-no-change",
		strings.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	rec := httptest.NewRecorder()
	h.closeNoChangeBoardTask(rec, req)
	return rec
}

// R1-4: the dispatcher picks the card up between the running check and the
// guarded write. The close is a 409 and the micro-plan's Completion Report stays
// blank — no evidence is left behind for a close that never happened.
func TestCloseNoChange_RaceToRunningLeavesTheReportBlank(t *testing.T) {
	_, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc9", reviewCard{})
	doc := seedMicroPlanCard(t, db, id, "T-nc9")
	h := &Handler{DB: db}
	h.closeNoChangeHook = func() {
		if _, err := db.Exec(`UPDATE tasks SET status='running', board_column='in_progress' WHERE id=?`, id); err != nil {
			t.Error(err)
		}
	}

	rec := postCloseNoChange(h, id, `{"reason":"already_fixed","evidence":"guarded at auth.go:42"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	if blank, err := wsingest.CompletionReportBlank(doc); err != nil || !blank {
		t.Fatalf("Completion Report blank = %v (%v), want true:\n%s", blank, err, readFileString(t, doc))
	}
	if col := taskRow(t, db, id, "board_column"); col != "in_progress" {
		t.Errorf("board_column = %q, want in_progress (the dispatcher's)", col)
	}
	if note := taskRow(t, db, id, "result_note"); note != "" {
		t.Errorf("result_note = %q, want empty", note)
	}
}

// R1-4: a Completion Report write that fails after the card moved puts the card
// back where it was and answers 500 — never a done card with an empty record.
func TestCloseNoChange_ReportWriteFailureRestoresTheCard(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only mode this test relies on")
	}
	_, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc10", reviewCard{})
	doc := seedMicroPlanCard(t, db, id, "T-nc10")
	if _, err := db.Exec(`UPDATE tasks SET result_note='prior note', column_moved_at='2026-10-24T08:00:00Z' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	cols := []string{"board_column", "status", "result_note", "finished_at", "column_moved_at"}
	for _, c := range cols {
		before[c] = taskRow(t, db, id, c)
	}
	if err := os.Chmod(doc, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(doc, 0o644) })

	rec := postCloseNoChange(&Handler{DB: db}, id, `{"reason":"duplicate","evidence":"same as T-1"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body)
	}
	for _, c := range cols {
		if got := taskRow(t, db, id, c); got != before[c] {
			t.Errorf("%s = %q, want %q (restored)", c, got, before[c])
		}
	}
}

// A card dispatched before micro-plans (or with minting off) still closes, and its
// result_note carries the evidence.
func TestCloseNoChange_WithoutAMicroPlanStillCloses(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc5", reviewCard{})

	resp, body := postReview(t, srv.URL, id, "close-no-change", `{"reason":"duplicate","evidence":"same as T-12"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if note := taskRow(t, db, id, "result_note"); note != "NO CHANGE (duplicate): same as T-12" {
		t.Errorf("result_note = %q", note)
	}
}

// Evidence is capped, so a pasted log cannot balloon the workspace doc.
func TestCloseNoChange_CapsEvidence(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc6", reviewCard{})
	doc := seedMicroPlanCard(t, db, id, "T-nc6")

	long := strings.Repeat("é", 6000) // 12000 bytes
	resp, body := postReview(t, srv.URL, id, "close-no-change", `{"reason":"already_fixed","evidence":"`+long+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	got := readFileString(t, doc)
	if n := strings.Count(got, "é"); n != noChangeEvidenceMax/2 {
		t.Errorf("evidence runes in doc = %d, want %d (cut at %d bytes on a rune boundary)",
			n, noChangeEvidenceMax/2, noChangeEvidenceMax)
	}
}

// A drag to done over a blank Completion Report must NOT tick the micro-plan: the
// board column is not evidence, and criterion 1's no-change arm needs words.
func TestPatchDone_BlankReportDoesNotTickTheMicroPlan(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc7", reviewCard{})
	doc := seedMicroPlanCard(t, db, id, "T-nc7")
	before := readFileString(t, doc)

	resp := patchBoard(t, srv.URL, id, `{"boardColumn":"done"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH to done = %d, want 200", resp.StatusCode)
	}
	if col := taskRow(t, db, id, "board_column"); col != "done" {
		t.Fatalf("board_column = %q, want done", col)
	}
	if after := readFileString(t, doc); after != before {
		t.Errorf("a blank-report drag to done changed the micro-plan:\n%s", after)
	}
}

// …while the same drag over a report that carries evidence closes the micro-plan,
// through the shared applyTerminalColumnEffects hook.
func TestPatchDone_WrittenReportTicksTheMicroPlan(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	id := seedReviewCard(t, db, "T-nc8", reviewCard{})
	doc := seedMicroPlanCard(t, db, id, "T-nc8")
	if err := os.WriteFile(doc, []byte(readFileString(t, doc)+"\nShipped on swarm/T-nc8.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := patchBoard(t, srv.URL, id, `{"boardColumn":"done"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH to done = %d, want 200", resp.StatusCode)
	}
	if got := readFileString(t, doc); strings.Contains(got, "- [ ]") {
		t.Errorf("want the micro-plan ticked once the report has content:\n%s", got)
	}
}
