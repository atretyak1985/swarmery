package claudeflags_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/extract"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/handoff"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retroanalysis"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/trajjudge"
)

// The five stdout-only engines build a FLAT argv by hand instead of going
// through runcore.Args, and each of them used to interpolate the resolved
// effort into a fixed positional slot: `…, "--effort", effort, …`.
//
// That is exactly where claudeflags.OmitEffort breaks. "off" resolves to "" —
// the documented escape hatch meaning "pass no --effort at all" — and a fixed
// slot turns "" into the literal argument pair `--effort ""`. The CLI rejects an
// empty flag value, so the knob an operator reaches for to reproduce pre-pinning
// behaviour did not restore the old shape: it killed the engine, and killed it
// only for the operator who used the hatch, which is the population least likely
// to be believed about it.
//
// So this test asserts the ARGV of a real spawn (a `claude` stub on
// SWARMERY_CLAUDE_BIN that dumps "$@"), not a log line and not the resolver in
// isolation: the resolver was already correct, it was the five argv builders
// that were not.
type effortSite struct {
	name string
	env  string // this site's SWARMERY_<ENGINE>_EFFORT knob
	def  string // the site's own DefaultEffort, for the fallback assertions
	// run spawns the engine once with override as its runner struct's Effort
	// field ("" = field unset, the production shape).
	run func(ctx context.Context, override string) (string, error)
}

func effortSites() []effortSite {
	return []effortSite{
		{"improve", "SWARMERY_IMPROVE_EFFORT", improve.DefaultEffort,
			func(ctx context.Context, o string) (string, error) {
				return improve.ClaudeRunner{Effort: o}.Run(ctx, "p")
			}},
		{"extract", "SWARMERY_EXTRACT_EFFORT", extract.DefaultEffort,
			func(ctx context.Context, o string) (string, error) {
				return extract.ClaudeRunner{Effort: o}.Run(ctx, "p")
			}},
		{"handoff", "SWARMERY_HANDOFF_EFFORT", handoff.DefaultEffort,
			func(ctx context.Context, o string) (string, error) {
				return handoff.ClaudeRunner{Effort: o}.Run(ctx, "p")
			}},
		{"retroanalysis", "SWARMERY_RETROANALYSIS_EFFORT", retroanalysis.DefaultEffort,
			func(ctx context.Context, o string) (string, error) {
				return retroanalysis.ClaudeRunner{Effort: o}.Run(ctx, "p")
			}},
		{"trajjudge", "SWARMERY_TRAJJUDGE_EFFORT", trajjudge.DefaultEffort,
			func(ctx context.Context, o string) (string, error) {
				return trajjudge.ClaudeRunner{Effort: o}.Run(ctx, "p")
			}},
		{"lessons", "SWARMERY_LESSON_EFFORT", lessons.DefaultEffort,
			func(ctx context.Context, o string) (string, error) {
				return lessons.ClaudeRunner{Effort: o}.Run(ctx, "p")
			}},
	}
}

// stubArgv installs a `claude` stub that records its argv and returns the
// recorded arguments of the next spawn. SWARMERY_CLAUDE_BIN is claudebin's
// first resolution rung, so this works without touching PATH.
func stubArgv(t *testing.T) func(t *testing.T, run func(context.Context, string) (string, error), override string) []string {
	t.Helper()
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n: > \"" + argvFile + "\"\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + argvFile + "\"; done\ncat > /dev/null\necho ok\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", bin)
	return func(t *testing.T, run func(context.Context, string) (string, error), override string) []string {
		t.Helper()
		if err := os.Remove(argvFile); err != nil && !os.IsNotExist(err) {
			t.Fatalf("reset argv file: %v", err)
		}
		if _, err := run(context.Background(), override); err != nil {
			t.Fatalf("spawn: %v", err)
		}
		raw, err := os.ReadFile(argvFile)
		if err != nil {
			t.Fatalf("read argv: %v", err)
		}
		return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
}

// effortValue returns the argument after --effort and how many --effort tokens
// the argv carries.
func effortValue(argv []string) (value string, count int) {
	for i, a := range argv {
		if a != "--effort" {
			continue
		}
		count++
		if i+1 < len(argv) {
			value = argv[i+1]
		}
	}
	return value, count
}

// TestFlatArgvSitesHonourTheOmitHatch is the regression for the defect: with the
// site's knob at "off" the argv must carry NO --effort token at all, and with a
// real value it must carry exactly one `--effort <value>` pair. An `--effort ""`
// pair — the old behaviour — fails both arms: the first because the token is
// present, the second because a count of one with an empty value is not a flag
// the CLI accepts.
func TestFlatArgvSitesHonourTheOmitHatch(t *testing.T) {
	for _, site := range effortSites() {
		t.Run(site.name, func(t *testing.T) {
			spawn := stubArgv(t)
			t.Setenv(claudeflags.EffortEnv, "")

			t.Setenv(site.env, claudeflags.OmitEffort)
			argv := spawn(t, site.run, "")
			if _, n := effortValue(argv); n != 0 {
				t.Errorf("%s=%q spawned %q — the omit hatch must drop the flag, not pass an empty value",
					site.env, claudeflags.OmitEffort, argv)
			}

			t.Setenv(site.env, "max")
			argv = spawn(t, site.run, "")
			if v, n := effortValue(argv); n != 1 || v != "max" {
				t.Errorf("%s=max spawned %q, want exactly one `--effort max`", site.env, argv)
			}
		})
	}
}

// TestFlatArgvSitesValidateTheExplicitOverride: the runner structs' Effort field
// is the rung ABOVE both env knobs and it used to reach argv unvalidated, so
// Effort:"off" spawned `--effort ""` and Effort:"bogus" handed the CLI a value
// it rejects. Both now go through the same NormalizeEffort the env path uses:
// "off" omits, and an unusable value degrades to the site's resolved default
// rather than killing the run.
func TestFlatArgvSitesValidateTheExplicitOverride(t *testing.T) {
	for _, site := range effortSites() {
		t.Run(site.name, func(t *testing.T) {
			spawn := stubArgv(t)
			t.Setenv(claudeflags.EffortEnv, "")
			t.Setenv(site.env, "")

			if _, n := effortValue(spawn(t, site.run, claudeflags.OmitEffort)); n != 0 {
				t.Errorf("Effort:%q must omit --effort entirely", claudeflags.OmitEffort)
			}

			argv := spawn(t, site.run, "low")
			if v, n := effortValue(argv); n != 1 || v != "low" {
				t.Errorf("Effort:\"low\" spawned %q, want exactly one `--effort low`", argv)
			}

			argv = spawn(t, site.run, "bogus")
			v, n := effortValue(argv)
			if n != 1 || v != site.def {
				t.Errorf("Effort:\"bogus\" spawned %q, want the site default %q — never the typo itself, never an empty value",
					argv, site.def)
			}
		})
	}
}
