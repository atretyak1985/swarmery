package claudeacct

// Tests for Resolve: the ladder, its bounds, and the independence of the two
// axes. Every tree lives under a fakeHome, every store under a t.TempDir()
// pointed at by SWARMERY_SECRETS_DIR, and every variable is a literal
// non-secret.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// declare writes <dir>/.claude/settings.local.json carrying ns as the swarmery
// namespace (plus one foreign key, so the file is never "ours only").
func declare(t *testing.T, dir string, ns map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"swarmery": ns, "permissions": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	writeSettingsFile(t, dir, string(raw))
}

// seedStores writes several stores (name → body) into ONE secrets dir, 0600.
func seedStores(t *testing.T, stores map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	t.Setenv(secretsDirEnv, dir)
	for name, body := range stores {
		p := filepath.Join(dir, name+".env")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeFile writes content at path, creating parents.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// deltaNames is the NAME set of a delta — assertions compare names and counts,
// never values.
func deltaNames(delta []string) []string {
	out := make([]string, 0, len(delta))
	for _, kv := range delta {
		out = append(out, envKey(kv))
	}
	return out
}

// THE fix: a project pinned to "default" on its own rung still inherits the
// estate an ancestor declares. The rung that decided the account says nothing
// about the estate.
func TestResolve_PinnedDefaultStillInheritsTheEstate(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	proj := filepath.Join(root, "deployment", "src", "php")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "acme"})
	declare(t, proj, map[string]any{"claudeAccount": "default"})

	r := Resolve(proj)
	if r.Account != "default" || r.Source != SourcePin || r.AccountRoot != proj {
		t.Fatalf("account = %q source %q root %q, want default / pin / %s", r.Account, r.Source, r.AccountRoot, proj)
	}
	if r.Estate != "acme" || r.EstateRoot != root {
		t.Fatalf("estate = %q root %q, want acme at %s — the estate must not depend on the account's rung", r.Estate, r.EstateRoot, root)
	}
	if !r.DefaultProfile || r.ConfigDir != "" {
		t.Errorf("DefaultProfile=%v ConfigDir=%q, want true and \"\"", r.DefaultProfile, r.ConfigDir)
	}

	// A descendant with no pin of its own inherits BOTH from the root.
	sub := filepath.Join(root, "repos", "one")
	mkdirs(t, sub)
	r = Resolve(sub)
	if r.Account != "work" || r.Source != SourcePinParent || r.AccountRoot != root {
		t.Fatalf("descendant account = %q/%q/%q, want work / pin(parent) / %s", r.Account, r.Source, r.AccountRoot, root)
	}
	if r.Estate != "acme" {
		t.Fatalf("descendant estate = %q, want acme", r.Estate)
	}
	if r.DefaultProfile {
		t.Error("DefaultProfile = true for a bound non-default account")
	}
}

// The payer can move at the root and the estate does not: flipping the root's
// account changes Account for every descendant and Estate for none.
func TestResolve_AccountAndEstateAreIndependent(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	proj := filepath.Join(root, "repos", "one")
	mkdirs(t, proj)
	for _, payer := range []string{"work", "default", ""} {
		ns := map[string]any{"estate": "acme"}
		if payer != "" {
			ns["claudeAccount"] = payer
		}
		declare(t, root, ns)
		r := Resolve(proj)
		if r.Account != payer {
			t.Errorf("payer %q: Account = %q", payer, r.Account)
		}
		if r.Estate != "acme" || r.EstateRoot != root {
			t.Errorf("payer %q: estate = %q at %q, want acme at %s", payer, r.Estate, r.EstateRoot, root)
		}
	}
	// With no pin anywhere the source is the default and Account stays "":
	// unbound must stay byte-identical to today (an inherited config dir passes).
	if r := Resolve(proj); r.Source != SourceDefault || r.Account != "" || !r.DefaultProfile {
		t.Errorf("unpinned: %+v, want Source default, Account \"\", DefaultProfile", r)
	}
}

// "" never walks: a relative binding path would be read against the process cwd.
func TestResolve_EmptyPathIsNone(t *testing.T) {
	fakeHome(t)
	r := Resolve("  ")
	if r.Source != SourceNone || r.Account != "" || r.Estate != "" || !r.DefaultProfile {
		t.Fatalf("Resolve(\"\") = %+v, want Source none and nothing resolved", r)
	}
}

