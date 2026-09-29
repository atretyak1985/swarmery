// Package accountprune is `swarmery account prune`: the only supported way to
// remove the settings keys an estate binding now supplies from the sub-repo
// files that still carry a hand-made copy.
//
// # What it removes, and what it never touches
//
// Only runsettings.EstateKeys (pluginConfigs, extraKnownMarketplaces), whole
// keys only, and only when runsettings.Redundant — the SUBSET rule the doctor's
// settings-block detector also calls — says every entry of the file's copy
// exists in the estate's with an identical value (keys.go). permissions,
// enabledMcpjsonServers, enabledPlugins and the swarmery binding object are
// never candidates (neverRedundant).
//
// # Three exclusions that keep it from eating its own source
//
//  1. The ESTATE'S OWN TWO FILES — <EstateRoot>/.claude/settings.json (the
//     --settings supply) and <EstateRoot>/.claude/settings.local.json (the
//     binding) — are matched by resolved path (claudeacct.SameFile, i.e.
//     filepath.EvalSymlinks on both sides, never a string compare) and listed
//     "estate source", never eligible. Compared against the estate, which is
//     itself, every key in them is trivially redundant: unexcluded, the prune
//     would empty the estate in one write.
//  2. A TRACKED file (claudeacct.FileProvenance: tracked, or git cannot tell)
//     that is eligible makes Apply refuse the WHOLE run unless
//     Options.IncludeTracked — writing it would push this machine's config at
//     everyone who pulls, and a partial apply would leave the operator unable
//     to tell which files changed.
//  3. An estate that is not ADMITTED (its store carries no `# swarmery-root:`
//     line for the root) delivers nothing, so nothing is redundant with it.
//
// # Scope
//
// Plan enumerates with claudeacct.ScanSettingsFiles — the one bounded walk the
// doctor's detector uses too, so the two always see the same files — and reads
// each file with claudeacct.ReadTrustedSettings. A file carrying no EstateKey
// is not listed at all. It never reads a credential store and never prints a
// value: names of keys and entries only.
package accountprune

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// Reasons a Target carries. None of them contains the word the dry run's
// eligible lines are counted by.
const (
	ReasonRedundant        = "redundant with the estate"
	ReasonEstateSource     = "estate source"
	ReasonNothingRedundant = "nothing redundant"
	ReasonNotAdmitted      = "estate not admitted"
	reasonUnreadablePrefix = "unreadable: "
)

// Target is one settings file Plan considered.
type Target struct {
	Path       string    `json:"path"`
	EstateRoot string    `json:"estateRoot"` // the estate the keys were compared against
	Keys       []string  `json:"keys"`       // redundant EstateKeys Apply removes; [] never null
	Kept       []KeptKey `json:"kept"`       // EstateKeys that stay, with the entry names the estate lacks
	Status     GitStatus `json:"status"`
	Eligible   bool      `json:"eligible"`
	Reason     string    `json:"reason"`
}

// Options steer Apply.
type Options struct {
	DryRun         bool // write nothing; report what would change
	IncludeTracked bool // allow writing an eligible TRACKED target
	// QuarantineDir is the phase's $Q; pre-images go to <QuarantineDir>/prune.
	// "" means DefaultQuarantineDir(Now).
	QuarantineDir string
	// Now dates the default quarantine directory; zero means time.Now().
	Now time.Time
}

// Change is one file Apply rewrote (or, in a dry run, would rewrite).
type Change struct {
	Path   string   `json:"path"`
	Keys   []string `json:"keys"`
	Backup string   `json:"backup,omitempty"` // the pre-image; "" in a dry run
}

// Result is what Apply did.
type Result struct {
	DryRun  bool     `json:"dryRun"`
	Changed []Change `json:"changed"` // never null
}

// TrackedError is Apply's refusal: eligible targets git tracks (or cannot
// classify) are in scope and Options.IncludeTracked is false. Nothing was
// written.
type TrackedError struct{ Paths []string }

func (e *TrackedError) Error() string {
	return fmt.Sprintf("refusing to prune: %d eligible file(s) are tracked by git, or git cannot tell — "+
		"writing them would commit this machine's settings for everyone who pulls. Nothing was written. "+
		"Re-run with --include-tracked to write them anyway:\n  %s",
		len(e.Paths), strings.Join(e.Paths, "\n  "))
}

