package main

// `swarmery triage` — the terminal surface of the inbox triage agent
// (internal/triage):
//
//	swarmery triage run     start a run on the RUNNING daemon (HTTP), optionally wait for it
//	swarmery triage check   read-only audit: one run's record against the rows it left
//
// `run` never opens the database: the daemon owns the run (single flight, the
// policy check, the audit trail), the CLI only asks for one. It sends no Origin
// header, which the daemon's cross-origin fence lets through for a loopback Host.
// `check` opens the database with store.OpenNoMigrate and query_only, like
// `decide eval`, and never contacts the daemon.
//
// Exit codes are the contract, because the nightly routine runs `triage run`
// as a command step and gates on them.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

const triageUsage = `usage:
  swarmery triage run [--kinds <a,b>] [--project <slug>] [--cap <n>]
                      [--trigger operator|schedule] [--wait] [--wait-timeout <dur>]
                      [--port <n>] [--url <base>]
      Starts a triage run on the running daemon. --wait polls it until it ends
      and prints one summary line. A run already active is not an error.
      exit: 0 the run ended ok, or another run is already active
            1 the run ended failed, it no longer exists, --wait-timeout elapsed
              (the run keeps going), or the command was cancelled (SIGINT/SIGTERM)
            2 usage, or the daemon is unreachable

  swarmery triage check [--run <id>] [--strict-leftovers] [--db <path>]
      Read-only audit: the run's status is ok and its counters match the verdict
      rows it left. The classifier sessions still queued in the run's own scope
      are split into covered by this run, held by an earlier run (the engine's
      own blocking rule, per queued decision, over the runs before this one),
      arrived after the run started, and unexplained; unexplained ones (possibly
      beyond the run's cap) fail the check only with --strict-leftovers. Default
      run: the newest fleet-wide run the operator started. Never writes.
      exit: 0 everything matches · 1 a mismatch, or no such run · 2 usage or database error`

// triagePollInterval is how often `triage run --wait` asks for the run's status.
// A var so tests can shorten it.
var triagePollInterval = 5 * time.Second

