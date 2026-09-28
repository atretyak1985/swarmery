package main

// `swarmery account doctor --fast [--json] [--path <dir>]` — the read-only
// credential-coverage report the accounts-pack SessionStart preflight parses.
//
// Flag parsing and encoding ONLY (account.go's rule 2: logic lives in
// internal/accountdoctor, inside the coverage gate). Exactly three flags are
// parsed here; a bare `doctor` with no arm flag is a usage error, not a
// default — that meaning is reserved. stdout carries the one JSON object and
// nothing else; every diagnostic goes to stderr. No socket is opened.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/accountdoctor"
)

const accountDoctorUsage = "usage: swarmery account doctor --fast [--json] [--path <dir>]"

// accountDoctor runs the fast arm and prints its Report.
func accountDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	path := pathFlag(fs)
	fast := fs.Bool("fast", false, "the read-only arm: resolution, estate, credential coverage (names only)")
	asJSON := fs.Bool("json", false, "print the report as one JSON object")
	if err := fs.Parse(args); err != nil {
		return errors.New(accountDoctorUsage)
	}
	if fs.NArg() != 0 || !*fast {
		return errors.New(accountDoctorUsage)
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	rep, err := accountdoctor.Fast(accountdoctor.Options{Path: dir})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(rep)
	}
	printDoctorText(out, rep)
	return nil
}

// printDoctorText is the human form: names and counts, never a value.
func printDoctorText(out io.Writer, r accountdoctor.Report) {
	fmt.Fprintf(out, "project:      %s\n", r.Path)
	fmt.Fprintf(out, "account:      %s (%s)\n", r.Account, r.Source)
	fmt.Fprintf(out, "config dir:   %s\n", orDash(r.ConfigDir))
	if r.Estate != "" {
		fmt.Fprintf(out, "estate:       %s (root %s)\n", r.Estate, r.EstateRoot)
	}
	fmt.Fprintf(out, "credentials:  %d supplied by the estate's store\n", r.Credentials)
	fmt.Fprintf(out, "coverage:     %d of %d referenced variable(s) set\n", len(r.VarsPresent), len(r.VarsExpected))
	if len(r.VarsMissing) > 0 {
		fmt.Fprintf(out, "missing:      %s\n", strings.Join(r.VarsMissing, ", "))
	}
	fmt.Fprintf(out, "launched via swarmery: %s\n", yesNo(r.LaunchedViaSwarmery))
	if r.Daemon {
		fmt.Fprintln(out, "daemon worktree: yes")
	}
}
