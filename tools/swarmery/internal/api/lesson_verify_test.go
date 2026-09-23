package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/calibration"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
)

// Acceptance (phase 16): calibration groups under 20 non-post-hoc samples are
// not rendered — the API does not return them at all, only their count.
func TestCalibrationHidesGroupsUnderTwentySamples(t *testing.T) {
	srv, db := testServerWithDB(t)
	if _, err := db.Exec(`INSERT OR IGNORE INTO projects(id, path, slug, first_seen) VALUES (1, '/repo', 'p', 'now')`); err != nil {
		t.Fatal(err)
	}
	n := 0
	seed := func(model string, count int, postHoc int) {
		for i := 0; i < count; i++ {
			n++
			uuid := fmt.Sprintf("cal-%d", n)
			if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, model, status, started_at)
				VALUES (1, ?, ?, 'completed', '2026-09-01T00:00:00Z')`, uuid, model); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO phase_surprise (phase_id, session_uuid, forecast_post_hoc, surprise_index,
				components_json, detail_json, computed_at) VALUES (1, ?, ?, 0.2, '{"outcome_miss":0}',
				'{"matchedAreas":["a"],"sizeDistance":0,"confidence":0.7}', 'now')`, uuid, postHoc); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("model-big", 20, 0)
	seed("model-small", 19, 0)
	seed("model-small", 5, 1) // post-hoc samples never count toward the gate

	var rep calibration.Report
	getJSON(t, srv.URL+"/api/calibration?by=model", &rep)
	if len(rep.Groups) != 1 || rep.Groups[0].Key["model"] != "model-big" || rep.Groups[0].Samples != 20 {
		t.Fatalf("groups = %+v", rep.Groups)
	}
	if rep.HiddenGroups != 1 || rep.HiddenRuns != 19 || rep.MinSamples != 20 {
		t.Fatalf("hidden = %d groups / %d runs (min %d)", rep.HiddenGroups, rep.HiddenRuns, rep.MinSamples)
	}
	if r := decisionReq(t, http.MethodGet, srv.URL+"/api/calibration?by=colour", ""); r.StatusCode != 400 {
		t.Fatalf("bad dimension = %d", r.StatusCode)
	}
}

func TestRetirementQueueEndpoints(t *testing.T) {
	srv, db := testServerWithDB(t)
	res, err := db.Exec(`INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title, guidance,
		area_globs, status, created_at, updated_at, activated_at)
		VALUES ('r', 1, 1, 'T', 't', 'Do it.', 'internal/**', 'active', 'now', 'now', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	lessonID, _ := res.LastInsertId()
	ins := func(reason string, lesson int64, state string) int64 {
		r, err := db.Exec(`INSERT INTO lesson_retirements (lesson_id, reason, detail, state, proposed_at)
			VALUES (?, ?, 'why', ?, '2026-09-20T00:00:00Z')`, lesson, reason, state)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	open := ins(lessons.ReasonIneffective, lessonID, lessons.ProposalOpen)
	kept := ins(lessons.ReasonStale, 424242, lessons.ProposalOpen)

	var got struct {
		Proposals      []lessons.Proposal `json:"proposals"`
		AutoRetireDays int                `json:"autoRetireDays"`
	}
	getJSON(t, srv.URL+"/api/lessons/retirements", &got)
	if len(got.Proposals) != 2 || got.AutoRetireDays != 14 || got.Proposals[1].AutoRetireAt == nil {
		t.Fatalf("queue = %+v", got)
	}
	base := srv.URL + "/api/lessons/retirements/"
	for _, tc := range []struct {
		url  string
		want int
	}{
		{base + itoa64(open) + "/confirm", 200},
		{base + itoa64(open) + "/confirm", 409},
		{base + itoa64(kept) + "/keep", 200},
		{base + "999/keep", 404},
		{base + "x/keep", 400},
	} {
		if r := decisionReq(t, http.MethodPost, tc.url, ""); r.StatusCode != tc.want {
			t.Errorf("POST %s = %d, want %d", tc.url, r.StatusCode, tc.want)
		}
	}
	l, err := lessons.Get(db, lessonID)
	if err != nil || l.Status != lessons.StatusRetired || l.RetireReason == nil || *l.RetireReason != lessons.ReasonIneffective {
		t.Fatalf("confirmed lesson = %+v, %v", l, err)
	}
	getJSON(t, srv.URL+"/api/lessons/retirements?all=1", &got)
	if len(got.Proposals) != 2 || got.Proposals[0].State != lessons.ProposalKept {
		t.Fatalf("history = %+v", got.Proposals)
	}
}
