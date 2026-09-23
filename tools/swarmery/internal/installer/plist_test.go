package installer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlistGolden(t *testing.T) {
	const (
		binPath = "/Users/tester/.swarmery/bin/swarmery"
		logsDir = "/Users/tester/.swarmery/logs"
	)
	cases := []struct {
		name   string
		port   int
		extra  []EnvVar
		golden string
	}{
		{"default port omits EnvironmentVariables", 0, nil, "plist_default.golden"},
		{"explicit port writes SWARMERY_PORT", 8899, nil, "plist_with_port.golden"},
		{"onboard roots write EnvironmentVariables without a port", 0, []EnvVar{
			{Key: "SWARMERY_ONBOARD_ROOTS", Value: "/home/dev/projects"},
			{Key: "SWARMERY_WORKSPACE_ROOT", Value: "/home/dev/swarmery-workspace"},
		}, "plist_with_onboard.golden"},
		// Risk R5: the daemon resolves the REAL claude through the baked
		// override, never the accounts-pack PATH shim.
		{"claude-bin bakes SWARMERY_CLAUDE_BIN", 0, []EnvVar{
			{Key: "SWARMERY_ONBOARD_ROOTS", Value: "/home/dev/projects"},
			{Key: "SWARMERY_WORKSPACE_ROOT", Value: "/home/dev/swarmery-workspace"},
			{Key: "SWARMERY_CLAUDE_BIN", Value: "/opt/homebrew/bin/claude"},
		}, "plist_with_claude_bin.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			got := Plist(binPath, logsDir, tc.port, tc.extra...)
			if got != string(want) {
				t.Errorf("plist mismatch with %s\n--- got ---\n%s\n--- want ---\n%s", tc.golden, got, want)
			}
		})
	}
}

func TestParseLaunchdField(t *testing.T) {
	out := "com.swarmery.daemon = {\n\tactive count = 1\n\tpath = /x.plist\n\tstate = running\n\n\tprogram = /y\n\tpid = 4242\n}\n"
	if got := parseLaunchdField(out, "pid"); got != "4242" {
		t.Errorf("pid = %q, want 4242", got)
	}
	if got := parseLaunchdField(out, "state"); got != "running" {
		t.Errorf("state = %q, want running", got)
	}
	if got := parseLaunchdField(out, "missing"); got != "" {
		t.Errorf("missing = %q, want empty", got)
	}
}
