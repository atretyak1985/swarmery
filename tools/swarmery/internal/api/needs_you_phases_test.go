package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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

// landPhase is one epic_phases row for the awaiting_merge fixture.
type landPhase struct {
	seq, total, done, manualOpen, landOpen int
	runBranch, runState, landingState      string
	prURL, docStatus                       any
	runEnded                               time.Duration
}

// awaitingMergeFixture plants phases on both sides of the awaiting_merge rule:
// three listed in alpha (ready / pushed / pr_open), one in beta, and every
// SC-6 exclusion.
func awaitingMergeFixture(t *testing.T, db *sql.DB) (ready, pushed, prOpen int64) {
	t.Helper()
	now := time.Now().UTC()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	task := func(project int64, ext, title string, archived any) int64 {
		t.Helper()
		res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id, archived_at)
			VALUES (?, ?, 'goal', 'running', ?, ?, 'workspace', ?, ?)`, project, title, ago(96*time.Hour), ago(96*time.Hour), ext, archived)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	phase := func(taskID int64, p landPhase) int64 {
		t.Helper()
		res, err := db.Exec(`INSERT INTO epic_phases
			(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
			 criteria_manual_open, criteria_land_open, doc_status, doc_updated_at,
			 run_branch, run_state, run_ended_at, landing_state, pr_url)
			VALUES (?, ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, p.seq, "Land "+strconv.Itoa(p.seq), "/ws/land-"+strconv.FormatInt(taskID, 10)+"/phase-"+strconv.Itoa(p.seq)+".md",
			p.total, p.done, p.manualOpen, p.landOpen, p.docStatus, ago(30*time.Hour),
			p.runBranch, p.runState, ago(p.runEnded), p.landingState, p.prURL)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	br := func(n int) string { return "swarm/phase-" + strconv.Itoa(n) }

	alpha := task(1, "2026-10-21-land-plan", "Land plan", nil)
	ready = phase(alpha, landPhase{seq: 1, total: 3, done: 2, landOpen: 1, runBranch: br(1), runState: "done", landingState: "none", runEnded: 5 * time.Hour})
	pushed = phase(alpha, landPhase{seq: 2, total: 4, done: 2, landOpen: 2, runBranch: br(2), runState: "done", landingState: "pushed", runEnded: 4 * time.Hour})
	prOpen = phase(alpha, landPhase{seq: 3, total: 2, done: 1, landOpen: 1, runBranch: br(3), runState: "done", landingState: "pr_open",
		prURL: "https://example.test/pr/3", runEnded: 3 * time.Hour})
	// Not listed (seq → reason, asserted by TestNeedsYouAwaitingMergeExclusions):
	phase(alpha, landPhase{seq: 4, total: 2, done: 1, landOpen: 1, runBranch: br(4), runState: "done", landingState: "merged", runEnded: time.Hour})
	phase(alpha, landPhase{seq: 5, total: 2, done: 1, landOpen: 1, runBranch: br(5), runState: "done", landingState: "returned", runEnded: time.Hour})
	phase(alpha, landPhase{seq: 6, total: 2, done: 1, landOpen: 1, runBranch: "", runState: "done", landingState: "none", runEnded: time.Hour})
	phase(alpha, landPhase{seq: 7, total: 3, done: 1, landOpen: 1, runBranch: br(7), runState: "done", landingState: "none", runEnded: time.Hour})
	phase(alpha, landPhase{seq: 8, total: 3, done: 1, manualOpen: 1, landOpen: 1, runBranch: br(8), runState: "done", landingState: "none", runEnded: time.Hour})
	phase(alpha, landPhase{seq: 9, total: 2, done: 1, landOpen: 1, runBranch: br(9), runState: "partial", landingState: "none", runEnded: time.Hour})

	archived := task(1, "2026-09-01-old-land", "Old land", ago(24*time.Hour))
	phase(archived, landPhase{seq: 1, total: 2, done: 1, landOpen: 1, runBranch: br(11), runState: "done", landingState: "none", runEnded: 6 * time.Hour})

	beta := task(2, "2026-10-21-beta-land", "Beta land", nil)
	phase(beta, landPhase{seq: 1, total: 2, done: 1, landOpen: 1, runBranch: br(12), runState: "done", landingState: "pushed", runEnded: 2 * time.Hour})
	return ready, pushed, prOpen
}

