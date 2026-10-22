package main

// verify-not-trust phase 18: `swarmery skill-audit`.
//
// Prints the skill audit — unused agents and skills, similar skill
// descriptions, skills per session — computed now from the registry and the
// last 30 days of events (internal/sysscan/skillaudit.go, the same code the
// linter files its findings from). The weekly skill-audit routine runs it with
// --json as its first step.
//
// Read-only like `memory lint --all`: store.OpenNoMigrate with query_only, so
// it is safe while the daemon is serving and safe against a copy of the live
// database. A report, never a gate: exit 0 whatever it finds.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

const skillAuditUsage = "usage: swarmery skill-audit [--db <path>] [--json] [--similarity <0..1>] [--p95 <n>] [--window <days>]"

func skillAudit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("skill-audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := dbFlag(fs)
	asJSON := fs.Bool("json", false, "emit the report as JSON instead of text")
	similarity := fs.Float64("similarity", 0,
		"similarity threshold for this run only (default: guards.json skillSimilarityThreshold)")
	p95Limit := fs.Int("p95", 0,
		"skills-per-session p95 limit for this run only (default: guards.json sessionSkillLoadP95)")
	window := fs.Int("window", sysscan.AuditWindowDays, "telemetry window in days")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{skillAuditUsage}
	}
	if *similarity < 0 || *similarity > 1 || *p95Limit < 0 || *window <= 0 {
		return usageError{skillAuditUsage + "\n  --similarity is in (0, 1], --p95 and --window are positive"}
	}

	opts := sysscan.AuditOptions{WindowDays: *window, SimilarityThreshold: *similarity, SessionSkillLoadP95: *p95Limit}
	if opts.SimilarityThreshold == 0 || opts.SessionSkillLoadP95 == 0 {
		// A broken SWARMERY_GUARDS override fails the command: the report must
		// say which thresholds it ran at, not quietly use the shipped ones.
		g, err := guards.Load()
		if err != nil {
			return err
		}
		if opts.SimilarityThreshold == 0 {
			opts.SimilarityThreshold = g.SkillSimilarityThreshold
		}
		if opts.SessionSkillLoadP95 == 0 {
			opts.SessionSkillLoadP95 = g.SessionSkillLoadP95
		}
	}

	db, err := store.OpenNoMigrate(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA query_only = ON`); err != nil {
		return fmt.Errorf("skill-audit: set query_only: %w", err)
	}
	rep, err := sysscan.ComputeSkillAudit(db, opts)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printSkillAudit(out, rep)
	return nil
}

// printSkillAudit writes the text form: one block per report section, then the
// per-project skills-per-session table.
func printSkillAudit(out io.Writer, rep sysscan.SkillAudit) {
	fmt.Fprintf(out, "skill audit  (last %d days since %s; similarity >= %.2f; skills/session p95 limit %d)\n",
		rep.WindowDays, rep.Since, rep.SimilarityThreshold, rep.SessionSkillLoadP95)

	section := func(title string, n int) {
		fmt.Fprintf(out, "\n%s (%d)\n", title, n)
	}
	component := func(c sysscan.AuditComponent) {
		where := c.Scope
		if c.ProjectSlug != nil {
			where += " " + *c.ProjectSlug
		}
		fmt.Fprintf(out, "  %-40s %-20s %s\n", c.Name, where, c.Path)
	}

	section("unused agents", len(rep.UnusedAgents))
	for _, c := range rep.UnusedAgents {
		component(c)
	}
	section("unused skills", len(rep.UnusedSkills))
	for _, c := range rep.UnusedSkills {
		component(c)
	}
	section("similar descriptions", len(rep.SimilarPairs))
	for _, p := range rep.SimilarPairs {
		fmt.Fprintf(out, "  %.2f  %s  ~  %s\n", p.Similarity, p.A.Name, p.B.Name)
	}
	section("used skills", len(rep.UsedSkills))
	for _, u := range rep.UsedSkills {
		fmt.Fprintf(out, "  %-40s %d %s\n", u.Name, u.Sessions, plural(u.Sessions, "session"))
	}

	section("skills per session", len(rep.SkillLoad))
	if len(rep.SkillLoad) == 0 {
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  project\tsessions\tp95\tmax\t")
	for _, l := range rep.SkillLoad {
		mark := ""
		if l.OverLimit {
			mark = "over limit"
		}
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%s\n", strings.TrimSpace(l.Slug), l.Sessions, l.P95, l.Max, mark)
	}
	tw.Flush()
}
