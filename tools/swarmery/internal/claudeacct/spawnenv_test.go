package claudeacct

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// bakedDir stands in for a CLAUDE_CONFIG_DIR the daemon inherited from its
// launchd plist (`swarmery install --claude-config-dir`) or the caller's shell.
const bakedDir = "/baked/by/launchd/.claude-work"

func configDirEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, configDirEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// An unbound project inherits the daemon's environment byte for byte — a baked
// CLAUDE_CONFIG_DIR included. That is the documented meaning of the install flag.
func TestSpawnEnv_UnboundIsBytePassthrough(t *testing.T) {
	base := []string{"PATH=/usr/bin", configDirEnv + "=" + bakedDir, "TERM=xterm"}
	got := SpawnEnv(base, "")
	if !slices.Equal(got, base) {
		t.Fatalf("SpawnEnv(base, \"\") = %v, want base unchanged %v", got, base)
	}
	if len(got) > 0 && &got[0] != &base[0] {
		t.Errorf("unbound spawn copied the environment; want the very same slice (passthrough identity)")
	}
}

// THE explicit-default case: a project deliberately bound to the default account
// must run under ~/.claude even when the daemon itself carries a baked
// CLAUDE_CONFIG_DIR — the variable has to be REMOVED, not merely left alone.
func TestSpawnEnv_ExplicitDefaultDropsInheritedConfigDir(t *testing.T) {
	fakeHome(t)
	base := []string{"PATH=/usr/bin", configDirEnv + "=" + bakedDir, "TERM=xterm"}
	got := SpawnEnv(base, "default")
	if entries := configDirEntries(got); len(entries) != 0 {
		t.Fatalf("explicit default binding kept %v — the daemon's baked account would win over the operator's choice", entries)
	}
	if want := []string{"PATH=/usr/bin", "TERM=xterm"}; !slices.Equal(got, want) {
		t.Errorf("env = %v, want %v (only CLAUDE_CONFIG_DIR removed, order kept)", got, want)
	}
	// And without an inherited variable there is nothing to do: passthrough.
	clean := []string{"PATH=/usr/bin"}
	if got := SpawnEnv(clean, "default"); !slices.Equal(got, clean) {
		t.Errorf("SpawnEnv(clean, default) = %v, want %v", got, clean)
	}
}

// A bound account: exactly one CLAUDE_CONFIG_DIR entry (the binding's, not the
// stale inherited one), the secret store appended after it, unrelated variables
// carried through untouched.
func TestSpawnEnv_BoundAccountMergesConfigDirAndSecrets(t *testing.T) {
	fakeHome(t)
	installAccount(t, "work") // a rootless store is released through the account route only for a real account
	seedStore(t, "work", "MCP_TOKEN=abc\n", 0o600)
	want, ok := ConfigDirForAccount("work")
	if !ok || want == "" {
		t.Fatalf("precondition: ConfigDirForAccount(work) = %q, %v", want, ok)
	}

	base := []string{"PATH=/usr/bin", configDirEnv + "=" + bakedDir, "MCP_TOKEN=stale", "TERM=xterm"}
	got := SpawnEnv(base, "work")

	if entries := configDirEntries(got); len(entries) != 1 || entries[0] != configDirEnv+"="+want {
		t.Fatalf("CLAUDE_CONFIG_DIR entries = %v, want exactly [%s=%s] — execve keeps duplicates and getenv takes the first",
			entries, configDirEnv, want)
	}
	var tokens []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "MCP_TOKEN=") {
			tokens = append(tokens, kv)
		}
	}
	if !slices.Equal(tokens, []string{"MCP_TOKEN=abc"}) {
		t.Errorf("MCP_TOKEN entries = %v, want exactly [MCP_TOKEN=abc] (store wins, stale copy removed)", tokens)
	}
	for _, keep := range []string{"PATH=/usr/bin", "TERM=xterm"} {
		if !slices.Contains(got, keep) {
			t.Errorf("dropped %q — only keys the delta sets may be removed", keep)
		}
	}
	// Delta order: config dir before the store, both after everything inherited.
	n := len(got)
	if got[n-2] != configDirEnv+"="+want || got[n-1] != "MCP_TOKEN=abc" {
		t.Errorf("tail = %v, want [CLAUDE_CONFIG_DIR=…, MCP_TOKEN=abc] last", got[n-2:])
	}
}

