package main

// memory-engineering phase 1: `swarmery memory lint`.
//
// A report, not a gate: it prints every auto-memory line that claims a PR is
// open when the project's git history already carries its merge, and exits 0
// whether or not it found any. The daemon's R13 and the Memory page produce
// the same report from the same package (internal/memlint).
//
// --project <path> never opens the database — like `memory consolidate`, it
// must be runnable while the daemon is serving and can never migrate a schema
// as a side effect. --all is the ONE path that reads the database: it needs
// the list of non-archived projects, and takes it through store.OpenNoMigrate
// with query_only set, the way `triage check` and `decide eval` do.

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const memoryLintUsage = "usage: swarmery memory lint (--project <path> | --all [--db <path>]) [--claude-dir <dir>] [--json]"

func cmdMemoryLint(args []string) error {
	fs := flag.NewFlagSet("memory lint", flag.ExitOnError)
	project := fs.String("project", "", "path of the project whose auto-memory to lint")
	all := fs.Bool("all", false, "lint every non-archived project in the database (read-only)")
	asJSON := fs.Bool("json", false, "emit the report(s) as JSON instead of text")
	claudeDir := fs.String("claude-dir", memconsolidate.DefaultClaudeDir(),
		"Claude Code config dir holding projects/<slug>/memory")
	dbPath := dbFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 || (*project == "") == !*all {
		return fmt.Errorf("%s", memoryLintUsage)
	}

	if !*all {
		rep, err := lintProject(*project, *claudeDir)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(rep)
		}
		printLintReport(rep)
		return nil
	}

	db, err := store.OpenNoMigrate(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	// The verb issues SELECTs alone; the connection refuses a write on top.
	if _, err := db.Exec(`PRAGMA query_only = ON`); err != nil {
		return fmt.Errorf("memory lint: set query_only: %w", err)
	}
	projects, err := nonArchivedProjects(db)
	if err != nil {
		return err
	}
	reports := make([]memlint.Report, 0, len(projects))
	for _, p := range projects {
		rep, err := lintProject(p.Path, *claudeDir)
		if err != nil {
			return fmt.Errorf("memory lint %s: %w", p.Slug, err)
		}
		reports = append(reports, rep)
	}
	if *asJSON {
		return printJSON(reports)
	}
	for i, rep := range reports {
		if i > 0 {
			fmt.Println()
		}
		printLintReport(rep)
	}
	return nil
}

// lintProject lints one project; a missing auto-memory directory is an empty
// report (the healthy state), not an error.
func lintProject(projectPath, claudeDir string) (memlint.Report, error) {
	rep, err := memlint.Lint(projectPath, claudeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return memlint.Report{
				Dir:      memconsolidate.AutoMemoryDirIn(claudeDir, projectPath),
				Project:  projectPath,
				Findings: []memlint.Finding{},
			}, nil
		}
		return rep, err
	}
	return rep, nil
}

// printLintReport writes one project's block:
//
//	memory lint /path/to/project  (3 files, 7 claims, 3 stale)
//	  model-lineup.md:12  PR #366 claimed open — merged 2026-09-22 (2151242)
func printLintReport(rep memlint.Report) {
	if rep.Files == 0 {
		fmt.Printf("memory lint %s  (no auto-memory at %s)\n", rep.Project, rep.Dir)
		return
	}
	fmt.Printf("memory lint %s  (%d %s, %d %s, %d stale)\n",
		rep.Project, rep.Files, plural(rep.Files, "file"), rep.Claims, plural(rep.Claims, "claim"), len(rep.Findings))
	for _, f := range rep.Findings {
		fmt.Printf("  %s:%d  PR #%d claimed open — merged %s (%s)\n",
			f.File, f.LineNo, f.PR, f.MergedDate(), f.ShortSHA())
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// projectRow is one non-archived project as the memory verbs see it.
type projectRow struct {
	Path string
	Slug string
}

// nonArchivedProjects lists every project the daemon still tracks, in id
// order. Shared by the memory verbs' --all paths (lint today; consolidate --all
// in the weekly review) so they iterate the same set the advisor's R10/R13 do.
func nonArchivedProjects(db *sql.DB) ([]projectRow, error) {
	rows, err := db.Query(`SELECT path, slug FROM projects WHERE archived = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []projectRow
	for rows.Next() {
		var p projectRow
		if err := rows.Scan(&p.Path, &p.Slug); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
