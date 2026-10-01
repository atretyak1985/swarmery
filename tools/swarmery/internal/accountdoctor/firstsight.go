package accountdoctor

// Arm (e) — the first-sight warning (D3 / R6): anything cloned into an estate
// root inherits its credentials, silently. The first time the doctor sees a
// path under an estate root that has no `projects` row, it says so once —
// naming the estate root and the NUMBER of credentials the path inherits,
// never a name — and records the path in a ledger so the warning is
// once-per-path, not once-per-session (a warning at every start is a warning
// nobody reads).
//
// The arm only DECIDES: the report carries the record, and Commit writes it
// once the caller has put the report out. A run that never delivers its
// report — killed by a hook watchdog mid-arm or mid-render, or a write to a
// closed pipe — leaves the path unrecorded, so the warning comes back next
// time instead of being spent unseen.
//
// The ledger is <StateDir>/estate-seen.json, dir 0700 / file 0600, written
// atomically. Options.Record false makes the arm read-only (the file is never
// created or modified); Options.StateDir "" means no ledger at all. The
// `projects` lookup is the doctor's only database read and is optional: nil,
// missing or locked, the arm relies on the ledger alone and a warn finding
// says so.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LedgerFile is the first-sight ledger's name inside Options.StateDir.
const LedgerFile = "estate-seen.json"

// StateDirEnv overrides the ledger root; DefaultStateDir is the fallback.
const StateDirEnv = "SWARMERY_DOCTOR_DIR"

// DefaultStateDir is $SWARMERY_DOCTOR_DIR, else ~/.swarmery/doctor ("" when
// neither resolves).
func DefaultStateDir() string {
	if d := getenv(StateDirEnv); d != "" {
		return d
	}
	home, err := userHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".swarmery", "doctor")
}

// ledger is the on-disk shape: path -> the time it was first reported.
type ledger struct {
	Seen map[string]string `json:"seen"`
}

// sightRecord is one first-sight warning a report owes its ledger.
type sightRecord struct {
	file, path string
	at         time.Time
}

func (r *run) firstSight() {
	res, rep := r.res, r.rep
	if res.EstateRoot == "" || rep.Daemon {
		return
	}
	indexed, dbOK := r.hasProjectRow()
	if !dbOK {
		r.add(Finding{ID: "first-sight-no-db", Severity: SevWarn,
			Title:  "the projects index could not be read; the first-sight check used its ledger alone",
			Detail: "no database handle, or the database is missing or locked — never a failure"})
	}
	if indexed {
		return
	}
	file := ""
	var l ledger
	if r.opts.StateDir != "" {
		file = filepath.Join(r.opts.StateDir, LedgerFile)
		l = readLedger(file)
		if _, seen := l.Seen[r.path]; seen {
			return
		}
	}
	r.add(Finding{ID: "first-sight", Severity: SevWarn,
		Title: "first sight of this path under an estate root",
		Detail: fmt.Sprintf("this path has no project row and sits under estate root %s, so it inherits that estate's %d credential(s)",
			res.EstateRoot, rep.Credentials),
		File: file})
	if file == "" || !r.opts.Record {
		return
	}
	now := r.opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	rep.pending = &sightRecord{file: file, path: r.path, at: now}
}

// Commit records the first-sight warning rep carries, if any. Call it only
// after rep has been written out: a warning recorded before it reached anyone
// would never be shown again. A report with nothing pending is a no-op.
func Commit(rep Report) error {
	p := rep.pending
	if p == nil {
		return nil
	}
	l := readLedger(p.file)
	l.Seen[p.path] = p.at.UTC().Format(time.RFC3339)
	return writeLedger(p.file, l)
}

// hasProjectRow asks the index whether r.path has a projects row. ok is false
// when there is no usable handle or the query fails (missing, locked).
func (r *run) hasProjectRow() (indexed, ok bool) {
	if r.opts.DB == nil {
		return false, false
	}
	var n int
	if err := r.opts.DB.QueryRow(`SELECT count(*) FROM projects WHERE path = ?`, r.path).Scan(&n); err != nil {
		return false, false
	}
	return n > 0, true
}

// readLedger loads the ledger; an absent or unreadable file is an empty one.
func readLedger(file string) ledger {
	var l ledger
	if !readJSON(file, &l) || l.Seen == nil {
		l.Seen = map[string]string{}
	}
	return l
}

// writeLedger writes the ledger atomically at 0600 inside a 0700 dir.
func writeLedger(file string, l ledger) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ") // map keys marshal sorted
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".estate-seen-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