// cmdTriage dispatches the `triage` subcommands and returns the exit code.
func cmdTriage(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, triageUsage)
		return 2
	}
	switch args[0] {
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return triageRun(ctx, args[1:], os.Stdout, os.Stderr, &http.Client{Timeout: 30 * time.Second})
	case "check":
		return triageCheck(args[1:], os.Stdout, os.Stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(os.Stderr, triageUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown triage subcommand %q\n%s\n", args[0], triageUsage)
		return 2
	}
}

// triageStartRequest is the POST body; omitempty keeps every unset flag out of it.
type triageStartRequest struct {
	Project string   `json:"project,omitempty"`
	Kinds   []string `json:"kinds,omitempty"`
	Trigger string   `json:"trigger,omitempty"`
	Cap     int      `json:"cap,omitempty"`
}

// triageRun starts a triage run on the daemon and, with --wait, follows it to the end.
// Exit codes: 0 the run ended ok, or another run was already active (nothing to do);
//
//	1 the run ended failed or is gone, the wait timed out, or ctx was cancelled;
//	2 usage, or the daemon is unreachable.
func triageRun(ctx context.Context, args []string, stdout, stderr io.Writer, client *http.Client) int {
	fs := flag.NewFlagSet("triage run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kinds := fs.String("kinds", "", "comma-separated kinds (default: every registered kind)")
	project := fs.String("project", "", "project slug (default: the whole fleet)")
	capN := fs.Int("cap", 0, "most items this run may take (0 = the server default)")
	trigger := fs.String("trigger", triage.TriggerOperator, "operator or schedule")
	wait := fs.Bool("wait", false, "follow the run until it ends")
	waitTimeout := fs.Duration("wait-timeout", 50*time.Minute, "give up waiting after this long")
	baseURL := fs.String("url", "", "daemon base URL (default http://127.0.0.1:<port>)")
	port := fs.Int("port", envPort(), "daemon port (env: SWARMERY_PORT)")

	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "triage run: "+format+"\n%s\n", append(a, triageUsage)...)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, triageUsage)
			return 0
		}
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
	}
	if *capN < 0 {
		return usageErr("--cap must be 0 or more")
	}
	if *trigger != triage.TriggerOperator && *trigger != triage.TriggerSchedule {
		return usageErr("--trigger must be operator or schedule, not %q", *trigger)
	}
	if *waitTimeout <= 0 {
		return usageErr("--wait-timeout must be positive")
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	body := triageStartRequest{Project: strings.TrimSpace(*project), Cap: *capN}
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			body.Kinds = append(body.Kinds, k)
		}
	}
	if set["trigger"] {
		body.Trigger = *trigger
	}

	base := strings.TrimRight(*baseURL, "/")
	if base == "" {
		base = fmt.Sprintf("http://127.0.0.1:%d", *port)
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/triage/runs", bytes.NewReader(raw))
	if err != nil {
		return usageErr("%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "triage run: cancelled")
			return 1
		}
		fmt.Fprintf(stderr, "triage run: daemon unreachable at %s: %v\n", base, err)
		return 2
	}
	var started struct {
		ID          int64  `json:"id"`
		ActiveRunID int64  `json:"activeRunId"`
		Error       string `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&started)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusConflict:
		if decodeErr != nil || started.ActiveRunID == 0 {
			fmt.Fprintln(stdout, "a triage run is already active")
		} else {
			fmt.Fprintf(stdout, "triage run %d is already active\n", started.ActiveRunID)
		}
		return 0
	case resp.StatusCode != http.StatusAccepted:
		msg := started.Error
		if msg == "" {
			msg = resp.Status
		}
		fmt.Fprintf(stderr, "triage run: the daemon refused the run: %s\n", msg)
		return 2
	case decodeErr != nil || started.ID == 0:
		fmt.Fprintf(stderr, "triage run: the daemon answered 202 without a run id\n")
		return 2
	}
	fmt.Fprintf(stdout, "triage run %d started\n", started.ID)
	if !*wait {
		return 0
	}
	return triageFollow(ctx, client, base, started.ID, *waitTimeout, stdout, stderr)
}

// triageFollow polls one run until it leaves running, the wait times out, the
// run turns out to be gone (404), or ctx ends. Any other poll error is reported
// and retried: the run lives in the daemon, not in this process.
func triageFollow(ctx context.Context, client *http.Client, base string, id int64, timeout time.Duration, stdout, stderr io.Writer) int {
	deadline := time.Now().Add(timeout)
	cancelled := func() int {
		fmt.Fprintf(stderr, "triage run %d: cancelled; stopped waiting, the run keeps going\n", id)
		return 1
	}
	for {
		run, err := triageGetRun(ctx, client, base, id)
		switch {
		case err != nil && ctx.Err() != nil:
			return cancelled()
		case errors.Is(err, errTriageRunGone):
			fmt.Fprintf(stderr, "triage run %d no longer exists\n", id)
			return 1
		case err != nil:
			fmt.Fprintf(stderr, "triage run: poll run %d: %v\n", id, err)
		case run.Status != triage.StatusRunning:
			fmt.Fprintf(stdout, "triage run %d: %s · applied %d · suggested %d · skipped %d · failed %d · $%.2f\n",
				run.ID, run.Status, run.Applied, run.Suggested, run.Skipped, run.Failed, run.CostUSD)
			if run.Status == triage.StatusOK {
				return 0
			}
			if run.Error != "" {
				fmt.Fprintf(stdout, "error: %s\n", run.Error)
			}
			return 1
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			fmt.Fprintf(stdout, "triage run %d: still running after %s; stopped waiting, the run keeps going\n", id, timeout)
			return 1
		}
		select {
		case <-ctx.Done():
			return cancelled()
		case <-time.After(min(triagePollInterval, remaining)):
		}
	}
}

// errTriageRunGone is triageGetRun's answer to a 404: the run no longer exists.
var errTriageRunGone = errors.New("triage run not found")

// triageGetRun reads GET /api/triage/runs/{id}.
func triageGetRun(ctx context.Context, client *http.Client, base string, id int64) (triage.Run, error) {
	var run triage.Run
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/triage/runs/%s", base, url.PathEscape(fmt.Sprint(id))), nil)
	if err != nil {
		return run, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return run, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return run, errTriageRunGone
	}
	if resp.StatusCode != http.StatusOK {
		return run, fmt.Errorf("status %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&run); err != nil {
		return run, fmt.Errorf("decode: %w", err)
	}
	return run, nil
}

// triageCheck compares one run's record with the rows it left. Default run: the newest
// fleet-wide run started by the operator. Rules 1 (status ok) and 2 (counters = verdict
// rows) are always strict. Rule 3 splits the classifier sessions still queued in the run's
// own scope (its project, or the fleet) into covered by this run, held by an earlier run
// (triage.BlockedBefore: the engine's blocking rule over the runs with a lower id),
// arrived after the run started, and unexplained; unexplained ones fail the check only
// with --strict-leftovers, since a run that stopped at its cap leaves them too. Rule 4
// counts what the classifier cannot reach. Rules 3 and 4 are skipped for a run whose
// kinds exclude the classifier. Exit 0 when everything matches, 1 on any mismatch,
// 2 on usage/db error. It opens the database read-only and never writes.
func triageCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("triage check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := dbFlag(fs)
	runID := fs.Int64("run", 0, "run id (default: the newest fleet-wide operator run)")
	strict := fs.Bool("strict-leftovers", false, "fail the check on unexplained classifier leftovers")
	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "triage check: "+format+"\n%s\n", append(a, triageUsage)...)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, triageUsage)
			return 0
		}
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
	}
	if *runID < 0 {
		return usageErr("--run must be a run id")
	}

	db, err := store.OpenNoMigrate(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "triage check: %v\n", err)
		return 2
	}
	defer db.Close()
	// The audit issues SELECTs alone; the connection refuses a write on top.
	if _, err := db.Exec(`PRAGMA query_only = ON`); err != nil {
		fmt.Fprintf(stderr, "triage check: set query_only: %v\n", err)
		return 2
	}
	ok, err := triageAudit(db, *runID, *strict, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "triage check: %v\n", err)
		return 2
	}
	if !ok {
		return 1
	}
	return 0
}

// triageCounterStates maps each run counter to the verdict states it counts.
var triageCounterStates = []struct {
	name   string
	states []string
}{
	{"applied", []string{triage.StateApplied, triage.StateUndoing, triage.StateUndone}},
	{"suggested", []string{triage.StateSuggested, triage.StateSample, triage.StateAccepted, triage.StateAudited, triage.StateStale}},
	{"skipped", []string{triage.StateSkipped}},
	{"failed", []string{triage.StateFailed}},
	{"rejected", []string{triage.StateRejected}},
}

// triageClassifierKind is the verdict kind of the label-queue classifier.
const triageClassifierKind = "classifier"

// triageCoveringStates are the classifier verdict states in which a question
// is (or is again) in the label queue; any of them from the checked run on a
// queued decision puts its session in the "covered by this run" bucket of
// check 3. applied is not one: an applied label takes the question out of the
// queue. undone and undoing are: the run applied a label and the operator took
// it back, so the decision waits again and the engine never judges it twice.
var triageCoveringStates = []string{
	triage.StateSample, triage.StateSkipped, triage.StateFailed, triage.StateRejected, triage.StateSuggested,
	triage.StateUndone, triage.StateUndoing,
}

// triageLeftoverSuffix explains an unexplained count when it does not fail the check.
const triageLeftoverSuffix = " (may be beyond the run's cap; re-run with --strict-leftovers to fail on it)"

// triageLeftoverShown caps how many session uuids checks 3 and 4 print.
const triageLeftoverShown = 10

// triageAudit runs the four checks and prints them. ok is false on any
// mismatch, or when no run matches; err is a database error. strict makes
// unexplained classifier leftovers a mismatch; otherwise they are informational.
func triageAudit(db *sql.DB, runID int64, strict bool, out io.Writer) (ok bool, err error) {
	var (
		id, applied, suggested, skipped, failed, rejected int64
		status, kinds, startedAt                          string
		scope                                             sql.NullInt64
	)
	const cols = `id, status, applied, suggested, skipped, failed, rejected, scope_project_id, kinds, started_at`
	q := `SELECT ` + cols + ` FROM triage_runs WHERE id = ?`
	qArgs := []any{runID}
	if runID == 0 {
		q = `SELECT ` + cols + ` FROM triage_runs
		      WHERE trigger = 'operator' AND scope_project_id IS NULL ORDER BY id DESC LIMIT 1`
		qArgs = nil
	}
	err = db.QueryRow(q, qArgs...).Scan(&id, &status, &applied, &suggested, &skipped, &failed, &rejected, &scope, &kinds, &startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if runID == 0 {
			fmt.Fprintln(out, "no fleet-wide operator triage run found")
		} else {
			fmt.Fprintf(out, "no triage run %d\n", runID)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fmt.Fprintf(out, "triage run %d\n", id)
	ok = true
	mark := func(good bool) string {
		if good {
			return "ok"
		}
		ok = false
		return "MISMATCH"
	}

	// 1. status
	fmt.Fprintf(out, "  %-8s status: %s (want ok)\n", mark(status == triage.StatusOK), status)

	// 2. counters vs verdict rows
	recorded := map[string]int64{"applied": applied, "suggested": suggested, "skipped": skipped, "failed": failed, "rejected": rejected}
	for _, c := range triageCounterStates {
		var rows int64
		ph := strings.TrimSuffix(strings.Repeat("?,", len(c.states)), ",")
		a := []any{id}
		for _, s := range c.states {
			a = append(a, s)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM triage_verdicts WHERE run_id = ? AND state IN (`+ph+`)`, a...).Scan(&rows); err != nil {
			return false, err
		}
		fmt.Fprintf(out, "  %-8s %s: run says %d, rows %d\n", mark(rows == recorded[c.name]), c.name, recorded[c.name], rows)
	}

	// 3 and 4 concern the classifier's queue: a run that did not include the
	// classifier has nothing to answer for there.
	var runKinds []string
	if err := json.Unmarshal([]byte(kinds), &runKinds); err != nil {
		return false, fmt.Errorf("run %d kinds %q: %w", id, kinds, err)
	}
	if len(runKinds) > 0 && !slices.Contains(runKinds, triageClassifierKind) {
		fmt.Fprintf(out, "  %-8s classifier was not part of this run\n", "info")
		return ok, nil
	}
	if err := triageLeftovers(db, id, scope, startedAt, strict, out, mark); err != nil {
		return false, err
	}
	return ok, nil
}

