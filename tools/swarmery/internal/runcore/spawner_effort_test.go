package runcore

import (
	"strings"
	"testing"
)

// Args is the one argv builder five engines share, so --effort has to be pinned
// here as well as per-engine: the order is a CHOICE (the CLI is
// order-insensitive), and an accidental reordering would be invisible to
// everything except an assertion like this one.
func TestArgs_Effort(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec Spec
		want []string
	}{
		{
			name: "effort sits beside the model",
			spec: Spec{Prompt: "p", SessionUUID: "u", Model: "m", Effort: "high"},
			want: []string{"-p", "p", "--session-id", "u", "--model", "m", "--effort", "high"},
		},
		{
			// "" is an explicit "off", reachable only through the claudeflags
			// escape hatch — never through a typo, which degrades to the
			// engine's default instead.
			name: "empty effort omits the flag",
			spec: Spec{Prompt: "p", SessionUUID: "u", Model: "m"},
			want: []string{"-p", "p", "--session-id", "u", "--model", "m"},
		},
		{
			name: "whitespace is not a value",
			spec: Spec{Prompt: "p", SessionUUID: "u", Effort: "   "},
			want: []string{"-p", "p", "--session-id", "u"},
		},
		{
			// An engine may pin depth without pinning a model (the model then
			// comes from the spec's own ladder), so the two are independent.
			name: "effort without a model",
			spec: Spec{Prompt: "p", SessionUUID: "u", Effort: "low"},
			want: []string{"-p", "p", "--session-id", "u", "--effort", "low"},
		},
		{
			name: "full flag order",
			spec: Spec{
				Prompt: "p", SessionUUID: "u", SettingSources: "project,local",
				PermissionMode: "bypassPermissions", Agent: "tech-lead",
				Model: "m", Effort: "max", SettingsFile: "/s.json",
			},
			want: []string{
				"-p", "p", "--session-id", "u",
				"--setting-sources", "project,local",
				"--permission-mode", "bypassPermissions",
				"--agent", "tech-lead",
				"--model", "m", "--effort", "max",
				"--settings", "/s.json",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Args(tc.spec)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("Args = %q, want %q", got, tc.want)
			}
		})
	}
}
