package phaserun

import (
	"os"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// TestMain opts this package's tests out of the executor deny list, so the argv
// pins (runner_test.go) keep pinning the flags they were written for. The deny
// list itself is pinned by runner_deny_test.go, which sets the knob back to its
// default.
func TestMain(m *testing.M) {
	os.Setenv(claudeflags.AgentDenyEnv, "0")
	os.Exit(m.Run())
}
