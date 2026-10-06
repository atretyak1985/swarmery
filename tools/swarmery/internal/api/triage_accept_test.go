package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// acceptFixture is advisorFixture (projects p=1 and q=2, the advisor source, a
// registry shipping tech-lead) plus the lesson and retire sources, attached and
// served through the real routes. Suggested verdicts are put in place by direct
// INSERTs into triage_verdicts under one real (empty) triage run, because the
// triage service's insert seam is unexported.
type acceptFixture struct {
	db    *sql.DB
	svc   *triage.Service
	h     *Handler
	url   string
	runID int64
}

func newAcceptFixture(t *testing.T) *acceptFixture {
	t.Helper()
	db, svc, _ := advisorFixture(t, &advisorJudge{value: "dismiss"})
	svc.Register(&triage.LessonSource{DB: db})
	svc.Register(&triage.RetireSource{DB: db, Cfg: lessons.DefaultVerifyConfig()})
	runID, err := svc.Start(triage.StartReq{Kinds: []string{"advisor"}}) // no recommendations yet: an empty run
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	AttachTriage(svc)
	t.Cleanup(func() { AttachTriage(nil) })
	h := &Handler{DB: db, Improve: &improve.Service{DB: db, Repo: "/repo",
		Exec: &repoAgentsExec{agents: []string{"tech-lead"}}}}
	h.improveGo = func(func()) {} // never run the improve pipeline
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &acceptFixture{db: db, svc: svc, h: h, url: srv.URL, runID: runID}
}

