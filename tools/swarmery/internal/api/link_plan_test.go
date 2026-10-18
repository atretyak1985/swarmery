package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

const mdFence = "```"

// Phase docs in the plan-format shape (plugins/core/skills/workspace-plans/
// resources/plan-format.md). Alpha's phase 2 quotes a checkbox inside a fenced
// agent prompt BEFORE its criteria: idx must skip it, or a tick would flip the
// template instead of the criterion.
const (
	alphaPhase1 = "# Phase 1 — Schema\nStatus: Done\n\n## Goal\nTables.\n\n" +
		"## Acceptance Criteria\n- [x] migration lands\n- [x] test passes\n\n## Completion Report\nShipped.\n"
	alphaPhase2 = "# Phase 2 — API\nStatus: In progress\n\n**Depends on:** 1\n\n## Goal\nServe the plan.\n\n" +
		"## Copy-paste Agent Prompt\n" + mdFence + "text\n- [ ] a fenced template line\n" + mdFence + "\n\n" +
		"## Acceptance Criteria\n- [x] route registered\n- [ ] tick flips the doc\n" +
		"- [ ] [LAND] push the branch\n- [x] [MANUAL] check the console\n\n## Completion Report\n"
	alphaPhase3 = "# Phase 3 — UI\nStatus: Pending\n\n## Acceptance Criteria\n- [ ] panel renders\n\n## Completion Report\n"
	betaPhase1  = "# Phase 1 — Docs\nStatus: Pending\n\n## Acceptance Criteria\n- [ ] write the docs\n\n## Completion Report\n"
	betaPhase2  = "# Phase 2 — Ship\nStatus: Pending\n\n## Acceptance Criteria\n- [ ] ship it\n\n## Completion Report\n"
)

// planFixture is a link env whose DB holds project `proj` (/work/proj) with two
// plans: alpha (older, phase 2 In progress) and beta (newer, nothing started),
// plus an empty project `empty` (/work/empty).
type planFixture struct {
	*linkTestEnv
	alphaID, betaID   int64
	alphaDir, betaDir string
	alpha, beta       []int64 // phase ids, seq order
}

const (
	alphaRef = "2026-10-01-alpha"
	betaRef  = "2026-10-10-beta"
)

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	f := &planFixture{linkTestEnv: newLinkTestEnv(t)}
	db := f.db
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO projects(id, path, slug, first_seen) VALUES
		(1, '/work/proj', 'proj', '2026-01-01T00:00:00Z'),
		(2, '/work/empty', 'empty', '2026-01-01T00:00:00Z')`)

	seedPlan := func(ref, title, started string, docs []string, rows func(dir string, taskID int64)) (int64, string) {
		dir := filepath.Join(t.TempDir(), ref, "plan")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i, body := range docs {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("phase-%d.md", i+1)), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id)
			VALUES (1, ?, 'goal', 'running', ?, ?, 'workspace', ?)`, title, started, started, ref)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		mustExec(`INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
			VALUES (?, 'plan', ?, 'hash', ?)`, id, dir, started)
		rows(dir, id)
		return id, dir
	}
	f.alphaID, f.alphaDir = seedPlan(alphaRef, "Alpha plan", "2026-10-01T00:00:00Z",
		[]string{alphaPhase1, alphaPhase2, alphaPhase3}, func(dir string, id int64) {
			mustExec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, depends_on,
				checkboxes_total, checkboxes_done, doc_status, doc_updated_at) VALUES
				(?, 1, 'Phase 1 — Schema', ?, '[]', 2, 2, 'done', '2026-10-02T00:00:00Z'),
				(?, 2, 'Phase 2 — API', ?, '[1]', 4, 2, 'in_progress', '2026-10-03T00:00:00Z'),
				(?, 3, 'Phase 3 — UI', ?, '[2]', 1, 0, 'pending', '2026-10-01T00:00:00Z')`,
				id, filepath.Join(dir, "phase-1.md"), id, filepath.Join(dir, "phase-2.md"),
				id, filepath.Join(dir, "phase-3.md"))
		})
	f.betaID, f.betaDir = seedPlan(betaRef, "Beta plan", "2026-10-10T00:00:00Z",
		[]string{betaPhase1, betaPhase2}, func(dir string, id int64) {
			mustExec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, depends_on,
				checkboxes_total, checkboxes_done, doc_status) VALUES
				(?, 1, 'Docs', ?, '[]', 1, 0, 'pending'),
				(?, 2, 'Ship', ?, '[1]', 1, 0, 'pending')`,
				id, filepath.Join(dir, "phase-1.md"), id, filepath.Join(dir, "phase-2.md"))
		})
	f.alpha = phaseIDs(t, f, f.alphaID)
	f.beta = phaseIDs(t, f, f.betaID)
	return f
}

