package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasereport"
)

func TestPhaseRunsReportAPI(t *testing.T) {
	srv, db := reviewServer(t, t.TempDir())
	if _, err := db.Exec(`INSERT INTO phase_actuals (phase_id, session_uuid, outcome, computed_at) VALUES
		(1, 'a', 'noop', '2026-10-01T00:00:00Z'), (1, 'b', 'completed', '2026-10-02T23:59:59.5Z'),
		(1, 'c', 'completed', '2026-10-03T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"", "?from=2026-10-01", "?to=2026-10-02", "?from=yesterday&to=2026-10-02", "?from=2026-10-02&to=2026-10-01"} {
		resp, err := http.Get(srv.URL + "/api/phaseruns/report" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %q = %d, want 400", q, resp.StatusCode)
		}
	}

	resp, err := http.Get(srv.URL + "/api/phaseruns/report?from=2026-10-01&to=2026-10-02")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got phasereport.Report
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if row, _ := got.Row(phasereport.KeyRuns); row.N != 2 {
		t.Errorf("runs = %d, want 2", row.N)
	}
	// The API body IS the package's report: same JSON as a direct Build.
	from, to, _ := phasereport.ParseWindow("2026-10-01", "2026-10-02")
	want, err := phasereport.Build(db, from, to)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("API JSON differs from Build:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}
