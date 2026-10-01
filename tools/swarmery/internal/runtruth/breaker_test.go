package runtruth

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func breaker(t *testing.T, db *sql.DB, account string) (store.AccountBreaker, bool) {
	t.Helper()
	b, ok, err := store.GetAccountBreaker(db, account)
	if err != nil {
		t.Fatalf("get breaker: %v", err)
	}
	return b, ok
}

// A run that died demanding a login opens the account's auth breaker, stamped
// with the run's time, the fixed reason and source 'run' — and surfaces it.
func TestRecordNoLoginOpensAuthBreaker(t *testing.T) {
	rec, db, now := newRecorder(t)
	rec.Record("nabu-org", noLogin)

	b, ok := breaker(t, db, "nabu-org")
	if !ok || !b.IsOpen() {
		t.Fatalf("breaker = %+v ok=%v, want open", b, ok)
	}
	if b.Kind != store.BreakerKindAuth || b.Reason != claudeprobe.ReasonNoLogin ||
		b.Source != store.BreakerSourceRun || b.OpenedAt != now.UTC().Format("2006-01-02T15:04:05Z") || b.ResetsAt != "" {
		t.Errorf("breaker = %+v", b)
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM config_lint_findings WHERE target = 'account:nabu-org' AND rule = ? AND resolved_at IS NULL`,
		store.AccountBreakerRule).Scan(&n); err != nil || n != 1 {
		t.Errorf("open alerts = %d (%v), want 1", n, err)
	}
}

// The org-disabled exit reaches the recorder as no-login with the
// access-refused reason (claudeprobe.ClassifyRun reads the run's output tail);
// it opens the same auth breaker.
func TestRecordOrgDisabledRunOpensAuthBreaker(t *testing.T) {
	rec, db, _ := newRecorder(t)
	rec.Record("", claudeprobe.ClassifyRun(1, "Your organization has disabled Claude subscription access", ""))

	b, ok := breaker(t, db, "default")
	if !ok || !b.IsOpen() || b.Kind != store.BreakerKindAuth || b.Reason != claudeprobe.ReasonAccessRefused {
		t.Errorf("breaker = %+v ok=%v, want an open auth breaker on the default account", b, ok)
	}
}

// A limited run opens a quota breaker with a reset time, and still never
// touches the runnable verdict.
func TestRecordLimitedOpensQuotaBreaker(t *testing.T) {
	rec, db, now := newRecorder(t)
	rec.Record("nabu-org", limited)

	b, ok := breaker(t, db, "nabu-org")
	if !ok || !b.IsOpen() || b.Kind != store.BreakerKindQuota || b.Reason != claudeprobe.ReasonRateLimited {
		t.Fatalf("breaker = %+v ok=%v, want an open quota breaker", b, ok)
	}
	if want := now.Add(3600e9).UTC().Format("2006-01-02T15:04:05Z"); b.ResetsAt != want {
		t.Errorf("resets_at = %q, want now + 1h (%q) with no stored quota window", b.ResetsAt, want)
	}
	if _, ok := mustGet(t, db, "nabu-org"); ok {
		t.Error("a limited run wrote a runnable verdict")
	}
}

// Ready and unknown never trip: a zero exit says nothing bad about the account
// whatever the run printed, and an API error or an ordinary failure is unknown.
func TestRecordReadyAndUnknownNeverTrip(t *testing.T) {
	rec, db, _ := newRecorder(t)
	for _, r := range []claudeprobe.Result{
		{Status: claudeprobe.StatusReady},
		{Status: claudeprobe.StatusUnknown, Reason: claudeprobe.ReasonUnrecognised},
		claudeprobe.ClassifyRun(0, "Your organization has disabled Claude subscription access", ""),
		claudeprobe.ClassifyRun(1, "API Error: 529 Overloaded", ""),
		claudeprobe.ClassifyRun(2, "tests failed", "exit status 2"),
	} {
		rec.Record("nabu-org", r)
		if b, ok := breaker(t, db, "nabu-org"); ok {
			t.Errorf("verdict %+v opened a breaker: %+v", r, b)
		}
	}
}

// The opening is not debounced: a breaker the operator closed is reopened by
// the very next failing run, inside the verdict's debounce window. A successful
// run does not close one.
func TestRecordReopensInsideTheDebounceWindow(t *testing.T) {
	rec, db, now := newRecorder(t)
	rec.Record("nabu-org", noLogin)
	if _, err := runcore.CloseBreaker(db, "nabu-org", store.BreakerClosedByOperator, *now); err != nil {
		t.Fatal(err)
	}

	*now = now.Add(debounceWindow / 4)
	rec.Record("nabu-org", claudeprobe.Result{Status: claudeprobe.StatusReady})
	if b, _ := breaker(t, db, "nabu-org"); b.IsOpen() {
		t.Fatal("a ready run reopened the breaker")
	}
	rec.Record("nabu-org", noLogin)
	b, _ := breaker(t, db, "nabu-org")
	if !b.IsOpen() || b.OpenedAt != now.UTC().Format("2006-01-02T15:04:05Z") {
		t.Errorf("breaker = %+v, want reopened at the failing run's time", b)
	}

	rec.Record("nabu-org", claudeprobe.Result{Status: claudeprobe.StatusReady})
	if b, _ := breaker(t, db, "nabu-org"); !b.IsOpen() {
		t.Error("a ready run closed the breaker — only a reset, a probe, a login or the operator may")
	}
}

// A store failure is logged, never a panic.
func TestRecordBreakerStoreError(t *testing.T) {
	rec, db, _ := newRecorder(t)
	db.Close()
	rec.Record("x", noLogin)
	rec.Record("x", limited)
}

// inheritAccountDir makes the daemon look like it inherited CLAUDE_CONFIG_DIR =
// a real account's dir: a hermetic HOME holding <home>/.claude-<key>/projects
// (what claudeacct.Discover lists), and the variable pointing at that dir.
func inheritAccountDir(t *testing.T, key string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude-"+key)
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
}

// TestRecordPausesEveryKeyOnTheFailedDir: an unbound run executes under the
// config dir the daemon inherited but is paused under the default key; runs of
// projects bound to the account that owns that dir use the same dir under its
// own key. A failure of either kind of run hits the other, so both keys open.
func TestRecordPausesEveryKeyOnTheFailedDir(t *testing.T) {
	t.Run("an unbound run's failure pauses the dir's account too", func(t *testing.T) {
		inheritAccountDir(t, "inherited")
		rec, db, _ := newRecorder(t)
		rec.Record("", noLogin)
		for _, key := range []string{"default", "inherited"} {
			if b, ok := breaker(t, db, key); !ok || !b.IsOpen() || b.Kind != store.BreakerKindAuth {
				t.Errorf("breaker %s = %+v ok=%v, want an open auth breaker", key, b, ok)
			}
		}
	})
	t.Run("a bound run on that dir pauses the unbound runs too", func(t *testing.T) {
		inheritAccountDir(t, "inherited")
		rec, db, _ := newRecorder(t)
		rec.Record("inherited", limited)
		for _, key := range []string{"inherited", "default"} {
			if b, ok := breaker(t, db, key); !ok || !b.IsOpen() || b.Kind != store.BreakerKindQuota {
				t.Errorf("breaker %s = %+v ok=%v, want an open quota breaker", key, b, ok)
			}
		}
	})
	t.Run("another account's failure pauses only itself", func(t *testing.T) {
		inheritAccountDir(t, "inherited")
		rec, db, _ := newRecorder(t)
		rec.Record("nabu-org", noLogin)
		for _, key := range []string{"default", "inherited"} {
			if _, ok := breaker(t, db, key); ok {
				t.Errorf("breaker %s exists after another account's failure", key)
			}
		}
	})
	t.Run("an inherited dir that is no account adds nothing", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "daemon-config-dir"))
		rec, db, _ := newRecorder(t)
		rec.Record("", noLogin)
		if _, ok := breaker(t, db, "daemon-config-dir"); ok {
			t.Error("opened a breaker for a dir no project can be bound to — its alert could never be resumed")
		}
	})
}