func phaseIDs(t *testing.T, f *planFixture, taskID int64) []int64 {
	t.Helper()
	rows, err := f.db.Query(`SELECT id FROM epic_phases WHERE workspace_task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (f *planFixture) addSession(t *testing.T, uuid string, projectID int64, seenAt string) int64 {
	t.Helper()
	var seen any
	if seenAt != "" {
		seen = seenAt
	}
	res, err := f.db.Exec(`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, link_seen_at)
		VALUES (?, ?, '/work/proj', 'active', '2026-10-14T00:00:00.000Z', ?)`, projectID, uuid, seen)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// tok sends with the env's link token.
func (f *planFixture) tok(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	return f.do(t, method, path, f.token, body)
}

func decodeView(t *testing.T, body string) planView {
	t.Helper()
	var v planView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

// sid renders a row id the way the wire carries it.
func sid(n int64) string { return fmt.Sprint(n) }

// tickBody is a POST /api/link/plan/tick body; extra is appended verbatim
// (e.g. `,"idx":1,"done":true`).
func tickBody(task, phase, extra string) string {
	return fmt.Sprintf(`{"session":"s-tick","taskId":%q,"phaseId":%q%s}`, task, phase, extra)
}

func TestLinkPlanRequiresToken(t *testing.T) {
	f := newPlanFixture(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/link/plan?cwd=/work/proj", ""},
		{http.MethodPost, "/api/link/plan/select", `{"session":"s","taskId":"` + alphaRef + `"}`},
		{http.MethodPost, "/api/link/plan/tick", `{"session":"s","taskId":"` + alphaRef + `","phaseId":"` + sid(f.alpha[1]) + `","idx":1,"done":true}`},
		{http.MethodPost, "/api/link/plan/run", `{"session":"s","taskId":"` + alphaRef + `","phaseId":"` + sid(f.alpha[1]) + `"}`},
		{http.MethodPost, "/api/link/plan/drift", `{"session":"s","verdict":"on-phase"}`},
	} {
		if code, _ := f.do(t, tc.method, tc.path, "", tc.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s without token = %d, want 401", tc.method, tc.path, code)
		}
	}
	if got := readDoc(t, filepath.Join(f.alphaDir, "phase-2.md")); got != alphaPhase2 {
		t.Errorf("an unauthenticated tick changed the doc:\n%s", got)
	}
	// With the link detached every plan route is 503, like the other link routes.
	AttachLink(nil, nil)
	if code, _ := f.tok(t, http.MethodGet, "/api/link/plan?cwd=/work/proj", ""); code != http.StatusServiceUnavailable {
		t.Errorf("detached GET = %d, want 503", code)
	}
}

// GET by cwd: no session row, no pick → the project's plan with a phase In
// progress (alpha), even though beta is newer. The exact wire shape is pinned.
func TestLinkPlanGetByCwdActiveEpic(t *testing.T) {
	f := newPlanFixture(t)
	code, body := f.tok(t, http.MethodGet, "/api/link/plan?session=s-cwd&cwd=/work/proj/sub/dir", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", code, body)
	}
	want := fmt.Sprintf(`{"taskId":%q,"title":"Alpha plan","url":"%s/p/proj/plans/%s/phase/2","chosenBy":"auto",`+
		`"phase":{"id":"%d","n":2,"title":"API","status":"In progress","done":2,"total":4,"criteria":[`+
		`{"idx":0,"text":"route registered","done":true,"class":""},`+
		`{"idx":1,"text":"tick flips the doc","done":false,"class":""},`+
		`{"idx":2,"text":"push the branch","done":false,"class":"land"},`+
		`{"idx":3,"text":"check the console","done":true,"class":"manual"}]},`+
		`"next":{"id":"%d","n":3,"title":"UI"}}`,
		alphaRef, f.srv.URL, alphaRef, f.alpha[1], f.alpha[2])
	if strings.TrimSpace(body) != want {
		t.Errorf("body =\n%s\nwant\n%s", strings.TrimSpace(body), want)
	}

	// The automatic resolution is recorded for the SPA, as 'auto'.
	var task, phase, by string
	if err := f.db.QueryRow(`SELECT task_id, phase_id, chosen_by FROM session_plan WHERE session_id = 's-cwd'`).
		Scan(&task, &phase, &by); err != nil {
		t.Fatalf("session_plan row: %v", err)
	}
	if task != alphaRef || phase != sid(f.alpha[1]) || by != planChosenAuto {
		t.Errorf("session_plan = %s/%s/%s", task, phase, by)
	}
}

// With nothing In progress anywhere, the newest plan the epics list calls
// `active` wins, at its first not-done phase.
func TestLinkPlanGetFallsBackToNewestActive(t *testing.T) {
	f := newPlanFixture(t)
	if _, err := f.db.Exec(`UPDATE epic_phases SET doc_status = 'pending', checkboxes_done = 0 WHERE workspace_task_id = ?`, f.alphaID); err != nil {
		t.Fatal(err)
	}
	code, body := f.tok(t, http.MethodGet, "/api/link/plan?cwd=/work/proj", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}
	v := decodeView(t, body)
	if v.TaskID != betaRef || v.Phase.ID != sid(f.beta[0]) || v.Phase.Status != phaseStatusPending {
		t.Errorf("view = %+v, want beta phase 1 Pending", v)
	}
	if v.Next == nil || v.Next.ID != sid(f.beta[1]) || v.Next.Title != "Ship" {
		t.Errorf("next = %+v, want beta phase 2", v.Next)
	}
}

func TestLinkPlanGetNoPlan(t *testing.T) {
	f := newPlanFixture(t)
	for _, q := range []string{
		"?cwd=/work/empty",               // a project with no plans
		"?cwd=/elsewhere/entirely",       // no project at all
		"?session=nobody&cwd=relative/x", // unknown session, non-absolute cwd
	} {
		if code, body := f.tok(t, http.MethodGet, "/api/link/plan"+q, ""); code != http.StatusNoContent {
			t.Errorf("%s = %d (%s), want 204", q, code, body)
		}
	}
	if code, _ := f.tok(t, http.MethodGet, "/api/link/plan", ""); code != http.StatusBadRequest {
		t.Errorf("no session, no cwd = %d, want 400", code)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM session_plan`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("session_plan rows = %d after 204s, want 0", n)
	}
}

