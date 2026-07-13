// Command swarmery is the control-plane daemon CLI:
//
//	swarmery ingest <file.jsonl>   parse one transcript into the local DB
//	swarmery backfill              one-shot full scan of the projects root
//	swarmery serve                 serve the API/SPA + live ingest pipeline
//	swarmery recost                recompute cost_usd for all turns
//	swarmery install               launchd auto-start (uninstall / status)
//	swarmery hook <event>          runtime shim invoked by Claude Code hooks
//	swarmery hooks <cmd>           manage hook entries in project settings
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cost"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/hookcfg"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/hookshim"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/installer"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
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
	case "wscan":
		err = cmdWscan(os.Args[2:])
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
  swarmery recost   [--db <path>]
  swarmery wscan    [--db <path>] [--workspace-root <dir>]   one-shot workspace scan
  swarmery sysscan  [--db <path>] [--claude-dir <dir>] [--overlays-dir <dir>]
                                   one-shot system-config scan (agents/skills/hooks/commands)
  swarmery install  [--port <n>]   launchd auto-start
  swarmery uninstall               remove launchd service (keeps logs+db)
  swarmery status                  service health, pid, uptime, db size
  swarmery hook <permission-request|stop>          Claude Code hook shim (reads stdin)
  swarmery hooks <install|uninstall|status> [--project <path>] [--all] [--port <n>]
  env: SWARMERY_PORT, SWARMERY_PROJECTS_ROOT, SWARMERY_PRICING, SWARMERY_EXCLUDE`)
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
		"agent-work.sh workspace repo to index (env: AGENT_WORKSPACE_ROOT)")
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

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := dbFlag(fs)
	port := fs.Int("port", envPort(), "HTTP port (env: SWARMERY_PORT)")
	// D4 hardening: loopback by default; --bind is the conscious override.
	bind := fs.String("bind", "127.0.0.1", "listen address (default loopback; set explicitly to expose beyond this machine)")
	noIngest := fs.Bool("no-ingest", false, "serve the API only, without the live ingest pipeline")
	approvalTimeout := fs.Duration("approval-timeout", approvals.DefaultTimeout,
		"how long a permission request stays pending before fail-open expiry")
	cfg := pipelineFlags(fs)
	wsCfg := wsingestFlags(fs)
	sysCfg := sysscanFlags(fs)
	fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var bus *ingest.Bus
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
		sys := sysscan.New(db, *sysCfg, bus)
		go func() {
			if err := sys.Run(context.Background()); err != nil && err != context.Canceled {
				log.Printf("error: sysscan scanner stopped: %v", err)
			}
		}()
		log.Printf("swarmery system scanner watching %s (rescan %s)", sysCfg.ClaudeDir, sysscan.DefaultRescanInterval)
	}

	// phase 2: approvals — long-poll registry + expiry sweeper + heartbeat.
	svc := approvals.New(db, bus, approvals.Options{
		Timeout:    *approvalTimeout,
		Thresholds: cfg.Thresholds,
		Exclude:    cfg.Exclude,
	})
	api.AttachApprovals(svc)
	go svc.RunSweeper(context.Background())

	// phase 4: system — GET /api/system/overlays reads overlays/*/project.json
	// live from this dir on every request (empty disables the listing).
	api.AttachOverlaysDir(sysCfg.OverlaysDir)

	handler, err := api.NewServer(db, !*noIngest)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(*bind, strconv.Itoa(*port))
	log.Printf("swarmery serving on http://%s (db: %s)", addr, *dbPath)
	return http.ListenAndServe(addr, handler)
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
	})
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
