package claudeacct

// Tests for Lock 2 — the store anchor (storeroot.go) — and for the release
// table as the composer applies it. Every store lives under a t.TempDir()
// pointed at by SWARMERY_SECRETS_DIR and holds literal non-secrets; every
// assertion is a verdict, a count or an absence — never a store value.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// anchorStore appends one `# swarmery-root:` line per root to store <key>.env
// in the current SWARMERY_SECRETS_DIR, creating an empty 0600 store (and a
// 0700 store dir, when none is set) as needed. It returns the store's path.
func anchorStore(t *testing.T, key string, roots ...string) string {
	t.Helper()
	dir := os.Getenv(secretsDirEnv)
	if dir == "" {
		dir = t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(secretsDirEnv, dir)
	}
	path := filepath.Join(dir, key+".env")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roots {
		if _, err := f.WriteString("# swarmery-root: " + r + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// installAccount makes key a real account in the (fake) home — a config dir
// with projects/, which is what Discover reports and what F2's rule requires
// before a ROOTLESS store is released through the account route.
func installAccount(t *testing.T, keys ...string) {
	t.Helper()
	h, err := userHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		mkdirs(t, filepath.Join(h, ".claude-"+key, "projects"))
	}
}

// resetRootlessWarn gives a test a fresh store-rootless warn-once ledger.
func resetRootlessWarn(t *testing.T) {
	t.Helper()
	rootlessWarned = &sync.Map{}
	t.Cleanup(func() { rootlessWarned = &sync.Map{} })
}

// namesWithPrefix counts env entries whose NAME starts with prefix.
func namesWithPrefix(env []string, prefix string) int {
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(envKey(kv), prefix) {
			n++
		}
	}
	return n
}

// ── admits ───────────────────────────────────────────────────────────────────

// A root admits itself and every directory below it. A rung that does not
// exist cannot be resolved, so it is never admitted; neither is "".
func TestStoreRootAdmitsDescendantRung(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	mkdirs(t, deep)
	for _, rung := range []string{root, filepath.Join(root, "a"), deep} {
		if got := admits(rung, []string{root}); got != root {
			t.Errorf("admits(%s) = %q, want the root %s", rung, got, root)
		}
	}
	if got := admits(filepath.Join(root, "missing"), []string{root}); got != "" {
		t.Errorf("an unresolvable rung was admitted by %q", got)
	}
	if got := admits("", []string{root}); got != "" {
		t.Errorf("the empty rung was admitted by %q", got)
	}
}

// A rung outside every root is refused — including a clone whose .claude is a
// symlink INTO the admitted tree: the rung is the clone, and the clone is not
// inside the root, whatever its .claude points at.
func TestStoreRootRefusesForeignRung(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "estate")
	clone := filepath.Join(parent, "clone")
	mkdirs(t, filepath.Join(root, ".claude"), clone)
	symlink(t, filepath.Join(root, ".claude"), filepath.Join(clone, ".claude"))
	if got := admits(clone, []string{root}); got != "" {
		t.Fatalf("a clone whose .claude links into the root was admitted by %q", got)
	}
	if got := admits(parent, []string{root}); got != "" {
		t.Fatalf("the root's PARENT was admitted by %q", got)
	}
}

// Admission walks the RESOLVED rung's ancestors, never the symbolic ones: a
// link planted inside the root that points outside it is not inside the root.
func TestAdmissionUsesResolvedAncestorsNotSymbolic(t *testing.T) {
	home := fakeHome(t)
	resetWarnOnce(t)
	root := filepath.Join(home, "projects", "ae")
	outside := filepath.Join(home, "elsewhere", "o")
	mkdirs(t, root, outside)
	declare(t, outside, map[string]any{"claudeAccount": "work", "estate": "ae"})
	seedStores(t, map[string]string{"ae": "AE_ONE=1\n", "work": "WORK_ONE=1\n"})
	anchorStore(t, "ae", root)
	anchorStore(t, "work", root)
	link := filepath.Join(root, "link")
	symlink(t, outside, link)

	if got := admits(link, []string{root}); got != "" {
		t.Fatalf("an outward link inside the root was admitted by %q — the symbolic ancestors were walked", got)
	}
	r := Resolve(link)
	if r.EstateAdmitted || r.AccountStoreAdmitted {
		t.Fatalf("resolution through the outward link: estate admitted %v, account store admitted %v, want neither",
			r.EstateAdmitted, r.AccountStoreAdmitted)
	}
	env := SpawnEnvResolved([]string{"PATH=/usr/bin"}, r)
	if n := namesWithPrefix(env, "AE_") + namesWithPrefix(env, "WORK_"); n != 0 {
		t.Fatalf("the outward link composed %d store names, want 0", n)
	}
}

