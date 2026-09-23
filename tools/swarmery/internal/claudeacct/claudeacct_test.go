package claudeacct

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestMain arms the $HOME seam with a FAILING stub, so a test that forgets to
// call fakeHome cannot silently fall through to the operator's real ~/.claude —
// it gets an error instead. This is what makes "no test reads the real $HOME" a
// property of the package rather than a promise in a review comment.
func TestMain(m *testing.M) {
	userHomeDir = func() (string, error) {
		return "", errors.New("claudeacct test: the real $HOME must never be read — call fakeHome(t)")
	}
	os.Exit(m.Run())
}

// fakeHome points discovery at a fresh temp dir for the duration of one test.
// The seam is a package var, so tests using it must not run in parallel.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = prev })
	return home
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// Discover reports every ~/.claude*/projects config dir, sorted, and skips the
// things that only look like one: a dir with no projects/ yet, a FILE named
// projects, and a non-.claude dir.
func TestDiscoverFindsEveryConfigDirSorted(t *testing.T) {
	home := fakeHome(t)
	mkdirs(t,
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude-nabu-org", "projects"),
		filepath.Join(home, ".claude-science", "projects"),
		filepath.Join(home, ".claude-empty"),       // config dir with no projects/ yet
		filepath.Join(home, ".config", "projects"), // not a .claude* dir
		filepath.Join(home, ".claude-file"),
	)
	// A FILE named projects under a .claude* dir must not be taken for an account.
	if err := os.WriteFile(filepath.Join(home, ".claude-file", "projects"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	want := []Account{
		{Key: "default", ConfigDir: filepath.Join(home, ".claude"), IsDefault: true},
		{Key: "nabu-org", ConfigDir: filepath.Join(home, ".claude-nabu-org")},
		{Key: "science", ConfigDir: filepath.Join(home, ".claude-science")},
	}
	if got := Discover(); !slices.Equal(got, want) {
		t.Errorf("Discover() = %+v, want %+v", got, want)
	}
}

// A machine with no Claude Code config dir at all yields no accounts — the
// caller decides what "no accounts" means, Discover does not invent a default.
func TestDiscoverOnAnEmptyHomeFindsNothing(t *testing.T) {
	fakeHome(t)
	if got := Discover(); len(got) != 0 {
		t.Errorf("Discover() = %+v, want empty", got)
	}
}

// Two dirs can key to ONE account (~/.claude-bak and ~/.claude.bak both yield
// "bak"). The first in sorted order wins, exactly as api.accountsFromRoots
// dedupes — two cards for one key would be two truths about one subscription.
func TestDiscoverDedupesKeysKeepingTheFirstDir(t *testing.T) {
	home := fakeHome(t)
	mkdirs(t,
		filepath.Join(home, ".claude-bak", "projects"),
		filepath.Join(home, ".claude.bak", "projects"),
	)
	want := []Account{{Key: "bak", ConfigDir: filepath.Join(home, ".claude-bak")}}
	if got := Discover(); !slices.Equal(got, want) {
		t.Errorf("Discover() = %+v, want %+v", got, want)
	}
}

// ProjectsRoots is cmd/swarmery's former globClaudeProjectsRoots: existing
// directories only, sorted. Covered HERE because cmd/swarmery is excluded from
// the coverage gate.
func TestProjectsRootsKeepsOnlyExistingDirsSorted(t *testing.T) {
	home := fakeHome(t)
	mkdirs(t,
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude-work", "projects"),
		filepath.Join(home, ".claude-empty"),
		filepath.Join(home, ".config", "projects"),
	)
	want := []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude-work", "projects"),
	}
	if got := ProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("ProjectsRoots() = %v, want %v", got, want)
	}
}

// An unresolvable home is not a panic and not a guess: no roots, no accounts.
func TestProjectsRootsWithoutAHomeIsEmpty(t *testing.T) {
	prev := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { userHomeDir = prev })

	if got := ProjectsRoots(); got != nil {
		t.Errorf("ProjectsRoots() = %v, want nil", got)
	}
	if got := Discover(); len(got) != 0 {
		t.Errorf("Discover() = %+v, want empty", got)
	}
}

