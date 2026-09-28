package acctops

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const estateBody = `{"swarmery":{"claudeAccount":"insart","estate":"tmpfixture"}}`

// newEstate makes <home>/work/estate bound insart with estate tmpfixture.
func newEstate(t *testing.T, home string) (root, file string) {
	t.Helper()
	root = filepath.Join(home, "work", "estate")
	return root, writeBinding(t, root, estateBody)
}

func noDB(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.db") }

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestSwitchRefusesAnEstatelessPath(t *testing.T) {
	home, _ := fakeHome(t)
	bare := filepath.Join(home, "work", "bare")
	file := writeBinding(t, bare, `{"swarmery":{"claudeAccount":"insart"}}`)
	before := sha(t, file)

	for _, opts := range []SwitchOptions{
		{Key: "default", Estate: bare, DBPath: noDB(t)},
		{Key: "default", Cwd: bare, DBPath: noDB(t), Force: true},
		{Key: "default", Cwd: filepath.Join(home, "nowhere"), Force: true},
	} {
		_, err := Switch(opts)
		if !errors.Is(err, ErrNoEstate) {
			t.Fatalf("%+v: err = %v, want ErrNoEstate", opts, err)
		}
		if !strings.Contains(err.Error(), "swarmery account use") || !strings.Contains(err.Error(), `"estate":"<name>"`) {
			t.Errorf("refusal does not name both remedies: %v", err)
		}
	}
	if sha(t, file) != before {
		t.Error("the bare binding file changed")
	}
	if exists_(file + ".bak") {
		t.Error("a .bak was created on a refusal")
	}
}

func TestSwitchRefusesUnknownHeadroom(t *testing.T) {
	home, _ := fakeHome(t)
	root, file := newEstate(t, home)
	before := sha(t, file)
	rep, err := Switch(SwitchOptions{Key: "default", Estate: root, DBPath: noDB(t)})
	if !errors.Is(err, ErrHeadroomUnknown) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want ErrHeadroomUnknown naming --force", err)
	}
	if rep.HeadroomKnown || rep.Wrote {
		t.Errorf("report = %+v", rep)
	}
	if sha(t, file) != before || exists_(file+".bak") {
		t.Error("a refused switch wrote")
	}
}