func (f *acceptFixture) verdict(t *testing.T, kind, class, ref, value, state string, project int64, payload string) int64 {
	t.Helper()
	var pid any
	if project != 0 {
		pid = project
	}
	res, err := f.db.Exec(`INSERT INTO triage_verdicts(run_id, kind, class, ref, item_key, title, value, reason,
		payload, prior, state, created_at, project_id) VALUES(?,?,?,?,?,'t',?,'judge reason',?,'{}',?,'2026-10-06T12:00:00.000Z',?)`,
		f.runID, kind, class, ref, kind+":"+ref, value, payload, state, pid)
	if err != nil {
		t.Fatalf("seed verdict: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *acceptFixture) state(t *testing.T, id int64) triage.Verdict {
	t.Helper()
	v, err := f.svc.GetVerdict(id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *acceptFixture) accept(t *testing.T, id int64) (int, []byte) {
	t.Helper()
	resp, body := doRoutineReq(t, http.MethodPost, fmt.Sprintf("%s/api/triage/verdicts/%d/accept", f.url, id), nil)
	return resp.StatusCode, body
}

func (f *acceptFixture) scalar(t *testing.T, q string, args ...any) string {
	t.Helper()
	var s string
	if err := f.db.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (f *acceptFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func seedSurpriseLesson(t *testing.T, db *sql.DB, title, status string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO surprise_lessons(source_phase_run, phase_id, seq, title, guidance,
		area_globs, cause, source_paragraph, surprise_index, status, recurrences, created_at, updated_at)
		VALUES(?, 1, 1, ?, ?, 'internal/api/**', 'plan-gap', 'para', 0.42, ?, 3,
		       '2026-10-01T10:00:00.000Z', '2026-10-01T10:00:00.000Z')`,
		"run-"+title, title, "Guide: "+title, status)
	if err != nil {
		t.Fatalf("seed lesson: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedRetirement(t *testing.T, db *sql.DB, lessonID int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO lesson_retirements(lesson_id, reason, detail, evidence_json, state, proposed_at)
		VALUES(?, 'unused_60d', 'not injected', '{}', 'proposed', '2026-10-02T09:00:00.000Z')`, lessonID)
	if err != nil {
		t.Fatalf("seed retirement: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestTriageAcceptLessonAndRetire(t *testing.T) {
	f := newAcceptFixture(t)
	l1 := seedSurpriseLesson(t, f.db, "one", "candidate")
	l2 := seedSurpriseLesson(t, f.db, "two", "candidate")
	l3 := seedSurpriseLesson(t, f.db, "three", "active")
	l4 := seedSurpriseLesson(t, f.db, "four", "active")
	r3, r4 := seedRetirement(t, f.db, l3), seedRetirement(t, f.db, l4)

	cases := []struct {
		kind, value string
		ref         int64
		q, want     string
	}{
		{"lesson", "accept", l1, `SELECT status FROM surprise_lessons WHERE id=?`, "active"},
		{"lesson", "not-useful", l2, `SELECT status FROM surprise_lessons WHERE id=?`, "dismissed"},
		{"retire", "stop", r3, `SELECT state FROM lesson_retirements WHERE id=?`, lessons.ProposalConfirmed},
		{"retire", "keep", r4, `SELECT state FROM lesson_retirements WHERE id=?`, lessons.ProposalKept},
	}
	for _, c := range cases {
		id := f.verdict(t, c.kind, "", fmt.Sprint(c.ref), c.value, triage.StateSuggested, 1, `{}`)
		if code, body := f.accept(t, id); code != http.StatusOK {
			t.Fatalf("%s/%s: status %d %s", c.kind, c.value, code, body)
		}
		if got := f.scalar(t, c.q, c.ref); got != c.want {
			t.Errorf("%s/%s: source row = %q, want %q", c.kind, c.value, got, c.want)
		}
		if v := f.state(t, id); v.State != triage.StateAccepted {
			t.Errorf("%s/%s: verdict state %q", c.kind, c.value, v.State)
		}
	}
}

func TestTriageAcceptAdvisorDismissTrackImprove(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 10, "R1", "agent", "x-agent", "{}")
	seedRec(t, f.db, 11, "R2", "agent", "y-agent", "{}")
	seedRec(t, f.db, 12, "R3", "agent", "tech-lead", "{}")
	for _, c := range []struct {
		ref               int64
		class, value, rec string
	}{
		{10, triage.ClassPlain, "dismiss", "dismissed"},
		{11, triage.ClassPlain, "track", "accepted"},
		{12, triage.ClassImprove, "improve", "accepted"},
	} {
		id := f.verdict(t, "advisor", c.class, fmt.Sprint(c.ref), c.value, triage.StateSuggested, 1, `{}`)
		code, body := f.accept(t, id)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d %s", c.value, code, body)
		}
		var v triage.Verdict
		decodeInto(t, body, &v)
		if v.State != triage.StateAccepted {
			t.Errorf("%s: verdict %q", c.value, v.State)
		}
		if got := recStatus(t, f.db, c.ref); got != c.rec {
			t.Errorf("%s: recommendation %q, want %q", c.value, got, c.rec)
		}
	}
}

func TestTriageAcceptFixCard(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 20, "R1", "agent", "tech-lead", "{}")
	long := strings.Repeat("é", 100)
	prompt := strings.Repeat("ж", 5000) // 10,000 bytes
	payload, _ := json.Marshal(map[string]any{"title": long, "prompt": prompt, "projectId": 2, "id": 99})
	id := f.verdict(t, "advisor", triage.ClassCard, "20", "fix-card", triage.StateSuggested, 1, string(payload))

	code, body := f.accept(t, id)
	if code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Fatalf("cards = %d, want 1", n)
	}
	var title, gotPrompt, column, ext string
	var project int64
	if err := f.db.QueryRow(`SELECT title, prompt, board_column, external_id, project_id FROM tasks`).
		Scan(&title, &gotPrompt, &column, &ext, &project); err != nil {
		t.Fatal(err)
	}
	if column != "todo" || project != 1 {
		t.Errorf("card column %q project %d, want todo / 1 (never the payload's)", column, project)
	}
	if utf8.RuneCountInString(title) != 80 {
		t.Errorf("title runes = %d, want 80", utf8.RuneCountInString(title))
	}
	footer := "\n\nRecommendation #20 (R1): R1 title"
	if !strings.HasSuffix(gotPrompt, footer) {
		t.Errorf("prompt does not end with the footer: …%q", gotPrompt[len(gotPrompt)-60:])
	}
	if n := len(strings.TrimSuffix(gotPrompt, footer)); n > 8000 || !utf8.ValidString(gotPrompt) {
		t.Errorf("judge prompt part = %d bytes (valid utf8 %v), want <= 8000", n, utf8.ValidString(gotPrompt))
	}
	if got := recStatus(t, f.db, 20); got != "accepted" {
		t.Errorf("recommendation %q, want accepted", got)
	}
	var stored map[string]any
	decodeInto(t, f.state(t, id).Payload, &stored)
	if stored["cardId"] != ext {
		t.Errorf("payload cardId = %v, want %s", stored["cardId"], ext)
	}

	// A retry is refused and creates no second card.
	if code, body := f.accept(t, id); code != http.StatusConflict || !strings.Contains(string(body), `"state":"accepted"`) {
		t.Fatalf("second accept: %d %s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Fatalf("cards after retry = %d, want 1", n)
	}
}

func TestTriageAcceptFixCardInvalid(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 30, "R1", "agent", "tech-lead", "{}")
	seedRec(t, f.db, 31, "R6", "agent", "tech-lead", "{}")
	noProject := f.verdict(t, "advisor", triage.ClassCard, "30", "fix-card", triage.StateSuggested, 0,
		`{"title":"T","prompt":"P"}`)
	noPrompt := f.verdict(t, "advisor", triage.ClassCard, "31", "fix-card", triage.StateSuggested, 1, `{"title":"T"}`)
	for _, c := range []struct {
		id  int64
		rec int64
	}{{noProject, 30}, {noPrompt, 31}} {
		if code, body := f.accept(t, c.id); code != http.StatusUnprocessableEntity {
			t.Errorf("verdict %d: status %d %s, want 422", c.id, code, body)
		}
		if v := f.state(t, c.id); v.State != triage.StateSuggested {
			t.Errorf("verdict %d: state %q, want suggested", c.id, v.State)
		}
		if got := recStatus(t, f.db, c.rec); got != "proposed" {
			t.Errorf("recommendation %d: %q, want proposed", c.rec, got)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tasks`); n != 0 {
		t.Fatalf("cards = %d, want 0", n)
	}
}

func TestTriageAcceptActionFailureReopens(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 40, "R1", "agent", "tech-lead", "{}")
	// Project 99 does not exist: CreateTask fails on the foreign key.
	id := f.verdict(t, "advisor", triage.ClassCard, "40", "fix-card", triage.StateSuggested, 99,
		`{"title":"T","prompt":"P"}`)
	code, body := f.accept(t, id)
	if code < 400 || !strings.Contains(string(body), "create card") {
		t.Fatalf("status %d %s, want the card creation to fail", code, body)
	}
	v := f.state(t, id)
	if v.State != triage.StateSuggested || v.DecidedAt != nil {
		t.Fatalf("verdict after failed action: %q decided %v, want suggested", v.State, v.DecidedAt)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tasks`); n != 0 {
		t.Fatalf("cards = %d, want 0", n)
	}
}

func TestTriageAcceptStaleAndRefusals(t *testing.T) {
	f := newAcceptFixture(t)
	// Decided elsewhere: the lesson is no longer a candidate.
	l := seedSurpriseLesson(t, f.db, "gone", "dismissed")
	stale := f.verdict(t, "lesson", "", fmt.Sprint(l), "accept", triage.StateSuggested, 1, `{}`)
	code, body := f.accept(t, stale)
	if code != http.StatusConflict || !strings.Contains(string(body), `"the item changed"`) ||
		!strings.Contains(string(body), `"state":"stale"`) {
		t.Fatalf("stale accept: %d %s", code, body)
	}
	if got := f.scalar(t, `SELECT status FROM surprise_lessons WHERE id=?`, l); got != "dismissed" {
		t.Errorf("lesson changed to %q", got)
	}
	if v := f.state(t, stale); v.State != triage.StateStale {
		t.Errorf("verdict %q, want stale", v.State)
	}

	c := seedSurpriseLesson(t, f.db, "cand", "candidate")
	for _, st := range []string{triage.StateSample, triage.StateApplied} {
		id := f.verdict(t, "lesson", "", fmt.Sprint(c), "accept", st, 1, `{}`)
		if code, body := f.accept(t, id); code != http.StatusConflict ||
			!strings.Contains(string(body), `"state":"`+st+`"`) {
			t.Errorf("%s verdict: %d %s", st, code, body)
		}
	}
	if got := f.scalar(t, `SELECT status FROM surprise_lessons WHERE id=?`, c); got != "candidate" {
		t.Errorf("lesson changed to %q by a refused accept", got)
	}
	if code, _ := f.accept(t, 99999); code != http.StatusNotFound {
		t.Errorf("unknown id: %d, want 404", code)
	}
	resp, _ := doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/abc/accept", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id: %d, want 400", resp.StatusCode)
	}
}

type acceptAllResp struct {
	Accepted []int64 `json:"accepted"`
	Stale    []int64 `json:"stale"`
	Failed   []struct {
		ID    int64  `json:"id"`
		Error string `json:"error"`
	} `json:"failed"`
}

func TestTriageAcceptAll(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 50, "R1", "agent", "x-agent", "{}")
	seedRec(t, f.db, 51, "R1", "agent", "tech-lead", "{}")
	seedRec(t, f.db, 52, "R2", "agent", "y-agent", "{}")
	l := seedSurpriseLesson(t, f.db, "gone", "dismissed")
	cand := seedSurpriseLesson(t, f.db, "cand", "candidate")
	first := f.verdict(t, "advisor", triage.ClassPlain, "50", "dismiss", triage.StateSuggested, 1, `{}`)
	second := f.verdict(t, "advisor", triage.ClassCard, "51", "fix-card", triage.StateSuggested, 1, `{"title":"T"}`)
	third := f.verdict(t, "advisor", triage.ClassPlain, "52", "track", triage.StateSuggested, 1, `{}`)
	stale := f.verdict(t, "lesson", "", fmt.Sprint(l), "accept", triage.StateSuggested, 1, `{}`)
	sample := f.verdict(t, "lesson", "", fmt.Sprint(cand), "accept", triage.StateSample, 1, `{}`)

	resp, body := doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/accept-all", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d %s", resp.StatusCode, body)
	}
	var got acceptAllResp
	decodeInto(t, body, &got)
	if fmt.Sprint(got.Accepted) != fmt.Sprint([]int64{first, third}) {
		t.Errorf("accepted = %v, want [%d %d]", got.Accepted, first, third)
	}
	if fmt.Sprint(got.Stale) != fmt.Sprint([]int64{stale}) {
		t.Errorf("stale = %v, want [%d]", got.Stale, stale)
	}
	if len(got.Failed) != 1 || got.Failed[0].ID != second || got.Failed[0].Error == "" {
		t.Errorf("failed = %+v, want verdict %d", got.Failed, second)
	}
	if v := f.state(t, sample); v.State != triage.StateSample {
		t.Errorf("sample verdict touched: %q", v.State)
	}
	if v := f.state(t, second); v.State != triage.StateSuggested {
		t.Errorf("failed verdict %q, want suggested", v.State)
	}

	// Nothing left: empty arrays, never null.
	_, body = doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/accept-all", nil)
	if !strings.Contains(string(body), `"accepted":[]`) || !strings.Contains(string(body), `"stale":[]`) {
		t.Errorf("empty accept-all body %s", body)
	}
}

