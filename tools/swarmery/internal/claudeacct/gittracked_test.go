package claudeacct

// Tests for Lock 1 — the provenance gate in gittracked.go.
//
// Two halves, deliberately separated:
//
//   - REAL git fixtures for the verdict table, because the whole point of the
//     probe is what git actually says about a real index, and a stubbed git
//     would have shipped the prototype's mount-boundary bug green;
//   - a PURE table over classifyGitProbe for the wordings a fixture cannot
//     arrange on a laptop — a filesystem boundary, a foreign-owned repository,
//     a missing git.
//
// No assertion in this file prints a store value. The one store these tests seed
// holds a literal non-secret, and every check on it is a COUNT or an absence.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── fixture helpers ──────────────────────────────────────────────────────────

// runGit runs one git command for FIXTURE BUILDING.
//
// Its environment is gitProbeEnv's, plus a neutralised global and system config.
// Both matter: without the scrub a hostile-env subtest would poison the very
// fixtures it then probes, and without the config isolation the operator's own
// core.excludesfile decides whether a fixture is tracked (on this machine it
// ignores .claude/settings.local.json, which silently turns every "committed"
// fixture into an untracked one — hence the -f on every add below as well).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{
		"-c", "user.name=swarmery test",
		"-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false",
		"-C", dir,
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(gitProbeEnv(os.Environ()),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// newRepo is a fresh work tree with an initialised repository.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", ".")
	return dir
}

// writeBinding writes <project>/.claude/settings.local.json binding `key`, plus
// any extra top-level keys, and returns the file's path.
func writeBinding(t *testing.T, project, key string, extra ...string) string {
	t.Helper()
	path := bindingPath(project)
	mkdirs(t, filepath.Dir(path))
	body := fmt.Sprintf(`{"%s":{"%s":%q}%s}`+"\n",
		bindingNamespace, bindingField, key, strings.Join(extra, ""))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeAt writes a file at an arbitrary path inside a fixture.
func writeAt(t *testing.T, path, body string) {
	t.Helper()
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// markerHook returns the path of an executable that CREATES marker when run,
// and the marker path. It stands in for anything a repository could make git
// spawn — an fsmonitor, a hook. The marker's absence is the assertion.
func markerHook(t *testing.T) (hook, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "fired")
	hook = filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n: > "+marker+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return hook, marker
}

func assertNoMarker(t *testing.T, marker, what string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("%s: the repository's %s ran — the probe executed repository-controlled code", what, "core.fsmonitor hook")
	}
}

// countingProbe wraps the real runner with a counter, so a test can assert that
// git was NOT started, or was started exactly once.
func countingProbe(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := runGitProbe
	runGitProbe = func(dir, name string) gitProbeResult {
		n++
		return prev(dir, name)
	}
	t.Cleanup(func() { runGitProbe = prev })
	return &n
}

// stubProbe replaces the runner with a fixed answer.
func stubProbe(t *testing.T, res gitProbeResult) {
	t.Helper()
	prev := runGitProbe
	runGitProbe = func(string, string) gitProbeResult { return res }
	t.Cleanup(func() { runGitProbe = prev })
}

func resetWarnOnce(t *testing.T) {
	t.Helper()
	distrustedWarned = &sync.Map{}
	t.Cleanup(func() { distrustedWarned = &sync.Map{} })
}

func resetDisplayCache(t *testing.T) {
	t.Helper()
	displayCacheMu.Lock()
	displayCache = map[string]displayEntry{}
	displayCacheMu.Unlock()
	t.Cleanup(func() {
		displayCacheMu.Lock()
		displayCache = map[string]displayEntry{}
		displayCacheMu.Unlock()
	})
}

// fakeClock pins displayNow and returns a knob that advances it.
func fakeClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Now()
	prev := displayNow
	displayNow = func() time.Time { return now }
	t.Cleanup(func() { displayNow = prev })
	return func(d time.Duration) { now = now.Add(d) }
}

