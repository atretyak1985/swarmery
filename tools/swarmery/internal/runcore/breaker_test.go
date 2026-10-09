package runcore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

var (
	probeReady   = claudeprobe.Result{Status: claudeprobe.StatusReady}
	probeNoLogin = claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonAccessRefused}
	probeLimited = claudeprobe.Result{Status: claudeprobe.StatusLimited, Reason: claudeprobe.ReasonRateLimited}
	probeUnknown = claudeprobe.Result{Status: claudeprobe.StatusUnknown, Reason: claudeprobe.ReasonTimeout}
)

// breakerNow is the tests' clock: the gate takes `now` as a parameter, so no
// test sleeps.
var breakerNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func work() claudeacct.Resolution {
	return claudeacct.Resolution{Account: "work"}
}

// noConfigDir is what the helpers below report for an environment that carries
// no CLAUDE_CONFIG_DIR at all — absence, which is what selects ~/.claude.
const noConfigDir = "(none)"

// configDirIn is the CLAUDE_CONFIG_DIR an environment selects: the FIRST
// assignment, as getenv resolves it, or noConfigDir.
func configDirIn(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			return v
		}
	}
	return noConfigDir
}

// stubProbe is the pre-flight probe seam: it counts its calls, records the
// config dir of the environment it was handed and answers with a fixed result.
// The tests built on it never reach a `claude`, real or fake.
type stubProbe struct {
	calls   atomic.Int32
	result  claudeprobe.Result
	mu      sync.Mutex
	dirs    []string
	started chan struct{} // closed on the first call, when set
	release chan struct{} // the call blocks on it, when set
	once    sync.Once
}

func (p *stubProbe) probe(_ context.Context, env []string) claudeprobe.Result {
	p.calls.Add(1)
	p.mu.Lock()
	p.dirs = append(p.dirs, configDirIn(env))
	p.mu.Unlock()
	if p.started != nil {
		p.once.Do(func() { close(p.started) })
	}
	if p.release != nil {
		<-p.release
	}
	return p.result
}

// hermeticHome gives the test its own machine: a fresh HOME (no accounts, no
// System project), an empty secret store dir, and no CLAUDE_CONFIG_DIR in the
// test process — RunEnv composes onto os.Environ(), so whatever the developer's
// shell exports would otherwise leak into every assertion. Returns HOME.
func hermeticHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	secrets := t.TempDir()
	if err := os.Chmod(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", secrets)
	if prev, had := os.LookupEnv("CLAUDE_CONFIG_DIR"); had {
		if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Setenv("CLAUDE_CONFIG_DIR", prev) })
	}
	return home
}

// accountDir is the config dir a named account's run gets under home.
func accountDir(home, account string) string {
	return filepath.Join(home, ".claude-"+account)
}

// installProbe points the pre-flight at p (nil switches it off) on a hermetic
// machine, with the package's memo cleared and the env knobs pinned, and
// restores all of it. Returns the test's HOME.
func installProbe(t *testing.T, p AccountProbeFunc) string {
	t.Helper()
	home := hermeticHome(t)
	t.Setenv(PreflightTTLEnv, "")
	t.Setenv(PreflightPingEnv, "")
	prev := SetPreflightProbe(p)
	clearMemo := func() {
		preflight.mu.Lock()
		preflight.unknownAt = map[string]time.Time{}
		preflight.mu.Unlock()
	}
	clearMemo()
	t.Cleanup(func() {
		SetPreflightProbe(prev)
		clearMemo()
	})
	return home
}

func breakerRow(t *testing.T, db *sql.DB, account string) (store.AccountBreaker, bool) {
	t.Helper()
	b, ok, err := store.GetAccountBreaker(db, account)
	if err != nil {
		t.Fatalf("get breaker %s: %v", account, err)
	}
	return b, ok
}

func openAlerts(t *testing.T, db *sql.DB, account string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NULL`,
		store.AccountBreakerTarget(account), store.AccountBreakerRule).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestBreakerBlocksSameAccount: one opening refuses every later admission on
// that account, as itself — the evidence travels on the error — and the refusal
// spends nothing: no probe runs while the breaker is open.
func TestBreakerBlocksSameAccount(t *testing.T) {
	db := quotaDB(t)
	p := &stubProbe{result: probeReady}
	installProbe(t, p.probe)

	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonAccessRefused,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	if n := openAlerts(t, db, "work"); n != 1 {
		t.Errorf("open alerts = %d, want 1 — an opening must surface", n)
	}

	for i := 0; i < 3; i++ {
		err := CheckAccount(context.Background(), db, work(), breakerNow.Add(time.Duration(i)*time.Minute))
		if !errors.Is(err, ErrAccountBreaker) {
			t.Fatalf("admission %d: err = %v, want ErrAccountBreaker", i, err)
		}
		var refused *AccountBreakerError
		if !errors.As(err, &refused) {
			t.Fatalf("admission %d: refusal is not an *AccountBreakerError: %v", i, err)
		}
		want := AccountBreakerError{Account: "work", Kind: "auth", Reason: claudeprobe.ReasonAccessRefused,
			OpenedAt: "2026-09-30T12:00:00Z"}
		if *refused != want {
			t.Errorf("refusal = %+v, want %+v", *refused, want)
		}
		if errors.Is(err, ErrLowQuota) || errors.Is(err, ErrNoSlot) {
			t.Error("the breaker refusal matches another sentinel")
		}
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("probe ran %d times against an open breaker, want 0", n)
	}
	const wantText = "account work is paused (auth): Claude refused this account's access, since 2026-09-30T12:00:00Z"
	if got := CheckAccount(context.Background(), db, work(), breakerNow).Error(); got != wantText {
		t.Errorf("Error() = %q, want %q", got, wantText)
	}

	// The unbound project and an explicit default both map to the poller's key.
	if err := OpenBreaker(db, "default", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceTranscript, breakerNow); err != nil {
		t.Fatal(err)
	}
	for _, res := range []claudeacct.Resolution{{}, {Account: "default", DefaultProfile: true}} {
		if err := CheckAccount(context.Background(), db, res, breakerNow); !errors.Is(err, ErrAccountBreaker) {
			t.Errorf("resolution %+v: err = %v, want the default account's breaker", res, err)
		}
	}
}

// TestBreakerAdmitsOtherAccount: a breaker is per account. While "work" is
// paused, another account is admitted — through its own pre-flight, which is
// asked about ITS config dir — and its verdict is stored.
func TestBreakerAdmitsOtherAccount(t *testing.T) {
	db := quotaDB(t)
	p := &stubProbe{result: probeReady}
	home := installProbe(t, p.probe)
	if err := OpenBreaker(db, "work", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}

	other := claudeacct.Resolution{Account: "other"}
	if err := CheckAccount(context.Background(), db, other, breakerNow); err != nil {
		t.Fatalf("other account refused: %v", err)
	}
	if err := CheckAccount(context.Background(), db, work(), breakerNow); !errors.Is(err, ErrAccountBreaker) {
		t.Fatalf("paused account admitted: %v", err)
	}
	if _, ok := breakerRow(t, db, "other"); ok {
		t.Error("admitting the other account wrote it a breaker row")
	}
	if n := p.calls.Load(); n != 1 || p.dirs[0] != accountDir(home, "other") {
		t.Errorf("probe calls = %d dirs = %v, want one call under %s", n, p.dirs, accountDir(home, "other"))
	}
	v, ok, err := store.GetAccountRunnable(db, "other")
	if err != nil || !ok || v.Status != "ready" || v.Source != preflightSource || !v.CheckedAt.Equal(breakerNow) {
		t.Errorf("stored verdict = %+v ok=%v err=%v, want ready/preflight at now", v, ok, err)
	}

	// Inside the TTL the stored verdict vouches for the account: no second probe.
	if err := CheckAccount(context.Background(), db, other, breakerNow.Add(14*time.Minute)); err != nil {
		t.Fatalf("admission inside the TTL: %v", err)
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("probe calls = %d inside the TTL, want still 1", n)
	}
	// Past it, the account is checked again.
	if err := CheckAccount(context.Background(), db, other, breakerNow.Add(15*time.Minute)); err != nil {
		t.Fatalf("admission past the TTL: %v", err)
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("probe calls = %d past the TTL, want 2", n)
	}
}

// probeEnvScript is a fake `claude` that answers both probe stages as a healthy
// account and appends one line per invocation to `seen`: the stage (its first
// argument), the config dir and the store variable IT saw, its working
// directory, and its whole argv. Observing the child — not the env slice the
// gate built — is the point: the claim is about what the CLI actually runs
// under.
func probeEnvScript(t *testing.T) (seen string) {
	t.Helper()
	seen = filepath.Join(t.TempDir(), "seen")
	script := filepath.Join(t.TempDir(), "claude")
	body := `#!/bin/sh
printf '%s cfg=%s store=%s dir=%s argv=%s\n' "$1" "${CLAUDE_CONFIG_DIR-` + noConfigDir + `}" "${` + secretVar + `-` + absentMarker + `}" "$(pwd -P)" "$*" >> ` + seen + `
if [ "$1" = auth ]; then printf '{"loggedIn": true}\n'; exit 0; fi
echo OK
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)
	return seen
}

