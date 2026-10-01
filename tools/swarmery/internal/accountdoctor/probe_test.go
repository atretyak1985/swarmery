package accountdoctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/channelprobe"
)

// versionedCLI plants <root>/versions/<v> and a `claude` symlink to it, the
// layout the native installer uses, and points SWARMERY_CLAUDE_BIN at the link.
func versionedCLI(t *testing.T, v string) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "versions", v)
	mustWrite(t, bin, "#!/bin/sh\n", 0o755)
	link := filepath.Join(root, "claude")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", link)
	return link
}

// plantResult writes testdata/<name> re-stamped to version v into the probes
// dir through channelprobe.Save (0700 / 0600).
func plantResult(t *testing.T, name, v string) string {
	t.Helper()
	r, err := channelprobe.Load(copyFixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	r.CLIVersion = v
	p, err := channelprobe.Save(channelprobe.Path(), r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// copyFixture copies a channelprobe testdata file to a 0600 file in a 0700 dir
// (Load refuses anything more open).
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "channelprobe", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	chmod(t, dir, 0o700)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCLIVersionFromPath(t *testing.T) {
	newFixture(t)
	link := versionedCLI(t, "2.1.290")
	if v, ok := CLIVersionFromPath(link); !ok || v != "2.1.290" {
		t.Errorf("CLIVersionFromPath(link) = %q %v", v, ok)
	}
	other := filepath.Join(t.TempDir(), "bin", "claude")
	mustWrite(t, other, "#!/bin/sh\n", 0o755)
	if _, ok := CLIVersionFromPath(other); ok {
		t.Error("a non-versions layout yielded a version")
	}
	if _, ok := CLIVersionFromPath(""); ok {
		t.Error("\"\" yielded a version")
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", other)
	rep, _ := Fast(Options{Path: t.TempDir()})
	if countFindings(rep, "probe-stale", SevWarn) != 1 {
		t.Errorf("any other layout must be probe-stale: %+v", rep.Findings)
	}
}

func TestProbeStale(t *testing.T) {
	newFixture(t)
	versionedCLI(t, "2.1.290")
	rep, _ := Fast(Options{Path: t.TempDir()})
	if countFindings(rep, "probe-stale", SevWarn) != 1 || countFindings(rep, "probe-drift", "") != 0 {
		t.Errorf("no file for V: %+v", rep.Findings)
	}
	plantResult(t, "result-2.1.280.json", "2.1.290")
	rep, _ = Fast(Options{Path: t.TempDir()})
	if countFindings(rep, "probe-stale", "")+countFindings(rep, "probe-drift", "") != 0 {
		t.Errorf("a matching verdict for V still reported: %+v", rep.Findings)
	}
}

func TestProbeDrift(t *testing.T) {
	newFixture(t)
	versionedCLI(t, "2.1.291")
	plantResult(t, "result-drifted.json", "2.1.291")
	rep, _ := Fast(Options{Path: t.TempDir()})
	if countFindings(rep, "probe-drift", SevError) != 1 {
		t.Fatalf("probe-drift = %+v", rep.Findings)
	}
	for _, f := range rep.Findings {
		if f.ID == "probe-drift" && !strings.Contains(f.Detail, "G1") {
			t.Errorf("detail = %q, want the drifted fact ids", f.Detail)
		}
	}
}

// fakeHarness replaces the runner: it records the env it was handed and, when
// write is set, writes a result for version v into $SWARMERY_PROBES_DIR.
type fakeHarness struct {
	calls  int
	env    []string
	script string
	write  string // the CLI version to stamp a result with; "" writes none
	src    string // a 0600 copy of a testdata result
	code   int
}

func (h *fakeHarness) run(_ context.Context, script string, env []string) (int, error) {
	h.calls++
	h.env, h.script = env, script
	if h.write != "" {
		out := ""
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "SWARMERY_PROBES_DIR="); ok {
				out = v
			}
		}
		r, err := channelprobe.Load(h.src)
		if err != nil {
			return -1, err
		}
		r.CLIVersion = h.write
		if _, err := channelprobe.Save(out, r); err != nil {
			return -1, err
		}
	}
	return h.code, nil
}

func useHarness(t *testing.T, h *fakeHarness) {
	t.Helper()
	h.src = copyFixture(t, "result-2.1.280.json")
	orig := runHarness
	t.Cleanup(func() { runHarness = orig })
	runHarness = h.run
}

// The probe child carries 0 store NAMES, a temp CLAUDE_CONFIG_DIR and the
// resolved CLI; its verdict is saved at 0600 under the version's name.
func TestProbeChildEnvAndSave(t *testing.T) {
	f := newFixture(t)
	f.store(t, "estate", "PACK_SECRET_ONE=zzq-v1\nPACK_SECRET_TWO=zzq-v2\n")
	t.Setenv("PACK_SECRET_ONE", "zzq-v1")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.home, ".claude-work"))
	link := versionedCLI(t, "2.1.292")
	script := filepath.Join(t.TempDir(), "probe.sh")
	mustWrite(t, script, "#!/bin/sh\n", 0o755)
	t.Setenv(ProbeScriptEnv, script)
	h := &fakeHarness{write: "2.1.292"}
	useHarness(t, h)

	rep, err := Probe(context.Background(), Options{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if h.script != script {
		t.Errorf("script = %s, want the SWARMERY_PROBE_SCRIPT one", h.script)
	}
	names := map[string]string{}
	for _, kv := range h.env {
		n, v, _ := strings.Cut(kv, "=")
		names[n] = v
	}
	for _, n := range []string{"PACK_SECRET_ONE", "PACK_SECRET_TWO"} {
		if _, ok := names[n]; ok {
			t.Errorf("the child carries store name %s", n)
		}
	}
	if cfg := names["CLAUDE_CONFIG_DIR"]; cfg == "" || strings.HasPrefix(cfg, f.home) {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want a fresh temp dir", cfg)
	}
	if names["SWARMERY_CLAUDE_BIN"] != link {
		t.Errorf("SWARMERY_CLAUDE_BIN = %q, want %q", names["SWARMERY_CLAUDE_BIN"], link)
	}
	saved := filepath.Join(channelprobe.Path(), "2.1.292.json")
	fi, err := os.Stat(saved)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved verdict = %v %v, want 0600", fi, err)
	}
	if countFindings(rep, "probe-stale", "")+countFindings(rep, "probe-drift", "") != 0 {
		t.Errorf("the report after --probe: %+v", rep.Findings)
	}
}

func TestProbeFailures(t *testing.T) {
	newFixture(t)
	versionedCLI(t, "2.1.293")
	h := &fakeHarness{code: 1}
	useHarness(t, h)
	// Neither SWARMERY_PROBE_SCRIPT nor an embedded harness (CI builds carry
	// none; a local build may) — with none, the error names both sources.
	if !channelprobe.HasHarness() {
		_, err := Probe(context.Background(), Options{Path: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), ProbeScriptEnv) || !strings.Contains(err.Error(), "embeds none") {
			t.Errorf("err = %v, want both sources named", err)
		}
	}
	script := filepath.Join(t.TempDir(), "probe.sh")
	mustWrite(t, script, "#!/bin/sh\n", 0o755)
	t.Setenv(ProbeScriptEnv, script)
	if _, err := Probe(context.Background(), Options{Path: t.TempDir()}); err == nil {
		t.Error("a harness exiting 1 with no result did not fail")
	}
	h.code = 2
	if _, err := Probe(context.Background(), Options{Path: t.TempDir()}); err == nil {
		t.Error("a harness exiting 2 did not fail")
	}
	orig := runHarness
	runHarness = func(context.Context, string, []string) (int, error) { return -1, errors.New("boom") }
	if _, err := Probe(context.Background(), Options{Path: t.TempDir()}); err == nil {
		t.Error("a harness that could not start did not fail")
	}
	runHarness = orig
}

// Criterion 31: the ticker helper runs the harness once for a new V and zero
// times when <V>.json already exists.
func TestProbeOnlyOnVersionChange(t *testing.T) {
	newFixture(t)
	versionedCLI(t, "2.1.294")
	script := filepath.Join(t.TempDir(), "probe.sh")
	mustWrite(t, script, "#!/bin/sh\n", 0o755)
	t.Setenv(ProbeScriptEnv, script)
	h := &fakeHarness{write: "2.1.294"}
	useHarness(t, h)

	ran, err := ProbeIfNewVersion(context.Background())
	if err != nil || !ran || h.calls != 1 {
		t.Fatalf("new V: ran=%v err=%v calls=%d", ran, err, h.calls)
	}
	ran, err = ProbeIfNewVersion(context.Background())
	if err != nil || ran || h.calls != 1 {
		t.Errorf("existing V: ran=%v err=%v calls=%d, want no run", ran, err, h.calls)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", filepath.Join(t.TempDir(), "claude"))
	if ran, _ := ProbeIfNewVersion(context.Background()); ran || h.calls != 1 {
		t.Error("an underivable V ran the probe")
	}
}

// The real runner: a harness script that writes nothing and exits 3 is a
// non-zero exit code, not a Go error.
func TestRunHarnessExitCode(t *testing.T) {
	script := filepath.Join(t.TempDir(), "h.sh")
	mustWrite(t, script, "exit 3\n", 0o755)
	code, err := runHarness(context.Background(), script, os.Environ())
	if err != nil || code != 3 {
		t.Errorf("code=%d err=%v, want 3 and nil", code, err)
	}
	if code, _ := runHarness(context.Background(), filepath.Join(t.TempDir(), "zero.sh"), nil); code == 0 {
		t.Error("a missing script exited 0")
	}
}
