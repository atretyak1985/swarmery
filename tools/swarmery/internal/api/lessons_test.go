package api

import (
	"net/http"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
)

// TestLessonsReviewQueueEndpoints: the queue lists candidates, accept is the
// transition to active (409 on a repeat), edit validates, dismiss/merge/retire
// map their errors, and a cross-origin mutation is refused.
func TestLessonsReviewQueueEndpoints(t *testing.T) {
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
	b := ins("b", "Pin the model always", "pin model always")
	c := ins("c", "Other", "other")

	var got struct {
		Lessons []lessons.Lesson `json:"lessons"`
	}
	getJSON(t, srv.URL+"/api/lessons?status=candidate", &got)
	if len(got.Lessons) != 3 {
		t.Fatalf("candidates = %d", len(got.Lessons))
	}
	if r := decisionReq(t, http.MethodGet, srv.URL+"/api/lessons?status=bogus", ""); r.StatusCode != 400 {
		t.Errorf("bad status filter = %d", r.StatusCode)
	}

	url := func(id int64, action string) string {
		u := srv.URL + "/api/lessons/" + itoa64(id)
		if action != "" {
			u += "/" + action
		}
		return u
	}
	for _, tc := range []struct {
		method, url, body string
		want              int
	}{
		{http.MethodPost, url(a, "accept"), "", 200},
		{http.MethodPost, url(a, "accept"), "", 409},
		{http.MethodPost, url(999, "accept"), "", 404},
		{http.MethodPost, srv.URL + "/api/lessons/x/accept", "", 400},
		{http.MethodPatch, url(b, ""), `{"guidance":"one\ntwo"}`, 400},
		{http.MethodPatch, url(b, ""), `{"title":"Pin the model in every run"}`, 200},
		{http.MethodPatch, url(b, ""), `not json`, 400},
		{http.MethodPost, url(b, "merge"), `{"lessonId":` + itoa64(a) + `}`, 200},
		{http.MethodPost, url(c, "merge"), `{}`, 400},
		{http.MethodPost, url(c, "dismiss"), `{"reason":"one-off"}`, 200},
		{http.MethodPost, url(a, "retire"), ``, 200},
		{http.MethodPost, url(a, "retire"), ``, 409},
	} {
		if r := decisionReq(t, tc.method, tc.url, tc.body); r.StatusCode != tc.want {
			t.Errorf("%s %s %s = %d, want %d", tc.method, tc.url, tc.body, r.StatusCode, tc.want)
		}
	}

	// requireLocalOrigin: a foreign Origin cannot accept.
	d := ins("d", "Fresh", "fresh")
	req, _ := http.NewRequest(http.MethodPost, url(d, "accept"), nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin accept = %d, want 403", resp.StatusCode)
	}
}

// TestAcceptOfALowLessonNeedsTheConfirmFlag: a low-confidence candidate is
// refused (409, still a candidate) until the body carries {"confirm": true} —
// the dashboard's second click; a medium one needs no flag.
func TestAcceptOfALowLessonNeedsTheConfirmFlag(t *testing.T) {
	srv, db := testServerWithDB(t)
	ins := func(uuid, confidence string) int64 {
		res, err := db.Exec(`INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title,
			guidance, area_globs, evidence_json, status, confidence, created_at, updated_at)
			VALUES (?, 1, 1, ?, ?, 'Do it.', 'internal/**', '["divergence:1"]', 'candidate', ?, 'now', 'now')`,
			uuid, "T "+uuid, "t "+uuid, confidence)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	low, med := ins("low", "low"), ins("med", "medium")
	accept := func(id int64) string { return srv.URL + "/api/lessons/" + itoa64(id) + "/accept" }
	status := func(id int64) string {
		var s string
		if err := db.QueryRow(`SELECT status FROM surprise_lessons WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, body := range []string{"", `{}`, `{"confirm":false}`} {
		if r := decisionReq(t, http.MethodPost, accept(low), body); r.StatusCode != http.StatusConflict {
			t.Errorf("accept low with %q = %d, want 409", body, r.StatusCode)
		}
	}
	if s := status(low); s != lessons.StatusCandidate {
		t.Fatalf("an unconfirmed low lesson became %s", s)
	}
	if r := decisionReq(t, http.MethodPost, accept(low), `{"confirm":true}`); r.StatusCode != http.StatusOK {
		t.Fatalf("confirmed accept = %d", r.StatusCode)
	}
	if s := status(low); s != lessons.StatusActive {
		t.Fatalf("confirmed low lesson is %s", s)
	}
	if r := decisionReq(t, http.MethodPost, accept(med), ""); r.StatusCode != http.StatusOK {
		t.Fatalf("medium accept without confirm = %d", r.StatusCode)
	}
}

// TestLessonGenerationsEndpoint: the generation log carries the model's
// no_entry_reason.
func TestLessonGenerationsEndpoint(t *testing.T) {
	srv, db := testServerWithDB(t)
	if _, err := db.Exec(`INSERT INTO lesson_generations (source_phase_run, phase_id, state, created_at, no_entry_reason)
		VALUES ('run-1', 1, 'done', '2026-11-02T00:00:00Z', 'Nothing here transfers.')`); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Generations []lessons.Generation `json:"generations"`
	}
	getJSON(t, srv.URL+"/api/lessons/generations", &got)
	if len(got.Generations) != 1 || got.Generations[0].NoEntryReason != "Nothing here transfers." {
		t.Fatalf("generations = %+v", got.Generations)
	}
}

// TestLessonsAreaFilterAndPromoteErrors: ?area= keeps only lessons whose globs
// overlap the directory (the injection matcher), and promote maps its refusals.
// The branch-writing path itself is covered in internal/lessons with a fixture repo.
func TestLessonsAreaFilterAndPromoteErrors(t *testing.T) {
	srv, db := testServerWithDB(t)
	ins := func(uuid, globs, status string) int64 {
		res, err := db.Exec(`INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title,
			guidance, area_globs, evidence_json, status, created_at, updated_at)
			VALUES (?, 1, 1, ?, ?, 'Do it.', ?, '[]', ?, 'now', 'now')`, uuid, "t"+uuid, "n"+uuid, globs, status)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	store := ins("s", "internal/store/**", "active")
	ins("w", "web/src/**", "active")
	cand := ins("c", "internal/**", "candidate")

	var got struct {
		Lessons []lessons.Lesson `json:"lessons"`
	}
	getJSON(t, srv.URL+"/api/lessons?status=active&area=tools/app/internal/store", &got)
	if len(got.Lessons) != 1 || got.Lessons[0].ID != store {
		t.Fatalf("area filter = %+v, want only lesson %d", got.Lessons, store)
	}
	getJSON(t, srv.URL+"/api/lessons?status=active&project=no-such-project", &got)
	if len(got.Lessons) != 0 {
		t.Fatalf("project filter = %+v, want none", got.Lessons)
	}
	for _, tc := range []struct {
		id   string
		want int
	}{
		{itoa64(cand), 409},  // only an active lesson can be promoted
		{itoa64(store), 409}, // its phase maps to no project
		{"999", 404},
		{"x", 400},
	} {
		r := decisionReq(t, http.MethodPost, srv.URL+"/api/lessons/"+tc.id+"/promote", "")
		if r.StatusCode != tc.want {
			t.Errorf("promote %s = %d, want %d", tc.id, r.StatusCode, tc.want)
		}
	}
}
