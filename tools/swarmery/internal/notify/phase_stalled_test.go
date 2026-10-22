package notify

import (
	"encoding/json"
	"io"
	"testing"
)

// phase_stalled is a first-class --notify-events value (verify-not-trust phase 11).
func TestPhaseStalledIsAKnownEvent(t *testing.T) {
	n, err := New(Config{URL: "http://127.0.0.1:1/x", Events: []string{EventPhaseStalled}})
	if err != nil {
		t.Fatalf("phase_stalled must be accepted by --notify-events: %v", err)
	}
	defer n.Close()
	if !n.enabled[EventPhaseStalled] {
		t.Error("phase_stalled configured but not enabled")
	}
}

// The generic JSON body carries the documented payload
// {taskId, phaseId, stage, reason, sinceMinutes, sessionUuid}.
func TestPhaseStalledEventPayload(t *testing.T) {
	e := PhaseStalledEvent(PhaseStalled{
		TaskID: 12, PhaseID: 34, PhaseSeq: 3, PhaseName: "Stall detector", PlanTitle: "Verify, not trust",
		Project: "swarmery", Stage: "reviewer", Reason: "ended", SinceMinutes: 25, SessionUUID: "rev-1",
		Detail: "the session ended without a verdict (25 min ago)",
	})
	if e.Title != "Phase stalled: Verify, not trust · Phase 3: Stall detector" {
		t.Errorf("Title = %q", e.Title)
	}
	if e.Body != "reviewer — the session ended without a verdict (25 min ago)" {
		t.Errorf("Body = %q", e.Body)
	}
	req, err := buildRequest(Config{URL: "http://receiver.local/hook"}.withDefaults(), e)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(req.Body)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "phase_stalled", "taskId": float64(12), "phaseId": float64(34), "stage": "reviewer",
		"reason": "ended", "sinceMinutes": float64(25), "sessionUuid": "rev-1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("payload[%s] = %v, want %v (body %s)", k, got[k], v, raw)
		}
	}
}