func TestTriageAcceptAllProjectFilter(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 60, "R1", "agent", "x-agent", "{}")
	seedRec(t, f.db, 61, "R2", "agent", "y-agent", "{}")
	inP := f.verdict(t, "advisor", triage.ClassPlain, "60", "dismiss", triage.StateSuggested, 1, `{}`)
	inQ := f.verdict(t, "advisor", triage.ClassPlain, "61", "dismiss", triage.StateSuggested, 2, `{}`)

	resp, _ := doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/accept-all", map[string]any{"project": "nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project: %d, want 404", resp.StatusCode)
	}
	resp, body := doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/accept-all", map[string]any{"project": "q"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d %s", resp.StatusCode, body)
	}
	var got acceptAllResp
	decodeInto(t, body, &got)
	if fmt.Sprint(got.Accepted) != fmt.Sprint([]int64{inQ}) {
		t.Errorf("accepted = %v, want [%d]", got.Accepted, inQ)
	}
	if v := f.state(t, inP); v.State != triage.StateSuggested {
		t.Errorf("project p verdict %q, want suggested", v.State)
	}
	if got := recStatus(t, f.db, 60); got != "proposed" {
		t.Errorf("recommendation 60 %q, want proposed", got)
	}
}

// Both new routes resolve to their own handler: accept-all is not captured
// by {id}/accept (it would answer 400 "invalid verdict id") or vice versa.
func TestTriageAcceptRoutesResolve(t *testing.T) {
	f := newAcceptFixture(t)
	resp, body := doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/accept-all", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"accepted":`) {
		t.Errorf("accept-all → %d %s, want the accept-all handler", resp.StatusCode, body)
	}
	resp, body = doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/7/accept", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "verdict not found") {
		t.Errorf("{id}/accept → %d %s, want the accept handler's 404", resp.StatusCode, body)
	}
}

