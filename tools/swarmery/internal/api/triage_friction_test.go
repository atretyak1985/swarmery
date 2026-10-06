package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// frictionFixture: projects p (1) and q (2), sessions 1 (p) and 2 (q), and a
// real engine with the friction Source behind a stub judge.
func frictionFixture(t *testing.T, j *advisorJudge) (*sql.DB, *triage.Service, *frictionSource) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "triage_friction.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	seedFrictionProjects(t, db)
	src := &frictionSource{h: &Handler{DB: db}}
	svc := triage.NewService(db, j)
	svc.Go = func(fn func()) { fn() }
	svc.Register(src)
	return db, svc, src
}

func seedFrictionProjects(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT OR IGNORE INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`,
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2,'/repo/q','q','2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions(id, project_id, session_uuid, status, started_at, title) VALUES(1,1,'u1','completed','2026-01-02T00:00:00Z','Login work')`,
		`INSERT INTO sessions(id, project_id, session_uuid, status, started_at) VALUES(2,2,'u2','completed','2026-01-02T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// seedErrors inserts n error events with msg in session sess (an hour ago) and
// returns the group key errorGroups gives them.
func seedErrors(t *testing.T, db *sql.DB, sess int64, msg string, n int) string {
	t.Helper()
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg}})
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (?, ?, 'error', NULL, 'error', ?, ?)`, sess, ts, string(payload), fmt.Sprintf("%s-%d-%d", msg, sess, i)); err != nil {
			t.Fatal(err)
		}
	}
	return normalizeErrKey(msg)
}

