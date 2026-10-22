package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
)

// The phase's workaround hits are served by their own route and ride the epic
// payload; a phase of another plan is a 404, and a phase with none carries [].
func TestWorkaroundHitsRouteAndEpicPayload(t *testing.T) {
	f := newReopenFixture(t, "idle")
	ins := `INSERT INTO workaround_hits (session_uuid, turn_id, phase_id, agent_name, word, sentence, sentence_hash,
		context_before, context_after, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-10-22T00:00:00Z')`
	for _, row := range [][]any{
		{"run-1", 1, f.phaseID, nil, "instead", "I used Edit instead.", "h1", "The guard blocked it.", ""},
		{"run-1", 2, f.phaseID, "core:implementation-agent", "поки що", "Поки що лишаю як є.", "h2", "", "Далі тести."},
		{"run-1", 3, f.phaseID, nil, "skip", "", "h3", "", ""}, // past the cap: key only
	} {
		if _, err := f.db.Exec(ins, row...); err != nil {
			t.Fatal(err)
		}
	}

	get := func(url string) (int, workaround.PhaseHits) {
		t.Helper()
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out workaround.PhaseHits
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, out
	}
	code, list := get(f.url(f.taskID, f.phaseID, "workaround-hits"))
	if code != http.StatusOK || list.Total != 3 || len(list.Hits) != 2 {
		t.Fatalf("GET = %d %+v, want 2 hits with text out of 3", code, list)
	}
	if h := list.Hits[1]; h.Word != "поки що" || h.AgentName != "core:implementation-agent" || h.After != "Далі тести." {
		t.Errorf("second hit = %+v", h)
	}
	if code, list := get(f.url(f.taskID, f.phaseID, "workaround-hits?session=other")); code != http.StatusOK || list.Total != 0 || list.Hits == nil {
		t.Errorf("?session=other = %d %+v, want 200 with [] and 0", code, list)
	}
	if code, _ := get(f.url(f.taskID+99, f.phaseID, "workaround-hits")); code != http.StatusNotFound {
		t.Errorf("a phase addressed through another plan = %d, want 404", code)
	}

	h := &Handler{DB: f.db}
	phases, _, _, err := h.epicPhases(f.taskID, filepath.Dir(f.docPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 1 || phases[0].WorkaroundTotal != 3 || len(phases[0].WorkaroundHits) != 2 ||
		phases[0].WorkaroundHits[0].Sentence != "I used Edit instead." {
		t.Errorf("epic DTO = %+v", phases)
	}

	if _, err := f.db.Exec(`DELETE FROM workaround_hits`); err != nil {
		t.Fatal(err)
	}
	phases, _, _, err = h.epicPhases(f.taskID, filepath.Dir(f.docPath))
	if err != nil || phases[0].WorkaroundHits == nil || phases[0].WorkaroundTotal != 0 {
		t.Errorf("a phase without hits must carry [] and 0: %+v %v", phases, err)
	}
}