// resolvedDelta is the same two pieces, delta-shaped, before the merge.
func TestResolvedDelta_ConfigDirThenSecrets(t *testing.T) {
	fakeHome(t)
	installAccount(t, "work") // a rootless store is released through the account route only for a real account
	seedStore(t, "work", "MCP_TOKEN=abc\n", 0o600)
	dir, _ := ConfigDirForAccount("work")
	if got, want := resolvedDelta(Resolution{Account: "work"}), []string{configDirEnv + "=" + dir, "MCP_TOKEN=abc"}; !slices.Equal(got, want) {
		t.Errorf("resolvedDelta(work) = %v, want %v", got, want)
	}
	for _, key := range []string{"", "default"} {
		if got := resolvedDelta(Resolution{Account: key}); len(got) != 0 {
			t.Errorf("resolvedDelta(%q) = %v, want empty", key, got)
		}
	}
}

// The empty-path guard: SpawnEnvFor("") must not read the RELATIVE
// .claude/settings.local.json under the process cwd. Proven non-vacuously by
// making the cwd a bound project for the duration of the test.
func TestSpawnEnvFor_EmptyProjectPathIsPassthrough(t *testing.T) {
	fakeHome(t)
	bound := t.TempDir()
	if err := SetBinding(bound, "work"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(bound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prevWd) })
	if Binding("") == "" {
		t.Fatalf("precondition: Binding(\"\") resolved nothing — the relative-path trap this test guards is gone")
	}

	base := []string{"PATH=/usr/bin"}
	if got := SpawnEnvFor(base, ""); !slices.Equal(got, base) {
		t.Errorf("SpawnEnvFor(base, \"\") = %v, want %v untouched", got, base)
	}
	// And a real bound path resolves through the same function.
	if entries := configDirEntries(SpawnEnvFor(base, bound)); len(entries) != 1 {
		t.Errorf("SpawnEnvFor(bound) CLAUDE_CONFIG_DIR entries = %v, want exactly one", entries)
	}
}

// entriesNamed returns every entry of env whose NAME is name.
func entriesNamed(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if envKey(kv) == name {
			out = append(out, kv)
		}
	}
	return out
}

// Precedence: account store first, estate store second, collapsed by name
// keeping the LAST writer — so the estate wins, and there is exactly ONE entry
// for the colliding name in the delta and in the spawned env alike. One entry is
// what makes the raw-execve path (first match wins) and the os/exec path (last
// wins) agree; the CLI half of that is exercised through a real execve in
// cmd/swarmery.
func TestSpawnEnvResolved_EstateWinsCollision(t *testing.T) {
	fakeHome(t)
	installAccount(t, "work") // a rootless store is released through the account route only for a real account
	seedStores(t, map[string]string{
		"work": "SHARED=from-account\nACCOUNT_ONLY=a\n",
		"acme": "SHARED=from-estate\nESTATE_ONLY=e\n",
	})
	// D5: an estate store releases only when ROOTED and its roots admit the
	// estate root, so the estate here has a root the store names.
	estateRoot := t.TempDir()
	anchorStore(t, "acme", estateRoot)
	r := Resolution{Account: "work", Estate: "acme"}
	r.EstateRoot = estateRoot

	delta := resolvedDelta(r)
	if got := entriesNamed(delta, "SHARED"); !slices.Equal(got, []string{"SHARED=from-estate"}) {
		t.Fatalf("delta SHARED entries = %v, want exactly the estate's one", got)
	}
	seen := map[string]bool{}
	for _, name := range deltaNames(delta) {
		if seen[name] {
			t.Fatalf("delta carries %s twice: %v", name, deltaNames(delta))
		}
		seen[name] = true
	}
	for _, name := range []string{configDirEnv, "ACCOUNT_ONLY", "ESTATE_ONLY"} {
		if !seen[name] {
			t.Errorf("delta lost %s", name)
		}
	}

	base := []string{"PATH=/usr/bin", "SHARED=inherited"}
	env := SpawnEnvResolved(base, r)
	if got := entriesNamed(env, "SHARED"); !slices.Equal(got, []string{"SHARED=from-estate"}) {
		t.Fatalf("spawned env SHARED entries = %v, want exactly one, the estate's", got)
	}
	if !slices.Equal(base, []string{"PATH=/usr/bin", "SHARED=inherited"}) {
		t.Fatalf("base was mutated: %v", base)
	}

	// Default payer: the config dir is dropped, the estate is still carried.
	env = SpawnEnvResolved([]string{configDirEnv + "=" + bakedDir, "PATH=/usr/bin"}, Resolution{Account: "default", Estate: "acme", EstateRoot: estateRoot})
	if len(configDirEntries(env)) != 0 {
		t.Fatalf("default payer kept a config dir: %v", configDirEntries(env))
	}
	if got := entriesNamed(env, "ESTATE_ONLY"); len(got) != 1 {
		t.Fatalf("default payer lost the estate's store: %v", deltaNames(env))
	}
	// Unbound payer with an estate: the inherited config dir passes, the estate
	// is added.
	env = SpawnEnvResolved([]string{configDirEnv + "=" + bakedDir}, Resolution{Estate: "acme", EstateRoot: estateRoot})
	if got := configDirEntries(env); !slices.Equal(got, []string{configDirEnv + "=" + bakedDir}) {
		t.Fatalf("unbound payer config dir = %v, want the inherited one", got)
	}
	if got := entriesNamed(env, "SHARED"); len(got) != 1 {
		t.Fatalf("unbound payer SHARED = %v", got)
	}
}

