package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// frictionTriageFixture serves the real routes over a DB holding two distinct
// recurring error groups; groups() returns them by key.
type frictionTriageFixture struct {
	t   *testing.T
	db  *sql.DB
	url string
}

func newFrictionTriageFixture(t *testing.T) *frictionTriageFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "friction-triage.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	today := retroDay(t, 0)
	for _, q := range []string{
		`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, '/work/alpha', '-work-alpha', 'Alpha', ?)`,
		`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES (1, 1, 'uuid-one', 'completed', ?)`,
		`INSERT INTO events (session_id, ts, type, status, payload, dedup_key) VALUES
		  (1, ?, 'error', 'error', '{"error":{"message":"API Error 529 overloaded"}}', 'g1')`,
		`INSERT INTO events (session_id, ts, type, status, payload, dedup_key) VALUES
		  (1, ?, 'error', 'error', '{"error":{"message":"git push: permission denied, (publickey)!"}}', 'g2')`,
	} {
		if _, err := db.Exec(q, today); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &frictionTriageFixture{t: t, db: db, url: srv.URL}
}

func (f *frictionTriageFixture) groups() map[string]frictionErrGroupDTO {
	f.t.Helper()
	var out frictionDTO
	getJSON(f.t, f.url+"/api/retro/friction?"+retroRange(7), &out)
	if len(out.ErrorGroups) != 2 {
		f.t.Fatalf("error_groups = %+v, want 2", out.ErrorGroups)
	}
	by := map[string]frictionErrGroupDTO{}
	for _, g := range out.ErrorGroups {
		if g.Triage.State == "" {
			f.t.Errorf("group %q has no triage.state", g.Key)
		}
		by[g.Key] = g
	}
	return by
}

// keys returns the two group keys, the overloaded one first.
func (f *frictionTriageFixture) keys() (string, string) {
	f.t.Helper()
	var a, b string
	for k, g := range f.groups() {
		if g.Example == "API Error 529 overloaded" {
			a = k
		} else {
			b = k
		}
	}
	return a, b
}

func (f *frictionTriageFixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("exec: %v\n%s", err, q)
	}
}

func (f *frictionTriageFixture) rec(id int64, key, status string) {
	f.t.Helper()
	f.exec(`INSERT INTO recommendations (id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (?, 'R3', 'error_group', ?, 't', 'd', '{}', ?, ?, '2026-10-06T00:00:00.000Z', '2026-10-06T00:00:00.000Z')`,
		id, key, status, "dk-"+key)
}

