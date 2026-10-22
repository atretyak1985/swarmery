package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
)

const workaroundBackfillUsage = `usage: swarmery workaround-backfill [--db <path>] [--since <Nd|duration>] [--plan <yyyy-mm-dd-slug>]

Scan the assistant prose of every phase-linked session that started within
--since (default 30d) for guards.json's workaroundWords, store the hits the
ingest tail would have stored, and print the per-phase counts. --plan keeps
one plan's phases in the report. Idempotent: a second run adds nothing.`

// cmdWorkaroundBackfill is `swarmery workaround-backfill`: the one-off scan of
// sessions ingested before the tail hook existed, or linked to a plan after
// their lines were tailed (verify-not-trust phase 5).
func cmdWorkaroundBackfill(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("workaround-backfill", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprintln(out, workaroundBackfillUsage) }
	dbPath := dbFlag(fs)
	sinceRaw := fs.String("since", "30d", "how far back sessions are scanned: <N>d or a Go duration")
	plan := fs.String("plan", "", "report only this plan's phases (tasks.external_id, yyyy-mm-dd-slug)")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	since, err := parseSince(*sinceRaw)
	if err != nil {
		return usageError{err.Error()}
	}
	m := workaround.Default()
	if m.Empty() {
		return fmt.Errorf("no workaround words to scan with — check guards.json / SWARMERY_GUARDS")
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	now := time.Now()
	res, err := workaround.Backfill(db, m, now.Add(-since), *plan, now)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "workaround-backfill: %d phase-linked sessions scanned, %d new hits\n", res.Sessions, res.Added)
	for _, p := range res.Phases {
		capped := ""
		if p.Total > p.Hits {
			capped = fmt.Sprintf(" (+%d past the cap)", p.Total-p.Hits)
		}
		fmt.Fprintf(out, "  %s  phase %d  %-60s %d hits%s\n", p.Plan, p.Seq, truncName(p.Name, 60), p.Hits, capped)
	}
	return nil
}

// parseSince reads "30d" (days, the usual unit here) or any Go duration.
func parseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if v, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("--since wants a positive day count like 30d, got %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since wants <N>d or a positive duration, got %q", s)
	}
	return d, nil
}

func truncName(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
