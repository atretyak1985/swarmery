package phaserun

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// fixtureAccount is the account key the fixture project ('/repo/p', unbound)
// runs under — resolved the way Start resolves it, never assumed.
func fixtureAccount() string {
	return runcore.QuotaAccountKey(runcore.AccountFor("/repo/p"))
}

// TestPhaseRunRefusedOnOpenBreaker goes through the REAL gate: the fixture
// project's account has an open breaker in the store, so Start is refused as
// itself — the *runcore.AccountBreakerError the API renders 409 — and like a
// quota refusal it leaves nothing behind: no slot, no worktree, run_state
// untouched, nothing spawned. Once the breaker closes, the very same Start runs.
func TestPhaseRunRefusedOnOpenBreaker(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)
	account := fixtureAccount()
	opened := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := runcore.OpenBreaker(db, account, store.BreakerKindAuth, claudeprobe.ReasonAccessRefused,
		store.BreakerSourceRun, opened); err != nil {
		t.Fatal(err)
	}

	_, err := s.Start(p1, "", "")
	if !errors.Is(err, runcore.ErrAccountBreaker) {
		t.Fatalf("err = %v, want runcore.ErrAccountBreaker", err)
	}
	var refused *runcore.AccountBreakerError
	if !errors.As(err, &refused) {
		t.Fatalf("refusal does not carry its evidence unwrapped: %v", err)
	}
	if refused.Account != account || refused.Kind != store.BreakerKindAuth ||
		refused.Reason != claudeprobe.ReasonAccessRefused || refused.OpenedAt != "2026-09-30T12:00:00Z" {
		t.Errorf("refusal = %+v", *refused)
	}
	if s.Slots.IsActive(s.slotKey(p1)) || s.Slots.Count() != 0 {
		t.Errorf("slot held after a breaker refusal (count=%d) — the gate must run before TryAcquire", s.Slots.Count())
	}
	if got := phaseRunState(t, db, p1); got != "idle" {
		t.Errorf("run_state = %q, want the untouched 'idle' — a paused account is not a failed phase", got)
	}
	if n := r.specCount(); n != 0 {
		t.Errorf("runner spawned %d times on a paused account, want 0", n)
	}
	if n := wt.acquiredCount(); n != 0 {
		t.Errorf("worktree acquired %d times on a paused account, want 0", n)
	}

	// The breaker closes (the operator's "Probe & resume"): the same Start runs.
	if _, err := runcore.CloseBreaker(db, account, store.BreakerClosedByProbe, opened.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start after the breaker closed: %v", err)
	}
	if n := r.specCount(); n == 0 {
		t.Error("nothing spawned after the breaker closed")
	}
}

// The gate is checked against the PROJECT's account — the resolution the run
// then executes under — and a Service built as a bare struct literal is still
// gated (a nil seam is the real check).
func TestStart_AccountCheckSeesProjectResolution(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	var got claudeacct.Resolution
	s.AccountCheck = func(_ context.Context, _ *sql.DB, res claudeacct.Resolution, _ time.Time) error {
		got = res
		return runcore.ErrAccountBreaker // a bare sentinel is still a refusal
	}
	if _, err := s.Start(p1, "", ""); !errors.Is(err, runcore.ErrAccountBreaker) {
		t.Fatalf("err = %v, want the seam's refusal", err)
	}
	if want := runcore.AccountFor("/repo/p"); got != want {
		t.Errorf("account check saw %+v, want the project's resolution %+v", got, want)
	}

	s.AccountCheck = nil
	if err := runcore.OpenBreaker(db, fixtureAccount(), store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceRun, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(p1, "", ""); !errors.Is(err, runcore.ErrAccountBreaker) {
		t.Errorf("nil seam: err = %v, want the real gate's refusal", err)
	}
}

// verdictScript installs a fake `claude` that prints line on stdout and exits
// with code.
func verdictScript(t *testing.T, line, code string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "line")
	if err := os.WriteFile(out, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fakeclaude-verdict.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat "+out+"\nexit "+code+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)
}

// The phase runner reports every finished run's exit as an account verdict, for
// the account the PROJECT is bound to: an account the API refuses reads as
// no-login, a usage limit as limited, an ordinary failure as unknown, a clean
// exit as ready. A nil hook is never called and changes nothing.
func TestRunnerAccountVerdict(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	for _, tc := range []struct {
		name, line, code string
		want             claudeprobe.Result
	}{
		{"access refused", "Your organization has disabled Claude subscription access", "1",
			claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonAccessRefused}},
		{"login demanded", "Not logged in · Please run /login", "1",
			claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}},
		{"usage limit", "You've hit your weekly limit · resets Oct 4 at 5pm (UTC)", "1",
			claudeprobe.Result{Status: claudeprobe.StatusLimited, Reason: claudeprobe.ReasonRateLimited}},
		{"ordinary failure", "the tests failed", "2",
			claudeprobe.Result{Status: claudeprobe.StatusUnknown, Reason: claudeprobe.ReasonUnrecognised}},
		{"zero exit over a failure line", "Your organization has disabled Claude subscription access", "0",
			claudeprobe.Result{Status: claudeprobe.StatusReady}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdictScript(t, tc.line, tc.code)
			var (
				calls   int
				account string
				got     claudeprobe.Result
			)
			r := ClaudeRunner{Timeout: 30 * time.Second, AccountVerdict: func(a string, res claudeprobe.Result) {
				calls++
				account, got = a, res
			}}
			if _, err := r.Start(context.Background(), RunSpec{
				Prompt: "p", SessionUUID: "verdict", Cwd: t.TempDir(), ProjectPath: project,
			}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if calls != 1 || account != "nabu-org" {
				t.Fatalf("hook calls = %d account = %q, want one call for nabu-org", calls, account)
			}
			if got != tc.want {
				t.Errorf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("nil hook", func(t *testing.T) {
		verdictScript(t, "Not logged in · Please run /login", "1")
		run, err := ClaudeRunner{Timeout: 30 * time.Second}.Start(context.Background(), RunSpec{
			Prompt: "p", SessionUUID: "verdict-nil", Cwd: t.TempDir(), ProjectPath: project,
		})
		if err != nil || run.ExitCode != 1 {
			t.Errorf("run = %+v err = %v, want the plain exit 1", run, err)
		}
	})

	t.Run("a run that never started reports nothing", func(t *testing.T) {
		t.Setenv("SWARMERY_CLAUDE_BIN", filepath.Join(t.TempDir(), "absent-claude"))
		calls := 0
		r := ClaudeRunner{Timeout: 30 * time.Second, AccountVerdict: func(string, claudeprobe.Result) { calls++ }}
		if _, err := r.Start(context.Background(), RunSpec{
			Prompt: "p", SessionUUID: "verdict-nostart", Cwd: t.TempDir(), ProjectPath: project,
		}); err == nil {
			t.Fatal("Start with no binary succeeded")
		}
		if calls != 0 {
			t.Errorf("hook calls = %d for a run that never started, want 0", calls)
		}
	})
}