// ── the verdict table, over real git ─────────────────────────────────────────

// THE table. Each row builds a real fixture on disk and asserts the verdict the
// gate will act on.
func TestGitTrackStateTable(t *testing.T) {
	// committed: the leak's own shape — the file arrived with a clone or a pull.
	t.Run("committed", func(t *testing.T) {
		repo := newRepo(t)
		path := writeBinding(t, repo, "work")
		runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
		runGit(t, repo, "commit", "-qm", "commit the binding")
		wantVerdict(t, path, trackTracked)
	})

	// staged but not committed is already shared intent, and is already visible
	// to anyone the index reaches. Tracked.
	t.Run("staged", func(t *testing.T) {
		repo := newRepo(t)
		path := writeBinding(t, repo, "work")
		runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
		wantVerdict(t, path, trackTracked)
	})

	// gitignored is the CORRECT shape and must keep working exactly as before:
	// a gitignored file cannot have arrived from someone else's commit.
	t.Run("gitignored", func(t *testing.T) {
		repo := newRepo(t)
		path := writeBinding(t, repo, "work")
		writeAt(t, filepath.Join(repo, ".gitignore"), ".claude/\n")
		runGit(t, repo, "add", "-f", "--", ".gitignore")
		runGit(t, repo, "commit", "-qm", "ignore the settings dir")
		wantVerdict(t, path, trackUntracked)
	})

	// Not a repository at all: honoured. This is also the extracted-tarball
	// residual Lock 2 closes later — recorded here as the verdict it really is,
	// not as something this phase pretends to have fixed.
	t.Run("norepo", func(t *testing.T) {
		path := writeBinding(t, t.TempDir(), "work")
		wantVerdict(t, path, trackNotRepo)
	})

	// The shared-overlay shape: .claude is a SYMLINK into another repository,
	// where the file is gitignored. Claude Code's own rule of thumb ("a
	// symlinked .claude is shared, therefore committed") is wrong here and must
	// not be copied: the question is what THAT repository's index says, and it
	// says untracked. Honoured.
	t.Run("symlink-into-another-repo-gitignored", func(t *testing.T) {
		other := newRepo(t)
		shared := filepath.Join(other, "shared", ".claude")
		writeAt(t, filepath.Join(shared, "settings.local.json"),
			fmt.Sprintf(`{"%s":{"%s":"work"}}`+"\n", bindingNamespace, bindingField))
		writeAt(t, filepath.Join(other, ".gitignore"), "shared/.claude/\n")
		runGit(t, other, "add", "-f", "--", ".gitignore")
		runGit(t, other, "commit", "-qm", "ignore the shared settings dir")

		proj := t.TempDir()
		if err := os.Symlink(shared, filepath.Join(proj, ".claude")); err != nil {
			t.Fatal(err)
		}
		wantVerdict(t, bindingPath(proj), trackUntracked)
	})

	// The same indirection, the other way round: .claude points at an in-repo
	// directory whose file IS committed. The link must not launder it.
	t.Run("symlink-to-in-repo-committed", func(t *testing.T) {
		repo := newRepo(t)
		writeAt(t, filepath.Join(repo, "config", "settings.local.json"),
			fmt.Sprintf(`{"%s":{"%s":"work"}}`+"\n", bindingNamespace, bindingField))
		runGit(t, repo, "add", "-f", "--", "config/settings.local.json")
		runGit(t, repo, "commit", "-qm", "commit the shared config")
		if err := os.Symlink(filepath.Join(repo, "config"), filepath.Join(repo, ".claude")); err != nil {
			t.Fatal(err)
		}
		wantVerdict(t, bindingPath(repo), trackTracked)
	})

	// A BARE repository planted where the settings directory belongs. git would
	// otherwise adopt it as the repository to answer from — which is a foreign
	// config, i.e. a foreign core.fsmonitor. safe.bareRepository=explicit
	// refuses, the refusal is unclassifiable, and unclassifiable means ignored.
	t.Run("decoy-bare-repo", func(t *testing.T) {
		repo := newRepo(t)
		runGit(t, repo, "init", "-q", "--bare", ".claude")
		path := writeBinding(t, repo, "work")
		wantVerdict(t, path, trackUnknown)
	})

	// A repository that configures core.fsmonitor as an executable. The verdict
	// must still be correct AND the executable must never run. Note the config
	// is set LAST: the fixture's own add/commit would otherwise fire it.
	t.Run("fsmonitor-never-fires", func(t *testing.T) {
		hook, marker := markerHook(t)
		repo := newRepo(t)
		path := writeBinding(t, repo, "work")
		runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
		runGit(t, repo, "commit", "-qm", "commit the binding")
		runGit(t, repo, "config", "core.fsmonitor", hook)
		wantVerdict(t, path, trackTracked)
		assertNoMarker(t, marker, "fsmonitor-configured repository")
	})

	// The gate is about PROVENANCE, not about which key was written: a committed
	// binding to the default account is tracked too. (Ignoring it is harmless
	// here — it resolves to the same account — but the verdict must not depend
	// on the value, or the rule would be arguable.)
	t.Run("committed-default-key", func(t *testing.T) {
		repo := newRepo(t)
		path := writeBinding(t, repo, "default")
		runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
		runGit(t, repo, "commit", "-qm", "commit a default binding")
		wantVerdict(t, path, trackTracked)
	})

	// A hostile CALLER environment: every variable below points git at a
	// different repository, a different index, or a marker-writing fsmonitor.
	// The verdicts must be unchanged and the marker must never appear.
	t.Run("hostile-caller-env", func(t *testing.T) {
		hook, marker := markerHook(t)

		// Fixtures FIRST — runGit scrubs the same variables, but building them
		// before the env is poisoned keeps that independent of runGit's care.
		tracked := newRepo(t)
		trackedPath := writeBinding(t, tracked, "work")
		runGit(t, tracked, "add", "-f", "--", ".claude/settings.local.json")
		runGit(t, tracked, "commit", "-qm", "commit the binding")
		norepoPath := writeBinding(t, t.TempDir(), "work")

		elsewhere := newRepo(t)
		t.Setenv("GIT_DIR", filepath.Join(elsewhere, ".git"))
		t.Setenv("GIT_INDEX_FILE", filepath.Join(elsewhere, ".git", "index"))
		t.Setenv("GIT_LITERAL_PATHSPECS", "1")
		t.Setenv("GIT_DISCOVERY_ACROSS_FILESYSTEM", "1")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
		t.Setenv("GIT_CONFIG_VALUE_0", hook)

		wantVerdict(t, trackedPath, trackTracked)
		wantVerdict(t, norepoPath, trackNotRepo)
		assertNoMarker(t, marker, "hostile GIT_CONFIG_KEY_0 environment")
	})
}