// DefaultProfile follows the resolved KEY, including for a WithAccount override,
// and ConfigDir is populated for a non-default account.
func TestResolve_DefaultProfileIsExplicit(t *testing.T) {
	home := fakeHome(t)
	proj := filepath.Join(home, "p")
	declare(t, proj, map[string]any{"claudeAccount": "work"})
	r := Resolve(proj)
	if r.DefaultProfile || r.ConfigDir != filepath.Join(home, ".claude-work") {
		t.Fatalf("bound work: DefaultProfile=%v ConfigDir=%q", r.DefaultProfile, r.ConfigDir)
	}
	d := r.WithAccount("default")
	if !d.DefaultProfile || d.ConfigDir != "" || d.Source != SourceForced || d.AccountRoot != "" {
		t.Fatalf("WithAccount(default) = %+v", d)
	}
	if lines := d.EnvLines(); len(lines) != 0 {
		t.Errorf("EnvLines for default = %v, want none", lines)
	}
	if lines := r.EnvLines(); len(lines) != 1 || lines[0] != configDirEnv+"="+filepath.Join(home, ".claude-work") {
		t.Errorf("EnvLines for work = %v", lines)
	}
}

// (a) EstateRoot == ProjectPath: the project declares the estate on itself, so
// its settings file IS the estate's — one contribution, decided by resolved
// path. Also through a symlinked spelling of the project path.
func TestResolve_EstateRootIsProjectPath(t *testing.T) {
	home := fakeHome(t)
	proj := filepath.Join(home, "projects", "code")
	declare(t, proj, map[string]any{"estate": "code"})
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), "{}\n")
	anchorStore(t, "code", proj) // D5: only an admitted estate contributes its settings file

	r := Resolve(proj)
	if r.Estate != "code" || r.EstateRoot != proj {
		t.Fatalf("estate %q at %q, want code at %s", r.Estate, r.EstateRoot, proj)
	}
	er, _ := filepath.EvalSymlinks(r.EstateRoot)
	pr, _ := filepath.EvalSymlinks(proj)
	if er != pr {
		t.Fatalf("EstateRoot %q and project %q differ after EvalSymlinks", er, pr)
	}
	if r.SettingsFile == "" || !r.EstateSettingsIsProjects {
		t.Fatalf("SettingsFile=%q EstateSettingsIsProjects=%v, want the file and true", r.SettingsFile, r.EstateSettingsIsProjects)
	}

	// The same project reached through a symlinked directory spelling.
	link := filepath.Join(home, "link-to-code")
	if err := os.Symlink(proj, link); err != nil {
		t.Fatal(err)
	}
	r = Resolve(link)
	if r.Estate != "code" || !r.EstateSettingsIsProjects {
		t.Fatalf("via symlink: %+v, want estate code and one contribution", r)
	}
}