// probeLines reads and clears what the fake `claude` recorded.
func probeLines(t *testing.T, seen string) []string {
	t.Helper()
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the probe never reached the CLI: %v", err)
	}
	if err := os.Remove(seen); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// wantStages asserts both probe stages ran, each under cfg and store.
func wantStages(t *testing.T, lines []string, cfg, storeValue string) {
	t.Helper()
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "auth ") || !strings.HasPrefix(lines[1], "-p ") {
		t.Fatalf("stages = %q, want `auth status` then the ping", lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, " cfg="+cfg+" ") {
			t.Errorf("stage ran under the wrong account, want cfg=%s:\n  %s", cfg, line)
		}
		if !strings.Contains(line, " store="+storeValue+" ") {
			t.Errorf("stage saw the wrong store, want store=%s:\n  %s", storeValue, line)
		}
	}
}

// TestPreflightProbesTheRunsOwnEnvironment is the parity claim, observed in the
// CHILD the production probe spawns: both stages run under the environment the
// run being admitted gets — composed by the function its spawn uses — and not
// under an account the gate guessed from the breaker key.
func TestPreflightProbesTheRunsOwnEnvironment(t *testing.T) {
	db := quotaDB(t)
	home := installProbe(t, ProbeAccount)
	seen := probeEnvScript(t)

	// A store for the named account, and the account itself (a rootless store is
	// released only to a real account on this machine — a config dir with projects/).
	seedSecretStoreNamed(t, "work")
	if err := os.MkdirAll(filepath.Join(accountDir(home, "work"), "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A System project, so the ping has the utility-run cwd to start in.
	system := filepath.Join(home, ".swarmery")
	if err := os.MkdirAll(system, 0o755); err != nil {
		t.Fatal(err)
	}
	systemResolved, err := filepath.EvalSymlinks(system)
	if err != nil {
		t.Fatal(err)
	}
	inherited := filepath.Join(t.TempDir(), "daemon-config-dir")
	admit := func(t *testing.T, res claudeacct.Resolution, at time.Time) []string {
		t.Helper()
		if err := CheckAccount(context.Background(), db, res, at); err != nil {
			t.Fatalf("resolution %+v refused by a healthy stub: %v", res, err)
		}
		return probeLines(t, seen)
	}

	t.Run("unbound project keeps the daemon's inherited config dir", func(t *testing.T) {
		// `swarmery install --claude-config-dir`: the daemon itself runs under a
		// non-default dir, and an unbound project's run inherits it untouched.
		t.Setenv("CLAUDE_CONFIG_DIR", inherited)
		lines := admit(t, claudeacct.Resolution{}, breakerNow)
		wantStages(t, lines, inherited, absentMarker)
		// The verdict is filed under the key the gate reads for such a run.
		if v, ok, err := store.GetAccountRunnable(db, "default"); err != nil || !ok || v.Status != "ready" {
			t.Errorf("stored verdict = %+v ok=%v err=%v, want ready under the default key", v, ok, err)
		}
	})

	t.Run("project bound to the default account drops it", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", inherited)
		lines := admit(t, claudeacct.Resolution{Account: "default", DefaultProfile: true}, breakerNow.Add(time.Hour))
		wantStages(t, lines, noConfigDir, absentMarker)
	})

	t.Run("unbound project with nothing inherited runs on ~/.claude", func(t *testing.T) {
		lines := admit(t, claudeacct.Resolution{}, breakerNow.Add(2*time.Hour))
		wantStages(t, lines, noConfigDir, absentMarker)
	})

	t.Run("named account gets its own dir and its secret store", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", inherited) // must be replaced, not kept
		lines := admit(t, work(), breakerNow)
		wantStages(t, lines, accountDir(home, "work"), secretValue)

		// The ping starts in the System project's directory and carries NOTHING of
		// that project's: its argv is the fixed one, with no --settings spliced in.
		ping := lines[1]
		if !strings.Contains(ping, " dir="+systemResolved+" ") {
			t.Errorf("ping ran outside the System project dir %s:\n  %s", systemResolved, ping)
		}
		wantArgv := "argv=-p " + claudeprobe.PingPrompt + " --model " + claudeprobe.PingModel +
			" --effort " + claudeprobe.PingEffort + " --max-turns 1 --no-session-persistence"
		if !strings.HasSuffix(ping, wantArgv) || strings.Contains(ping, "--settings") {
			t.Errorf("ping argv is not the fixed one:\n  %s\nwant suffix %q", ping, wantArgv)
		}
	})

	t.Run("another account never sees that store", func(t *testing.T) {
		lines := admit(t, claudeacct.Resolution{Account: "other"}, breakerNow)
		wantStages(t, lines, accountDir(home, "other"), absentMarker)
	})
}

