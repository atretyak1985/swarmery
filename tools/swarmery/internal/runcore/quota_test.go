package runcore

import (
	"bytes"
	"database/sql"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func quotaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// quotaEnv pins both knobs the gate reads, so a developer's shell cannot leak a
// floor or a poll interval into the table.
func quotaEnv(t *testing.T, floor string) {
	t.Helper()
	t.Setenv(QuotaFloorEnv, floor)
	t.Setenv(QuotaIntervalEnv, "") // default 10m ⇒ maxAge 30m
}

// TestCheckQuota is the gate's contract, over rows written the way the poller
// writes them (store.PutAccountQuota). It refuses ONLY on a fresh reading below
// the floor; every flavour of "unknown" admits (SC-4).
func TestCheckQuota(t *testing.T) {
	now := time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	stale := now.Add(-31 * time.Minute) // past 3 × the 10m default interval
	reset := "2027-01-01T00:00:00Z"     // four hours ahead of now
	passed := "2026-12-31T19:00:00Z"    // an hour behind now

	type seed struct {
		account string
		at      time.Time
		rows    []store.QuotaRow
	}
	cases := []struct {
		name       string
		floor      string
		res        claudeacct.Resolution
		seeds      []seed
		wantRefuse bool
		wantWindow string
		wantLeft   float64
		wantAcct   string
	}{
		{
			name:  "fresh reading below the floor refuses",
			floor: "",
			res:   claudeacct.Resolution{Account: "work"},
			seeds: []seed{{"work", fresh, []store.QuotaRow{
				{WindowKey: "five_hour", Label: "Session (5h)", PercentLeft: 5, ResetsAt: reset}}}},
			wantRefuse: true, wantWindow: "five_hour", wantLeft: 5, wantAcct: "work",
		},
		{
			name:  "fresh reading above the floor admits",
			floor: "",
			res:   claudeacct.Resolution{Account: "work"},
			seeds: []seed{{"work", fresh, []store.QuotaRow{
				{WindowKey: "five_hour", PercentLeft: 50, ResetsAt: reset}}}},
		},
		{
			name:  "stale reading is unknown and admits",
			floor: "",
			res:   claudeacct.Resolution{Account: "work"},
			seeds: []seed{{"work", stale, []store.QuotaRow{
				{WindowKey: "five_hour", PercentLeft: 1, ResetsAt: reset}}}},
		},
		{
			name:  "no rows is unknown and admits",
			floor: "",
			res:   claudeacct.Resolution{Account: "work"},
			// Another account's low reading must not leak onto this one.
			seeds: []seed{{"other", fresh, []store.QuotaRow{{WindowKey: "five_hour", PercentLeft: 1}}}},
		},
		{
			name:  "floor 0 disables the gate",
			floor: "0",
			res:   claudeacct.Resolution{Account: "work"},
			seeds: []seed{{"work", fresh, []store.QuotaRow{
				{WindowKey: "five_hour", PercentLeft: 1, ResetsAt: reset}}}},
		},
		{
			name:  "tightest window wins",
			floor: "",
			res:   claudeacct.Resolution{Account: "work"},
			seeds: []seed{{"work", fresh, []store.QuotaRow{
				{WindowKey: "five_hour", Label: "Session (5h)", PercentLeft: 60, ResetsAt: reset},
				{WindowKey: "seven_day", Label: "Weekly", PercentLeft: 4, ResetsAt: "2027-01-05T00:00:00Z"}}}},
			wantRefuse: true, wantWindow: "seven_day", wantLeft: 4, wantAcct: "work",
		},
		{
			// Read a minute ago, but its window reset an hour ago: the 1% describes
			// a window that no longer exists.
			name:  "fresh reading whose reset already passed is unknown and admits",
			floor: "",
			res:   claudeacct.Resolution{Account: "work"},
			seeds: []seed{{"work", fresh, []store.QuotaRow{
				{WindowKey: "five_hour", PercentLeft: 1, ResetsAt: passed}}}},
		},
		{
			name:  "a passed window yields to the next live one",
			floor: "",
			res:   claudeacct.Resolution{Account: "work"},
			seeds: []seed{{"work", fresh, []store.QuotaRow{
				{WindowKey: "five_hour", Label: "Session (5h)", PercentLeft: 1, ResetsAt: passed},
				{WindowKey: "seven_day", Label: "Weekly", PercentLeft: 6, ResetsAt: "2027-01-05T00:00:00Z"}}}},
			wantRefuse: true, wantWindow: "seven_day", wantLeft: 6, wantAcct: "work",
		},
		{
			name:  "unbound project reads the poller's default-account key",
			floor: "",
			res:   claudeacct.Resolution{Source: claudeacct.SourceDefault, DefaultProfile: true},
			seeds: []seed{{"default", fresh, []store.QuotaRow{
				{WindowKey: "five_hour", PercentLeft: 2, ResetsAt: reset}}}},
			wantRefuse: true, wantWindow: "five_hour", wantLeft: 2, wantAcct: "default",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quotaEnv(t, tc.floor)
			db := quotaDB(t)
			for _, s := range tc.seeds {
				if err := store.PutAccountQuota(db, s.account, s.rows, s.at); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckQuota(db, tc.res, now)
			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("CheckQuota = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrLowQuota) {
				t.Fatalf("CheckQuota = %v, want ErrLowQuota", err)
			}
			var low *LowQuotaError
			if !errors.As(err, &low) {
				t.Fatalf("CheckQuota = %T, want *LowQuotaError", err)
			}
			if low.Account != tc.wantAcct || low.Window != tc.wantWindow || low.PercentLeft != tc.wantLeft || low.Floor != DefaultQuotaFloor {
				t.Errorf("refusal = %+v, want account=%s window=%s left=%v floor=%v",
					low, tc.wantAcct, tc.wantWindow, tc.wantLeft, DefaultQuotaFloor)
			}
			if low.ResetsAt == "" {
				t.Error("refusal carries no reset time — the operator cannot tell when to retry")
			}
		})
	}
}

// A nil db (no store wired) is unknown, and unknown admits.
func TestCheckQuota_NilDBAdmits(t *testing.T) {
	quotaEnv(t, "")
	if err := CheckQuota(nil, claudeacct.Resolution{Account: "work"}, time.Now()); err != nil {
		t.Fatalf("CheckQuota(nil db) = %v, want nil", err)
	}
}

func TestQuotaFloorFromEnv(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	for _, tc := range []struct {
		raw  string
		want float64
	}{
		{"", DefaultQuotaFloor},
		{"0", 0},
		{"-5", 0},
		{"25", 25},
		{"2.5", 2.5},
		{"abc", DefaultQuotaFloor},
		{"NaN", DefaultQuotaFloor},
		{"101", DefaultQuotaFloor},
	} {
		t.Setenv(QuotaFloorEnv, tc.raw)
		if got := QuotaFloorFromEnv(); got != tc.want {
			t.Errorf("QuotaFloorFromEnv(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}

	// One log line per distinct bad value, however often it is read.
	logs.Reset()
	t.Setenv(QuotaFloorEnv, "oops")
	for i := 0; i < 5; i++ {
		QuotaFloorFromEnv()
	}
	if n := strings.Count(logs.String(), "ignoring invalid "+QuotaFloorEnv); n != 1 {
		t.Errorf("logged %d warnings for one bad value, want 1:\n%s", n, logs.String())
	}
}

func TestLowQuotaError_MessageAndSentinel(t *testing.T) {
	e := &LowQuotaError{Account: "work", Window: "five_hour", Label: "Session (5h)",
		ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 9.64, Floor: 10}
	want := "account work: Session (5h) at 9.6% left (floor 10%), resets 2027-01-01T00:00:00Z"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(e, ErrLowQuota) || errors.Is(e, ErrNoSlot) {
		t.Error("LowQuotaError must match ErrLowQuota and only it")
	}
	noReset := &LowQuotaError{Account: "a", Window: "w", PercentLeft: 1, Floor: 10}
	if !strings.HasSuffix(noReset.Error(), "w at 1% left (floor 10%), resets unknown") {
		t.Errorf("Error() without label/reset = %q", noReset.Error())
	}
}

// A card re-evaluated every scheduling pass records ONE quota_wait per
// (account, window, reset): a new reading of the same window does not add a row;
// a new reset time, a different window or a different account does.
func TestRecordQuotaWait_OncePerAccountWindowReset(t *testing.T) {
	db := quotaDB(t)
	at := func(hhmm string) time.Time {
		ts, err := time.Parse(time.RFC3339, "2026-09-29T"+hhmm+":00Z")
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	first := &LowQuotaError{Account: "work", Window: "five_hour", ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 5, Floor: 10}
	sameWindow := &LowQuotaError{Account: "work", Window: "five_hour", ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 3, Floor: 10}
	otherWindow := &LowQuotaError{Account: "work", Window: "seven_day", ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 3, Floor: 10}
	otherAccount := &LowQuotaError{Account: "work2", Window: "five_hour", ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 3, Floor: 10}
	nextReset := &LowQuotaError{Account: "work", Window: "five_hour", ResetsAt: "2027-01-01T05:00:00Z", PercentLeft: 2, Floor: 10}

	if !RecordQuotaWait(db, "dispatch", 7, first, at("10:00")) {
		t.Fatal("first refusal not recorded")
	}
	if RecordQuotaWait(db, "dispatch", 7, sameWindow, at("12:01")) {
		t.Error("a second refusal for the same account, window and reset was recorded")
	}
	if !RecordQuotaWait(db, "dispatch", 7, otherWindow, at("12:02")) {
		t.Error("a refusal on a different window (same reset time) was not recorded")
	}
	if !RecordQuotaWait(db, "dispatch", 7, otherAccount, at("12:03")) {
		t.Error("a refusal on a different account was not recorded — \"work\" must not match \"work2\"")
	}
	// Another subject is its own timeline.
	if !RecordQuotaWait(db, "dispatch", 8, first, at("12:04")) {
		t.Error("another task's first refusal was not recorded")
	}
	if !RecordQuotaWait(db, "dispatch", 7, nextReset, at("15:01")) {
		t.Error("a refusal in the next reset window was not recorded")
	}
	evs := RunEvents(db, "dispatch", 7)
	if len(evs) != 4 {
		t.Fatalf("events = %+v, want 4", evs)
	}
	if evs[0].CreatedAt != "2026-09-29T10:00:00Z" {
		t.Errorf("created_at = %q, want the RFC 3339 stamp of now", evs[0].CreatedAt)
	}
	for _, e := range evs {
		if e.Kind != EventQuotaWait {
			t.Errorf("kind = %q, want %q", e.Kind, EventQuotaWait)
		}
	}
	if evs[0].Detail != first.Error() {
		t.Errorf("detail = %q, want the refusal's text", evs[0].Detail)
	}
}

// A window that reports no reset time has nothing to key the window on, and
// "once forever" would hide every later wait. It dedupes per (account, window)
// per clock hour instead.
func TestRecordQuotaWait_NoResetDedupesPerHour(t *testing.T) {
	db := quotaDB(t)
	noReset := &LowQuotaError{Account: "work", Window: "five_hour", PercentLeft: 4, Floor: 10}
	base := time.Date(2026, 9, 29, 10, 5, 0, 0, time.UTC)

	if !RecordQuotaWait(db, "dispatch", 7, noReset, base) {
		t.Fatal("first refusal not recorded")
	}
	if RecordQuotaWait(db, "dispatch", 7, noReset, base.Add(50*time.Minute)) { // 10:55
		t.Error("a second refusal in the same hour was recorded")
	}
	if !RecordQuotaWait(db, "dispatch", 7, noReset, base.Add(56*time.Minute)) { // 11:01
		t.Error("a refusal in the next hour was not recorded")
	}
	// A bare sentinel carries no account or window: its text, per hour.
	if !RecordQuotaWait(db, "dispatch", 9, ErrLowQuota, base) {
		t.Error("a bare refusal was not recorded")
	}
	if RecordQuotaWait(db, "dispatch", 9, ErrLowQuota, base.Add(10*time.Minute)) {
		t.Error("a bare refusal was recorded twice in one hour")
	}
	if n := len(RunEvents(db, "dispatch", 7)); n != 2 {
		t.Errorf("events = %d, want 2 (one per hour)", n)
	}
}