// The symlinked-.claude shape: the estate root's .claude is a SYMLINK to a
// directory deeper in the tree, and a descendant project's settings.json is a
// link to that same file. As strings the two paths differ; as files they are
// one. A string compare would report two contributions and fail this test.
func TestResolve_EstateRootSymlinked(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "sky", "code")
	agents := filepath.Join(root, "general", "agents")
	mkdirs(t, agents)
	writeFile(t, filepath.Join(agents, "settings.local.json"), `{"swarmery":{"estate":"sky"}}`)
	writeFile(t, filepath.Join(agents, "settings.json"), "{}\n")
	if err := os.Symlink(agents, filepath.Join(root, ".claude")); err != nil {
		t.Fatal(err)
	}
	anchorStore(t, "sky", root) // D5: only an admitted estate contributes its settings file
	proj := filepath.Join(root, "app")
	mkdirs(t, filepath.Join(proj, ".claude"))
	if err := os.Symlink(filepath.Join(root, ".claude", "settings.json"), filepath.Join(proj, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}

	r := Resolve(proj)
	if r.Estate != "sky" || r.EstateRoot != root {
		t.Fatalf("estate %q at %q, want sky at %s", r.Estate, r.EstateRoot, root)
	}
	if r.SettingsFile != filepath.Join(root, ".claude", "settings.json") {
		t.Fatalf("SettingsFile = %q", r.SettingsFile)
	}
	if r.SettingsFile == filepath.Join(proj, ".claude", "settings.json") {
		t.Fatal("precondition: the two spellings must differ as strings")
	}
	if !r.EstateSettingsIsProjects {
		t.Fatal("EstateSettingsIsProjects = false: the same file spelled two ways was counted twice (a string compare?)")
	}
	if !SameFile(r.SettingsFile, filepath.Join(proj, ".claude", "settings.json")) {
		t.Fatal("SameFile disagrees with the resolution")
	}
	// A dangling side is DIFFERENT, never equal.
	if SameFile(r.SettingsFile, filepath.Join(proj, "missing.json")) {
		t.Fatal("SameFile(existing, missing) = true")
	}
	// A project with its OWN settings file is a different contribution.
	other := filepath.Join(root, "other")
	writeFile(t, filepath.Join(other, ".claude", "settings.json"), "{}\n")
	if Resolve(other).EstateSettingsIsProjects {
		t.Fatal("a distinct project settings file was treated as the estate's")
	}
}

// (b) A nested declaration replaces the ancestor's key, root AND settings file
// wholesale — even where the sub-root has no settings file of its own.
func TestResolve_NestedEstateReplaces(t *testing.T) {
	home := fakeHome(t)
	outer := filepath.Join(home, "projects", "big")
	inner := filepath.Join(outer, "sub")
	declare(t, outer, map[string]any{"estate": "outer"})
	writeFile(t, filepath.Join(outer, ".claude", "settings.json"), "{}\n")
	declare(t, inner, map[string]any{"estate": "inner"})
	proj := filepath.Join(inner, "p")
	mkdirs(t, proj)
	anchorStore(t, "outer", outer) // D5: an unanchored estate contributes no settings file
	anchorStore(t, "inner", inner)

	r := Resolve(proj)
	if r.Estate != "inner" || r.EstateRoot != inner {
		t.Fatalf("estate %q at %q, want inner at %s", r.Estate, r.EstateRoot, inner)
	}
	if r.SettingsFile != "" {
		t.Fatalf("SettingsFile = %q, want \"\" — the ancestor's settings file must not leak into a sub-estate", r.SettingsFile)
	}
	// Outside the sub-root the outer estate still applies.
	if r := Resolve(filepath.Join(outer, "elsewhere")); r.Estate != "outer" || r.SettingsFile == "" {
		t.Fatalf("outer tree: %+v", r)
	}
}

// (b) continued: the ancestor's STORE is not merged into the sub-estate's
// composed env. Asserted as a COUNT of names.
func TestResolve_NestedEstateDoesNotMerge(t *testing.T) {
	home := fakeHome(t)
	seedStores(t, map[string]string{
		"outer": "OUTER_ONE=x\nOUTER_TWO=y\n",
		"inner": "INNER_ONE=z\n",
	})
	outer := filepath.Join(home, "projects", "big")
	inner := filepath.Join(outer, "sub")
	declare(t, outer, map[string]any{"estate": "outer"})
	declare(t, inner, map[string]any{"estate": "inner"})
	proj := filepath.Join(inner, "p")
	mkdirs(t, proj)
	anchorStore(t, "outer", outer) // D5: an estate store releases only when rooted
	anchorStore(t, "inner", inner)

	r := Resolve(proj)
	if n := len(resolvedDelta(r)); n != 1 {
		t.Fatalf("composed %d names under the sub-estate, want 1 — the ancestor's store was merged in", n)
	}
	if n := r.CredentialCount(); n != 1 {
		t.Fatalf("CredentialCount = %d, want 1", n)
	}
	if n := len(resolvedDelta(Resolve(filepath.Join(outer, "q")))); n != 2 {
		t.Fatalf("outer tree composed %d names, want 2", n)
	}
}