// The session's own project beats the cwd.
func TestLinkPlanGetBySessionProject(t *testing.T) {
	f := newPlanFixture(t)
	f.addSession(t, "s-row", 1, "")
	code, body := f.tok(t, http.MethodGet, "/api/link/plan?session=s-row&cwd=/work/empty", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}
	if v := decodeView(t, body); v.TaskID != alphaRef {
		t.Errorf("taskId = %s, want %s", v.TaskID, alphaRef)
	}
}

// A session_plan terminal row is resolution step 1, phase included.
func TestLinkPlanGetWithSessionPlanRow(t *testing.T) {
	f := newPlanFixture(t)
	if _, err := f.db.Exec(`INSERT INTO session_plan (session_id, task_id, phase_id, chosen_by, at)
		VALUES ('s-pick', ?, ?, 'terminal', '2026-10-14T00:00:00.000Z')`, betaRef, sid(f.beta[1])); err != nil {
		t.Fatal(err)
	}
	code, body := f.tok(t, http.MethodGet, "/api/link/plan?session=s-pick&cwd=/work/proj", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}
	v := decodeView(t, body)
	if v.TaskID != betaRef || v.ChosenBy != planChosenTerminal || v.Phase.ID != sid(f.beta[1]) || v.Phase.N != 2 {
		t.Errorf("view = %+v, want beta phase 2 chosen in the terminal", v)
	}
	if v.Phase.Status != phaseStatusBlocked {
		t.Errorf("status = %q, want Blocked (depends on an unfinished phase 1)", v.Phase.Status)
	}
	if v.Next != nil {
		t.Errorf("next = %+v, want null after the last phase", v.Next)
	}

	// A pick whose plan is gone falls back to the automatic resolution, and the
	// automatic record never overwrites the terminal row.
	if _, err := f.db.Exec(`UPDATE session_plan SET task_id = 'gone' WHERE session_id = 's-pick'`); err != nil {
		t.Fatal(err)
	}
	code, body = f.tok(t, http.MethodGet, "/api/link/plan?session=s-pick&cwd=/work/proj", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}
	if v := decodeView(t, body); v.TaskID != alphaRef || v.ChosenBy != planChosenAuto {
		t.Errorf("fallback view = %+v, want alpha auto", v)
	}
	var task, by string
	if err := f.db.QueryRow(`SELECT task_id, chosen_by FROM session_plan WHERE session_id = 's-pick'`).Scan(&task, &by); err != nil {
		t.Fatal(err)
	}
	if task != "gone" || by != planChosenTerminal {
		t.Errorf("terminal row = %s/%s, want it untouched", task, by)
	}
}

func TestLinkPlanSelect(t *testing.T) {
	f := newPlanFixture(t)
	code, body := f.tok(t, http.MethodPost, "/api/link/plan/select",
		`{"session":"s-sel","taskId":"`+betaRef+`","phaseId":"`+sid(f.beta[1])+`"}`)
	if code != http.StatusOK {
		t.Fatalf("select = %d (%s)", code, body)
	}
	if v := decodeView(t, body); v.TaskID != betaRef || v.Phase.ID != sid(f.beta[1]) || v.ChosenBy != planChosenTerminal {
		t.Errorf("select view = %+v", v)
	}
	// GET now answers with the pick, not with alpha's In progress phase.
	code, body = f.tok(t, http.MethodGet, "/api/link/plan?session=s-sel&cwd=/work/proj", "")
	if code != http.StatusOK {
		t.Fatalf("get = %d (%s)", code, body)
	}
	if v := decodeView(t, body); v.TaskID != betaRef || v.Phase.ID != sid(f.beta[1]) || v.ChosenBy != planChosenTerminal {
		t.Errorf("get after select = %+v", v)
	}
	// Re-select without a phase: the plan's active phase, numeric task id accepted.
	code, body = f.tok(t, http.MethodPost, "/api/link/plan/select", fmt.Sprintf(`{"session":"s-sel","taskId":%d}`, f.alphaID))
	if code != http.StatusOK {
		t.Fatalf("reselect = %d (%s)", code, body)
	}
	if v := decodeView(t, body); v.TaskID != alphaRef || v.Phase.ID != sid(f.alpha[1]) {
		t.Errorf("reselect view = %+v, want alpha phase 2", v)
	}
	var phase any
	if err := f.db.QueryRow(`SELECT phase_id FROM session_plan WHERE session_id = 's-sel'`).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != nil {
		t.Errorf("phase_id = %v, want NULL for a plan-only pick", phase)
	}

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"session":"s-sel","taskId":"nope"}`, http.StatusNotFound},
		{`{"session":"s-sel","taskId":"` + betaRef + `","phaseId":"` + sid(f.alpha[0]) + `"}`, http.StatusNotFound},
		{`{"taskId":"` + betaRef + `"}`, http.StatusBadRequest},
		{`{"session":"s-sel"}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
	} {
		if code, body := f.tok(t, http.MethodPost, "/api/link/plan/select", tc.body); code != tc.want {
			t.Errorf("select %s = %d (%s), want %d", tc.body, code, body, tc.want)
		}
	}
}

