package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMigrateAccountQuotaTables: the quota migration is recorded (matched by
// NAME — a renumber renames nothing here), creates both tables and both
// indexes, and is purely additive.
func TestMigrateAccountQuotaTables(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(
		`SELECT name FROM schema_migrations WHERE name LIKE '%_account_quota.sql'`).Scan(&name); err != nil {
		t.Fatalf("account_quota migration not recorded: %v", err)
	}
	if !strings.HasSuffix(name, "_account_quota.sql") {
		t.Errorf("recorded name %q", name)
	}
	mustHaveColumns(t, db, "account_quota",
		"account", "window_key", "label", "percent_used", "percent_left",
		"resets_at", "window_ms", "source", "fetched_at")
	mustHaveColumns(t, db, "account_limit_hits",
		"id", "account", "observed_at", "scope", "source", "engine", "session_uuid", "record_uuid")
	mustHaveIndex(t, db, "idx_account_limit_hits_record")
	mustHaveIndex(t, db, "idx_account_limit_hits_account_time")

	// Primary key (account, window_key): a duplicate window is refused.
	ins := `INSERT INTO account_quota (account, window_key, percent_used, percent_left, source, fetched_at)
	        VALUES ('a', 'five_hour', 1, 99, 'poller', 1)`
	if _, err := db.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Error("duplicate (account, window_key) accepted")
	}
}

// TestMigrateAccountQuotaPreservesRows: rows written before the migration are
// untouched by it — the additive claim, proven rather than assumed.
func TestMigrateAccountQuotaPreservesRows(t *testing.T) {
	latest := latestVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, latest-1)
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (1, 'u1', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sessions after migrate = %d, %v", n, err)
	}
}

func latestVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestOpenNoMigrate: a database missing the newest migration keeps missing it
// after OpenNoMigrate — schema_migrations is byte-for-byte the same.
func TestOpenNoMigrate(t *testing.T) {
	latest := latestVersion(t)
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if err := MigrateUpTo(raw, latest-1); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := OpenNoMigrate(path)
	if err != nil {
		t.Fatalf("OpenNoMigrate: %v", err)
	}
	defer db.Close()
	var maxV, count int
	if err := db.QueryRow(`SELECT MAX(version), COUNT(*) FROM schema_migrations`).Scan(&maxV, &count); err != nil {
		t.Fatal(err)
	}
	if maxV != latest-1 {
		t.Errorf("MAX(version) = %d after OpenNoMigrate, want %d (it migrated)", maxV, latest-1)
	}
	// The table the pending migration would add is absent — callers see "no such
	// table". The pending migration is whichever one is newest: account_breaker
	// since it landed on top of account_quota.
	if _, _, err := GetAccountBreaker(db, "default"); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("GetAccountBreaker on a pre-migration db: err = %v, want no such table", err)
	}

	if _, err := OpenNoMigrate(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Error("OpenNoMigrate created a missing database instead of failing")
	}
}

func TestAccountQuotaRoundTrip(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if rows, err := QuotaForAccount(db, "default"); err != nil || len(rows) != 0 {
		t.Fatalf("empty store: %v %v", rows, err)
	}
	t1 := time.Unix(1_800_000_000, 0)
	if err := PutAccountQuota(db, "default", []QuotaRow{
		{WindowKey: "five_hour", Label: "Session (5h)", PercentUsed: 30, PercentLeft: 70, ResetsAt: "2027-01-01T00:00:00Z", WindowMs: 18000000},
		{WindowKey: "seven_day", Label: "Weekly", PercentUsed: 80, PercentLeft: 20},
	}, t1); err != nil {
		t.Fatal(err)
	}
	if err := PutAccountQuota(db, "work", []QuotaRow{{WindowKey: "five_hour", PercentLeft: 50, PercentUsed: 50}}, t1); err != nil {
		t.Fatal(err)
	}
	rows, err := QuotaForAccount(db, "default")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if rows[0].WindowKey != "five_hour" || rows[0].PercentLeft != 70 || rows[0].Source != "poller" ||
		!rows[0].FetchedAt.Equal(t1) || rows[0].WindowMs != 18000000 {
		t.Errorf("row 0 = %+v", rows[0])
	}
	// Replace: a window no longer reported does not linger.
	t2 := t1.Add(time.Minute)
	if err := PutAccountQuota(db, "default", []QuotaRow{{WindowKey: "seven_day", PercentUsed: 90, PercentLeft: 10}}, t2); err != nil {
		t.Fatal(err)
	}
	all, err := AllAccountQuota(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["default"]) != 1 || all["default"][0].PercentLeft != 10 || len(all["work"]) != 1 {
		t.Errorf("all = %+v", all)
	}
}

func TestAccountLimitHits(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := LimitHit{Account: "default", ObservedAt: "2026-09-14T10:00:00Z", Scope: "weekly", Source: "transcript", RecordUUID: "r1"}
	if ok, err := InsertAccountLimitHit(db, h); err != nil || !ok {
		t.Fatalf("first insert: %v %v", ok, err)
	}
	if ok, err := InsertAccountLimitHit(db, h); err != nil || ok {
		t.Fatalf("duplicate record_uuid inserted: %v %v", ok, err)
	}
	run := LimitHit{Account: "default", ObservedAt: "2026-09-15T10:00:00Z", Source: "run"}
	for i := 0; i < 2; i++ {
		if ok, err := InsertAccountLimitHit(db, run); err != nil || !ok {
			t.Fatalf("run hit %d: %v %v", i, ok, err)
		}
	}
	days, err := LimitHitDays(db, "default", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(days, ",") != "2026-09-14,2026-09-15" {
		t.Errorf("days = %v", days)
	}
	if days, _ := LimitHitDays(db, "other", time.Time{}); len(days) != 0 {
		t.Errorf("other account days = %v", days)
	}
}

func TestSetSessionAccount(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at, account) VALUES (1, 'u1', 'x', 'default')`); err != nil {
		t.Fatal(err)
	}
	if ok, err := SetSessionAccount(db, "u1", "work"); err != nil || !ok {
		t.Fatalf("set: %v %v", ok, err)
	}
	var acct string
	if err := db.QueryRow(`SELECT account FROM sessions WHERE session_uuid = 'u1'`).Scan(&acct); err != nil || acct != "work" {
		t.Fatalf("account = %q, %v", acct, err)
	}
	if ok, err := SetSessionAccount(db, "missing", "work"); err != nil || ok {
		t.Errorf("missing row: %v %v", ok, err)
	}
}
