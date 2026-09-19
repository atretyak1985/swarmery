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
// THE GUARD IS THE CONTRACT. cmd.Dir and cmd.Env are set together or not at
// all. When ~/.swarmery does not exist there is no System project to attribute
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
)

// Attach points cmd at the System project — cwd and account environment both —
// and is a no-op when that directory does not exist. Call it on a command that
// has not had Dir or Env set: Attach owns both fields when it acts, and touches
// neither when it does not.
//
// The account composition is claudeacct.SpawnEnvFor, the same call every other
// swarmery spawn site uses, so a bound System project gets its CLAUDE_CONFIG_DIR
// and its per-account secret store exactly as a bound repo does, and an unbound
// one gets os.Environ() back unchanged (same backing array).
func Attach(cmd *exec.Cmd) {
	dir := ingest.SystemDir()
	if dir == "" || !isDir(dir) {
		return
	}
	cmd.Dir = dir
	cmd.Env = claudeacct.SpawnEnvFor(os.Environ(), dir)
}

// isDir reports whether path exists and is a directory. A file at that path is
// not a System home, and chdir into it would fail the spawn with ENOTDIR.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
