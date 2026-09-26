package notify

import "testing"

// phase_surprise is a first-class --notify-events value (learning loop phase 13).
func TestPhaseSurpriseIsAKnownEvent(t *testing.T) {
	n, err := New(Config{URL: "http://127.0.0.1:1/x", Events: []string{EventPhaseSurprise}})
	if err != nil {
		t.Fatalf("phase_surprise must be accepted by --notify-events: %v", err)
	}
	defer n.Close()
	if !n.enabled[EventPhaseSurprise] {
		t.Error("phase_surprise configured but not enabled")
	}
}
