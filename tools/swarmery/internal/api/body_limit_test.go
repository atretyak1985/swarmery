package api

// Every JSON request body the daemon decodes is bounded: a body past the cap
// is refused with 413 instead of being buffered without limit. One row per
// handler that used to decode r.Body unbounded; each is driven directly (no
// origin/auth middleware) so the row exercises the handler's own decode.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
)

// oversizedJSONBody is a syntactically valid JSON object a little over 2 MiB,
// so a decoder must read past any 1 MiB cap to finish it.
func oversizedJSONBody() string {
	return `{"text":"` + strings.Repeat("a", 2<<20) + `"}`
}

func TestOversizedBodiesAre413(t *testing.T) {
	AttachConnectorReader(mcpcfg.NewWithRunner(func(context.Context, ...string) ([]byte, error) {
		t.Error("an oversized body must be refused before the CLI runs")
		return nil, nil
	}))
	t.Cleanup(func() { AttachConnectorReader(nil) })
	prevEditor := hookEditor
	AttachHookEditor(sysedit.New(nil, nil, sysedit.Config{}))
	t.Cleanup(func() { AttachHookEditor(prevEditor) })

	// A real (empty) store: a handler that ignored its decode error used to
	// carry on into the database, so the pre-fix run needs one to fail cleanly.
	db, err := store.Open(filepath.Join(t.TempDir(), "limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &Handler{DB: db}
	cases := []struct {
		name, method, pattern, path string
		handler                     http.HandlerFunc
	}{
		{"session patch", "PATCH", "PATCH /api/sessions/{id}", "/api/sessions/1", h.patchSession},
		{"hook session-start", "POST", "POST /api/hooks/session-start", "/api/hooks/session-start", h.hookSessionStart},
		{"session kill", "POST", "POST /api/sessions/{id}/kill", "/api/sessions/1/kill", h.KillSession},
		{"connector add", "POST", "POST /api/connectors", "/api/connectors", h.addConnector},
		{"system hook toggle", "POST", "POST /api/system/hooks/{id}/toggle", "/api/system/hooks/1/toggle", h.toggleSystemHook},
		{"system hook update", "PUT", "PUT /api/system/hooks/{id}", "/api/system/hooks/1", h.updateSystemHook},
		{"session message", "POST", "POST /api/sessions/{id}/message", "/api/sessions/1/message", h.PostSessionMessage},
		{"retro recommendation", "PATCH", "PATCH /api/retro/recommendations/{id}", "/api/retro/recommendations/1", h.patchRecommendation},
		{"retro proposal", "PATCH", "PATCH /api/retro/proposals/{id}", "/api/retro/proposals/1", h.patchProposal},
		{"project patch", "PATCH", "PATCH /api/projects/{id}", "/api/projects/1", h.patchProject},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(c.pattern, c.handler)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(oversizedJSONBody()))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("%s %s with a 2 MiB body = %d, want 413 (body %.120q)", c.method, c.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// A body under the cap is decoded exactly as before: the limit is not a new
// failure mode for an ordinary request.
func TestDecodeJSONLimitedPassesSmallBodies(t *testing.T) {
	var v struct {
		Text string `json:"text"`
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"hi"}`))
	if err := decodeJSONLimited(rec, req, &v, maxJSONBody); err != nil {
		t.Fatalf("small body: %v", err)
	}
	if v.Text != "hi" {
		t.Errorf("decoded %q, want hi", v.Text)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("a decoded body must not write a response (code %d, %q)", rec.Code, rec.Body.String())
	}
}
