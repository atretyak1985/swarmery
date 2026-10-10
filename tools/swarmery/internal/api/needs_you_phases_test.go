package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// manualPhaseFixture plants one plan per project plus phases on both sides of
// the manual_phase rule, on top of needsYouServerDB's session fixture.
func manualPhaseFixture(t *testing.T, db *sql.DB) (alphaPhase, betaPhase int64) {
	t.Helper()
	now := time.Now().UTC()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	exec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
		return res
	}
	task := func(project int64, ext, title string, archived any) int64 {
		res := exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id, archived_at)
			VALUES (?, ?, 'goal', 'running', ?, ?, 'workspace', ?, ?)`, project, title, ago(96*time.Hour), ago(96*time.Hour), ext, archived)
		id, _ := res.LastInsertId()
		return id
	}
	// phase inserts a phase with total/done criteria and manualOpen open [MANUAL] ones.
	phase := func(taskID int64, seq, total, done, manualOpen, landOpen int, docStatus any, updated string) int64 {
		res := exec(`INSERT INTO epic_phases
			(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
			 criteria_manual_open, criteria_land_open, doc_status, doc_updated_at)
			VALUES (?, ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?)`,
			taskID, seq, "Phase name "+string(rune('0'+seq)), "/ws/"+string(rune('a'+taskID))+"/phase-"+string(rune('0'+seq))+".md",
			total, done, manualOpen, landOpen, docStatus, updated)
		id, _ := res.LastInsertId()
		return id
	}

	alpha := task(1, "2026-10-01-alpha-plan", "Alpha plan", nil)
	alphaPhase = phase(alpha, 1, 4, 2, 2, 0, "in_progress", ago(3*time.Hour)) // only [MANUAL] open → listed
	phase(alpha, 2, 4, 1, 2, 0, nil, ago(3*time.Hour))                        // an executable one still open
	phase(alpha, 3, 4, 2, 1, 1, nil, ago(3*time.Hour))                        // a [LAND] one still open
	phase(alpha, 4, 3, 3, 0, 0, nil, ago(3*time.Hour))                        // all ticked
	phase(alpha, 5, 3, 2, 1, 0, "done", ago(3*time.Hour))                     // doc says done
	phase(alpha, 6, 3, 3, 1, 0, nil, ago(3*time.Hour))                        // inconsistent counts: nothing open

	beta := task(2, "2026-10-02-beta-plan", "Beta plan", nil)
	betaPhase = phase(beta, 1, 2, 1, 1, 0, nil, ago(2*time.Hour)) // NULL doc_status still counts

	archived := task(1, "2026-09-01-old-plan", "Old plan", ago(24*time.Hour))
	phase(archived, 1, 2, 1, 1, 0, nil, ago(4*time.Hour)) // archived plan → skipped
	return alphaPhase, betaPhase
}

func manualItems(items []needsYouItem) []needsYouItem {
	var out []needsYouItem
	for _, it := range items {
		if it.Kind == needsYouManualPhase {
			out = append(out, it)
		}
	}
	return out
}

func TestNeedsYouManualPhase(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, betaPhase := manualPhaseFixture(t, db)

	resp := getNeedsYou(t, srv.URL+"/api/needs-you")
	got := manualItems(resp.Items)
	if len(got) != 2 {
		t.Fatalf("manual_phase items = %d, want 2: %+v", len(got), got)
	}
	a, b := got[0], got[1] // oldest blocker first: alpha (3h) before beta (2h)
	if a.Phase == nil || a.Phase.PhaseID != alphaPhase || b.Phase == nil || b.Phase.PhaseID != betaPhase {
		t.Fatalf("phases = %+v / %+v, want %d then %d", a.Phase, b.Phase, alphaPhase, betaPhase)
	}
	want := needsYouPhase{
		TaskID: a.Phase.TaskID, PlanExternalID: "2026-10-01-alpha-plan", PlanTitle: "Alpha plan",
		PhaseID: alphaPhase, Seq: 1, Name: "Phase name 1", ManualOpen: 2,
	}
	if *a.Phase != want {
		t.Errorf("phase = %+v\nwant  %+v", *a.Phase, want)
	}
	if a.SessionName != "Alpha plan · Phase 1: Phase name 1" || a.ProjectSlug != "-work-alpha" {
		t.Errorf("name/slug = %q/%q", a.SessionName, a.ProjectSlug)
	}
	if a.Preview != "2 [MANUAL] criteria left — only you can close them" {
		t.Errorf("preview = %q", a.Preview)
	}
	if b.Preview != "1 [MANUAL] criterion left — only you can close it" {
		t.Errorf("singular preview = %q", b.Preview)
	}
	if a.SessionID != 0 || a.RequestID != nil || a.BlockingSeconds <= 0 {
		t.Errorf("session/request/blocking = %d/%v/%d", a.SessionID, a.RequestID, a.BlockingSeconds)
	}

	// ?project= scopes the phase source like every other source.
	scoped := manualItems(getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-beta").Items)
	if len(scoped) != 1 || scoped[0].Phase.PhaseID != betaPhase {
		t.Fatalf("scoped to beta = %+v", scoped)
	}
}

func TestNeedsYouManualPhaseJSONShape(t *testing.T) {
	srv, db := needsYouServerDB(t)
	manualPhaseFixture(t, db)
	res, err := http.Get(srv.URL + "/api/needs-you?project=-work-beta")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var phaseItems, sessionItems int
	for _, it := range body.Items {
		_, hasPhase := it["phase"]
		if string(it["kind"]) == `"manual_phase"` {
			phaseItems++
			if !hasPhase {
				t.Errorf("manual_phase without phase: %s", raw)
			}
			for _, k := range []string{"taskId", "planExternalId", "planTitle", "phaseId", "seq", "name", "manualOpen"} {
				if !strings.Contains(string(it["phase"]), `"`+k+`":`) {
					t.Errorf("phase lacks %q: %s", k, it["phase"])
				}
			}
			continue
		}
		sessionItems++
		if hasPhase {
			t.Errorf("a session item carries phase: %s", raw)
		}
	}
	if phaseItems != 1 || sessionItems == 0 {
		t.Fatalf("phase items %d, session items %d: %s", phaseItems, sessionItems, raw)
	}
}

func TestSortNeedsYouManualPhaseRanksLast(t *testing.T) {
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	items := []needsYouItem{
		{Kind: needsYouManualPhase, Phase: &needsYouPhase{PhaseID: 9}, at: at},
		{Kind: needsYouManualPhase, Phase: &needsYouPhase{PhaseID: 3}, at: at},
		{Kind: needsYouFailed, SessionID: 50, at: at},
	}
	sortNeedsYou(items)
	if items[0].Kind != needsYouFailed || items[1].Phase.PhaseID != 3 || items[2].Phase.PhaseID != 9 {
		t.Fatalf("order = %+v", items)
	}
}
