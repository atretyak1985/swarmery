package repoprovider

import (
	"context"
	"strings"
	"testing"
)

// The VCS-token scrub (internal/runcore) is ON by default and applies to the
// AGENTS the daemon spawns. The daemon's own land path — push, PR create, merge
// polling — runs through this package's OSExec, which composes its env from the
// daemon's own environment plus credstore's delta and never through runcore. This
// pins that split: with the scrub knob at its default the daemon's gh/git calls
// still see the token, so landing keeps working after the default flipped.
func TestOSExec_KeepsVCSTokensWithAgentScrubOn(t *testing.T) {
	t.Setenv("SWARMERY_AGENT_SCRUB_VCS_TOKENS", "") // the default: agents scrubbed
	t.Setenv("GH_TOKEN", "fake-land-token")
	out, _, err := OSExec{}.Run(context.Background(), t.TempDir(), []string{"GLAB_TOKEN=fake-glab"},
		"sh", "-c", `printf '%s|%s' "${GH_TOKEN-absent}" "${GLAB_TOKEN-absent}"`)
	if err != nil {
		t.Fatalf("OSExec.Run: %v", err)
	}
	if got := strings.TrimSpace(out); got != "fake-land-token|fake-glab" {
		t.Errorf("daemon-side exec env = %q, want both tokens present — the land path must not be scrubbed", got)
	}
}
