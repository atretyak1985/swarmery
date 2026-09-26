package api

import (
	"encoding/json"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// A run with no timeline must encode runEvents as [] — the Plans page filters
// over it unconditionally, and null crashed the whole view.
func TestRunEventsEncodeAsArrayWhenEmpty(t *testing.T) {
	b, err := json.Marshal(struct {
		RunEvents []runcore.RunEvent `json:"runEvents"`
	}{runEventsOrEmpty(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"runEvents":[]}` {
		t.Errorf("encoded = %s, want {\"runEvents\":[]}", got)
	}
}
