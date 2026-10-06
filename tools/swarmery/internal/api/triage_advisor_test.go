package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// advisorJudge answers every part with value (and payload), counting calls.
type advisorJudge struct {
	mu      sync.Mutex
	calls   int
	value   string
	payload json.RawMessage
}

func (j *advisorJudge) Judge(_ context.Context, it triage.Item) (triage.Answer, error) {
	j.mu.Lock()
	j.calls++
	j.mu.Unlock()
	vals := map[string]string{}
	for _, p := range it.Parts {
		vals[p.Ref] = j.value
	}
	return triage.Answer{Values: vals, Reason: "judged", Payload: j.payload}, nil
}

// advisorFixture: projects p (1) and q (2), session u1 in p, an improve
// registry that ships tech-lead, and a real engine with the advisor Source.
func advisorFixture(t *testing.T, j *advisorJudge) (*sql.DB, *triage.Service, *advisorSource) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "triage_advisor.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`,
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2,'/repo/q','q','2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions(id, project_id, session_uuid, status, started_at) VALUES(1,1,'u1','completed','2026-01-02T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{DB: db, Improve: &improve.Service{DB: db, Repo: "/repo",
		Exec: &repoAgentsExec{agents: []string{"tech-lead"}}}}
	src := &advisorSource{h: h}
	svc := triage.NewService(db, j)
	svc.Go = func(fn func()) { fn() }
	svc.Register(src)
	return db, svc, src
}

func seedRec(t *testing.T, db *sql.DB, id int64, rule, kind, target, evidence string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO recommendations
		(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Detail', ?, 'proposed', ?, '2026-01-03T00:00:00Z', '2026-01-03T00:00:00Z')`,
		id, rule, kind, target, rule+" title", evidence, rule+":"+target); err != nil {
		t.Fatal(err)
	}
}