func itemsOfKind(items []needsYouItem, kind string) []needsYouItem {
	var out []needsYouItem
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

func TestNeedsYouAwaitingMerge(t *testing.T) {
	srv, db := needsYouServerDB(t)
	ready, pushed, prOpen := awaitingMergeFixture(t, db)

	scoped := itemsOfKind(getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-alpha").Items, needsYouAwaitingMerge)
	if len(scoped) != 3 {
		t.Fatalf("awaiting_merge items in alpha = %d, want 3 (ready, pushed, pr_open): %+v", len(scoped), scoped)
	}
	// Oldest blocker first: run_ended_at 5h, 4h, 3h ago.
	type want struct {
		id      int64
		state   string
		open    int
		prURL   string
		preview string
	}
	wants := []want{
		{ready, "ready", 1, "", "1 [LAND] criterion left — push and open a PR from Review"},
		{pushed, "pushed", 2, "", "2 [LAND] criteria left — open a PR from Review"},
		{prOpen, "pr_open", 1, "https://example.test/pr/3", "1 [LAND] criterion left — the PR is open, merge it on the host"},
	}
	for i, w := range wants {
		it := scoped[i]
		if it.Phase == nil || it.Phase.PhaseID != w.id {
			t.Fatalf("item %d phase = %+v, want id %d", i, it.Phase, w.id)
		}
		if it.Phase.LandingState != w.state || it.Phase.LandOpen != w.open || it.Phase.PrURL != w.prURL {
			t.Errorf("item %d state/open/pr = %q/%d/%q, want %q/%d/%q", i,
				it.Phase.LandingState, it.Phase.LandOpen, it.Phase.PrURL, w.state, w.open, w.prURL)
		}
		if it.Preview != w.preview {
			t.Errorf("item %d preview = %q, want %q", i, it.Preview, w.preview)
		}
		if it.Phase.ManualOpen != 0 || it.SessionID != 0 || it.RequestID != nil || it.BlockingSeconds <= 0 {
			t.Errorf("item %d manual/session/request/blocking = %d/%d/%v/%d", i,
				it.Phase.ManualOpen, it.SessionID, it.RequestID, it.BlockingSeconds)
		}
	}
	a := scoped[0]
	if a.SessionName != "Land plan · Phase 1: Land 1" || a.ProjectSlug != "-work-alpha" ||
		a.Phase.PlanExternalID != "2026-10-21-land-plan" || a.Phase.Seq != 1 {
		t.Errorf("title/slug/plan/seq = %q/%q/%q/%d", a.SessionName, a.ProjectSlug, a.Phase.PlanExternalID, a.Phase.Seq)
	}
	// blockingSince is the run's end (5h ago), not the doc's mtime (30h ago).
	if a.BlockingSeconds > int64((6 * time.Hour).Seconds()) {
		t.Errorf("blockingSeconds = %d, want ≈ 5h (run_ended_at)", a.BlockingSeconds)
	}

	// Unscoped: the beta phase joins; the archived plan's ready phase never does.
	all := itemsOfKind(getNeedsYou(t, srv.URL+"/api/needs-you").Items, needsYouAwaitingMerge)
	if len(all) != 4 {
		t.Fatalf("unscoped awaiting_merge = %d, want 4: %+v", len(all), all)
	}
	// None of them is mistaken for a manual_phase item.
	if m := itemsOfKind(getNeedsYou(t, srv.URL+"/api/needs-you").Items, needsYouManualPhase); len(m) != 0 {
		t.Errorf("manual_phase items = %+v, want none", m)
	}
}

// TestNeedsYouAwaitingMergeListsDoneDoc pins the live case that hid every
// real candidate: an executor marks the doc Status: Done once its own criteria
// are ticked and leaves [LAND] open — that phase is awaiting merge.
func TestNeedsYouAwaitingMergeListsDoneDoc(t *testing.T) {
	srv, db := needsYouServerDB(t)
	awaitingMergeFixture(t, db)
	now := time.Now().UTC()
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (1, 'Done doc', 'goal', 'running', ?, ?, 'workspace', '2026-10-21-done-doc')`,
		now.Add(-48*time.Hour).Format(time.RFC3339), now.Add(-48*time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
		 criteria_manual_open, criteria_land_open, doc_status, doc_updated_at,
		 run_branch, run_state, run_ended_at, landing_state)
		VALUES (?, 6, 'Today v6', '/ws/done-doc/phase-6.md', '[]', 10, 9, 0, 1, 'done', ?,
		        'swarm/phase-6', 'done', ?, 'none')`,
		taskID, now.Add(-time.Hour).Format(time.RFC3339), now.Add(-2*time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	phaseID, _ := res.LastInsertId()
	for _, it := range itemsOfKind(getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-alpha").Items, needsYouAwaitingMerge) {
		if it.Phase.PhaseID == phaseID {
			if it.Phase.LandingState != "ready" || it.Phase.LandOpen != 1 {
				t.Errorf("done-doc phase state/open = %q/%d, want ready/1", it.Phase.LandingState, it.Phase.LandOpen)
			}
			return
		}
	}
	t.Fatalf("phase %d (doc done, only [LAND] open, landing ready) not listed as awaiting_merge", phaseID)
}

// TestNeedsYouAwaitingMergeExclusions names every SC-6 exclusion on its own,
// so a regression says which rule broke.
func TestNeedsYouAwaitingMergeExclusions(t *testing.T) {
	srv, db := needsYouServerDB(t)
	awaitingMergeFixture(t, db)
	listed := map[int]bool{}
	for _, it := range itemsOfKind(getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-alpha").Items, needsYouAwaitingMerge) {
		if it.Phase.PlanExternalID == "2026-10-21-land-plan" {
			listed[it.Phase.Seq] = true
		}
	}
	for seq, why := range map[int]string{
		4: "landing merged",
		5: "landing returned",
		6: "run_branch empty",
		7: "an executable criterion is open",
		8: "a [MANUAL] criterion is open",
		9: "landing none but the run is not done",
	} {
		if listed[seq] {
			t.Errorf("phase %d listed although %s", seq, why)
		}
	}
	for _, it := range itemsOfKind(getNeedsYou(t, srv.URL+"/api/needs-you").Items, needsYouAwaitingMerge) {
		if it.Phase.PlanTitle == "Old land" {
			t.Errorf("archived plan listed: %+v", it)
		}
	}
}

func TestNeedsYouAwaitingMergeJSONShape(t *testing.T) {
	srv, db := needsYouServerDB(t)
	manualPhaseFixture(t, db)
	awaitingMergeFixture(t, db)
	res, err := http.Get(srv.URL + "/api/needs-you")
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
	var merges, withPR, manuals int
	for _, it := range body.Items {
		phase := string(it["phase"])
		switch string(it["kind"]) {
		case `"awaiting_merge"`:
			merges++
			for _, k := range []string{"taskId", "planExternalId", "planTitle", "phaseId", "seq", "name", "landOpen", "landingState"} {
				if !strings.Contains(phase, `"`+k+`":`) {
					t.Errorf("awaiting_merge phase lacks %q: %s", k, phase)
				}
			}
			if strings.Contains(phase, `"prUrl":`) {
				withPR++
				if !strings.Contains(phase, `"landingState":"pr_open"`) {
					t.Errorf("prUrl on a phase that is not pr_open: %s", phase)
				}
			}
		case `"manual_phase"`:
			manuals++
			for _, k := range []string{"landOpen", "landingState", "prUrl"} {
				if strings.Contains(phase, `"`+k+`":`) {
					t.Errorf("manual_phase carries %q: %s", k, phase)
				}
			}
		}
	}
	if merges != 4 || withPR != 1 || manuals == 0 {
		t.Fatalf("awaiting_merge %d (want 4), with prUrl %d (want 1), manual_phase %d (want >0): %s", merges, withPR, manuals, raw)
	}
}

func TestSortNeedsYouAwaitingMergeRanksAfterManual(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	items := []needsYouItem{
		{Kind: needsYouAwaitingMerge, Phase: &needsYouPhase{PhaseID: 1}, at: at},
		{Kind: needsYouManualPhase, Phase: &needsYouPhase{PhaseID: 9}, at: at},
		{Kind: needsYouFailed, SessionID: 50, at: at},
	}
	sortNeedsYou(items)
	if items[0].Kind != needsYouFailed || items[1].Kind != needsYouManualPhase || items[2].Kind != needsYouAwaitingMerge {
		t.Fatalf("order = %+v", items)
	}
}
