// Package accountprune is `swarmery account prune`: the only supported way to
// remove the settings keys an estate binding now supplies from the sub-repo
// files that still carry a hand-made copy.
//
// # What it removes, and what it never touches
//
// Only runsettings.EstateKeys (pluginConfigs, extraKnownMarketplaces), whole
// keys only, and only when runsettings.Redundant — the SUBSET rule — says every
// entry of the file's copy exists in the estate's with an identical value
// (keys.go). The doctor's settings-block detector reports Redundancies, this
// package's verdict, so it names exactly the copies Apply removes. permissions,
// enabledMcpjsonServers, enabledPlugins and the swarmery binding object are
// never candidates (neverRedundant).
//
// # Each file is judged against ITS OWN estate
//
// Every file is resolved from its own project directory (the one holding its
// .claude/) with claudeacct.Resolve — the resolution a session started there
// gets — and compared only with THAT resolution's admitted estate. A file
// that resolves to a different estate than the --path root (a nested
// sub-estate, admitted or not) is listed "other estate: <root>" and never
// written: pruning it against the outer estate would drop configuration its
// own estate does not supply.
//
// # Exclusions that keep it from eating a source, or writing elsewhere
//
//  1. EVERY ESTATE'S OWN TWO FILES — <root>/.claude/settings.json (the
//     --settings supply) and <root>/.claude/settings.local.json (the binding),
//     for the outer estate and every nested one — are matched by resolved path
//     (claudeacct.SameFile) and listed "estate source", never eligible.
//  2. A SYMLINKED PATH — any link between the estate root and the file (a
//     linked .claude directory, a linked parent) — is listed "symlinked path"
//     and never written: a rename in a linked directory lands in the link's
//     target, which may lie outside the estate and be shared with other
//     projects. Apply re-checks this at write time.
//  3. A TRACKED file (claudeacct.FileProvenance: tracked, or git cannot tell)
//     that is eligible makes Apply refuse the WHOLE run unless
//     Options.IncludeTracked.
//  4. An estate that is not ADMITTED delivers nothing, so nothing is redundant
//     with it.
//
// # Apply writes what the plan showed, or nothing
//
// Apply re-evaluates every eligible target on its CURRENT bytes before
// writing anything; a file whose verdict or content changed since the plan is
// skipped "changed since plan". Every pre-image path is checked before the
// first write, so the common aborts leave zero files changed; a failure
// mid-run returns the files already changed with their pre-images.
//
// It never reads a credential store and never prints a value: names of keys
// and entries only.
package accountprune

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// Reasons a Target carries. None of them contains the word the dry run's
// eligible lines are counted by.
const (
	ReasonRedundant         = "redundant with the estate"
	ReasonEstateSource      = "estate source"
	ReasonNothingRedundant  = "nothing redundant"
	ReasonNotAdmitted       = "estate not admitted"
	ReasonOtherEstatePrefix = "other estate: "
	ReasonSymlinked         = "symlinked path"
	ReasonChangedSincePlan  = "changed since plan"
	reasonUnreadablePrefix  = "unreadable: "
	noEstate                = "(none)"
)

