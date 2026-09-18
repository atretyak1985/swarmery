package claudeacct

import (
	"slices"
	"strings"
	"testing"
)

// The binding owns CLAUDE_CONFIG_DIR. A store that tries to re-point it is
// refused for that one line — and only that line — with a log naming the
// variable, which is not a secret.
func TestParseSecretEnv_RefusesTheConfigDirVariable(t *testing.T) {
	var got []string
	logged := captureLog(t, func() {
		got = parseSecretEnv("store.env", strings.NewReader(
			"MCP_TOKEN=abc\nCLAUDE_CONFIG_DIR=/elsewhere/.claude-other\nOTHER=1\n"))
	})
	want := []string{"MCP_TOKEN=abc", "OTHER=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseSecretEnv = %v, want %v (the config-dir line dropped, nothing else)", got, want)
	}
	if !strings.Contains(logged, "CLAUDE_CONFIG_DIR") || !strings.Contains(logged, "line 2") {
		t.Errorf("log = %q, want it to name CLAUDE_CONFIG_DIR and line 2", logged)
	}
	if strings.Contains(logged, "/elsewhere") {
		t.Errorf("log = %q leaked the line's VALUE", logged)
	}
}