// Plan lists every settings file under each root that carries an EstateKey,
// plus the estate's own two files, each classified against the estate the
// root resolves to. Sorted by path, de-duplicated. A root that resolves to no
// estate is an error: with nothing supplying these keys, nothing is redundant.
func Plan(roots []string) ([]Target, error) {
	seen := map[string]bool{}
	out := []Target{}
	for _, root := range roots {
		ts, err := planRoot(root)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if !seen[t.Path] {
				seen[t.Path] = true
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func planRoot(root string) ([]Target, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", root, err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}
	res := claudeacct.Resolve(abs)
	if res.EstateRoot == "" {
		return nil, fmt.Errorf("%s resolves to no estate: nothing supplies these keys, so nothing is redundant "+
			"(declare one with `swarmery account estate use <key> --path <root>`)", abs)
	}
	estateFile := filepath.Join(res.EstateRoot, filepath.FromSlash(claudeacct.ProjectSettingsFile))
	bindingFile := filepath.Join(res.EstateRoot, filepath.FromSlash(claudeacct.BindingFile))

	var estate map[string]any
	if res.EstateAdmitted {
		estate, _ = claudeacct.ReadTrustedSettingsWithin(estateFile, res.EstateRoot)
	}

	var out []Target
	for _, f := range claudeacct.ScanSettingsFiles(abs) {
		t := Target{Path: f, EstateRoot: res.EstateRoot, Keys: []string{}, Kept: []KeptKey{}}
		if claudeacct.SameFile(f, estateFile) || claudeacct.SameFile(f, bindingFile) {
			t.Reason = ReasonEstateSource
			t.Status = gitStatus(f)
			out = append(out, t)
			continue
		}
		doc, why := claudeacct.ReadTrustedSettings(f)
		if doc == nil {
			if why != "" {
				t.Reason = reasonUnreadablePrefix + why
				t.Status = gitStatus(f)
				out = append(out, t)
			}
			continue // absent: raced away since the walk
		}
		if len(candidateKeys(doc)) == 0 {
			continue // carries no EstateKey: out of scope, not listed
		}
		t.Status = gitStatus(f)
		if estate == nil {
			t.Kept = keptAll(doc)
			t.Reason = ReasonNotAdmitted
			out = append(out, t)
			continue
		}
		t.Keys, t.Kept = classifyKeys(estate, doc)
		if len(t.Keys) == 0 {
			t.Reason = ReasonNothingRedundant
		} else {
			t.Eligible = true
			t.Reason = ReasonRedundant
		}
		out = append(out, t)
	}
	return out, nil
}

// keptAll is every candidate key of doc, kept, with all its entry names.
func keptAll(doc map[string]any) []KeptKey {
	out := []KeptKey{}
	for _, k := range candidateKeys(doc) {
		out = append(out, KeptKey{Key: k, NotInEstate: entryNames(doc[k])})
	}
	return out
}

// Apply removes each eligible target's Keys from its file. It refuses the
// whole run — writing nothing — when an eligible target is TRACKED and
// opts.IncludeTracked is false. An ineligible target is never written. A file
// that no longer carries any of its keys is left alone and not reported, which
// is what makes a second run report zero changes.
func Apply(targets []Target, opts Options) (Result, error) {
	res := Result{DryRun: opts.DryRun, Changed: []Change{}}
	var eligible []Target
	var tracked []string
	for _, t := range targets {
		if !t.Eligible || len(t.Keys) == 0 {
			continue
		}
		eligible = append(eligible, t)
		if t.Status == StatusTracked {
			tracked = append(tracked, t.Path)
		}
	}
	if len(tracked) > 0 && !opts.IncludeTracked {
		return res, &TrackedError{Paths: tracked}
	}
	if opts.DryRun {
		for _, t := range eligible {
			res.Changed = append(res.Changed, Change{Path: t.Path, Keys: t.Keys})
		}
		return res, nil
	}
	q := opts.QuarantineDir
	if q == "" {
		now := opts.Now
		if now.IsZero() {
			now = time.Now()
		}
		var err error
		if q, err = DefaultQuarantineDir(now); err != nil {
			return res, err
		}
	}
	for _, t := range eligible {
		changed, backup, err := pruneFile(t.Path, t.Keys, q)
		if err != nil {
			return res, err
		}
		if changed {
			res.Changed = append(res.Changed, Change{Path: t.Path, Keys: t.Keys, Backup: backup})
		}
	}
	return res, nil
}

// IsTrackedRefusal reports whether err is Apply's tracked-file refusal.
func IsTrackedRefusal(err error) bool {
	var te *TrackedError
	return errors.As(err, &te)
}

// RenderTargets writes one line per target — verdict, path, what happens and
// why, git status LAST — so every line ends in TRACKED, untracked or NO-REPO.
// Only an eligible line carries the word "eligible". Names only: key names and,
// for a kept key, the entry names the estate lacks; never a value.
func RenderTargets(w io.Writer, targets []Target) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range targets {
		verdict, detail := "skip", t.Reason
		if t.Eligible {
			verdict = "eligible"
			detail = "remove " + strings.Join(t.Keys, ",")
		}
		if len(t.Kept) > 0 && t.Reason != ReasonEstateSource {
			detail += " (keep " + keptSummary(t.Kept) + ")"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", verdict, t.Path, detail, t.Status); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// keptSummary is "key[entry,entry] key[…]": the kept keys and the entry names
// the estate lacks.
func keptSummary(kept []KeptKey) string {
	parts := make([]string, 0, len(kept))
	for _, k := range kept {
		parts = append(parts, k.Key+"["+strings.Join(k.NotInEstate, ",")+"]")
	}
	return strings.Join(parts, " ")
}

// RenderResult writes the apply summary: one line per changed file naming its
// pre-image, then "<n> files changed".
func RenderResult(w io.Writer, r Result) error {
	for _, c := range r.Changed {
		line := fmt.Sprintf("changed %s: removed %s", c.Path, strings.Join(c.Keys, ","))
		if c.Backup != "" {
			line += "; pre-image " + c.Backup
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	verb := "changed"
	if r.DryRun {
		verb = "would change"
	}
	_, err := fmt.Fprintf(w, "%d files %s\n", len(r.Changed), verb)
	return err
}
