package main

// `swarmery decide` — the terminal surface of the local classifier
// (internal/decide):
//
//	swarmery decide eval   replay the classifier over recorded ground truth
//
// `eval` is READ-ONLY and safe against the live database while the daemon
// serves: it opens the store with store.OpenNoMigrate (a terminal command never
// migrates behind the daemon — store.Open would), sets the connection to
// query_only, and decide.Eval issues SELECTs alone. It never contacts the
// daemon, and nothing leaves the machine: the only backend it may call is the
// local model, and only under --llm.
//
// Exit code is the contract, so a script can gate on it: 0 on success, 1 when a
// --min-* agreement floor is missed, 2 on a usage or database error.

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const decideUsage = `usage:
  swarmery decide eval [--db <path>] [--llm] [--truth-since <RFC3339>] [--questions <a,b>]
                       [--limit <n>] [--json] [--out <file>]
                       [--min-outcome <f>] [--min-failure <f>] [--min-task <f>]

  Replays the classifier over the ground truth already recorded in the
  decisions table and reports, per question, how often the replayed answer
  agrees with the label: labelled / replayed / skipped, agreement, a split by
  backend with precision, the top confusions and ten confidence buckets.
  READ-ONLY: it writes no decision, label or mode, and never migrates the
  database — safe while the daemon is serving.

  --llm          also ask the local model (needs SWARMERY_DECIDE_URL; the other
                 SWARMERY_DECIDE_* knobs apply). Without it only the rules
                 answer and everything else is counted as unanswered.
  --truth-since  keep labels recorded at or after this instant
  --questions    comma-separated subset of d2.task_type,d2.outcome,d2.failure_cause
                 (the d2. prefix may be dropped)
  --limit        replay at most <n> sessions, newest label first
  --json         emit the report as JSON instead of a table
  --out          also write the report to <file>
  --min-outcome / --min-failure / --min-task
                 agreement floors in [0, 1] for d2.outcome, d2.failure_cause and
                 d2.task_type; a missed floor exits 1

  SWARMERY_DECIDE_R5=on replays with rule R5 (a phase or plan run is task type
  feature) switched on, with or without --llm; each rule's coverage and
  precision is listed per question.

  exit: 0 ok · 1 a --min-* floor was missed · 2 usage or database error`

// decideProgressEvery is how many sessions pass between --llm progress lines.
const decideProgressEvery = 50

// cmdDecide dispatches the `decide` subcommands and returns the exit code.
func cmdDecide(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, decideUsage)
		return 2
	}
	switch args[0] {
	case "eval":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return decideEval(ctx, args[1:], os.Stdout, os.Stderr, os.Getenv)
	case "-h", "--help", "help":
		fmt.Fprintln(os.Stderr, decideUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown decide subcommand %q\n%s\n", args[0], decideUsage)
		return 2
	}
}