// The sibling /x/ae-evil shares a string prefix with the root /x/ae, and is
// not inside it.
func TestSiblingPrefixDirNotAdmitted(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "ae")
	evil := filepath.Join(parent, "ae-evil")
	mkdirs(t, root, evil, filepath.Join(evil, "sub"))
	for _, rung := range []string{evil, filepath.Join(evil, "sub")} {
		if got := admits(rung, []string{root}); got != "" {
			t.Fatalf("%s was admitted by %q — a string-prefix test", rung, got)
		}
	}
}

// A root line spelled in another case admits on a case-insensitive filesystem:
// admission compares files, not spellings.
func TestStoreRootCaseVariantPath(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "Estate")
	mkdirs(t, filepath.Join(root, "p"))
	variant := filepath.Join(parent, "ESTATE")
	if _, err := os.Stat(variant); err != nil {
		t.Skipf("case-sensitive filesystem: %v", err)
	}
	if got := admits(filepath.Join(root, "p"), []string{variant}); got == "" {
		t.Fatal("a case-variant root line admitted nothing on a case-insensitive filesystem")
	}
}

// A relative, missing or empty root line admits nothing, and still makes the
// store ROOTED — fail closed, reported by line number and reason.
func TestMalformedRootLineAdmitsNothing(t *testing.T) {
	fakeHome(t)
	root := t.TempDir()
	seedStores(t, map[string]string{"acme": "ACME_ONE=1\n"})
	path := filepath.Join(os.Getenv(secretsDirEnv), "acme.env")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"# swarmery-root: relative/path", "# swarmery-root: " + filepath.Join(root, "missing"), "# swarmery-root:"} {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	sr := readStoreRoots(path)
	if !sr.rooted || len(sr.roots) != 0 || len(sr.bad) != 3 {
		t.Fatalf("rooted %v, %d usable roots, %d bad lines — want rooted, 0, 3", sr.rooted, len(sr.roots), len(sr.bad))
	}
	for _, why := range sr.bad {
		if strings.Contains(why, "ACME_ONE") {
			t.Fatalf("a root problem quoted the store's content: %q", why)
		}
	}
	if got := admits(root, sr.roots); got != "" {
		t.Fatalf("a store with only malformed root lines admitted %q", got)
	}
	if n := len(StoreRootProblems("acme")); n != 3 {
		t.Fatalf("StoreRootProblems = %d lines, want 3", n)
	}
}

// ── the release table through Resolve and the composer ───────────────────────

// An estate store with no root line releases nothing — the unanchored state:
// zero credentials, no settings file, not admitted, and never an error.
func TestEstateStoreWithoutRootReleasesNothing(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	declare(t, root, map[string]any{"estate": "acme"})
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	seedStores(t, map[string]string{"acme": "ACME_ONE=1\nACME_TWO=2\n"})

	r := Resolve(root)
	if r.EstateAdmitted || r.SettingsFile != "" || r.CredentialCount() != 0 || r.HasCredentialStore() {
		t.Fatalf("rootless estate: admitted %v, settings %q, credentials %d, has %v — want unanchored",
			r.EstateAdmitted, r.SettingsFile, r.CredentialCount(), r.HasCredentialStore())
	}
	if !strings.Contains(r.AdmissionNote, "estate acme unanchored") {
		t.Fatalf("AdmissionNote = %q, want the unanchored line", r.AdmissionNote)
	}
	if n := namesWithPrefix(SpawnEnvResolved(nil, r), "ACME_"); n != 0 {
		t.Fatalf("a rootless estate store composed %d names", n)
	}
	if state, why := r.CredentialStore(); state != StoreUnadmitted || !strings.Contains(why, "unanchored") {
		t.Fatalf("CredentialStore = %v %q, want unadmitted/unanchored", state, why)
	}

	// Anchoring it is the whole fix.
	anchorStore(t, "acme", root)
	r = Resolve(root)
	if !r.EstateAdmitted || r.SettingsFile == "" || r.CredentialCount() != 2 {
		t.Fatalf("anchored estate: admitted %v, settings %q, credentials %d", r.EstateAdmitted, r.SettingsFile, r.CredentialCount())
	}
	if !strings.Contains(r.AdmissionNote, "acme.env admitted by root "+root) {
		t.Fatalf("AdmissionNote = %q", r.AdmissionNote)
	}
}

