package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retroanalysis"
)

// planRecorder is a planning runner that records every spawn and, when block
// is non-nil, keeps the run in flight so ErrActive can be provoked.
type planRecorder struct {
	block chan struct{}
	mu    sync.Mutex
	specs []planning.RunSpec
}

func (p *planRecorder) Start(ctx context.Context, spec planning.RunSpec) (*planning.Run, error) {
	p.mu.Lock()
	p.specs = append(p.specs, spec)
	block := p.block
	p.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	return &planning.Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

func (p *planRecorder) seen() []planning.RunSpec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]planning.RunSpec(nil), p.specs...)
}

// planHandoffServer wires the seeded report database to a handler with BOTH an
// inline analysis service and a real planning service over a stub runner.
func planHandoffServer(t *testing.T, out string, rec *planRecorder) (*httptest.Server, *sql.DB, *retroanalysis.Service) {
	t.Helper()
	db := seedRetroReportDB(t)
	// seedRetroReportDB's project has no path; planning needs one.
	if _, err := db.Exec(`UPDATE projects SET path = '/repo/alpha' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	svc := &retroanalysis.Service{
		DB: db, Runner: &analysisRunner{out: out},
		Go:  func(fn func()) { fn() },
		Now: func() time.Time { return time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC) },
	}
	psvc := planning.NewService(db, rec)
	psvc.UUID = func() string { return "uuid-plan" }
	AttachPlanning(psvc)
	t.Cleanup(func() {
		psvc.Cancel(1)
		for i := 0; i < 400 && psvc.Snapshot(1).Active; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		AttachPlanning(nil)
	})

	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db, RetroAnalysis: svc})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

// startAndDecide runs one analysis and moves it to `status`.
func startAndDecide(t *testing.T, srv *httptest.Server, svc *retroanalysis.Service, status string) int64 {
	t.Helper()
	code, body := analysisReq(t, srv, "POST", "/api/retro/analysis?"+retroRange(7), `{}`)
	if code != http.StatusAccepted {
		t.Fatalf("start = %d %s", code, body)
	}
	id := decodeAnalysis(t, body).ID
	if status == "" {
		return id
	}
	if _, err := svc.Decide(id, status); err != nil {
		t.Fatalf("decide %s: %v", status, err)
	}
	return id
}

func planPath(id int64) string {
	return "/api/retro/analysis/" + strconv.FormatInt(id, 10) + "/plan"
}

func TestAcceptedAnalysisStartsExactlyOnePlanningSession(t *testing.T) {
	rec := &planRecorder{}
	srv, db, svc := planHandoffServer(t, analysisValidOut, rec)
	id := startAndDecide(t, srv, svc, "accepted")

	code, body := analysisReq(t, srv, "POST", planPath(id), `{"projectId":1}`)
	if code != http.StatusAccepted {
		t.Fatalf("plan = %d %s, want 202", code, body)
	}
	var resp struct {
		SessionUUID string `json:"sessionUuid"`
		ProjectSlug string `json:"projectSlug"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if resp.SessionUUID != "uuid-plan" {
		t.Errorf("sessionUuid = %q, want the planner's uuid", resp.SessionUUID)
	}
	if resp.ProjectSlug != "-work-alpha" {
		t.Errorf("projectSlug = %q, want the project's slug for the deep link", resp.ProjectSlug)
	}

	specs := rec.seen()
	if len(specs) != 1 {
		t.Fatalf("planner spawns = %d, want exactly 1", len(specs))
	}
	// The idea is the CHANGE section plus provenance — not the whole analysis.
	// It reaches the runner inside the planner prompt.
	idea := specs[0].Prompt
	if !strings.Contains(idea, "Додати правило схвалення") {
		t.Errorf("idea does not carry the change section:\n%s", idea)
	}
	if strings.Contains(idea, "## Що болить") || strings.Contains(idea, "tech-lead провалює") {
		t.Errorf("idea carries the diagnosis sections too:\n%s", idea)
	}
	if !strings.Contains(idea, "the whole agent fleet") {
		t.Errorf("idea has no provenance header:\n%s", idea)
	}

	var status, uuid string
	if err := db.QueryRow(
		`SELECT status, COALESCE(planning_session_uuid,'') FROM retro_analyses WHERE id = ?`, id,
	).Scan(&status, &uuid); err != nil {
		t.Fatal(err)
	}
	if status != "planned" || uuid != "uuid-plan" {
		t.Errorf("row = (%s, %s), want (planned, uuid-plan)", status, uuid)
	}
}

// SC-5: every non-accepted status refuses, in words, and never spawns.
func TestPlanRefusesEveryNonAcceptedStatus(t *testing.T) {
	cases := []struct {
		name, decide, wantText string
		runnerOut              string
	}{
		{name: "proposed", decide: "", wantText: "has not been accepted yet", runnerOut: analysisValidOut},
		{name: "dismissed", decide: "dismissed", wantText: "was dismissed", runnerOut: analysisValidOut},
		{name: "failed", decide: "", wantText: "failed and has nothing to plan from",
			runnerOut: "## Що болить\nx\n\n## Чому\ny\n\n## Що я б змінив\nz\n"}, // uncited ⇒ failed
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &planRecorder{}
			srv, _, svc := planHandoffServer(t, c.runnerOut, rec)
			id := startAndDecide(t, srv, svc, c.decide)
			code, body := analysisReq(t, srv, "POST", planPath(id), `{"projectId":1}`)
			if code != http.StatusConflict {
				t.Fatalf("plan = %d %s, want 409", code, body)
			}
			if !strings.Contains(body, c.wantText) {
				t.Errorf("409 body = %s, want it to say %q", body, c.wantText)
			}
			if n := len(rec.seen()); n != 0 {
				t.Errorf("planner spawns = %d, want 0 — the gate leaked", n)
			}
		})
	}
}

