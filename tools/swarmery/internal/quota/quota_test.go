package quota

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var accounts = []claudeacct.Account{
	{Key: "default", ConfigDir: "/h/.claude", IsDefault: true},
	{Key: "work", ConfigDir: "/h/.claude-work"},
}

const secretBody = "Bearer sk-ant-oat01-SHOULD-NEVER-BE-LOGGED"

func TestPollOnceWritesOKAndSkipsErrors(t *testing.T) {
	db := openDB(t)
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	var seen []usage.Source
	now := time.Unix(1_800_000_000, 0)
	p := &Poller{
		DB:       db,
		Accounts: func() []claudeacct.Account { return accounts },
		Now:      func() time.Time { return now },
		Fetch: func(_ context.Context, src usage.Source) usage.Provider {
			seen = append(seen, src)
			if src.Account == "work" {
				return usage.Provider{Account: "work", Status: usage.StatusError, Error: secretBody}
			}
			return usage.Provider{Account: src.Account, Status: usage.StatusOK, Windows: []usage.Window{
				{Key: "five_hour", Label: "Session (5h)", PercentUsed: 40, PercentLeft: 60, ResetAt: "2027-01-01T00:00:00Z", WindowMs: 1},
				{Key: "seven_day", Label: "Weekly", PercentUsed: 90, PercentLeft: 10},
				{Key: "", PercentLeft: 1},
			}}
		},
	}
	if n := p.PollOnce(context.Background()); n != 1 {
		t.Fatalf("written = %d, want 1", n)
	}
	if len(seen) != 2 || seen[0].ConfigDir != "" || seen[1].ConfigDir != "/h/.claude-work" {
		t.Errorf("sources = %+v (default must get an EMPTY ConfigDir)", seen)
	}
	all, err := store.AllAccountQuota(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["default"]) != 2 || len(all["work"]) != 0 {
		t.Errorf("stored = %+v", all)
	}
	if strings.Contains(logs.String(), "sk-ant") || strings.Contains(logs.String(), "Bearer") {
		t.Error("the provider's Error body reached the log")
	}
	if !strings.Contains(logs.String(), "account=work status=error") {
		t.Errorf("log = %q, want one line naming the account and status", logs.String())
	}

	h, ok := Headroom(db, "default", now.Add(time.Minute), time.Hour)
	if !ok || h.PercentLeft != 10 || h.Window != "seven_day" || h.Label != "Weekly" || !h.FetchedAt.Equal(now) {
		t.Errorf("headroom = %+v ok=%v, want the minimum window", h, ok)
	}
	if _, ok := Headroom(db, "default", now.Add(2*time.Hour), time.Hour); ok {
		t.Error("stale rows reported as known")
	}
	if _, ok := Headroom(db, "default", now.Add(48*time.Hour), 0); !ok {
		t.Error("maxAge 0 must disable the staleness bound")
	}
	if _, ok := Headroom(db, "work", now, time.Hour); ok {
		t.Error("an account with no rows reported as known")
	}
	if _, ok := Headroom(nil, "default", now, time.Hour); ok {
		t.Error("nil db reported as known")
	}
}

// An expired token records nothing for that account and logs one line naming
// the account; the default account's Source ignores an inherited
// CLAUDE_CONFIG_DIR.
func TestPollOnceExpiredTokenRecordsNothing(t *testing.T) {
	db := openDB(t)
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	var seen []usage.Source
	p := &Poller{
		DB:       db,
		Accounts: func() []claudeacct.Account { return accounts },
		Fetch: func(_ context.Context, src usage.Source) usage.Provider {
			seen = append(seen, src)
			if src.Account == "work" {
				return usage.Provider{Account: "work", Status: usage.StatusNoAuth, TokenExpired: true, Error: secretBody}
			}
			return usage.Provider{Status: usage.StatusOK, Windows: []usage.Window{{Key: "k", PercentLeft: 50}}}
		},
	}
	if n := p.PollOnce(context.Background()); n != 1 {
		t.Fatalf("written = %d, want 1 (only the unexpired account)", n)
	}
	all, err := store.AllAccountQuota(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["work"]) != 0 {
		t.Errorf("an expired-token account got rows: %+v", all["work"])
	}
	if got := strings.Count(logs.String(), "account=work token expired, not refreshing"); got != 1 {
		t.Errorf("log = %q, want one expired line for work", logs.String())
	}
	if strings.Contains(logs.String(), "Bearer") {
		t.Error("the provider's Error body reached the log")
	}
	if len(seen) != 2 || !seen[0].IgnoreConfigDirEnv || seen[1].IgnoreConfigDirEnv {
		t.Errorf("sources = %+v, want only the default to ignore CLAUDE_CONFIG_DIR", seen)
	}
}

