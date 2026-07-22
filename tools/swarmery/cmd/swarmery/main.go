// Command swarmery is the control-plane daemon CLI:
//
//	swarmery ingest <file.jsonl>   parse one transcript into the local DB
//	swarmery backfill              one-shot full scan of the projects root
//	swarmery serve                 serve the API/SPA + live ingest pipeline
//	swarmery recost                recompute cost_usd for all turns
//	swarmery backup                write a VACUUM-INTO snapshot of the DB
//	swarmery prune                 retention: roll up + delete old sessions' raw rows
//	swarmery install               launchd auto-start (uninstall / status)
//	swarmery hook <event>          runtime shim invoked by Claude Code hooks
//	swarmery hooks <cmd>           manage hook entries in project settings
//	swarmery onboard <slug>        bootstrap a consumer project (.claude + workspace)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cost"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/evals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/hookcfg"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/hookshim"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/installer"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/notify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/onboard"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/prune"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/toolproc"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

const defaultPort = 7777

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = cmdIngest(os.Args[2:])
	case "backfill":
		err = cmdBackfill(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "recost":
		err = cmdRecost(os.Args[2:])
	case "backup":
		err = cmdBackup(os.Args[2:])
	case "prune":
		err = cmdPrune(os.Args[2:])
	case "wscan":
		err = cmdWscan(os.Args[2:])
	case "evals-import":
		err = cmdEvalsImport(os.Args[2:])
	case "sysscan":
		err = cmdSysscan(os.Args[2:])
	case "install":
		err = installer.CmdInstall(os.Args[2:])
	case "uninstall":
		err = installer.CmdUninstall(os.Args[2:])
	case "status":
		err = installer.CmdStatus(os.Args[2:])
	case "hook":
		// Runtime shim: NEVER fails (fail-open D3) — exit code is always 0.
		os.Exit(cmdHook(os.Args[2:]))
	case "hooks":
		err = hookcfg.Cmd(os.Args[2:])
	case "onboard":
		err = cmdOnboard(os.Args[2:])
	case "offboard":
		err = cmdOffboard(os.Args[2:])
	case "attach":
		err = cmdAttach(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("error: %v", err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  swarmery ingest   [--db <path>] <file.jsonl>
  swarmery backfill [--db <path>] [--projects-root <dir>] [--rebuild-text]
  swarmery serve    [--db <path>] [--port <n>] [--bind <addr>] [--projects-root <dir>]
                    [--rescan <dur>] [--status-tick <dur>] [--approval-timeout <dur>]
                    [--active-window <dur>] [--idle-window <dur>] [--no-ingest]
                    [--exclude-projects <globs>]  (default '/tmp/*,/private/tmp/*')
                    [--answer-delivery <updated-input|deny-message>]
                    [--notify-url <url>] [--notify-events <list>] [--notify-template <generic|ntfy|telegram>]
                    [--notify-telegram-chat <id>]
  swarmery recost   [--db <path>]
  swarmery backup   [--db <path>] [--out <path>]   VACUUM-INTO snapshot (safe while serving)
  swarmery prune    [--db <path>] --older-than <Nd> [--dry-run]
                                   retention: write daily_rollups for sessions ended > Nd ago,
                                   delete their events/file_changes/turns (headers kept, pruned=1),
                                   VACUUM at the end; --dry-run prints per-table counts only
  swarmery wscan    [--db <path>] [--workspace-root <dir>]   one-shot workspace scan
  swarmery evals-import [--db <path>] --agent <name> <results.json>
                                   import a promptfoo results.json as an eval run for a
                                   registry agent (idempotent per suite + started_at)
  swarmery sysscan  [--db <path>] [--claude-dir <dir>] [--overlays-dir <dir>]
                                   one-shot system-config scan (agents/skills/hooks/commands)
  swarmery install  [--port <n>] [--onboard-roots <dirs>] [--workspace-root <dir>] [--statusline-src <dir>]
                                   launchd auto-start; bakes SWARMERY_* into the plist's EnvironmentVariables
                                   (--onboard-roots enables POST /api/projects/onboard + the dashboard button)
  swarmery uninstall               remove launchd service (keeps logs+db)
  swarmery status                  service health, pid, uptime, db size
  swarmery hook <permission-request|stop>          Claude Code hook shim (reads stdin)
  swarmery hooks <install|uninstall|status> [--project <path>] [--all] [--port <n>]
  swarmery onboard <slug> [pack ...] [--dir <path>] [--workspace-root <path>] [--statusline-src <path>]
                                   bootstrap a consumer project: .claude/settings.json +
                                   project.json skeleton + workspace namespace (idempotent;
                                   the statusline is opt-in — deployed + wired only with --statusline-src)
  swarmery offboard [slug] [--dir <path>] [--dry-run]
                                   detach swarmery from a project: prune the swarmery-owned
                                   entries from .claude/settings.json (backs up to .bak; idempotent)
  swarmery attach   [--dir <path>] [--workspace-root <path>] [--statusline-src <path>] [--dry-run]
                                   re-enable a detached project: merge the swarmery entries back
                                   into settings.json, restore project.json from .bak, reinstall
                                   hooks (idempotent; the inverse of offboard)
  env: SWARMERY_PORT, SWARMERY_PROJECTS_ROOT, SWARMERY_PRICING, SWARMERY_EXCLUDE, SWARMERY_WORKSPACE_ROOT
       SWARMERY_ONBOARD_ROOTS (comma-separated allow-list; enables POST /api/projects/onboard), SWARMERY_STATUSLINE_SRC
       SWARMERY_NOTIFY_URL, SWARMERY_NOTIFY_EVENTS, SWARMERY_NOTIFY_TEMPLATE, SWARMERY_NOTIFY_TELEGRAM_CHAT`)
}

// defaultProjectsRoot resolves SWARMERY_PROJECTS_ROOT, falling back to
// ~/.claude/projects.
func defaultProjectsRoot() string {
	if v := os.Getenv("SWARMERY_PROJECTS_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude/projects"
	}
	return home + "/.claude/projects"
}

func pipelineFlags(fs *flag.FlagSet) *ingest.Config {
	cfg := &ingest.Config{Exclude: defaultExclude()}
	fs.StringVar(&cfg.ProjectsRoot, "projects-root", defaultProjectsRoot(),
		"Claude Code projects root to ingest (env: SWARMERY_PROJECTS_ROOT)")
	fs.DurationVar(&cfg.RescanInterval, "rescan", 2*time.Second, "fallback rescan interval")
	fs.DurationVar(&cfg.StatusInterval, "status-tick", 10*time.Second, "session-status recompute interval")
	fs.DurationVar(&cfg.Thresholds.Active, "active-window", 2*time.Minute, "session considered active within this window")
	fs.DurationVar(&cfg.Thresholds.Idle, "idle-window", 30*time.Minute, "session considered idle within this window")
	fs.Var(&cfg.Exclude, "exclude-projects",
		"comma-separated path globs never tracked as projects (env: SWARMERY_EXCLUDE; '' disables)")
	return cfg
}

// defaultExclude resolves SWARMERY_EXCLUDE, falling back to the throwaway-dir
// default. An explicitly EMPTY env value disables exclusion.
func defaultExclude() ingest.ExcludeList {
	if v, ok := os.LookupEnv("SWARMERY_EXCLUDE"); ok {
		return ingest.ParseExcludeList(v)
	}
	return ingest.ParseExcludeList(ingest.DefaultExclude)
}

func dbFlag(fs *flag.FlagSet) *string {
	def, err := store.DefaultDBPath()
	if err != nil {
		def = "swarmery.db"
	}
	return fs.String("db", def, "path to the SQLite database")
}

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	dbPath := dbFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: swarmery ingest [--db <path>] <file.jsonl>")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	stats, err := ingest.File(db, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("ingested %s\n  projects: %d created\n  sessions: %d created\n  turns: %d created\n  events: %d created\n  file_changes: %d created\n  skipped lines: %d\n",
		fs.Arg(0), stats.Projects, stats.Sessions, stats.Turns, stats.Events, stats.FileChanges, stats.SkippedLines)
	return nil
}

// cmdRecost recomputes turns.cost_usd for every turn from stored usage and
// the current pricing table — run it after changing config/pricing.json.
// Idempotent: converges to the same values on every run.
func cmdRecost(args []string) error {
	fs := flag.NewFlagSet("recost", flag.ExitOnError)
	dbPath := dbFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery recost [--db <path>]")
	}

	if port := envPort(); daemonRunning(port) {
		log.Printf("warn: a swarmery daemon appears to be running on port %d — recost writes to the same WAL; concurrent ingest may interleave (busy_timeout handles locking, but consider stopping the daemon first)", port)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	stats, err := cost.Recost(db, cost.Default())
	if err != nil {
		return err
	}
	fmt.Printf("recost %s\n  turns examined: %d\n  priced: %d\n  unpriced (unknown model → NULL): %d\n  no usage (user turns → NULL): %d\n",
		*dbPath, stats.Total, stats.Priced, stats.Unpriced, stats.NoUsage)
	return nil
}

// cmdBackup writes a consistent snapshot of the database to a standalone file
// via SQLite VACUUM INTO. Safe to run while the daemon is serving (brief read
// lock, no downtime). Restore is a stop-copy-start: stop the daemon, copy the
// snapshot back over the live --db path, restart (see tools/swarmery/README.md).
func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := dbFlag(fs)
	out := fs.String("out", "",
		"snapshot output path (default: <db-dir>/backups/swarmery-<timestamp>.db)")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery backup [--db <path>] [--out <path>]")
	}

	dest := *out
	if dest == "" {
		dest = filepath.Join(filepath.Dir(*dbPath), "backups",
			fmt.Sprintf("swarmery-%s.db", time.Now().Format("20060102-150405")))
	}

	size, err := store.Backup(*dbPath, dest)
	if err != nil {
		return err
	}
	fmt.Printf("backup %s -> %s (%d bytes)\n", *dbPath, dest, size)
	return nil
}

// cmdPrune implements retention — see internal/prune. --older-than is
// REQUIRED (a destructive default would be a foot-gun); --dry-run prints the
// per-table candidate counts and writes nothing.
func cmdPrune(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	dbPath := dbFlag(fs)
	olderThan := fs.String("older-than", "",
		"retention window, e.g. 90d — prune sessions that ENDED more than this long ago (required)")
	dryRun := fs.Bool("dry-run", false, "count what would be pruned per table; write nothing")
	fs.Parse(args)
	if fs.NArg() != 0 || *olderThan == "" {
		return fmt.Errorf("usage: swarmery prune [--db <path>] --older-than <Nd> [--dry-run]")
	}
	days, err := parseRetentionDays(*olderThan)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02T15:04:05.000Z")

	if port := envPort(); daemonRunning(port) {
		log.Printf("warn: a swarmery daemon appears to be running on port %d — prune deletes rows and VACUUMs the same WAL; prefer stopping the daemon first", port)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	st, err := prune.Run(db, cutoff, *dryRun)
	if err != nil {
		return err
	}
	mode := "pruned"
	if st.DryRun {
		mode = "would prune (dry-run)"
	}
	fmt.Printf("prune %s (cutoff %s)\n  %s:\n  sessions marked: %d\n  turns: %d\n  events: %d\n  file_changes: %d\n  daily_rollups rows written: %d\n",
		*dbPath, st.Cutoff, mode, st.Sessions, st.Turns, st.Events, st.FileChanges, st.RollupRows)
	// A post-commit VACUUM failure (e.g. SQLITE_BUSY from a live daemon) is
	// only about disk space — the prune itself committed. Warn, exit 0.
	if st.VacuumErr != nil {
		log.Printf("warn: vacuum failed (space not reclaimed, data pruned OK): %v", st.VacuumErr)
	}
	return nil
}

// parseRetentionDays parses "90d" → 90. Days are the only supported unit:
// retention is a calendar policy, not duration arithmetic.
func parseRetentionDays(s string) (int, error) {
	v, ok := strings.CutSuffix(s, "d")
	if !ok {
		return 0, fmt.Errorf("--older-than wants <N>d (e.g. 90d), got %q", s)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("--older-than wants a positive day count, got %q", s)
	}
	return n, nil
}

// daemonRunning probes the local API port to detect a live daemon.
func daemonRunning(port int) bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/projects", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func cmdBackfill(args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	dbPath := dbFlag(fs)
	rebuildText := fs.Bool("rebuild-text", false,
		"re-read all transcripts from byte 0 to fill turns.text for pre-0005 rows (idempotent; dedup absorbs the replay)")
	cfg := pipelineFlags(fs)
	fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := os.Stat(cfg.ProjectsRoot); err != nil {
		return fmt.Errorf("projects root: %w", err)
	}
	if *rebuildText {
		stats := ingest.RebuildText(context.Background(), db, cfg.ProjectsRoot)
		fmt.Printf("rebuild-text %s\n  transcripts re-read: %d\n  errors: %d\n",
			cfg.ProjectsRoot, stats.Files, stats.Errors)
		return nil
	}
	ingest.NewPipeline(db, *cfg, nil).Backfill(context.Background())
	return nil
}

// wsingestFlags registers the phase-3.5 workspace-scanner flags.
func wsingestFlags(fs *flag.FlagSet) *wsingest.Config {
	cfg := &wsingest.Config{}
	fs.StringVar(&cfg.WorkspaceRoot, "workspace-root", wsingest.Root(),
		"agent-work.sh workspace repo to index (env: AGENT_WORKSPACE_ROOT, SWARMERY_WORKSPACE_ROOT)")
	return cfg
}

// cmdWscan runs one workspace scan pass — the CLI twin of the periodic
// scanner inside serve (phase 3.5: workspaces). READ-ONLY on the workspace.
func cmdWscan(args []string) error {
	fs := flag.NewFlagSet("wscan", flag.ExitOnError)
	dbPath := dbFlag(fs)
	wsCfg := wsingestFlags(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery wscan [--db <path>] [--workspace-root <dir>]")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	stats, err := wsingest.New(db, *wsCfg).Scan()
	if err != nil {
		return err
	}
	fmt.Printf("wscan %s\n  %s\n", wsCfg.WorkspaceRoot, stats)
	return nil
}

// cmdEvalsImport imports one promptfoo results.json as an eval run for a
// registry agent — see internal/evals. Unknown agents are a hard error;
// re-importing the same run is a friendly skip.
func cmdEvalsImport(args []string) error {
	fs := flag.NewFlagSet("evals-import", flag.ExitOnError)
	dbPath := dbFlag(fs)
	agent := fs.String("agent", "", "registry agent name the results belong to (required)")
	fs.Parse(args)
	if fs.NArg() != 1 || *agent == "" {
		return fmt.Errorf("usage: swarmery evals-import [--db <path>] --agent <name> <results.json>")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	res, err := evals.Import(db, *agent, fs.Arg(0))
	if err != nil {
		return err
	}
	if res.Skipped {
		fmt.Printf("skipped: run already imported (agent %s, suite %q, started %s — run #%d)\n",
			res.Agent, res.Suite, res.StartedAt, res.RunID)
		return nil
	}
	fmt.Printf("evals-import %s\n  agent: %s\n  suite: %s\n  run: #%d (started %s)\n  cases: %d (passed %d, failed %d)\n",
		fs.Arg(0), res.Agent, res.Suite, res.RunID, res.StartedAt, res.Cases, res.Passed, res.Failed)
	return nil
}

// sysscanFlags registers the phase-4 system-config scanner flags.
func sysscanFlags(fs *flag.FlagSet) *sysscan.Config {
	cfg := &sysscan.Config{}
	fs.StringVar(&cfg.ClaudeDir, "claude-dir", sysscan.DefaultClaudeDir(),
		"Claude Code config dir to scan (agents/skills/commands/settings + plugin cache)")
	fs.StringVar(&cfg.OverlaysDir, "overlays-dir", "",
		"overlays/ dir listed in the scan report (optional)")
	return cfg
}

// cmdSysscan runs one system-config scan pass — the CLI twin of the periodic
// scanner inside serve (phase 4: system registry). READ-ONLY on ~/.claude
// and every project's .claude/.
func cmdSysscan(args []string) error {
	fs := flag.NewFlagSet("sysscan", flag.ExitOnError)
	dbPath := dbFlag(fs)
	sysCfg := sysscanFlags(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery sysscan [--db <path>] [--claude-dir <dir>] [--overlays-dir <dir>]")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	stats, err := sysscan.New(db, *sysCfg, nil).Scan()
	if err != nil {
		return err
	}
	fmt.Printf("sysscan %s\n  %s\n", sysCfg.ClaudeDir, stats)
	for _, p := range stats.Overlays {
		fmt.Printf("  overlay: %s\n", p)
	}

	// Step-04 post-pass: re-evaluate the config lint rules against the
	// registry this scan just converged (writes config_lint_findings only).
	lint, err := sysscan.Lint(db, *sysCfg)
	if err != nil {
		return err
	}
	fmt.Printf("  lint: %s\n", lint)
	return nil
}

// defaultWorkspaceRoot resolves SWARMERY_WORKSPACE_ROOT (the same env
// scripts/init.sh reads), falling back to the self-hosted default so the CLI
// and the script stay behaviourally identical.
func defaultWorkspaceRoot() string {
	if v := os.Getenv("SWARMERY_WORKSPACE_ROOT"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "swarmery-workspace")
	}
	return "swarmery-workspace"
}

// onboardRoots parses SWARMERY_ONBOARD_ROOTS (comma-separated parent dirs) into
// the allow-list that fences POST /api/projects/onboard. Empty/unset ⇒ the
// endpoint is disabled — writing .claude/ into an arbitrary path is opt-in.
func onboardRoots() []string {
	v := os.Getenv("SWARMERY_ONBOARD_ROOTS")
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cmdOnboard bootstraps a new consumer project via the shared onboard package —
// the CLI twin of the control-plane onboarding endpoint and the delegation
// target of scripts/init.sh when this binary is on PATH.
func cmdOnboard(args []string) error {
	fs := flag.NewFlagSet("onboard", flag.ExitOnError)
	dir := fs.String("dir", "", "project root to bootstrap (default: current directory)")
	wsRoot := fs.String("workspace-root", defaultWorkspaceRoot(),
		"shared workspace repo root (env: SWARMERY_WORKSPACE_ROOT)")
	statuslineSrc := fs.String("statusline-src", "",
		"plugins/core/statusline dir to deploy the statusline from (opt-in: also wires statusLine in settings.json; off by default)")

	// The natural invocation is `onboard <slug> [packs...] [flags...]`, but the
	// flag package stops at the first positional. Split leading positionals
	// (never dash-prefixed) from the flag tail so both orderings work.
	positional, flagArgs := splitPositional(args)
	fs.Parse(flagArgs)
	if len(positional) < 1 {
		return fmt.Errorf("usage: swarmery onboard <slug> [pack ...] [--dir <path>] [--workspace-root <path>] [--statusline-src <path>]\n  packs: %v", onboard.KnownPacks)
	}

	projectDir := *dir
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		projectDir = cwd
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	res, err := onboard.Run(onboard.Config{
		Slug:          positional[0],
		ProjectDir:    abs,
		Packs:         positional[1:],
		WorkspaceRoot: *wsRoot,
		StatuslineSrc: *statuslineSrc,
	})
	if err != nil {
		return err
	}
	for _, s := range res.Steps {
		fmt.Println(s)
	}
	fmt.Printf("\nNext: open a FRESH Claude Code session in %s\n", abs)
	fmt.Println("      → accept the 'swarmery' marketplace trust prompt → plugins install.")
	fmt.Println("      Fill in .claude/project.json TODOs so agents know your repos/stack.")
	return nil
}

// cmdOffboard removes the swarmery-owned entries from a project's
// .claude/settings.json — the CLI twin of POST /api/projects/{id}/detach and
// the inverse of `swarmery onboard`. It delegates to onboard.Detach so both
// surfaces prune identically. --dry-run prints the plan without writing.
func cmdOffboard(args []string) error {
	fs := flag.NewFlagSet("offboard", flag.ExitOnError)
	dir := fs.String("dir", "", "project root to detach (default: current directory)")
	dryRun := fs.Bool("dry-run", false, "print what would be removed without writing")

	positional, flagArgs := splitPositional(args)
	fs.Parse(flagArgs)

	projectDir := *dir
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		projectDir = cwd
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	// An optional leading slug guards env.AGENT_PROJECT pruning (removed only
	// when it matches); omitting it removes the var regardless.
	slug := ""
	if len(positional) > 0 {
		slug = positional[0]
	}

	res, err := onboard.Detach(onboard.DetachConfig{
		ProjectDir:    abs,
		Slug:          slug,
		WorkspaceRoot: defaultWorkspaceRoot(),
		DryRun:        *dryRun,
	})
	if err != nil {
		return err
	}
	for _, s := range res.Steps {
		fmt.Println(s)
	}
	return nil
}

// cmdAttach re-enables swarmery for a detached project — the CLI twin of
// POST /api/projects/{id}/attach and the inverse of `swarmery offboard`. It
// delegates to onboard.Attach (merge-only settings surgery, project.json
// restore from .bak, statusline redeploy) and then reinstalls the approvals
// hooks. --dry-run prints the plan without writing.
func cmdAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", "", "project root to attach (default: current directory)")
	wsRoot := fs.String("workspace-root", defaultWorkspaceRoot(),
		"shared workspace repo root (env: SWARMERY_WORKSPACE_ROOT)")
	statuslineSrc := fs.String("statusline-src", os.Getenv("SWARMERY_STATUSLINE_SRC"),
		"plugins/core/statusline dir to copy statusline scripts from (env: SWARMERY_STATUSLINE_SRC)")
	dryRun := fs.Bool("dry-run", false, "print what would be restored without writing")
	fs.Parse(args)

	projectDir := *dir
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		projectDir = cwd
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	res, err := onboard.Attach(onboard.AttachConfig{
		ProjectDir:    abs,
		WorkspaceRoot: *wsRoot,
		StatuslineSrc: *statuslineSrc,
		DryRun:        *dryRun,
	})
	if err != nil {
		return err
	}
	for _, s := range res.Steps {
		fmt.Println(s)
	}
	if *dryRun {
		return nil
	}

	// Hooks live in settings.local.json, outside onboard's remit — reinstall
	// them here like `swarmery hooks install` would.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	if err := (&hookcfg.System{Home: home, Out: os.Stdout}).Install(abs, 0); err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	if res.Attached {
		fmt.Printf("\nNext: open a FRESH Claude Code session in %s\n", abs)
		fmt.Println("      → accept the 'swarmery' marketplace trust prompt → plugins install.")
	}
	return nil
}

// splitPositional partitions args into the leading run of positional tokens and
// the remaining flag tail (everything from the first dash-prefixed token on).
func splitPositional(args []string) (positional, flags []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			return args[:i], args[i:]
		}
	}
	return args, nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := dbFlag(fs)
	port := fs.Int("port", envPort(), "HTTP port (env: SWARMERY_PORT)")
	// D4 hardening: loopback by default; --bind is the conscious override.
	bind := fs.String("bind", "127.0.0.1", "listen address (default loopback; set explicitly to expose beyond this machine)")
	noIngest := fs.Bool("no-ingest", false, "serve the API only, without the live ingest pipeline")
	approvalTimeout := fs.Duration("approval-timeout", envApprovalTimeout(),
		"how long a permission request stays answerable from the dashboard before fail-open to the terminal prompt (env: SWARMERY_APPROVAL_TIMEOUT)")
	answerDelivery := fs.String("answer-delivery", approvals.DeliveryUpdatedInput,
		"AskUserQuestion dashboard-answer wire form: updated-input (hook updatedInput injection, spike-verified default) or deny-message (fallback: deny carrying the answers as the message)")
	notifyURL := fs.String("notify-url", os.Getenv("SWARMERY_NOTIFY_URL"),
		"webhook URL to POST notifications to (env: SWARMERY_NOTIFY_URL; empty disables). NOTE: bodies include project names and tool arguments — point this only at receivers you trust")
	notifyEvents := fs.String("notify-events", envOr("SWARMERY_NOTIFY_EVENTS", notify.EventApprovalRequested),
		"comma-separated events to send: approval_requested, approval_expired, session_completed, session_error (env: SWARMERY_NOTIFY_EVENTS)")
	notifyTemplate := fs.String("notify-template", envOr("SWARMERY_NOTIFY_TEMPLATE", notify.TemplateGeneric),
		"webhook body template: generic (raw JSON) | ntfy (text body + Title/Priority/Tags headers) | telegram (Bot API sendMessage JSON) (env: SWARMERY_NOTIFY_TEMPLATE)")
	notifyTelegramChat := fs.String("notify-telegram-chat", os.Getenv("SWARMERY_NOTIFY_TELEGRAM_CHAT"),
		"Telegram chat_id, required with --notify-template=telegram (env: SWARMERY_NOTIFY_TELEGRAM_CHAT)")
	cfg := pipelineFlags(fs)
	wsCfg := wsingestFlags(fs)
	sysCfg := sysscanFlags(fs)
	fs.Parse(args)
	if *answerDelivery != approvals.DeliveryUpdatedInput && *answerDelivery != approvals.DeliveryDenyMessage {
		return fmt.Errorf("--answer-delivery must be %q or %q, got %q",
			approvals.DeliveryUpdatedInput, approvals.DeliveryDenyMessage, *answerDelivery)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// control-plane v2: outbound webhook notifier (nil = disabled; Emit is
	// nil-receiver-safe everywhere it is wired). Built before the pipeline so
	// cfg.OnSessionTerminal is set when NewPipeline copies the config.
	var notifier *notify.Notifier
	if *notifyURL != "" {
		notifier, err = notify.New(notify.Config{
			URL:          *notifyURL,
			Events:       strings.Split(*notifyEvents, ","),
			Template:     *notifyTemplate,
			TelegramChat: *notifyTelegramChat,
		})
		if err != nil {
			return fmt.Errorf("notify config: %w", err)
		}
		log.Printf("swarmery notifier posting [%s] to %s (template %s)",
			*notifyEvents, *notifyURL, *notifyTemplate)
		cfg.OnSessionTerminal = func(sessionID int64, errorCount int) {
			evt, err := notify.SessionEvent(db, sessionID, errorCount)
			if err != nil {
				log.Printf("warn: notify: session event %d: %v", sessionID, err)
				return
			}
			notifier.Emit(evt)
		}
	}

	var bus *ingest.Bus
	var sys *sysscan.Scanner
	if !*noIngest {
		bus = ingest.NewBus()
		api.AttachBus(bus)
		pipeline := ingest.NewPipeline(db, *cfg, bus)
		go func() {
			if err := pipeline.Run(context.Background()); err != nil && err != context.Canceled {
				log.Printf("error: ingest pipeline stopped: %v", err)
			}
		}()
		log.Printf("swarmery ingest pipeline watching %s (rescan %s)", cfg.ProjectsRoot, cfg.RescanInterval)

		// phase 3.5: workspaces — read-only periodic scan of the agent-work.sh
		// workspace repo (tasks + task↔session links). Missing root is not
		// fatal: the scanner logs and keeps ticking.
		scanner := wsingest.New(db, *wsCfg)
		go func() {
			if err := scanner.Run(context.Background()); err != nil && err != context.Canceled {
				log.Printf("error: wsingest scanner stopped: %v", err)
			}
		}()
		log.Printf("swarmery workspace scanner watching %s (rescan %s)", wsCfg.WorkspaceRoot, wsingest.DefaultRescanInterval)

		// phase 4: system registry — read-only scanner of the agent-system
		// config (agents/skills/hooks/commands) with fsnotify + periodic
		// rescan. Never writes to ~/.claude or any project's .claude/.
		sys = sysscan.New(db, *sysCfg, bus)
		go func() {
			if err := sys.Run(context.Background()); err != nil && err != context.Canceled {
				log.Printf("error: sysscan scanner stopped: %v", err)
			}
		}()
		log.Printf("swarmery system scanner watching %s (rescan %s)", sysCfg.ClaudeDir, sysscan.DefaultRescanInterval)

		// phase 4 Stage 2 (step-10): hooks toggle/edit go through the sysedit
		// pipeline against the same scanner instance (the post-write rescan
		// converges the registry). Without ingest there is no scanner, so the
		// endpoints stay detached and serve 503.
		api.AttachHookEditor(sysedit.New(db, sys, sysedit.Config{ClaudeDir: sysCfg.ClaudeDir}))
	}

	// process liveness — checks active/idle sessions every 30 s, fast-forwards
	// dead ones to status='completed', publishes session_updated when proc_state
	// changes so the UI picks up orphan/dead badges in real time.
	pw := &procwatch.Ticker{
		DB:       db,
		Provider: procwatch.OsProvider{},
		Interval: 30 * time.Second,
		OnStateChange: func(id int64) {
			if bus != nil {
				bus.Publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: id})
			}
		},
	}
	go pw.Run(context.Background())
	log.Printf("swarmery procwatch ticker started (interval 30s)")

	// retro phase 3: the advisor rule engine — deterministic recommendations
	// (R1..R6) refreshed once at startup and every 24h, plus on demand via
	// POST /api/retro/advise. Works purely off the DB, so it runs with or
	// without ingest; failures are logged, never fatal.
	go func() {
		runAdvisor := func() {
			stats, err := advisor.Run(db, time.Now())
			if err != nil {
				log.Printf("error: advisor: %v", err)
				return
			}
			log.Printf("swarmery advisor: %s", stats)
		}
		runAdvisor()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runAdvisor()
		}
	}()
	log.Printf("swarmery advisor started (interval 24h)")

	// phase 2: approvals — long-poll registry + expiry sweeper + heartbeat.
	svc := approvals.New(db, bus, approvals.Options{
		Timeout:        *approvalTimeout,
		Thresholds:     cfg.Thresholds,
		Exclude:        cfg.Exclude,
		AnswerDelivery: *answerDelivery,
		Notifier:       notifier,
	})
	api.AttachApprovals(svc)
	go svc.RunSweeper(context.Background())

	// phase 4: system — GET /api/system/overlays reads overlays/*/project.json
	// live from this dir on every request (empty disables the listing).
	api.AttachOverlaysDir(sysCfg.OverlaysDir)

	// onboarding: POST /api/projects/onboard writes .claude/ into a
	// caller-supplied path, so it is opt-in and fenced to an explicit
	// allow-list. Empty SWARMERY_ONBOARD_ROOTS ⇒ the endpoint stays disabled.
	api.AttachOnboard(api.OnboardConfig{
		Roots:         onboardRoots(),
		WorkspaceRoot: defaultWorkspaceRoot(),
		StatuslineSrc: os.Getenv("SWARMERY_STATUSLINE_SRC"),
	})

	// project plugins: GET /api/projects/{id}/plugins reads the marketplace
	// clone under <claude-dir>/plugins/marketplaces/. Wire the same resolved
	// dir the sys scanner/editor uses so --claude-dir overrides apply here too.
	api.AttachPluginCatalog(sysCfg.ClaudeDir)

	// phase 4: system, Stage 2 (step-09) — the write surface for agents and
	// skills. Every write goes through the sysedit pipeline; the editor reuses
	// the live scanner for its post-write rescan (under --no-ingest a private
	// scanner instance converges the registry on demand instead).
	if sys == nil {
		sys = sysscan.New(db, *sysCfg, nil)
	}
	api.AttachSysEditor(sysedit.New(db, sys, sysedit.Config{ClaudeDir: sysCfg.ClaudeDir}))

	// tool dashboards (step 02): the daemon-owned serena process manager. The
	// signal handler below guarantees StopAll on shutdown, so no serena child
	// outlives the daemon.
	toolMgr := toolproc.NewManager(toolproc.Config{Command: toolproc.DefaultCommand})
	api.AttachToolManager(toolMgr)

	handler, err := api.NewServer(db, !*noIngest)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(*bind, strconv.Itoa(*port))
	log.Printf("swarmery serving on http://%s (db: %s)", addr, *dbPath)

	// Graceful shutdown: SIGINT/SIGTERM → stop tool children, drain HTTP, exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: handler}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		stop() // restore default signal handling so a second signal kills immediately
		log.Printf("swarmery shutting down: stopping tool processes")
		// Drain HTTP first so no in-flight handler (e.g. POST serena/start) can
		// register a child after StopAll — then stop every tool process.
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx) //nolint:errcheck // best-effort drain on the way out
		toolMgr.StopAll()
		// If ctx.Done won a race against a real ListenAndServe failure, surface it.
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		default:
		}
		return nil
	}
}

// cmdHook runs the `swarmery hook <event>` shim. It ALWAYS exits 0 (fail-open
// D3): any transport/daemon problem means "no decision", and Claude Code then
// shows its native permission dialog.
func cmdHook(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: swarmery hook <permission-request|stop>")
		return 0
	}
	logPath := ""
	if home, err := os.UserHomeDir(); err == nil {
		logPath = filepath.Join(home, ".swarmery", "hook.log")
	}
	return hookshim.Run(args[0], os.Stdin, hookshim.Config{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", envPort()),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		LogPath: logPath,
		// Keep the shim's long-poll in sync with the daemon's approval window
		// (both read SWARMERY_APPROVAL_TIMEOUT, else the shared baked default).
		PollTimeout: envApprovalTimeout(),
	})
}

// envApprovalTimeout resolves the approval window from SWARMERY_APPROVAL_TIMEOUT
// (a Go duration, e.g. "10m"), falling back to the baked default. Read by both
// the shim (poll wall clock) and serve (--approval-timeout default) so the two
// never drift.
func envApprovalTimeout() time.Duration {
	if v := os.Getenv("SWARMERY_APPROVAL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Printf("warn: ignoring invalid SWARMERY_APPROVAL_TIMEOUT=%q", v)
	}
	return approvals.DefaultTimeout
}

// envOr returns the env value when set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envPort() int {
	if v := os.Getenv("SWARMERY_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
		log.Printf("warn: ignoring invalid SWARMERY_PORT=%q", v)
	}
	return defaultPort
}
