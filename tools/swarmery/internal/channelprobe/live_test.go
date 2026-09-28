//go:build ccprobe

// The live half of the channel probe. It spawns the real `claude` CLI through
// scripts/tests/cc-channel-probe.sh, so it is build-tagged OFF: a plain
// `go test ./...` never compiles this file, and CI (which has no CLI and no
// login) can never try to run it. Run it on a machine with the CLI installed:
//
//	cd tools/swarmery && go test -tags ccprobe ./internal/channelprobe/ -run TestLiveChannelProbe -v
//
// It costs no tokens: the harness observes substitution through `claude mcp
// list`, which makes no model call.
package channelprobe

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
)

func TestLiveChannelProbe(t *testing.T) {
	bin, err := claudebin.Resolve()
	if err != nil {
		t.Skipf("claude CLI not resolvable (%v) — nothing to probe", err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("claude CLI %s not present (%v) — nothing to probe", bin, err)
	}

	// tools/swarmery/internal/channelprobe → repo root is four levels up.
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "scripts", "tests", "cc-channel-probe.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("harness not found at %s: %v", script, err)
	}

	dir := filepath.Join(t.TempDir(), "probes")
	cmd := exec.Command("bash", script, "--json")
	cmd.Env = append(os.Environ(), "SWARMERY_PROBES_DIR="+dir, "SWARMERY_CLAUDE_BIN="+bin)
	out, err := cmd.CombinedOutput()
	t.Logf("harness output:\n%s", out)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		// The harness saw a drift; fall through so Compare names it.
	default:
		t.Fatalf("harness failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one result in %s, got %d (%v)", dir, len(entries), err)
	}
	got, err := Load(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	base, err := Baseline()
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	for _, d := range Compare(base, got) {
		t.Errorf("DRIFT on CLI %s: %s.%s = %s, baseline says %s", d.CLIVersion, d.Fact, d.Observation, d.Got, d.Want)
	}
	u := Unobserved(base, got)
	if total := baselineObservations(base); total > 0 && len(u) == total {
		// A resolved CLI that yielded not one observation is not "inconclusive":
		// the likeliest cause is a changed `claude mcp list` output format, and
		// a probe that went blind must not pass.
		t.Fatalf("BLIND on CLI %s (%s): the harness observed none of the %d baseline observations — "+
			"has the `claude mcp list` detail line changed? Unobserved: %v", got.CLIVersion, bin, total, u)
	}
	if len(u) > 0 {
		// A partial gap is not a drift: say so loudly and leave the verdict to
		// the harness's own "inconclusive".
		t.Logf("INCONCLUSIVE on CLI %s — observations not made: %v", got.CLIVersion, u)
	}
}

// baselineObservations counts every observation the baseline expects, across
// all facts — the denominator for "the probe saw nothing".
func baselineObservations(base Result) int {
	n := 0
	for _, f := range base.Facts {
		n += len(f.Observed)
	}
	return n
}
