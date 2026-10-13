package plugindrift

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modscan"
)

// insertMod seeds one mods row (the table internal/sysscan fills).
func insertMod(t *testing.T, db *sql.DB, pluginID any, name, dir, errorsJSON string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO mods (name, plugin_id, dir, source, errors) VALUES (?, ?, ?, 'cache', ?)`,
		name, pluginID, dir, errorsJSON); err != nil {
		t.Fatalf("insert mod: %v", err)
	}
}

// healthyTicker has the CLI report every id as installed user-scoped, so the
// only rule that can fire is the mod advisory.
func healthyTicker(t *testing.T, db *sql.DB, spy *newErrorSpy, ids ...string) *Ticker {
	t.Helper()
	var inst []Installed
	for _, id := range ids {
		inst = append(inst, Installed{ID: id, Version: "unknown", Scope: "user", Enabled: true, InstallPath: t.TempDir()})
	}
	tk := &Ticker{DB: db, Detector: &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: listJSON(t, inst...)}}}
	if spy != nil {
		tk.OnNewError = spy.fn
	}
	return tk
}

func TestModAdvisoryForEnabledModWithValidateErrors(t *testing.T) {
	db := tickerDB(t)
	path := project(t, db, "p1", "guard@mkt", "quiet@mkt")
	insertMod(t, db, "guard@mkt", "guard", "/cache/mkt/guard/1.0.0",
		`["manifest: missing author","./register.tsx: unknown hook foo"]`)
	insertMod(t, db, "quiet@mkt", "quiet", "/cache/mkt/quiet/1.0.0", `[]`)
	spy := &newErrorSpy{}

	healthyTicker(t, db, spy, "guard@mkt", "quiet@mkt").Once(context.Background())

	got := activeRows(t, db, RuleModValidateFailed)
	want := Target("guard@mkt", path)
	if len(got) != 1 || got[want] == "" {
		t.Fatalf("want one advisory for %q, got %v", want, got)
	}
	msg := got[want]
	for _, part := range []string{"mod guard", "2 error(s)", "manifest: missing author", "System → Mods"} {
		if !strings.Contains(msg, part) {
			t.Errorf("message %q lacks %q", msg, part)
		}
	}
	var severity string
	if err := db.QueryRow(`SELECT severity FROM config_lint_findings WHERE rule = ? AND target = ?`,
		RuleModValidateFailed, want).Scan(&severity); err != nil {
		t.Fatal(err)
	}
	if severity != "warn" {
		t.Errorf("severity = %q, want warn (advisory)", severity)
	}
	if len(spy.calls) != 0 {
		t.Errorf("an advisory must not fire OnNewError, got %v", spy.calls)
	}
}

func TestModAdvisorySkipsNonFailures(t *testing.T) {
	db := tickerDB(t)
	project(t, db, "p1", "nobin@mkt", "clean@mkt", "nullerr@mkt")
	insertMod(t, db, "nobin@mkt", "nobin", "/c/nobin", `["`+modscan.MsgNoBinary+`"]`)
	insertMod(t, db, "clean@mkt", "clean", "/c/clean", `[]`)
	insertMod(t, db, "nullerr@mkt", "nullerr", "/c/nullerr", `null`)
	// A failing mod nobody enables, and a plugin-dir mod (no plugin id).
	insertMod(t, db, "unused@mkt", "unused", "/c/unused", `["boom"]`)
	insertMod(t, db, nil, "local", "/dev/local", `["boom"]`)

	healthyTicker(t, db, nil, "nobin@mkt", "clean@mkt", "nullerr@mkt").Once(context.Background())

	if got := activeRows(t, db, RuleModValidateFailed); len(got) != 0 {
		t.Fatalf("want no advisories, got %v", got)
	}
}

func TestModAdvisoryTruncatesLongError(t *testing.T) {
	db := tickerDB(t)
	path := project(t, db, "p1", "long@mkt")
	insertMod(t, db, "long@mkt", "long", "/c/long", `["`+strings.Repeat("x", 500)+`"]`)

	healthyTicker(t, db, nil, "long@mkt").Once(context.Background())

	msg := activeRows(t, db, RuleModValidateFailed)[Target("long@mkt", path)]
	if !strings.Contains(msg, strings.Repeat("x", maxModErrorLen)+"…") || strings.Contains(msg, strings.Repeat("x", maxModErrorLen+1)) {
		t.Fatalf("want the error cut at %d chars, got %q", maxModErrorLen, msg)
	}
}

// The advisory reads the DB, so a blind CLI must not stop it.
func TestModAdvisoryEvaluatedWhenDetectorBlind(t *testing.T) {
	db := tickerDB(t)
	path := project(t, db, "p1", "guard@mkt")
	insertMod(t, db, "guard@mkt", "guard", "/c/guard", `["bad"]`)
	tk := &Ticker{DB: db, Detector: &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{err: errors.New("boom")}}}

	tk.Once(context.Background())

	if got := activeRows(t, db, RuleModValidateFailed); got[Target("guard@mkt", path)] == "" {
		t.Fatalf("want the advisory despite a blind detector, got %v", got)
	}
}

func TestModAdvisoryResolvesWhenValidatePasses(t *testing.T) {
	db := tickerDB(t)
	project(t, db, "p1", "guard@mkt")
	insertMod(t, db, "guard@mkt", "guard", "/c/guard", `["bad"]`)
	tk := healthyTicker(t, db, nil, "guard@mkt")
	tk.Once(context.Background())
	if len(activeRows(t, db, RuleModValidateFailed)) != 1 {
		t.Fatal("setup: expected one advisory")
	}

	if _, err := db.Exec(`UPDATE mods SET errors = '[]' WHERE plugin_id = 'guard@mkt'`); err != nil {
		t.Fatal(err)
	}
	tk.Once(context.Background())

	if got := activeRows(t, db, RuleModValidateFailed); len(got) != 0 {
		t.Fatalf("want the advisory resolved after a clean validate, got %v", got)
	}
}

// An unreadable mods table is "not evaluated": existing rows stay active.
func TestModAdvisoryKeptWhenModsUnreadable(t *testing.T) {
	db := tickerDB(t)
	path := project(t, db, "p1", "guard@mkt")
	seeded := Target("guard@mkt", path)
	if err := findings.Upsert(db, seeded, RuleModValidateFailed, "warn", "seeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE mods`); err != nil {
		t.Fatal(err)
	}

	healthyTicker(t, db, nil, "guard@mkt").Once(context.Background())

	if got := activeRows(t, db, RuleModValidateFailed); got[seeded] != "seeded" {
		t.Fatalf("an unreadable mods table must leave the advisory active, got %v", got)
	}
}