func wantVerdict(t *testing.T, path string, want trackVerdict) {
	t.Helper()
	if got := probeGitTracked(path); got != want {
		t.Fatalf("probeGitTracked(%s) = %s, want %s", path, got, want)
	}
}

// ── the pure classifier ──────────────────────────────────────────────────────

// Every wording git can hand back, including the two a laptop cannot stage: the
// filesystem-boundary refusal (needs a separate mount) and a missing git.
func TestClassifyGitProbe(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		stderr   string
		runErr   error
		want     trackVerdict
	}{
		{"exit-0-is-tracked", 0, "", nil, trackTracked},
		{
			"pathspec-miss-is-untracked", 1,
			"error: pathspec ':(icase,literal)settings.local.json' did not match any file(s) known to git\n" +
				"Did you forget to 'git add'?\n",
			nil, trackUntracked,
		},
		{
			"walked-to-root-is-not-a-repo", 128,
			"fatal: not a git repository (or any of the parent directories): .git\n",
			nil, trackNotRepo,
		},
		{
			// THE prototype's bug. Because the probe scrubs
			// GIT_DISCOVERY_ACROSS_FILESYSTEM, git stops at a filesystem
			// boundary and says this instead — on an external disk, a separate
			// /home, a tmpfs /tmp. Matching only the wording above would have
			// classified every such binding as unknown and silently ignored it.
			"mount-boundary-is-not-a-repo", 128,
			"fatal: not a git repository (or any parent up to mount point /Volumes/x)\n" +
				"Stopping at filesystem boundary (GIT_DISCOVERY_ACROSS_FILESYSTEM not set).\n",
			nil, trackNotRepo,
		},
		{
			"dubious-ownership-is-unknown", 128,
			"fatal: detected dubious ownership in repository at '/elsewhere/repo'\n",
			nil, trackUnknown,
		},
		{
			"refused-bare-repo-is-unknown", 128,
			"fatal: cannot use bare repository '/proj/.claude' (safe.bareRepository is 'explicit')\n",
			nil, trackUnknown,
		},
		{"exit-1-without-the-text-is-unknown", 1, "", nil, trackUnknown},
		{"exit-1-with-other-text-is-unknown", 1, "fatal: index file corrupt\n", nil, trackUnknown},
		{"usage-error-is-unknown", 129, "usage: git ls-files [<options>] [<file>...]\n", nil, trackUnknown},
		{"timeout-is-unknown", -1, "", context.DeadlineExceeded, trackUnknown},
		{
			"git-missing-is-unknown", -1, "",
			errors.New(`exec: "git": executable file not found in $PATH`),
			trackUnknown,
		},
		{
			// A run error outranks a success status: a killed git that already
			// printed a match is not an answer.
			"run-error-outranks-exit-0", 0, "", context.DeadlineExceeded, trackUnknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyGitProbe(c.exitCode, c.stderr, c.runErr); got != c.want {
				t.Errorf("classifyGitProbe(%d, %q, %v) = %s, want %s",
					c.exitCode, c.stderr, c.runErr, got, c.want)
			}
		})
	}
}

