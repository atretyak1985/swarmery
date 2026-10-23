package main

// `swarmery ghinbox sync` — the terminal surface of the GitHub inbox
// (internal/ghinbox), and the command step of the seeded github-inbox routine
// (config/routines/github-inbox.json).
//
// It writes board cards straight into the database, like `routine seed` writes
// a routine: the daemon is not asked, so a run works while the daemon is down
// and the cards appear on the board's next read. --dry-run opens the database
// without migrating and with query_only, and writes nothing at all.
//
// Output is shaped for routine_runs.detail, which keeps the TAIL of a step's
// output: the card lines come first, the one-line-per-project summary last.
// Without --dry-run no line quotes GitHub-authored text (titles, bodies) —
// the routine hands this output to a model step. --dry-run is for an operator
// at a terminal and prints each card's title and description too.

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ghinbox"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const ghinboxUsage = `usage:
  swarmery ghinbox sync (--project <id|slug> | --all) [--since <RFC3339>] [--dry-run] [--db <path>]
      For each project with a prReview block in .claude/project.json, reads every
      repo in prReview.repos with the daemon's stored GitHub token (never your gh
      login) and keeps one triage card per open issue / PR that is new or got a
      comment since --since (default: the start of the github-inbox routine's last
      ok run, else 24h ago). A card with a new comment returns to triage, marked
      new-comment. Cards are never written to todo. A project without a stored
      token is skipped with a reason; --all skips projects without prReview.
      --dry-run prints the cards it would write and writes nothing.
      exit: 0 synced (or skipped) · 1 a repository could not be read or a write
            failed · 2 usage or database error`

// ghinboxNewReader builds the GitHub reader for a host. A var so tests can
// serve fixtures instead of running gh.
var ghinboxNewReader = func(host string) (ghinbox.Reader, error) {
	return repoprovider.StoredReadOnlyGH(repoprovider.OSExec{}, host)
}

// cmdGhinbox dispatches the `ghinbox` subcommands and returns the exit code.
func cmdGhinbox(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, ghinboxUsage)
		return 2
	}
	switch args[0] {
	case "sync":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return ghinboxSync(ctx, args[1:], os.Stdout, os.Stderr, time.Now)
	case "-h", "--help", "help":
		fmt.Fprintln(os.Stderr, ghinboxUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown ghinbox subcommand %q\n%s\n", args[0], ghinboxUsage)
		return 2
	}
}

func ghinboxSync(ctx context.Context, args []string, stdout, stderr io.Writer, now func() time.Time) int {
	fs := flag.NewFlagSet("ghinbox sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := dbFlag(fs)
	project := fs.String("project", "", "one project, by id or slug")
	all := fs.Bool("all", false, "every non-archived project with a prReview block")
	sinceFlag := fs.String("since", "", "window start, RFC3339 (default: the routine's last ok run start, else 24h ago)")
	dryRun := fs.Bool("dry-run", false, "print the cards it would write; write nothing")

	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "ghinbox sync: "+format+"\n%s\n", append(a, ghinboxUsage)...)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, ghinboxUsage)
			return 0
		}
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
	}
	if (*project == "") == !*all {
		return usageErr("give exactly one of --project or --all")
	}
	var since time.Time
	if s := strings.TrimSpace(*sinceFlag); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return usageErr("--since %q is not RFC3339", s)
		}
		since = t.UTC()
	}

	var db *sql.DB
	var err error
	if *dryRun {
		db, err = store.OpenNoMigrate(*dbPath)
		if err == nil {
			if _, qerr := db.Exec(`PRAGMA query_only = ON`); qerr != nil {
				db.Close()
				err = fmt.Errorf("set query_only: %w", qerr)
			}
		}
	} else {
		db, err = store.Open(*dbPath)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ghinbox sync: open database: %v\n", err)
		return 2
	}
	defer db.Close()

	projects, err := ghinboxProjects(db, *project)
	if err != nil {
		fmt.Fprintf(stderr, "ghinbox sync: %v\n", err)
		return 2
	}
	if since.IsZero() {
		if since, err = ghinbox.Since(db, now()); err != nil {
			fmt.Fprintf(stderr, "ghinbox sync: read the last run: %v\n", err)
			return 2
		}
	}

	// One reader per host per invocation: the stored token is read once.
	readers := map[string]ghinbox.Reader{}
	readerErrs := map[string]error{}
	opt := ghinbox.Options{
		DryRun: *dryRun,
		Now:    now,
		NewReader: func(host string) (ghinbox.Reader, error) {
			if r, ok := readers[host]; ok {
				return r, nil
			}
			if err, ok := readerErrs[host]; ok {
				return nil, err
			}
			r, err := ghinboxNewReader(host)
			if err != nil {
				readerErrs[host] = err
				return nil, err
			}
			readers[host] = r
			return r, nil
		},
	}

	var lines []string
	noBlock, failed := 0, false
	for _, p := range projects {
		if *all {
			if _, ok := ghinbox.LoadConfig(p.Path); !ok {
				noBlock++
				continue
			}
		}
		rep, err := ghinbox.Sync(ctx, db, p, since, opt)
		if err != nil {
			fmt.Fprintf(stderr, "ghinbox sync: %s: %v\n", p.Slug, err)
			failed = true
			continue
		}
		for _, c := range rep.Cards {
			if *dryRun {
				fmt.Fprintf(stdout, "would %s %s  %s  %s  (+%d new comments)\n%s\n\n",
					strings.TrimSuffix(c.Action, "d"), c.Label, c.Title, c.URL, c.NewComments, indent(c.Description))
			} else {
				fmt.Fprintf(stdout, "%s %s (+%d new comments)\n", c.Action, c.Label, c.NewComments)
			}
		}
		lines = append(lines, rep.Line(*dryRun))
		failed = failed || rep.Failed()
	}
	if *all && noBlock > 0 {
		lines = append(lines, strconv.Itoa(noBlock)+" project(s) without a prReview block: not watched")
	}
	if len(lines) == 0 {
		lines = append(lines, "no project has a prReview block: nothing to sync")
	}
	for _, l := range lines {
		fmt.Fprintln(stdout, l)
	}
	if failed {
		return 1
	}
	return 0
}

// ghinboxProjects is the project set of one invocation: the one --project
// names (by id or slug, archived or not — the operator asked for it), or every
// non-archived project.
func ghinboxProjects(db *sql.DB, sel string) ([]ghinbox.Project, error) {
	q := `SELECT id, slug, path FROM projects WHERE archived = 0 ORDER BY id`
	var args []any
	if sel = strings.TrimSpace(sel); sel != "" {
		q = `SELECT id, slug, path FROM projects WHERE slug = ? OR CAST(id AS TEXT) = ? ORDER BY id LIMIT 1`
		args = []any{sel, sel}
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ghinbox.Project
	for rows.Next() {
		var p ghinbox.Project
		if err := rows.Scan(&p.ID, &p.Slug, &p.Path); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if sel != "" && len(out) == 0 {
		return nil, fmt.Errorf("no project %q (want a project id or slug)", sel)
	}
	return out, nil
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