// triageLeftovers runs checks 3 and 4 for run id. Check 3 splits the queued,
// ingested sessions of the run's own scope into four buckets, each session in
// the first that applies. The classifier offers one item per session with one
// part per queued decision (ref = the decision id), and the engine skips the
// whole item when any part is blocked, so every probe is per queued decision:
//
//	covered by this run     a covering verdict from this run on a queued decision
//	held by an earlier run  triage.BlockedBefore on a queued decision — the
//	                        engine's own blocking rule, over the runs before this one
//	arrived after the run   every queued decision is newer than the run's start
//	unexplained             none of the above
//
// Only unexplained can fail the check, and only when strict: a non-strict run
// may simply have stopped at its cap, which the run row does not record.
// Check 4 is informational.
func triageLeftovers(db *sql.DB, id int64, scope sql.NullInt64, startedAt string, strict bool,
	out io.Writer, mark func(bool) string) error {
	// decisions.created_at and triage_runs.started_at are RFC 3339, with or
	// without fractional seconds: parse both, never compare the strings.
	start, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return fmt.Errorf("run %d started_at %q: %w", id, startedAt, err)
	}
	opts := decide.QueueOptions{Limit: -1, SystemPath: ingest.SystemDir()}
	if scope.Valid {
		opts.ProjectID = scope.Int64
	}
	queue, err := decide.LabelQueue(db, opts)
	if err != nil {
		return fmt.Errorf("label queue: %w", err)
	}
	// The classifier collects neither a decision with no session nor one whose
	// session has no sessions row (not ingested yet, or removed by retention):
	// those wait for a later run and belong to rule 4, not to the leftovers.
	ingested, err := db.Prepare(`SELECT EXISTS (SELECT 1 FROM sessions WHERE session_uuid = ?)`)
	if err != nil {
		return err
	}
	defer ingested.Close()
	ph := strings.TrimSuffix(strings.Repeat("?,", len(triageCoveringStates)), ",")
	coveredBy, err := db.Prepare(`SELECT EXISTS (SELECT 1 FROM triage_verdicts
		 WHERE kind = ? AND run_id = ? AND ref = ? AND state IN (` + ph + `))`)
	if err != nil {
		return err
	}
	defer coveredBy.Close()
	covers := func(ref string) (bool, error) {
		a := []any{triageClassifierKind, id, ref}
		for _, s := range triageCoveringStates {
			a = append(a, s)
		}
		var yes bool
		err := coveredBy.QueryRow(a...).Scan(&yes)
		return yes, err
	}

	type queued struct {
		ref   string // the decision id, as the classifier's part ref
		after bool   // created after the run started; an unparsable time is not
	}
	var (
		sessions, notIngested         []string // first-seen order
		decisions                     = map[string][]queued{}
		isIngested                    = map[string]bool{}
		sessionless, notIngestedCount int
	)
	for _, it := range queue {
		s := it.SessionUUID
		if s == "" {
			sessionless++
			continue
		}
		known, checked := isIngested[s]
		if !checked {
			if err := ingested.QueryRow(s).Scan(&known); err != nil {
				return err
			}
			isIngested[s] = known
			if known {
				sessions = append(sessions, s)
			} else {
				notIngested = append(notIngested, s)
			}
		}
		if !known {
			notIngestedCount++
			continue
		}
		created, perr := time.Parse(time.RFC3339, it.CreatedAt)
		decisions[s] = append(decisions[s], queued{
			ref: strconv.FormatInt(it.ID, 10), after: perr == nil && created.After(start),
		})
	}

	var covered, held, later int
	var unexplained []string
	for _, s := range sessions {
		var isCovered, isHeld bool
		allAfter := true
		for _, d := range decisions[s] {
			allAfter = allAfter && d.after
			if !isCovered {
				if isCovered, err = covers(d.ref); err != nil {
					return err
				}
			}
			if !isHeld {
				if isHeld, err = triage.BlockedBefore(db, triageClassifierKind, d.ref, id, start); err != nil {
					return err
				}
			}
		}
		switch {
		case isCovered:
			covered++
		case isHeld:
			held++
		case allAfter:
			later++
		default:
			unexplained = append(unexplained, s)
		}
	}
	fmt.Fprintf(out, "  %-8s queued sessions covered by this run: %d\n", "info", covered)
	fmt.Fprintf(out, "  %-8s queued sessions held by an earlier run: %d\n", "info", held)
	fmt.Fprintf(out, "  %-8s queued sessions arrived after the run started: %d\n", "info", later)
	label, suffix := "info", ""
	switch {
	case strict:
		label = mark(len(unexplained) == 0)
	case len(unexplained) > 0:
		suffix = triageLeftoverSuffix
	}
	fmt.Fprintf(out, "  %-8s queued sessions unexplained: %d%s\n", label, len(unexplained), suffix)
	printTriageSessions(out, unexplained)

	// 4. informational: queue decisions out of the classifier's reach. Never fails the check.
	fmt.Fprintf(out, "  %-8s queue decisions with no session (out of the classifier's reach): %d\n", "info", sessionless)
	fmt.Fprintf(out, "  %-8s queue decisions whose session is not ingested (out of the classifier's reach): %d\n", "info", notIngestedCount)
	printTriageSessions(out, notIngested)
	return nil
}

// printTriageSessions lists up to triageLeftoverShown session uuids under a check line.
func printTriageSessions(out io.Writer, uuids []string) {
	for i, s := range uuids {
		if i == triageLeftoverShown {
			fmt.Fprintf(out, "           … and %d more\n", len(uuids)-triageLeftoverShown)
			return
		}
		fmt.Fprintf(out, "           %s\n", s)
	}
}
