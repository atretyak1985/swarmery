package claudeacct

// The writers' half of Lock 1 (D5): SetBinding and SetEstate — and so `use`,
// `estate use`, `switch` and ClearPins, which all write through them — refuse a
// binding file git tracks, or whose status git cannot tell, on set AND clear,
// and leave it byte-identical. Plus the tripwire every spawn seam rests on.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// committedBinding is a repository whose .claude/settings.local.json — holding
// both of our fields — is committed.
func committedBinding(t *testing.T) (repo string, before []byte) {
	t.Helper()
	repo = newRepo(t)
	writeAt(t, bindingPath(repo), `{"swarmery":{"claudeAccount":"work","estate":"acme"},"permissions":{}}`+"\n")
	runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
	runGit(t, repo, "commit", "-qm", "commit the binding")
	raw, err := os.ReadFile(bindingPath(repo))
	if err != nil {
		t.Fatal(err)
	}
	return repo, raw
}

func assertByteIdentical(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("%s changed although the write was refused", path)
	}
	if _, err := os.Lstat(path + ".bak"); err == nil {
		t.Fatalf("%s.bak was written although the write was refused", path)
	}
}

func TestSetBindingRefusesTrackedTarget(t *testing.T) {
	fakeHome(t)
	repo, before := committedBinding(t)
	for name, key := range map[string]string{"set": "other", "clear": ""} {
		err := SetBinding(repo, key)
		if err == nil || !errors.Is(err, ErrTrackedBinding) || !strings.Contains(err.Error(), "rm --cached") {
			t.Fatalf("%s on a committed binding: err = %v, want ErrTrackedBinding naming `git rm --cached`", name, err)
		}
		if !strings.Contains(err.Error(), bindingPath(repo)) {
			t.Fatalf("%s: the refusal does not name the path: %v", name, err)
		}
		assertByteIdentical(t, bindingPath(repo), before)
	}
}

func TestSetEstateRefusesTrackedTarget(t *testing.T) {
	fakeHome(t)
	repo, before := committedBinding(t)
	for name, key := range map[string]string{"set": "other", "clear": ""} {
		if err := SetEstate(repo, key); err == nil || !errors.Is(err, ErrTrackedBinding) {
			t.Fatalf("%s: SetEstate on a committed binding: err = %v, want ErrTrackedBinding", name, err)
		}
		assertByteIdentical(t, bindingPath(repo), before)
	}
}

// Unclassifiable is refused exactly like tracked: a .git FILE whose gitdir is
// gone makes git answer with an error, not a verdict.
func TestSetBindingRefusesIndeterminateTarget(t *testing.T) {
	fakeHome(t)
	dir := t.TempDir()
	writeAt(t, filepath.Join(dir, ".git"), "gitdir: /nonexistent/nowhere\n")
	writeBinding(t, dir, "work")
	before, _ := os.ReadFile(bindingPath(dir))
	if err := SetBinding(dir, "other"); err == nil || !errors.Is(err, ErrTrackedBinding) {
		t.Fatalf("SetBinding on an unclassifiable binding: err = %v, want ErrTrackedBinding", err)
	}
	assertByteIdentical(t, bindingPath(dir), before)
}

// A missing target is never probed, and an UNTRACKED one is written as before.
func TestSetBindingWritesUntrackedAndNewTargets(t *testing.T) {
	fakeHome(t)
	calls := countingProbe(t)
	fresh := t.TempDir()
	if err := SetBinding(fresh, "work"); err != nil {
		t.Fatalf("SetBinding on a new file: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("a missing target was probed %d time(s)", *calls)
	}
	repo := newRepo(t)
	writeBinding(t, repo, "work")
	if err := SetBinding(repo, "other"); err != nil {
		t.Fatalf("SetBinding on an untracked file: %v", err)
	}
	if got := Binding(repo); got != "other" {
		t.Fatalf("Binding after the write = %q, want other", got)
	}
}

// BindingFileUntrusted and DeclarationUnreadable name Lock 1's reason, so the
// surfaces that refuse or report an ignored file say WHY.
func TestUntrustedSurfacesIncludeProvenance(t *testing.T) {
	fakeHome(t)
	repo, _ := committedBinding(t)
	if why := BindingFileUntrusted(repo); !strings.Contains(why, "git tracks") {
		t.Fatalf("BindingFileUntrusted = %q, want the provenance reason", why)
	}
	if why := DeclarationUnreadable(repo); !strings.Contains(why, "git tracks") {
		t.Fatalf("DeclarationUnreadable = %q, want the provenance reason", why)
	}
}

// THE tripwire, on the integrated launch path: a committed binding reaches none
// of SpawnEnvFor or SpawnEnvResolved(Resolve(p)) — no config dir, no store name.
// (runcore.AccountFor is pinned by the same test in internal/runcore.)
func TestTrackedBindingReachesNoSpawnSeam(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)
	seedProbeStore(t, "work")
	anchorStore(t, "acme") // exists; rooted nowhere — the estate half must not appear either
	repo, _ := committedBinding(t)

	for name, env := range map[string][]string{
		"SpawnEnvFor":               SpawnEnvFor([]string{"PATH=/usr/bin"}, repo),
		"SpawnEnvResolved(Resolve)": SpawnEnvResolved([]string{"PATH=/usr/bin"}, Resolve(repo)),
	} {
		if n := countPrefix(env, configDirEnv+"="); n != 0 {
			t.Errorf("%s carries %d %s entries; want 0", name, n, configDirEnv)
		}
		if n := countPrefix(env, probeStoreName+"="); n != 0 {
			t.Errorf("%s carries %d store names; want 0", name, n)
		}
	}
	if r := Resolve(repo); r.Account != "" || r.Estate != "" {
		t.Fatalf("Resolve read a committed binding: account %q estate %q", r.Account, r.Estate)
	}
}