func TestPlanRefusesAnAlreadyPlannedAnalysis(t *testing.T) {
	rec := &planRecorder{}
	srv, _, svc := planHandoffServer(t, analysisValidOut, rec)
	id := startAndDecide(t, srv, svc, "accepted")
	if code, body := analysisReq(t, srv, "POST", planPath(id), `{"projectId":1}`); code != 202 {
		t.Fatalf("first plan = %d %s", code, body)
	}
	code, body := analysisReq(t, srv, "POST", planPath(id), `{"projectId":1}`)
	if code != http.StatusConflict {
		t.Fatalf("second plan = %d %s, want 409", code, body)
	}
	if !strings.Contains(body, "already started a planning session") {
		t.Errorf("409 body = %s", body)
	}
	if n := len(rec.seen()); n != 1 {
		t.Errorf("planner spawns = %d, want 1", n)
	}
}

// SC-7: an active planning run answers with the ACTIVE session's uuid, so the
// UI can link there instead of printing a bare 409.
func TestPlanReportsAnActivePlanningSession(t *testing.T) {
	block := make(chan struct{})
	rec := &planRecorder{block: block}
	srv, _, svc := planHandoffServer(t, analysisValidOut, rec)
	id := startAndDecide(t, srv, svc, "accepted")

	// Occupy the project's planning slot through the normal endpoint.
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "something else"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("seed planning = %d, want 202", resp.StatusCode)
	}

	code, body := analysisReq(t, srv, "POST", planPath(id), `{"projectId":1}`)
	if code != http.StatusConflict {
		t.Fatalf("plan while active = %d %s, want 409", code, body)
	}
	var out struct {
		Error       string `json:"error"`
		SessionUUID string `json:"sessionUuid"`
		ProjectSlug string `json:"projectSlug"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if !strings.Contains(out.Error, "already active") {
		t.Errorf("error = %q, want a human sentence", out.Error)
	}
	if out.SessionUUID != "uuid-plan" {
		t.Errorf("sessionUuid = %q, want the ACTIVE session's uuid so the UI can link to it", out.SessionUUID)
	}
	if out.ProjectSlug == "" {
		t.Error("projectSlug is empty — the UI cannot build the link")
	}
	close(block)
}

// SC-6 negative: an idea over the planner's limit is refused with the numbers
// in the text, and nothing is spawned.
func TestPlanRefusesAnOversizedIdea(t *testing.T) {
	rec := &planRecorder{}
	srv, db, svc := planHandoffServer(t, analysisValidOut, rec)
	id := startAndDecide(t, srv, svc, "accepted")
	// Widen the stored change section past the planner's budget. The validator
	// caps it at generation time, so this models a row written by an older
	// build — exactly the case the endpoint must refuse rather than truncate.
	long := "## Що болить\nx [E:agent:tech-lead]\n\n## Чому\ny [E:rec:1]\n\n## Що я б змінив\n" +
		strings.Repeat("змінити щось конкретне. ", 2500) + "[E:session:sess-alpha]\n"
	if _, err := db.Exec(`UPDATE retro_analyses SET markdown = ? WHERE id = ?`, long, id); err != nil {
		t.Fatal(err)
	}
	code, body := analysisReq(t, srv, "POST", planPath(id), `{"projectId":1}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("plan = %d %s, want 422", code, body)
	}
	if !strings.Contains(body, "100000-byte limit") {
		t.Errorf("422 body = %s, want the limit in the text", body)
	}
	if !strings.Contains(body, "-byte planning idea") {
		t.Errorf("422 body = %s, want the actual length in the text", body)
	}
	if n := len(rec.seen()); n != 0 {
		t.Errorf("planner spawns = %d, want 0", n)
	}
}

func TestPlanValidatesItsInputs(t *testing.T) {
	rec := &planRecorder{}
	srv, _, svc := planHandoffServer(t, analysisValidOut, rec)
	id := startAndDecide(t, srv, svc, "accepted")

	if code, body := analysisReq(t, srv, "POST", planPath(id), `{}`); code != http.StatusBadRequest {
		t.Errorf("missing projectId = %d %s, want 400", code, body)
	}
	if code, _ := analysisReq(t, srv, "POST", planPath(id), `{"projectId":404}`); code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", code)
	}
	if code, _ := analysisReq(t, srv, "POST", planPath(9999), `{"projectId":1}`); code != http.StatusNotFound {
		t.Errorf("unknown analysis = %d, want 404", code)
	}
	if code, _ := analysisReq(t, srv, "POST", "/api/retro/analysis/abc/plan", `{"projectId":1}`); code != http.StatusBadRequest {
		t.Errorf("non-numeric id = %d, want 400", code)
	}
	if n := len(rec.seen()); n != 0 {
		t.Errorf("planner spawns = %d, want 0 on every invalid input", n)
	}
}

func TestPlanRefusesAProjectWithNoPath(t *testing.T) {
	rec := &planRecorder{}
	srv, db, svc := planHandoffServer(t, analysisValidOut, rec)
	id := startAndDecide(t, srv, svc, "accepted")
	if _, err := db.Exec(`UPDATE projects SET path = '' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	code, body := analysisReq(t, srv, "POST", planPath(id), `{"projectId":1}`)
	if code != http.StatusConflict || !strings.Contains(body, "no known path") {
		t.Errorf("plan = %d %s, want 409 naming the missing path", code, body)
	}
}
