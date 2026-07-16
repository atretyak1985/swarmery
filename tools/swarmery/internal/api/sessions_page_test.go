package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// pageSession is the thin projection the pagination tests need.
type pageSession struct {
	ID          int64  `json:"id"`
	SessionUUID string `json:"sessionUuid"`
	StartedAt   string `json:"startedAt"`
}

// pageEnvelope mirrors sessionsPageDTO.
type pageEnvelope struct {
	Sessions   []pageSession `json:"sessions"`
	NextCursor *string       `json:"nextCursor"`
}

// pageServer plants 7 sessions: five with distinct start times plus two
// sharing one timestamp, so the (started_at, id) keyset tiebreak is exercised.
func pageServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "page.db"))
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
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/p', '-work-p', 'P', '2026-07-01T00:00:00Z')`)
	for i := 1; i <= 5; i++ {
		mustExec(fmt.Sprintf(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at)
			VALUES (%d, 1, 'u%d', 'completed', '2026-07-%02dT10:00:00.000Z')`, i, i, i))
	}
	// 6 and 7 share a start time: DESC order must yield 7 before 6 (id DESC).
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(6, 1, 'u6', 'completed', '2026-07-06T10:00:00.000Z'),
		(7, 1, 'u7', 'completed', '2026-07-06T10:00:00.000Z')`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestSessionsEnvelopeDefault(t *testing.T) {
	srv := pageServer(t)
	var page pageEnvelope
	getJSON(t, srv.URL+"/api/sessions", &page)
	if len(page.Sessions) != 7 {
		t.Fatalf("sessions = %d, want 7", len(page.Sessions))
	}
	if page.NextCursor != nil {
		t.Errorf("nextCursor = %q, want null (fewer rows than the default limit)", *page.NextCursor)
	}
}

func TestSessionsCursorWalk(t *testing.T) {
	srv := pageServer(t)
	var got []int64
	url := srv.URL + "/api/sessions?limit=3"
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("cursor walk did not terminate")
		}
		var page pageEnvelope
		getJSON(t, url, &page)
		if len(page.Sessions) > 3 {
			t.Fatalf("page size = %d, want <= 3", len(page.Sessions))
		}
		for _, s := range page.Sessions {
			got = append(got, s.ID)
		}
		if page.NextCursor == nil {
			break
		}
		url = srv.URL + "/api/sessions?limit=3&cursor=" + *page.NextCursor
	}
	want := []int64{7, 6, 5, 4, 3, 2, 1} // started_at DESC, id DESC tiebreak; no dupes, no gaps
	if len(got) != len(want) {
		t.Fatalf("walked ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("walked ids = %v, want %v", got, want)
		}
	}
}

func TestSessionsLimitValidation(t *testing.T) {
	srv := pageServer(t)
	for _, q := range []string{"limit=0", "limit=-2", "limit=abc"} {
		resp, err := http.Get(srv.URL + "/api/sessions?" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, resp.StatusCode)
		}
	}
	// Over-max clamps to 500 instead of erroring.
	var page pageEnvelope
	getJSON(t, srv.URL+"/api/sessions?limit=9999", &page)
	if len(page.Sessions) != 7 {
		t.Errorf("clamped limit: sessions = %d, want 7", len(page.Sessions))
	}
}

func TestSessionsBadCursor(t *testing.T) {
	srv := pageServer(t)
	resp, err := http.Get(srv.URL + "/api/sessions?cursor=%21%21not-base64")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad cursor: status = %d, want 400", resp.StatusCode)
	}
}