// verdict inserts a triage verdict under a fresh run and returns its id.
func (f *frictionTriageFixture) verdict(kind, ref, value, state string) int64 {
	f.t.Helper()
	res, err := f.db.Exec(`INSERT INTO triage_runs (trigger, kinds, status, started_at) VALUES ('operator', '[]', 'ok', '2026-10-06T00:00:00.000Z')`)
	if err != nil {
		f.t.Fatal(err)
	}
	run, _ := res.LastInsertId()
	res, err = f.db.Exec(`INSERT INTO triage_verdicts (run_id, kind, ref, value, state, created_at)
		VALUES (?, ?, ?, ?, ?, '2026-10-06T00:00:00.000Z')`, run, kind, ref, value, state)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *frictionTriageFixture) unmute(key, origin string) int {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, f.url+"/api/retro/friction/mute?key="+url.QueryEscape(key), nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestFrictionTriageUntriagedByDefault(t *testing.T) {
	f := newFrictionTriageFixture(t)
	for k, g := range f.groups() {
		if g.Triage != (frictionTriageDTO{State: frictionUntriaged}) {
			t.Errorf("%q triage = %+v, want untriaged", k, g.Triage)
		}
	}
}

func TestFrictionTriageMuted(t *testing.T) {
	f := newFrictionTriageFixture(t)
	a, b := f.keys()
	now := time.Now()
	if err := triage.Mute(f.db, a, "provider noise", 0, now); err != nil {
		t.Fatal(err)
	}
	gs := f.groups() // the muted group stays listed
	if g := gs[a].Triage; g.State != frictionMuted || g.Reason != "provider noise" || g.MutedUntil == "" {
		t.Errorf("muted triage = %+v", g)
	}
	if gs[b].Triage.State != frictionUntriaged {
		t.Errorf("other group = %+v, want untriaged", gs[b].Triage)
	}
}

func TestFrictionTriageExpiredMuteIsUntriaged(t *testing.T) {
	f := newFrictionTriageFixture(t)
	a, _ := f.keys()
	if err := triage.Mute(f.db, a, "old", 0, time.Now().AddDate(0, 0, -(triage.MuteDays+1))); err != nil {
		t.Fatal(err)
	}
	if g := f.groups()[a].Triage; g.State != frictionUntriaged {
		t.Errorf("expired mute triage = %+v, want untriaged", g)
	}
}

func TestFrictionTriageTrackedAndFixProposed(t *testing.T) {
	f := newFrictionTriageFixture(t)
	a, b := f.keys()
	f.rec(11, a, "proposed")
	f.rec(12, b, "proposed")
	gs := f.groups()
	if g := gs[a].Triage; g.State != frictionTracked || g.RecommendationID != 11 {
		t.Errorf("proposed rec triage = %+v, want tracked/11", g)
	}

	// A suggested advisor fix-card on rec 12 → fix_proposed; a fix-card that is
	// not suggested (or a different value) does not count.
	f.verdict("advisor", "12", "fix-card", "suggested")
	f.verdict("advisor", "11", "fix-card", "stale")
	f.verdict("advisor", "11", "dismiss", "suggested")
	gs = f.groups()
	if g := gs[b].Triage; g.State != frictionFixProposed || g.RecommendationID != 12 {
		t.Errorf("fix-card suggestion triage = %+v, want fix_proposed/12", g)
	}
	if g := gs[a].Triage; g.State != frictionTracked {
		t.Errorf("non-matching verdicts triage = %+v, want tracked", g)
	}

	f.exec(`UPDATE recommendations SET status='accepted' WHERE id=11`)
	if g := f.groups()[a].Triage; g.State != frictionFixProposed || g.RecommendationID != 11 {
		t.Errorf("accepted rec triage = %+v, want fix_proposed/11", g)
	}

	f.exec(`UPDATE recommendations SET status='dismissed' WHERE id=11`)
	if g := f.groups()[a].Triage; g.State != frictionUntriaged {
		t.Errorf("dismissed rec triage = %+v, want untriaged", g)
	}
}

func TestFrictionTriageMuteWinsOverRecommendation(t *testing.T) {
	f := newFrictionTriageFixture(t)
	a, _ := f.keys()
	f.rec(21, a, "accepted")
	if err := triage.Mute(f.db, a, "noise", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if g := f.groups()[a].Triage; g.State != frictionMuted || g.RecommendationID != 0 {
		t.Errorf("triage = %+v, want muted without a recommendation id", g)
	}
}

func TestFrictionUnmute(t *testing.T) {
	f := newFrictionTriageFixture(t)
	_, b := f.keys() // "git push: permission denied, (publickey)!" — spaces and punctuation
	svc := triage.NewService(f.db, nil)
	AttachTriage(svc)
	t.Cleanup(func() { AttachTriage(nil) })
	vid := f.verdict("friction", b, "mute", triage.StateApplied)
	if err := triage.Mute(f.db, b, "noise", vid, time.Now()); err != nil {
		t.Fatal(err)
	}
	if f.groups()[b].Triage.State != frictionMuted {
		t.Fatal("not muted before the DELETE")
	}
	if code := f.unmute(b, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("foreign Origin = %d, want 403", code)
	}
	if code := f.unmute(b, ""); code != http.StatusNoContent {
		t.Fatalf("unmute = %d, want 204", code)
	}
	if f.groups()[b].Triage.State != frictionUntriaged {
		t.Error("group still muted after the DELETE")
	}
	if v, err := svc.GetVerdict(vid); err != nil || v.State != triage.StateUndone {
		t.Errorf("muting verdict = %+v (%v), want undone", v, err)
	}
	if code := f.unmute(b, ""); code != http.StatusNotFound {
		t.Errorf("second unmute = %d, want 404", code)
	}
	if code := f.unmute("", ""); code != http.StatusBadRequest {
		t.Errorf("empty key = %d, want 400", code)
	}
}

func TestFrictionUnmuteOperatorMuteAndUndoneVerdict(t *testing.T) {
	f := newFrictionTriageFixture(t)
	a, b := f.keys()
	svc := triage.NewService(f.db, nil)
	AttachTriage(svc)
	t.Cleanup(func() { AttachTriage(nil) })
	if err := triage.Mute(f.db, a, "by hand", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code := f.unmute(a, ""); code != http.StatusNoContent {
		t.Errorf("operator unmute = %d, want 204", code)
	}
	// The verdict was already undone elsewhere: the unmute still succeeds.
	vid := f.verdict("friction", b, "mute", triage.StateUndone)
	if err := triage.Mute(f.db, b, "noise", vid, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code := f.unmute(b, ""); code != http.StatusNoContent {
		t.Errorf("unmute with an undone verdict = %d, want 204", code)
	}
}

// A mute that already expired is no mute: the DELETE is 404 and it leaves the
// noise verdict applied, so a later run may judge the group again.
func TestFrictionUnmuteExpiredMuteIsNotFound(t *testing.T) {
	f := newFrictionTriageFixture(t)
	a, _ := f.keys()
	svc := triage.NewService(f.db, nil)
	AttachTriage(svc)
	t.Cleanup(func() { AttachTriage(nil) })
	vid := f.verdict(frictionKind, a, frictionNoise, triage.StateApplied)
	if err := triage.Mute(f.db, a, "old", 0, time.Now().AddDate(0, 0, -(triage.MuteDays+1))); err != nil {
		t.Fatal(err)
	}
	if code := f.unmute(a, ""); code != http.StatusNotFound {
		t.Fatalf("unmute of an expired mute = %d, want 404", code)
	}
	if v, err := svc.GetVerdict(vid); err != nil || v.State != triage.StateApplied {
		t.Errorf("noise verdict = %+v (%v), want still applied", v, err)
	}
}
