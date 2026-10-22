package main

// `swarmery wtcleanup` — the terminal surface of internal/wtcleanup, the
// janitor's keep-unmerged pile narrowed to closed phases and cards with a proof
// their content is in main:
//
//	swarmery wtcleanup scan      read-only: candidates by proof kind, per repository
//	swarmery wtcleanup propose   store them as the Needs-you "Worktree cleanup" card
//	swarmery wtcleanup apply     remove selected paths (salvage branch first)
//
// The daily worktree-cleanup routine runs scan (for its summary step) and
// propose. apply is the card's "Remove selected" for a terminal: it is the only
// subcommand that removes anything, and only paths an open proposal with a
// proof names.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtcleanup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

const wtcleanupUsage = `usage:
  swarmery wtcleanup scan    [--db <path>] [--json] [--out <file>]
      Read-only (no migrations, query_only): keep-unmerged worktrees whose
      phase or board card is closed, each with a proof its content is in main
      (landing merged, or merging it into main changes nothing). A candidate
      without proof is counted, never offered. --out also writes the JSON to
      <file> (the routine's summary step reads it).
  swarmery wtcleanup propose [--db <path>] [--json]
      Scan, then store the result as the open proposals the Needs-you
      "Worktree cleanup" card lists. Removes nothing.
  swarmery wtcleanup apply   --id <proposal> --path <worktree> [--path <worktree> ...] [--db <path>]
      Remove the given worktrees of that proposal's repository: proof required,
      salvage branch salvage/<branch>-<ts> written first, idempotent. The card's
      "Remove selected" from a terminal. Exits 1 when a path failed.`

// wtcleanupLive is the liveness seam: the real process + session check.
var wtcleanupLive = func(db *sql.DB) wtjanitor.Liveness {
	return wtjanitor.ProcLiveness{Proc: procwatch.OsProvider{}, DB: db}
}

func cmdWtcleanup(args []string) error {
	return runWtcleanup(args, os.Stdout)
}

func runWtcleanup(args []string, out io.Writer) error {
	if len(args) == 0 {
		return usageError{wtcleanupUsage}
	}
	switch args[0] {
	case "scan":
		return wtcleanupScan(args[1:], out)
	case "propose":
		return wtcleanupPropose(args[1:], out)
	case "apply":
		return wtcleanupApply(args[1:], out)
	case "-h", "--help", "help":
		fmt.Fprintln(out, wtcleanupUsage)
		return nil
	default:
		return usageError{fmt.Sprintf("unknown wtcleanup subcommand %q\n%s", args[0], wtcleanupUsage)}
	}
}

// scanReport is what scan prints with --json and writes with --out.
type scanReport struct {
	wtcleanup.Scan
	Repos []wtcleanup.RepoSummary `json:"repos"`
}

