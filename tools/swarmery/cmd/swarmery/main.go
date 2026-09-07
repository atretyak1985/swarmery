// Command swarmery is the control-plane daemon CLI:
//
//	swarmery ingest <file.jsonl>   parse one transcript into the local DB
//	swarmery backfill              one-shot full scan of every projects root
//	swarmery serve                 serve the API/SPA + live ingest pipeline
//	swarmery recost                recompute cost_usd for all turns
//	swarmery economics             five token-economy metrics (read-only)
//	swarmery stale                 tasks claiming to run with no sign of it (read-only)
//	swarmery backup                write a VACUUM-INTO snapshot of the DB
//	swarmery prune                 retention: roll up + delete old sessions' raw rows
//	swarmery install               launchd auto-start (uninstall / status)
//	swarmery hook <event>          runtime shim invoked by Claude Code hooks
//	swarmery hooks <cmd>           manage hook entries in project settings
//	swarmery onboard <slug>        bootstrap a consumer project (.claude + workspace)
//	swarmery agents sync           regenerate the project-local agent overrides
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/agentsync"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cost"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/economics"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/evals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/extract"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/handoff"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/hookcfg"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/hookshim"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/installer"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/logbuf"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modeleval"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/notify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/onboard"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/playbooks"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/plugindrift"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/prune"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/routines"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runtruth"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/settingsoverlay"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/staleness"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskcap"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/term"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/toolproc"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/trajeval"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/trajjudge"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
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
	case "modeleval":
		err = cmdModelEval(os.Args[2:])
	case "routine":
		err = cmdRoutine(os.Args[2:])
	case "stale":
		err = cmdStale(os.Args[2:])
	case "economics":
		err = cmdEconomics(os.Args[2:])
	case "backup":
		err = cmdBackup(os.Args[2:])
	case "prune":
		err = cmdPrune(os.Args[2:])
	case "worktrees":
		err = cmdWorktrees(os.Args[2:])
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
		// fusion phase 9 (Console/DX): `status` now prints the live DAEMON
		// snapshot (health + today-stats + approvals) and exits 1 when the
		// daemon is unreachable, so it is usable in scripts. The launchd
		// service introspection the command used to print moved to
		// `service-status` (below) and stays reachable.
		os.Exit(cmdStatus(os.Args[2:]))
	case "service-status":
		err = installer.CmdStatus(os.Args[2:])
	case "console":
		err = cmdConsole(os.Args[2:])
	case "hook":
		// Runtime shim: NEVER fails (fail-open D3) — exit code is always 0.
		os.Exit(cmdHook(os.Args[2:]))
	case "hooks":
		err = hookcfg.Cmd(os.Args[2:])
	case "agents":
		// Exit code is the contract: `agents sync --check` exits 1 on drift so
		// CI can gate on it, which log.Fatalf's blanket 1 could not express.
		os.Exit(agentsync.Cmd(os.Args[2:]))
	case "onboard":
		err = cmdOnboard(os.Args[2:])
	case "offboard":
		err = cmdOffboard(os.Args[2:])
	case "attach":
		err = cmdAttach(os.Args[2:])
	case "account":
		err = cmdAccount(os.Args[2:])
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
  swarmery stale [--db <path>] [--project <id>] [--all]
  swarmery economics [--db <path>] [--since <YYYY-MM-DD>] [--until <YYYY-MM-DD>]
                    [--project <id>] [--json]
                                   token economy of the agent system: cost per completed task,
                                   cache efficiency, delegation cost, wasted work, model mix
                                   (read-only; safe while the daemon is serving)
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
                    [--projects-roots <dirs|auto>]
                                   launchd auto-start; bakes SWARMERY_* into the plist's EnvironmentVariables
                                   (--onboard-roots enables POST /api/projects/onboard + the dashboard button;
                                   --projects-roots auto makes every ~/.claude*/projects account visible)
  swarmery uninstall               remove launchd service (keeps logs+db)
  swarmery status   [--port <n>] [--url <base>]
                                   live daemon snapshot (version/uptime/db/migrations/
                                   ingest-lag + today sessions/cost/tokens + dispatch +
                                   approvals/ws-clients); exit 1 when the daemon is down
  swarmery console  [--port <n>] [--url <base>]
                                   interactive TUI attached to the daemon: live event
                                   feed + approvals (y/n) + dispatcher pause ([p])
  swarmery service-status          launchd service health: pid, uptime, db size
  swarmery hook <permission-request|stop>          Claude Code hook shim (reads stdin)
  swarmery hooks <install|uninstall|status> [--project <path>] [--all] [--port <n>]
  swarmery agents sync [--project <name|dir>] [--check] [--claude-dir <dir>] [--marketplace <name>]
                                   regenerate the .claude/agents/<name>.md overrides a project
                                   declares under swarmery.agents in its settings.json: the upstream
                                   pack agent with exactly the isolation key changed, stamped with
                                   the upstream source_sha; --check writes nothing and exits 1 when
                                   a stamp no longer matches upstream
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
  swarmery account list|which|use|clear|env|exec [--path <dir>]
                                   multi-account terminal surface: which account a project runs
                                   under, bind/clear it, print its env line, run a command under it
                                   (never contacts the daemon)
  env: SWARMERY_PORT, SWARMERY_PRICING, SWARMERY_EXCLUDE, SWARMERY_WORKSPACE_ROOT
       SWARMERY_PROJECTS_ROOTS (comma-separated transcript roots, one per Claude Code config dir;
       'auto' = every ~/.claude*/projects that exists — legacy singular: SWARMERY_PROJECTS_ROOT)
       SWARMERY_ONBOARD_ROOTS (comma-separated allow-list; enables POST /api/projects/onboard), SWARMERY_STATUSLINE_SRC
       SWARMERY_SETTINGS_OVERLAYS (descriptor of settings files that also apply to given project
       roots; default ~/.swarmery/overlays.json — missing = repo-only plugin detection)
       SWARMERY_NOTIFY_URL, SWARMERY_NOTIFY_EVENTS, SWARMERY_NOTIFY_TEMPLATE, SWARMERY_NOTIFY_TELEGRAM_CHAT`)
}

// autoProjectsRoots is the SWARMERY_PROJECTS_ROOTS value that means "find them
// yourself": every ~/.claude*/projects that exists. It covers the
// CLAUDE_CONFIG_DIR convention (~/.claude, ~/.claude-<account>, …) without the
// user having to enumerate accounts that differ per machine.
const autoProjectsRoots = "auto"

// defaultProjectsRoots resolves the ingest roots, in this order:
//
//  1. SWARMERY_PROJECTS_ROOTS (plural, comma-separated). The literal value
//     "auto" globs $HOME/.claude*/projects, keeps the existing dirs, sorted.
//  2. SWARMERY_PROJECTS_ROOT (singular, legacy) as a one-element list.
//  3. ~/.claude/projects alone.
//
// Configure nothing and you get exactly rule 3 — byte-identical to the
// single-root daemon this replaced.
func defaultProjectsRoots() []string {
	if v := os.Getenv("SWARMERY_PROJECTS_ROOTS"); v != "" {
		if v == autoProjectsRoots {
			// An auto scan that finds nothing falls through to the plain
			// default so failures name a concrete path ("~/.claude/projects
			// does not exist") instead of an empty list.
			if roots := claudeacct.ProjectsRoots(); len(roots) > 0 {
				return roots
			}
		} else if roots := splitRoots(v); len(roots) > 0 {
			return roots
		}
	}
	if v := os.Getenv("SWARMERY_PROJECTS_ROOT"); v != "" {
		return []string{v}
	}
	return []string{defaultClaudeProjectsRoot()}
}

// defaultClaudeProjectsRoot is the stock single root: ~/.claude/projects.
func defaultClaudeProjectsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude/projects"
	}
	return home + "/.claude/projects"
}

// splitRoots parses a comma-separated roots list: blanks dropped, order kept,
// duplicates collapsed (a doubled root would scan and log everything twice).
func splitRoots(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

// rootsFlag is the --projects-root flag value: comma-separated AND repeatable.
// The FIRST Set call REPLACES the env/HOME-derived default (an explicit flag
// must never silently inherit an env root), every later one appends — so
// `--projects-root a,b --projects-root c` yields [a b c].
type rootsFlag struct {
	vals     *[]string
	fromFlag bool
}

func (r *rootsFlag) String() string {
	if r == nil || r.vals == nil {
		return ""
	}
	return strings.Join(*r.vals, ",")
}

func (r *rootsFlag) Set(v string) error {
	parts := splitRoots(v)
	if len(parts) == 0 {
		return fmt.Errorf("--projects-root: no path in %q", v)
	}
	if !r.fromFlag {
		*r.vals = nil
		r.fromFlag = true
	}
	for _, p := range parts {
		if !slices.Contains(*r.vals, p) {
			*r.vals = append(*r.vals, p)
		}
	}
	return nil
}

func pipelineFlags(fs *flag.FlagSet) *ingest.Config {
	cfg := &ingest.Config{Exclude: defaultExclude(), ProjectsRoots: defaultProjectsRoots()}
	fs.Var(&rootsFlag{vals: &cfg.ProjectsRoots}, "projects-root",
		"Claude Code projects root(s) to ingest — comma-separated, repeatable "+
			"(env: SWARMERY_PROJECTS_ROOTS, 'auto' = every ~/.claude*/projects; "+
			"legacy singular: SWARMERY_PROJECTS_ROOT)")
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

// cmdModelEval aggregates the advisory trajectory judgments into one dated,
// gateable verdict for a SUBJECT model — the model the agents were running on,
// not the judge. See internal/modeleval for why those must stay distinct.
//
// Exits non-zero on a fail verdict so a routine step can branch on it. An
// inconclusive verdict exits 0: "not enough evidence yet" is not a failure, and
// a monthly routine should not go red because a model is new.
func cmdModelEval(args []string) error {
	fs := flag.NewFlagSet("modeleval", flag.ExitOnError)
	dbPath := dbFlag(fs)
	model := fs.String("model", "", "subject model id to evaluate (required)")
	setPath := fs.String("golden-set", "", "path to the golden set manifest "+
		"(default: testdata/goldenset/manifest.json next to the binary source)")
	asJSON := fs.Bool("json", false, "emit the result as JSON")
	fs.Parse(args)
	if *model == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery modeleval --model <id> [--golden-set <path>] [--json] [--db <path>]")
	}

	path := *setPath
	if path == "" {
		path = filepath.Join("testdata", "goldenset", "manifest.json")
	}
	gs, err := modeleval.LoadGoldenSet(path)
	if err != nil {
		return err
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	res, err := modeleval.Evaluate(db, gs, *model)
	if err != nil {
		return err
	}
	if err := modeleval.Persist(db, res, time.Now()); err != nil {
		return err
	}

	if *asJSON {
		out, err := json.MarshalIndent(map[string]any{
			"model": res.Model, "goldenSetVersion": res.GoldenSetVersion,
			"verdict": res.Verdict, "score": res.Score,
			"trajectories": res.Trajectories, "agentsCovered": res.AgentsCovered,
			"detail": res.Detail,
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	} else {
		fmt.Printf("modeleval %s (golden set %s)\n  verdict:      %s\n  score:        %.2f\n  trajectories: %d\n  agents:       %d covered\n  %s\n",
			res.Model, res.GoldenSetVersion, res.Verdict, res.Score,
			res.Trajectories, res.AgentsCovered, res.Detail)
	}

	if res.Verdict == "fail" {
		return fmt.Errorf("model %s failed the golden set", res.Model)
	}
	return nil
}

// cmdRoutine currently carries one subcommand: `seed`, which installs a routine
// definition shipped in the repo. A routine that lives only in one machine's
// SQLite is a local habit, not part of the system — and catching drift is
// exactly what local habits fail at.
func cmdRoutine(args []string) error {
	if len(args) == 0 || args[0] != "seed" {
		return fmt.Errorf("usage: swarmery routine seed --from <file> [--db <path>]")
	}
	fs := flag.NewFlagSet("routine seed", flag.ExitOnError)
	dbPath := dbFlag(fs)
	from := fs.String("from", "", "path to the routine definition JSON (required)")
	fs.Parse(args[1:])
	if *from == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery routine seed --from <file> [--db <path>]")
	}

	sf, err := routines.LoadSeedFile(*from)
	if err != nil {
		return err
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// nil Runner/TaskCreator: seeding writes the definition, it never executes
	// a step. The daemon wires the real ones when it schedules.
	svc := routines.NewService(db, nil, nil, true)
	r, created, err := svc.Seed(sf, sql.NullInt64{})
	if err != nil {
		return err
	}
	verb := "updated"
	if created {
		verb = "created"
	}
	fmt.Printf("routine %s %s (id %s, cron %q, %d steps)\n", sf.Name, verb, r.ID, r.CronExpr, len(sf.Steps))
	return nil
}

// cmdEconomics prints the five token-economy metrics the agent-system audit is
// measured on: cost per completed task, cache efficiency, delegation cost,
// wasted work, and model mix. Read-only — internal/economics issues no
// INSERT/UPDATE/DELETE, so this is safe to run against a live daemon's
// database. --json emits the same report machine-readably, which is how a
// baseline is captured for later comparison.
func cmdEconomics(args []string) error {
	fs := flag.NewFlagSet("economics", flag.ExitOnError)
	dbPath := dbFlag(fs)
	since := fs.String("since", "", "lower bound, YYYY-MM-DD inclusive")
	until := fs.String("until", "", "upper bound, YYYY-MM-DD inclusive")
	project := fs.Int64("project", 0, "project id filter (0 = all)")
	asJSON := fs.Bool("json", false, "emit the report as JSON instead of text")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery economics [--db <path>] [--since <d>] [--until <d>] [--project <id>] [--json]")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	rep, err := economics.Compute(db, economics.Options{
		Since: *since, Until: *until, ProjectID: *project,
	})
	if err != nil {
		return err
	}
	return economics.Render(os.Stdout, rep, *asJSON)
}

// cmdStale lists tasks that CLAIM to be running but show no sign of it, with the
// evidence each verdict rests on. Read-only.
//
// This is the surface that covers workspace tasks: the board API lists only
// source='queue' rows, and every stuck task in a real database sits on the
// workspace side — which is precisely why the class stayed invisible until an audit
// went looking for it with ad-hoc SQL.
//
// It deliberately does not offer a --fix flag. A workspace task's status is a
// projection of its workspace artifacts (internal/wsingest rewrites it on every
// scan), so "fixing" it here would be reverted minutes later while looking like it
// worked. Closing such a task out happens in the workspace, by a human.
func cmdStale(args []string) error {
	fs := flag.NewFlagSet("stale", flag.ExitOnError)
	dbPath := dbFlag(fs)
	project := fs.Int64("project", 0, "project id filter (0 = all)")
	all := fs.Bool("all", false, "include live and unknown verdicts, not just stale ones")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery stale [--db <path>] [--project <id>] [--all]")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := staleness.Load(db, *project)
	if err != nil {
		return err
	}

	shown := 0
	for _, r := range rows {
		if !*all && r.Verdict.Kind != staleness.KindStale && r.Verdict.Kind != staleness.KindDeadProc {
			continue
		}
		if shown == 0 {
			fmt.Printf("%-6s %-12s %-10s %-10s %s\n", "ID", "VERDICT", "SOURCE", "EVIDENCE", "TITLE")
		}
		shown++
		fmt.Printf("%-6d %-12s %-10s %-10s %s\n",
			r.TaskID, r.Verdict.Kind, r.Input.Source, r.Verdict.Confidence, r.Title)
		fmt.Printf("       └─ %s\n", r.Verdict.Reason)
	}
	if shown == 0 {
		fmt.Println("no stale tasks")
		return nil
	}
	fmt.Printf("\n%d task(s) shown of %d scanned.\n", shown, len(rows))
	// The count is the actionable part: a heuristic-evidence verdict is a prompt to
	// look, not a conclusion, and saying so here keeps the number from being quoted
	// as a fact it cannot support.
	fmt.Println("Verdicts marked `heuristic` rest on inferred task↔session links — read them as a prompt to check, not as proof.")
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

	// Backfill runs HealProjectAttribution too — feed it the onboard roots
	// before the pipeline touches the DB, or a manual backfill could re-merge
	// projects into an onboard-root row (the exact bug SetOnboardRoots fixes).
	ingest.SetOnboardRoots(onboardRoots())

	// Same tolerate-some / refuse-all rule the daemon applies (ingest.Run): a
	// roots list is shared across machines and may name a config dir this one
	// does not have.
	present, missing := ingest.ExistingRoots(cfg.ProjectsRoots)
	for _, r := range missing {
		log.Printf("warn: projects root %s does not exist — skipped", r)
	}
	if len(present) == 0 {
		return fmt.Errorf("projects root: %w (configured: %s)",
			ingest.ErrNoProjectsRoots, strings.Join(cfg.ProjectsRoots, ", "))
	}
	if *rebuildText {
		stats := ingest.RebuildText(context.Background(), db, present)
		fmt.Printf("rebuild-text %s\n  transcripts re-read: %d\n  errors: %d\n",
			strings.Join(present, ", "), stats.Files, stats.Errors)
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
	claudeBin := fs.String("claude-bin", envOr("SWARMERY_CLAUDE_BIN", ""),
		"path to the claude CLI used for plugin drift detection (default: PATH, then ~/.local/bin, /opt/homebrew/bin, /usr/local/bin)")
	driftInterval := fs.Duration("plugin-drift-interval", plugindrift.DefaultInterval,
		"plugin drift scan interval (0 disables the scanner)")
	cfg := pipelineFlags(fs)
	wsCfg := wsingestFlags(fs)
	sysCfg := sysscanFlags(fs)
	fs.Parse(args)
	if *answerDelivery != approvals.DeliveryUpdatedInput && *answerDelivery != approvals.DeliveryDenyMessage {
		return fmt.Errorf("--answer-delivery must be %q or %q, got %q",
			approvals.DeliveryUpdatedInput, approvals.DeliveryDenyMessage, *answerDelivery)
	}

	// trajjudge config (advisory LLM-judge, best-effort; cap<=0 disables).
	trajjudgeModel := os.Getenv("SWARMERY_TRAJJUDGE_MODEL")
	if trajjudgeModel == "" {
		// Full ID, not the "sonnet" alias — aliases re-resolve over time and the
		// judged model is stored per verdict, so the pin keeps scores comparable.
		trajjudgeModel = "claude-sonnet-5"
	}
	// Minimum age of the newest verdict before another automatic batch may
	// run (startup + 24h tick); the manual advise endpoint is not gated.
	const trajjudgeCooldown = 6 * time.Hour
	trajjudgeCap := 10
	if v := os.Getenv("SWARMERY_TRAJJUDGE_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			trajjudgeCap = n
		} else {
			log.Printf("warn: ignoring invalid SWARMERY_TRAJJUDGE_CAP=%q", v)
		}
	}

	// fusion phase 9 (Console/DX): stand up the in-memory structured log ring and
	// tee both slog and the stdlib `log` package into it. slog gets a tagging
	// handler (boot code uses logbuf.Tagged / WithGroup for subsystem tags); the
	// stdlib logger's output is redirected through a ring writer that mirrors to
	// stderr so launchd logs are unchanged. `bootStart` anchors the phase timings.
	bootStart := time.Now()
	ring := logbuf.New(logbuf.DefaultCapacity)
	slog.SetDefault(slog.New(logbuf.NewHandler(ring,
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))
	log.SetOutput(logbuf.NewWriter(ring, "daemon", os.Stderr))
	api.AttachLogRing(ring)
	api.AttachUptime(bootStart)
	bootLog := logbuf.Tagged(slog.Default(), "boot")

	migrateStart := time.Now()
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	bootLog.Info(logbuf.Phasef("store.migrate", time.Since(migrateStart)))

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

	// Attribution needs the onboarding roots before the pipeline starts: an
	// onboarding root is a parent dir of many repos, and the pipeline's first
	// Backfill runs HealProjectAttribution, which would otherwise fold every
	// repo under such a root into it. api.AttachOnboard below fences the
	// onboarding endpoint with the same list — same source, wired twice
	// because the ingest package must not import api.
	ingest.SetOnboardRoots(onboardRoots())

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
		log.Printf("swarmery ingest pipeline watching %s (rescan %s)",
			strings.Join(cfg.ProjectsRoots, ", "), cfg.RescanInterval)

		// phase 3.5: workspaces — read-only periodic scan of the agent-work.sh
		// workspace repo (tasks + task↔session links). Missing root is not
		// fatal: the scanner logs and keeps ticking.
		// plans-page-lifecycle phase 1: a plan hash change publishes
		// plan_updated so the Plans page refetches live (the one-shot scan
		// subcommand leaves NotifyPlan nil = no publishing).
		wsCfg.NotifyPlan = func(taskID int64) {
			bus.Publish(ingest.Notification{Type: ingest.NotePlanUpdated, TaskID: taskID})
		}
		scanner := wsingest.New(db, *wsCfg)
		go func() {
			if err := scanner.Run(context.Background()); err != nil && err != context.Canceled {
				log.Printf("error: wsingest scanner stopped: %v", err)
			}
		}()
		// plan-revision phase 3: a revision Apply pokes the SAME scanner pass
		// that converges plan-doc writes — once, after all writes and renames —
		// so the Plans page never renders a half-applied plan.
		api.AttachRevisionRescan(func(string) {
			if _, err := scanner.Scan(); err != nil {
				log.Printf("error: wsingest rescan after revision apply: %v", err)
			}
		})
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
	// procDeadHook lets a liveness transition reach the dispatcher without procwatch
	// importing dispatch. It is assigned after dispatchSvc is constructed (further
	// down this function), and pw.Run is started only after that — reading this var
	// from the ticker goroutine while main still assigned it would be a data race.
	var procDeadHook func()
	pw := &procwatch.Ticker{
		DB:       db,
		Provider: procwatch.OsProvider{},
		Interval: 30 * time.Second,
		OnStateChange: func(id int64) {
			if bus != nil {
				bus.Publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: id})
			}
			if procDeadHook != nil {
				procDeadHook()
			}
		},
	}

	// plugin drift — enabled-but-not-installed / version-behind / orphaned
	// plugins, persisted into config_lint_findings under the plugin_* rules.
	if *driftInterval > 0 {
		bin, berr := plugindrift.ResolveBin(*claudeBin)
		dt := &plugindrift.Ticker{
			DB:       db,
			Detector: &plugindrift.Detector{ClaudeDir: sysCfg.ClaudeDir, Runner: plugindrift.ExecRunner{Bin: bin}},
			Interval: *driftInterval,
		}
		if berr != nil {
			// Record the blindness instead of scanning with a broken binary:
			// a silent no-op here would render as "no drift" in every surface.
			plugindrift.RecordUnavailable(db, berr)
			log.Printf("swarmery plugin-drift scanner DISABLED: %v", berr)
		} else {
			// Repair shares the resolved binary — attached only here, so the
			// endpoint answers 503 rather than shelling out to something absent.
			api.AttachPluginRepairer(plugindrift.ExecRunner{Bin: bin})
			// SessionStart answers from the last persisted pass (2s hook budget
			// rules out scanning inline) and kicks a refresh through this.
			api.AttachDriftRefresher(func() { dt.Once(context.Background()) })
			// One webhook per NEWLY inserted error finding. The lifecycle does the
			// deduplication: a standing problem refreshes its row in place, so it
			// is announced once rather than every five minutes.
			dt.OnNewError = func(target, rule, message string) {
				if notifier == nil {
					return
				}
				id, projectPath, ok := plugindrift.ParseTarget(target)
				if !ok {
					id, projectPath = target, ""
				}
				notifier.Emit(notify.Event{
					Type:    notify.EventPluginDrift,
					TS:      time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
					Project: projectPath,
					Title:   "plugin unavailable: " + id,
					Body:    message,
				})
			}
			go dt.Run(context.Background())
			log.Printf("swarmery plugin-drift scanner started (interval %s, claude %s)", *driftInterval, bin)
		}
	}

	// retro phase 3: the advisor rule engine — deterministic recommendations
	// (R1..R6) refreshed once at startup and every 24h, plus on demand via
	// POST /api/retro/advise. Works purely off the DB, so it runs with or
	// without ingest; failures are logged, never fatal.
	go func() {
		runAdvisor := func() {
			if err := trajeval.Compute(db, time.Now()); err != nil {
				log.Printf("trajeval.Compute: %v", err)
			}
			// Cooldown: a dev day restarts the daemon on every `make install`,
			// and each restart lands here — without the gate that's a paid capN
			// batch per restart. The 24h ticker is unaffected (24h > cooldown);
			// POST /api/retro/advise stays unconditional.
			if trajjudge.JudgedWithin(db, trajjudgeModel, time.Now(), trajjudgeCooldown) {
				log.Printf("trajjudge: batch skipped, last %s verdict is younger than %s", trajjudgeModel, trajjudgeCooldown)
			} else if err := trajjudge.Score(db, trajjudge.ClaudeRunner{Model: trajjudgeModel}, trajjudgeModel, time.Now(), trajjudgeCap); err != nil {
				log.Printf("trajjudge.Score: %v", err)
			}
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

	// fat-session handoff (migration 0039): when a live session's context crosses
	// handoff.Threshold, generate ~/.swarmery/handoffs/<uuid>.md from the DB via a
	// pinned cheap-model headless run so the user can /clear and resume cheaply.
	// Off switch: SWARMERY_HANDOFF=off. Model pin: SWARMERY_HANDOFF_MODEL (default
	// claude-sonnet-5, the same house-rule full-ID pin as trajjudge). Best-effort
	// off the DB — failures are logged, never fatal; each generation publishes
	// NoteSessionUpdated so open dashboards pick up the new brief.
	if strings.EqualFold(os.Getenv("SWARMERY_HANDOFF"), "off") {
		log.Printf("swarmery handoff disabled (SWARMERY_HANDOFF=off)")
	} else {
		handoffModel := os.Getenv("SWARMERY_HANDOFF_MODEL")
		if handoffModel == "" {
			handoffModel = "claude-sonnet-5"
		}
		go func() {
			runHandoff := func() {
				cands, dropped, err := handoff.Candidates(db, time.Now())
				if err != nil {
					log.Printf("error: handoff.Candidates: %v", err)
					return
				}
				if dropped > 0 {
					log.Printf("handoff: %d fat session(s) over the per-tick cap of %d, deferred to next tick", dropped, handoff.MaxPerTick)
				}
				runner := handoff.ClaudeRunner{Model: handoffModel}
				for _, c := range cands {
					path, err := handoff.Generate(db, runner, c.SessionID, time.Now())
					if err != nil {
						log.Printf("error: handoff: session %d: %v", c.SessionID, err)
						continue
					}
					log.Printf("handoff: session %d ctx=%dk → %s", c.SessionID, c.ContextTokens/1000, path)
					bus.Publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: c.SessionID})
				}
			}
			// Startup delay so the daemon is fully up and ingest has caught up
			// before the first paid batch; then every 30 min.
			time.Sleep(2 * time.Minute)
			runHandoff()
			ticker := time.NewTicker(30 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				runHandoff()
			}
		}()
		log.Printf("swarmery handoff started (interval 30m, model %s)", handoffModel)
	}

	// board inbox lifecycle: a captured card is a suggestion, not a commitment,
	// so one that sat untouched in Triage past SWARMERY_INBOX_TTL is archived
	// rather than left to accumulate as permanent debt. Pure DB work off
	// internal/taskcap (the same package that mints the cards), so it runs with
	// or without ingest; a failure is logged, never fatal.
	//
	// Hourly rather than the advisor's 24h: the TTL is measured in days, so the
	// cadence only decides how promptly a card crosses it, and an hourly UPDATE
	// that usually matches zero rows costs nothing. Silent on a no-op sweep —
	// a log line per hour saying "archived 0" is noise in `swarmery console`.
	if inboxTTL := envInboxTTL(); inboxTTL <= 0 {
		log.Printf("swarmery inbox sweeper disabled (SWARMERY_INBOX_TTL=%q)", os.Getenv("SWARMERY_INBOX_TTL"))
	} else {
		go func() {
			sweepInbox := func() {
				n, err := taskcap.SweepStaleInbox(db, inboxTTL, time.Now())
				if err != nil {
					log.Printf("error: inbox sweep: %v", err)
					return
				}
				if n > 0 {
					log.Printf("swarmery inbox sweep: archived %d stale captured card(s) (ttl %s)", n, inboxTTL)
				}
			}
			sweepInbox()
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				sweepInbox()
			}
		}()
		log.Printf("swarmery inbox sweeper started (interval 1h, ttl %s)", inboxTTL)
	}

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

	// settings overlays: an operator can DECLARE settings files that also apply
	// to given project roots, for projects whose plugin set is injected at CLI
	// precedence instead of being committed to the repo. Always attached — a
	// missing descriptor is the normal case and degrades to repo-only detection.
	api.AttachSettingsOverlays(settingsoverlay.New(os.Getenv("SWARMERY_SETTINGS_OVERLAYS")))

	// fusion phase 18: System Hub — GET /api/system/templates scans the plugin
	// cache (<claude-dir>/plugins/cache) + each project's .claude/templates on
	// demand. Wire the same resolved dir so --claude-dir applies here too.
	api.AttachSystemHubDir(sysCfg.ClaudeDir)

	// self-improvement phase 4: the apply/PR pipeline fetches + worktrees from
	// the marketplace clone under <claude-dir>/plugins/marketplaces/swarmery.
	// Wire the same resolved dir so --claude-dir applies here too.
	api.AttachImproveRepo(sysCfg.ClaudeDir)

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

	// connectors (mcp servers): the reader that shells to `claude mcp …` behind
	// GET/POST/DELETE /api/connectors. Read-through, no daemon-owned process, so
	// nothing to stop on shutdown.
	api.AttachConnectorReader(mcpcfg.New())

	// fusion phase 15: the embedded-terminal PTY manager. Owns every live PTY
	// behind GET /api/term/ws; the shutdown path below runs CloseAll so no shell
	// (or child of one) outlives the daemon. Its idle reaper starts under ctx.
	termMgr := term.NewManager(term.Config{})
	api.AttachTermManager(termMgr)

	// fusion phase 3: the task dispatcher. Picks Todo board tasks and runs each
	// as a headless `claude -p --session-id <uuid>` inside a swarm/<id> worktree
	// (default root <home>/.swarmery/worktrees). Conservative caps + kill-switch
	// from env (SWARMERY_DISPATCH=0 disables). Heal in_progress rows a crashed
	// daemon left behind back to todo, then attach — the scheduler ticker starts
	// below under the shutdown context. The worktree Manager is Git-mockable but
	// runs the real git here.
	// One worktree.Manager runs the real git for both the dispatcher (acquire/
	// remove/tree) and auto-verification (tree-hash). Shared so both agree on the
	// worktree root and git boundary.
	wtMgr := &worktree.Manager{Git: worktree.ExecGit{}}

	// fusion phase 13: the playbook registry (embedded built-ins + per-project
	// .claude/playbooks overrides). Shared read-only by the dispatcher (multi-stage
	// execution) and the verifier (verify strictness knob) and the api layer (list
	// + duplicate). A malformed built-in fails startup loudly (they ship in the
	// binary); project files are rescanned lazily on mtime change.
	playbookReg, err := playbooks.New()
	if err != nil {
		return fmt.Errorf("playbooks registry: %w", err)
	}
	api.AttachPlaybooks(playbookReg)

	// Run-truth (account-connect phase 4): every dispatched/verification run
	// already executes `claude` under its account's config dir, so an exit that
	// demands a login is a free authoritative probe. The recorder persists such
	// verdicts with source='run' under runtruth's write rules (negative-only,
	// no-login→ready recovery, debounced).
	runTruth := runtruth.NewRecorder(db)

	// ONE run budget for the whole daemon (SWARMERY_MAX_RUNS, default 4). Every
	// engine below is handed this same registry: board, phase and plan runs draw
	// from one pool, which is what stops three engines each spawning up to their own
	// cap against a total none of them could see. NewService gives each Service its
	// own pool by default (so unit tests stay hermetic) — these three assignments
	// are what make the budget global, so they must travel with the services they
	// belong to.
	runSlots := runcore.NewSlots(0)

	dispatchSvc := dispatch.NewService(
		db, dispatch.ConfigFromEnv(),
		dispatch.ClaudeRunner{AccountVerdict: runTruth.Record}, wtMgr,
	)
	dispatchSvc.Slots = runSlots
	// The workspace tree dispatched cards materialize their micro-plans into — the
	// SAME value the scanner below is configured with, wired from one place so the
	// writer and the reader can never disagree about paths (execution-engine
	// unification phase 4). SWARMERY_MICRO_PLANS=0 disables minting.
	dispatchSvc.WorkspaceRoot = wsCfg.WorkspaceRoot
	dispatchSvc.Playbooks = playbookReg
	if err := dispatchSvc.HealStale(); err != nil {
		log.Printf("warning: dispatch heal on startup: %v", err)
	}
	// Evidence-driven heal, unlike HealStale's unconditional boot sweep: it needs the
	// dispatcher, so it is wired here and the procwatch ticker is started right after.
	if err := dispatchSvc.HealDeadProcess(); err != nil {
		log.Printf("warning: dispatch heal dead process on startup: %v", err)
	}
	procDeadHook = func() {
		if err := dispatchSvc.HealDeadProcess(); err != nil {
			log.Printf("warning: dispatch heal dead process: %v", err)
			return
		}
		// Heal only changes state; scheduling the reclaimed task is the caller's
		// job. Without this the task would wait for the poll fallback.
		dispatchSvc.Poke()
	}
	go pw.Run(context.Background())
	log.Printf("swarmery procwatch ticker started (interval 30s)")
	api.AttachDispatch(dispatchSvc)

	// fusion phase 6: auto-verification. After a dispatched run lands in_review
	// WITHOUT a sentinel, a bounded READ-ONLY headless `claude -p` grades the
	// task's acceptance criteria and stamps verify_verdict (pass|fail|
	// inconclusive). FAIL spawns a fix task within the root's retry budget (3);
	// INCONCLUSIVE spawns nothing. Kill-switch SWARMERY_AUTOVERIFY=0 disables the
	// auto trigger (the manual POST /api/tasks/{id}/verify still works). Heal
	// interrupted runs (crash left them 'running') to error+inconclusive, then
	// attach — to the api layer AND, as the dispatcher's Verifier seam, to the
	// dispatcher so a no-sentinel exit pokes verification while the worktree lives.
	verifySvc := verify.NewService(db, verify.ConfigFromEnv(),
		verify.ClaudeRunner{AccountVerdict: runTruth.Record}, wtMgr)
	// fusion phase 13: resolve each task's verify strictness knob (strict|normal|
	// off) from its playbook via the shared registry — off skips the run, strict
	// tightens the prompt bar. The seam keeps verify decoupled from the playbook
	// package (wired here at the composition root).
	verifySvc.PlaybookVerify = func(projectPath, name string) string {
		if pb, ok := playbookReg.Get(projectPath, name); ok {
			return pb.Verify
		}
		return "normal"
	}
	if err := verifySvc.HealStale(); err != nil {
		log.Printf("warning: verify heal on startup: %v", err)
	}
	api.AttachVerify(verifySvc)
	dispatchSvc.Verifier = verifySvc

	// On-demand LLM task extraction (POST /api/sessions/{id}/extract-tasks).
	// Purely operator-triggered — no ticker, no startup heal, nothing to reap:
	// an extraction lives entirely inside one request, so unlike verify there is
	// no interrupted-run state a crash could leave behind.
	api.AttachExtract(extract.NewService(db))

	// fusion phase 7: routines (scheduled automation). A 60s scheduler ticks due
	// cron routines and runs their typed steps (command / ai-prompt / create-task)
	// as headless `claude -p` runs (ai-prompt) or board-task inserts (create-task,
	// via the api-layer TaskCreator adapter so board semantics + WS publish + poke
	// stay in one place). Kill-switch SWARMERY_ROUTINES=0 disables the ticker; heal
	// any 'running' run rows a crashed daemon left behind to 'failed' before the
	// ticker starts below under the shutdown context.
	routinesSvc := routines.NewService(
		db, routines.ClaudeRunner{}, api.NewRoutinesTaskCreator(db), routines.Enabled(),
	)
	if err := routinesSvc.HealStale(); err != nil {
		log.Printf("warning: routines heal on startup: %v", err)
	}
	api.AttachRoutines(routinesSvc)

	// fusion phase 8: planning mode. A headless `claude -p --session-id <uuid>`
	// planner run per project (single-flight, in-memory — no new tables) turns an
	// idea into a plan in the private workspace; the run asks clarifying questions
	// as reply text (the spike proved AskUserQuestion does not fire the permission
	// hook under `-p`), answered via the existing session-resume chat. No heal on
	// startup: in-flight planning is in-memory only, so a restart simply forgets
	// any orphaned run (the plan it wrote is still picked up by wsingest).
	planningSvc := planning.NewService(db, planning.ClaudeRunner{})
	// Revise sessions stage proposed plan files here (one subdir per session)
	// until the daemon validates them into plan_revisions rows.
	planningSvc.ScratchRoot = filepath.Join(filepath.Dir(*dbPath), "revisions")
	// The planner must write where the scanner reads: same root, one value, so a
	// moved workspace cannot leave plans in a tree nothing indexes.
	planningSvc.WorkspaceRoot = wsCfg.WorkspaceRoot
	// plan-revision phase 5: a revise session that died before its sentinel
	// leaves its scratch dir behind — sweep dirs whose session can no longer
	// stage (no wizard row, or the wizard is terminal) once, on start.
	if removed, err := planning.SweepScratchOrphans(db, planningSvc.ScratchRoot); err != nil {
		log.Printf("warning: revision scratch sweep: %v", err)
	} else if len(removed) > 0 {
		log.Printf("revision scratch sweep: removed %d orphaned dir(s)", len(removed))
	}
	api.AttachPlanning(planningSvc)

	// plan-revision phase 5: revision retention, daily alongside the other
	// maintenance tickers — staged proposals nobody decided go superseded
	// after 14 days; decided revisions' document bodies are nulled after 90.
	go func() {
		tick := func() {
			if st, err := prune.PruneRevisions(db, time.Now()); err != nil {
				log.Printf("error: revision prune: %v", err)
			} else if st.Superseded > 0 || st.ContentNulled > 0 {
				log.Printf("revision prune: superseded=%d content_nulled=%d", st.Superseded, st.ContentNulled)
			}
		}
		tick()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			tick()
		}
	}()

	// interactive planning v2 phase 5: phase runs — execute ONE plan phase
	// headlessly in an isolated worktree straight from its phase doc (state on
	// epic_phases, no board task). Shares the worktree.Manager with dispatch/
	// verify so all three agree on the worktree root and git boundary. Heal any
	// 'running' rows a crashed daemon left behind to failed before serving.
	phaserunSvc := phaserun.NewService(db, phaserun.ClaudeRunner{}, wtMgr)
	phaserunSvc.Slots = runSlots // the one daemon-wide budget (see runSlots above)
	// Read-only git seam, through the same boundary the worktree manager uses: it
	// NAMES the base a dirty-branch refusal counted commits against. NewService
	// does not set it, so without this line BranchDirtyError.Base is always "" in
	// the running daemon and the 409 cannot qualify its own count — the field would
	// be dead weight. Same wiring planrunSvc gets below.
	phaserunSvc.Git = wtMgr.Git
	if err := phaserunSvc.HealStale(); err != nil {
		log.Printf("warning: phaserun heal on startup: %v", err)
	}
	api.AttachPhaseRun(phaserunSvc)
	// Opt-in post-run verification for phases (execution-engine unification phase 5):
	// a phase doc carrying `**Verify:** strict` gets graded by the SAME read-only
	// engine board cards go through, before its worktree is reclaimed. Wired here
	// because the verifier is constructed earlier and phaserun must not import it;
	// the verdict lands on epic_phases as an INPUT to the phase's diagnosis (D5), never
	// as a second status. Every plan that does not ask keeps today's behaviour.
	phaserunSvc.Verify = verifySvc
	// The diagnosis endpoint reads git directly (branch ancestry) through the same
	// boundary the worktree manager uses.
	api.AttachPhaseDiag(wtMgr.Git, wtMgr)

	// Plan runs: hand a WHOLE plan to one agent — one headless session in one
	// worktree, driving core's run-plan skill (state on plan_runs). Same
	// worktree.Manager as dispatch/verify/phaserun; same startup heal posture.
	planrunSvc := planrun.NewService(db, planrun.ClaudeRunner{}, wtMgr)
	planrunSvc.Slots = runSlots // the one daemon-wide budget (see runSlots above)
	// Read-only git seam, through the same boundary the worktree manager uses: it
	// NAMES the base a dirty-branch refusal counted commits against, and answers
	// whether a run branch existed before DeleteRunBranch removed it. Without it
	// both answers degrade to "unknown" rather than being guessed.
	planrunSvc.Git = wtMgr.Git
	if err := planrunSvc.HealStale(); err != nil {
		log.Printf("warning: planrun heal on startup: %v", err)
	}
	api.AttachPlanRun(planrunSvc)

	buildStart := time.Now()
	// The board derives each captured card's expiry from the same TTL the
	// sweeper runs with, so the two can never tell the operator different dates.
	api.AttachInboxTTL(envInboxTTL())
	handler, err := api.NewServer(db, !*noIngest)
	if err != nil {
		return err
	}
	// Wire the projects roots into the on-demand transcript-parsing endpoints
	// (GET /api/sessions/{id}/context-hogs resolves uuid→transcript under them,
	// searching every configured root the way the ingest heal does).
	api.AttachProjectsRoots(cfg.ProjectsRoots)
	bootLog.Info(logbuf.Phasef("api.build", time.Since(buildStart)))
	addr := net.JoinHostPort(*bind, strconv.Itoa(*port))
	log.Printf("swarmery serving on http://%s (db: %s)", addr, *dbPath)
	// Final startup-phase summary: api.listen (the mux + SPA are ready to bind)
	// and the total boot time in the exact spec wording.
	bootLog.Info(logbuf.Phasef("api.listen", time.Since(buildStart)))
	bootLog.Info(logbuf.Readyf(time.Since(bootStart)))

	// Graceful shutdown: SIGINT/SIGTERM → stop tool children, drain HTTP, exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// fusion phase 3: run the dispatcher poll-fallback ticker under the shutdown
	// context. It runs an initial Schedule immediately (drains any Todo backlog
	// on restart) then sweeps every PollInterval; the event fast path is the
	// board handlers' pokeDispatch(). Exits when ctx is cancelled.
	go dispatchSvc.StartScheduler(ctx)

	// fusion phase 6: the verification stale-run reaper. A verifier that was
	// killed or wedged leaves a 'running' verification_runs row; the reaper marks
	// runs older than the stale window (2h) as error and stamps their task
	// inconclusive so it never parks forever. Runs on a 10-minute ticker under the
	// shutdown context; a first pass fires immediately.
	go func() {
		reap := func() {
			if _, err := verifySvc.Reap(); err != nil {
				log.Printf("error: verify reaper: %v", err)
			}
		}
		reap()
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				reap()
			}
		}
	}()
	log.Printf("swarmery verify reaper started (interval 10m)")

	// fusion phase 7: run the routines scheduler (60s cron ticker) under the same
	// shutdown context, in its own goroutine independent of the dispatcher's. It
	// touches disjoint tables (routines/routine_runs); a create-task step hands
	// off to the dispatcher through the shared board (pokeDispatch inside the
	// TaskCreator), so the two schedulers never contend on a lock.
	go routinesSvc.StartScheduler(ctx)

	// fusion phase 15: reap PTYs idle past the timeout (4h) on a 1-minute ticker
	// under the shutdown context.
	go termMgr.Reap(ctx.Done())

	// worktree janitor: agent worktrees — the harness's <repo>/.claude/worktrees/*
	// and our own under the manager's root — are never cleaned by whoever created
	// them, so they accumulate until a human notices. This sweeps them on a
	// 15-minute tick: a worktree is removed only when every dirty path's blob is
	// already in git, or after its content has been committed to a salvage/*
	// branch, and never while anything is live inside it. Kill-switch
	// SWARMERY_WTJANITOR=0. One pass runs immediately so a restart is also a
	// cleanup; it logs only when it actually did something, because a quiet
	// janitor printing a line every 15 minutes forever is just noise.
	wtjCfg := wtjanitor.ConfigFromEnv()
	if wtjCfg.Enabled {
		wtj := &wtjanitor.Service{
			DB:      db,
			Git:     wtjanitor.RepoGit{},
			Live:    wtjanitor.ProcLiveness{Proc: procwatch.OsProvider{}, DB: db},
			MinIdle: wtjCfg.MinIdle,
			Remover: wtjanitor.NewRealRemover(wtMgr),
		}
		go func() {
			sweep := func() {
				res, err := wtj.Sweep(false)
				switch {
				case err != nil:
					log.Printf("warning: worktree janitor sweep: %v", err)
				case res.Removed+res.Salvaged > 0:
					log.Printf("worktree janitor: removed %d, salvaged %d, kept %d, skipped %d",
						res.Removed, res.Salvaged, res.Kept, res.Skipped)
				}
			}
			sweep()
			t := time.NewTicker(wtjCfg.TickInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					sweep()
				}
			}
		}()
		log.Printf("swarmery worktree janitor started (interval %s, min-idle %s)",
			wtjCfg.TickInterval, wtjCfg.MinIdle)
	}

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
		// Drain done ⇒ no new /api/term/ws upgrade can register a PTY; tear down
		// every live terminal (SIGHUP→SIGKILL its process group) so none survive.
		termMgr.CloseAll()
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

// envInboxTTL resolves how long a captured card may sit untouched in Triage
// before the sweeper archives it, from SWARMERY_INBOX_TTL (a Go duration, e.g.
// "168h"), falling back to taskcap.DefaultInboxTTL (14 days).
//
// A non-positive value ("0") is the documented off switch and is returned as-is
// — the caller reads <= 0 as "do not start the sweeper". An UNPARSEABLE value
// falls back to the default instead: a typo must not silently disable a
// retention job, and the warning says which value was ignored.
func envInboxTTL() time.Duration {
	v := os.Getenv("SWARMERY_INBOX_TTL")
	if v == "" {
		return taskcap.DefaultInboxTTL
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("warn: ignoring invalid SWARMERY_INBOX_TTL=%q", v)
		return taskcap.DefaultInboxTTL
	}
	return d
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

// cmdWorktrees implements `swarmery worktrees clean [--dry-run] [--repo <path>]`
// — the SAME Service.Sweep the daemon ticks, exposed so a human can read the
// verdicts before trusting them. --dry-run decides and journals but removes
// nothing, which is also how you audit the rules after changing them.
func cmdWorktrees(args []string) error {
	if len(args) == 0 || args[0] != "clean" {
		return fmt.Errorf("usage: swarmery worktrees clean [--db <path>] [--dry-run] [--repo <path>]")
	}
	fs := flag.NewFlagSet("worktrees clean", flag.ExitOnError)
	dbPath := dbFlag(fs)
	dryRun := fs.Bool("dry-run", false, "decide and journal, but remove nothing")
	repo := fs.String("repo", "", "limit the sweep to one repository path")
	fs.Parse(args[1:])
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: swarmery worktrees clean [--db <path>] [--dry-run] [--repo <path>]")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	cfg := wtjanitor.ConfigFromEnv()
	svc := &wtjanitor.Service{
		DB:       db,
		Git:      wtjanitor.RepoGit{},
		Live:     wtjanitor.ProcLiveness{Proc: procwatch.OsProvider{}, DB: db},
		MinIdle:  cfg.MinIdle,
		OnlyRepo: *repo,
		Remover:  wtjanitor.NewRealRemover(&worktree.Manager{Git: worktree.ExecGit{}}),
	}
	// Stamp the journal cursor BEFORE sweeping so the report prints this pass's
	// rows only, not the whole history.
	var before int64
	_ = db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM worktree_sweeps`).Scan(&before)

	res, err := svc.Sweep(*dryRun)
	if err != nil {
		return err
	}

	rows, err := db.Query(
		`SELECT path, COALESCE(branch, '-'), verdict, reason, COALESCE(salvage_branch, ''), removed, COALESCE(error, '')
		   FROM worktree_sweeps WHERE id > ? ORDER BY id`, before)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path, branch, verdict, reason, salvage, errStr string
		var removed int
		if err := rows.Scan(&path, &branch, &verdict, &reason, &salvage, &removed, &errStr); err != nil {
			return err
		}
		line := fmt.Sprintf("  %-13s %s [%s] — %s", verdict, path, branch, reason)
		if salvage != "" {
			line += " → " + salvage
		}
		if removed == 1 {
			line += " (removed)"
		}
		if errStr != "" {
			line += " !! " + errStr
		}
		fmt.Println(line)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	mode := "swept"
	if *dryRun {
		mode = "would sweep (dry-run)"
	}
	fmt.Printf("worktrees %s: inspected %d, removed %d, salvaged %d, kept %d, skipped %d, errors %d\n",
		mode, res.Inspected, res.Removed, res.Salvaged, res.Kept, res.Skipped, res.Errors)
	return nil
}
