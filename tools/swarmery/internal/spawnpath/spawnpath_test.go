package spawnpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const launchdPATH = "/usr/bin:/bin:/usr/sbin:/sbin"

func dirsOf(set ...string) func(string) bool {
	m := map[string]bool{}
	for _, s := range set {
		m[s] = true
	}
	return func(p string) bool { return m[p] }
}

func noGlob(string) []string { return nil }

// The launchd case: the user's tool dirs that exist land ahead of the system
// PATH, missing ones are skipped, and the original entries keep their order.
func TestAugment_PrependsExistingToolDirsUnderLaunchd(t *testing.T) {
	home := "/Users/op"
	exists := dirsOf(
		filepath.Join(home, ".local/bin"),
		filepath.Join(home, ".bun/bin"),
		"/opt/homebrew/bin",
	)
	got := Augment(launchdPATH, home, "", exists, noGlob)
	want := strings.Join([]string{
		filepath.Join(home, ".local/bin"),
		filepath.Join(home, ".bun/bin"),
		"/opt/homebrew/bin",
		launchdPATH,
	}, ":")
	if got != want {
		t.Fatalf("Augment =\n  %s\nwant\n  %s", got, want)
	}
}

// An interactive shell already has everything: nothing is added and the string
// comes back byte-identical, so Apply stays silent there.
func TestAugment_NoOpWhenEverythingIsAlreadyOnPath(t *testing.T) {
	home := "/Users/op"
	path := filepath.Join(home, ".local/bin") + ":/opt/homebrew/bin:" + launchdPATH
	exists := dirsOf(filepath.Join(home, ".local/bin"), "/opt/homebrew/bin")
	if got := Augment(path, home, "", exists, noGlob); got != path {
		t.Fatalf("Augment changed a complete PATH:\n  %s\nwant\n  %s", got, path)
	}
}

// SWARMERY_SPAWN_PATH is the operator's word: verbatim, first, no existence check.
func TestAugment_ExtraDirsGoFirstVerbatim(t *testing.T) {
	got := Augment(launchdPATH, "/Users/op", "/custom/tools:/also/this", dirsOf(), noGlob)
	if !strings.HasPrefix(got, "/custom/tools:/also/this:") {
		t.Fatalf("extra dirs not first: %s", got)
	}
	// Duplicates of the existing PATH are never re-added.
	got = Augment(launchdPATH, "/Users/op", "/usr/bin", dirsOf(), noGlob)
	if got != launchdPATH {
		t.Fatalf("an extra dir already on PATH was duplicated: %s", got)
	}
}

// nvm keeps every installed node side by side; the newest one wins, compared
// numerically (v9 < v22), never lexically.
func TestAugment_PicksTheNewestNvmNode(t *testing.T) {
	home := "/Users/op"
	base := filepath.Join(home, ".nvm", "versions", "node")
	glob := func(pattern string) []string {
		return []string{
			filepath.Join(base, "v9.11.2"),
			filepath.Join(base, "v22.1.0"),
			filepath.Join(base, "v20.19.0"),
			filepath.Join(base, "junk"),
		}
	}
	newest := filepath.Join(base, "v22.1.0", "bin")
	got := Augment(launchdPATH, home, "", dirsOf(newest), glob)
	if !strings.HasPrefix(got, newest+":") {
		t.Fatalf("newest nvm node not chosen: %s", got)
	}
}

// No home: only the system dirs and the extra list can apply.
func TestAugment_EmptyHomeSkipsUserDirs(t *testing.T) {
	got := Augment(launchdPATH, "", "", dirsOf("/usr/local/bin"), noGlob)
	if got != "/usr/local/bin:"+launchdPATH {
		t.Fatalf("Augment = %s", got)
	}
}

// Apply really changes this process's PATH when a probed dir exists.
func TestApply_WidensTheProcessPath(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv(EnvExtra, "")
	t.Setenv("PATH", launchdPATH)

	before, after := Apply()
	if before != launchdPATH {
		t.Fatalf("before = %q", before)
	}
	if !strings.HasPrefix(after, filepath.Join(home, ".local", "bin")+":") {
		t.Fatalf("after = %q, want the existing ~/.local/bin first", after)
	}
	if os.Getenv("PATH") != after {
		t.Fatalf("process PATH = %q, want %q", os.Getenv("PATH"), after)
	}
}
