// Package systemspawn points a daemon-spawned headless `claude` at the System
// project: the ONE place that decides both the working directory such a run
// starts in and the Claude account it runs under.
//
// WHY BOTH IN ONE PLACE. The two are not independent decisions that merely
// happen to share a guard — they are the same decision seen twice. A headless
// run chdirs into ~/.swarmery so its transcript attributes to the deliberate
// "System" project (internal/ingest) instead of the launchd cwd "/"; and
// ~/.swarmery is itself a registered project, so that same directory is what
// an account binding resolves FROM. Before this package five runners
// (improve, retroanalysis, trajjudge, handoff, extract) each carried a private
// copy of the block and a private isDir, and the copies drifted: three of them
// were left behind when claudeacct.SpawnEnvFor became the single spawn-env
// composition, so the daemon's background engines silently ran under a
// different account than every other spawn site in this program.
//
// THE GUARD IS THE CONTRACT. cmd.Dir and cmd.Env — and the composed
// `--settings` splice into cmd.Args (internal/runsettings) — are set together
// or not at all. When ~/.swarmery does not exist there is no System project to attribute
// to AND no project to resolve an account for, so the spawn must stay
// byte-identical to one issued before either feature existed — setting Dir to
// a missing directory would fail the spawn with chdir ENOENT, and losing
// attribution beats not running at all. (The daemon owns ~/.swarmery, so in
// production the directory is always there; the guard is for tests and for a
// CLI invocation on a machine that never installed the daemon.)
package systemspawn

import (
	"os"
	"os/exec"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runsettings"
)

// Attach points cmd at the System project — cwd and account environment both —
// and is a no-op when that directory does not exist. Call it on a command that
// has not had Dir or Env set: Attach owns both fields when it acts, and touches
// neither when it does not.
//
// The account composition is claudeacct.SpawnEnvResolved over Resolve(dir) —
// what SpawnEnvFor computes, the composition every other swarmery spawn site uses, so a bound System project gets its CLAUDE_CONFIG_DIR
// and its per-account secret store exactly as a bound repo does, and an unbound
// one gets os.Environ() back unchanged (same backing array).
func Attach(cmd *exec.Cmd) {
	dir, ok := Dir()
	if !ok {
		return
	}
	cmd.Dir = dir
	// ONE resolution feeds the env and the settings composer, so D5's git probe
	// runs once per spawn. SpawnEnvResolved over Resolve(dir) is exactly what
	// SpawnEnvFor(os.Environ(), dir) computed for a non-empty dir.
	res := claudeacct.Resolve(dir)
	cmd.Env = claudeacct.SpawnEnvResolved(os.Environ(), res)
	// On the SAME fall-through path as Dir and Env — never on the guard's abort
	// path above: the admitted estate's composed settings, spliced right after
	// argv[0] because --settings is a root option. exec.Cmd.Args is read at
	// Start, so mutating it here is valid. No admitted estate over ~/.swarmery
	// ⇒ "" ⇒ Args untouched, byte-identical to before.
	if f := runsettings.Compose("systemspawn", res, runsettings.Inputs{}); f != "" && len(cmd.Args) > 0 {
		cmd.Args = append([]string{cmd.Args[0], "--settings", f}, cmd.Args[1:]...)
	}
}

// Dir is the System project's directory — the working directory a
// daemon-spawned utility run starts in — and whether it exists. It is Attach's
// own guard, exported for the ONE caller that wants the directory and nothing
// else: the account pre-flight ping (internal/runcore), whose transcript must
// attribute to the System project like every other utility run's, but whose
// account environment and settings must be those of the run it vouches for,
// not the System project's. Every other spawn uses Attach.
//
// ok=false means there is no System project on this machine; the caller then
// leaves its working directory alone, exactly as Attach does.
func Dir() (dir string, ok bool) {
	dir = ingest.SystemDir()
	if dir == "" || !isDir(dir) {
		return "", false
	}
	return dir, true
}

// isDir reports whether path exists and is a directory. A file at that path is
// not a System home, and chdir into it would fail the spawn with ENOTDIR.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