// No estate ⇒ byte-identical to the pre-estate composition: the config dir,
// then the account's store. This is the SC-12 property at the unit level.
func TestSpawnEnvResolved_NoEstateIsTheOldDelta(t *testing.T) {
	fakeHome(t)
	installAccount(t, "work") // a rootless store is released through the account route only for a real account
	seedStores(t, map[string]string{"work": "MCP_TOKEN=abc\nOTHER=1\n"})
	old := append(EnvForAccount("work"), SecretEnvForAccount("work")...)
	if got := resolvedDelta(Resolution{Account: "work"}); !slices.Equal(got, old) {
		t.Fatalf("resolvedDelta without an estate = %v, want the old delta %v", deltaNames(got), deltaNames(old))
	}
	base := []string{"PATH=/usr/bin"}
	if got, want := SpawnEnvResolved(base, Resolution{Account: "work"}), SpawnEnv(base, "work"); !slices.Equal(got, want) {
		t.Fatalf("SpawnEnvResolved and SpawnEnv disagree: %v vs %v", deltaNames(got), deltaNames(want))
	}
}

// D2a at the spawn seam: a declared estate with no store file composes the same
// environment as no estate, silently.
func TestSpawnEnvResolved_StorelessEstateEqualsNoEstate(t *testing.T) {
	fakeHome(t)
	seedStores(t, map[string]string{"work": "MCP_TOKEN=abc\n"})
	base := []string{"PATH=/usr/bin"}
	var with, without []string
	logged := captureLog(t, func() {
		with = SpawnEnvResolved(base, Resolution{Account: "work", Estate: "nostore"})
		without = SpawnEnvResolved(base, Resolution{Account: "work"})
	})
	if !slices.Equal(with, without) {
		t.Fatalf("store-less estate changed the env: %v vs %v", deltaNames(with), deltaNames(without))
	}
	if logged != "" {
		t.Fatalf("store-less estate logged %q", logged)
	}
	// And with nothing at all to add, base passes through by identity.
	if got := SpawnEnvResolved(base, Resolution{Estate: "nostore"}); &got[0] != &base[0] {
		t.Fatal("an empty resolution copied base")
	}
}

// collapseLastWins keeps the last writer and the order of those last entries.
func TestCollapseLastWins(t *testing.T) {
	in := []string{"A=1", "B=1", "A=2", "C=1", "B=2"}
	if got, want := collapseLastWins(in), []string{"A=2", "C=1", "B=2"}; !slices.Equal(got, want) {
		t.Fatalf("collapseLastWins = %v, want %v", got, want)
	}
	uniq := []string{"A=1", "B=1"}
	if got := collapseLastWins(uniq); &got[0] != &uniq[0] {
		t.Fatal("a delta with no repeated name was copied")
	}
}

// SpawnEnvFor resolves the estate by walking: a project under a declaring root
// composes the root's store even though its own pin says "default".
func TestSpawnEnvFor_ComposesTheInheritedEstate(t *testing.T) {
	home := fakeHome(t)
	seedStores(t, map[string]string{"acme": "ACME_ONE=1\nACME_TWO=2\n"})
	root := filepath.Join(home, "projects", "acme")
	proj := filepath.Join(root, "deployment", "src", "php")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "acme"})
	declare(t, proj, map[string]any{"claudeAccount": "default"})
	anchorStore(t, "acme", root) // D5: an estate store releases only when rooted

	env := SpawnEnvFor([]string{configDirEnv + "=" + bakedDir}, proj)
	if len(configDirEntries(env)) != 0 {
		t.Fatalf("a default-pinned project kept a config dir: %v", configDirEntries(env))
	}
	n := 0
	for _, name := range deltaNames(env) {
		if strings.HasPrefix(name, "ACME_") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("composed %d estate names, want 2", n)
	}
}