// The verdict names appear in every failure message in this file and in nothing
// else, so a silent rename would make a future failure unreadable. Four distinct
// names, and no verdict rendering as the zero value's name by accident.
func TestTrackVerdictNames(t *testing.T) {
	want := map[trackVerdict]string{
		trackUnknown:   "unknown",
		trackUntracked: "untracked",
		trackNotRepo:   "not-a-repo",
		trackTracked:   "tracked",
	}
	seen := map[string]bool{}
	for v, name := range want {
		if got := v.String(); got != name {
			t.Errorf("trackVerdict(%d).String() = %q, want %q", int(v), got, name)
		}
		if seen[name] {
			t.Errorf("two verdicts render as %q", name)
		}
		seen[name] = true
	}
}

// ── the pre-check ────────────────────────────────────────────────────────────

// No .git above the file → not-a-repo, decided in Go. This is the guard that
// keeps a broken git installation (a stale Command Line Tools shim, say) from
// turning every unbound project on the machine into an ignored binding: git is
// never started, so it cannot fail.
func TestNoGitAncestorRunsNoGit(t *testing.T) {
	path := writeBinding(t, t.TempDir(), "work")
	if hasGitAncestor(filepath.Dir(path)) {
		t.Skipf("precondition: %s has a .git ancestor, so this machine cannot stage the case", path)
	}
	n := countingProbe(t)
	wantVerdict(t, path, trackNotRepo)
	if *n != 0 {
		t.Fatalf("git was started %d time(s) for a path with no .git ancestor; want 0", *n)
	}
}

// ── the tripwire: nothing tracked may reach a spawn ──────────────────────────