func recStatus(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM recommendations WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func runAdvisor(t *testing.T, svc *triage.Service, project int64) []triage.Verdict {
	t.Helper()
	id, err := svc.Start(triage.StartReq{Scope: triage.Scope{ProjectID: project}, Kinds: []string{"advisor"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	vs, err := svc.ListVerdicts(triage.VerdictFilter{RunID: id})
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func collectByRef(t *testing.T, src *advisorSource, project int64) map[string]triage.Item {
	t.Helper()
	items, err := src.Collect(context.Background(), triage.Scope{ProjectID: project}, 0)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]triage.Item{}
	for _, it := range items {
		out[it.Parts[0].Ref] = it
	}
	return out
}

// R9 is dismissed by rule: no judge call, an applied verdict with the rule's
// reason; Undo reopens the row, and a second Undo is a no-op returning nil.
func TestTriageAdvisorSourceInformationalAppliedAndUndone(t *testing.T) {
	j := &advisorJudge{value: "dismiss"}
	db, svc, src := advisorFixture(t, j)
	seedRec(t, db, 1, "R9", "session", "s-9", `{"session_ids":["u1"]}`)

	vs := runAdvisor(t, svc, 0)
	if j.calls != 0 {
		t.Fatalf("judge calls = %d, want 0", j.calls)
	}
	if len(vs) != 1 || vs[0].State != triage.StateApplied || vs[0].Value != "dismiss" ||
		vs[0].Reason != advisorInformationalReason || vs[0].Class != triage.ClassInformational {
		t.Fatalf("verdicts = %+v", vs)
	}
	if got := recStatus(t, db, 1); got != "dismissed" {
		t.Fatalf("status after run = %s", got)
	}
	if _, err := svc.Undo(context.Background(), vs[0].ID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if got := recStatus(t, db, 1); got != "proposed" {
		t.Fatalf("status after undo = %s", got)
	}
	if err := src.Undo(context.Background(), vs[0]); err != nil {
		t.Fatalf("second undo = %v, want nil", err)
	}
	if got := recStatus(t, db, 1); got != "proposed" {
		t.Fatalf("status after second undo = %s", got)
	}
}

// R3 with a project is a card: a judge dismiss is only suggested, the row stays
// proposed; a fix-card answer keeps its payload and the verdict's project.
func TestTriageAdvisorSourceCardSuggestsOnly(t *testing.T) {
	j := &advisorJudge{value: "dismiss"}
	db, svc, _ := advisorFixture(t, j)
	seedRec(t, db, 1, "R3", "error_group", "eg-1", `{"session_ids":["u1"]}`)

	vs := runAdvisor(t, svc, 0)
	if j.calls != 1 || len(vs) != 1 || vs[0].State != triage.StateSuggested ||
		vs[0].Class != triage.ClassCard || vs[0].Value != "dismiss" {
		t.Fatalf("calls=%d verdicts=%+v", j.calls, vs)
	}
	if got := recStatus(t, db, 1); got != "proposed" {
		t.Fatalf("a run dismissed a card recommendation: %s", got)
	}

	j.value = "fix-card"
	j.payload = json.RawMessage(`{"title":"Fix eg-1","prompt":"Do the thing and verify it."}`)
	seedRec(t, db, 2, "R3", "error_group", "eg-2", `{"session_ids":["u1"]}`)
	vs = runAdvisor(t, svc, 0)
	var fix *triage.Verdict
	for i := range vs {
		if vs[i].Ref == "2" {
			fix = &vs[i]
		}
	}
	if fix == nil || fix.State != triage.StateSuggested || fix.Value != "fix-card" ||
		fix.ProjectID == nil || *fix.ProjectID != 1 {
		t.Fatalf("fix-card verdict = %+v (all %+v)", fix, vs)
	}
	var p struct{ Title, Prompt string }
	if err := json.Unmarshal(fix.Payload, &p); err != nil || p.Title != "Fix eg-1" || p.Prompt == "" {
		t.Fatalf("payload = %s (%v)", fix.Payload, err)
	}
	if got := recStatus(t, db, 2); got != "proposed" {
		t.Fatalf("fix-card row status = %s", got)
	}
}

// Class table: R3 without a project is plain; R2 is improve only when its
// agent is in the registry; R11 (skill) is improve; R9 is informational.
func TestTriageAdvisorSourceClasses(t *testing.T) {
	db, _, src := advisorFixture(t, &advisorJudge{})
	seedRec(t, db, 1, "R3", "error_group", "eg", `{"session_ids":["nowhere"]}`)
	seedRec(t, db, 2, "R2", "agent", "tech-lead", `{}`)
	seedRec(t, db, 3, "R2", "agent", "ghost", `{}`)
	seedRec(t, db, 4, "R11", "skill", "lesson-x", `{}`)
	seedRec(t, db, 5, "R9", "session", "s", `{"blob":"`+strings.Repeat("é", 2000)+`"}`)

	got := collectByRef(t, src, 0)
	want := map[string]string{"1": triage.ClassPlain, "2": triage.ClassImprove, "3": triage.ClassPlain,
		"4": triage.ClassImprove, "5": triage.ClassInformational}
	for ref, class := range want {
		if got[ref].Class != class {
			t.Errorf("ref %s class = %q, want %q", ref, got[ref].Class, class)
		}
	}
	if a := got["1"].Parts[0].Allowed; len(a) != 2 || a[0] != "track" || got["1"].ProjectID != 0 {
		t.Errorf("plain item = %+v", got["1"])
	}
	if a := got["2"].Parts[0].Allowed; len(a) != 2 || a[0] != "improve" || !strings.HasPrefix(got["2"].Instruction, "improve when") {
		t.Errorf("improve item = %+v", got["2"])
	}
	inf := got["5"]
	if len(inf.Parts[0].Allowed) != 1 || inf.Instruction != "" || inf.Title != "R9 title" ||
		inf.WaitingSince != "2026-01-03T00:00:00Z" || inf.Key != "5" {
		t.Errorf("informational item = %+v", inf)
	}
	ev := inf.Evidence[strings.Index(inf.Evidence, "evidence: ")+len("evidence: "):]
	if len(ev) > advisorEvidenceMax || !strings.HasPrefix(inf.Evidence, "rule: R9\n") || !utf8Valid(ev) {
		t.Errorf("evidence cut = %d bytes, valid=%v", len(ev), utf8Valid(ev))
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// A scoped collect returns only the project's recommendations (evidence or a
// project target); a fleet collect attributes each row to its own project.
func TestTriageAdvisorSourceScopedCollect(t *testing.T) {
	db, svc, src := advisorFixture(t, &advisorJudge{value: "dismiss"})
	seedRec(t, db, 1, "R3", "error_group", "eg", `{"session_ids":["u1"]}`)
	seedRec(t, db, 2, "R7", "project", "q", `{}`)
	seedRec(t, db, 3, "R5", "process", "x", `{}`)

	if vs := runAdvisor(t, svc, 2); len(vs) != 1 || vs[0].Ref != "2" {
		t.Fatalf("scoped run verdicts = %+v", vs)
	}

	p := collectByRef(t, src, 1)
	if len(p) != 1 || p["1"].ProjectID != 1 || p["1"].Class != triage.ClassCard {
		t.Fatalf("scope p = %+v", p)
	}
	q := collectByRef(t, src, 2)
	if len(q) != 1 || q["2"].ProjectID != 2 || q["2"].Class != triage.ClassCard {
		t.Fatalf("scope q = %+v", q)
	}
	all := collectByRef(t, src, 0)
	if len(all) != 3 || all["1"].ProjectID != 1 || all["2"].ProjectID != 2 || all["3"].ProjectID != 0 {
		t.Fatalf("fleet = %+v", all)
	}
	if _, err := db.Exec(`UPDATE recommendations SET status='accepted' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if got := collectByRef(t, src, 1); len(got) != 0 {
		t.Fatalf("collect lists a non-proposed row: %+v", got)
	}
}

// Open follows the status only; Apply refuses anything but an informational
// dismiss and a row that left proposed.
func TestTriageAdvisorSourceOpenAndApplyBackstop(t *testing.T) {
	db, _, src := advisorFixture(t, &advisorJudge{})
	ctx := context.Background()
	seedRec(t, db, 1, "R9", "session", "s", `{}`)
	if ok, err := src.Open(ctx, "1"); !ok || err != nil {
		t.Fatalf("open proposed = %v %v", ok, err)
	}
	if _, err := db.Exec(`UPDATE recommendations SET updated_at = '2026-02-01T00:00:00Z' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := src.Open(ctx, "1"); !ok {
		t.Fatal("a refreshed updated_at closed the item")
	}
	if ok, err := src.Open(ctx, "99"); ok || err != nil {
		t.Fatalf("open unknown = %v %v", ok, err)
	}

	part := triage.Part{Ref: "1", Allowed: []string{"dismiss"}}
	card := triage.Item{Kind: "advisor", Class: triage.ClassCard, Parts: []triage.Part{part}}
	if _, err := src.Apply(ctx, card, part, "dismiss", "", nil); err == nil {
		t.Fatal("Apply dismissed a card item")
	}
	inf := triage.Item{Kind: "advisor", Class: triage.ClassInformational, Parts: []triage.Part{part}}
	a, err := src.Apply(ctx, inf, part, "dismiss", "", nil)
	if err != nil || string(a.Prior) != `{"status":"proposed"}` {
		t.Fatalf("apply = %+v %v", a, err)
	}
	if ok, _ := src.Open(ctx, "1"); ok {
		t.Fatal("Open still true after the row left proposed")
	}
	if _, err := src.Apply(ctx, inf, part, "dismiss", "", nil); err == nil ||
		err.Error() != "recommendation is no longer proposed" {
		t.Fatalf("second apply = %v", err)
	}
}

// NewServer registers the advisor Source on the attached triage service.
func TestTriageAdvisorSourceRegisteredByNewServer(t *testing.T) {
	_, svc, _ := serverWithTriage(t)
	seedRec(t, svc.DB, 7, "R9", "session", "s", `{}`)
	vs := runAdvisor(t, svc, 0)
	if len(vs) != 1 || vs[0].Kind != "advisor" || vs[0].State != triage.StateApplied {
		t.Fatalf("verdicts = %+v", vs)
	}
}

// NewServer also registers the retire Source, with the SAME verification config
// the accept path confirms retirements with (lessonVerifyCfg) — main.go attaches
// that config only after it builds the triage service, so it cannot register it.
func TestTriageRetireSourceRegisteredByNewServer(t *testing.T) {
	_, svc, _ := serverWithTriage(t)
	lessonID := seedSurpriseLesson(t, svc.DB, "old", "active")
	propID := seedRetirement(t, svc.DB, lessonID)
	id, err := svc.Start(triage.StartReq{Kinds: []string{"retire"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	vs, err := svc.ListVerdicts(triage.VerdictFilter{RunID: id, Kind: "retire"})
	if err != nil {
		t.Fatal(err)
	}
	// The stub judge answers "noise", which a retirement does not allow, so the
	// verdict is rejected — what matters here is that the proposal was collected.
	if len(vs) != 1 || vs[0].Ref != strconv.FormatInt(propID, 10) || vs[0].State != triage.StateRejected {
		t.Fatalf("retire verdicts = %+v, want one rejected verdict for proposal %d", vs, propID)
	}
}