// TestDiscoverWithDefault covers the branch Discover() alone deliberately does
// not: whether the default account is synthesised when it is missing from what
// physically exists on disk. Package var seam, so no t.Parallel() anywhere here.
func TestDiscoverWithDefault(t *testing.T) {
	t.Run("default already present", func(t *testing.T) {
		home := fakeHome(t)
		mkdirs(t,
			filepath.Join(home, ".claude", "projects"),
			filepath.Join(home, ".claude-work", "projects"),
		)
		want := Discover()
		got := DiscoverWithDefault()
		if !slices.Equal(got, want) {
			t.Errorf("DiscoverWithDefault() = %+v, want %+v (== Discover())", got, want)
		}
		count := 0
		for _, a := range got {
			if a.Key == "default" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("got %d entries keyed \"default\", want exactly 1: %+v", count, got)
		}
	})

	t.Run("default missing, synthesised first", func(t *testing.T) {
		home := fakeHome(t)
		mkdirs(t, filepath.Join(home, ".claude-work", "projects"))

		got := DiscoverWithDefault()
		if len(got) != 2 {
			t.Fatalf("DiscoverWithDefault() = %+v, want len 2", got)
		}
		if got[0].Key != "default" {
			t.Errorf("got[0].Key = %q, want %q", got[0].Key, "default")
		}
		if !got[0].IsDefault {
			t.Errorf("got[0].IsDefault = false, want true")
		}
		if want := filepath.Join(home, ".claude"); got[0].ConfigDir != want {
			t.Errorf("got[0].ConfigDir = %q, want %q", got[0].ConfigDir, want)
		}
		if got[1].Key != "work" {
			t.Errorf("got[1].Key = %q, want %q", got[1].Key, "work")
		}
	})

	t.Run("empty $HOME yields only the synthetic default", func(t *testing.T) {
		fakeHome(t)
		got := DiscoverWithDefault()
		if len(got) != 1 {
			t.Fatalf("DiscoverWithDefault() = %+v, want len 1", got)
		}
		if !got[0].IsDefault || got[0].Key != "default" {
			t.Errorf("got[0] = %+v, want the synthetic default", got[0])
		}
	})

	t.Run("userHomeDir errors, no panic and no fabricated path", func(t *testing.T) {
		prev := userHomeDir
		userHomeDir = func() (string, error) { return "", errors.New("no home") }
		t.Cleanup(func() { userHomeDir = prev })

		want := Discover() // Discover() itself is empty in this situation
		got := DiscoverWithDefault()
		if !slices.Equal(got, want) {
			t.Errorf("DiscoverWithDefault() = %+v, want %+v (== Discover())", got, want)
		}
	})
}

// ConfigDirFor is a pure mapping — it answers for accounts that do not exist yet
// (the provisioning case), so it must not consult the filesystem.
func TestConfigDirForMapsKeysWithoutTouchingDisk(t *testing.T) {
	home := fakeHome(t) // deliberately EMPTY: nothing is created

	for _, tc := range []struct{ key, want string }{
		{"default", filepath.Join(home, ".claude")},
		{"nabu-org", filepath.Join(home, ".claude-nabu-org")},
		{"science", filepath.Join(home, ".claude-science")},
	} {
		got, err := ConfigDirFor(tc.key)
		if err != nil {
			t.Fatalf("ConfigDirFor(%q): %v", tc.key, err)
		}
		if got != tc.want {
			t.Errorf("ConfigDirFor(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// An invalid key never becomes a path: the error comes back instead.
func TestConfigDirForRejectsUnsafeKeys(t *testing.T) {
	fakeHome(t)
	for _, key := range []string{"", "..", "../escape", "a/b", ".hidden"} {
		if got, err := ConfigDirFor(key); err == nil {
			t.Errorf("ConfigDirFor(%q) = %q, want an error", key, got)
		}
	}
}

func TestValidKey(t *testing.T) {
	valid := []string{"default", "nabu-org", "science", "work2", "a_b", "UPPER"}
	invalid := []string{
		"",          // no key at all
		".",         // the config dir itself
		"..",        // the parent of the config dir
		".hidden",   // would shadow a dotfile
		"a/b",       // path separator
		`a\b`,       // path separator (windows-style)
		"x..y",      // traversal hidden mid-key
		"../escape", // traversal
		"a b",       // space: unquotable as a file name
		"a\tb",      // control whitespace
		"a\x01b",    // non-printable
		" ",         // whitespace only
	}
	for _, key := range valid {
		if !ValidKey(key) {
			t.Errorf("ValidKey(%q) = false, want true", key)
		}
	}
	for _, key := range invalid {
		if ValidKey(key) {
			t.Errorf("ValidKey(%q) = true, want false", key)
		}
	}
}

// TestValidKeyIsLooserThanTheHookGate pins a DELIBERATE divergence: the
// accounts-pack SessionStart hook (plugins/accounts-pack/hooks/
// preflight-account.sh, valid_account_key) only lets [A-Za-z0-9._-] reach
// additionalContext, while ValidKey accepts these — so the shell test that
// asserts the hook stays silent for them proves a strictly tighter gate, and
// this test fails loudly if ValidKey is ever tightened to match (making that
// shell assertion vacuous). "a b" is on the hook's list too, but ValidKey
// already rejects it (see TestValidKey).
func TestValidKeyIsLooserThanTheHookGate(t *testing.T) {
	for _, key := range []string{"a$b", "a;b", "a`b", "wörk", `"a"`} {
		if !ValidKey(key) {
			t.Errorf("ValidKey(%q) = false; the hook's stricter gate is only meaningful while Go accepts it", key)
		}
	}
}