// seedProbeStore writes a store for `account` holding exactly one name. The
// value is a literal non-secret and is never asserted on — only counted.
func seedProbeStore(t *testing.T, account string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(secretsDirEnv, dir)
	path := filepath.Join(dir, account+".env")
	if err := os.WriteFile(path, []byte(probeStoreName+"=not-a-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// probeStoreName is the one variable the seeded store defines. Tests assert its
// PRESENCE or ABSENCE in an env array, never its value.
const probeStoreName = "PROBE_STORE_NAME"

func countPrefix(env []string, prefix string) int {
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			n++
		}
	}
	return n
}

// THE tripwire. A committed binding must reach neither the config dir nor the
// account's store — and untracking it must restore both, so the gate is a
// provenance check and not a permanent lockout.
func TestTrackedBindingNeverReachesTheSpawnEnv(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)
	const account = "work"
	seedProbeStore(t, account)

	repo := newRepo(t)
	writeBinding(t, repo, account)
	runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
	runGit(t, repo, "commit", "-qm", "commit the binding")

	if got := Binding(repo); got != "" {
		t.Fatalf("Binding on a COMMITTED binding = %q, want \"\" — the leak is open", got)
	}
	env := SpawnEnvFor([]string{"PATH=/usr/bin"}, repo)
	if n := countPrefix(env, probeStoreName+"="); n != 0 {
		t.Errorf("the spawn environment carries %d entry/entries from the account's store; want 0", n)
	}
	if n := countPrefix(env, configDirEnv+"="); n != 0 {
		t.Errorf("the spawn environment carries %d %s entry/entries; want 0", n, configDirEnv)
	}
	if got := len(SecretEnvFor(repo)); got != 0 {
		t.Errorf("SecretEnvFor length = %d, want 0 — a tracked binding unlocked the store", got)
	}

	// Untrack it — the documented fix — and everything comes back.
	runGit(t, repo, "rm", "--cached", "-q", "--", ".claude/settings.local.json")
	if got := Binding(repo); got != account {
		t.Fatalf("after `git rm --cached`, Binding = %q, want %q", got, account)
	}
	if got := len(SecretEnvFor(repo)); got != 1 {
		t.Errorf("after `git rm --cached`, SecretEnvFor length = %d, want 1", got)
	}
	if n := countPrefix(SpawnEnvFor([]string{"PATH=/usr/bin"}, repo), configDirEnv+"="); n != 1 {
		t.Errorf("after `git rm --cached`, %s entries = %d, want exactly 1", configDirEnv, n)
	}
}

// A git that cannot answer is not a licence to trust the file: dubious
// ownership, a corrupt index, a git that is not there. Fail CLOSED.
func TestBindingFailsClosedWhenGitCannotAnswer(t *testing.T) {
	for _, c := range []struct {
		name string
		res  gitProbeResult
	}{
		{"dubious-ownership", gitProbeResult{exitCode: 128, stderr: "fatal: detected dubious ownership in repository at '/x'\n"}},
		{"corrupt-index", gitProbeResult{exitCode: 1, stderr: "fatal: index file corrupt\n"}},
		{"git-missing", gitProbeResult{exitCode: -1, err: errors.New(`exec: "git": executable file not found in $PATH`)}},
		{"timeout", gitProbeResult{exitCode: -1, err: context.DeadlineExceeded}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeHome(t)
			resetWarnOnce(t)
			const account = "work"
			seedProbeStore(t, account)

			// A repo, so the Go pre-check passes and the (stubbed) runner is the
			// thing deciding.
			repo := newRepo(t)
			writeBinding(t, repo, account)
			stubProbe(t, c.res)

			if got := Binding(repo); got != "" {
				t.Errorf("Binding = %q, want \"\" — an unclassifiable binding was trusted", got)
			}
			if got := len(SecretEnvFor(repo)); got != 0 {
				t.Errorf("SecretEnvFor length = %d, want 0", got)
			}
		})
	}
}

// ── path shapes ──────────────────────────────────────────────────────────────