// D2a: an estate whose store file does not exist is healthy. Non-empty Estate
// and EstateRoot, the same delta as no estate at all, and no log output.
func TestResolve_EstateWithoutStore(t *testing.T) {
	home := fakeHome(t)
	seedStores(t, map[string]string{"work": "WORK_ONE=a\n"})
	root := filepath.Join(home, "projects", "demo")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "demo"})
	// The account store is anchored at the root, so the only thing left to be
	// silent about is the store-less estate (a rootless ACCOUNT store WARNs).
	anchorStore(t, "work", root)

	var r Resolution
	var withEstate, without []string
	logged := captureLog(t, func() {
		r = Resolve(root)
		withEstate = resolvedDelta(r)
		noEstate := r
		noEstate.Estate, noEstate.EstateRoot = "", ""
		without = resolvedDelta(noEstate)
		_ = SpawnEnvResolved([]string{"PATH=/usr/bin"}, r)
	})
	if r.Estate != "demo" || r.EstateRoot != root {
		t.Fatalf("estate %q at %q, want demo at %s", r.Estate, r.EstateRoot, root)
	}
	if !slices.Equal(withEstate, without) {
		t.Fatalf("delta with a store-less estate differs from no estate: %v vs %v", deltaNames(withEstate), deltaNames(without))
	}
	if r.CredentialCount() != 0 {
		t.Fatalf("CredentialCount = %d, want 0", r.CredentialCount())
	}
	if logged != "" {
		t.Fatalf("a store-less estate logged %q — it is a healthy state and must be silent", logged)
	}
}

// $HOME is never a candidate: a declaration in ~/.claude/settings.local.json
// must never be read as an estate or a pin.
func TestResolve_StopsAtHome(t *testing.T) {
	home := fakeHome(t)
	declare(t, home, map[string]any{"claudeAccount": "work", "estate": "everything"})
	proj := filepath.Join(home, "projects", "p")
	mkdirs(t, proj)
	r := Resolve(proj)
	if r.Account != "" || r.Estate != "" || r.Source != SourceDefault {
		t.Fatalf("Resolve under a declaring $HOME = %+v, want nothing resolved", r)
	}
	if r := Resolve(home); r.Account != "" || r.Estate != "" {
		t.Fatalf("Resolve($HOME) = %+v, want nothing — $HOME is not a candidate even for itself", r)
	}
}

// Outside $HOME the walk ends BELOW the filesystem root, and a path deeper than
// the cap is walked only maxLadderRungs deep.
func TestResolve_StopsAtRoot(t *testing.T) {
	fakeHome(t)
	sep := string(filepath.Separator)
	got := ladder(filepath.Join(sep, "a", "b", "c"))
	want := []string{filepath.Join(sep, "a", "b", "c"), filepath.Join(sep, "a", "b"), filepath.Join(sep, "a")}
	if !slices.Equal(got, want) {
		t.Fatalf("ladder = %v, want %v (root excluded)", got, want)
	}
	if got := ladder(sep); len(got) != 0 {
		t.Fatalf("ladder(root) = %v, want none", got)
	}
	deep := sep + strings.Repeat("d"+sep, maxLadderRungs+20)
	if got := ladder(filepath.Clean(deep)); len(got) != maxLadderRungs {
		t.Fatalf("ladder of a %d-deep path = %d rungs, want the cap %d", maxLadderRungs+20, len(got), maxLadderRungs)
	}
	// And Resolve itself terminates on a path that does not exist at all.
	if r := Resolve(filepath.Join(sep, "no", "such", "tree")); r.Source != SourceDefault {
		t.Fatalf("Resolve(nonexistent) = %+v", r)
	}
}

// An unparseable ancestor means "nothing declared here": the walk continues past
// it, and nothing errors.
func TestResolve_MalformedAncestor(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	mid := filepath.Join(root, "mid")
	proj := filepath.Join(mid, "p")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "acme"})
	writeSettingsFile(t, mid, "{")
	writeSettingsFile(t, proj, `{"swarmery": "not an object"}`)

	r := Resolve(proj)
	if r.Account != "work" || r.Estate != "acme" || r.Source != SourcePinParent {
		t.Fatalf("Resolve past a malformed ancestor = %+v, want work/acme from %s", r, root)
	}
}

