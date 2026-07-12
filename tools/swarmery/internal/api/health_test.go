package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/version"
)

// TestHealth pins the FROZEN parity contract:
// {"status":"ok","version":"<semver>","db_size_bytes":<int>,"watching":<bool>}
func TestHealth(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, watching := range []bool{false, true} {
		h, err := NewServer(db, watching)
		if err != nil {
			t.Fatalf("new server: %v", err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/api/health")
		if err != nil {
			t.Fatalf("GET /api/health: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		// Exact key set (JSON keys are frozen verbatim).
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(body, &keys); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, k := range []string{"status", "version", "db_size_bytes", "watching"} {
			if _, ok := keys[k]; !ok {
				t.Errorf("watching=%v: missing key %q in %s", watching, k, body)
			}
		}
		if len(keys) != 4 {
			t.Errorf("watching=%v: got %d keys, want 4: %s", watching, len(keys), body)
		}

		var got struct {
			Status      string `json:"status"`
			Version     string `json:"version"`
			DBSizeBytes int64  `json:"db_size_bytes"`
			Watching    bool   `json:"watching"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Status != "ok" {
			t.Errorf("status = %q, want ok", got.Status)
		}
		if got.Version != version.Version {
			t.Errorf("version = %q, want the shared constant %q", got.Version, version.Version)
		}
		if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(got.Version) {
			t.Errorf("version = %q, want semver", got.Version)
		}
		if got.DBSizeBytes <= 0 {
			t.Errorf("db_size_bytes = %d, want > 0 (migrated schema has pages)", got.DBSizeBytes)
		}
		if got.Watching != watching {
			t.Errorf("watching = %v, want %v", got.Watching, watching)
		}
	}
}