// The tick flips exactly the idx-th criterion — past the fenced template line —
// through the PATCH core, publishes plan_updated, and the next GET shows it.
func TestLinkPlanTick(t *testing.T) {
	bus := ingest.NewBus()
	AttachBus(bus)
	t.Cleanup(func() { AttachBus(nil) })
	f := newPlanFixture(t)
	frames, cancel := bus.Subscribe(16)
	t.Cleanup(cancel)
	doc := filepath.Join(f.alphaDir, "phase-2.md")

	p2 := sid(f.alpha[1])
	tick := tickBody(alphaRef, p2, `,"idx":1,"done":true`)
	code, body := f.tok(t, http.MethodPost, "/api/link/plan/tick", tick)
	if code != http.StatusOK {
		t.Fatalf("tick = %d (%s)", code, body)
	}
	want := strings.Replace(alphaPhase2, "- [ ] tick flips the doc", "- [x] tick flips the doc", 1)
	if got := readDoc(t, doc); got != want {
		t.Errorf("doc after tick =\n%s\nwant\n%s", got, want)
	}
	v := decodeView(t, body)
	if !v.Phase.Criteria[1].Done || v.Phase.Done != 3 || v.Phase.Total != 4 {
		t.Errorf("tick view phase = %+v", v.Phase)
	}
	select {
	case n := <-frames:
		if n.Type != ingest.NotePlanUpdated || n.TaskID != f.alphaID {
			t.Errorf("frame = %+v, want plan_updated task %d", n, f.alphaID)
		}
	case <-time.After(2 * time.Second):
		t.Error("no plan_updated frame")
	}
	// A backup was taken, exactly like PATCH.
	if m, _ := filepath.Glob(filepath.Join(f.alphaDir, ".backups", "*", "phase-2.md")); len(m) == 0 {
		t.Error("no backup written")
	}

	code, body = f.tok(t, http.MethodGet, "/api/link/plan?session=s-tick&cwd=/work/proj", "")
	if code != http.StatusOK {
		t.Fatalf("get = %d (%s)", code, body)
	}
	if g := decodeView(t, body); !g.Phase.Criteria[1].Done || g.Phase.Done != 3 {
		t.Errorf("second GET phase = %+v, want idx 1 done", g.Phase)
	}

	// Untick goes back to the original bytes.
	untick := tickBody(alphaRef, p2, `,"idx":1,"done":false`)
	if code, body := f.tok(t, http.MethodPost, "/api/link/plan/tick", untick); code != http.StatusOK {
		t.Fatalf("untick = %d (%s)", code, body)
	}
	if got := readDoc(t, doc); got != alphaPhase2 {
		t.Errorf("doc after untick =\n%s", got)
	}

	for _, tc := range []struct {
		body string
		want int
	}{
		{tickBody(alphaRef, p2, `,"idx":4,"done":true`), http.StatusNotFound},
		{tickBody(alphaRef, p2, `,"idx":-1,"done":true`), http.StatusBadRequest},
		{tickBody(alphaRef, p2, `,"idx":1`), http.StatusBadRequest},
		{tickBody(alphaRef, p2, `,"done":true`), http.StatusBadRequest},
		{tickBody(alphaRef, sid(f.beta[0]), `,"idx":0,"done":true`), http.StatusNotFound},
		{tickBody("nope", p2, `,"idx":0,"done":true`), http.StatusNotFound},
		{tickBody(alphaRef, "", `,"idx":0,"done":true`), http.StatusBadRequest},
	} {
		if code, body := f.tok(t, http.MethodPost, "/api/link/plan/tick", tc.body); code != tc.want {
			t.Errorf("tick %s = %d (%s), want %d", tc.body, code, body, tc.want)
		}
	}
	if got := readDoc(t, doc); got != alphaPhase2 {
		t.Errorf("a refused tick changed the doc:\n%s", got)
	}
}

