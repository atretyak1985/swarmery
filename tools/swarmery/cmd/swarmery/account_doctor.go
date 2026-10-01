package main

// `swarmery account doctor [--fast | --probe] [--json] [--path <dir>]
//                          [--timeout <dur>] [--no-record]`
//
//	--fast   the turn-zero arms the accounts-pack SessionStart preflight parses:
//	         pure filesystem (plus the Lock 1 git probe Resolve runs), no
//	         `claude` spawn, no socket
//	(bare)   --fast plus the two findings that cost further git calls
//	         (binding-tracked, estate-settings-tracked)
//	--probe  run the channel-probe harness against the installed CLI, store its
//	         verdict under ~/.swarmery/probes/<cliVersion>.json, then report —
//	         the ONLY form that starts a process
//
// Flag parsing and wiring ONLY (account.go's rule 2: logic lives in
// internal/accountdoctor, inside the coverage gate). stdout carries the report
// and nothing else; every diagnostic goes to stderr. No socket is opened. The
// one database read — the first-sight arm's projects-row lookup — opens the
// index READ-ONLY and degrades to a warn finding when it is missing or locked.

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/accountdoctor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const accountDoctorUsage = "usage: swarmery account doctor [--fast | --probe] [--json] [--path <dir>] [--timeout <dur>] [--no-record]"

// usageError is a malformed invocation: main exits 2 for it (a finding is
// never an error — a report with findings exits 0).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// accountDoctor parses the flags, runs the chosen arm and prints its Report.
func accountDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := pathFlag(fs)
	fast := fs.Bool("fast", false, "the turn-zero arms only: no claude spawn, no further git calls")
	probe := fs.Bool("probe", false, "run the channel probe against the installed CLI and store its verdict")
	asJSON := fs.Bool("json", false, "print the report as one JSON object")
	timeout := fs.Duration("timeout", 0, "bound the whole call (e.g. 2.5s); 0 = no bound")
	noRecord := fs.Bool("no-record", false, "leave the first-sight ledger untouched (read-only)")
	if err := fs.Parse(args); err != nil {
		return usageError{accountDoctorUsage}
	}
	if fs.NArg() != 0 || (*fast && *probe) || *timeout < 0 {
		return usageError{accountDoctorUsage}
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	db := openIndexReadOnly()
	if db != nil {
		defer db.Close()
	}
	opts := accountdoctor.Options{
		Path:     dir,
		DB:       db,
		Now:      time.Now(),
		StateDir: accountdoctor.DefaultStateDir(),
		Timeout:  *timeout,
		Record:   !*noRecord,
	}
	var rep accountdoctor.Report
	switch {
	case *probe:
		rep, err = accountdoctor.Probe(context.Background(), opts)
	case *fast:
		rep, err = accountdoctor.Fast(opts)
	default:
		rep, err = accountdoctor.Full(opts)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return accountdoctor.RenderJSON(out, rep)
	}
	return accountdoctor.RenderText(out, rep)
}

// openIndexReadOnly opens the daemon's index read-only for the first-sight
// lookup, or returns nil (the arm then says it used its ledger alone). It never
// creates the file and never migrates it — store.Open would do both.
func openIndexReadOnly() *sql.DB {
	p, err := store.DefaultDBPath()
	if err != nil {
		return nil
	}
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+p+"?mode=ro&_pragma=busy_timeout(200)")
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(1)
	return db
}

// isUsage reports whether err is a doctor usage error (exit 2).
func isUsage(err error) bool {
	var ue usageError
	return errors.As(err, &ue)
}
