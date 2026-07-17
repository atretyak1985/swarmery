package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func patchTestServer(t *testing.T) (*httptest.Server, *sql.DB, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "patch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen, last_activity)
		 VALUES ('/tmp/op', '-tmp-op', 'op', '2026-07-16T00:00:00Z', '2026-07-16T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	res, err := db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, status, started_at, source)
		 VALUES (1, 'u-outcome-1', 'completed', '2026-07-16T00:00:00Z', 'jsonl')`)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	id, _ := res.LastInsertId()

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, id
}

func TestPatchSessionOutcome(t *testing.T) {
	srv, db, id := patchTestServer(t)
	url := srv.URL + "/api/sessions/" + strconv.FormatInt(id, 10)

	doJSON(t, http.MethodPatch, url, map[string]any{"outcome": "success"}, http.StatusOK)

	var got sql.NullString
	if err := db.QueryRow(`SELECT outcome FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != "success" {
		t.Errorf("outcome = %v, want success", got)
	}

	// The shared projection surfaces it (list + detail + WS use sessionSelect).
	var detail struct {
		Outcome *string `json:"outcome"`
	}
	getJSON(t, url, &detail)
	if detail.Outcome == nil || *detail.Outcome != "success" {
		t.Errorf("detail outcome = %v, want success", detail.Outcome)
	}
}

func TestPatchSessionOutcomeClear(t *testing.T) {
	srv, db, id := patchTestServer(t)
	url := srv.URL + "/api/sessions/" + strconv.FormatInt(id, 10)

	doJSON(t, http.MethodPatch, url, map[string]any{"outcome": "fail"}, http.StatusOK)
	doJSON(t, http.MethodPatch, url, map[string]any{"outcome": nil}, http.StatusOK)

	var got sql.NullString
	if err := db.QueryRow(`SELECT outcome FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Errorf("outcome = %q, want NULL after clear", got.String)
	}
}

func TestPatchSessionOutcomeValidation(t *testing.T) {
	srv, _, id := patchTestServer(t)
	url := srv.URL + "/api/sessions/" + strconv.FormatInt(id, 10)

	doJSON(t, http.MethodPatch, url, map[string]any{"outcome": "meh"}, http.StatusBadRequest)
	doJSON(t, http.MethodPatch, url, map[string]any{}, http.StatusBadRequest)
	doJSON(t, http.MethodPatch, srv.URL+"/api/sessions/99999",
		map[string]any{"outcome": "success"}, http.StatusNotFound)
}

func sessionDetailTitle(t *testing.T, url string) *string {
	t.Helper()
	var detail struct {
		Title *string `json:"title"`
	}
	getJSON(t, url, &detail)
	return detail.Title
}

func TestPatchSessionTitle(t *testing.T) {
	srv, db, id := patchTestServer(t)
	url := srv.URL + "/api/sessions/" + strconv.FormatInt(id, 10)

	doJSON(t, http.MethodPatch, url, map[string]any{"title": "Deploy hotfix"}, http.StatusOK)

	var custom sql.NullString
	if err := db.QueryRow(`SELECT custom_title FROM sessions WHERE id = ?`, id).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if !custom.Valid || custom.String != "Deploy hotfix" {
		t.Errorf("custom_title = %v, want 'Deploy hotfix'", custom)
	}
	if got := sessionDetailTitle(t, url); got == nil || *got != "Deploy hotfix" {
		t.Errorf("detail title = %v, want 'Deploy hotfix'", got)
	}
}

func TestPatchSessionTitleClearRevertsToIngested(t *testing.T) {
	srv, db, id := patchTestServer(t)
	url := srv.URL + "/api/sessions/" + strconv.FormatInt(id, 10)
	if _, err := db.Exec(`UPDATE sessions SET title = 'ingested name' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	// A custom title wins over the ingested one.
	doJSON(t, http.MethodPatch, url, map[string]any{"title": "manual name"}, http.StatusOK)
	if got := sessionDetailTitle(t, url); got == nil || *got != "manual name" {
		t.Errorf("title = %v, want custom 'manual name'", got)
	}

	// Clearing (null) reverts to the ingested title, not empty.
	doJSON(t, http.MethodPatch, url, map[string]any{"title": nil}, http.StatusOK)
	if got := sessionDetailTitle(t, url); got == nil || *got != "ingested name" {
		t.Errorf("after clear title = %v, want 'ingested name'", got)
	}
	// Blank string clears too.
	doJSON(t, http.MethodPatch, url, map[string]any{"title": "x"}, http.StatusOK)
	doJSON(t, http.MethodPatch, url, map[string]any{"title": "   "}, http.StatusOK)
	if got := sessionDetailTitle(t, url); got == nil || *got != "ingested name" {
		t.Errorf("after blank-clear title = %v, want 'ingested name'", got)
	}
}

func TestPatchSessionTitleTrimAndCap(t *testing.T) {
	srv, db, id := patchTestServer(t)
	url := srv.URL + "/api/sessions/" + strconv.FormatInt(id, 10)

	doJSON(t, http.MethodPatch, url, map[string]any{"title": "  padded  "}, http.StatusOK)
	var custom string
	if err := db.QueryRow(`SELECT custom_title FROM sessions WHERE id = ?`, id).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if custom != "padded" {
		t.Errorf("custom_title = %q, want trimmed 'padded'", custom)
	}

	long := make([]byte, sessionTitleLimit+50)
	for i := range long {
		long[i] = 'a'
	}
	doJSON(t, http.MethodPatch, url, map[string]any{"title": string(long)}, http.StatusOK)
	if err := db.QueryRow(`SELECT custom_title FROM sessions WHERE id = ?`, id).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if len(custom) != sessionTitleLimit {
		t.Errorf("custom_title len = %d, want capped %d", len(custom), sessionTitleLimit)
	}
}

// The DELETE soft-hide contract must survive the PATCH addition untouched.
func TestPatchDoesNotBreakSoftHide(t *testing.T) {
	srv, _, id := patchTestServer(t)
	doJSON(t, http.MethodDelete, srv.URL+"/api/sessions/"+strconv.FormatInt(id, 10), nil, http.StatusOK)
	if n := sessionsListLen(t, srv.URL); n != 0 {
		t.Errorf("after hide: list len = %d, want 0", n)
	}
}
