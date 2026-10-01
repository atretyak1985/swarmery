package accountprune

// Regression tests for the independent review of 7ea67195..593f0f62:
// P1-1 nested estates, P1-2 symlinked paths, P1-3 pre-image pre-flight and
// partial results, and the re-verification of what the dry run showed.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
)

// the subset of the outer estate's pluginConfigs every nested fixture copies.
const subsetPC = `{"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`

// nestedEstate declares estate "sub" at <root>/sub, optionally admitted, with
// its own settings supply (a copy the OUTER estate would call redundant) and a
// sub-repo copy below it.
func nestedEstate(t *testing.T, f estateFixture, admit bool) (subRoot, subSupply, subBinding, below string) {
	t.Helper()
	subRoot = filepath.Join(f.root, "sub")
	subBinding = write(t, filepath.Join(subRoot, ".claude", "settings.local.json"), `{"swarmery":{"estate":"sub"}}`)
	subSupply = write(t, filepath.Join(subRoot, ".claude", "settings.json"), subsetPC)
	below = write(t, filepath.Join(subRoot, "x", ".claude", "settings.json"), subsetPC)
	if admit {
		accttest.AdmitEstate(t, "sub", subRoot)
	}
	return
}

// P1-1: a nested estate's own two files are "estate source", and a file under
// it is "other estate: <sub root>" — never compared with the outer estate,
// never written.
func TestNestedAdmittedEstateNeverPrunedAgainstOuter(t *testing.T) {
	f := newEstate(t)
	subRoot, supply, binding, below := nestedEstate(t, f, true)
	assertNested(t, f, subRoot, supply, binding, below)
}

func TestNestedUnadmittedEstateNeverPrunedAgainstOuter(t *testing.T) {
	f := newEstate(t)
	subRoot, supply, binding, below := nestedEstate(t, f, false)
	assertNested(t, f, subRoot, supply, binding, below)
}

func assertNested(t *testing.T, f estateFixture, subRoot, supply, binding, below string) {
	t.Helper()
	before := map[string]string{supply: read(t, supply), binding: read(t, binding), below: read(t, below)}
	targets := mustPlan(t, f.root)
	got := byPath(targets)
	for _, p := range []string{supply, binding} {
		if tg := got[p]; tg.Eligible || tg.Reason != ReasonEstateSource {
			t.Errorf("nested estate file %s = %+v, want estate source", p, tg)
		}
	}
	if tg := got[below]; tg.Eligible || tg.Reason != ReasonOtherEstatePrefix+subRoot {
		t.Errorf("file under the nested estate = %+v, want %q", tg, ReasonOtherEstatePrefix+subRoot)
	}
	if _, err := Apply(targets, Options{QuarantineDir: f.q}); err != nil {
		t.Fatal(err)
	}
	for p, b := range before {
		if read(t, p) != b {
			t.Errorf("%s was written", p)
		}
	}
}

// A nested binding that pins only an ACCOUNT declares no estate: the file
// still resolves to the outer estate and is prunable.
func TestNestedPinOnlyStillOuterEstate(t *testing.T) {
	f := newEstate(t)
	p := write(t, filepath.Join(f.root, "pin", ".claude", "settings.local.json"),
		`{"swarmery":{"claudeAccount":"default"},"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`)
	tg := byPath(mustPlan(t, f.root))[p]
	if !tg.Eligible || tg.EstateRoot != f.root {
		t.Fatalf("pin-only file = %+v, want eligible against %s", tg, f.root)
	}
}

// P1-2: a .claude directory symlinked OUTSIDE the estate is listed
// "symlinked path" and its target is never written.
func TestSymlinkedClaudeDirOutsideEstate(t *testing.T) {
	f := newEstate(t)
	outside := filepath.Join(filepath.Dir(f.root), "outside")
	target := write(t, filepath.Join(outside, ".claude", "settings.json"), subsetPC)
	proj := filepath.Join(f.root, "a")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, ".claude"), filepath.Join(proj, ".claude")); err != nil {
		t.Fatal(err)
	}
	assertSymlinkedSkipped(t, f, filepath.Join(proj, ".claude", "settings.json"), target, f.root)
}

