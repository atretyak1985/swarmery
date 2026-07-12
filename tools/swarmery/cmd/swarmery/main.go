// Command swarmery is the control-plane daemon CLI:
//
//	swarmery ingest <file.jsonl>   parse one transcript into the local DB
//	swarmery backfill              one-shot full scan of the projects root
//	swarmery serve                 serve the API/SPA + live ingest pipeline
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
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
  swarmery backfill [--db <path>] [--projects-root <dir>]
  swarmery serve    [--db <path>] [--port <n>] [--projects-root <dir>]
                    [--rescan <dur>] [--status-tick <dur>]
                    [--active-window <dur>] [--idle-window <dur>] [--no-ingest]
  env: SWARMERY_PORT, SWARMERY_PROJECTS_ROOT`)
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
	cfg := &ingest.Config{}
	fs.StringVar(&cfg.ProjectsRoot, "projects-root", defaultProjectsRoot(),
		"Claude Code projects root to ingest (env: SWARMERY_PROJECTS_ROOT)")
	fs.DurationVar(&cfg.RescanInterval, "rescan", 2*time.Second, "fallback rescan interval")
	fs.DurationVar(&cfg.StatusInterval, "status-tick", 10*time.Second, "session-status recompute interval")
	fs.DurationVar(&cfg.Thresholds.Active, "active-window", 2*time.Minute, "session considered active within this window")
	fs.DurationVar(&cfg.Thresholds.Idle, "idle-window", 30*time.Minute, "session considered idle within this window")
	return cfg
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

func cmdBackfill(args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	dbPath := dbFlag(fs)
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
	ingest.NewPipeline(db, *cfg, nil).Backfill(context.Background())
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := dbFlag(fs)
	port := fs.Int("port", envPort(), "HTTP port (env: SWARMERY_PORT)")
	noIngest := fs.Bool("no-ingest", false, "serve the API only, without the live ingest pipeline")
	cfg := pipelineFlags(fs)
	fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if !*noIngest {
		bus := ingest.NewBus()
		api.AttachBus(bus)
		pipeline := ingest.NewPipeline(db, *cfg, bus)
		go func() {
			if err := pipeline.Run(context.Background()); err != nil && err != context.Canceled {
				log.Printf("error: ingest pipeline stopped: %v", err)
			}
		}()
		log.Printf("swarmery ingest pipeline watching %s (rescan %s)", cfg.ProjectsRoot, cfg.RescanInterval)
	}

	handler, err := api.NewServer(db)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("swarmery serving on http://localhost%s (db: %s)", addr, *dbPath)
	return http.ListenAndServe(addr, handler)
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
