package main

// `swarmery account switch` and `swarmery account move-session` — argument
// parsing and printing ONLY. Every decision lives in internal/acctops, which
// the coverage gate covers; this package is excluded from it. Neither command
// opens a socket.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/acctops"
)

const switchUsage = "usage: swarmery account switch <key> [--estate <root>] [--force] [--clear-pins] [--dry-run]"

const moveUsage = "usage: swarmery account move-session <uuid> --to <key> [--from <key>] [--cwd <path>] " +
	"[--project-dir <name>] [--force] [--overwrite] [--dry-run]"

// accountSwitch parses `switch` and prints acctops.Switch's report. On a
// refusal the report gathered so far (estate, pins, headroom) is still printed
// when there is an estate to describe; the refusal itself is the returned
// error, which main prints to stderr with a non-zero exit.
func accountSwitch(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account switch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	estate := fs.String("estate", "", "the estate root (default: the estate the current directory belongs to)")
	force := fs.Bool("force", false, "switch even when the target's quota headroom is unknown")
	clearPins := fs.Bool("clear-pins", false, "clear pins redundant with the estate (only on a run that keeps the payer)")
	dryRun := fs.Bool("dry-run", false, "report what would happen; write nothing")
	positional, flagArgs := splitPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return fmt.Errorf("%v\n%s", err, switchUsage)
	}
	rest := append(append([]string{}, positional...), fs.Args()...)
	if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
		return errors.New(switchUsage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	rep, err := acctops.Switch(acctops.SwitchOptions{
		Key: rest[0], Estate: *estate, Cwd: cwd,
		Force: *force, ClearPins: *clearPins, DryRun: *dryRun,
	})
	if rep.EstateRoot != "" && rep.NewAccount != "" {
		for _, l := range rep.Lines() {
			fmt.Fprintln(out, l)
		}
	}
	return err
}

// accountMoveSession parses `move-session` and prints acctops.MoveSession's
// report.
func accountMoveSession(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account move-session", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	to := fs.String("to", "", "the account to copy the session into (required)")
	from := fs.String("from", "", "the account that holds it, when the database has no row for it")
	cwd := fs.String("cwd", "", "the path the resume command runs from (default: the session's recorded cwd)")
	projectDir := fs.String("project-dir", "", "pick one of several located transcript directories, by name")
	force := fs.Bool("force", false, "move a session the database reports as live")
	overwrite := fs.Bool("overwrite", false, "keep each differing destination file as <name>.pre-move-<UTC timestamp>, then copy over it")
	dryRun := fs.Bool("dry-run", false, "print every source and destination path; write nothing")
	positional, flagArgs := splitPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return fmt.Errorf("%v\n%s", err, moveUsage)
	}
	rest := append(append([]string{}, positional...), fs.Args()...)
	if len(rest) != 1 || strings.TrimSpace(*to) == "" {
		return errors.New(moveUsage)
	}
	rep, err := acctops.MoveSession(acctops.MoveOptions{
		UUID: rest[0], To: *to, From: *from, Cwd: *cwd, ProjectDir: *projectDir,
		Force: *force, Overwrite: *overwrite, DryRun: *dryRun,
	})
	if err != nil {
		// A copy that started and failed: say what landed before the error.
		if errors.Is(err, acctops.ErrCopyFailed) {
			for _, l := range rep.Lines() {
				fmt.Fprintln(out, l)
			}
		}
		return err
	}
	for _, l := range rep.Lines() {
		fmt.Fprintln(out, l)
	}
	return nil
}