func TestSwitchForceWritesAndIsIdempotent(t *testing.T) {
	home, _ := fakeHome(t)
	root, file := newEstate(t, home)
	rep, err := Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Wrote || rep.OldAccount != "insart" || rep.NewAccount != "default" || rep.EstateSource != "--estate" {
		t.Errorf("report = %+v", rep)
	}
	if got := claudeacct.Resolve(root).Account; got != "default" {
		t.Errorf("resolved account = %q, want default", got)
	}
	out := joined(rep.Lines())
	for _, want := range []string{"estate:      " + root + " (source: --estate)", "estate key:  tmpfixture",
		"account:     insart -> default", "headroom:    unknown", "pins:        none", "result:      bound"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	afterFirst, bakFirst := sha(t, file), sha(t, file+".bak")

	rep2, err := Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Wrote || !strings.Contains(joined(rep2.Lines()), "already bound") {
		t.Errorf("second switch reported a write: %+v", rep2)
	}
	if sha(t, file) != afterFirst || sha(t, file+".bak") != bakFirst {
		t.Error("an identical second switch changed the file or its .bak")
	}
}

// The estate is found from the cwd, from any depth below the root.
func TestSwitchResolvesTheEstateFromTheCwd(t *testing.T) {
	home, _ := fakeHome(t)
	root, _ := newEstate(t, home)
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := Switch(SwitchOptions{Key: "default", Cwd: deep, Force: true, DryRun: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.EstateRoot != root || rep.EstateSource != "binding" || rep.Wrote {
		t.Errorf("report = %+v", rep)
	}
}

func TestSwitchKnownHeadroomProceedsWithoutForce(t *testing.T) {
	home, _ := fakeHome(t)
	root, _ := newEstate(t, home)
	dbPath := filepath.Join(t.TempDir(), "s.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	if err := store.PutAccountQuota(db, "default", []store.QuotaRow{
		{WindowKey: "five_hour", Label: "Session (5h)", PercentUsed: 40, PercentLeft: 60},
		{WindowKey: "seven_day", Label: "Weekly", PercentUsed: 75, PercentLeft: 25},
	}, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	rep, err := Switch(SwitchOptions{Key: "default", Estate: root, DBPath: dbPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HeadroomKnown || rep.Headroom.PercentLeft != 25 {
		t.Errorf("headroom = %+v", rep.Headroom)
	}
	if !strings.Contains(joined(rep.Lines()), "headroom:    25% left in Weekly, read 2m0s ago") {
		t.Errorf("output:\n%s", joined(rep.Lines()))
	}
	// A stale reading is unknown again.
	_, err = Switch(SwitchOptions{Key: "insart", Estate: root, DBPath: dbPath,
		Now: func() time.Time { return now.Add(24 * time.Hour) }})
	if !errors.Is(err, ErrHeadroomUnknown) {
		t.Errorf("stale headroom: err = %v", err)
	}
}

// The staleness bound follows SWARMERY_QUOTA_INTERVAL (3x the effective
// interval), and the refusal tells "stale (last reading <age> ago)" from
// "unknown (no reading)", and says when polling is off.
func TestSwitchHeadroomStalenessFollowsTheInterval(t *testing.T) {
	home, _ := fakeHome(t)
	root, _ := newEstate(t, home)
	dbPath := filepath.Join(t.TempDir(), "s.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	if err := store.PutAccountQuota(db, "default", []store.QuotaRow{
		{WindowKey: "seven_day", Label: "Weekly", PercentUsed: 50, PercentLeft: 50},
	}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	at := func() time.Time { return now }

	// Default 10m interval → 30m bound: a 2h-old reading is stale, and says so.
	rep, err := Switch(SwitchOptions{Key: "default", Estate: root, DBPath: dbPath, Now: at})
	if !errors.Is(err, ErrHeadroomUnknown) || !strings.Contains(err.Error(), "stale (last reading 2h0m0s ago)") ||
		!strings.Contains(err.Error(), "30m0s") {
		t.Fatalf("stale: err = %v", err)
	}
	if !strings.Contains(joined(rep.Lines()), "headroom:    stale (last reading 2h0m0s ago)") {
		t.Errorf("stale line:\n%s", joined(rep.Lines()))
	}
	// An account with no row at all: unknown (no reading).
	_, err = Switch(SwitchOptions{Key: "insart", Estate: root, DBPath: dbPath, Now: at})
	if err == nil || !strings.Contains(err.Error(), "unknown (no reading)") {
		t.Errorf("no reading: err = %v", err)
	}
	// A 1h interval → 3h bound: the same reading vouches.
	t.Setenv(quotaIntervalEnv, "1h")
	if rep, err := Switch(SwitchOptions{Key: "default", Estate: root, DBPath: dbPath, Now: at, DryRun: true}); err != nil ||
		!rep.HeadroomKnown || rep.HeadroomMaxAge != 3*time.Hour {
		t.Errorf("1h interval: known=%v maxAge=%s err=%v", rep.HeadroomKnown, rep.HeadroomMaxAge, err)
	}
	// Polling disabled: the refusal says no reading will arrive.
	t.Setenv(quotaIntervalEnv, "off")
	rep, err = Switch(SwitchOptions{Key: "default", Estate: root, DBPath: dbPath, Now: at})
	if err == nil || !strings.Contains(err.Error(), "poller is disabled") || !rep.PollingOff {
		t.Errorf("polling off: err = %v", err)
	}
	if !strings.Contains(joined(rep.Lines()), "quota polling is disabled") {
		t.Errorf("polling-off line:\n%s", joined(rep.Lines()))
	}
	assertNoSecretShape(t, append(rep.Lines(), err.Error()))
}

func TestHeadroomMaxAge(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    time.Duration
		polling bool
	}{
		{"", 30 * time.Minute, true},
		{"5m", 15 * time.Minute, true},
		{"garbage", 30 * time.Minute, true},
		{"off", 30 * time.Minute, false},
		{"0", 30 * time.Minute, false},
	} {
		if d, p := HeadroomMaxAge(c.in); d != c.want || p != c.polling {
			t.Errorf("HeadroomMaxAge(%q) = %s %v, want %s %v", c.in, d, p, c.want, c.polling)
		}
	}
}

func TestSwitchKeepsBindingWriteDiscipline(t *testing.T) {
	home, _ := fakeHome(t)
	root := filepath.Join(home, "work", "estate")
	file := writeBinding(t, root,
		`{"permissions":{"deny":["Bash(rm:*)"]},"swarmery":{"claudeAccount":"insart","estate":"tmpfixture"}}`)
	if _, err := Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DBPath: noDB(t)}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !regexp.MustCompile(`"permissions":\s*\{\s*"deny":\s*\[\s*"Bash\(rm:\*\)"\s*\]`).Match(b) ||
		!strings.Contains(string(b), `"estate": "tmpfixture"`) {
		t.Errorf("a foreign key or the estate did not survive:\n%s", b)
	}

	// Unparseable: refused, nothing written, no .bak.
	root2 := filepath.Join(home, "work", "broken")
	file2 := writeBinding(t, root2, `{"swarmery":{"claudeAccount":"insart","estate":"x"}}`)
	// Estate() must see a declaration, so break the file only after resolution
	// would read it: a trailing garbage byte keeps JSON invalid for the writer.
	if err := os.WriteFile(file2, []byte(`{"swarmery":{"claudeAccount":"insart","estate":"x"}} garbage`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := sha(t, file2)
	if _, err := Switch(SwitchOptions{Key: "default", Estate: root2, Force: true, DBPath: noDB(t)}); err == nil {
		t.Error("switch over an unparseable file succeeded")
	}
	if sha(t, file2) != before || exists_(file2+".bak") {
		t.Error("an unparseable file was written or backed up")
	}
}

func TestSwitchCredentialCountOnly(t *testing.T) {
	home, secrets := fakeHome(t)
	root, _ := newEstate(t, home)
	rep, err := Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DryRun: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	lines := rep.Lines()
	if n := countPrefix(lines, "credentials: 0 (no store file"); n != 1 {
		t.Errorf("no-store credentials line count = %d\n%s", n, joined(lines))
	}
	assertNoSecretShape(t, lines)

	// A rootless estate store is unanchored (D5): it supplies nothing, and the
	// report says so rather than counting names no spawn would receive.
	store := filepath.Join(secrets, "tmpfixture.env")
	if err := os.WriteFile(store, []byte("FIXTURE_ONE=fixture-value-1\nFIXTURE_TWO=fixture-value-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DryRun: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	if n := countPrefix(rep.Lines(), "credentials: 0 (store not admitted: estate tmpfixture unanchored"); n != 1 {
		t.Errorf("unanchored store line missing\n%s", joined(rep.Lines()))
	}
	// Anchored at the estate root, it is admitted and counted.
	if err := os.WriteFile(store, []byte("# swarmery-root: "+root+"\nFIXTURE_ONE=fixture-value-1\nFIXTURE_TWO=fixture-value-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DryRun: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	lines = rep.Lines()
	if n := countPrefix(lines, "credentials: 2 from "+store); n != 1 {
		t.Errorf("store credentials line missing\n%s", joined(lines))
	}
	for _, l := range lines {
		if strings.Contains(l, "FIXTURE_") {
			t.Error("a variable name reached the output")
		}
	}
	assertNoSecretShape(t, lines)

	// A refused store (group-readable) is reported as refused, count 0.
	if err := os.Chmod(store, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, _ = Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DryRun: true, DBPath: noDB(t)})
	if n := countPrefix(rep.Lines(), "credentials: 0 (store refused"); n != 1 {
		t.Errorf("refused store line missing\n%s", joined(rep.Lines()))
	}
}

func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestSwitchRejectsBadKeys(t *testing.T) {
	home, _ := fakeHome(t)
	root, file := newEstate(t, home)
	before := sha(t, file)
	for _, key := range []string{"../x", "", "nosuch"} {
		if _, err := Switch(SwitchOptions{Key: key, Estate: root, Force: true, DBPath: noDB(t)}); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
	if sha(t, file) != before {
		t.Error("a refused key wrote")
	}
}

// Pins are classified against the estate's CURRENT account, a disagreeing pin
// survives a round trip in both directions, and only a standing-still run
// sweeps.
func TestSwitchPinsClassifiedAndRoundTrip(t *testing.T) {
	home, _ := fakeHome(t)
	root, _ := newEstate(t, home)
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	fileA := writeBinding(t, repoA, `{"swarmery":{"claudeAccount":"insart"}}`)
	fileB := writeBinding(t, repoB, `{"swarmery":{"claudeAccount":"default"}}`)
	// A pin inside a skipped directory is never reported.
	writeBinding(t, filepath.Join(root, ".swarmery", "worktrees", "wt"), `{"swarmery":{"claudeAccount":"insart"}}`)
	shaB := sha(t, fileB)

	rep, err := Switch(SwitchOptions{Key: "default", Estate: root, Force: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	lines := rep.Lines()
	pinsLine := regexp.MustCompile(`^pins: +1 redundant, 1 disagreeing`)
	n := 0
	for _, l := range lines {
		if pinsLine.MatchString(l) {
			n++
			if !strings.HasSuffix(l, ": "+repoB) {
				t.Errorf("pins line names the wrong repo: %s", l)
			}
		}
	}
	if n != 1 {
		t.Errorf("pins line count = %d\n%s", n, joined(lines))
	}
	if !strings.Contains(joined(lines), "keeps its own payer after the switch: "+repoB) ||
		strings.Contains(joined(lines), "keeps its own payer after the switch: "+repoA) {
		t.Errorf("wrong repo named as keeping its payer\n%s", joined(lines))
	}
	if !strings.Contains(joined(lines), "swarmery account switch insart --estate "+root+" --clear-pins") {
		t.Errorf("the clearing command is not printed\n%s", joined(lines))
	}

	// Round trip with --clear-pins (the estate is on default now): insart,
	// default, insart — every run changes the payer, so 0 cleared each time
	// and neither pin moves.
	shaA := sha(t, fileA)
	for _, key := range []string{"insart", "default", "insart"} {
		rep, err := Switch(SwitchOptions{Key: key, Estate: root, Force: true, ClearPins: true, DBPath: noDB(t)})
		if err != nil {
			t.Fatal(err)
		}
		if rep.OldAccount == key {
			t.Fatalf("run to %s did not change the payer", key)
		}
		if len(rep.Cleared) != 0 || !strings.Contains(joined(rep.Lines()), "0 cleared") {
			t.Errorf("account-changing --clear-pins cleared %v", rep.Cleared)
		}
		if sha(t, fileB) != shaB || sha(t, fileA) != shaA {
			t.Fatal("a pin changed on an account-changing run")
		}
		if claudeacct.Resolve(repoB).Account != "default" {
			t.Fatal("repo-b lost its override")
		}
	}
	// Standing still (the estate already carries insart) does sweep.
	rep, err = Switch(SwitchOptions{Key: "insart", Estate: root, Force: true, ClearPins: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	if claudeacct.Binding(repoA) != "" {
		t.Errorf("standing still did not sweep repo-a (cleared %v)", rep.Cleared)
	}
	r := claudeacct.Resolve(repoA)
	if r.Account != "insart" || r.Source != claudeacct.SourcePinParent {
		t.Errorf("repo-a resolves %s/%s, want insart/pin(parent)", r.Account, r.Source)
	}
	if claudeacct.Resolve(repoB).Account != "default" || sha(t, fileB) != shaB {
		t.Error("repo-b lost its override")
	}
	if len(rep.Cleared) != 1 || rep.Cleared[0] != repoA || !strings.Contains(joined(rep.Lines()), "1 cleared") {
		t.Errorf("standing-still report: cleared %v\n%s", rep.Cleared, joined(rep.Lines()))
	}
}

func TestSwitchDryRunWritesNothing(t *testing.T) {
	home, _ := fakeHome(t)
	root, file := newEstate(t, home)
	fileA := writeBinding(t, filepath.Join(root, "repo-a"), `{"swarmery":{"claudeAccount":"insart"}}`)
	before, beforeA := sha(t, file), sha(t, fileA)
	rep, err := Switch(SwitchOptions{Key: "insart", Estate: root, Force: true, ClearPins: true, DryRun: true, DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	if sha(t, file) != before || sha(t, fileA) != beforeA || exists_(file+".bak") {
		t.Error("a dry run wrote")
	}
	out := joined(rep.Lines())
	if !strings.Contains(out, "dry run:     nothing written") || !strings.Contains(out, "0 cleared (dry run; would clear 1)") {
		t.Errorf("output:\n%s", out)
	}
}

func TestClearPinsRefusesDisagreeing(t *testing.T) {
	home, _ := fakeHome(t)
	a := filepath.Join(home, "a")
	b := filepath.Join(home, "b")
	fileA := writeBinding(t, a, `{"swarmery":{"claudeAccount":"insart"}}`)
	fileB := writeBinding(t, b, `{"swarmery":{"claudeAccount":"default"}}`)
	shaA, shaB := sha(t, fileA), sha(t, fileB)
	err := ClearPins([]Pin{{Dir: a, Account: "insart", Redundant: true}, {Dir: b, Account: "default", Redundant: false}})
	if err == nil || !strings.Contains(err.Error(), b) {
		t.Fatalf("err = %v, want a refusal naming %s", err, b)
	}
	if sha(t, fileA) != shaA || sha(t, fileB) != shaB {
		t.Error("ClearPins wrote before refusing")
	}
	if err := ClearPins([]Pin{{Dir: a, Account: "insart", Redundant: true}}); err != nil {
		t.Fatal(err)
	}
	if claudeacct.Binding(a) != "" {
		t.Error("a redundant pin was not cleared")
	}
}

func TestAccountConfigDir(t *testing.T) {
	home, _ := fakeHome(t)
	if d, ok := AccountConfigDir("insart"); !ok || d != filepath.Join(home, ".claude-insart") {
		t.Errorf("insart = %s %v", d, ok)
	}
	if d, ok := AccountConfigDir("nosuch"); ok || d != filepath.Join(home, ".claude-nosuch") {
		t.Errorf("nosuch = %s %v", d, ok)
	}
	if _, ok := AccountConfigDir("../bad"); ok {
		t.Error("an invalid key resolved")
	}
}