// Binding(".") and Binding("") hand the probe a RELATIVE path. Without the Abs
// in resolveProbePath the probe's -C would be "." — the daemon's own working
// directory — and the verdict would describe an unrelated repository.
func TestBindingRelativePath(t *testing.T) {
	t.Run("tracked", func(t *testing.T) {
		fakeHome(t)
		resetWarnOnce(t)
		repo := newRepo(t)
		writeBinding(t, repo, "work")
		runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
		runGit(t, repo, "commit", "-qm", "commit the binding")

		t.Chdir(repo)
		if abs, rel := Binding(repo), Binding("."); abs != rel {
			t.Fatalf("Binding(abs) = %q but Binding(\".\") = %q", abs, rel)
		}
		if got := Binding("."); got != "" {
			t.Errorf("Binding(\".\") on a tracked binding = %q, want \"\"", got)
		}
		if got := Binding(""); got != "" {
			t.Errorf("Binding(\"\") on a tracked binding = %q, want \"\"", got)
		}
	})

	t.Run("untracked", func(t *testing.T) {
		fakeHome(t)
		repo := newRepo(t)
		writeBinding(t, repo, "work")

		t.Chdir(repo)
		if abs, rel := Binding(repo), Binding("."); abs != rel {
			t.Fatalf("Binding(abs) = %q but Binding(\".\") = %q", abs, rel)
		}
		if got := Binding("."); got != "work" {
			t.Errorf("Binding(\".\") on an untracked binding = %q, want %q", got, "work")
		}
	})
}

// ── the warning ──────────────────────────────────────────────────────────────

// One line per path per process. Binding() runs on every spawn and on several
// dashboard endpoints; a line per CALL would bury the operator's log, and a
// warning nobody reads is not a warning.
func TestWarnOncePerPath(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)
	const planted = "PLANTED-VALUE-MUST-NOT-REACH-THE-LOG"

	newTrackedRepo := func(extra ...string) string {
		repo := newRepo(t)
		writeBinding(t, repo, "work", extra...)
		runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
		runGit(t, repo, "commit", "-qm", "commit the binding")
		return repo
	}
	repoA := newTrackedRepo(`,"plantedKey":"` + planted + `"`)
	repoB := newTrackedRepo()

	out := captureLog(t, func() {
		Binding(repoA)
		Binding(repoA)
		Binding(repoA)
	})
	if n := strings.Count(out, "IGNORING binding"); n != 1 {
		t.Fatalf("three Binding() calls on one path logged %d warnings; want exactly 1\n%s", n, out)
	}
	if !strings.Contains(out, bindingPath(repoA)) {
		t.Errorf("the warning does not name the path %s:\n%s", bindingPath(repoA), out)
	}
	if !strings.Contains(out, "git rm --cached") {
		t.Errorf("the warning does not carry the fix:\n%s", out)
	}
	if strings.Contains(out, planted) {
		t.Fatalf("the warning leaked a value from the binding file:\n%s", out)
	}

	out2 := captureLog(t, func() { Binding(repoB) })
	if n := strings.Count(out+out2, "IGNORING binding"); n != 2 {
		t.Errorf("a second distrusted path brought the total to %d warnings; want 2", n)
	}
}

// ── the display-only cache ───────────────────────────────────────────────────

