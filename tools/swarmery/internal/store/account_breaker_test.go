package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func breakerDB(t *testing.T) Querier {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestMigrateAccountBreakerTable: the breaker migration is recorded (matched by
// NAME — a renumber renames nothing here), creates the table, and the state
// column refuses anything outside its closed set.
func TestMigrateAccountBreakerTable(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(
		`SELECT name FROM schema_migrations WHERE name LIKE '%_account_breaker.sql'`).Scan(&name); err != nil {
		t.Fatalf("account_breaker migration not recorded: %v", err)
	}
	if !strings.HasSuffix(name, "_account_breaker.sql") {
		t.Errorf("recorded name %q", name)
	}
	mustHaveColumns(t, db, "account_breaker",
		"account", "state", "kind", "reason", "opened_at", "resets_at", "source", "closed_at", "closed_by")
	if _, err := db.Exec(`INSERT INTO account_breaker (account, state) VALUES ('a', 'half-open')`); err == nil {
		t.Error("a state outside ('open','closed') was accepted")
	}
}

// TestAccountBreakerLifecycle walks open → get → list → close → reopen.
func TestAccountBreakerLifecycle(t *testing.T) {
	db := breakerDB(t)

	if _, ok, err := GetAccountBreaker(db, "work"); err != nil || ok {
		t.Fatalf("never-opened breaker: ok=%v err=%v, want absent", ok, err)
	}
	if closed, err := CloseAccountBreaker(db, "work", BreakerClosedByOperator, time.Unix(0, 0)); err != nil || closed {
		t.Fatalf("closing a breaker that never opened: closed=%v err=%v", closed, err)
	}

	opening := AccountBreaker{
		Account: "work", Kind: BreakerKindQuota, Reason: "limit",
		OpenedAt: "2026-09-30T10:00:00Z", ResetsAt: "2026-09-30T15:00:00Z", Source: BreakerSourceRun,
	}
	if changed, err := OpenAccountBreaker(db, opening); err != nil || !changed {
		t.Fatalf("first opening: changed=%v err=%v", changed, err)
	}
	got, ok, err := GetAccountBreaker(db, "work")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	want := opening
	want.State = BreakerOpen
	if got != want || !got.IsOpen() {
		t.Errorf("row = %+v, want %+v", got, want)
	}

	// A second trip of the same kind writes nothing: opened_at and the reset
	// time belong to the first one.
	again := opening
	again.OpenedAt, again.ResetsAt = "2026-09-30T11:00:00Z", "2026-09-30T20:00:00Z"
	if changed, err := OpenAccountBreaker(db, again); err != nil || changed {
		t.Fatalf("repeat opening: changed=%v err=%v, want a no-op", changed, err)
	}
	if got, _, _ := GetAccountBreaker(db, "work"); got != want {
		t.Errorf("row moved on a repeat opening: %+v", got)
	}

	if _, err := OpenAccountBreaker(db, AccountBreaker{
		Account: "other", Kind: BreakerKindAuth, Reason: "login", OpenedAt: "2026-09-30T10:05:00Z", Source: BreakerSourceProbe,
	}); err != nil {
		t.Fatal(err)
	}
	all, err := ListAccountBreakers(db, false)
	if err != nil || len(all) != 2 || all[0].Account != "other" || all[1].Account != "work" {
		t.Fatalf("list = %+v, %v", all, err)
	}
	if all[0].ResetsAt != "" {
		t.Errorf("auth opening has resets_at %q, want none", all[0].ResetsAt)
	}

	at := time.Date(2026, 9, 30, 15, 0, 1, 0, time.UTC)
	if closed, err := CloseAccountBreaker(db, "work", BreakerClosedByReset, at); err != nil || !closed {
		t.Fatalf("close: closed=%v err=%v", closed, err)
	}
	got, _, _ = GetAccountBreaker(db, "work")
	if got.IsOpen() || got.ClosedBy != BreakerClosedByReset || got.ClosedAt != "2026-09-30T15:00:01Z" {
		t.Errorf("closed row = %+v", got)
	}
	if closed, _ := CloseAccountBreaker(db, "work", BreakerClosedByOperator, at); closed {
		t.Error("closing an already-closed breaker reported a close")
	}
	open, err := ListAccountBreakers(db, true)
	if err != nil || len(open) != 1 || open[0].Account != "other" {
		t.Fatalf("open list = %+v, %v", open, err)
	}

	// Reopening a closed breaker is a NEW opening: its own opened_at, and the
	// close stamps are gone.
	if changed, err := OpenAccountBreaker(db, again); err != nil || !changed {
		t.Fatalf("reopen: changed=%v err=%v", changed, err)
	}
	got, _, _ = GetAccountBreaker(db, "work")
	if !got.IsOpen() || got.OpenedAt != again.OpenedAt || got.ClosedAt != "" || got.ClosedBy != "" {
		t.Errorf("reopened row = %+v", got)
	}
}

// TestAccountBreakerEscalatesQuotaToAuth: an auth failure on an account already
// paused for quota turns the opening into an auth one — a reset time must not
// close a breaker that needs a probe — while a quota failure never downgrades
// an auth opening.
func TestAccountBreakerEscalatesQuotaToAuth(t *testing.T) {
	db := breakerDB(t)
	quota := AccountBreaker{Account: "work", Kind: BreakerKindQuota, Reason: "limit",
		OpenedAt: "2026-09-30T10:00:00Z", ResetsAt: "2026-09-30T15:00:00Z", Source: BreakerSourceRun}
	auth := AccountBreaker{Account: "work", Kind: BreakerKindAuth, Reason: "login",
		OpenedAt: "2026-09-30T10:30:00Z", Source: BreakerSourceTranscript}
	if _, err := OpenAccountBreaker(db, quota); err != nil {
		t.Fatal(err)
	}
	if changed, err := OpenAccountBreaker(db, auth); err != nil || !changed {
		t.Fatalf("escalation: changed=%v err=%v", changed, err)
	}
	got, _, _ := GetAccountBreaker(db, "work")
	if got.Kind != BreakerKindAuth || got.Reason != "login" || got.Source != BreakerSourceTranscript ||
		got.ResetsAt != "" || got.OpenedAt != quota.OpenedAt {
		t.Errorf("escalated row = %+v, want auth with no reset and the original opened_at", got)
	}
	if changed, err := OpenAccountBreaker(db, quota); err != nil || changed {
		t.Fatalf("quota over auth: changed=%v err=%v, want a no-op", changed, err)
	}
}

// TestAccountBreakerRefusesOtherKinds: an API error never opens a breaker, and
// neither does an opening with no account.
func TestAccountBreakerRefusesOtherKinds(t *testing.T) {
	db := breakerDB(t)
	_, err := OpenAccountBreaker(db, AccountBreaker{Account: "work", Kind: "api-error"})
	if !errors.Is(err, ErrBreakerKind) {
		t.Errorf("api-error opening: err = %v, want ErrBreakerKind", err)
	}
	if _, err := OpenAccountBreaker(db, AccountBreaker{Kind: BreakerKindAuth}); err == nil {
		t.Error("an opening with no account was accepted")
	}
	if all, _ := ListAccountBreakers(db, false); len(all) != 0 {
		t.Errorf("rows written by refused openings: %+v", all)
	}
}

// TestQuotaResetHint: the scope's own window when the limit line named one,
// else the EARLIEST still-ahead reset of the freshest poll, else now + 1h.
func TestQuotaResetHint(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	const (
		fallback = "2026-09-30T11:00:00Z"
		session  = "2026-09-30T13:30:00Z"
		weekly   = "2026-10-04T00:00:00Z"
		opus     = "2026-10-02T00:00:00Z"
	)

	if got := QuotaResetHint(db, "work", "session", now); got != fallback {
		t.Errorf("no reading: %q, want %q", got, fallback)
	}
	// The WEEKLY window is the tight one here (2% left) while the session window
	// has plenty: the regression this guards is a session-limit trip taking the
	// weekly reset and pausing the account for days.
	if err := PutAccountQuota(db, "work", []QuotaRow{
		{WindowKey: "five_hour", PercentLeft: 60, ResetsAt: session},
		{WindowKey: "seven_day", PercentLeft: 2, ResetsAt: weekly},
		{WindowKey: "seven_day_opus", PercentLeft: 30, ResetsAt: opus},
	}, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ scope, want, why string }{
		{"session", session, "a session limit resets with the five-hour window"},
		{"weekly", weekly, "a weekly limit resets with the seven-day window"},
		{"model", opus, "a model limit resets with a per-model weekly window"},
		{"", session, "an unknown scope takes the EARLIEST reset, not the tightest window's"},
		{"monthly", session, "a scope with no window takes the earliest reset"},
	} {
		if got := QuotaResetHint(db, "work", tc.scope, now); got != tc.want {
			t.Errorf("scope %q: %q, want %q — %s", tc.scope, got, tc.want, tc.why)
		}
	}
	// The scope's window already reset: the earliest live one answers.
	if got := QuotaResetHint(db, "work", "session", now.Add(4*time.Hour)); got != opus {
		t.Errorf("passed session window: %q, want the next reset ahead (%q)", got, opus)
	}
	// Every reset passed: the fallback.
	if got := QuotaResetHint(db, "work", "weekly", now.Add(30*24*time.Hour)); got != "2026-10-30T11:00:00Z" {
		t.Errorf("all passed: %q, want now + 1h", got)
	}
	// A window with no (or an unparseable) reset says nothing.
	if err := PutAccountQuota(db, "other", []QuotaRow{
		{WindowKey: "five_hour", PercentLeft: 1},
		{WindowKey: "seven_day", PercentLeft: 1, ResetsAt: "soon"},
	}, now); err != nil {
		t.Fatal(err)
	}
	if got := QuotaResetHint(db, "other", "session", now); got != fallback {
		t.Errorf("windows with no usable reset: %q, want %q", got, fallback)
	}
	// Only the FRESHEST poll counts: a window the endpoint stopped reporting
	// cannot supply a reset (PutAccountQuota replaces the whole set).
	if err := PutAccountQuota(db, "work", []QuotaRow{
		{WindowKey: "seven_day", PercentLeft: 2, ResetsAt: weekly},
	}, now); err != nil {
		t.Fatal(err)
	}
	if got := QuotaResetHint(db, "work", "session", now); got != weekly {
		t.Errorf("session scope with no session window stored: %q, want the earliest reset ahead (%q)", got, weekly)
	}
}

// TestTripAccountBreakerUsesTheScope: a session-limit trip gets the session
// window's reset even when the weekly window is the tighter one.
func TestTripAccountBreakerUsesTheScope(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	if err := PutAccountQuota(db, "work", []QuotaRow{
		{WindowKey: "five_hour", PercentLeft: 60, ResetsAt: "2026-09-30T13:30:00Z"},
		{WindowKey: "seven_day", PercentLeft: 2, ResetsAt: "2026-10-04T00:00:00Z"},
	}, now); err != nil {
		t.Fatal(err)
	}
	if changed, err := TripAccountBreaker(db, "work", BreakerKindQuota, "limit", BreakerSourceTranscript, "session", now); err != nil || !changed {
		t.Fatalf("trip: changed=%v err=%v", changed, err)
	}
	b, _, err := GetAccountBreaker(db, "work")
	if err != nil || b.ResetsAt != "2026-09-30T13:30:00Z" || b.OpenedAt != "2026-09-30T10:00:00Z" {
		t.Errorf("breaker = %+v (%v), want the session window's reset", b, err)
	}
	// An auth trip never carries a reset, whatever scope it is handed.
	if _, err := TripAccountBreaker(db, "other", BreakerKindAuth, "login", BreakerSourceRun, "session", now); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := GetAccountBreaker(db, "other"); b.ResetsAt != "" {
		t.Errorf("auth breaker resets_at = %q, want none", b.ResetsAt)
	}
}

// TestQuotaResetHintFailsToTheFallback: a database without the quota table (one
// that predates 0089) still yields a usable reset time.
func TestQuotaResetHintFailsToTheFallback(t *testing.T) {
	db := openRaw(t)
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	if got := QuotaResetHint(db, "work", "session", now); got != "2026-09-30T11:00:00Z" {
		t.Errorf("unmigrated db: %q, want now + 1h", got)
	}
	if _, _, err := GetAccountBreaker(db, "work"); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("GetAccountBreaker on an unmigrated db: err = %v, want no such table", err)
	}
	if _, err := ListAccountBreakers(db, true); err == nil {
		t.Error("ListAccountBreakers on an unmigrated db succeeded")
	}
}

// TestAccountBreakerFindingVocabulary: the finding target round-trips and the
// message depends on the kind alone.
func TestAccountBreakerFindingVocabulary(t *testing.T) {
	if got := AccountBreakerTarget("work"); got != "account:work" {
		t.Errorf("target = %q", got)
	}
	if a, ok := AccountFromBreakerTarget("account:work"); !ok || a != "work" {
		t.Errorf("round trip = %q, %v", a, ok)
	}
	for _, bad := range []string{"agent:work", "account:", "work"} {
		if _, ok := AccountFromBreakerTarget(bad); ok {
			t.Errorf("%q parsed as a breaker target", bad)
		}
	}
	if AccountBreakerMessage(BreakerKindQuota) == AccountBreakerMessage(BreakerKindAuth) {
		t.Error("quota and auth share one message")
	}
}