func runFriction(t *testing.T, svc *triage.Service, project int64) []triage.Verdict {
	t.Helper()
	id, err := svc.Start(triage.StartReq{Scope: triage.Scope{ProjectID: project}, Kinds: []string{frictionKind}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	vs, err := svc.ListVerdicts(triage.VerdictFilter{RunID: id, Kind: frictionKind})
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func frictionState(t *testing.T, src *frictionSource, key string) frictionTriageDTO {
	t.Helper()
	st, err := src.h.frictionTriageStates([]string{key}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return st[key]
}

func countRecs(t *testing.T, db *sql.DB, where string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// noise mutes the group with the judge's reason; it is not collected again;
// Undo unmutes and a second Undo is nil.
func TestTriageFrictionNoiseMutesAndUndo(t *testing.T) {
	db, svc, src := frictionFixture(t, &advisorJudge{value: "noise"})
	key := seedErrors(t, db, 1, "API Error 529 overloaded", 3)
	vs := runFriction(t, svc, 0)
	if len(vs) != 1 || vs[0].State != triage.StateApplied || vs[0].Ref != key || vs[0].Value != "noise" {
		t.Fatalf("verdicts = %+v", vs)
	}
	if st := frictionState(t, src, key); st.State != frictionMuted || st.Reason != "judged" {
		t.Fatalf("state = %+v, want muted with the judge's reason", st)
	}
	if vs2 := runFriction(t, svc, 0); len(vs2) != 0 {
		t.Fatalf("muted group collected again: %+v", vs2)
	}
	if _, err := svc.Undo(context.Background(), vs[0].ID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if st := frictionState(t, src, key); st.State != frictionUntriaged {
		t.Fatalf("after undo state = %+v", st)
	}
	if err := src.Undo(context.Background(), vs[0]); err != nil {
		t.Fatalf("second undo: %v", err)
	}
}

// fixable files ONE T1 recommendation and ONE suggested fix-card advisor
// verdict with the cut payload and the item's project; a re-run files nothing;
// Undo dismisses the recommendation.
func TestTriageFrictionFixableFilesOnce(t *testing.T) {
	long := strings.Repeat("é", 100)
	payload, _ := json.Marshal(map[string]string{"title": long, "prompt": "Fix the retry loop."})
	db, svc, src := frictionFixture(t, &advisorJudge{value: "fixable", payload: payload})
	key := seedErrors(t, db, 1, "ENOENT reading config", 2)
	vs := runFriction(t, svc, 0)
	if len(vs) != 1 || vs[0].State != triage.StateApplied {
		t.Fatalf("verdicts = %+v", vs)
	}
	if n := countRecs(t, db, `rule='T1' AND target_kind='error_group' AND target=? AND status='proposed' AND dedup_key=?`, key, "T1:"+key); n != 1 {
		t.Fatalf("T1 recs = %d, want 1", n)
	}
	var recID int64
	if err := db.QueryRow(`SELECT id FROM recommendations WHERE rule='T1'`).Scan(&recID); err != nil {
		t.Fatal(err)
	}
	sug, err := svc.ListVerdicts(triage.VerdictFilter{Kind: advisorKind})
	if err != nil {
		t.Fatal(err)
	}
	if len(sug) != 1 || sug[0].State != triage.StateSuggested || sug[0].Value != "fix-card" ||
		sug[0].Class != triage.ClassCard || sug[0].Ref != strconv.FormatInt(recID, 10) ||
		sug[0].ProjectID == nil || *sug[0].ProjectID != 1 {
		t.Fatalf("suggestions = %+v", sug)
	}
	var fix frictionFix
	if err := json.Unmarshal(sug[0].Payload, &fix); err != nil || len([]rune(fix.Title)) != 80 || fix.Prompt != "Fix the retry loop." {
		t.Fatalf("suggestion payload = %s (%v)", sug[0].Payload, err)
	}
	if st := frictionState(t, src, key); st.State != frictionFixProposed || st.RecommendationID != recID {
		t.Fatalf("state = %+v, want fix_proposed", st)
	}
	runFriction(t, svc, 0)
	if n := countRecs(t, db, `rule='T1'`); n != 1 {
		t.Fatalf("second run: T1 recs = %d, want 1", n)
	}
	if err := src.Undo(context.Background(), vs[0]); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if s := recStatus(t, db, recID); s != "dismissed" {
		t.Fatalf("rec status after undo = %q", s)
	}
	if err := src.Undo(context.Background(), vs[0]); err != nil {
		t.Fatalf("second undo: %v", err)
	}
}

// A fixable answer without a prompt files nothing; the part fails.
func TestTriageFrictionFixableBadPayloadFails(t *testing.T) {
	db, svc, _ := frictionFixture(t, &advisorJudge{value: "fixable", payload: json.RawMessage(`{"title":"t"}`)})
	seedErrors(t, db, 1, "ENOENT reading config", 2)
	vs := runFriction(t, svc, 0)
	if len(vs) != 1 || vs[0].State != triage.StateFailed {
		t.Fatalf("verdicts = %+v, want one failed", vs)
	}
	if n := countRecs(t, db, `1=1`); n != 0 {
		t.Fatalf("recs = %d, want 0", n)
	}
}

// A group with no attributable project gets a plain track suggestion.
func TestTriageFrictionNoProjectTracks(t *testing.T) {
	db, _, src := frictionFixture(t, &advisorJudge{})
	key := seedErrors(t, db, 1, "ENOENT reading config", 2)
	items, err := src.Collect(context.Background(), triage.Scope{}, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("collect = %+v err=%v", items, err)
	}
	it := items[0]
	it.ProjectID = 0
	a, err := src.Apply(context.Background(), it, it.Parts[0], "fixable", "why",
		json.RawMessage(`{"title":"t","prompt":"p"}`))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(a.Follow) != 1 || a.Follow[0].Class != triage.ClassPlain || a.Follow[0].Value != "track" ||
		a.Follow[0].Payload != nil || a.Follow[0].ProjectID != 0 {
		t.Fatalf("follow = %+v", a.Follow)
	}
	// A second Apply on the now-tracked key files nothing.
	if _, err := src.Apply(context.Background(), it, it.Parts[0], "fixable", "why",
		json.RawMessage(`{"title":"t","prompt":"p"}`)); err == nil || !strings.Contains(err.Error(), "already tracked") {
		t.Fatalf("second apply err = %v, want already tracked", err)
	}
	if n := countRecs(t, db, `target=?`, key); n != 1 {
		t.Fatalf("recs = %d", n)
	}
}

// Collect skips singletons and groups with an open recommendation of any
// rule; a scoped run collects only that project's groups.
func TestTriageFrictionCollectFilters(t *testing.T) {
	db, _, src := frictionFixture(t, &advisorJudge{})
	seedErrors(t, db, 1, "single shot failure", 1)
	tracked := seedErrors(t, db, 1, "API Error 529 overloaded", 2)
	seedRec(t, db, 50, "R3", "error_group", tracked, `{}`)
	inP := seedErrors(t, db, 1, "ENOENT reading config", 2)
	inQ := seedErrors(t, db, 2, "permission denied writing cache", 3)

	keys := func(project int64) []string {
		items, err := src.Collect(context.Background(), triage.Scope{ProjectID: project}, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			if it.Key != it.Parts[0].Ref || !strings.HasPrefix(it.Title, "Recurring error: ") {
				t.Fatalf("item = %+v", it)
			}
			out = append(out, fmt.Sprintf("%s@%d", it.Key, it.ProjectID))
		}
		return out
	}
	if got, want := strings.Join(keys(0), "|"), fmt.Sprintf("%s@2|%s@1", inQ, inP); got != want {
		t.Fatalf("fleet collect = %s, want %s", got, want)
	}
	if got, want := strings.Join(keys(2), "|"), inQ+"@2"; got != want {
		t.Fatalf("scoped collect = %s, want %s", got, want)
	}
	items, err := src.Collect(context.Background(), triage.Scope{}, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("limited collect = %d (%v)", len(items), err)
	}
	it := items[0]
	if err := src.Prepare(context.Background(), &it); err != nil || it.Instruction != frictionInstruction ||
		!strings.Contains(it.Evidence, "count: 3") || !strings.Contains(it.Evidence, "in project q") {
		t.Fatalf("prepare = %+v (%v)", it, err)
	}
}

// serverWithRealSources is serverWithTriage without the fake "friction"
// Source: every Source is the one NewServer registers.
func serverWithRealSources(t *testing.T) (string, *triage.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "triage_real.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := triage.NewService(db, apiTriageJudge{}) // answers noise
	svc.Go = func(fn func()) { fn() }
	AttachTriage(svc)
	t.Cleanup(func() { AttachTriage(nil) })
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, svc
}

// NewServer registers the friction Source, and lifting a mute through
// DELETE /api/retro/friction/mute records the applied verdict as undone.
func TestTriageFrictionSourceRegisteredByNewServer(t *testing.T) {
	base, svc := serverWithRealSources(t)
	seedFrictionProjects(t, svc.DB)
	key := seedErrors(t, svc.DB, 1, "API Error 529 overloaded", 2)
	vs := runFriction(t, svc, 0) // the stub judge answers noise
	if len(vs) != 1 || vs[0].State != triage.StateApplied {
		t.Fatalf("verdicts = %+v", vs)
	}
	req, _ := http.NewRequest(http.MethodDelete, base+"/api/retro/friction/mute?key="+url.QueryEscape(key), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unmute status = %d", resp.StatusCode)
	}
	v, err := svc.GetVerdict(vs[0].ID)
	if err != nil || v.State != triage.StateUndone {
		t.Fatalf("verdict after unmute = %+v (%v)", v, err)
	}
	// The operator said "not noise": the next run does not mute the group again.
	if vs2 := runFriction(t, svc, 0); len(vs2) != 0 {
		t.Fatalf("run after the operator's unmute judged the group again: %+v", vs2)
	}
	st, err := (&Handler{DB: svc.DB}).frictionTriageStates([]string{key}, time.Now())
	if err != nil || st[key].State != frictionUntriaged {
		t.Fatalf("state after the re-run = %+v (%v), want untriaged", st[key], err)
	}
}

// dismissRec marks recommendation id dismissed at the given moment.
func dismissRec(t *testing.T, db *sql.DB, id int64, at time.Time) {
	t.Helper()
	if _, err := db.Exec(`UPDATE recommendations SET status='dismissed', updated_at=? WHERE id=?`,
		at.UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
}

// A recommendation the operator dismissed inside the suppression window —
// under ANY rule — keeps the group out of a run, and Apply refuses it too. Once
// the window has passed the group is judged again.
func TestTriageFrictionRespectsDismissalOfAnyRule(t *testing.T) {
	payload := json.RawMessage(`{"title":"t","prompt":"p"}`)
	db, svc, src := frictionFixture(t, &advisorJudge{value: "fixable", payload: payload})
	key := seedErrors(t, db, 1, "ENOENT reading config", 2)
	items, err := src.Collect(context.Background(), triage.Scope{}, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("collect before the dismissal = %+v (%v)", items, err)
	}
	seedRec(t, db, 50, "R3", "error_group", key, `{}`)
	dismissRec(t, db, 50, time.Now().Add(-24*time.Hour))

	if vs := runFriction(t, svc, 0); len(vs) != 0 {
		t.Fatalf("run judged a group dismissed under R3: %+v", vs)
	}
	if _, err := src.Apply(context.Background(), items[0], items[0].Parts[0], "fixable", "why", payload); !errors.Is(err, errRecentlyDismissed) {
		t.Fatalf("apply err = %v, want errRecentlyDismissed", err)
	}
	if n := countRecs(t, db, `rule='T1'`); n != 0 {
		t.Fatalf("T1 recs = %d, want 0", n)
	}

	dismissRec(t, db, 50, time.Now().AddDate(0, 0, -(advisor.DismissSuppressDays+1)))
	if vs := runFriction(t, svc, 0); len(vs) != 1 || vs[0].State != triage.StateApplied {
		t.Fatalf("run after the window = %+v, want one applied", vs)
	}
	if n := countRecs(t, db, `rule='T1' AND status='proposed'`); n != 1 {
		t.Fatalf("T1 recs after the window = %d, want 1", n)
	}
}

// Undoing an older noise verdict leaves the mute a later run made: the newest
// applied noise verdict owns it.
func TestTriageFrictionUndoOfOlderNoiseKeepsNewerMute(t *testing.T) {
	db, svc, src := frictionFixture(t, &advisorJudge{value: "noise"})
	key := seedErrors(t, db, 1, "API Error 529 overloaded", 3)
	first := runFriction(t, svc, 0)
	if len(first) != 1 || first[0].State != triage.StateApplied {
		t.Fatalf("first run = %+v", first)
	}
	if _, err := db.Exec(`UPDATE friction_mutes SET muted_until='2026-01-01T00:00:00.000Z' WHERE key=?`, key); err != nil {
		t.Fatal(err)
	}
	second := runFriction(t, svc, 0) // the mute expired: the group is judged again
	if len(second) != 1 || second[0].State != triage.StateApplied {
		t.Fatalf("second run = %+v", second)
	}
	if _, err := svc.Undo(context.Background(), first[0].ID); err != nil {
		t.Fatalf("undo of the older verdict: %v", err)
	}
	if st := frictionState(t, src, key); st.State != frictionMuted {
		t.Fatalf("state after undoing the older verdict = %+v, want still muted", st)
	}
	if _, err := svc.Undo(context.Background(), second[0].ID); err != nil {
		t.Fatalf("undo of the newer verdict: %v", err)
	}
	if st := frictionState(t, src, key); st.State != frictionUntriaged {
		t.Fatalf("state after undoing the newer verdict = %+v, want untriaged", st)
	}
}