// A .claude symlinked to another project INSIDE the estate: the link's
// spelling is skipped; the real file is listed on its own.
func TestSymlinkedClaudeDirInsideEstate(t *testing.T) {
	f := newEstate(t)
	real := write(t, filepath.Join(f.root, "b", ".claude", "settings.json"), subsetPC)
	proj := filepath.Join(f.root, "a")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, "b", ".claude"), filepath.Join(proj, ".claude")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(proj, ".claude", "settings.json")
	got := byPath(mustPlan(t, f.root))
	if tg := got[link]; tg.Eligible || tg.Reason != ReasonSymlinked {
		t.Errorf("link spelling = %+v, want %q", tg, ReasonSymlinked)
	}
	if tg := got[real]; !tg.Eligible {
		t.Errorf("real file = %+v, want eligible", tg)
	}
}

// A symlinked PARENT directory used as --path.
func TestSymlinkedParentDir(t *testing.T) {
	f := newEstate(t)
	outside := filepath.Join(filepath.Dir(f.root), "elsewhere", "proj")
	target := write(t, filepath.Join(outside, ".claude", "settings.json"), subsetPC)
	link := filepath.Join(f.root, "p")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	assertSymlinkedSkipped(t, f, filepath.Join(link, ".claude", "settings.json"), target, link)
}

func assertSymlinkedSkipped(t *testing.T, f estateFixture, spelled, target, scan string) {
	t.Helper()
	before := read(t, target)
	targets := mustPlan(t, scan)
	tg, ok := byPath(targets)[spelled]
	if !ok || tg.Eligible || tg.Reason != ReasonSymlinked {
		t.Errorf("%s = %+v (listed %v), want %q", spelled, tg, ok, ReasonSymlinked)
	}
	// Apply re-checks at write time: a target handed over as eligible is still
	// refused when its path runs through a link.
	forged := Target{Path: spelled, EstateRoot: f.root, Keys: []string{"pluginConfigs"}, Status: StatusNoRepo,
		Eligible: true, Reason: ReasonRedundant}
	res, err := Apply([]Target{forged}, Options{QuarantineDir: f.q})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 0 || read(t, target) != before {
		t.Errorf("wrote through the symlink: %+v", res)
	}
}

// P1-3b: a differing pre-image for the SECOND target aborts before the FIRST
// is written — every pre-image is checked before any write.
func TestPreImagePreflightZeroWrites(t *testing.T) {
	f := newEstate(t)
	a := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), subsetPC)
	b := write(t, filepath.Join(f.root, "b", ".claude", "settings.json"), subsetPC)
	write(t, filepath.Join(f.q, "prune", backupName(b)), "something else")
	before := map[string]string{a: read(t, a), b: read(t, b)}
	res, err := Apply(mustPlan(t, f.root), Options{QuarantineDir: f.q})
	if err == nil || !strings.Contains(err.Error(), "different pre-image") {
		t.Fatalf("err = %v", err)
	}
	if len(res.Changed) != 0 {
		t.Errorf("changed = %+v, want none", res.Changed)
	}
	for p, s := range before {
		if read(t, p) != s {
			t.Errorf("%s written before the pre-flight failed", p)
		}
	}
}

// P1-3b: '-' in a path can no longer make two paths share one pre-image.
func TestBackupNamesDistinct(t *testing.T) {
	a, b := backupName("/x/b-c/.claude/settings.json"), backupName("/x/b/c/.claude/settings.json")
	if a == b {
		t.Errorf("both map to %s", a)
	}
	if !strings.HasSuffix(a, ".bak.json") || !strings.HasPrefix(a, "-x-b-c-") {
		t.Errorf("name = %s, want the readable mangling plus a disambiguator", a)
	}
	// The pre-flight still refuses two jobs that would share one name.
	dup := []job{{t: Target{Path: "/p"}}, {t: Target{Path: "/p"}}}
	if err := preflightBackups(t.TempDir(), dup); err == nil {
		t.Error("two jobs on one pre-image: want an error")
	}
}