// ValidKey gates BOTH fields before any path join. An invalid value is
// "nothing declared here" — the walk continues to a valid ancestor.
func TestResolve_RejectsUnsafeKey(t *testing.T) {
	home := fakeHome(t)
	proj := filepath.Join(home, "projects", "p")
	declare(t, proj, map[string]any{"claudeAccount": "../../etc", "estate": "../../etc"})
	r := Resolve(proj)
	if r.Account != "" || r.Estate != "" {
		t.Fatalf("unsafe keys resolved: %+v", r)
	}
	declare(t, filepath.Join(home, "projects"), map[string]any{"claudeAccount": "work", "estate": "safe"})
	r = Resolve(proj)
	if r.Account != "work" || r.Estate != "safe" {
		t.Fatalf("after an unsafe rung: %+v, want the valid ancestor's work/safe", r)
	}
	for _, bad := range []string{"a/b", ".hidden", "a b", ".."} {
		declare(t, proj, map[string]any{"estate": bad})
		if key, _ := Estate(proj); key != "" {
			t.Errorf("Estate with %q = %q, want \"\"", bad, key)
		}
	}
}

// A sibling whose NAME prefixes the estate root's is not a descendant: the
// ancestor walk cannot capture it (a HasPrefix implementation would).
func TestResolve_SiblingPrefix(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "ae")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "ae"})
	sib := filepath.Join(home, "projects", "ae-test", "p")
	mkdirs(t, sib)
	if r := Resolve(sib); r.Estate != "" || r.Account != "" {
		t.Fatalf("sibling %s inherited %+v from %s", sib, r, root)
	}
	if r := Resolve(filepath.Join(home, "projects", "ae-test")); r.Estate != "" {
		t.Fatalf("sibling root inherited estate %q", r.Estate)
	}
}

// Shadowed names the ancestors above the winning rung that pin a DIFFERENT
// account — and only those.
func TestShadowed(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	mid := filepath.Join(root, "deployment")
	proj := filepath.Join(mid, "src", "php")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "acme"})
	declare(t, mid, map[string]any{"claudeAccount": "default"}) // same as the winner: not a shadow
	declare(t, proj, map[string]any{"claudeAccount": "default"})

	got := Shadowed(proj)
	if len(got) != 1 || got[0] != (AncestorPin{Dir: root, Account: "work"}) {
		t.Fatalf("Shadowed = %+v, want only %s says work", got, root)
	}
	if got := Shadowed(filepath.Join(root, "other")); len(got) != 0 {
		t.Fatalf("an inheriting path shadows nothing: %+v", got)
	}
	if got := Shadowed(filepath.Join(home, "unpinned")); got != nil {
		t.Fatalf("an unpinned ladder shadows nothing: %+v", got)
	}
	if Shadowed("") != nil {
		t.Fatal("Shadowed(\"\") walked")
	}
	r := Resolve(proj)
	if r.HasCredentialStore() || r.CredentialCount() != 0 {
		t.Fatal("no store dir configured, yet a store was reported")
	}
}

// A path under the daemon's worktree root resolves through its source checkout.
func TestResolve_WorktreeResolvesFromItsSource(t *testing.T) {
	home := fakeHome(t)
	src := filepath.Join(home, "projects", "acme", "tools", "some-repo")
	declare(t, filepath.Join(home, "projects", "acme"), map[string]any{"claudeAccount": "work", "estate": "acme"})
	declare(t, src, map[string]any{"claudeAccount": "default"})
	wt := filepath.Join(home, ".swarmery", "worktrees", strings.ReplaceAll(src, "/", "-"), "phase-7")
	linkWorktree(t, src, wt, "phase-7")
	// The lent copy of the pin a worktree carries: it must not matter.
	declare(t, wt, map[string]any{"claudeAccount": "work"})

	r := Resolve(wt)
	if r.Estate != "acme" || r.Account != "default" || r.Source != SourcePin {
		t.Fatalf("worktree resolved %+v, want the SOURCE's default pin and the acme estate", r)
	}
	if r := Resolve(filepath.Join(wt, "sub", "dir")); r.Estate != "acme" || r.Source != SourcePinParent {
		t.Fatalf("worktree subdir resolved %+v", r)
	}
}
