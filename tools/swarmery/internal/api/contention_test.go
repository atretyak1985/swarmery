package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// contentionSession is one seeded session and the file paths it touched, each
// touch `ago` before now.
type contentionSession struct {
	id      int64
	project int64
	status  string
	hidden  int
	cwd     string
	title   string
	touches []contentionTouch
}

type contentionTouch struct {
	path string
	ago  time.Duration
}

// contentionServer plants projects alpha (1) and beta (2), the given sessions,
// and one file_change event per touch, all timestamped relative to now so the
// window filter is exercised for real.
func contentionServer(t *testing.T, sessions ...contentionSession) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "contention.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	stamp := func(ago time.Duration) string {
		return time.Now().Add(-ago).UTC().Format(contentionTSFormat)
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen, archived) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?, 0),
		(2, '/work/beta',  '-work-beta',  'Beta',  ?, 0)`, stamp(0), stamp(0))

	eventID := int64(0)
	for _, s := range sessions {
		mustExec(`INSERT INTO sessions (id, project_id, session_uuid, git_branch, status, started_at, title, hidden, cwd)
			VALUES (?, ?, ?, 'main', ?, ?, ?, ?, ?)`,
			s.id, s.project, fmt.Sprintf("u%d", s.id), s.status, stamp(24*time.Hour), s.title, s.hidden, s.cwd)
		for _, tc := range s.touches {
			eventID++
			mustExec(`INSERT INTO events (id, session_id, ts, type, dedup_key) VALUES (?, ?, ?, 'file_change', ?)`,
				eventID, s.id, stamp(tc.ago), fmt.Sprintf("ev%d", eventID))
			mustExec(`INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions)
				VALUES (?, ?, ?, 'edit', 1, 0)`, eventID, s.id, tc.path)
		}
	}

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getContention(t *testing.T, srv *httptest.Server, query string) (contentionResponseDTO, []byte) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/files/contention" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var out contentionResponseDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if out.Paths == nil {
		t.Fatalf("paths decoded as null, want [] — body %s", body)
	}
	return out, body
}

const sharedFile = "/work/alpha/internal/reactor/reactor.go"

func TestContentionTwoActiveSessionsOnOnePath(t *testing.T) {
	srv := contentionServer(t,
		contentionSession{id: 1, project: 1, status: "active", cwd: "/work/alpha", title: "Cooling loop",
			touches: []contentionTouch{{sharedFile, 30 * time.Minute}, {sharedFile, 20 * time.Minute}}},
		contentionSession{id: 2, project: 1, status: "idle", cwd: "/work/alpha", title: "Magnet tuning",
			touches: []contentionTouch{{sharedFile, 10 * time.Minute}, {"/work/alpha/README.md", 5 * time.Minute}}},
	)
	got, body := getContention(t, srv, "")
	t.Logf("2-session repro response: %s", body)

	if got.Hours != contentionDefaultHours {
		t.Errorf("hours = %d, want default %d", got.Hours, contentionDefaultHours)
	}
	if len(got.Paths) != 1 || got.Paths[0].Path != sharedFile {
		t.Fatalf("paths = %+v, want exactly %s", got.Paths, sharedFile)
	}
	ss := got.Paths[0].Sessions
	if len(ss) != 2 {
		t.Fatalf("sessions = %+v, want 2", ss)
	}
	// Newest touch first: s2 (10m ago) before s1 (20m ago).
	if ss[0].SessionID != 2 || ss[1].SessionID != 1 {
		t.Errorf("session order = %d,%d, want 2,1 (newest touch first)", ss[0].SessionID, ss[1].SessionID)
	}
	if ss[1].Changes != 2 || ss[0].Changes != 1 {
		t.Errorf("changes = s2:%d s1:%d, want 1 and 2", ss[0].Changes, ss[1].Changes)
	}
	if ss[0].ProjectSlug != "-work-alpha" || ss[0].Status != "idle" || ss[0].Title == nil || *ss[0].Title != "Magnet tuning" {
		t.Errorf("session[0] = %+v, want s2 fields populated", ss[0])
	}

	// Project scoping: alpha matches (by slug and by pretty name), beta does not.
	if scoped, _ := getContention(t, srv, "?project=alpha"); len(scoped.Paths) != 1 {
		t.Errorf("project=alpha paths = %d, want 1", len(scoped.Paths))
	}
	if scoped, _ := getContention(t, srv, "?project=-work-beta"); len(scoped.Paths) != 0 {
		t.Errorf("project=-work-beta paths = %d, want 0", len(scoped.Paths))
	}
}

func TestContentionActivePlusCompletedIsNotContention(t *testing.T) {
	srv := contentionServer(t,
		contentionSession{id: 1, project: 1, status: "active", cwd: "/work/alpha",
			touches: []contentionTouch{{sharedFile, 10 * time.Minute}}},
		contentionSession{id: 2, project: 1, status: "completed", cwd: "/work/alpha",
			touches: []contentionTouch{{sharedFile, 5 * time.Minute}}},
		contentionSession{id: 3, project: 1, status: "killed", cwd: "/work/alpha",
			touches: []contentionTouch{{sharedFile, 5 * time.Minute}}},
	)
	if got, _ := getContention(t, srv, ""); len(got.Paths) != 0 {
		t.Errorf("paths = %+v, want none (completed/killed sessions are not live)", got.Paths)
	}
}

func TestContentionRelativePathResolvesAgainstCwd(t *testing.T) {
	srv := contentionServer(t,
		contentionSession{id: 1, project: 1, status: "active", cwd: "/work/alpha",
			touches: []contentionTouch{{"internal/reactor/reactor.go", 10 * time.Minute}}},
		contentionSession{id: 2, project: 1, status: "waiting_approval", cwd: "/work/alpha/internal",
			touches: []contentionTouch{{sharedFile, 5 * time.Minute}}},
		// A worktree has its own absolute root: same relative path, different file.
		contentionSession{id: 3, project: 1, status: "active", cwd: "/work/alpha-wt/feature",
			touches: []contentionTouch{{"internal/reactor/reactor.go", 5 * time.Minute}}},
	)
	got, _ := getContention(t, srv, "")
	if len(got.Paths) != 1 || got.Paths[0].Path != sharedFile {
		t.Fatalf("paths = %+v, want exactly %s", got.Paths, sharedFile)
	}
	if n := len(got.Paths[0].Sessions); n != 2 {
		t.Errorf("sessions = %d, want 2 (s1 relative + s2 absolute; the worktree s3 is a different file)", n)
	}
}

func TestContentionTouchOutsideWindowIsIgnored(t *testing.T) {
	srv := contentionServer(t,
		contentionSession{id: 1, project: 1, status: "active", cwd: "/work/alpha",
			touches: []contentionTouch{{sharedFile, 10 * time.Minute}}},
		contentionSession{id: 2, project: 1, status: "active", cwd: "/work/alpha",
			touches: []contentionTouch{{sharedFile, 3 * time.Hour}}},
	)
	if got, _ := getContention(t, srv, "?hours=2"); len(got.Paths) != 0 {
		t.Errorf("hours=2 paths = %+v, want none (s2's touch is 3h old)", got.Paths)
	}
	// Widening the window brings it back — the same fixture is contention at 6h.
	if got, _ := getContention(t, srv, "?hours=6"); len(got.Paths) != 1 {
		t.Errorf("hours=6 paths = %d, want 1", len(got.Paths))
	}
}

func TestContentionHiddenSessionIsExcluded(t *testing.T) {
	srv := contentionServer(t,
		contentionSession{id: 1, project: 1, status: "active", cwd: "/work/alpha",
			touches: []contentionTouch{{sharedFile, 10 * time.Minute}}},
		contentionSession{id: 2, project: 1, status: "active", cwd: "/work/alpha", hidden: 1,
			touches: []contentionTouch{{sharedFile, 5 * time.Minute}}},
	)
	if got, _ := getContention(t, srv, ""); len(got.Paths) != 0 {
		t.Errorf("paths = %+v, want none (hidden session excluded)", got.Paths)
	}
}

func TestContentionHoursClamp(t *testing.T) {
	srv := contentionServer(t)
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?hours=0", contentionMinHours},
		{"?hours=-5", contentionMinHours},
		{"?hours=500", contentionMaxHours},
		{"?hours=12", 12},
	} {
		got, body := getContention(t, srv, tc.query)
		if got.Hours != tc.want {
			t.Errorf("%s hours = %d, want %d", tc.query, got.Hours, tc.want)
		}
		if string(body) == "" || got.Paths == nil {
			t.Errorf("%s: paths must be [] on an empty store", tc.query)
		}
	}

	resp, err := http.Get(srv.URL + "/api/files/contention?hours=abc")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("hours=abc status = %d, want 400", resp.StatusCode)
	}
}

func TestContentionCapsAtFiftyPathsNewestFirst(t *testing.T) {
	// 51 contended paths; path i is touched by both sessions i+1 minutes ago,
	// so path 0 is the newest and path 50 the oldest — the one the cap drops.
	var s1, s2 []contentionTouch
	for i := range contentionPathsLimit + 1 {
		p := fmt.Sprintf("/work/alpha/pkg/f%02d.go", i)
		ago := time.Duration(i+1) * time.Minute
		s1 = append(s1, contentionTouch{p, ago})
		s2 = append(s2, contentionTouch{p, ago})
	}
	srv := contentionServer(t,
		contentionSession{id: 1, project: 1, status: "active", cwd: "/work/alpha", touches: s1},
		contentionSession{id: 2, project: 1, status: "active", cwd: "/work/alpha", touches: s2},
	)
	got, _ := getContention(t, srv, "")
	if len(got.Paths) != contentionPathsLimit {
		t.Fatalf("paths = %d, want %d (capped)", len(got.Paths), contentionPathsLimit)
	}
	for i, p := range got.Paths {
		if want := fmt.Sprintf("/work/alpha/pkg/f%02d.go", i); p.Path != want {
			t.Fatalf("paths[%d] = %s, want %s (newest touch first)", i, p.Path, want)
		}
	}
}

func TestContentionOneSessionTwoSpellingsMergesIntoOneEntry(t *testing.T) {
	srv := contentionServer(t,
		// s1 reaches reactor.go both relative-to-cwd and absolute.
		contentionSession{id: 1, project: 1, status: "active", cwd: "/work/alpha",
			touches: []contentionTouch{
				{"internal/reactor/reactor.go", 30 * time.Minute},
				{sharedFile, 5 * time.Minute},
			}},
		contentionSession{id: 2, project: 1, status: "active", cwd: "/work/alpha",
			touches: []contentionTouch{{sharedFile, 10 * time.Minute}}},
	)
	got, _ := getContention(t, srv, "")
	if len(got.Paths) != 1 || got.Paths[0].Path != sharedFile {
		t.Fatalf("paths = %+v, want exactly %s", got.Paths, sharedFile)
	}
	ss := got.Paths[0].Sessions
	if len(ss) != 2 {
		t.Fatalf("sessions = %+v, want 2 (s1's two spellings merge into one entry)", ss)
	}
	// s1 carries the newest touch (5m, absolute spelling) and both changes.
	if ss[0].SessionID != 1 || ss[0].Changes != 2 {
		t.Errorf("sessions[0] = %+v, want session 1 with 2 merged changes", ss[0])
	}
	if want := time.Now().Add(-5 * time.Minute); ss[0].LastTouched < want.Add(-time.Minute).UTC().Format(contentionTSFormat) {
		t.Errorf("sessions[0].lastTouched = %s, want the newer (≈5m ago) spelling's touch", ss[0].LastTouched)
	}
}