// decideEval parses `decide eval`, runs the replay and renders it. stdout gets
// the report and nothing else; warnings, progress and errors go to stderr.
func decideEval(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("decide eval", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := dbFlag(fs)
	llm := fs.Bool("llm", false, "also ask the local model (SWARMERY_DECIDE_URL)")
	truthSince := fs.String("truth-since", "", "keep labels recorded at or after this RFC 3339 instant")
	questions := fs.String("questions", "", "comma-separated subset of the D2 questions")
	limit := fs.Int("limit", 0, "replay at most this many sessions, newest label first (0 = all)")
	asJSON := fs.Bool("json", false, "emit the report as JSON instead of a table")
	out := fs.String("out", "", "also write the report to this file")
	floorFlags := map[string]*float64{
		decide.QD2Outcome:  fs.Float64("min-outcome", 0, "agreement floor for d2.outcome, in [0, 1]"),
		decide.QD2Failure:  fs.Float64("min-failure", 0, "agreement floor for d2.failure_cause, in [0, 1]"),
		decide.QD2TaskType: fs.Float64("min-task", 0, "agreement floor for d2.task_type, in [0, 1]"),
	}
	floorNames := map[string]string{"min-outcome": decide.QD2Outcome, "min-failure": decide.QD2Failure, "min-task": decide.QD2TaskType}

	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "decide eval: "+format+"\n%s\n", append(a, decideUsage)...)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		// Asking for help is not a usage error: same stream and exit code as
		// `swarmery decide help`.
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, decideUsage)
			return 0
		}
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
	}
	if *limit < 0 {
		return usageErr("--limit must be 0 or more")
	}

	opts := decide.EvalOptions{Limit: *limit}
	if raw := strings.TrimSpace(*truthSince); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return usageErr("--truth-since %q is not an RFC 3339 instant (e.g. 2026-09-29T00:00:00Z)", raw)
		}
		opts.TruthSince = t
	}
	for _, q := range strings.Split(*questions, ",") {
		if q = strings.TrimSpace(q); q == "" {
			continue
		}
		if !strings.Contains(q, ".") {
			q = "d2." + q
		}
		opts.Questions = append(opts.Questions, q)
	}
	// Only a floor the operator actually passed is a gate: 0 is a valid floor
	// value, so "unset" is read off the flag set, never off the number.
	floors := map[string]float64{}
	var badFloor string
	fs.Visit(func(f *flag.Flag) {
		id, ok := floorNames[f.Name]
		if !ok {
			return
		}
		// NaN fails every comparison, so it is rejected by name: as a floor it
		// would never trip.
		if v := *floorFlags[id]; math.IsNaN(v) || v < 0 || v > 1 {
			badFloor = f.Name
		} else {
			floors[id] = v
		}
	})
	if badFloor != "" {
		return usageErr("--%s must be in [0, 1]", badFloor)
	}

	// Rules-only needs no backend at all — Engine.Configured() is false and the
	// eval still runs. --llm builds the local backend from the daemon's own env
	// knobs; the claude backend is never built here, so nothing leaves the machine.
	// The rule switch (SWARMERY_DECIDE_R5) applies in both modes: it changes what
	// the rules answer, which is what a rules-only replay measures. So do the
	// config warnings: a mistyped SWARMERY_DECIDE_R5 must not leave the rule off
	// in silence.
	cfg, warn := decide.ConfigFromEnv(getenv)
	for _, w := range warn {
		fmt.Fprintf(stderr, "warning: decide: %s\n", w)
	}
	engine := &decide.Engine{R5PhaseRunFeature: cfg.R5PhaseRunFeature}
	if *llm {
		if cfg.URL == "" {
			return usageErr("--llm needs SWARMERY_DECIDE_URL (the local model server)")
		}
		cfg.Claude = false
		engine = decide.New(nil, cfg)
		fmt.Fprintf(stderr, "decide eval: %s\n", cfg)
		opts.Progress = func(done, total, errs int) {
			if done%decideProgressEvery == 0 || done == total {
				fmt.Fprintf(stderr, "decide eval: %d/%d sessions, %d backend errors\n", done, total, errs)
			}
		}
	}

	db, err := store.OpenNoMigrate(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "decide eval: %v\n", err)
		return 2
	}
	defer db.Close()
	// Belt and braces on top of Eval's SELECT-only contract: the connection
	// itself refuses a write. A pragma, not a data change.
	if _, err := db.ExecContext(ctx, `PRAGMA query_only = ON`); err != nil {
		fmt.Fprintf(stderr, "decide eval: set query_only: %v\n", err)
		return 2
	}

	rep, err := decide.Eval(ctx, db, engine, opts)
	if err != nil {
		fmt.Fprintf(stderr, "decide eval: %v\n", err)
		return 2
	}
	var buf bytes.Buffer
	if err := decide.RenderEval(&buf, rep, *asJSON); err != nil {
		fmt.Fprintf(stderr, "decide eval: render: %v\n", err)
		return 2
	}
	if _, err := stdout.Write(buf.Bytes()); err != nil {
		fmt.Fprintf(stderr, "decide eval: write report: %v\n", err)
		return 2
	}
	if *out != "" {
		if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
			fmt.Fprintf(stderr, "decide eval: write %s: %v\n", *out, err)
			return 2
		}
	}
	if missed := rep.MissedFloors(floors); len(missed) > 0 {
		for _, m := range missed {
			fmt.Fprintf(stderr, "decide eval: floor missed — %s\n", m)
		}
		return 1
	}
	return 0
}