// A model-written cardId must not stand in for a card: accept creates one, and
// the stored payload is rebuilt from title, prompt and the real card id only.
func TestTriageAcceptFixCardIgnoresPayloadCardID(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 70, "R1", "agent", "tech-lead", "{}")
	id := f.verdict(t, "advisor", triage.ClassCard, "70", "fix-card", triage.StateSuggested, 1,
		`{"title":"T","prompt":"P","cardId":"x","extra":"junk"}`)

	if code, body := f.accept(t, id); code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Fatalf("cards = %d, want 1", n)
	}
	ext := f.scalar(t, `SELECT external_id FROM tasks`)
	var stored map[string]any
	decodeInto(t, f.state(t, id).Payload, &stored)
	if stored["cardId"] != ext || ext == "x" {
		t.Errorf("payload cardId = %v, want the created card %s", stored["cardId"], ext)
	}
	if _, ok := stored["extra"]; ok || len(stored) != 3 || stored["title"] != "T" || stored["prompt"] != "P" {
		t.Errorf("stored payload = %v, want exactly title, prompt, cardId", stored)
	}
	if got := recStatus(t, f.db, 70); got != "accepted" {
		t.Errorf("recommendation %q, want accepted", got)
	}
}

// accept-all pages through every suggestion in scope, oldest first, and says
// how many are still suggested when it returns.
func TestTriageAcceptAllPagesOldestFirst(t *testing.T) {
	f := newAcceptFixture(t)
	const n = 1100
	want := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		rec := int64(1000 + i)
		seedRec(t, f.db, rec, "R1", "agent", fmt.Sprintf("a%d", i), "{}")
		want = append(want, f.verdict(t, "advisor", triage.ClassPlain, fmt.Sprint(rec), "dismiss", triage.StateSuggested, 1, `{}`))
	}
	type allResp struct {
		acceptAllResp
		Remaining *int `json:"remaining"`
	}
	resp, body := doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/accept-all", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d %.300s", resp.StatusCode, body)
	}
	var got allResp
	decodeInto(t, body, &got)
	if fmt.Sprint(got.Accepted) != fmt.Sprint(want) {
		t.Fatalf("accepted %d verdicts (first %v), want all %d in ascending id order", len(got.Accepted),
			got.Accepted[:min(3, len(got.Accepted))], n)
	}
	if got.Remaining == nil || *got.Remaining != 0 {
		t.Errorf("remaining = %v, want 0", got.Remaining)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM recommendations WHERE status='dismissed'`); c != n {
		t.Errorf("dismissed recommendations = %d, want %d", c, n)
	}

	// One verdict whose action cannot run: it stays suggested and is counted.
	seedRec(t, f.db, 5000, "R1", "agent", "tech-lead", "{}")
	bad := f.verdict(t, "advisor", triage.ClassCard, "5000", "fix-card", triage.StateSuggested, 1, `{"title":"T"}`)
	_, body = doRoutineReq(t, http.MethodPost, f.url+"/api/triage/verdicts/accept-all", nil)
	got = allResp{}
	decodeInto(t, body, &got)
	if got.Remaining == nil || *got.Remaining != 1 {
		t.Errorf("remaining = %v, want 1", got.Remaining)
	}
	if len(got.Failed) != 1 || got.Failed[0].ID != bad {
		t.Errorf("failed = %+v, want verdict %d", got.Failed, bad)
	}
}

// assertRecReopened checks a failed accept left recommendation rec proposed
// (baseline cleared) and verdict id an open suggestion again.
func (f *acceptFixture) assertRecReopened(t *testing.T, id, rec int64) {
	t.Helper()
	if got := recStatus(t, f.db, rec); got != "proposed" {
		t.Errorf("recommendation %d %q, want proposed", rec, got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM recommendations WHERE id=? AND baseline IS NULL`, rec); n != 1 {
		t.Errorf("recommendation %d baseline not cleared", rec)
	}
	v := f.state(t, id)
	if v.State != triage.StateSuggested {
		t.Fatalf("verdict %d %q, want suggested", id, v.State)
	}
	if open, err := f.svc.VerdictOpen(context.Background(), v); err != nil || !open {
		t.Errorf("verdict %d open = %v (%v), want true", id, open, err)
	}
}

