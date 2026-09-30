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
	return claudeacct.Resolution{Account: "work", ConfigDir: "/cfg/work"}
}

// stubProbe is the pre-flight probe seam: it counts its calls, records the
// config dir it was asked about and answers with a fixed result. No test in
// this file reaches a real `claude`.
type stubProbe struct {
	calls   atomic.Int32
	result  claudeprobe.Result
	mu      sync.Mutex
	dirs    []string
	started chan struct{} // closed on the first call, when set
	release chan struct{} // the call blocks on it, when set
	once    sync.Once
}

func (p *stubProbe) probe(_ context.Context, configDir string) claudeprobe.Result {
	p.calls.Add(1)
	p.mu.Lock()
	p.dirs = append(p.dirs, configDir)
	p.mu.Unlock()
	if p.started != nil {
		p.once.Do(func() { close(p.started) })
	}
	if p.release != nil {
		<-p.release
	}
	return p.result
}

// installProbe points the pre-flight at p (nil switches it off), with the
// package's memo cleared and the env knobs pinned, and restores all of it.
func installProbe(t *testing.T, p AccountProbeFunc) {
	t.Helper()
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
	installProbe(t, p.probe)
	if err := OpenBreaker(db, "work", store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, breakerNow); err != nil {
		t.Fatal(err)
	}

	other := claudeacct.Resolution{Account: "other", ConfigDir: "/cfg/other"}
	if err := CheckAccount(context.Background(), db, other, breakerNow); err != nil {
		t.Fatalf("other account refused: %v", err)
	}
	if err := CheckAccount(context.Background(), db, work(), breakerNow); !errors.Is(err, ErrAccountBreaker) {
		t.Fatalf("paused account admitted: %v", err)
	}
	if _, ok := breakerRow(t, db, "other"); ok {
		t.Error("admitting the other account wrote it a breaker row")
	}
	if n := p.calls.Load(); n != 1 || p.dirs[0] != "/cfg/other" {
		t.Errorf("probe calls = %d dirs = %v, want one call about /cfg/other", n, p.dirs)
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

// TestPreflightProbesTheRunsOwnAccount: the probe is asked about the config dir
// the RUN would get — nothing for the default account (absence selects it), the
// resolution's dir when it carries one, and for a resolution that names only an
// account, the dir the spawn env derives from that key. An account whose dir
// cannot be determined is not probed as somebody else: it is unknown, and
// admitted.
func TestPreflightProbesTheRunsOwnAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	db := quotaDB(t)
	p := &stubProbe{result: probeReady}
	installProbe(t, p.probe)

	for _, res := range []claudeacct.Resolution{
		{},
		{Account: "default", DefaultProfile: true},
		{Account: "work", ConfigDir: "/cfg/work"},
		{Account: "keyonly"},
	} {
		if err := CheckAccount(context.Background(), db, res, breakerNow); err != nil {
			t.Fatalf("resolution %+v refused: %v", res, err)
		}
	}
	// The two default spellings share one key, so the second is inside the TTL.
	want := []string{"", "/cfg/work", filepath.Join(home, ".claude-keyonly")}
	if strings.Join(p.dirs, "|") != strings.Join(want, "|") {
		t.Errorf("probed dirs = %q, want %q", p.dirs, want)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if err := CheckAccount(context.Background(), db, claudeacct.Resolution{Account: "not a key"}, breakerNow); err != nil {
		t.Errorf("an account with no resolvable dir was refused: %v", err)
	}
	if n := p.calls.Load(); n != 3 {
		t.Errorf("probe calls = %d, want still 3 — an unresolvable account must not be probed as the default one", n)
	}
	if !strings.Contains(buf.String(), "could not answer") {
		t.Errorf("the skipped probe was not reported:\n%s", buf.String())
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

		errs := make(chan error, admissions)
		var wg sync.WaitGroup
		for i := 0; i < admissions; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- CheckAccount(context.Background(), db, work(), breakerNow)
			}()
		}
		// The leader is inside the probe; give the other four time to queue behind
		// it, then let the probe answer. The count below holds whether or not they
		// made it: a caller that arrives after the flight reads what it stored.
		select {
		case <-p.started:
		case <-time.After(10 * time.Second):
			t.Fatal("no admission reached the probe")
		}
		time.Sleep(50 * time.Millisecond)
		close(p.release)
		wg.Wait()
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
		t.Errorf("refusal = %+v, want the tight window's reset time", *refused)
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

// TestAuthBreakerNeedsReadyProbe: an auth opening never closes on time. A probe
// that is not ready leaves it open; only a ready one closes it ('probe') — and
// an API error never opens one at all.
func TestAuthBreakerNeedsReadyProbe(t *testing.T) {
	db := quotaDB(t)
	gate := &stubProbe{result: probeReady}
	installProbe(t, gate.probe)
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
		got, err := ResumeBreaker(context.Background(), db, "work", "/cfg/work", p.probe, later)
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if p.calls.Load() != 1 || p.dirs[0] != "/cfg/work" {
			t.Fatalf("resume probe calls = %d dirs = %v", p.calls.Load(), p.dirs)
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
	if _, err := ResumeBreaker(context.Background(), db, "work", "", p.probe, later); err != nil {
		t.Fatal(err)
	}
	b, _ := breakerRow(t, db, "work")
	if !b.IsOpen() || b.Kind != store.BreakerKindQuota || b.ResetsAt != "2026-09-30T14:00:00Z" {
		t.Fatalf("breaker = %+v, want an open quota breaker resetting an hour on", b)
	}
	if n := openAlerts(t, db, "work"); n != 1 {
		t.Errorf("open alerts = %d, want 1", n)
	}
	if _, err := ResumeBreaker(context.Background(), db, "work", "", p.probe, later.Add(time.Minute)); err != nil {
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
		if got := ProbeAccount(context.Background(), ""); got != want {
			t.Errorf("ProbeAccount = %+v, want %+v", got, want)
		}
		if got := stages(t); got != "auth" {
			t.Errorf("stages run = %q, want auth only — no tokens spent on an account with no login", got)
		}
	})
	t.Run("healthy account passes both", func(t *testing.T) {
		t.Setenv(PreflightPingEnv, "")
		stub(t, true, "OK", "0")
		if got := ProbeAccount(context.Background(), ""); got != probeReady {
			t.Errorf("ProbeAccount = %+v, want ready", got)
		}
		if got := stages(t); got != "auth,-p" {
			t.Errorf("stages run = %q, want auth then the ping", got)
		}
	})
	t.Run("the ping sees what auth status cannot", func(t *testing.T) {
		t.Setenv(PreflightPingEnv, "")
		stub(t, true, "Your organization has disabled Claude subscription access for Claude Code.", "1")
		if got := ProbeAccount(context.Background(), ""); got != probeNoLogin {
			t.Errorf("ProbeAccount = %+v, want %+v", got, probeNoLogin)
		}
	})
	t.Run("ping off skips stage two", func(t *testing.T) {
		stub(t, true, "Your organization has disabled Claude subscription access", "1")
		for _, off := range []string{"off", "OFF", "0", "false", "no"} {
			t.Setenv(PreflightPingEnv, off)
			if got := ProbeAccount(context.Background(), ""); got != probeReady {
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