// Non-blocking: a file edited between the plan and the apply is skipped with
// "changed since plan" — whether the verdict changed or only the bytes.
func TestChangedSincePlan(t *testing.T) {
	f := newEstate(t)
	verdict := write(t, filepath.Join(f.root, "v", ".claude", "settings.json"), subsetPC)
	bytesOnly := write(t, filepath.Join(f.root, "w", ".claude", "settings.json"), subsetPC)
	targets := mustPlan(t, f.root)

	write(t, verdict, `{"pluginConfigs":{"a@m":{"options":{"k":"v1"}},"new@m":{}}}`)
	write(t, bytesOnly, `{"permissions":{"allow":[]},"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`)
	after := map[string]string{verdict: read(t, verdict), bytesOnly: read(t, bytesOnly)}

	res, err := Apply(targets, Options{QuarantineDir: f.q})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 0 || len(res.Skipped) != 2 {
		t.Fatalf("result = %+v, want two skips", res)
	}
	for _, s := range res.Skipped {
		if s.Reason != ReasonChangedSincePlan {
			t.Errorf("skip %+v", s)
		}
	}
	for p, s := range after {
		if read(t, p) != s {
			t.Errorf("%s written although it changed since the plan", p)
		}
	}
}

// A mid-run failure returns the files already changed, with their pre-images.
func TestMidRunFailureReportsChanged(t *testing.T) {
	f := newEstate(t)
	a := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), subsetPC)
	b := write(t, filepath.Join(f.root, "b", ".claude", "settings.json"), subsetPC)
	targets := mustPlan(t, f.root)
	dir := filepath.Dir(b)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	res, err := Apply(targets, Options{QuarantineDir: f.q})
	if err == nil {
		t.Fatal("want the rewrite of b to fail")
	}
	if len(res.Changed) != 1 || res.Changed[0].Path != a || res.Changed[0].Backup == "" {
		t.Errorf("changed = %+v, want a with its pre-image", res.Changed)
	}
}

// Redundancies is Plan's eligible set, key by key — the doctor's settings-block
// detector reads it, so the doctor reports exactly what the prune removes. One
// fixture carries every exclusion evaluate applies (the root's binding copy,
// a nested estate's two files and a file below it, a symlinked .claude) next
// to two ordinary copies.
func TestRedundanciesArePlanEligible(t *testing.T) {
	f := newEstate(t)
	nestedEstate(t, f, true)
	a := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), subsetPC)
	b := write(t, filepath.Join(f.root, "b", ".claude", "settings.local.json"),
		`{"pluginConfigs":{"b@m":{"options":{}}},"extraKnownMarketplaces":{"mk":{"source":{"source":"github","repo":"o/r"}}}}`)
	outside := filepath.Join(filepath.Dir(f.root), "outside")
	write(t, filepath.Join(outside, ".claude", "settings.json"), subsetPC)
	if err := os.MkdirAll(filepath.Join(f.root, "linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, ".claude"), filepath.Join(f.root, "linked", ".claude")); err != nil {
		t.Fatal(err)
	}

	var fromPlan []string
	for _, tg := range mustPlan(t, f.root) {
		if tg.Eligible {
			for _, k := range tg.Keys {
				fromPlan = append(fromPlan, tg.Path+" "+k)
			}
		}
	}
	var fromRedundancies []string
	for _, r := range Redundancies(f.root) {
		if r.EstateFile != f.estateFile {
			t.Errorf("%s pairs with %s, want the estate's own %s", r.Path, r.EstateFile, f.estateFile)
		}
		fromRedundancies = append(fromRedundancies, r.Path+" "+r.Key)
	}
	want := []string{a + " pluginConfigs", b + " pluginConfigs", b + " extraKnownMarketplaces"} // EstateKeys order
	if !reflect.DeepEqual(fromPlan, want) {
		t.Fatalf("precondition: Plan eligible = %v, want %v", fromPlan, want)
	}
	if !reflect.DeepEqual(fromRedundancies, fromPlan) {
		t.Errorf("Redundancies = %v\nPlan eligible = %v", fromRedundancies, fromPlan)
	}
}

// Entries are the copy's entry NAMES, sorted — never a value.
func TestRedundancyEntries(t *testing.T) {
	f := newEstate(t)
	write(t, filepath.Join(f.root, "a", ".claude", "settings.json"),
		`{"pluginConfigs":{"b@m":{"options":{}},"a@m":{"options":{"k":"v1"}}}}`)
	rs := Redundancies(f.root)
	if len(rs) != 1 || !reflect.DeepEqual(rs[0].Entries, []string{"a@m", "b@m"}) {
		t.Errorf("Redundancies = %+v, want one pluginConfigs entry naming a@m, b@m", rs)
	}
}