// A failed card creation puts the recommendation back to proposed, so the
// suggestion stays retryable; once the cause is fixed the retry succeeds.
func TestTriageAcceptFixCardFailureRestoresRecommendation(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 80, "R1", "agent", "tech-lead", "{}")
	id := f.verdict(t, "advisor", triage.ClassCard, "80", "fix-card", triage.StateSuggested, 99,
		`{"title":"T","prompt":"P"}`)
	if code, body := f.accept(t, id); code < 400 || !strings.Contains(string(body), "create card") {
		t.Fatalf("status %d %s, want the card creation to fail", code, body)
	}
	f.assertRecReopened(t, id, 80)

	if _, err := f.db.Exec(`UPDATE triage_verdicts SET project_id=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if code, body := f.accept(t, id); code != http.StatusOK {
		t.Fatalf("retry: status %d %s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Fatalf("cards = %d, want 1", n)
	}
	if got := recStatus(t, f.db, 80); got != "accepted" {
		t.Errorf("recommendation %q, want accepted", got)
	}
}

// improve on an agent outside the registry is refused; the recommendation goes
// back to proposed.
func TestTriageAcceptImproveFailureRestoresRecommendation(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 81, "R1", "agent", "x-agent", "{}")
	id := f.verdict(t, "advisor", triage.ClassImprove, "81", "improve", triage.StateSuggested, 1, `{}`)
	if code, body := f.accept(t, id); code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d %s, want 422", code, body)
	}
	f.assertRecReopened(t, id, 81)
}

// A recommendation this request did not move is never put back.
func TestTriageAcceptFailureKeepsPreAcceptedRecommendation(t *testing.T) {
	f := newAcceptFixture(t)
	seedRec(t, f.db, 82, "R1", "agent", "tech-lead", "{}")
	id := f.verdict(t, "advisor", triage.ClassCard, "82", "fix-card", triage.StateSuggested, 99,
		`{"title":"T","prompt":"P"}`)
	f.h.triageAcceptHook = func() {
		if _, err := f.db.Exec(`UPDATE recommendations SET status='accepted' WHERE id=82`); err != nil {
			t.Error(err)
		}
	}
	if code, body := f.accept(t, id); code < 400 || !strings.Contains(string(body), "create card") {
		t.Fatalf("status %d %s, want the card creation to fail", code, body)
	}
	if got := recStatus(t, f.db, 82); got != "accepted" {
		t.Errorf("recommendation %q, want accepted (not moved by this request)", got)
	}
	if v := f.state(t, id); v.State != triage.StateSuggested {
		t.Errorf("verdict %q, want suggested", v.State)
	}
}

// The operator decided the recommendation after the open check: dismiss and
// track answer stale instead of overriding them.
func TestTriageAcceptDismissTrackAfterOperatorDecided(t *testing.T) {
	for _, c := range []struct{ value, operator string }{{"dismiss", "accepted"}, {"track", "dismissed"}} {
		t.Run(c.value, func(t *testing.T) {
			f := newAcceptFixture(t)
			seedRec(t, f.db, 83, "R1", "agent", "x-agent", "{}")
			id := f.verdict(t, "advisor", triage.ClassPlain, "83", c.value, triage.StateSuggested, 1, `{}`)
			f.h.triageAcceptHook = func() {
				if _, err := f.db.Exec(`UPDATE recommendations SET status=? WHERE id=83`, c.operator); err != nil {
					t.Error(err)
				}
			}
			code, body := f.accept(t, id)
			if code != http.StatusConflict || !strings.Contains(string(body), `"the item changed"`) ||
				!strings.Contains(string(body), `"state":"stale"`) {
				t.Fatalf("status %d %s, want 409 stale", code, body)
			}
			if got := recStatus(t, f.db, 83); got != c.operator {
				t.Errorf("recommendation %q, want the operator's %q", got, c.operator)
			}
			if v := f.state(t, id); v.State != triage.StateStale {
				t.Errorf("verdict %q, want stale", v.State)
			}
		})
	}
}
