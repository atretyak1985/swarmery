package planrun

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

// TestPlanRunRefusedOnOpenBreaker goes through the REAL gate: the fixture
// project's account has an open breaker in the store, so Start is refused as
// itself — the *runcore.AccountBreakerError the API renders 409 — and like a
// quota refusal it leaves nothing behind: no slot, no worktree, no plan_runs
// row, nothing spawned. Once the breaker closes, the very same Start runs.
func TestPlanRunRefusedOnOpenBreaker(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)
	account := fixtureAccount()
	opened := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := runcore.OpenBreaker(db, account, store.BreakerKindQuota, claudeprobe.ReasonRateLimited,
		store.BreakerSourceTranscript, opened); err != nil {
		t.Fatal(err)
	}
	// Opened in the past with the one-hour fallback, so push the reset well ahead:
	// the refusal below must not depend on the wall clock the gate reads.
	if _, err := db.Exec(`UPDATE account_breaker SET resets_at = '2099-01-01T00:00:00Z' WHERE account = ?`, account); err != nil {
		t.Fatal(err)
	}

	_, err := s.Start(taskID, "", "")
	if !errors.Is(err, runcore.ErrAccountBreaker) {
		t.Fatalf("err = %v, want runcore.ErrAccountBreaker", err)
	}
	var refused *runcore.AccountBreakerError
	if !errors.As(err, &refused) {
		t.Fatalf("refusal does not carry its evidence unwrapped: %v", err)
	}
	if refused.Account != account || refused.Kind != store.BreakerKindQuota ||
		refused.Reason != claudeprobe.ReasonRateLimited || refused.ResetsAt != "2099-01-01T00:00:00Z" {
		t.Errorf("refusal = %+v", *refused)
	}
	if s.Slots.IsActive(s.slotKey(taskID)) || s.Slots.Count() != 0 {
		t.Errorf("slot held after a breaker refusal (count=%d) — the gate must run before TryAcquire", s.Slots.Count())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs WHERE workspace_task_id=?`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0 — a paused account must not stamp a run", n)
	}
	if r.count() != 0 || wt.acquiredCount() != 0 {
		t.Errorf("spawned %d, acquired %d on a paused account, want 0 and 0", r.count(), wt.acquiredCount())
	}

	// A quota opening closes itself once its reset time has passed: the very same
	// Start is then admitted, with no operator involved.
	if _, err := db.Exec(`UPDATE account_breaker SET resets_at = '2020-01-01T00:00:00Z' WHERE account = ?`, account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start after the reset: %v", err)
	}
	if r.count() == 0 {
		t.Error("nothing spawned after the reset")
	}
	b, _, err := store.GetAccountBreaker(db, account)
	if err != nil || b.IsOpen() || b.ClosedBy != store.BreakerClosedByReset {
		t.Errorf("breaker = %+v (%v), want closed by reset", b, err)
	}
}

// The gate is checked against the PROJECT's account, and a Service built as a
// bare struct literal is still gated (a nil seam is the real check).
func TestStart_AccountCheckSeesProjectResolution(t *testing.T) {
	db, taskID, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	var got claudeacct.Resolution
	s.AccountCheck = func(_ context.Context, _ *sql.DB, res claudeacct.Resolution, _ time.Time) error {
		got = res
		return runcore.ErrAccountBreaker // a bare sentinel is still a refusal
	}
	if _, err := s.Start(taskID, "", ""); !errors.Is(err, runcore.ErrAccountBreaker) {
		t.Fatalf("err = %v, want the seam's refusal", err)
	}
	if want := runcore.AccountFor("/repo/p"); got != want {
		t.Errorf("account check saw %+v, want the project's resolution %+v", got, want)
	}

	s.AccountCheck = nil
	if err := runcore.OpenBreaker(db, fixtureAccount(), store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		store.BreakerSourceRun, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(taskID, "", ""); !errors.Is(err, runcore.ErrAccountBreaker) {
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

// The plan runner reports every finished run's exit as an account verdict, for
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
