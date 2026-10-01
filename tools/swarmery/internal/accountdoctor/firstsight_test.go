package accountdoctor

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Criterion 12: the first-sight warning fires once per path, names the estate
// root and a COUNT (never a name), fires again once the ledger is removed, and
// Record false never creates or modifies the ledger.
func TestFirstSightOncePerPath(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "PACK_ONE=zzq-first-one\nPACK_TWO=zzq-first-two\n")
	fresh := filepath.Join(root, "fresh")
	mustMkdir(t, fresh, 0o755)
	state := filepath.Join(t.TempDir(), "doctor")
	ledger := filepath.Join(state, LedgerFile)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	opts := Options{Path: fresh, StateDir: state, Record: true, Now: now}

	rep, _ := Fast(opts)
	var fs []Finding
	for _, fd := range rep.Findings {
		if fd.ID == "first-sight" {
			fs = append(fs, fd)
		}
	}
	if len(fs) != 1 || !strings.Contains(fs[0].Detail, root) || !strings.Contains(fs[0].Detail, "2 credential") {
		t.Fatalf("first run: first-sight = %+v", fs)
	}
	if strings.Contains(fs[0].Detail, "PACK_ONE") || strings.Contains(fs[0].Detail, "zzq-first") {
		t.Error("the warning names a credential")
	}
	fi, err := os.Stat(ledger)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("ledger = %v %v, want 0600", fi, err)
	}
	if di, _ := os.Stat(state); di.Mode().Perm() != 0o700 {
		t.Errorf("ledger dir mode = %v, want 0700", di.Mode().Perm())
	}

	if rep, _ := Fast(opts); countFindings(rep, "first-sight", "") != 0 {
		t.Error("second run warned again")
	}
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	if rep, _ := Fast(opts); countFindings(rep, "first-sight", "") != 1 {
		t.Error("after removing the ledger: no warning")
	}

	// Record false: a fresh path warns, and the ledger is not touched.
	other := filepath.Join(root, "other")
	mustMkdir(t, other, 0o755)
	before, _ := os.Stat(ledger)
	ro := opts
	ro.Path, ro.Record = other, false
	if rep, _ := Fast(ro); countFindings(rep, "first-sight", "") != 1 {
		t.Error("read-only run did not warn")
	}
	after, _ := os.Stat(ledger)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("--no-record modified the ledger")
	}
	os.Remove(ledger)
	if _, err := Fast(ro); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Error("--no-record created the ledger")
	}
}

// A path that has a projects row is not new; with no DB handle the arm says
// it used the ledger alone, and a locked / broken DB degrades the same way.
func TestFirstSightProjectsRow(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "")
	indexed := filepath.Join(root, "indexed")
	mustMkdir(t, indexed, 0o755)

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE projects (path TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (path) VALUES (?)`, indexed); err != nil {
		t.Fatal(err)
	}
	rep, _ := Fast(Options{Path: indexed, DB: db, StateDir: t.TempDir(), Record: true})
	if countFindings(rep, "first-sight", "") != 0 || countFindings(rep, "first-sight-no-db", "") != 0 {
		t.Errorf("indexed path: %+v", rep.Findings)
	}

	rep, _ = Fast(Options{Path: indexed})
	if countFindings(rep, "first-sight-no-db", SevWarn) != 1 {
		t.Errorf("no DB: %+v", rep.Findings)
	}
	broken, _ := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db")) // no projects table
	defer broken.Close()
	rep, err = Fast(Options{Path: indexed, DB: broken})
	if err != nil || countFindings(rep, "first-sight-no-db", SevWarn) != 1 || countFindings(rep, "first-sight", "") != 1 {
		t.Errorf("broken DB: err=%v %+v", err, rep.Findings)
	}
}

func TestDefaultStateDir(t *testing.T) {
	f := newFixture(t)
	t.Setenv(StateDirEnv, "")
	if got, want := DefaultStateDir(), filepath.Join(f.home, ".swarmery", "doctor"); got != want {
		t.Errorf("DefaultStateDir = %q, want %q", got, want)
	}
	t.Setenv(StateDirEnv, "/x/y")
	if DefaultStateDir() != "/x/y" {
		t.Error("the env override was ignored")
	}
}