// TestResumeProbesTheBreakersOwnEnvironment: "Probe & resume" checks the account
// the STOPPED runs use. For the default key that is the unbound environment —
// the daemon's inherited config dir kept, not ~/.claude — and for a named key
// its own dir with its store.
func TestResumeProbesTheBreakersOwnEnvironment(t *testing.T) {
	db := quotaDB(t)
	home := installProbe(t, nil)
	seen := probeEnvScript(t)
	seedSecretStoreNamed(t, "work")
	if err := os.MkdirAll(filepath.Join(accountDir(home, "work"), "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	inherited := filepath.Join(t.TempDir(), "daemon-config-dir")
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)

	for _, account := range []string{"default", "work"} {
		if err := OpenBreaker(db, account, store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
			store.BreakerSourceRun, breakerNow); err != nil {
			t.Fatal(err)
		}
	}
	resume := func(account string) []string {
		t.Helper()
		// nil probe: the production two-stage probe, against the fake CLI.
		got, err := ResumeBreaker(context.Background(), db, account, nil, breakerNow.Add(time.Minute))
		if err != nil || got.Status != claudeprobe.StatusReady {
			t.Fatalf("resume %s = %+v, %v; want ready", account, got, err)
		}
		if b, _ := breakerRow(t, db, account); b.IsOpen() {
			t.Errorf("breaker %s still open after a ready resume", account)
		}
		return probeLines(t, seen)
	}

	wantStages(t, resume("default"), inherited, absentMarker)
	wantStages(t, resume("work"), accountDir(home, "work"), secretValue)

	// The helper itself, for the spellings a caller may hand it.
	for _, key := range []string{"", " default "} {
		if got := configDirIn(AccountEnv(key)); got != inherited {
			t.Errorf("AccountEnv(%q) selects %q, want the inherited %q", key, got, inherited)
		}
	}
	if got := configDirIn(AccountEnv("other")); got != accountDir(home, "other") {
		t.Errorf("AccountEnv(other) selects %q, want its own dir", got)
	}
}

// TestPreflightPanickingProbeFailsOpen: a probe that panics has answered
// nothing. The admission that ran it is admitted (unknown fails open), its
// waiters are released rather than blocked for ever, nothing is stored, and the
// next admission is not wedged behind a leaked flight.
func TestPreflightPanickingProbeFailsOpen(t *testing.T) {
	db := quotaDB(t)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	installProbe(t, func(context.Context, []string) claudeprobe.Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		panic("probe blew up")
	})
	joined := make(chan struct{}, 8)
	preflight.mu.Lock()
	preflight.onWait = func(string) { joined <- struct{}{} }
	preflight.mu.Unlock()
	t.Cleanup(func() {
		preflight.mu.Lock()
		preflight.onWait = nil
		preflight.mu.Unlock()
	})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// A leader inside the probe, and one waiter blocked on its flight.
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { errs <- CheckAccount(context.Background(), db, work(), breakerNow) }()
	}
	<-started
	<-joined
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("admission %d: %v, want admitted — a panicking probe is unknown", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("an admission is still blocked after the probe panicked")
		}
	}
	if !strings.Contains(buf.String(), "panicked") || !strings.Contains(buf.String(), "could not answer") {
		t.Errorf("the panic was not reported as an unanswered probe:\n%s", buf.String())
	}
	if _, ok := breakerRow(t, db, "work"); ok {
		t.Error("a panicking probe opened the breaker")
	}
	preflight.mu.Lock()
	leaked := len(preflight.flights)
	preflight.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("flights left in the map = %d, want 0", leaked)
	}

	// The next admission is answered at once from the unknown memo…
	done := make(chan error, 1)
	go func() { done <- CheckAccount(context.Background(), db, work(), breakerNow.Add(time.Minute)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("next admission: %v, want admitted", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the next admission is wedged behind the panicked flight")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("probe calls = %d inside the TTL, want 1", n)
	}
	// …and a TTL later the probe is tried again, panics again, and still admits.
	if err := CheckAccount(context.Background(), db, work(), breakerNow.Add(16*time.Minute)); err != nil {
		t.Errorf("admission after the TTL: %v, want admitted", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("probe calls = %d after the TTL, want 2", n)
	}

	// "Probe & resume" with a panicking probe changes nothing and reports unknown.
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	got, err := ResumeBreaker(context.Background(), db, "work",
		func(context.Context, []string) claudeprobe.Result { panic("resume probe blew up") }, breakerNow)
	if err != nil || got.Status != claudeprobe.StatusUnknown {
		t.Errorf("resume with a panicking probe = %+v, %v; want unknown", got, err)
	}
	if b, _ := breakerRow(t, db, "work"); !b.IsOpen() {
		t.Error("a panicking resume probe closed the breaker")
	}
}

// TestPreflightDoReleasesWaitersOnPanic: a panic ANYWHERE in the flight — not
// only in the probe — still tears the flight down, so its waiters return and
// the account's next flight can start. The panic reaches the leader's caller.
func TestPreflightDoReleasesWaitersOnPanic(t *testing.T) {
	installProbe(t, (&stubProbe{result: probeReady}).probe)
	joined := make(chan struct{}, 1)
	preflight.mu.Lock()
	preflight.onWait = func(string) { joined <- struct{}{} }
	preflight.mu.Unlock()
	t.Cleanup(func() {
		preflight.mu.Lock()
		preflight.onWait = nil
		preflight.mu.Unlock()
	})

	entered, release := make(chan struct{}), make(chan struct{})
	leader := make(chan any, 1)
	go func() {
		defer func() { leader <- recover() }()
		_ = preflightDo("work", func(AccountProbeFunc) error {
			close(entered)
			<-release
			panic("flight blew up")
		})
	}()
	<-entered
	waiter := make(chan error, 1)
	go func() {
		waiter <- preflightDo("work", func(AccountProbeFunc) error {
			t.Error("the waiter ran its own flight")
			return nil
		})
	}()
	<-joined
	close(release)

	select {
	case err := <-waiter:
		if err != nil {
			t.Errorf("waiter returned %v, want nil — a flight with no result admits", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter is blocked for ever behind a panicked flight")
	}
	if p := <-leader; p == nil {
		t.Error("the panic did not reach the leader's caller")
	}
	ran := false
	if err := preflightDo("work", func(AccountProbeFunc) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("the next flight did not run (ran=%v err=%v)", ran, err)
	}
}

// TestPreflightSingleFlight: five concurrent admissions of one account make
// exactly ONE probe call, and a negative result admits none of them. The same
// five against a healthy account make one call and admit all five.
func TestPreflightSingleFlight(t *testing.T) {
	const admissions = 5
	run := func(t *testing.T, result claudeprobe.Result) (admitted, refused int, p *stubProbe, db *sql.DB) {
		t.Helper()
		db = quotaDB(t)
		p = &stubProbe{result: result, started: make(chan struct{}), release: make(chan struct{})}
		installProbe(t, p.probe)
		// Every caller that finds the flight in progress announces itself here just
		// before it blocks on it — the gate that makes the waiter branch certain.
		joined := make(chan struct{}, admissions)
		preflight.mu.Lock()
		preflight.onWait = func(string) { joined <- struct{}{} }
		preflight.mu.Unlock()
		t.Cleanup(func() {
			preflight.mu.Lock()
			preflight.onWait = nil
			preflight.mu.Unlock()
		})

		errs := make(chan error, admissions)
		var wg sync.WaitGroup
		for i := 0; i < admissions; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- CheckAccount(context.Background(), db, work(), breakerNow)
			}()
		}
		// The probe does not answer until the leader is inside it AND the other
		// four have all joined its flight: nothing is stored while it blocks, so
		// each of them passes the fast checks and must wait — no sleep, no luck.
		deadline := time.After(10 * time.Second)
		select {
		case <-p.started:
		case <-deadline:
			t.Fatal("no admission reached the probe")
		}
		for w := 0; w < admissions-1; w++ {
			select {
			case <-joined:
			case <-deadline:
				t.Fatalf("only %d of %d admissions joined the leader's flight", w, admissions-1)
			}
		}
		close(p.release)
		wg.Wait()
		select {
		case <-joined:
			t.Error("more waiters joined than there were admissions behind the leader")
		default:
		}
		close(errs)
		for err := range errs {
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, ErrAccountBreaker):
				refused++
			default:
				t.Errorf("unexpected admission error: %v", err)
			}
		}
		return admitted, refused, p, db
	}

	t.Run("negative result admits none", func(t *testing.T) {
		admitted, refused, p, db := run(t, probeNoLogin)
		if n := p.calls.Load(); n != 1 {
			t.Errorf("probe calls = %d, want exactly 1 for %d concurrent admissions", n, admissions)
		}
		if admitted != 0 || refused != admissions {
			t.Errorf("admitted = %d refused = %d, want 0 and %d", admitted, refused, admissions)
		}
		b, ok := breakerRow(t, db, "work")
		if !ok || !b.IsOpen() || b.Kind != store.BreakerKindAuth || b.Source != store.BreakerSourceProbe ||
			b.Reason != claudeprobe.ReasonAccessRefused {
			t.Errorf("breaker = %+v ok=%v, want an open auth breaker opened by the probe", b, ok)
		}
		if n := openAlerts(t, db, "work"); n != 1 {
			t.Errorf("open alerts = %d, want 1", n)
		}
		// A sixth admission afterwards is refused from the stored state alone.
		if err := CheckAccount(context.Background(), db, work(), breakerNow); !errors.Is(err, ErrAccountBreaker) {
			t.Errorf("later admission: %v, want the open breaker", err)
		}
		if n := p.calls.Load(); n != 1 {
			t.Errorf("probe calls = %d after a later admission, want still 1", n)
		}
	})

	t.Run("usage limit admits none and carries a reset", func(t *testing.T) {
		admitted, refused, p, db := run(t, probeLimited)
		if n := p.calls.Load(); n != 1 || admitted != 0 || refused != admissions {
			t.Errorf("calls = %d admitted = %d refused = %d, want 1/0/%d", n, admitted, refused, admissions)
		}
		b, _ := breakerRow(t, db, "work")
		if b.Kind != store.BreakerKindQuota || b.ResetsAt != "2026-09-30T13:00:00Z" {
			t.Errorf("breaker = %+v, want a quota breaker resetting at now + 1h", b)
		}
		// A limit is not a login verdict: account_runnable is left alone.
		if _, ok, _ := store.GetAccountRunnable(db, "work"); ok {
			t.Error("a usage limit was stored as a runnable verdict")
		}
	})

	t.Run("healthy account admits all", func(t *testing.T) {
		admitted, refused, p, db := run(t, probeReady)
		if n := p.calls.Load(); n != 1 {
			t.Errorf("probe calls = %d, want exactly 1", n)
		}
		if admitted != admissions || refused != 0 {
			t.Errorf("admitted = %d refused = %d, want %d and 0", admitted, refused, admissions)
		}
		if _, ok := breakerRow(t, db, "work"); ok {
			t.Error("a ready probe wrote a breaker row")
		}
	})
}