func wtcleanupScan(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("wtcleanup scan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := dbFlag(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	outFile := fs.String("out", "", "also write the JSON report to this file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{wtcleanupUsage}
	}
	db, err := store.OpenReader(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	scan, err := wtcleanup.Candidates(db, wtcleanup.ExecGit{}, wtcleanupLive(db))
	if err != nil {
		return err
	}
	rep := scanReport{Scan: scan, Repos: wtcleanup.Summarize(scan)}
	if *outFile != "" {
		if err := writeJSONFile(*outFile, rep); err != nil {
			return err
		}
	}
	if *asJSON {
		return writeJSONTo(out, rep)
	}
	printScan(out, rep)
	return nil
}

func wtcleanupPropose(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("wtcleanup propose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := dbFlag(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{wtcleanupUsage}
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	scan, err := wtcleanup.Candidates(db, wtcleanup.ExecGit{}, wtcleanupLive(db))
	if err != nil {
		return err
	}
	sums, err := wtcleanup.Propose(db, scan, time.Now())
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSONTo(out, sums)
	}
	if len(sums) == 0 {
		fmt.Fprintln(out, "wtcleanup propose: no candidates, no open proposals")
		return nil
	}
	for _, s := range sums {
		fmt.Fprintf(out, "%s: %d with proof (listed), %d without (counted) · added %d, refreshed %d, dropped %d\n",
			repoLabel(s.RepoRoot, s.ProjectSlug), s.WithProof, s.WithoutProof, s.Added, s.Refreshed, s.Dropped)
	}
	return nil
}

// pathsFlag collects a repeatable --path.
type pathsFlag []string

func (p *pathsFlag) String() string { return strings.Join(*p, ",") }
func (p *pathsFlag) Set(v string) error {
	*p = append(*p, v)
	return nil
}

func wtcleanupApply(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("wtcleanup apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := dbFlag(fs)
	id := fs.Int64("id", 0, "proposal id (any open proposal of the repository)")
	var paths pathsFlag
	fs.Var(&paths, "path", "worktree path to remove (repeatable)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *id <= 0 || len(paths) == 0 {
		return usageError{wtcleanupUsage}
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	mgr := &worktree.Manager{Git: worktree.ExecGit{}}
	root, _ := mgr.RootDir()
	janitor := &wtjanitor.Service{
		DB: db, Git: wtjanitor.RepoGit{}, Live: wtcleanupLive(db),
		DaemonRoot: root, Remover: wtjanitor.NewRealRemover(mgr),
	}
	res, err := wtcleanup.Apply(db, wtcleanup.ExecGit{}, janitor, *id, paths, time.Now())
	if errors.Is(err, wtcleanup.ErrNotFound) {
		return fmt.Errorf("wtcleanup apply: no proposal %d", *id)
	}
	if err != nil {
		return err
	}
	failed := 0
	for _, p := range res.Paths {
		line := fmt.Sprintf("  %-15s %s", p.Outcome, p.Path)
		if p.SalvageBranch != "" {
			line += " → " + p.SalvageBranch
		}
		if p.Reason != "" {
			line += " — " + p.Reason
		}
		fmt.Fprintln(out, line)
		if p.Outcome == wtjanitor.ForceFailed {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("wtcleanup apply: %d path(s) failed; their worktrees are untouched", failed)
	}
	return nil
}

func printScan(out io.Writer, rep scanReport) {
	if len(rep.Repos) == 0 {
		fmt.Fprintln(out, "wtcleanup scan: no keep-unmerged worktrees in the journal")
		return
	}
	for _, r := range rep.Repos {
		fmt.Fprintf(out, "%s: landing %d · content-diff %d · without proof %d",
			repoLabel(r.RepoRoot, r.ProjectSlug), r.ByProof[wtcleanup.ProofLanding],
			r.ByProof[wtcleanup.ProofContentDiff], r.ByProof[wtcleanup.ProofNone])
		if len(r.Skipped) > 0 {
			var parts []string
			for _, k := range []string{wtcleanup.SkipOpenOwner, wtcleanup.SkipUnknownOwner, wtcleanup.SkipActive,
				wtcleanup.SkipLocked, wtcleanup.SkipGone, wtcleanup.SkipRepoError} {
				if n := r.Skipped[k]; n > 0 {
					parts = append(parts, fmt.Sprintf("%s %d", k, n))
				}
			}
			fmt.Fprintf(out, " · not candidates: %s", strings.Join(parts, ", "))
		}
		fmt.Fprintln(out)
		for _, c := range rep.Candidates {
			if c.RepoRoot != r.RepoRoot {
				continue
			}
			fmt.Fprintf(out, "  %-12s %s [%s] — %s · %s\n", c.Proof.Kind, c.Path, c.Branch, c.Owner, c.Proof.Detail)
		}
	}
}

func repoLabel(root, slug string) string {
	if slug == "" {
		return root
	}
	return slug + " (" + root + ")"
}

func writeJSONTo(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
