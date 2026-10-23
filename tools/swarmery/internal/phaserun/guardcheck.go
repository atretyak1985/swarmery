package phaserun

// The guard check (verify-not-trust phase 4): the main line of defence against a
// run that edits what its phase doc protects, independent of the agent AND of the
// phase-guard hook. The hook is the early, explained refusal inside the run; this
// is the after-the-fact audit of what the run actually left behind:
//
//   - the guard is read from the doc AS IT WAS LENT — never from the worktree copy,
//     and never from the workspace doc after the run's copy was returned over it
//     (ReturnPlanDoc copies a tampered copy back like any other edit);
//   - the lent copy is compared with that doc (DocCopyMismatch);
//   - `git diff --name-only <base> -- <protected globs>` plus the untracked files
//     under the same globs name every protected file the run changed;
//   - `.swarmery/guard.log` holds every call the hook refused.
//
// Nothing here fails the run: the result lands on the review record and the
// Review tab, and a non-empty ProtectedChanged or GuardBlocks raises a
// phase_decision (guard_decision.go) even when the run ended PHASE DONE (D25).

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// GuardLogRel is the hook's refusal record, relative to the worktree root
// (plugins/core/hooks/phase-guard.sh).
const GuardLogRel = ".swarmery/guard.log"

// guardLogMaxLines bounds how many guard.log lines are kept on the record; the
// rest are counted in GuardResult.GuardBlocksTotal.
const guardLogMaxLines = 200

// GuardLogLine is one refusal the hook logged:
// `<RFC3339 ts>\t<tool>\t<path-or-token>\t<reason>`. A line that does not have
// four tab-separated fields is kept with Malformed set and the raw text in Reason
// — counted, never dropped: a broken line is still an attempt.
type GuardLogLine struct {
	At        string `json:"at"`
	Tool      string `json:"tool"`
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	Malformed bool   `json:"malformed,omitempty"`
}

// GuardResult is what one guard check found. Its JSON is phase_reviews.guard_json
// and phase_decisions.guard_json (migration 0124), and the web's GuardSummary.
type GuardResult struct {
	// HasGuard: the doc carries a parseable `kind: guard` block.
	HasGuard       bool     `json:"hasGuard"`
	ProtectedPaths []string `json:"protectedPaths"`
	Allow          []string `json:"allow"`
	// ProtectedChanged are the worktree-relative files under a protected glob
	// (and under no allow entry) that differ from the run's base — committed,
	// uncommitted or untracked.
	ProtectedChanged []string `json:"protectedChanged"`
	// DocCopyMismatch: the lent copy differs from the doc as lent in more than
	// the parts an executor is told to edit, or it is missing while the doc
	// carries a guard block (DocCopyMissing).
	DocCopyMismatch bool `json:"docCopyMismatch"`
	DocCopyMissing  bool `json:"docCopyMissing,omitempty"`
	// GuardBlocks are guard.log's lines, the first guardLogMaxLines of
	// GuardBlocksTotal; MalformedLogLines of them did not parse.
	GuardBlocks       []GuardLogLine `json:"guardBlocks"`
	GuardBlocksTotal  int            `json:"guardBlocksTotal"`
	MalformedLogLines int            `json:"malformedLogLines"`
	// ParseError is an unparsable guard block (an absent one is no error). The
	// hook already blocked every call on it; here it is reported, not fatal.
	ParseError *mdfence.ParseError `json:"parseError"`
	// Base is the commit the diff ran from; DiffError why it could not run.
	Base      string `json:"base"`
	DiffError string `json:"diffError,omitempty"`
}

// Flagged reports whether the run touched, or tried to touch, protected paths —
// the condition that raises a phase_decision.
func (r GuardResult) Flagged() bool {
	return len(r.ProtectedChanged) > 0 || len(r.GuardBlocks) > 0
}

// HasFindings reports whether the Review tab shows a guard badge at all.
func (r GuardResult) HasFindings() bool {
	return r.Flagged() || r.DocCopyMismatch || r.ParseError != nil
}

// Paths is every path the decision is about: the changed protected files, then
// the paths the hook refused (the "-" of a refusal without one is skipped), in
// first-seen order.
func (r GuardResult) Paths() []string {
	return uniquePaths(r.ProtectedChanged, r.refusedPaths())
}

// refusedPaths are the paths of guard.log's well-formed lines, the guard's own
// inputs (.swarmery/…) left out: allowing those is never on offer.
func (r GuardResult) refusedPaths() []string {
	var out []string
	for _, l := range r.GuardBlocks {
		if !l.Malformed && !strings.HasPrefix(l.Path, ".swarmery") {
			out = append(out, l.Path)
		}
	}
	return uniquePaths(out)
}