// TestPreflightUnknownFailsOpen: a probe that cannot answer — a timeout, no
// binary — admits, opens nothing, is said once, and is not retried until a TTL
// has passed. Every other flavour of "unknown" admits too.
func TestPreflightUnknownFailsOpen(t *testing.T) {
	db := quotaDB(t)
	p := &stubProbe{result: probeUnknown}
	installProbe(t, p.probe)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	for i := 0; i < 3; i++ {
		if err := CheckAccount(context.Background(), db, work(), breakerNow.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("admission %d refused on an unknown probe: %v", i, err)
		}
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("probe calls = %d inside one TTL, want 1", n)
	}
	if n := strings.Count(buf.String(), "could not answer"); n != 1 {
		t.Errorf("logged %d times, want once per account per TTL:\n%s", n, buf.String())
	}
	if _, ok := breakerRow(t, db, "work"); ok {
		t.Error("an unknown probe opened the breaker")
	}
	if _, ok, _ := store.GetAccountRunnable(db, "work"); ok {
		t.Error("an unknown probe was stored as a verdict — it would erase a good one")
	}

	// A TTL later the account is checked again; a ready answer clears the memo.
	p.result = probeReady
	if err := CheckAccount(context.Background(), db, work(), breakerNow.Add(16*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("probe calls = %d after the TTL, want 2", n)
	}

	t.Run("no probe installed", func(t *testing.T) {
		installProbe(t, nil)
		if err := CheckAccount(context.Background(), quotaDB(t), work(), breakerNow); err != nil {
			t.Errorf("refused with no probe installed: %v", err)
		}
	})
	t.Run("TTL 0 disables the pre-flight", func(t *testing.T) {
		q := &stubProbe{result: probeNoLogin}
		installProbe(t, q.probe)
		t.Setenv(PreflightTTLEnv, "0")
		if err := CheckAccount(context.Background(), quotaDB(t), work(), breakerNow); err != nil {
			t.Errorf("refused with the pre-flight off: %v", err)
		}
		if n := q.calls.Load(); n != 0 {
			t.Errorf("probe calls = %d with TTL 0, want 0", n)
		}
	})
	t.Run("nil db", func(t *testing.T) {
		if err := CheckAccount(context.Background(), nil, work(), breakerNow); err != nil {
			t.Errorf("nil db refused: %v", err)
		}
	})
	t.Run("a database that predates the breaker migration", func(t *testing.T) {
		q := &stubProbe{result: probeNoLogin}
		installProbe(t, q.probe)
		raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "old.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { raw.Close() })
		if err := CheckAccount(context.Background(), raw, work(), breakerNow); err != nil {
			t.Errorf("an unreadable store refused: %v", err)
		}
		if n := q.calls.Load(); n != 0 {
			t.Errorf("probe calls = %d against an unreadable store, want 0", n)
		}
	})
}

// TestQuotaBreakerAutoCloses: a quota opening refuses until its reset time and
// then closes itself ('reset') on the next admission, alert and all. With no
// stored window it resets an hour after opening; with one, at that window's
// reset.
func TestQuotaBreakerAutoCloses(t *testing.T) {
	db := quotaDB(t)
	installProbe(t, nil)
	if err := store.PutAccountQuota(db, "work", []store.QuotaRow{
		{WindowKey: "five_hour", Label: "Session (5h)", PercentLeft: 0, ResetsAt: "2026-09-30T14:30:00Z"},
		{WindowKey: "seven_day", Label: "Weekly", PercentLeft: 55, ResetsAt: "2026-10-05T00:00:00Z"},
	}, breakerNow.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := OpenBreaker(db, "work", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}

	err := CheckAccount(context.Background(), db, work(), breakerNow.Add(2*time.Hour))
	var refused *AccountBreakerError
	if !errors.As(err, &refused) {
		t.Fatalf("before the reset: err = %v, want a refusal", err)
	}
	if refused.Kind != store.BreakerKindQuota || refused.ResetsAt != "2026-09-30T14:30:00Z" {
		t.Errorf("refusal = %+v, want the earliest reset ahead (the scope is unknown on the run path)", *refused)
	}
	if !strings.HasSuffix(refused.Error(), ", resets 2026-09-30T14:30:00Z") {
		t.Errorf("Error() = %q, want the reset time in it", refused.Error())
	}

	// At the reset: closed, admitted, alert resolved.
	if err := CheckAccount(context.Background(), db, work(), breakerNow.Add(150*time.Minute)); err != nil {
		t.Fatalf("at the reset: %v, want admitted", err)
	}
	b, _ := breakerRow(t, db, "work")
	if b.IsOpen() || b.ClosedBy != store.BreakerClosedByReset || b.ClosedAt != "2026-09-30T14:30:00Z" {
		t.Errorf("breaker = %+v, want closed by reset at the admission's time", b)
	}
	if n := openAlerts(t, db, "work"); n != 0 {
		t.Errorf("open alerts = %d after the reset, want 0", n)
	}

	// No stored window: the fallback hour.
	if err := OpenBreaker(db, "bare", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceTranscript, breakerNow); err != nil {
		t.Fatal(err)
	}
	bare := claudeacct.Resolution{Account: "bare"}
	if err := CheckAccount(context.Background(), db, bare, breakerNow.Add(59*time.Minute)); !errors.Is(err, ErrAccountBreaker) {
		t.Errorf("59 minutes in: %v, want still refused", err)
	}
	if err := CheckAccount(context.Background(), db, bare, breakerNow.Add(time.Hour)); err != nil {
		t.Errorf("an hour in: %v, want admitted", err)
	}
}

// TestCloseExpiredBreakers: a quota opening whose reset has passed is closed
// ('reset') and its alert resolved WITHOUT an admission — an idle account must not
// stay "paused" on the Inbox for hours after its limit reset. An opening still
// before its reset, and an auth opening (which never closes on time), stay open.
func TestCloseExpiredBreakers(t *testing.T) {
	db := quotaDB(t)
	// No stored quota window: each quota opening resets an hour after it opened.
	for _, o := range []struct {
		account, kind string
		at            time.Time
	}{
		{"expired", store.BreakerKindQuota, breakerNow},
		{"pending", store.BreakerKindQuota, breakerNow.Add(30 * time.Minute)},
		{"login", store.BreakerKindAuth, breakerNow},
	} {
		if err := OpenBreaker(db, o.account, o.kind, claudeprobe.ReasonRateLimited, store.BreakerSourceRun, o.at); err != nil {
			t.Fatal(err)
		}
	}

	closed, err := CloseExpiredBreakers(db, breakerNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("CloseExpiredBreakers: %v", err)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1", closed)
	}
	b, _ := breakerRow(t, db, "expired")
	if b.IsOpen() || b.ClosedBy != store.BreakerClosedByReset || b.ClosedAt != "2026-09-30T13:00:00Z" {
		t.Errorf("expired breaker = %+v, want closed by reset at 13:00", b)
	}
	if n := openAlerts(t, db, "expired"); n != 0 {
		t.Errorf("open alerts for the reset account = %d, want 0", n)
	}
	for _, account := range []string{"pending", "login"} {
		if b, _ := breakerRow(t, db, account); !b.IsOpen() {
			t.Errorf("%s breaker = %+v, want still open", account, b)
		}
		if n := openAlerts(t, db, account); n != 1 {
			t.Errorf("open alerts for %s = %d, want 1", account, n)
		}
	}
}

// TestBreakerTickerClosesOnItsClock: one tick sweeps as of the ticker's clock.
func TestBreakerTickerClosesOnItsClock(t *testing.T) {
	db := quotaDB(t)
	if err := OpenBreaker(db, "work", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	(&BreakerTicker{DB: db, Now: func() time.Time { return breakerNow.Add(59 * time.Minute) }}).Once()
	if b, _ := breakerRow(t, db, "work"); !b.IsOpen() {
		t.Fatalf("closed before its reset: %+v", b)
	}
	(&BreakerTicker{DB: db, Now: func() time.Time { return breakerNow.Add(time.Hour) }}).Once()
	if b, _ := breakerRow(t, db, "work"); b.IsOpen() {
		t.Errorf("still open at its reset: %+v", b)
	}
}

// reprobeTicker is a BreakerTicker whose re-probe of open auth breakers is on:
// stage one answers with login, the full probe with run, and the clock is
// whatever *now holds when a sweep starts.
func reprobeTicker(db *sql.DB, now *time.Time, login, run *stubProbe) *BreakerTicker {
	return &BreakerTicker{
		DB: db, Now: func() time.Time { return *now },
		LoginProbe: login.probe, RunProbe: run.probe,
	}
}

// TestBreakerTickerReprobesAuth: an auth opening closes on its own once the
// account can run again — the operator logged in again in a terminal, which
// nothing tells the daemon. The sweep asks stage one (free) once per
// DefaultAuthLoginGap and spends the full probe only when stage one says the
// login is back; a quota opening is never asked stage one, and its own full
// probe (TestBreakerTickerReprobesQuota) is not due inside this test's window.
func TestBreakerTickerReprobesAuth(t *testing.T) {
	db := quotaDB(t)
	home := installProbe(t, nil)
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceTranscript, breakerNow); err != nil {
		t.Fatal(err)
	}
	if err := OpenBreaker(db, "limited", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	login := &stubProbe{result: claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}}
	run := &stubProbe{result: probeReady}
	now := breakerNow
	ticker := reprobeTicker(db, &now, login, run)
	sweepAt := func(after time.Duration) {
		now = breakerNow.Add(after)
		ticker.Once()
	}
	wantCalls := func(when string, wantLogin, wantRun int32) {
		t.Helper()
		if l, r := login.calls.Load(), run.calls.Load(); l != wantLogin || r != wantRun {
			t.Fatalf("%s: login probes = %d, full probes = %d, want %d and %d", when, l, r, wantLogin, wantRun)
		}
	}

	sweepAt(DefaultAuthLoginGap - time.Second)
	wantCalls("before the first gap has passed", 0, 0)

	sweepAt(DefaultAuthLoginGap)
	wantCalls("one gap after the opening", 1, 0)
	if b, _ := breakerRow(t, db, "work"); !b.IsOpen() {
		t.Fatalf("closed while the login is still missing: %+v", b)
	}

	sweepAt(DefaultAuthLoginGap + time.Minute)
	wantCalls("inside the gap after a login probe", 1, 0)

	// The operator logs in again in a terminal.
	login.result = probeReady
	sweepAt(2 * DefaultAuthLoginGap)
	wantCalls("the login is back", 2, 1)
	for _, dir := range append(login.dirs, run.dirs...) {
		if dir != accountDir(home, "work") {
			t.Errorf("a re-probe ran under %s, want only the paused account's own dir %s", dir, accountDir(home, "work"))
		}
	}
	b, _ := breakerRow(t, db, "work")
	if b.IsOpen() || b.ClosedBy != store.BreakerClosedByProbe {
		t.Errorf("breaker = %+v, want closed by probe", b)
	}
	if n := openAlerts(t, db, "work"); n != 0 {
		t.Errorf("open alerts = %d, want 0", n)
	}
	if err := CheckAccount(context.Background(), db, work(), now); err != nil {
		t.Errorf("after the re-probe: %v, want admitted", err)
	}

	sweepAt(3 * DefaultAuthLoginGap)
	wantCalls("nothing auth is open any more", 2, 1)
}

// TestBreakerTickerReprobesQuota: a quota opening closes on its own once the
// account can run again BEFORE its reset time — the reset is a hint derived from
// the stored usage windows (or the fallback hour), and the real limit can lift
// earlier (seen 2026-10-09: lifted 35 minutes before the hint). There is no free
// stage one for a limit, so the sweep spends the full probe, once per
// DefaultQuotaPingGap counted from the opening; a refused ping leaves the opening
// — its opened_at and reset time included — exactly as it was.
func TestBreakerTickerReprobesQuota(t *testing.T) {
	db := quotaDB(t)
	home := installProbe(t, nil)
	if err := OpenBreaker(db, "work", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	opened, _ := breakerRow(t, db, "work")
	if opened.ResetsAt == "" {
		t.Fatalf("opening = %+v, want a reset hint", opened)
	}
	login := &stubProbe{result: probeReady}
	run := &stubProbe{result: probeLimited}
	now := breakerNow
	ticker := reprobeTicker(db, &now, login, run)
	sweepAt := func(after time.Duration) {
		now = breakerNow.Add(after)
		ticker.Once()
	}
	wantRuns := func(when string, want int32) {
		t.Helper()
		if l := login.calls.Load(); l != 0 {
			t.Fatalf("%s: login probes = %d, want none for a quota opening", when, l)
		}
		if r := run.calls.Load(); r != want {
			t.Fatalf("%s: full probes = %d, want %d", when, r, want)
		}
	}

	sweepAt(DefaultQuotaPingGap - time.Second)
	wantRuns("before the first gap has passed", 0)

	sweepAt(DefaultQuotaPingGap)
	wantRuns("one gap after the opening", 1)
	b, _ := breakerRow(t, db, "work")
	if !b.IsOpen() || b.Kind != store.BreakerKindQuota || b.OpenedAt != opened.OpenedAt || b.ResetsAt != opened.ResetsAt {
		t.Fatalf("breaker = %+v after a refused ping, want the opening untouched (%+v)", b, opened)
	}

	sweepAt(DefaultQuotaPingGap + time.Minute)
	wantRuns("inside the gap after a ping", 1)

	// The limit lifts before the hinted reset.
	run.result = probeReady
	sweepAt(2 * DefaultQuotaPingGap)
	wantRuns("the limit lifted", 2)
	for _, dir := range run.dirs {
		if dir != accountDir(home, "work") {
			t.Errorf("a re-probe ran under %s, want only the paused account's own dir %s", dir, accountDir(home, "work"))
		}
	}
	b, _ = breakerRow(t, db, "work")
	if b.IsOpen() || b.ClosedBy != store.BreakerClosedByProbe {
		t.Errorf("breaker = %+v, want closed by probe", b)
	}
	if n := openAlerts(t, db, "work"); n != 0 {
		t.Errorf("open alerts = %d, want 0", n)
	}
	if err := CheckAccount(context.Background(), db, work(), now); err != nil {
		t.Errorf("after the re-probe: %v, want admitted", err)
	}

	sweepAt(3 * DefaultQuotaPingGap)
	wantRuns("nothing is open any more", 2)

	// A new opening owes the old one's gap nothing: it is pinged one gap after
	// ITS opening, not after the last ping.
	reopened := breakerNow.Add(3*DefaultQuotaPingGap + time.Minute)
	if err := OpenBreaker(db, "work", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, reopened); err != nil {
		t.Fatal(err)
	}
	run.result = probeLimited
	now = reopened.Add(DefaultQuotaPingGap - time.Second)
	ticker.Once()
	wantRuns("a new opening, before its gap", 2)
	now = reopened.Add(DefaultQuotaPingGap)
	ticker.Once()
	wantRuns("a new opening, one gap in", 3)
}

// TestBreakerTickerReprobeBacksOffThePing: an account whose login is intact but
// which Claude refuses answers ready at stage one on every sweep. The full probe
// — a real API call — is then made at most once per DefaultAuthPingGap, and a
// stage one that cannot answer never reaches it. A NEW opening starts over.
func TestBreakerTickerReprobeBacksOffThePing(t *testing.T) {
	db := quotaDB(t)
	installProbe(t, nil)
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonAccessRefused,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	login := &stubProbe{result: probeUnknown}
	run := &stubProbe{result: probeNoLogin}
	now := breakerNow
	ticker := reprobeTicker(db, &now, login, run)
	sweepAt := func(at time.Time) {
		now = at
		ticker.Once()
	}
	wantCalls := func(when string, wantLogin, wantRun int32) {
		t.Helper()
		if l, r := login.calls.Load(), run.calls.Load(); l != wantLogin || r != wantRun {
			t.Fatalf("%s: login probes = %d, full probes = %d, want %d and %d", when, l, r, wantLogin, wantRun)
		}
	}

	first := breakerNow.Add(DefaultAuthLoginGap)
	sweepAt(first)
	wantCalls("stage one could not answer", 1, 0)

	login.result = probeReady
	pinged := first.Add(DefaultAuthLoginGap)
	sweepAt(pinged)
	wantCalls("stage one ready", 2, 1)
	if b, _ := breakerRow(t, db, "work"); !b.IsOpen() || b.Kind != store.BreakerKindAuth {
		t.Fatalf("breaker = %+v, want the auth opening kept after a refused ping", b)
	}

	sweepAt(pinged.Add(DefaultAuthLoginGap))
	wantCalls("inside the ping gap", 3, 1)

	sweepAt(pinged.Add(DefaultAuthPingGap))
	wantCalls("one ping gap later", 4, 2)

	// The breaker closes and opens again: the new opening owes the old one's
	// ping gap nothing.
	reopened := pinged.Add(DefaultAuthPingGap + time.Minute)
	if _, err := CloseBreaker(db, "work", store.BreakerClosedByOperator, reopened); err != nil {
		t.Fatal(err)
	}
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceRun, reopened); err != nil {
		t.Fatal(err)
	}
	sweepAt(reopened.Add(DefaultAuthLoginGap))
	wantCalls("a new opening", 5, 3)
}

// TestBreakerTickerReprobeOff: without both probes the sweep spawns nothing —
// the default, which keeps every test that builds a ticker away from the real
// CLI — and SWARMERY_PREFLIGHT_TTL=0 (the daemon probes nothing on its own)
// switches the re-probe off as well.
func TestBreakerTickerReprobeOff(t *testing.T) {
	db := quotaDB(t)
	installProbe(t, nil)
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	later := breakerNow.Add(24 * time.Hour)
	login, run := &stubProbe{result: probeReady}, &stubProbe{result: probeReady}

	for _, ticker := range []*BreakerTicker{
		{DB: db},
		{DB: db, LoginProbe: login.probe},
		{DB: db, RunProbe: run.probe},
	} {
		ticker.Now = func() time.Time { return later }
		ticker.Once()
	}
	t.Setenv(PreflightTTLEnv, "0")
	reprobeTicker(db, &later, login, run).Once()

	if l, r := login.calls.Load(), run.calls.Load(); l != 0 || r != 0 {
		t.Errorf("login probes = %d, full probes = %d, want none", l, r)
	}
	if b, _ := breakerRow(t, db, "work"); !b.IsOpen() {
		t.Errorf("breaker = %+v, want still open", b)
	}
}

// TestAuthBreakerNeedsReadyProbe: an auth opening never closes on time. A probe
// that is not ready leaves it open; only a ready one closes it ('probe') — and
// an API error never opens one at all.
func TestAuthBreakerNeedsReadyProbe(t *testing.T) {
	db := quotaDB(t)
	gate := &stubProbe{result: probeReady}
	home := installProbe(t, gate.probe)
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}

	// Days later it still refuses, and admission never probes it shut.
	later := breakerNow.Add(72 * time.Hour)
	if err := CheckAccount(context.Background(), db, work(), later); !errors.Is(err, ErrAccountBreaker) {
		t.Fatalf("three days later: %v, want still refused", err)
	}
	if n := gate.calls.Load(); n != 0 {
		t.Errorf("admission probed an open auth breaker %d times, want 0", n)
	}

	resume := func(r claudeprobe.Result) claudeprobe.Result {
		t.Helper()
		p := &stubProbe{result: r}
		got, err := ResumeBreaker(context.Background(), db, "work", p.probe, later)
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if p.calls.Load() != 1 || p.dirs[0] != accountDir(home, "work") {
			t.Fatalf("resume probe calls = %d dirs = %v, want one call under the account's own dir", p.calls.Load(), p.dirs)
		}
		return got
	}

	for _, notReady := range []claudeprobe.Result{probeUnknown, probeNoLogin} {
		if got := resume(notReady); got != notReady {
			t.Errorf("resume returned %+v, want the probe's %+v", got, notReady)
		}
		b, _ := breakerRow(t, db, "work")
		if !b.IsOpen() || b.Kind != store.BreakerKindAuth || b.OpenedAt != "2026-09-30T12:00:00Z" {
			t.Errorf("after a %s probe: breaker = %+v, want the same auth opening", notReady.Status, b)
		}
		if err := CheckAccount(context.Background(), db, work(), later); !errors.Is(err, ErrAccountBreaker) {
			t.Errorf("after a %s probe: admitted", notReady.Status)
		}
	}

	if got := resume(probeReady); got.Status != claudeprobe.StatusReady {
		t.Fatalf("resume = %+v", got)
	}
	b, _ := breakerRow(t, db, "work")
	if b.IsOpen() || b.ClosedBy != store.BreakerClosedByProbe {
		t.Errorf("breaker = %+v, want closed by probe", b)
	}
	if n := openAlerts(t, db, "work"); n != 0 {
		t.Errorf("open alerts = %d, want 0", n)
	}
	if err := CheckAccount(context.Background(), db, work(), later); err != nil {
		t.Errorf("after a ready probe: %v, want admitted", err)
	}
	// The resume's own verdict is fresh, so that admission needed no pre-flight.
	if n := gate.calls.Load(); n != 0 {
		t.Errorf("pre-flight ran %d times right after a ready resume, want 0", n)
	}

	// Only auth and quota trip: an API error is refused by OpenBreaker itself.
	if err := OpenBreaker(db, "work", claudeprobe.FailureAPIError, claudeprobe.ReasonAPIError,
		store.BreakerSourceRun, later); !errors.Is(err, store.ErrBreakerKind) {
		t.Errorf("api-error opening: err = %v, want store.ErrBreakerKind", err)
	}
	if b, _ := breakerRow(t, db, "work"); b.IsOpen() {
		t.Error("an API error opened the breaker")
	}
}

// TestResumeBreakerLimitedReplacesAuth: the login works again but the account
// is out of usage — the auth opening gives way to a quota one, which closes on
// its own; a quota opening is left exactly as it was.
func TestResumeBreakerLimitedReplacesAuth(t *testing.T) {
	db := quotaDB(t)
	installProbe(t, nil)
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	p := &stubProbe{result: probeLimited}
	later := breakerNow.Add(time.Hour)
	if _, err := ResumeBreaker(context.Background(), db, "work", p.probe, later); err != nil {
		t.Fatal(err)
	}
	b, _ := breakerRow(t, db, "work")
	if !b.IsOpen() || b.Kind != store.BreakerKindQuota || b.ResetsAt != "2026-09-30T14:00:00Z" {
		t.Fatalf("breaker = %+v, want an open quota breaker resetting an hour on", b)
	}
	if n := openAlerts(t, db, "work"); n != 1 {
		t.Errorf("open alerts = %d, want 1", n)
	}
	if _, err := ResumeBreaker(context.Background(), db, "work", p.probe, later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again, _ := breakerRow(t, db, "work"); again != b {
		t.Errorf("a second limited probe moved the quota opening: %+v → %+v", b, again)
	}
}

// TestProbeAccountStages runs the production probe against a STUB `claude`: the
// ping only follows a ready `auth status`, it is what sees an account whose
// login is intact but which the API refuses, and the env knob skips it.
func TestProbeAccountStages(t *testing.T) {
	// HOME is a temp dir, so there is no System project to attach the ping to and
	// nothing outside the test is touched.
	t.Setenv("HOME", t.TempDir())
	calls := filepath.Join(t.TempDir(), "calls")
	stub := func(t *testing.T, authOK bool, pingLine string, pingExit string) {
		t.Helper()
		if err := os.RemoveAll(calls); err != nil {
			t.Fatal(err)
		}
		auth := `printf '{"loggedIn": true}\n'; exit 0`
		if !authOK {
			auth = `printf '{"loggedIn": false, "authMethod": "none"}\n'; exit 1`
		}
		line := filepath.Join(t.TempDir(), "line")
		if err := os.WriteFile(line, []byte(pingLine+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(t.TempDir(), "claude")
		body := "#!/bin/sh\necho \"$1\" >> " + calls + "\nif [ \"$1\" = auth ]; then " + auth + "; fi\ncat " + line + "\nexit " + pingExit + "\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SWARMERY_CLAUDE_BIN", script)
	}
	stages := func(t *testing.T) string {
		t.Helper()
		raw, err := os.ReadFile(calls)
		if err != nil {
			t.Fatalf("read calls: %v", err)
		}
		return strings.Join(strings.Fields(string(raw)), ",")
	}

	t.Run("no login stops at stage one", func(t *testing.T) {
		t.Setenv(PreflightPingEnv, "")
		stub(t, false, "OK", "0")
		want := claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}
		if got := ProbeAccount(context.Background(), nil); got != want {
			t.Errorf("ProbeAccount = %+v, want %+v", got, want)
		}
		if got := stages(t); got != "auth" {
			t.Errorf("stages run = %q, want auth only — no tokens spent on an account with no login", got)
		}
	})
	t.Run("healthy account passes both", func(t *testing.T) {
		t.Setenv(PreflightPingEnv, "")
		stub(t, true, "OK", "0")
		if got := ProbeAccount(context.Background(), nil); got != probeReady {
			t.Errorf("ProbeAccount = %+v, want ready", got)
		}
		if got := stages(t); got != "auth,-p" {
			t.Errorf("stages run = %q, want auth then the ping", got)
		}
	})
	t.Run("the ping sees what auth status cannot", func(t *testing.T) {
		t.Setenv(PreflightPingEnv, "")
		stub(t, true, "Your organization has disabled Claude subscription access for Claude Code.", "1")
		if got := ProbeAccount(context.Background(), nil); got != probeNoLogin {
			t.Errorf("ProbeAccount = %+v, want %+v", got, probeNoLogin)
		}
	})
	t.Run("ping off skips stage two", func(t *testing.T) {
		stub(t, true, "Your organization has disabled Claude subscription access", "1")
		for _, off := range []string{"off", "OFF", "0", "false", "no"} {
			t.Setenv(PreflightPingEnv, off)
			if got := ProbeAccount(context.Background(), nil); got != probeReady {
				t.Errorf("%s=%s: ProbeAccount = %+v, want stage one's ready", PreflightPingEnv, off, got)
			}
		}
		if got := stages(t); strings.Contains(got, "-p") {
			t.Errorf("stages run = %q, want no ping", got)
		}
	})
}

// TestPreflightTTLFromEnv: the knob's parse rules, including one warning per
// distinct bad value.
func TestPreflightTTLFromEnv(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", DefaultPreflightTTL},
		{"  ", DefaultPreflightTTL},
		{"0", 0},
		{"0s", 0},
		{"-5m", 0},
		{"90s", 90 * time.Second},
		{"1h", time.Hour},
		{"soon", DefaultPreflightTTL},
		{"soon", DefaultPreflightTTL},
	} {
		t.Setenv(PreflightTTLEnv, tc.raw)
		if got := PreflightTTLFromEnv(); got != tc.want {
			t.Errorf("%s=%q: %s, want %s", PreflightTTLEnv, tc.raw, got, tc.want)
		}
	}
	if n := strings.Count(buf.String(), "ignoring invalid"); n != 1 {
		t.Errorf("warned %d times about one bad value, want 1:\n%s", n, buf.String())
	}
}

// TestCheckAccountWith: a nil seam is the real gate, a set one is what runs.
func TestCheckAccountWith(t *testing.T) {
	db := quotaDB(t)
	installProbe(t, nil)
	if err := OpenBreaker(db, "work", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}
	if err := CheckAccountWith(context.Background(), nil, db, work(), breakerNow); !errors.Is(err, ErrAccountBreaker) {
		t.Errorf("nil seam: %v, want the real gate's refusal", err)
	}
	called := false
	admit := func(context.Context, *sql.DB, claudeacct.Resolution, time.Time) error {
		called = true
		return nil
	}
	if err := CheckAccountWith(context.Background(), admit, db, work(), breakerNow); err != nil || !called {
		t.Errorf("set seam: err = %v called = %v", err, called)
	}
}

// TestRecordBreakerWait: one event per OPENING. A later opening, or a bare
// sentinel in a later hour, records again; nothing is written without a db.
func TestRecordBreakerWait(t *testing.T) {
	db := quotaDB(t)
	first := &AccountBreakerError{Account: "work", Kind: "auth", Reason: claudeprobe.ReasonNoLogin,
		OpenedAt: "2026-09-30T12:00:00Z"}
	at := func(d time.Duration) time.Time { return breakerNow.Add(d) }

	if !RecordBreakerWait(db, "dispatch", 7, first, at(0)) {
		t.Fatal("first refusal not recorded")
	}
	if RecordBreakerWait(db, "dispatch", 7, first, at(3*time.Hour)) {
		t.Error("the same opening recorded twice")
	}
	if !RecordBreakerWait(db, "dispatch", 8, first, at(time.Minute)) {
		t.Error("another card was deduped against this one")
	}
	reopened := *first
	reopened.OpenedAt = "2026-10-01T09:00:00Z"
	if !RecordBreakerWait(db, "dispatch", 7, &reopened, at(21*time.Hour)) {
		t.Error("a new opening was not recorded")
	}
	evs := RunEvents(db, "dispatch", 7)
	if len(evs) != 2 || evs[0].Kind != EventAccountBreaker || evs[0].Detail != first.Error() ||
		evs[0].CreatedAt != "2026-09-30T12:00:00Z" {
		t.Errorf("events = %+v", evs)
	}

	if !RecordBreakerWait(db, "dispatch", 9, ErrAccountBreaker, at(0)) {
		t.Error("bare sentinel not recorded")
	}
	if RecordBreakerWait(db, "dispatch", 9, ErrAccountBreaker, at(10*time.Minute)) {
		t.Error("bare sentinel recorded twice in one hour")
	}
	if !RecordBreakerWait(db, "dispatch", 9, ErrAccountBreaker, at(61*time.Minute)) {
		t.Error("bare sentinel not recorded in the next hour")
	}
	if RecordBreakerWait(nil, "dispatch", 7, first, at(0)) || RecordBreakerWait(db, "dispatch", 7, nil, at(0)) {
		t.Error("recorded with no db or no refusal")
	}
}

// TestBreakerNilDB: the open/close helpers are no-ops without a store.
func TestBreakerNilDB(t *testing.T) {
	if err := OpenBreaker(nil, "work", store.BreakerKindAuth, "", store.BreakerSourceRun, breakerNow); err != nil {
		t.Errorf("OpenBreaker(nil): %v", err)
	}
	if closed, err := CloseBreaker(nil, "work", store.BreakerClosedByOperator, breakerNow); closed || err != nil {
		t.Errorf("CloseBreaker(nil) = %v, %v", closed, err)
	}
}
