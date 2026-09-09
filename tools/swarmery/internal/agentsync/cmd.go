package agentsync

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// Cmd implements `swarmery agents sync [--project <name|dir>] [--check]`.
//
// It returns a process exit code rather than an error because the exit code IS
// the contract: `--check` exits 1 on drift so CI can gate on it, and that has
// to stay distinguishable from "the command itself blew up".
func Cmd(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	sub := args[0]
	if sub != "sync" {
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n%s\n", sub, usage)
		return 2
	}

	fs := flag.NewFlagSet("agents sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "project name or directory (default: current directory)")
	check := fs.Bool("check", false, "report drift without writing; exit 1 when a generated file no longer matches upstream")
	claudeDir := fs.String("claude-dir", "", "Claude Code config dir holding the plugin install (default ~/.claude)")
	mkt := fs.String("marketplace", DefaultMarketplace, "marketplace name to resolve upstream agents from")
	dbPath := fs.String("db", "", "daemon DB path used to resolve --project by name (default: ~/.swarmery/swarmery.db)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	dir, err := ResolveProjectDir(*project, *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	opts := Options{ProjectDir: dir, ClaudeDir: *claudeDir, Marketplace: *mkt}

	if *check {
		return runCheck(os.Stdout, os.Stderr, opts)
	}
	return runSync(os.Stdout, os.Stderr, opts)
}

const usage = `usage:
  swarmery agents sync [--project <name|dir>] [--check] [--claude-dir <dir>]
                       [--marketplace <name>] [--db <path>]

Generates the project-local agent overrides declared in the project's
.claude/settings.json under swarmery.agents:

  "swarmery": { "agents": { "implementation-agent": { "isolation": "none" } } }

Each generated .claude/agents/<name>.md is a copy of the upstream pack agent
with exactly the isolation key changed, stamped with the upstream's source_sha.
--check writes nothing and exits 1 when a stamp no longer matches upstream.`

func runSync(out, errW io.Writer, opts Options) int {
	plans, err := Plans(opts)
	if err != nil {
		fmt.Fprintf(errW, "error: %v\n", err)
		return 2
	}
	if len(plans) == 0 {
		fmt.Fprintf(out, "%s: no swarmery.agents overrides declared — nothing to generate\n", opts.ProjectDir)
		return 0
	}
	if err := Apply(plans); err != nil {
		fmt.Fprintf(errW, "error: %v\n", err)
		return 2
	}
	for _, p := range plans {
		rel := relTo(opts.ProjectDir, p.Path)
		fmt.Fprintf(out, "%-9s %s  (%s=%s from %s %s)\n", p.Action, rel, isolationKey, p.Isolation, p.Source, p.SHA)
	}
	return 0
}

func runCheck(out, errW io.Writer, opts Options) int {
	drifts, err := Check(opts)
	if err != nil {
		fmt.Fprintf(errW, "error: %v\n", err)
		return 2
	}
	if len(drifts) == 0 {
		fmt.Fprintf(out, "%s: generated agents are in sync with upstream\n", opts.ProjectDir)
		return 0
	}
	for _, d := range drifts {
		fmt.Fprintf(errW, "drift: %s (%s): %s\n", d.Name, relTo(opts.ProjectDir, d.Path), d.Reason)
	}
	fmt.Fprintf(errW, "run `swarmery agents sync` to regenerate\n")
	return 1
}

func relTo(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil {
		return rel
	}
	return path
}

// ResolveProjectDir turns --project into a checkout directory. An existing
// directory is taken at face value; anything else is looked up as a project
// name/slug in the daemon DB, which is where `--project english-grammar` gets
// its meaning. Empty means the current directory.
func ResolveProjectDir(project, dbPath string) (string, error) {
	if project == "" {
		return os.Getwd()
	}
	if fi, err := os.Stat(project); err == nil && fi.IsDir() {
		return filepath.Abs(project)
	}
	if filepath.IsAbs(project) || project[0] == '.' {
		return "", fmt.Errorf("project directory %s does not exist", project)
	}
	return projectDirFromDB(project, dbPath)
}

func projectDirFromDB(name, dbPath string) (string, error) {
	if dbPath == "" {
		var err error
		if dbPath, err = store.DefaultDBPath(); err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(dbPath); err != nil {
		return "", fmt.Errorf("no project directory %q and no daemon DB to resolve it by name: %w", name, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var path string
	err = db.QueryRow(
		`SELECT path FROM projects WHERE archived = 0 AND (name = ? OR slug = ?) AND path LIKE '/%' ORDER BY last_activity DESC LIMIT 1`,
		name, name).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("project %q is neither a directory nor a known project in %s", name, dbPath)
	}
	if err != nil {
		return "", err
	}
	if fi, statErr := os.Stat(path); statErr != nil || !fi.IsDir() {
		return "", fmt.Errorf("project %q resolves to %s, which is not a directory", name, path)
	}
	return path, nil
}