// An estate with no store at all is the same unanchored state — zero, empty,
// no error and nothing logged.
func TestEstateWithoutStoreIsUnanchored(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "demo")
	declare(t, root, map[string]any{"estate": "demo"})
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	seedStores(t, map[string]string{})

	var r Resolution
	logged := captureLog(t, func() {
		r = Resolve(root)
		_ = SpawnEnvResolved([]string{"PATH=/usr/bin"}, r)
	})
	if r.CredentialCount() != 0 || r.SettingsFile != "" || r.EstateAdmitted {
		t.Fatalf("store-less estate: credentials %d, settings %q, admitted %v", r.CredentialCount(), r.SettingsFile, r.EstateAdmitted)
	}
	if logged != "" {
		t.Fatalf("an unanchored estate logged %q — it is never an error", logged)
	}
}

// A rootless ACCOUNT store keeps today's behaviour for an untracked binding:
// released, with exactly one store-rootless WARN however many spawns.
func TestRootlessAccountStoreUntrackedLoadsAndWarns(t *testing.T) {
	home := fakeHome(t)
	resetWarnOnce(t)
	resetRootlessWarn(t)
	proj := filepath.Join(home, "projects", "p")
	declare(t, proj, map[string]any{"claudeAccount": "work"})
	seedStores(t, map[string]string{"work": "WORK_ONE=1\n"})
	installAccount(t, "work")

	var total int
	logged := captureLog(t, func() {
		for i := 0; i < 3; i++ {
			total += namesWithPrefix(SpawnEnvFor([]string{"PATH=/usr/bin"}, proj), "WORK_")
		}
	})
	if total != 3 {
		t.Fatalf("three spawns composed %d store names, want 1 each", total)
	}
	if n := strings.Count(logged, "store-rootless"); n != 1 {
		t.Fatalf("store-rootless WARN logged %d times for three spawns, want 1\n%s", n, logged)
	}
	if strings.Contains(logged, "WORK_ONE") {
		t.Fatal("the WARN named a variable")
	}
	if r := Resolve(proj); !r.AccountStoreAdmitted || !strings.Contains(r.AdmissionNote, "work.env rootless") {
		t.Fatalf("admitted %v, note %q", r.AccountStoreAdmitted, r.AdmissionNote)
	}
}

// A rootless account store behind a TRACKED binding releases nothing: Lock 1
// ignores the binding, so no rung names the account at all.
func TestRootlessAccountStoreTrackedBindingReleasesNothing(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)
	seedStores(t, map[string]string{"work": "WORK_ONE=1\n"})
	repo := newRepo(t)
	writeBinding(t, repo, "work")
	runGit(t, repo, "add", "-f", "--", ".claude/settings.local.json")
	runGit(t, repo, "commit", "-qm", "commit the binding")

	r := Resolve(repo)
	if r.Account != "" || r.AccountStoreAdmitted {
		t.Fatalf("tracked binding resolved account %q (admitted %v), want nothing", r.Account, r.AccountStoreAdmitted)
	}
	if n := namesWithPrefix(SpawnEnvFor([]string{"PATH=/usr/bin"}, repo), "WORK_"); n != 0 {
		t.Fatalf("a tracked binding composed %d store names", n)
	}
	if !strings.Contains(r.IgnoredNote, "tracked by git") || !strings.Contains(r.IgnoredNote, "rm --cached") {
		t.Fatalf("IgnoredNote = %q, want the tracked reason and its remedy", r.IgnoredNote)
	}
}

