package runcore

// The explicit-default half of the account contract at the daemon seam.
//
// `swarmery install --claude-config-dir` bakes a CLAUDE_CONFIG_DIR into the
// daemon's own environment. An UNBOUND project is documented to inherit it. A
// project the operator EXPLICITLY bound to the default account must not: the
// CLI selects ~/.claude by the variable's absence, so the spawn has to remove
// what the daemon inherited, or the run executes under the baked account while
// `swarmery account which` reports "default".

import (
	"context"
	"strings"
	"testing"
	"time"
)

const bakedConfigDir = "/baked/by/launchd/.claude-work"

// childConfigDir runs the real ClaudeRunner against a fake `claude` that reports
// the CLAUDE_CONFIG_DIR it saw, or absentMarker when the variable is unset.
func childConfigDir(t *testing.T, account string) string {
	t.Helper()
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-cfg-" + account, Cwd: t.TempDir(), Account: account,
		Bin:           fakeBin(t, `printf '%s\n' "${CLAUDE_CONFIG_DIR-`+absentMarker+`}"`+"\n"),
		Timeout:       30 * time.Second,
		CaptureStdout: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return strings.TrimSpace(res.Output)
}

func TestStart_ExplicitDefaultBindingDropsTheDaemonsBakedConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", bakedConfigDir)

	if got := childConfigDir(t, "default"); got != absentMarker {
		t.Fatalf("a spawn bound to the default account saw CLAUDE_CONFIG_DIR=%q, want it ABSENT — "+
			"the daemon's baked account overrode the operator's explicit binding", got)
	}
	// The documented counterpart: no binding at all inherits the baked value.
	if got := childConfigDir(t, ""); got != bakedConfigDir {
		t.Fatalf("an unbound spawn saw CLAUDE_CONFIG_DIR=%q, want the inherited %q", got, bakedConfigDir)
	}
}