// The cache is a DASHBOARD affordance, never a security decision: Binding()
// probes fresh every single time, BindingForDisplay reuses a verdict until the
// file changes or the TTL runs out.
func TestDisplayCacheIsDisplayOnly(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)
	resetDisplayCache(t)
	advance := fakeClock(t)

	repo := newRepo(t) // untracked binding: both functions answer "work"
	path := writeBinding(t, repo, "work")
	n := countingProbe(t)

	base := *n
	Binding(repo)
	Binding(repo)
	if got := *n - base; got != 2 {
		t.Fatalf("two Binding() calls started git %d time(s); want 2 — a spawn path must never reuse a verdict", got)
	}

	base = *n
	if a, b := BindingForDisplay(repo), BindingForDisplay(repo); a != "work" || b != "work" {
		t.Fatalf("BindingForDisplay = %q/%q, want %q", a, b, "work")
	}
	if got := *n - base; got != 1 {
		t.Fatalf("two BindingForDisplay() calls started git %d time(s); want 1", got)
	}

	// A file change invalidates the entry (size+mtime are part of the key).
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	base = *n
	BindingForDisplay(repo)
	if got := *n - base; got != 1 {
		t.Fatalf("after an mtime change, BindingForDisplay started git %d time(s); want 1", got)
	}

	// …and so does the TTL, which is the ONLY thing that catches `git add` /
	// `git rm --cached`: those rewrite the index and leave the file untouched.
	base = *n
	BindingForDisplay(repo)
	if got := *n - base; got != 0 {
		t.Fatalf("within the TTL, BindingForDisplay started git %d time(s); want 0", got)
	}
	advance(displayCacheTTL + time.Second)
	base = *n
	BindingForDisplay(repo)
	if got := *n - base; got != 1 {
		t.Fatalf("after the TTL, BindingForDisplay started git %d time(s); want 1", got)
	}
}

// BindingForDisplay re-parses the settings file rather than delegating to
// Binding (delegating would probe twice and defeat the cache). This pins the two
// answers together so that duplication cannot drift.
func TestBindingForDisplayAgreesWithBinding(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)

	cases := map[string]func(t *testing.T) string{
		"tracked": func(t *testing.T) string {
			repo := newRepo(t)
			writeBinding(t, repo, "work")
			runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
			runGit(t, repo, "commit", "-qm", "commit the binding")
			return repo
		},
		"untracked-in-repo": func(t *testing.T) string {
			repo := newRepo(t)
			writeBinding(t, repo, "work")
			return repo
		},
		"not-a-repo": func(t *testing.T) string {
			dir := t.TempDir()
			writeBinding(t, dir, "work")
			return dir
		},
		"missing-file": func(t *testing.T) string { return t.TempDir() },
		"broken-json": func(t *testing.T) string {
			dir := t.TempDir()
			writeAt(t, bindingPath(dir), "{not json")
			return dir
		},
		"invalid-key": func(t *testing.T) string {
			dir := t.TempDir()
			writeBinding(t, dir, "../escape")
			return dir
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			resetDisplayCache(t)
			project := build(t)
			if a, b := Binding(project), BindingForDisplay(project); a != b {
				t.Errorf("Binding = %q but BindingForDisplay = %q", a, b)
			}
		})
	}
}

// ── the environment scrub ────────────────────────────────────────────────────

func TestGitProbeEnvDropsEveryRedirection(t *testing.T) {
	dropped := []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
		"GIT_NAMESPACE", "GIT_IMPLICIT_WORK_TREE", "GIT_CONFIG",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
		"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_CONFIG_KEY_17", "GIT_CONFIG_VALUE_17",
		"GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS",
		"GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS",
	}
	base := []string{"PATH=/usr/bin", "HOME=/home/x"}
	for _, name := range dropped {
		base = append(base, name+"=poison")
	}
	got := gitProbeEnv(base)

	for _, name := range dropped {
		if countPrefix(got, name+"=") != 0 {
			t.Errorf("%s survived the scrub — it can re-point the probe at another repository", name)
		}
	}
	for _, kept := range []string{"PATH=/usr/bin", "HOME=/home/x"} {
		if countPrefix(got, strings.SplitN(kept, "=", 2)[0]+"=") != 1 {
			t.Errorf("%s did not survive the scrub; the probe needs the ordinary environment", kept)
		}
	}
	for _, pinned := range []string{"LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"} {
		if countPrefix(got, pinned) != 1 {
			t.Errorf("the probe environment is missing %s", pinned)
		}
	}
}
