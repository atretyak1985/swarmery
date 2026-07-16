package api

import (
	"net/http"
	"testing"
)

type fileSessionsResp struct {
	Path     string `json:"path"`
	Sessions []struct {
		SessionID   int64   `json:"sessionId"`
		Title       *string `json:"title"`
		ProjectSlug string  `json:"projectSlug"`
		Status      string  `json:"status"`
		StartedAt   string  `json:"startedAt"`
		Changes     int64   `json:"changes"`
		LastTouched string  `json:"lastTouched"`
	} `json:"sessions"`
}

func TestFileSessions(t *testing.T) {
	srv := searchServer(t)

	t.Run("sessions grouped with counts, ordered by recency", func(t *testing.T) {
		var r fileSessionsResp
		getJSON(t, srv.URL+"/api/files/sessions?path=reactor.go", &r)
		if r.Path != "reactor.go" {
			t.Errorf("path = %q, want reactor.go", r.Path)
		}
		if len(r.Sessions) != 2 {
			t.Fatalf("sessions = %d, want 2", len(r.Sessions))
		}
		// Session 2 touched the file last (11:30) with 2 changes → first.
		if r.Sessions[0].SessionID != 2 || r.Sessions[0].Changes != 2 ||
			r.Sessions[0].LastTouched != "2026-07-15T11:30:00.000Z" {
			t.Errorf("sessions[0] = %+v, want session 2, 2 changes, last 11:30", r.Sessions[0])
		}
		if r.Sessions[1].SessionID != 1 || r.Sessions[1].Changes != 1 {
			t.Errorf("sessions[1] = %+v, want session 1 with 1 change", r.Sessions[1])
		}
	})

	t.Run("project scoping", func(t *testing.T) {
		var r fileSessionsResp
		getJSON(t, srv.URL+"/api/files/sessions?path=reactor.go&project=-work-beta", &r)
		if len(r.Sessions) != 0 {
			t.Errorf("beta-scoped sessions = %d, want 0", len(r.Sessions))
		}
	})

	t.Run("no match → empty array, not null", func(t *testing.T) {
		var r fileSessionsResp
		getJSON(t, srv.URL+"/api/files/sessions?path=nonexistent.xyz", &r)
		if r.Sessions == nil || len(r.Sessions) != 0 {
			t.Errorf("sessions = %v, want []", r.Sessions)
		}
	})

	t.Run("missing path → 400", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/files/sessions")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})
}
