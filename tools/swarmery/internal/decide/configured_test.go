package decide

import "testing"

// The claude backend alone is not a configuration: no shipped question sets
// AllowRemote, so D1/D2 would only write "no backend" error rows.
func TestClaudeAloneIsNotConfigured(t *testing.T) {
	if (&Engine{Claude: &stub{name: BackendClaude}}).Configured() {
		t.Fatal("a claude-only engine claims to be configured")
	}
	if !(&Engine{Local: &stub{name: BackendLocal}}).Configured() {
		t.Fatal("a local backend must configure the engine")
	}
}