// A store the loader refuses (0644) counts as rootless, whatever root lines it
// carries — and releases no names either way.
func TestRefusedStoreCountsAsRootless(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "acme"})
	seedStores(t, map[string]string{})
	for _, key := range []string{"acme", "work"} {
		p := anchorStore(t, key, root)
		if err := os.WriteFile(p, []byte("# swarmery-root: "+root+"\nNAME_"+key+"=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sr := readStoreRoots(SecretsPath("acme"))
	if sr.state != StoreRefused || sr.rooted {
		t.Fatalf("refused store: state %v rooted %v, want refused and rootless", sr.state, sr.rooted)
	}
	r := Resolve(root)
	if r.EstateAdmitted {
		t.Fatal("a refused estate store was admitted")
	}
	if n := namesWithPrefix(SpawnEnvResolved(nil, r), "NAME_"); n != 0 {
		t.Fatalf("a refused store composed %d names", n)
	}
}

// An ACCOUNT store that is rooted releases only to a rung its roots admit.
func TestRootedAccountStoreReleasesOnlyToAdmittedRung(t *testing.T) {
	home := fakeHome(t)
	resetRootlessWarn(t)
	ae := filepath.Join(home, "projects", "ae")
	other := filepath.Join(home, "projects", "other")
	declare(t, ae, map[string]any{"claudeAccount": "work"})
	declare(t, other, map[string]any{"claudeAccount": "work"})
	seedStores(t, map[string]string{"work": "WORK_ONE=1\n"})
	anchorStore(t, "work", ae)

	in, out := Resolve(filepath.Join(ae, "sub")), Resolve(other)
	if !in.AccountStoreAdmitted || out.AccountStoreAdmitted {
		t.Fatalf("admitted inside %v, outside %v — want true, false", in.AccountStoreAdmitted, out.AccountStoreAdmitted)
	}
	if n := namesWithPrefix(SpawnEnvResolved(nil, out), "WORK_"); n != 0 {
		t.Fatalf("a rooted account store reached a foreign rung: %d names", n)
	}
	if !strings.Contains(out.AdmissionNote, "not admitted by work.env roots") {
		t.Fatalf("AdmissionNote = %q", out.AdmissionNote)
	}
	// The payer is not gated by roots: the foreign rung still pays with work.
	if out.Account != "work" {
		t.Fatalf("the payer followed the roots: account %q", out.Account)
	}
}

// ── the forced and key-only compositions ─────────────────────────────────────

// Resume: the payer is forced to K, but the cwd resolves a different key — no
// K.env name reaches the child.
func TestResumeForcedAccountUnadmittedCwdCarriesNoStoreNames(t *testing.T) {
	home := fakeHome(t)
	proj := filepath.Join(home, "projects", "p")
	declare(t, proj, map[string]any{"claudeAccount": "other"})
	seedStores(t, map[string]string{"work": "WORK_ONE=1\n"})

	r := Resolve(proj).WithAccount("work")
	if r.Account != "work" || r.Source != SourceForced {
		t.Fatalf("forced resolution %+v", r)
	}
	if r.AccountStoreAdmitted {
		t.Fatal("a forced account the cwd does not resolve was admitted")
	}
	if n := namesWithPrefix(SpawnEnvResolved(nil, r), "WORK_"); n != 0 {
		t.Fatalf("forced unadmitted resume composed %d names", n)
	}

	// Same key, but K.env is rooted elsewhere: still nothing.
	declare(t, proj, map[string]any{"claudeAccount": "work"})
	aeOnly := filepath.Join(home, "projects", "ae-only")
	mkdirs(t, aeOnly)
	anchorStore(t, "work", aeOnly)
	r = Resolve(proj).WithAccount("work")
	if n := namesWithPrefix(SpawnEnvResolved(nil, r), "WORK_"); n != 0 {
		t.Fatalf("forced resume in a cwd K.env does not admit composed %d names", n)
	}
}

// Resume in a cwd that independently resolves K under a rung K.env admits: the
// store is carried.
func TestResumeForcedAccountAdmittedCwdCarriesStore(t *testing.T) {
	home := fakeHome(t)
	resetRootlessWarn(t)
	root := filepath.Join(home, "projects", "ae")
	declare(t, root, map[string]any{"claudeAccount": "work"})
	seedStores(t, map[string]string{"work": "WORK_ONE=1\nWORK_TWO=2\n"})
	anchorStore(t, "work", root)

	r := Resolve(filepath.Join(root, "deep")).WithAccount("work")
	if !r.AccountStoreAdmitted {
		t.Fatalf("forced resume in an admitted cwd: note %q", r.AdmissionNote)
	}
	if n := namesWithPrefix(SpawnEnvResolved(nil, r), "WORK_"); n != 2 {
		t.Fatalf("composed %d names, want 2", n)
	}
}

// The dashboard account terminal holds only a key: a ROOTED store is not
// released to it; a rootless one is (today's behaviour).
func TestSpawnEnvKeyOnlyReleasesNoRootedStore(t *testing.T) {
	fakeHome(t)
	resetRootlessWarn(t)
	installAccount(t, "work")
	seedStores(t, map[string]string{"work": "WORK_ONE=1\n"})
	if n := namesWithPrefix(SpawnEnv([]string{"PATH=/usr/bin"}, "work"), "WORK_"); n != 1 {
		t.Fatalf("key-only, rootless: %d names, want 1", n)
	}
	anchorStore(t, "work", t.TempDir())
	if n := namesWithPrefix(SpawnEnv([]string{"PATH=/usr/bin"}, "work"), "WORK_"); n != 0 {
		t.Fatalf("key-only, rooted: %d names, want 0", n)
	}
}

// The forced path never probes a relative binding path: Resolve("") does not
// walk, and WithAccount adds no probe of its own.
func TestForcedPathNeverProbesEmptyBindingPath(t *testing.T) {
	fakeHome(t)
	seedStores(t, map[string]string{"work": "WORK_ONE=1\n"})
	var relative int
	prev := runGitProbe
	runGitProbe = func(dir, name string) gitProbeResult {
		if !filepath.IsAbs(dir) {
			relative++
		}
		return prev(dir, name)
	}
	t.Cleanup(func() { runGitProbe = prev })

	r := Resolve("").WithAccount("work")
	_ = SpawnEnvResolved(nil, r)
	if relative != 0 {
		t.Fatalf("the forced path probed %d relative path(s)", relative)
	}
	if r.AccountStoreAdmitted {
		t.Fatal("a forced account with no cwd resolution was admitted")
	}
}

// ── D5's attacker cases ──────────────────────────────────────────────────────

// Root R carries an untracked binding and every store is anchored at R. A
// sub-repo under R COMMITS a binding naming a third key X: Lock 1 ignores it,
// so R/sub resolves exactly as if the file were not there, and X.env — rooted
// at R, so admission alone would have let it through — composes nothing.
func TestTrackedBindingUnderAnchoredRootReleasesNothing(t *testing.T) {
	home := fakeHome(t)
	resetWarnOnce(t)
	root := filepath.Join(home, "projects", "estate")
	declare(t, root, map[string]any{"claudeAccount": "acct", "estate": "est"})
	seedStores(t, map[string]string{"acct": "ACCT_ONE=1\n", "est": "EST_ONE=1\n", "x": "X_ONE=1\n"})
	for _, k := range []string{"acct", "est", "x"} {
		anchorStore(t, k, root)
	}
	sub := filepath.Join(root, "sub")
	mkdirs(t, sub)
	runGit(t, sub, "init", "-q", ".")
	writeAt(t, bindingPath(sub), `{"swarmery":{"claudeAccount":"x","estate":"x"}}`+"\n")
	runGit(t, sub, "add", "-f", "--", ".claude/settings.local.json")
	runGit(t, sub, "commit", "-qm", "commit a binding")

	with := Resolve(sub)
	env := SpawnEnvResolved(nil, with)
	if n := namesWithPrefix(env, "X_"); n != 0 {
		t.Fatalf("the committed binding released %d X.env names", n)
	}
	if err := os.Remove(bindingPath(sub)); err != nil {
		t.Fatal(err)
	}
	without := Resolve(sub)
	with.IgnoredNote = ""
	if with != without {
		t.Fatalf("Resolve(R/sub) with the committed file differs from without it:\n with    %+v\n without %+v", with, without)
	}
}

// A committed `claudeAccount: default` in a sub-repo does not override an
// inherited untracked pin: `default` is a key like any other to Lock 1.
func TestTrackedDefaultPinIgnored(t *testing.T) {
	home := fakeHome(t)
	resetWarnOnce(t)
	root := filepath.Join(home, "projects", "ae")
	declare(t, root, map[string]any{"claudeAccount": "work"})
	sub := filepath.Join(root, "repo")
	mkdirs(t, sub)
	runGit(t, sub, "init", "-q", ".")
	writeBinding(t, sub, "default")
	runGit(t, sub, "add", "-f", "--", ".claude/settings.local.json")
	runGit(t, sub, "commit", "-qm", "commit a default pin")

	r := Resolve(sub)
	if r.Account != "work" || r.Source != SourcePinParent || r.AccountRoot != root {
		t.Fatalf("resolved %q from %s at %q — want the inherited work pin", r.Account, r.Source, r.AccountRoot)
	}
}

// R13, pinned as a deliberate residual: an untracked binding outside every
// root keeps its PAYER, and receives no name from a rooted store.
func TestPayerFollowsUnadmittedBinding(t *testing.T) {
	home := fakeHome(t)
	mkdirs(t, filepath.Join(home, ".claude-work", "projects"))
	seedStores(t, map[string]string{"work": "WORK_ONE=1\n"})
	anchorStore(t, "work", filepath.Join(home, "projects", "ae"))
	mkdirs(t, filepath.Join(home, "projects", "ae"))
	loose := filepath.Join(home, "downloads", "tarball")
	declare(t, loose, map[string]any{"claudeAccount": "work"})

	r := Resolve(loose)
	if r.Account != "work" {
		t.Fatalf("payer = %q, want work — R13 says the payer is not root-gated", r.Account)
	}
	env := SpawnEnvResolved(nil, r)
	if got := configDirEntries(env); len(got) != 1 {
		t.Fatalf("config dir entries = %v, want the work account's", got)
	}
	if n := namesWithPrefix(env, "WORK_"); n != 0 {
		t.Fatalf("an unadmitted binding received %d store names", n)
	}
}

// sc14Fixture builds the SC-14 binding {claudeAccount: K, estate: E} plus a
// committed-or-not .claude/settings.json, with K.env and E.env rooted at an
// unrelated tree.
func sc14Fixture(t *testing.T, dir string) {
	t.Helper()
	writeAt(t, bindingPath(dir), `{"swarmery":{"claudeAccount":"k","estate":"e"}}`+"\n")
	writeAt(t, filepath.Join(dir, ".claude", "settings.json"), `{"enabledPlugins":{"x@y":true}}`+"\n")
}

// SC-14 (i): a git clone outside every root carrying the committed binding —
// 0 names, payer default, no settings file, and the ignored reason recorded.
func TestSC14_GitCloneOutsideRoot(t *testing.T) {
	home := fakeHome(t)
	resetWarnOnce(t)
	elsewhere := filepath.Join(home, "projects", "ae")
	mkdirs(t, elsewhere, filepath.Join(home, ".claude-k", "projects"))
	seedStores(t, map[string]string{"k": "K_ONE=1\n", "e": "E_ONE=1\n"})
	anchorStore(t, "k", elsewhere)
	anchorStore(t, "e", elsewhere)
	clone := cloneOf(t, func(src string) { sc14Fixture(t, src) },
		".claude/settings.local.json", ".claude/settings.json")

	r := Resolve(clone)
	env := SpawnEnvResolved(nil, r)
	if n := namesWithPrefix(env, "K_") + namesWithPrefix(env, "E_"); n != 0 {
		t.Fatalf("clone composed %d store names", n)
	}
	if r.Account != "" || !r.DefaultProfile || r.SettingsFile != "" {
		t.Fatalf("clone: account %q default %v settings %q — want the default payer and no settings", r.Account, r.DefaultProfile, r.SettingsFile)
	}
	if !strings.Contains(r.IgnoredNote, "tracked by git") {
		t.Fatalf("IgnoredNote = %q, want the tracked-by-git reason", r.IgnoredNote)
	}
}

// SC-14 (ii): an extracted tarball (no .git) outside every root — Lock 1
// honours it, Lock 2 releases nothing: 0 names, no settings, not admitted, and
// the payer is K (R13).
func TestSC14_ExtractedTarballOutsideRoot(t *testing.T) {
	home := fakeHome(t)
	elsewhere := filepath.Join(home, "projects", "ae")
	mkdirs(t, elsewhere, filepath.Join(home, ".claude-k", "projects"))
	seedStores(t, map[string]string{"k": "K_ONE=1\n", "e": "E_ONE=1\n"})
	anchorStore(t, "k", elsewhere)
	anchorStore(t, "e", elsewhere)
	tarball := filepath.Join(home, "downloads", "x")
	sc14Fixture(t, tarball)

	r := Resolve(tarball)
	env := SpawnEnvResolved(nil, r)
	if n := namesWithPrefix(env, "K_") + namesWithPrefix(env, "E_"); n != 0 {
		t.Fatalf("tarball composed %d store names", n)
	}
	if r.SettingsFile != "" || r.EstateAdmitted {
		t.Fatalf("tarball: settings %q admitted %v", r.SettingsFile, r.EstateAdmitted)
	}
	for _, want := range []string{"not admitted by k.env roots", "not admitted by e.env roots"} {
		if !strings.Contains(r.AdmissionNote, want) {
			t.Fatalf("AdmissionNote = %q, want %q", r.AdmissionNote, want)
		}
	}
	if r.Account != "k" {
		t.Fatalf("payer = %q, want k (R13)", r.Account)
	}
}

// ── surfaces count only what is admitted ─────────────────────────────────────

func TestCredentialCountAdmittedOnly(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	declare(t, root, map[string]any{"estate": "acme"})
	seedStores(t, map[string]string{"acme": "A=1\nB=2\n"})
	anchorStore(t, "acme", filepath.Join(home, "projects", "other"))
	mkdirs(t, filepath.Join(home, "projects", "other"))

	r := Resolve(root)
	if r.CredentialCount() != 0 || r.HasCredentialStore() {
		t.Fatalf("unadmitted: count %d has %v", r.CredentialCount(), r.HasCredentialStore())
	}
	if state, why := r.CredentialStore(); state != StoreUnadmitted || why != "not admitted by acme.env roots" {
		t.Fatalf("CredentialStore = %v %q", state, why)
	}
}

// ── review round 1 (2026-09-28): F1, F2, F3 and root-line hardening ──────────

// F1: a writer about to CREATE the binding file probes the links on the way.
// A clone that commits `.claude -> <victim>/.claude` must not get the write
// into the victim's directory, even though no binding file exists yet.
func TestWriterRefusesCreateThroughTrackedLink(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)
	victim := filepath.Join(t.TempDir(), "victim")
	mkdirs(t, filepath.Join(victim, ".claude"))
	clone := cloneOf(t, func(src string) {
		symlink(t, filepath.Join(victim, ".claude"), filepath.Join(src, ".claude"))
	}, ".claude")

	for name, write := range map[string]func() error{
		"SetBinding": func() error { return SetBinding(clone, "j") },
		"SetEstate":  func() error { return SetEstate(clone, "j") },
	} {
		if err := write(); err == nil || !strings.Contains(err.Error(), "tracked by git") {
			t.Fatalf("%s through a committed .claude link: err = %v, want the tracked refusal", name, err)
		}
		if _, err := os.Lstat(bindingPath(victim)); err == nil {
			t.Fatalf("%s created a binding in the victim's directory", name)
		}
	}
	// The operator's own (untracked) link still takes a write.
	own := t.TempDir()
	symlink(t, filepath.Join(victim, ".claude"), filepath.Join(own, ".claude"))
	if err := SetBinding(own, "j"); err != nil {
		t.Fatalf("SetBinding through an untracked link: %v", err)
	}
}

// F2: a ROOTLESS store is released through the account route only for a key
// that is a real account here. A tarball naming an estate key as its "account"
// gets nothing from that estate's unanchored store.
func TestRootlessStoreNotReleasedForANonAccountKey(t *testing.T) {
	home := fakeHome(t)
	resetRootlessWarn(t)
	seedStores(t, map[string]string{"ins": "INS_ONE=1\nINS_TWO=2\n"})
	tarball := filepath.Join(home, "Downloads", "x")
	declare(t, tarball, map[string]any{"claudeAccount": "ins"})

	r := Resolve(tarball)
	if n := namesWithPrefix(SpawnEnvResolved(nil, r), "INS_"); n != 0 {
		t.Fatalf("a non-account key pulled %d names from a rootless store", n)
	}
	if r.AccountStoreAdmitted || !strings.Contains(r.AdmissionNote, "no account on this machine") {
		t.Fatalf("admitted %v, note %q", r.AccountStoreAdmitted, r.AdmissionNote)
	}
	// Once ins IS an account, the rootless store keeps today's behaviour.
	installAccount(t, "ins")
	if n := namesWithPrefix(SpawnEnvResolved(nil, Resolve(tarball)), "INS_"); n != 2 {
		t.Fatalf("a real account's rootless store composed %d names, want 2", n)
	}
}

// F3: the ladder climbs LOGICAL ancestors, so `<root>/link -> ~/outside` puts a
// physically foreign directory under the root. Admission also requires the
// resolved path the walk ran from to be inside a root.
func TestPhysicalPathMustBeInsideTheRoot(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "ae")
	outside := filepath.Join(home, "outside")
	mkdirs(t, filepath.Join(outside, "deep"))
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "ae"})
	seedStores(t, map[string]string{"ae": "AE_ONE=1\n", "work": "WORK_ONE=1\n"})
	anchorStore(t, "ae", root)
	anchorStore(t, "work", root)
	symlink(t, outside, filepath.Join(root, "link"))

	r := Resolve(filepath.Join(root, "link", "deep"))
	if r.EstateRoot != root || r.AccountRoot != root {
		t.Fatalf("precondition: the logical ladder reaches the root: %+v", r)
	}
	if r.EstateAdmitted || r.AccountStoreAdmitted || r.SettingsFile != "" {
		t.Fatalf("a physically foreign cwd was admitted: estate %v account %v", r.EstateAdmitted, r.AccountStoreAdmitted)
	}
	if n := namesWithPrefix(SpawnEnvResolved(nil, r), "AE_") + namesWithPrefix(SpawnEnvResolved(nil, r), "WORK_"); n != 0 {
		t.Fatalf("a physically foreign cwd composed %d names", n)
	}
	// A real directory under the root, and one not created yet, still admit.
	mkdirs(t, filepath.Join(root, "real"))
	for _, p := range []string{filepath.Join(root, "real"), filepath.Join(root, "not", "yet")} {
		if !Resolve(p).EstateAdmitted {
			t.Fatalf("%s under the root was not admitted", p)
		}
	}
}