// The link run answers exactly what the epics run route answers for the same
// phase. Alpha's phase 3 depends on the unfinished phase 2: 409 deps-unmet,
// nothing started.
func TestLinkPlanRunDelegates(t *testing.T) {
	f := newPlanFixture(t)
	runner := &phaseStubRunner{}
	attachPhaseRun(t, f.db, runner, true)

	linkCode, linkBody := f.tok(t, http.MethodPost, "/api/link/plan/run",
		fmt.Sprintf(`{"session":"s-run","taskId":%q,"phaseId":%q}`, alphaRef, sid(f.alpha[2])))
	epicCode, epicBody := f.do(t, http.MethodPost,
		fmt.Sprintf("/api/epics/%d/phases/%d/run", f.alphaID, f.alpha[2]), "", "")
	if linkCode != http.StatusConflict || epicCode != linkCode {
		t.Fatalf("link run = %d (%s), epics run = %d (%s); want the same 409", linkCode, linkBody, epicCode, epicBody)
	}
	if linkBody != epicBody {
		t.Errorf("link body %s != epics body %s", linkBody, epicBody)
	}
	if specs := runner.dispatchedSpecs(); len(specs) != 0 {
		t.Errorf("a refused run dispatched %d specs", len(specs))
	}
	if code, _ := f.tok(t, http.MethodPost, "/api/link/plan/run",
		fmt.Sprintf(`{"session":"s-run","taskId":%q,"phaseId":"999999"}`, alphaRef)); code != http.StatusNotFound {
		t.Errorf("unknown phase = %d, want 404", code)
	}
	detachPhaseRun(t)
	if code, _ := f.tok(t, http.MethodPost, "/api/link/plan/run",
		fmt.Sprintf(`{"session":"s-run","taskId":%q,"phaseId":%q}`, alphaRef, sid(f.alpha[2]))); code != http.StatusServiceUnavailable {
		t.Errorf("detached run = %d, want 503", code)
	}
}

