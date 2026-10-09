package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reopenTestDoc = "# Phase 1\n\n## Acceptance Criteria\n" +
	"- [x] **POST creates a line item**\n" +
	"- [x] DELETE removes it\n" +
	"- [ ] docs updated\n" +
	"```\n- [x] DELETE removes it\n```\n"

type reopenFixture struct {
	srv             string
	db              *sql.DB
	taskID, phaseID int64
	docPath         string
}

func newReopenFixture(t *testing.T, runState string) reopenFixture {
	t.Helper()
	srv, db := reviewServer(t, t.TempDir())
	prev := phaserunSvc
	phaserunSvc = nil
	t.Cleanup(func() { phaserunSvc = prev })
	docPath := filepath.Join(t.TempDir(), "phase-1.md")
	if err := os.WriteFile(docPath, []byte(reopenTestDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (1, 'Orders', 'goal', 'running', '2026-10-09T00:00:00Z', '2026-10-09T00:00:00Z', 'workspace', '2026-10-09-orders')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, checkboxes_total, checkboxes_done, run_state)
		VALUES (?, 1, 'Phase 1', ?, 3, 3, ?)`, taskID, docPath, runState)
	if err != nil {
		t.Fatal(err)
	}
	phaseID, _ := res.LastInsertId()
	return reopenFixture{srv: srv.URL, db: db, taskID: taskID, phaseID: phaseID, docPath: docPath}
}

func (f reopenFixture) url(taskID, phaseID int64, suffix string) string {
	return fmt.Sprintf("%s/api/epics/%d/phases/%d/%s", f.srv, taskID, phaseID, suffix)
}

func (f reopenFixture) post(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	return f.postTo(t, f.url(f.taskID, f.phaseID, "reopen"), body)
}

func (f reopenFixture) postTo(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func (f reopenFixture) count(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM phase_reopens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f reopenFixture) doc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.docPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReopenPhaseWritesLedgerAndUnticks(t *testing.T) {
	f := newReopenFixture(t, "done")
	status, body := f.post(t, map[string]any{
		"reason": "  delete left orphans  ", "fixUrl": "https://example.test/pr/9", "caughtBy": "operator",
		"criteria": []string{"DELETE removes it", "no such criterion"},
	})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body %v", status, body)
	}
	if body["unticked"] != float64(1) {
		t.Errorf("unticked = %v", body["unticked"])
	}
	doc := f.doc(t)
	if !strings.Contains(doc, "- [ ] DELETE removes it\n") || !strings.Contains(doc, "- [x] **POST creates") {
		t.Errorf("doc after reopen:\n%s", doc)
	}
	if !strings.Contains(doc, "```\n- [x] DELETE removes it\n```") {
		t.Errorf("fenced example was rewritten:\n%s", doc)
	}
	var (
		phaseID                                      int64
		wsTask, docPath, reason, fix, caught, labels string
	)
	if err := f.db.QueryRow(`SELECT phase_id, workspace_task_id, doc_path, reason, fix_url, caught_by, criteria_json FROM phase_reopens`).
		Scan(&phaseID, &wsTask, &docPath, &reason, &fix, &caught, &labels); err != nil {
		t.Fatal(err)
	}
	if phaseID != f.phaseID || wsTask != fmt.Sprint(f.taskID) || docPath != f.docPath || reason != "delete left orphans" ||
		fix != "https://example.test/pr/9" || caught != "operator" || labels != `["DELETE removes it"]` {
		t.Errorf("row = %d %s %s %q %q %q %s", phaseID, wsTask, docPath, reason, fix, caught, labels)
	}

	// The history endpoint and the epic DTO both carry it.
	resp, err := http.Get(f.url(f.taskID, f.phaseID, "reopens"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Reopens []reopenDTO `json:"reopens"`
		Ticked  []string    `json:"ticked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Reopens) != 1 || list.Reopens[0].CaughtBy != "operator" || list.Reopens[0].Criteria[0] != "DELETE removes it" {
		t.Errorf("reopens = %+v", list.Reopens)
	}
	if len(list.Ticked) != 1 || list.Ticked[0] != "POST creates a line item" {
		t.Errorf("ticked = %q", list.Ticked)
	}

	h := &Handler{DB: f.db}
	phases, _, _, err := h.epicPhases(f.taskID, filepath.Dir(f.docPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 1 || len(phases[0].Reopens) != 1 || phases[0].Reopens[0].Reason != "delete left orphans" {
		t.Errorf("epic DTO reopens = %+v", phases)
	}
}

func TestReopenPhaseRefusals(t *testing.T) {
	f := newReopenFixture(t, "done")
	ok := map[string]any{"reason": "r", "caughtBy": "review", "criteria": []string{"DELETE removes it"}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range ok {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	cases := []struct {
		name string
		url  string
		body any
		want int
	}{
		{"empty reason", "", with("reason", "   "), http.StatusBadRequest},
		{"no criteria", "", with("criteria", []string{}), http.StatusBadRequest},
		{"blank criteria", "", with("criteria", []string{" "}), http.StatusBadRequest},
		{"unknown caughtBy", "", with("caughtBy", "luck"), http.StatusBadRequest},
		{"javascript fixUrl", "", with("fixUrl", "javascript:alert(1)"), http.StatusBadRequest},
		{"relative fixUrl", "", with("fixUrl", "pulls/9"), http.StatusBadRequest},
		{"bad json", "", "nope", http.StatusBadRequest},
		{"unknown phase", f.url(f.taskID, 99999, "reopen"), ok, http.StatusNotFound},
		{"phase of another plan", f.url(f.taskID+1, f.phaseID, "reopen"), ok, http.StatusNotFound},
		{"no matching label", "", with("criteria", []string{"docs updated", "made up"}), http.StatusUnprocessableEntity},
	}
	before := f.doc(t)
	for _, c := range cases {
		url := c.url
		if url == "" {
			url = f.url(f.taskID, f.phaseID, "reopen")
		}
		if status, body := f.postTo(t, url, c.body); status != c.want {
			t.Errorf("%s: status = %d, want %d (%v)", c.name, status, c.want, body)
		}
	}
	if n := f.count(t); n != 0 {
		t.Errorf("refusals wrote %d ledger rows", n)
	}
	if f.doc(t) != before {
		t.Error("a refusal rewrote the doc")
	}
}

func TestReopenPhaseRunning(t *testing.T) {
	f := newReopenFixture(t, "running")
	status, body := f.post(t, map[string]any{"reason": "r", "caughtBy": "verifier", "criteria": []string{"DELETE removes it"}})
	if status != http.StatusConflict || body["code"] != codePhaseRunning {
		t.Fatalf("status = %d, body %v; want 409 %s", status, body, codePhaseRunning)
	}
	if f.count(t) != 0 {
		t.Error("a running phase got a ledger row")
	}
}

func TestReopenPhaseMissingDoc(t *testing.T) {
	f := newReopenFixture(t, "done")
	if err := os.Remove(f.docPath); err != nil {
		t.Fatal(err)
	}
	status, _ := f.post(t, map[string]any{"reason": "r", "caughtBy": "none", "criteria": []string{"DELETE removes it"}})
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
	resp, err := http.Get(f.url(f.taskID, f.phaseID, "reopens"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list map[string][]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if resp.StatusCode != http.StatusOK || list["ticked"] == nil || len(list["ticked"]) != 0 {
		t.Errorf("GET reopens without a doc = %d %v", resp.StatusCode, list)
	}
}

// TestPhaseReopensMatchesRenamedPhase: a row whose phase id is gone is found
// by its doc path; one whose doc is gone too is dropped.
func TestPhaseReopensMatchesRenamedPhase(t *testing.T) {
	f := newReopenFixture(t, "done")
	if _, err := f.db.Exec(`INSERT INTO phase_reopens (phase_id, workspace_task_id, doc_path, reason, caught_by, criteria_json, created_at)
		VALUES (424242, ?, ?, 'old id', 'none', 'not json', '2026-10-01T00:00:00Z'),
		       (424243, ?, '/gone.md', 'gone', 'none', '[]', '2026-10-01T00:00:00Z')`, f.taskID, f.docPath, f.taskID); err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: f.db}
	got := h.phaseReopens(f.taskID, map[int64]string{f.phaseID: f.docPath})
	if len(got[f.phaseID]) != 1 || got[f.phaseID][0].Reason != "old id" || len(got[f.phaseID][0].Criteria) != 0 {
		t.Errorf("reopens = %+v", got)
	}
}

// A failed ledger insert must not untick the doc: the row is written first,
// inside a transaction, and the doc only after it — so a retry still finds the
// criteria ticked instead of answering 422 over a reopen that was never
// recorded.
func TestReopenPhaseInsertFailureLeavesDocUntouched(t *testing.T) {
	f := newReopenFixture(t, "done")
	before := f.doc(t)
	if _, err := f.db.Exec(`DROP TABLE phase_reopens`); err != nil {
		t.Fatal(err)
	}
	status, body := f.post(t, map[string]any{"reason": "r", "caughtBy": "operator", "criteria": []string{"DELETE removes it"}})
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, body %v; want 500", status, body)
	}
	if f.doc(t) != before {
		t.Errorf("doc was unticked although the ledger insert failed:\n%s", f.doc(t))
	}
}

// Only a finished phase is reopened: one with an unticked criterion is still
// open and gets 409 phase-not-done, with nothing written.
func TestReopenPhaseRefusesNotDone(t *testing.T) {
	f := newReopenFixture(t, "done")
	if _, err := f.db.Exec(`UPDATE epic_phases SET checkboxes_done = 2 WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	before := f.doc(t)
	status, body := f.post(t, map[string]any{"reason": "r", "caughtBy": "none", "criteria": []string{"DELETE removes it"}})
	if status != http.StatusConflict || body["code"] != codePhaseNotDone {
		t.Fatalf("status = %d, body %v; want 409 %s", status, body, codePhaseNotDone)
	}
	if f.count(t) != 0 || f.doc(t) != before {
		t.Error("a not-done phase was written to")
	}
}

// A label that matches two ticked lines is refused (422) rather than unticking
// both: the ledger would record one reopen for two criteria the operator did
// not both choose.
func TestReopenPhaseRefusesAmbiguousLabel(t *testing.T) {
	f := newReopenFixture(t, "done")
	if err := os.WriteFile(f.docPath, []byte(reopenTestDoc+"- [x] DELETE removes it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := f.doc(t)
	status, body := f.post(t, map[string]any{"reason": "r", "caughtBy": "review", "criteria": []string{"DELETE removes it"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(body["error"]), "more than one") {
		t.Fatalf("status = %d, body %v; want 422 naming the ambiguity", status, body)
	}
	if f.count(t) != 0 || f.doc(t) != before {
		t.Error("an ambiguous reopen was written")
	}
}