func TestPollOnceGuardsAndEmptyWindows(t *testing.T) {
	if n := (&Poller{}).PollOnce(context.Background()); n != 0 {
		t.Error("a poller without a DB wrote")
	}
	db := openDB(t)
	p := &Poller{DB: db, Accounts: func() []claudeacct.Account { return accounts[:1] },
		Fetch: func(context.Context, usage.Source) usage.Provider {
			return usage.Provider{Status: usage.StatusOK}
		}}
	if n := p.PollOnce(context.Background()); n != 0 {
		t.Errorf("no windows: written = %d", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := p.PollOnce(ctx); n != 0 {
		t.Errorf("cancelled ctx: written = %d", n)
	}
	// A store failure is logged, not fatal.
	p.Fetch = func(context.Context, usage.Source) usage.Provider {
		return usage.Provider{Status: usage.StatusOK, Windows: []usage.Window{{Key: "k", PercentLeft: 1}}}
	}
	db.Close()
	if n := p.PollOnce(context.Background()); n != 0 {
		t.Errorf("closed db: written = %d", n)
	}
}

// LastReading reports the newest reading however old, and nothing for an
// account with no rows or a nil db.
func TestLastReading(t *testing.T) {
	db := openDB(t)
	at := time.Unix(1_800_000_000, 0).UTC()
	if err := store.PutAccountQuota(db, "default", []store.QuotaRow{{WindowKey: "a"}, {WindowKey: "b"}}, at); err != nil {
		t.Fatal(err)
	}
	if got, ok := LastReading(db, "default"); !ok || !got.Equal(at) {
		t.Errorf("LastReading = %v %v, want %v", got, ok, at)
	}
	if _, ok := LastReading(db, "work"); ok {
		t.Error("an account with no rows has a reading")
	}
	if _, ok := LastReading(nil, "default"); ok {
		t.Error("nil db has a reading")
	}
}

// Headroom over a database missing the table degrades to unknown.
func TestHeadroomPreMigrationIsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE sessions (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := store.OpenNoMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, ok := Headroom(db, "default", time.Now(), time.Hour); ok {
		t.Error("pre-migration db reported known headroom")
	}
}

func TestRunPollsThenStops(t *testing.T) {
	db := openDB(t)
	var calls atomic.Int32
	p := &Poller{
		DB: db, Interval: 10 * time.Millisecond, StartDelay: -1,
		Accounts: func() []claudeacct.Account { return accounts[:1] },
		Fetch: func(context.Context, usage.Source) usage.Provider {
			calls.Add(1)
			return usage.Provider{Status: usage.StatusOK, Windows: []usage.Window{{Key: "k", PercentLeft: 50}}}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if calls.Load() < 2 {
		t.Errorf("calls = %d, want the first pass plus a tick", calls.Load())
	}

	// Cancelled during the start delay: returns without polling.
	p2 := &Poller{DB: db, StartDelay: time.Hour, Accounts: p.Accounts, Fetch: p.Fetch}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	before := calls.Load()
	p2.Run(ctx2)
	if calls.Load() != before {
		t.Error("polled despite a cancelled start delay")
	}
}

func TestParseInterval(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		enabled bool
		err     bool
	}{
		{"", DefaultInterval, true, false},
		{"0", 0, false, false},
		{"OFF", 0, false, false},
		{"0s", 0, false, false},
		{"5m", 5 * time.Minute, true, false},
		{"soon", DefaultInterval, true, true},
		{"-1m", DefaultInterval, true, true},
	}
	for _, c := range cases {
		d, en, err := ParseInterval(c.in)
		if d != c.want || en != c.enabled || (err != nil) != c.err {
			t.Errorf("ParseInterval(%q) = %s %v %v", c.in, d, en, err)
		}
	}
}

// The default fetcher caches one usage.Client per account key.
func TestClientIsCachedPerAccount(t *testing.T) {
	p := &Poller{}
	a := p.client(usage.Source{Account: "work", ConfigDir: "/x"})
	b := p.client(usage.Source{Account: "work", ConfigDir: "/x"})
	c := p.client(usage.Source{Account: "default"})
	if a != b || a == c || a.Src.ConfigDir != "/x" {
		t.Error("client cache is not per account")
	}
	if !a.NoRefresh || !c.NoRefresh {
		t.Error("the poller's clients may refresh a token")
	}
	if p.now().IsZero() {
		t.Error("default clock")
	}
}