func TestLinkPlanDrift(t *testing.T) {
	f := newPlanFixture(t)
	sessID := f.addSession(t, "s-drift", 1, "")
	long := strings.Repeat("é", 250)
	code, body := f.tok(t, http.MethodPost, "/api/link/plan/drift",
		fmt.Sprintf(`{"session":"s-drift","taskId":%q,"phaseId":%q,"verdict":"off-phase","reason":%q}`, alphaRef, sid(f.alpha[1]), long))
	if code != http.StatusAccepted {
		t.Fatalf("drift = %d (%s), want 202", code, body)
	}
	var (
		kind, payload string
		n             int
	)
	if err := f.db.QueryRow(`SELECT COUNT(*), MAX(kind), MAX(payload) FROM link_events WHERE session_id = ?`, sessID).
		Scan(&n, &kind, &payload); err != nil {
		t.Fatal(err)
	}
	if n != 1 || kind != "drift" {
		t.Fatalf("link_events = %d rows, kind %q; want one drift", n, kind)
	}
	want := fmt.Sprintf(`{"taskId":%q,"phaseId":%q,"verdict":"off-phase","reason":%q}`, alphaRef, sid(f.alpha[1]), strings.Repeat("é", 200))
	if payload != want {
		t.Errorf("payload = %s\nwant %s", payload, want)
	}
	// The drift row moves no link state.
	var status any
	if err := f.db.QueryRow(`SELECT link_status FROM sessions WHERE id = ?`, sessID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != nil {
		t.Errorf("link_status = %v, want untouched NULL", status)
	}

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"session":"nobody","verdict":"on-phase"}`, http.StatusNotFound},
		{`{"session":"s-drift","verdict":"sideways"}`, http.StatusBadRequest},
		{`{"verdict":"adjacent"}`, http.StatusBadRequest},
	} {
		if code, body := f.tok(t, http.MethodPost, "/api/link/plan/drift", tc.body); code != tc.want {
			t.Errorf("drift %s = %d (%s), want %d", tc.body, code, body, tc.want)
		}
	}
}

func TestTerminalSessions(t *testing.T) {
	f := newPlanFixture(t)
	now := time.Now().UTC()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(sessionPlanTS) }
	termID := f.addSession(t, "s-term", 1, ts(time.Minute)) // a live mod
	f.addSession(t, "s-stale", 1, ts(time.Hour))            // its mod went away
	f.addSession(t, "s-old", 1, "")
	f.addSession(t, "s-beta", 1, "")
	for _, row := range []struct {
		uuid, task, by, at string
		phase              any
	}{
		{"s-term", alphaRef, planChosenTerminal, ts(2 * time.Hour), sid(f.alpha[1])},
		{"s-stale", alphaRef, planChosenAuto, ts(5 * time.Minute), nil},
		{"s-old", alphaRef, planChosenAuto, ts(20 * time.Minute), nil},  // outside the 10 min window
		{"s-beta", betaRef, planChosenTerminal, ts(time.Minute), nil},   // another plan
		{"s-norow", alphaRef, planChosenTerminal, ts(time.Minute), nil}, // no sessions row
	} {
		if _, err := f.db.Exec(`INSERT INTO session_plan (session_id, task_id, phase_id, chosen_by, at) VALUES (?, ?, ?, ?, ?)`,
			row.uuid, row.task, row.phase, row.by, row.at); err != nil {
			t.Fatal(err)
		}
	}
	// An automatic GET records its session — it shows up too, newest first.
	f.addSession(t, "s-get", 1, "")
	if code, body := f.tok(t, http.MethodGet, "/api/link/plan?session=s-get", ""); code != http.StatusOK {
		t.Fatalf("auto get = %d (%s)", code, body)
	}

	code, body := f.do(t, http.MethodGet, fmt.Sprintf("/api/epics/%d/terminal-sessions", f.alphaID), "", "")
	if code != http.StatusOK {
		t.Fatalf("terminal-sessions = %d (%s)", code, body)
	}
	var got []terminalSessionDTO
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	var uuids []string
	for _, d := range got {
		uuids = append(uuids, d.SessionUUID)
	}
	if strings.Join(uuids, ",") != "s-get,s-stale,s-term" {
		t.Fatalf("sessions = %v, want [s-get s-stale s-term]", uuids)
	}
	term := got[2]
	if term.SessionID != termID || term.ChosenBy != planChosenTerminal || !term.Live ||
		term.PhaseID == nil || *term.PhaseID != sid(f.alpha[1]) {
		t.Errorf("s-term = %+v", term)
	}
	if got[1].Live || got[1].PhaseID != nil || got[1].ChosenBy != planChosenAuto {
		t.Errorf("s-stale = %+v", got[1])
	}
	if got[0].ChosenBy != planChosenAuto || got[0].PhaseID == nil || *got[0].PhaseID != sid(f.alpha[1]) {
		t.Errorf("s-get = %+v", got[0])
	}
	if _, err := time.Parse(time.RFC3339, got[0].At); err != nil {
		t.Errorf("at %q is not RFC 3339: %v", got[0].At, err)
	}

	// Beta has its own row; an empty plan answers [] not null; unknown task 404.
	if _, body := f.do(t, http.MethodGet, fmt.Sprintf("/api/epics/%d/terminal-sessions", f.betaID), "", ""); !strings.Contains(body, `"s-beta"`) {
		t.Errorf("beta terminal-sessions = %s", body)
	}
	if _, err := f.db.Exec(`DELETE FROM session_plan`); err != nil {
		t.Fatal(err)
	}
	if _, body := f.do(t, http.MethodGet, fmt.Sprintf("/api/epics/%d/terminal-sessions", f.alphaID), "", ""); strings.TrimSpace(body) != "[]" {
		t.Errorf("empty = %s, want []", body)
	}
	if code, _ := f.do(t, http.MethodGet, "/api/epics/999999/terminal-sessions", "", ""); code != http.StatusNotFound {
		t.Errorf("unknown task = %d, want 404", code)
	}
}

// The view's statuses mirror the Plans page's chip derivation.
func TestPhaseViewStatuses(t *testing.T) {
	done, col := "done", "in_review"
	inProg := "in_progress"
	phases := []epicPhaseDTO{
		{Seq: 1, CompletionState: "complete"},
		{Seq: 2, CompletionState: "unverified"},
		{Seq: 3, CompletionState: "incomplete", DependsOn: []int{2}},
		{Seq: 4, CompletionState: "incomplete", DependsOn: []int{2}, DocStatus: &inProg},
		{Seq: 5, CompletionState: "incomplete", DependsOn: []int{2}, RunState: "running"},
		{Seq: 6, CompletionState: "incomplete", DependsOn: []int{1}, CheckboxesDone: 1},
		{Seq: 7, CompletionState: "incomplete", BoardColumn: &col},
		{Seq: 8, CompletionState: "incomplete", BoardColumn: &done},
		{Seq: 9, CompletionState: "unreported"},
		{Seq: 10, CompletionState: "incomplete", DependsOn: []int{8}},
	}
	want := []string{phaseStatusDone, phaseStatusUnverified, phaseStatusBlocked, phaseStatusInProgress,
		phaseStatusInProgress, phaseStatusInProgress, phaseStatusInProgress, phaseStatusDone,
		phaseStatusUnreported, phaseStatusPending}
	got := phaseViewStatuses(phases)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("phase %d: %q, want %q", phases[i].Seq, got[i], want[i])
		}
	}
	if phaseViewTitle("Phase 7 — Plan in the terminal") != "Plan in the terminal" ||
		phaseViewTitle("Step 2: Wire") != "Wire" || phaseViewTitle("Docs") != "Docs" || phaseViewTitle("Phase 3") != "Phase 3" {
		t.Error("phaseViewTitle prefix strip")
	}
}

// A tick that carries the criterion's drawn text is refused 409 when the doc's
// idx-th criterion no longer reads so (a line inserted above, or the
// criterion gone); a matching text ticks; no text keeps the positional tick.
func TestLinkPlanTickText(t *testing.T) {
	f := newPlanFixture(t)
	doc := filepath.Join(f.alphaDir, "phase-2.md")
	p2 := sid(f.alpha[1])

	// Absent: the positional tick, as before.
	if code, body := f.tok(t, http.MethodPost, "/api/link/plan/tick", tickBody(alphaRef, p2, `,"idx":1,"done":true`)); code != http.StatusOK {
		t.Fatalf("tick without text = %d (%s), want 200", code, body)
	}
	if got := readDoc(t, doc); !strings.Contains(got, "- [x] tick flips the doc") {
		t.Fatalf("tick without text did not flip idx 1:\n%s", got)
	}

	// Match: ticks (here: unticks) the criterion it names.
	if code, body := f.tok(t, http.MethodPost, "/api/link/plan/tick",
		tickBody(alphaRef, p2, `,"idx":1,"done":false,"text":"tick flips the doc"`)); code != http.StatusOK {
		t.Fatalf("tick with matching text = %d (%s), want 200", code, body)
	}
	if got := readDoc(t, doc); got != alphaPhase2 {
		t.Fatalf("matching untick did not restore the doc:\n%s", got)
	}

	// A criterion inserted above idx 1 since the mod drew the band: idx 1 is
	// now the new line. Mismatch → 409, nothing written.
	moved := strings.Replace(alphaPhase2, "- [x] route registered\n", "- [x] route registered\n- [ ] a criterion added since\n", 1)
	if err := os.WriteFile(doc, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	code, body := f.tok(t, http.MethodPost, "/api/link/plan/tick",
		tickBody(alphaRef, p2, `,"idx":1,"done":true,"text":"tick flips the doc"`))
	if code != http.StatusConflict || strings.TrimSpace(body) != `{"error":"criterion changed"}` {
		t.Fatalf("stale tick = %d %s, want 409 criterion changed", code, body)
	}
	// Out of range with text is the same conflict, not a 404.
	if code, body := f.tok(t, http.MethodPost, "/api/link/plan/tick",
		tickBody(alphaRef, p2, `,"idx":9,"done":true,"text":"tick flips the doc"`)); code != http.StatusConflict {
		t.Errorf("stale idx past the end = %d (%s), want 409", code, body)
	}
	if got := readDoc(t, doc); got != moved {
		t.Errorf("a refused tick changed the doc:\n%s", got)
	}
}

// otherAlpha seeds project `other` (/work/other, id 3) with a plan whose
// external id is alphaRef too — tasks.external_id is unique per workspace, not
// across projects — and returns its task id, plan dir and phase ids. It is the
// newer task, so an unscoped, newest-first lookup would pick it.
func otherAlpha(t *testing.T, f *planFixture) (int64, string, []int64) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES (3, '/work/other', 'other', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "other", "plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Phase 1 — Elsewhere\nStatus: In progress\n\n## Acceptance Criteria\n- [ ] the other project's criterion\n\n## Completion Report\n"
	if err := os.WriteFile(filepath.Join(dir, "phase-1.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := f.db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (3, 'Other alpha', 'goal', 'running', '2026-10-12T00:00:00Z', '2026-10-12T00:00:00Z', 'workspace', ?)`, alphaRef)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := f.db.Exec(`INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, 'plan', ?, 'hash', '2026-10-12T00:00:00Z')`, id, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, depends_on,
		checkboxes_total, checkboxes_done, doc_status) VALUES (?, 1, 'Elsewhere', ?, '[]', 1, 0, 'in_progress')`,
		id, filepath.Join(dir, "phase-1.md")); err != nil {
		t.Fatal(err)
	}
	return id, dir, phaseIDs(t, f, id)
}

// Two projects share an external id: every lookup stays in the session's
// project, a phaseId pins the plan exactly, and an unscoped lookup is
// deterministic.
func TestLinkPlanSharedExternalIDAcrossProjects(t *testing.T) {
	f := newPlanFixture(t)
	otherID, otherDir, other := otherAlpha(t, f)
	f.addSession(t, "s-proj", 1, "")
	if _, err := f.db.Exec(`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at)
		VALUES (3, 's-other', '/work/other', 'active', '2026-10-14T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}

	// select by ref, no phase: each session gets its own project's plan.
	for _, tc := range []struct{ session, cwd, want string }{
		{"s-proj", "", "Alpha plan"},
		{"s-other", "", "Other alpha"},
		{"s-new", "/work/proj/sub", "Alpha plan"}, // no sessions row yet: the body's cwd scopes it
		{"s-new2", "/work/other", "Other alpha"},
	} {
		body := fmt.Sprintf(`{"session":%q,"cwd":%q,"taskId":%q}`, tc.session, tc.cwd, alphaRef)
		code, resp := f.tok(t, http.MethodPost, "/api/link/plan/select", body)
		if code != http.StatusOK {
			t.Fatalf("select %s = %d (%s)", tc.session, code, resp)
		}
		if v := decodeView(t, resp); v.Title != tc.want {
			t.Errorf("select from %s = %q, want %q", tc.session, v.Title, tc.want)
		}
	}

	// GET of the terminal pick (task_id = the shared ref, no phase) stays in
	// the session's project.
	for _, tc := range []struct{ session, want string }{{"s-proj", "Alpha plan"}, {"s-other", "Other alpha"}} {
		code, resp := f.tok(t, http.MethodGet, "/api/link/plan?session="+tc.session, "")
		if code != http.StatusOK {
			t.Fatalf("get %s = %d (%s)", tc.session, code, resp)
		}
		if v := decodeView(t, resp); v.Title != tc.want || v.ChosenBy != planChosenTerminal {
			t.Errorf("get %s = %q/%s, want %q terminal", tc.session, v.Title, v.ChosenBy, tc.want)
		}
	}

	// A phaseId pins the plan through its row, even unscoped and even though
	// the other plan is the newer task: the tick lands in project 1's doc.
	if code, resp := f.tok(t, http.MethodPost, "/api/link/plan/tick",
		tickBody(alphaRef, sid(f.alpha[1]), `,"idx":1,"done":true`)); code != http.StatusOK {
		t.Fatalf("tick alpha phase 2 = %d (%s)", code, resp)
	}
	if got := readDoc(t, filepath.Join(f.alphaDir, "phase-2.md")); !strings.Contains(got, "- [x] tick flips the doc") {
		t.Errorf("project 1's doc not ticked:\n%s", got)
	}
	if got := readDoc(t, filepath.Join(otherDir, "phase-1.md")); strings.Contains(got, "[x]") {
		t.Errorf("the other project's doc was ticked:\n%s", got)
	}
	// The other plan's phase under the shared ref is that plan's, from any session.
	if code, resp := f.tok(t, http.MethodPost, "/api/link/plan/tick",
		fmt.Sprintf(`{"session":"s-proj","taskId":%q,"phaseId":%q,"idx":0,"done":true}`, alphaRef, sid(other[0]))); code != http.StatusOK {
		t.Fatalf("tick other phase 1 = %d (%s)", code, resp)
	}
	if got := readDoc(t, filepath.Join(otherDir, "phase-1.md")); !strings.Contains(got, "- [x] the other project's criterion") {
		t.Errorf("the other project's doc not ticked by its phase id:\n%s", got)
	}
	// A phase of another plan under a ref that does not name it: 404.
	if code, _ := f.tok(t, http.MethodPost, "/api/link/plan/tick",
		tickBody(betaRef, sid(other[0]), `,"idx":0,"done":true`)); code != http.StatusNotFound {
		t.Errorf("foreign phase under beta = %d, want 404", code)
	}

	// Unscoped (no session row, no cwd) and no phase: deterministic, the newest
	// unarchived plan — the same answer every time.
	for i := 0; i < 3; i++ {
		_, resp := f.tok(t, http.MethodPost, "/api/link/plan/select", `{"session":"s-loose","taskId":"`+alphaRef+`"}`)
		if v := decodeView(t, resp); v.Title != "Other alpha" {
			t.Fatalf("unscoped select #%d = %q, want the newest (Other alpha)", i, v.Title)
		}
	}

	// terminal-sessions lists only the sessions of each plan's own project
	// (s-new / s-new2 / s-loose have no sessions row and are left out).
	list := func(taskID int64) string {
		t.Helper()
		code, body := f.do(t, http.MethodGet, fmt.Sprintf("/api/epics/%d/terminal-sessions", taskID), "", "")
		if code != http.StatusOK {
			t.Fatalf("terminal-sessions %d = %d (%s)", taskID, code, body)
		}
		var got []terminalSessionDTO
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		var uuids []string
		for _, d := range got {
			uuids = append(uuids, d.SessionUUID)
		}
		return strings.Join(uuids, ",")
	}
	if got := list(f.alphaID); got != "s-proj" {
		t.Errorf("alpha terminal-sessions = %q, want s-proj only", got)
	}
	if got := list(otherID); got != "s-other" {
		t.Errorf("other terminal-sessions = %q, want s-other only", got)
	}

	// A row keyed by the numeric task id, or pinned to one of the plan's
	// phases, counts whatever project the session sits in.
	if _, err := f.db.Exec(`UPDATE session_plan SET task_id = ?, phase_id = NULL WHERE session_id = 's-proj'`, sid(otherID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE session_plan SET task_id = ?, phase_id = ? WHERE session_id = 's-other'`, alphaRef, sid(f.alpha[1])); err != nil {
		t.Fatal(err)
	}
	if got := list(f.alphaID); got != "s-other" {
		t.Errorf("alpha terminal-sessions after re-pin = %q, want s-other (pinned to alpha's phase)", got)
	}
	if got := list(otherID); got != "s-proj" {
		t.Errorf("other terminal-sessions after re-pin = %q, want s-proj (numeric id)", got)
	}
}