// Target is one settings file Plan considered.
type Target struct {
	Path       string    `json:"path"`
	EstateRoot string    `json:"estateRoot"` // the estate the file itself resolves to
	Keys       []string  `json:"keys"`       // redundant EstateKeys Apply removes; [] never null
	Kept       []KeptKey `json:"kept"`       // EstateKeys that stay, with the entry names the estate lacks
	Status     GitStatus `json:"status"`
	Eligible   bool      `json:"eligible"`
	Reason     string    `json:"reason"`

	// digest is the SHA-256 of the bytes the verdict was made on; Apply writes
	// only a file whose current bytes still match. Never rendered.
	digest [sha256.Size]byte
	// entries is each of Keys' entry names in the file's copy — names only,
	// for Redundancies. Never rendered.
	entries map[string][]string
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

// Skip is an eligible target Apply left alone, and why.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Result is what Apply did.
type Result struct {
	DryRun  bool     `json:"dryRun"`
	Changed []Change `json:"changed"` // never null
	Skipped []Skip   `json:"skipped"` // never null
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
// plus every estate's own two files, each judged against the estate the file
// itself resolves to. Sorted by path, de-duplicated. A root that resolves to no
// estate is an error: with nothing supplying these keys, nothing is redundant.
func Plan(roots []string) ([]Target, error) {
	seen := map[string]bool{}
	out := []Target{}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", root, err)
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", abs)
		}
		outer := claudeacct.Resolve(abs).EstateRoot
		if outer == "" {
			return nil, fmt.Errorf("%s resolves to no estate: nothing supplies these keys, so nothing is redundant "+
				"(declare one with `swarmery account estate use <key> --path <root>`)", abs)
		}
		for _, f := range claudeacct.ScanSettingsFiles(abs) {
			if seen[f] {
				continue
			}
			if t, listed := evaluate(f, outer); listed {
				seen[f] = true
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Redundancy is one EstateKey Apply would remove from one file: the file's
// copy duplicates EstateFile's, entry for entry. Names only, never a value.
type Redundancy struct {
	EstateFile string   // <the file's estate root>/.claude/settings.json
	Path       string   // the settings file carrying the copy
	Key        string   // the EstateKey
	Entries    []string // the copy's entry names, sorted
}

// Redundancies is what Plan([]string{root}) marks eligible, one entry per key,
// sorted by path, keys in EstateKeys order — every exclusion evaluate applies,
// because it is evaluate's own verdict. It is the doctor's settings-block
// detector (internal/accountdoctor), so the doctor reports exactly what the
// prune removes. Read-only and git-free: no provenance probe (Status is the
// prune's concern), and Lock 1 answered through claudeacct.ResolveForDisplay —
// a report, never a write. A root that resolves to no estate supplies nothing:
// [].
func Redundancies(root string) []Redundancy {
	out := []Redundancy{}
	abs, err := filepath.Abs(root)
	if err != nil {
		return out
	}
	outer := claudeacct.ResolveForDisplay(abs).EstateRoot
	if outer == "" {
		return out
	}
	files := claudeacct.ScanSettingsFiles(abs)
	sort.Strings(files)
	for _, f := range files {
		t, _ := judge(f, outer, claudeacct.ResolveForDisplay)
		if !t.Eligible {
			continue
		}
		estateFile := filepath.Join(t.EstateRoot, filepath.FromSlash(claudeacct.ProjectSettingsFile))
		for _, k := range t.Keys {
			out = append(out, Redundancy{EstateFile: estateFile, Path: f, Key: k, Entries: t.entries[k]})
		}
	}
	return out
}

// evaluate judges one settings file for Plan and Apply: judge on a fresh Lock 1
// verdict (this path writes), plus the file's git status. outer is the estate
// root of the scan; listed is false for a file that carries no EstateKey (out
// of scope) or vanished since the walk.
func evaluate(f, outer string) (Target, bool) {
	t, listed := judge(f, outer, claudeacct.Resolve)
	if listed {
		t.Status = gitStatus(f)
	}
	return t, listed
}

// judge is every verdict evaluate reaches, with Status left unset: it runs no
// git of its own. resolve answers which estate the file's project directory
// belongs to.
func judge(f, outer string, resolve func(string) claudeacct.Resolution) (Target, bool) {
	res := resolve(filepath.Dir(filepath.Dir(f)))
	t := Target{Path: f, EstateRoot: res.EstateRoot, Keys: []string{}, Kept: []KeptKey{}}
	if isEstateFile(f, res.EstateRoot) || isEstateFile(f, outer) {
		t.Reason = ReasonEstateSource
		return t, true
	}
	doc, why := claudeacct.ReadTrustedSettings(f)
	if doc == nil {
		if why == "" {
			return t, false // raced away since the walk
		}
		t.Reason = reasonUnreadablePrefix + why
		return t, true
	}
	if len(candidateKeys(doc)) == 0 {
		return t, false // carries no EstateKey: not listed
	}
	t.Kept = keptAll(doc)
	switch {
	case res.EstateRoot == "":
		t.Reason = ReasonOtherEstatePrefix + noEstate
		return t, true
	case !claudeacct.SameFile(res.EstateRoot, outer):
		t.Reason = ReasonOtherEstatePrefix + res.EstateRoot
		return t, true
	case !unlinkedBelow(f, res.EstateRoot):
		t.Reason = ReasonSymlinked
		return t, true
	case !res.EstateAdmitted:
		t.Reason = ReasonNotAdmitted
		return t, true
	}
	raw, _, err := readForWrite(f)
	if err != nil {
		t.Reason = reasonUnreadablePrefix + err.Error()
		return t, true
	}
	estateFile := filepath.Join(res.EstateRoot, filepath.FromSlash(claudeacct.ProjectSettingsFile))
	estate, _ := claudeacct.ReadTrustedSettingsWithin(estateFile, res.EstateRoot)
	if estate == nil {
		t.Reason = ReasonNothingRedundant
		return t, true
	}
	t.digest = sha256.Sum256(raw)
	t.Keys, t.Kept = classifyKeys(estate, doc)
	if len(t.Keys) == 0 {
		t.Reason = ReasonNothingRedundant
	} else {
		t.Eligible = true
		t.Reason = ReasonRedundant
		t.entries = make(map[string][]string, len(t.Keys))
		for _, k := range t.Keys {
			t.entries[k] = entryNames(doc[k])
		}
	}
	return t, true
}

// isEstateFile reports whether f is one of root's own two files, by resolved
// path.
func isEstateFile(f, root string) bool {
	if root == "" {
		return false
	}
	for _, name := range []string{claudeacct.ProjectSettingsFile, claudeacct.BindingFile} {
		if claudeacct.SameFile(f, filepath.Join(root, filepath.FromSlash(name))) {
			return true
		}
	}
	return false
}

// unlinkedBelow reports whether path lies inside root with NO symlink on any
// component between the two: the fully resolved path must equal the resolved
// root joined with path's lexical remainder. Links above root (the operator's
// choice of where the estate lives) are allowed; one below it is not.
func unlinkedBelow(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rp, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return rp == filepath.Join(rr, rel) && claudeacct.WithinRoot(path, root)
}

// keptAll is every candidate key of doc, kept, with all its entry names.
func keptAll(doc map[string]any) []KeptKey {
	out := []KeptKey{}
	for _, k := range candidateKeys(doc) {
		out = append(out, KeptKey{Key: k, NotInEstate: entryNames(doc[k])})
	}
	return out
}

// job is one target Apply will write, with the bytes it was re-verified on.
type job struct {
	t      Target
	raw    []byte
	backup string
}

// Apply removes each eligible target's Keys from its file.
//
// Before anything is written it re-evaluates every eligible target on its
// current bytes (a changed verdict or changed bytes → skipped "changed since
// plan"), refuses the whole run when a remaining target is TRACKED and
// opts.IncludeTracked is false, and checks every pre-image path. Only then
// does it write, one file at a time; a failure mid-run returns the changes
// already made alongside the error. An ineligible target is never written.
func Apply(targets []Target, opts Options) (Result, error) {
	res := Result{DryRun: opts.DryRun, Changed: []Change{}, Skipped: []Skip{}}
	var jobs []job
	var tracked []string
	for _, t := range targets {
		if !t.Eligible || len(t.Keys) == 0 {
			continue
		}
		fresh, ok := evaluate(t.Path, t.EstateRoot)
		switch {
		case !ok:
			res.Skipped = append(res.Skipped, Skip{Path: t.Path, Reason: ReasonChangedSincePlan})
			continue
		case fresh.Reason == ReasonSymlinked:
			res.Skipped = append(res.Skipped, Skip{Path: t.Path, Reason: ReasonSymlinked})
			continue
		case !fresh.Eligible || fresh.EstateRoot != t.EstateRoot || !slices.Equal(fresh.Keys, t.Keys) ||
			(t.digest != [sha256.Size]byte{} && fresh.digest != t.digest):
			res.Skipped = append(res.Skipped, Skip{Path: t.Path, Reason: ReasonChangedSincePlan})
			continue
		}
		raw, _, err := readForWrite(t.Path)
		if err != nil || sha256.Sum256(raw) != fresh.digest {
			res.Skipped = append(res.Skipped, Skip{Path: t.Path, Reason: ReasonChangedSincePlan})
			continue
		}
		jobs = append(jobs, job{t: fresh, raw: raw})
		if fresh.Status == StatusTracked {
			tracked = append(tracked, t.Path)
		}
	}
	if len(tracked) > 0 && !opts.IncludeTracked {
		return res, &TrackedError{Paths: tracked}
	}
	if opts.DryRun {
		for _, j := range jobs {
			res.Changed = append(res.Changed, Change{Path: j.t.Path, Keys: j.t.Keys})
		}
		return res, nil
	}
	if len(jobs) == 0 {
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
	bdir := filepath.Join(q, "prune")
	for i := range jobs {
		jobs[i].backup = filepath.Join(bdir, backupName(jobs[i].t.Path))
	}
	if err := preflightBackups(bdir, jobs); err != nil {
		return res, err
	}
	if _, err := ensureQuarantine(q); err != nil {
		return res, err
	}
	for _, j := range jobs {
		changed, skip, err := pruneFile(j, unlinkedBelow)
		if err != nil {
			return res, err
		}
		switch {
		case skip != "":
			res.Skipped = append(res.Skipped, Skip{Path: j.t.Path, Reason: skip})
		case changed:
			res.Changed = append(res.Changed, Change{Path: j.t.Path, Keys: j.t.Keys, Backup: j.backup})
		}
	}
	return res, nil
}

// preflightBackups refuses, before any write, the two ways a pre-image could
// not be made: two jobs mapping to one backup file, and an existing backup
// whose bytes differ from the file as it is now (the earliest pre-image is the
// rollback artifact and nothing may replace it).
func preflightBackups(bdir string, jobs []job) error {
	owner := map[string]string{}
	for _, j := range jobs {
		bak := j.backup
		if bak == "" {
			bak = filepath.Join(bdir, backupName(j.t.Path))
		}
		if prev, dup := owner[bak]; dup {
			return fmt.Errorf("%s and %s would share the pre-image %s; nothing was written", prev, j.t.Path, bak)
		}
		owner[bak] = j.t.Path
		if existing, err := os.ReadFile(bak); err == nil && !bytes.Equal(existing, j.raw) {
			return fmt.Errorf("a different pre-image of %s already exists at %s; move it aside before pruning again. "+
				"Nothing was written", j.t.Path, bak)
		}
	}
	return nil
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
		if len(t.Kept) > 0 && (t.Eligible || t.Reason == ReasonNothingRedundant) {
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
// pre-image, one per skipped file with its reason, then "<n> files changed". A
// dry run writes the count alone — its target lines already say what would
// change. Call it on a PARTIAL result too: after a mid-run failure it is the
// only record of which files were already rewritten.
func RenderResult(w io.Writer, r Result) error {
	for _, c := range r.Changed {
		if r.DryRun {
			break
		}
		line := fmt.Sprintf("changed %s: removed %s", c.Path, strings.Join(c.Keys, ","))
		if c.Backup != "" {
			line += "; pre-image " + c.Backup
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	for _, s := range r.Skipped {
		if _, err := fmt.Fprintf(w, "skipped %s: %s\n", s.Path, s.Reason); err != nil {
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

// LaunchNotice is what a prune costs a session swarmery does not start. The
// estate's keys reach a session only as the composed --settings file that
// `account exec`, the shell function, the PATH shim and the daemon's spawns
// pass, so once a sub-repo's own copy is gone, a `claude` started there any
// other way runs without them.
const LaunchNotice = "note: a pruned file's keys then reach a session only through the estate's --settings, " +
	"which only a swarmery launch passes (account exec, the claude shell function, the PATH shim, daemon runs); " +
	"a claude started any other way in those directories — an IDE extension, the desktop app, `command claude` — runs without them"

// RenderNotice writes LaunchNotice when r changed — or, in a dry run, would
// change — at least one file, and nothing otherwise.
func RenderNotice(w io.Writer, r Result) error {
	if len(r.Changed) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(w, LaunchNotice)
	return err
}
