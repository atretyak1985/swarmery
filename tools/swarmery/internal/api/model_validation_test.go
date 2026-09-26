package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// The gate reads this endpoint and blocks on anything that is not a pass, so
// both outcomes matter: a recorded verdict must come back intact, and an
// unevaluated model must 404 rather than defaulting to something reassuring.
func TestModelValidationEndpoint(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		INSERT INTO model_validations
		  (model, golden_set_version, verdict, score, trajectories, agents_covered, detail, created_at)
		VALUES ('claude-opus-6','2026-09-1','pass',3.4,42,7,'ok','2026-09-02T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db})

	t.Run("recorded verdict", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/models/claude-opus-6/validation", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["verdict"] != "pass" || got["goldenSetVersion"] != "2026-09-1" {
			t.Errorf("body = %v, want the recorded pass and its golden set version", got)
		}
		if got["trajectories"].(float64) != 42 {
			t.Errorf("trajectories = %v, want 42 — the gate reports how thin the "+
				"evidence was, not just the verdict", got["trajectories"])
		}
	})

	// The context-window marker is a REQUEST property, not a different model.
	// modeleval.Evaluate bases the id before it writes, so the row above is the
	// row a 1M-window session must find; if the read side does not base too, the
	// hook's lookup of `…-5-5[1m]/validation` 404s, the gate reads that as "no
	// recorded validation" and blocks a model the operator has already validated.
	t.Run("a context-marked id resolves to the same row", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/models/claude-opus-6[1m]/validation", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["verdict"] != "pass" {
			t.Errorf("verdict = %v, want pass — both spellings are one model", got["verdict"])
		}
		if got["model"] != "claude-opus-6" {
			t.Errorf("model = %v, want the based id claude-opus-6", got["model"])
		}
	})

	t.Run("never evaluated is 404, not a default", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/models/claude-opus-9/validation", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404: an unknown model must not read as fine", rec.Code)
		}
	})
}
