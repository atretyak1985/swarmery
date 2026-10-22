package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cachecold"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/handoff"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// cacheColdTick is how often the daemon re-derives the «cache cold» prediction.
// The API TTL is 5 min, so a minute is the coarsest tick that still flags an
// API session before most of its idle spell is gone.
const cacheColdTick = 60 * time.Second

// cacheColdConfig is the daemon's cache-cold config: the TTLs from guards.json
// (SWARMERY_GUARDS override honoured), the handoff threshold as the prediction's
// footprint gate, and claudeacct's billing-kind resolver. A broken guards
// override is logged and the shipped defaults apply — a cost warning is not
// worth refusing to start for.
func cacheColdConfig() cachecold.Config {
	g, err := guards.Load()
	if err != nil {
		log.Printf("warn: cachecold: %v — using the shipped cacheColdMinutes", err)
		if g, err = guards.Defaults(); err != nil {
			log.Printf("warn: cachecold: %v — cache-cold marks are off", err)
		}
	}
	return cachecold.Config{
		ColdMinutes:  g.CacheColdMinutes,
		MinFootprint: handoff.Threshold,
		KindOf:       func(account string) cachecold.Kind { return cachecold.Kind(claudeacct.BillingKind(account)) },
	}
}

// startCacheColdTick runs cachecold.Sync every cacheColdTick and announces each
// session whose flag opened or cleared. bus may be nil (serve --no-ingest).
func startCacheColdTick(db *sql.DB, bus *ingest.Bus, cfg cachecold.Config) {
	go func() {
		tick := func() {
			res, err := cachecold.Sync(db, time.Now(), cfg)
			if err != nil {
				log.Printf("error: cachecold.Sync: %v", err)
				return
			}
			if len(res.Opened) > 0 {
				log.Printf("cachecold: %d session(s) went cache-cold", len(res.Opened))
			}
			if bus == nil {
				return
			}
			for _, id := range append(res.Opened, res.Cleared...) {
				bus.Publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: id})
			}
		}
		tick()
		t := time.NewTicker(cacheColdTick)
		defer t.Stop()
		for range t.C {
			tick()
		}
	}()
	log.Printf("swarmery cache-cold tick started (interval %s, ttl subscription %dm / api %dm)",
		cacheColdTick, cfg.ColdMinutes.Subscription, cfg.ColdMinutes.API)
}

// cmdCacheColdBackfill is `swarmery cachecold-backfill [--db <path>] [--since 30d]`:
// re-derive turns.cold_restart over the window with the live rule and print how
// many turns each compared factor would mark. Idempotent — a second run reports
// 0 rows changed.
func cmdCacheColdBackfill(args []string, out io.Writer) error {
	const usageLine = "usage: swarmery cachecold-backfill [--db <path>] [--since <Nd|duration>]"
	fs := flag.NewFlagSet("cachecold-backfill", flag.ExitOnError)
	dbPath := dbFlag(fs)
	sinceArg := fs.String("since", "30d", "window to re-derive: Nd (days) or a Go duration")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("%s", usageLine)
	}
	window, err := parseWindow(*sinceArg)
	if err != nil {
		return fmt.Errorf("--since: %w (%s)", err, usageLine)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	cfg := cacheColdConfig()
	since := time.Now().Add(-window).UTC()
	res, err := cachecold.Backfill(db, since, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "cachecold-backfill %s since %s (ttl subscription %dm / api %dm)\n",
		*dbPath, since.Format(time.RFC3339), cfg.ColdMinutes.Subscription, cfg.ColdMinutes.API)
	fmt.Fprintf(out, "  main-thread assistant turns evaluated: %d\n", res.Evaluated)
	fmt.Fprintf(out, "  cold restarts (factor %.2f): %d  ($%.2f)\n", cachecold.Factor, res.Marked, res.MarkedCostUSD)
	fmt.Fprintf(out, "  rows changed: %d\n", res.Changed)
	fmt.Fprintf(out, "  factor comparison:\n")
	for _, f := range res.ByFactor {
		fmt.Fprintf(out, "    %.1f  %5d marks  $%.2f\n", f.Factor, f.Marked, f.CostUSD)
	}
	return nil
}

// parseWindow reads "30d" as 30 days, anything else as a Go duration.
func parseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%q is not a positive day count", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration", s)
	}
	return d, nil
}