// A misspelled marker makes a store MORE rooted, never rootless.
func TestLooseRootMarkersCount(t *testing.T) {
	for _, line := range []string{"# Swarmery-Root: /x", "#swarmery_root: /x", "\ufeff# swarmery-root: /x", "# SWARMERY ROOT: /x"} {
		if v, ok := rootLineValue(line); !ok || v != "/x" {
			t.Errorf("rootLineValue(%q) = %q, %v — want /x, true", line, v, ok)
		}
	}
	for _, line := range []string{"# swarmery", "# comment", "SWARMERY_ROOT=/x", "#"} {
		if _, ok := rootLineValue(line); ok {
			t.Errorf("rootLineValue(%q) read a root line", line)
		}
	}
}

// A root of "/" or $HOME (or any ancestor of home) admits nothing: it would
// admit every archive and clone that lands under home.
func TestTooBroadRootAdmitsNothing(t *testing.T) {
	home := fakeHome(t)
	for _, root := range []string{"/", home, filepath.Dir(home)} {
		if _, why := usableRoot(root); !strings.Contains(why, "home directory") {
			t.Errorf("usableRoot(%s) = %q, want the too-broad refusal", root, why)
		}
	}
	mkdirs(t, filepath.Join(home, "projects", "ae"))
	if _, why := usableRoot(filepath.Join(home, "projects", "ae")); why != "" {
		t.Errorf("a project root was refused: %q", why)
	}
}
