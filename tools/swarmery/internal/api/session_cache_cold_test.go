package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
)

// fixtureSession is the one session testServerWithDB ingests.
func fixtureSession(t *testing.T, db *sql.DB) (int64, string) {
	t.Helper()
	var id int64
	var uuid string
	if err := db.QueryRow(`SELECT id, session_uuid FROM sessions ORDER BY id LIMIT 1`).Scan(&id, &uuid); err != nil {
		t.Fatal(err)
	}
	return id, uuid
}

// TestSessionPayloadCarriesCacheCold: an open session_cache_cold row surfaces
// as cacheCold on the list and the detail; a cleared one reads null; the detail
// counts cold_restart turns and marks them in the turn list.
func TestSessionPayloadCarriesCacheCold(t *testing.T) {
	srv, db := testServerWithDB(t)
	id, uuid := fixtureSession(t, db)

	type cold struct {
		Since     string `json:"since"`
		Kind      string `json:"kind"`
		Footprint int64  `json:"footprint"`
	}
	type row struct {
		ID        int64 `json:"id"`
		CacheCold *cold `json:"cacheCold"`
	}
	var page struct {
		Sessions []row `json:"sessions"`
	}
	getJSON(t, srv.URL+"/api/sessions", &page)
	if len(page.Sessions) == 0 || page.Sessions[0].CacheCold != nil {
		t.Fatalf("warm session: %+v", page.Sessions)
	}

	if _, err := db.Exec(`INSERT INTO session_cache_cold (session_uuid, since, kind, footprint)
		VALUES (?, '2026-10-22T13:00:00Z', 'subscription', 210000)`, uuid); err != nil {
		t.Fatal(err)
	}
	var turnID int64
	if err := db.QueryRow(`SELECT id FROM turns WHERE session_id = ? AND role = 'assistant' AND agent_name IS NULL
		ORDER BY seq DESC LIMIT 1`, id).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE turns SET cold_restart = 1 WHERE id = ?`, turnID); err != nil {
		t.Fatal(err)
	}

	getJSON(t, srv.URL+"/api/sessions", &page)
	if c := page.Sessions[0].CacheCold; c == nil || c.Since != "2026-10-22T13:00:00Z" || c.Kind != "subscription" || c.Footprint != 210000 {
		t.Fatalf("list cacheCold = %+v", c)
	}

	var detail struct {
		CacheCold    *cold `json:"cacheCold"`
		ColdRestarts int64 `json:"coldRestarts"`
		Turns        []struct {
			ID          int64 `json:"id"`
			ColdRestart bool  `json:"coldRestart"`
		} `json:"turns"`
	}
	getJSON(t, srv.URL+"/api/sessions/"+strconv.FormatInt(id, 10), &detail)
	if detail.CacheCold == nil || detail.ColdRestarts != 1 {
		t.Fatalf("detail cacheCold=%+v coldRestarts=%d", detail.CacheCold, detail.ColdRestarts)
	}
	marked := 0
	for _, tr := range detail.Turns {
		if tr.ColdRestart {
			marked++
			if tr.ID != turnID {
				t.Errorf("turn %d marked, want %d", tr.ID, turnID)
			}
		}
	}
	if marked != 1 {
		t.Errorf("marked turns in the list = %d, want 1", marked)
	}

	if _, err := db.Exec(`UPDATE session_cache_cold SET cleared_at = '2026-10-22T14:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	getJSON(t, srv.URL+"/api/sessions/"+uuid, &detail)
	if detail.CacheCold != nil {
		t.Errorf("cleared row still surfaces: %+v", detail.CacheCold)
	}
}

type fakeHandoffRunner struct{ calls int }

func (f *fakeHandoffRunner) Run(context.Context, string) (string, error) {
	f.calls++
	return "# Handoff: continue\n", nil
}

func postHandoff(t *testing.T, url string) (int, handoffRequestDTO) {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out handoffRequestDTO
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestPostSessionHandoff: off → 503; a thin session → 409; a fat one → one
// generation through the candidate path; afterwards the existing brief is the
// answer and nothing regenerates.
func TestPostSessionHandoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // handoff.Generate writes under ~/.swarmery/handoffs
	srv, db := testServerWithDB(t)
	thinID, _ := fixtureSession(t, db)

	prevRunner, prevGo := handoffRunner, handoffGo
	t.Cleanup(func() { handoffRunner, handoffGo = prevRunner, prevGo })
	handoffGo = func(f func()) { f() }

	base := srv.URL + "/api/sessions/"
	handoffRunner = nil
	if code, _ := postHandoff(t, base+strconv.FormatInt(thinID, 10)+"/handoff"); code != http.StatusServiceUnavailable {
		t.Fatalf("runner off: status %d, want 503", code)
	}

	runner := &fakeHandoffRunner{}
	AttachHandoff(runner)
	if code, _ := postHandoff(t, base+strconv.FormatInt(thinID, 10)+"/handoff"); code != http.StatusConflict {
		t.Fatalf("thin session: status %d, want 409", code)
	}
	if code, _ := postHandoff(t, base+"no-such-session/handoff"); code != http.StatusNotFound {
		t.Fatalf("unknown session: status %d, want 404", code)
	}

	// A fat session, idle for a day — past the tick's activity window.
	var projectID int64
	if err := db.QueryRow(`SELECT project_id FROM sessions WHERE id = ?`, thinID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	r, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at, status, cwd)
		VALUES (?, 'fat-cold', '2026-10-21T08:00:00Z', 'idle', '/work/fat')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	fatID, _ := r.LastInsertId()
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, ended_at, tokens_in, tokens_cache_read, tokens_cache_write)
		VALUES (?, 0, 'assistant', '2026-10-21T09:00:00Z', '2026-10-21T09:00:00Z', 10, 199990, 0)`, fatID); err != nil {
		t.Fatal(err)
	}

	code, out := postHandoff(t, base+"fat-cold/handoff")
	if code != http.StatusAccepted || out.Status != "generating" || runner.calls != 1 {
		t.Fatalf("fat session: status %d %+v calls %d", code, out, runner.calls)
	}
	var path string
	if err := db.QueryRow(`SELECT path FROM handoffs WHERE session_id = ?`, fatID).Scan(&path); err != nil {
		t.Fatalf("no handoff row: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("brief not written: %v", err)
	}

	code, out = postHandoff(t, base+strconv.FormatInt(fatID, 10)+"/handoff")
	if code != http.StatusOK || out.Status != "ready" || out.Path != path || runner.calls != 1 {
		t.Fatalf("existing brief: status %d %+v calls %d", code, out, runner.calls)
	}
}