// uniquePaths concatenates lists, dropping blanks, "-" and repeats.
func uniquePaths(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range lists {
		for _, p := range list {
			if p = strings.TrimSpace(p); p != "" && p != "-" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// Summary is the guard check in the reviewer's terms, "" when there is nothing to
// say. reviewPrompt puts it first, so the reviewer reads it before the doc.
func (r GuardResult) Summary() string {
	var b strings.Builder
	if len(r.ProtectedChanged) > 0 {
		b.WriteString("The run changed protected paths: " + strings.Join(r.ProtectedChanged, ", ") + ".\n")
	}
	if r.GuardBlocksTotal > 0 {
		b.WriteString("The phase guard refused " + plural(r.GuardBlocksTotal, "attempt") + " to touch protected paths")
		if ps := r.refusedPaths(); len(ps) > 0 {
			b.WriteString(" (" + strings.Join(ps, ", ") + ")")
		}
		b.WriteString(".\n")
	}
	if r.DocCopyMissing {
		b.WriteString("The phase doc lent into the worktree is missing after the run.\n")
	} else if r.DocCopyMismatch {
		b.WriteString("The phase doc lent into the worktree was changed outside its checkboxes, Status, Forecast and Completion Report.\n")
	}
	if r.ParseError != nil {
		b.WriteString("The phase doc's guard block is unreadable: " + r.ParseError.Error() + ".\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "GUARD CHECK (independent of the run's own report — verify each item against the diff):\n" + b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// GuardInput is what a guard check reads.
type GuardInput struct {
	// DocPath is the canonical phase doc (epic_phases.doc_path, the workspace).
	DocPath string
	// LentDoc is the doc's text as it was lent into the worktree at Start; "" when
	// it is unknown (an adopted run), and the workspace doc is read instead.
	LentDoc string
	// Worktree is the run's worktree; Base the commit the run started from.
	Worktree string
	Base     string
}

// GuardCheck runs the guard check over one finished run. It never fails: what it
// cannot establish is recorded on the result (DiffError, ParseError).
func GuardCheck(g worktree.Git, in GuardInput) GuardResult {
	res := GuardResult{ProtectedPaths: []string{}, Allow: []string{}, ProtectedChanged: []string{}, GuardBlocks: []GuardLogLine{}}
	if in.Worktree == "" {
		return res
	}
	canonical := in.LentDoc
	if canonical == "" && in.DocPath != "" {
		if b, err := os.ReadFile(in.DocPath); err == nil {
			canonical = string(b)
		} else {
			res.ParseError = &mdfence.ParseError{Kind: mdfence.ErrUnparsable, Reason: "the phase doc is unreadable: " + err.Error()}
		}
	}

	guard, perr := mdfence.ParseGuard(canonical)
	switch {
	case perr == nil:
		res.HasGuard = true
		res.ProtectedPaths = append(res.ProtectedPaths, guard.ProtectedPaths...)
		res.Allow = append(res.Allow, guard.Allow...)
	case perr.Kind == mdfence.ErrUnparsable && res.ParseError == nil:
		res.ParseError = perr
	}

	if in.DocPath != "" && canonical != "" {
		res.DocCopyMismatch, res.DocCopyMissing = docCopyMismatch(in.Worktree, in.DocPath, canonical, res.HasGuard || res.ParseError != nil)
	}
	res.GuardBlocks, res.GuardBlocksTotal, res.MalformedLogLines = readGuardLog(filepath.Join(in.Worktree, GuardLogRel))
	if len(res.ProtectedPaths) > 0 {
		res.Base = strings.TrimSpace(in.Base)
		res.ProtectedChanged, res.DiffError = protectedChanges(g, in.Worktree, res.Base, res.ProtectedPaths, res.Allow)
	}
	return res
}

// docCopyMismatch compares the worktree's lent copy with the doc as lent. Equal
// bytes are the fast path; otherwise the two are compared without the parts the
// contract tells the executor to edit — checkbox ticks, the Status line, the
// Forecast and Completion Report sections — so a run that ticked its criteria is
// not a mismatch and one that rewrote its guard block, a criterion or the goal is.
// A missing copy is a mismatch only when the doc has a guard to tamper with.
func docCopyMismatch(wt, docPath, canonical string, guarded bool) (mismatch, missing bool) {
	copyHash, err := worktree.DocCopyHash(wt, docPath)
	if err != nil {
		return guarded, guarded
	}
	if copyHash == sha256Hex(canonical) {
		return false, false
	}
	b, err := os.ReadFile(filepath.Join(wt, worktree.LentPlanDocRel(docPath)))
	if err != nil {
		return guarded, guarded
	}
	return executorInvariant(string(b)) != executorInvariant(canonical), false
}

var (
	tickRe   = regexp.MustCompile(`(?m)^(\s*[-*+]\s+)\[[xX ]\]`)
	statusRe = regexp.MustCompile(`(?m)^\*{0,2}Status:?\*{0,2}:?.*$`)
)

// executorInvariant is the doc without what an executor legitimately edits.
func executorInvariant(doc string) string {
	body := stableDocBody(doc)
	body = tickRe.ReplaceAllString(body, "$1[ ]")
	return statusRe.ReplaceAllString(body, "Status:")
}

// readGuardLog parses guard.log leniently: tab-separated, four fields. A line
// with fewer or more fields is kept as Malformed with its raw text.
func readGuardLog(path string) (lines []GuardLogLine, total, malformed int) {
	lines = []GuardLogLine{}
	f, err := os.Open(path)
	if err != nil {
		return lines, 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		raw := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		total++
		l := GuardLogLine{}
		if parts := strings.Split(raw, "\t"); len(parts) == 4 {
			l.At, l.Tool, l.Path, l.Reason = parts[0], parts[1], parts[2], parts[3]
		} else {
			l.Malformed, l.Reason = true, raw
			malformed++
		}
		if len(lines) < guardLogMaxLines {
			lines = append(lines, l)
		}
	}
	return lines, total, malformed
}

// protectedChanges lists the files under a protected glob that the worktree holds
// differently from base: `git diff --name-only <base>` (committed and uncommitted
// tracked changes, deletions included) and `git ls-files --others` (new untracked
// files), both restricted to the globs and with the allow entries excluded. The
// globs are matched the way the hook matches them: `**` across directories and
// zero of them, a directory glob as everything under it, letter case ignored.
func protectedChanges(g worktree.Git, wt, base string, globs, allow []string) ([]string, string) {
	if base == "" {
		return []string{}, "the run recorded no start point, so there is no base to diff against"
	}
	spec := guardPathspecs(globs, allow)
	diff, err := g.Run(wt, append([]string{"-c", "core.quotepath=off", "diff", "--name-only", "--no-renames", base, "--"}, spec...)...)
	if err != nil {
		return []string{}, "git diff --name-only " + base + ": " + err.Error()
	}
	others, err := g.Run(wt, append([]string{"-c", "core.quotepath=off", "ls-files", "--others", "--exclude-standard", "--"}, spec...)...)
	if err != nil {
		return []string{}, "git ls-files --others: " + err.Error()
	}
	seen := map[string]bool{}
	out := []string{}
	for _, l := range strings.Split(diff+"\n"+others, "\n") {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out, ""
}

// guardPathspecs renders protected globs and allow entries as git pathspecs.
func guardPathspecs(globs, allow []string) []string {
	spec := make([]string, 0, len(globs)+len(allow))
	for _, gl := range globs {
		spec = append(spec, ":(glob,icase)"+gitGlob(gl))
	}
	for _, a := range allow {
		spec = append(spec, ":(glob,icase,exclude)"+gitGlob(a))
	}
	return spec
}

// gitGlob is a protected glob in git's `:(glob)` dialect: a directory glob
// (`dir/`, `dir/**`) protects everything under it, as the hook reads it.
func gitGlob(g string) string {
	g = strings.TrimPrefix(strings.TrimSpace(g), "./")
	if strings.HasSuffix(g, "/") {
		return g + "**"
	}
	return g
}

// guardGlobMatch reports whether the repo-relative path p falls under the
// protected glob — the matching protectedChanges asks git for, done in Go for a
// path that need not exist: the glob in gitGlob's spelling, read in git's
// `:(glob,icase)` dialect. `*`, `?` and `[…]` stay inside one path segment, a
// `**` segment matches zero or more whole segments (a trailing one at least
// one: `dir/**` is everything under dir), and letter case is ignored. A
// malformed glob matches nothing.
func guardGlobMatch(glob, p string) bool {
	g := strings.ToLower(gitGlob(glob))
	if g == "" || p == "" {
		return false
	}
	return globSegsMatch(strings.Split(g, "/"), strings.Split(strings.ToLower(p), "/"))
}

func globSegsMatch(g, p []string) bool {
	for len(g) > 0 {
		if g[0] == "**" {
			if len(g) == 1 {
				return len(p) > 0
			}
			for i := 0; i <= len(p); i++ {
				if globSegsMatch(g[1:], p[i:]) {
					return true
				}
			}
			return false
		}
		if len(p) == 0 {
			return false
		}
		if ok, err := path.Match(g[0], p[0]); err != nil || !ok {
			return false
		}
		g, p = g[1:], p[1:]
	}
	return len(p) == 0
}

// guardJSON is r as stored in a guard_json column.
func guardJSON(r GuardResult) string {
	b, err := json.Marshal(r)
	if err != nil {
		return "{}"
	}
	return string(b)
}
