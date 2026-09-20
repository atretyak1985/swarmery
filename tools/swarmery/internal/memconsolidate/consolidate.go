// Package memconsolidate shrinks a project's ALWAYS-LOADED auto-memory index
// deterministically — no LLM, no summarisation, no merging.
//
// The harness loads `<auto-memory>/MEMORY.md` into every conversation and pulls
// the topic files it points at on demand. So the index is a standing context
// tax: every closed line in it is paid for on every turn, forever. Consolidation
// is the repayment — a closed entry's topic file moves to `memory/closed/`, its
// index line moves to `memory/closed/INDEX.md`, and the file gains a
// `status: closed` stamp. Moving the FILE (not just the line) is the load-bearing
// half: a file left in `memory/` stays a recall candidate even with no index line.
//
// Three deliberate limits, so this can never lose a memory:
//
//   - Classification is textual and conservative (IsClosed): a hook must carry a
//     closed marker AND no open marker. A line carrying both is an open tail and
//     stays.
//   - A closed entry whose file is still referenced by a `[[wiki-link]]` from any
//     OTHER memory file stays open (the dependency guard) — closing it would
//     dangle a link the open memory depends on.
//   - Apply never deletes, and refuses any destination it can observe: it
//     moves, the callers (API, CLI) take a verified backup of every touched file
//     first, and a move whose destination name already exists in
//     `memory/closed/` is REFUSED rather than written over. "Can observe" is
//     literal, not hedging: the guard is a check (pathExists) followed by a
//     separate write, so it is not an atomic no-clobber guarantee. Only a writer
//     racing into `closed/` between those two syscalls could beat it, and by
//     this package's own premise there is no such writer — nothing else writes
//     to `closed/`. That refusal is load-bearing, not theoretical: the harness
//     cannot see `closed/`, so it recreates a memory of a name it already
//     consolidated, and renaming the new file over the old one would destroy the
//     only copy of the old one — including in the backup, which is taken from
//     the paths the plan names.
//
// Duplicate detection beyond these exact markers is explicitly out of scope.
package memconsolidate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeproj"
)

// ── auto-memory root resolution ─────────────────────────────────────────────
//
// One implementation of "where does this project's auto-memory live", shared by
// the advisor rule, the API handler and the CLI, so they can never disagree:
// <claudeDir>/projects/<slug>/memory, slug = claudeproj.Slug(project path).
// internal/api/memory.go's kindAutoMemory root calls AutoMemoryDirIn rather
// than mirroring it, so there is one resolver and not two.
//
// The slug here is claudeproj.Slug, NOT ingest.SlugForPath. They agree for any
// dot-free path, which is how they drifted apart unnoticed; they disagree the
// moment a project lives under a dot-directory (`/Users/dev/.local/src/acme`),
// and there the ingest encoding names a directory Claude Code never created —
// so the stat fails, the advisor concludes the project has no auto-memory and
// the dashboard shows an empty root. See internal/claudeproj for which slug
// answers which question.

// claudeDir is the resolved ~/.claude root. SetClaudeDir overrides it (the
// daemon's --claude-dir, and tests).
var claudeDir = DefaultClaudeDir()

// DefaultClaudeDir is ~/.claude, or ".claude" when the home dir is unresolvable.
func DefaultClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

// SetClaudeDir points the auto-memory resolver at a claude dir. An empty
// argument keeps the current value.
func SetClaudeDir(dir string) {
	if dir != "" {
		claudeDir = dir
	}
}

// ClaudeDir is the currently resolved claude dir.
func ClaudeDir() string { return claudeDir }

// AutoMemoryDirIn is the pure resolver: the auto-memory dir of projectPath under
// an explicit claude dir. claudeproj.Slug cleans the path itself.
func AutoMemoryDirIn(claude, projectPath string) string {
	return filepath.Join(claude, "projects", claudeproj.Slug(projectPath), "memory")
}

// AutoMemoryDir resolves projectPath's auto-memory dir under the package claude dir.
func AutoMemoryDir(projectPath string) string { return AutoMemoryDirIn(claudeDir, projectPath) }

// indexName is the basename of the index — never itself a consolidation candidate.
const indexName = "MEMORY.md"

// IndexPath is the always-loaded index inside an auto-memory dir.
func IndexPath(dir string) string { return filepath.Join(dir, indexName) }

// ClosedDir is where consolidated topic files land.
func ClosedDir(dir string) string { return filepath.Join(dir, "closed") }

// closedIndexName is the basename of the closed/ ledger — like indexName, never
// itself a consolidation candidate.
const closedIndexName = "INDEX.md"

// ClosedIndexPath is the append-only ledger of consolidated index lines.
func ClosedIndexPath(dir string) string { return filepath.Join(ClosedDir(dir), closedIndexName) }

// ── index parsing ───────────────────────────────────────────────────────────

// indexLineRe matches one index entry: `- [Title](file.md) — hook`. The dash
// between link and hook is optional and may be an em dash, an en dash, `--` or
// `-` (the index is hand-written; all four shapes occur).
var indexLineRe = regexp.MustCompile(`^\s*[-*]\s+\[([^\]]*)\]\(([^)]*)\)\s*(?:\x{2014}|\x{2013}|--|-)?\s*(.*)$`)

// closedRe is the closed-marker set. Uppercase on purpose: prose "merged" inside
// a sentence is narration, `MERGED` is a status stamp.
var closedRe = regexp.MustCompile(`\b(DONE|MERGED|CLOSED|SHIPPED|RESOLVED|LIVE)\b`)

// openRe is the open-tail marker set — the veto that keeps a closed-stamped line
// in the index.
//
// DEVIATION (documented): the phase doc writes this set as a single
// `\b(OPEN|open:|tail\s*=|impl open|awaits|OPEN:)\b`. A TRAILING `\b` cannot
// hold after an alternative that ends in a non-word character, so under that
// literal form `open:`, `OPEN:` and `tail\s*=` are dead branches: in
// "tail = deploy" the `=` is followed by a space, which is not a word boundary,
// so the alternative never matches. That silently mis-closes every "…SHIPPED…;
// tail = …" line — exactly the shape the doc's own prose says must stay open
// ("Lines with both markers stay (they carry an open tail)"). Each alternative
// below is anchored only where an anchor can hold.
//
// Three corrections against real index lines, each measured on the operator's
// live index before being made:
//
//   - `tail` introduces its remainder with `:` as often as with `=`
//     ("…5 phases SHIPPED; 2026-07-31 tail: run process lifecycle…"), so the
//     separator class is `[=:]`. One live line changes verdict, correctly.
//   - openTailPhraseRe below catches a COUNTED open tail written without any
//     colon ("…134 usage guides SHIPPED…; 4 deferrals open."). A bare lowercase
//     `open` stays deliberately non-vetoing, so "…; no open tail." still closes
//     — the phrase rule needs a number and a noun in front of `open`, which
//     "no open tail" does not have. One live line changes verdict, correctly.
//   - the `\bOPEN:` alternative is dropped as dead weight: `\bOPEN\b` already
//     matches "OPEN:" (the `:` IS the trailing word boundary).
var openRe = regexp.MustCompile(`\bOPEN\b|\b[Oo]pen:|\btail\s*[=:]|\bimpl open\b|\bawaits\b`)

// openTailPhraseRe matches a counted open tail — "<N> <noun> open", as in
// "4 deferrals open" or "2 phases open". It requires a number AND a word before
// `open`, which is exactly what keeps the non-vetoing "no open tail" shape
// closing: bare lowercase `open` on its own is narration, a count in front of it
// is a work item.
//
// It is an ALLOWLIST of shapes observed in real indexes, NOT a general rule for
// "this hook has an open tail". Real variants it does not match, each of which
// therefore classifies CLOSED:
//
//   - "4 deferrals still open" — an adverb between the noun and `open`.
//   - "tail — deploy" — an em dash where openRe's separator class wants `=`/`:`.
//   - "follow-up open" — an open tail with no count in front of it.
//
// So an unmatched shape fails toward "closed", which is the less safe of the two
// directions. What bounds the cost is that closing destroys nothing: the topic
// file moves to `closed/` and its index line is preserved verbatim in
// `closed/INDEX.md`, so a wrong verdict costs a lookup, never a memory. Widening
// the pattern one observed shape at a time is safe; measure each widening
// against a live index first, the way the three corrections above were measured.
var openTailPhraseRe = regexp.MustCompile(`\b\d+\s+[A-Za-z][\w-]*\s+open\b`)

// wikiLinkRe matches a `[[memory-name]]` cross-reference inside a memory file.
var wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// Entry is one parsed line of the index.
type Entry struct {
	LineNo int    `json:"lineNo"` // 1-based line number in MEMORY.md
	Raw    string `json:"-"`      // the verbatim line (what Apply removes/re-files)
	Title  string `json:"title"`
	File   string `json:"file"` // link target as written, e.g. "user-role.md"
	Hook   string `json:"hook"` // the one-line hook after the dash
	Closed bool   `json:"closed"`
}

// IsClosed classifies an index hook: a closed marker AND no open marker.
func IsClosed(hook string) bool {
	if !closedRe.MatchString(hook) {
		return false
	}
	return !openRe.MatchString(hook) && !openTailPhraseRe.MatchString(hook)
}

// ParseIndex extracts the index entries from MEMORY.md content. Lines that are
// not `- [..](..)` entries (headings, prose, blanks) are ignored, never counted.
func ParseIndex(content string) []Entry {
	var out []Entry
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		m := indexLineRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		hook := strings.TrimSpace(m[3])
		out = append(out, Entry{
			LineNo: i + 1,
			Raw:    trimmed,
			Title:  strings.TrimSpace(m[1]),
			File:   strings.TrimSpace(m[2]),
			Hook:   hook,
			Closed: IsClosed(hook),
		})
	}
	return out
}

// ── inspection (the R10 grain) ──────────────────────────────────────────────

// Stats is the cheap, parse-only shape of an index: what R10 fires on.
type Stats struct {
	Dir         string  `json:"dir"`
	IndexPath   string  `json:"indexPath"`
	IndexBytes  int     `json:"indexBytes"`
	TotalLines  int     `json:"totalLines"`
	ClosedCount int     `json:"closedCount"`
	ClosedShare float64 `json:"closedShare"`
}

// Inspect reads the index of an auto-memory dir and counts it. A missing index
// returns an os.IsNotExist error, so callers can skip projects without one.
func Inspect(dir string) (Stats, error) {
	idx := IndexPath(dir)
	data, err := os.ReadFile(idx)
	if err != nil {
		return Stats{}, err
	}
	entries := ParseIndex(string(data))
	closed := 0
	for _, e := range entries {
		if e.Closed {
			closed++
		}
	}
	st := Stats{
		Dir:         dir,
		IndexPath:   idx,
		IndexBytes:  len(data),
		TotalLines:  len(entries),
		ClosedCount: closed,
	}
	if st.TotalLines > 0 {
		st.ClosedShare = float64(closed) / float64(st.TotalLines)
	}
	return st, nil
}

// HumanBytes renders an index size the way the R10 detail and the CLI print it.
func HumanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// ── the plan ────────────────────────────────────────────────────────────────

// Reasons a closed-stamped entry is nevertheless kept in the index.
const (
	ReasonClosedMarker = "closed marker, no open tail"
	ReasonLinked       = "kept: referenced by [[link]] from an open memory"
	// ReasonMissingFile covers every target that is not a plain REGULAR file at
	// that path — absent, a directory, or a symlink. regularFile Lstats rather
	// than Stats, so a symlink is held back even when its target exists; the
	// constant's name is historical, its text is not. The hold is right either
	// way: only a regular file can be consolidated without following a link.
	ReasonMissingFile = "kept: index line does not point at a plain file on disk"
	ReasonNotAFile    = "kept: link target is not a plain .md file in this directory"
	// ReasonClosedCollision is the data-safety hold. The harness cannot see
	// closed/, so it recreates a memory of a name already consolidated there;
	// moving the new one would rename it over the old one and destroy it.
	ReasonClosedCollision = "kept: a different memory of this name is already consolidated in closed/"
)

// Action is one line of the plan: an index entry and what happens to it.
type Action struct {
	Title  string `json:"title"`
	File   string `json:"file"`
	Hook   string `json:"hook"`
	LineNo int    `json:"lineNo"`
	Reason string `json:"reason"`
	raw    string // the verbatim index line, for Apply
}

// Plan is a dry-run: everything Apply would do, and nothing it did.
type Plan struct {
	Stats
	ClosedDir string   `json:"closedDir"`
	Move      []Action `json:"move"` // closed entries that will be consolidated
	Keep      []Action `json:"keep"` // closed-stamped entries held back, with the reason
}

// BuildPlan reads the auto-memory dir and computes the consolidation plan.
// It only ever READS — the dry-run contract of the API and the CLI rests on this.
func BuildPlan(dir string) (Plan, error) {
	st, err := Inspect(dir)
	if err != nil {
		return Plan{}, err
	}
	data, err := os.ReadFile(st.IndexPath)
	if err != nil {
		return Plan{}, err
	}
	entries := ParseIndex(string(data))
	closedDir := ClosedDir(dir)

	// The holds that do NOT depend on the link graph are decided first, because a
	// held-back entry stays in memory/ and stays in MEMORY.md — it is functionally
	// open, so its own [[links]] must count in the guard below. Computing the
	// candidate set from "every closed entry" instead (the shape this replaced)
	// silently skipped the links of every entry that was later held back.
	hold := map[string]string{}
	candidates := map[string]bool{}
	for _, e := range entries {
		if !e.Closed {
			continue
		}
		switch {
		case !plainMemoryFile(e.File):
			hold[e.File] = ReasonNotAFile
		case !regularFile(filepath.Join(dir, e.File)):
			hold[e.File] = ReasonMissingFile
		case pathExists(filepath.Join(closedDir, e.File)):
			hold[e.File] = ReasonClosedCollision
		default:
			candidates[e.File] = true
		}
	}

	linked, err := linkedFromOpenFixpoint(dir, candidates)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{Stats: st, ClosedDir: closedDir, Move: []Action{}, Keep: []Action{}}
	for _, e := range entries {
		if !e.Closed {
			continue
		}
		act := Action{Title: e.Title, File: e.File, Hook: e.Hook, LineNo: e.LineNo, raw: e.Raw}
		switch {
		case hold[e.File] != "":
			act.Reason = hold[e.File]
			plan.Keep = append(plan.Keep, act)
		case linked[memoryName(e.File)]:
			act.Reason = ReasonLinked
			plan.Keep = append(plan.Keep, act)
		default:
			act.Reason = ReasonClosedMarker
			plan.Move = append(plan.Move, act)
		}
	}
	return plan, nil
}

// linkedFromOpenFixpoint runs the dependency guard to a fixed point.
//
// One pass is not enough: holding an entry back makes it open, and an open
// entry's links hold OTHER candidates back in turn. A -> B where an open memory
// links A and A links B needs two passes to reach B. Each pass can only shrink
// the candidate set, so the loop terminates in at most len(candidates) passes;
// the returned set is the one computed from the surviving candidates, so it is
// consistent with the classification that follows.
func linkedFromOpenFixpoint(dir string, candidates map[string]bool) (map[string]bool, error) {
	live := make(map[string]bool, len(candidates))
	for name := range candidates {
		live[name] = true
	}
	for {
		linked, err := linkedFromOpen(dir, live)
		if err != nil {
			return nil, err
		}
		shrank := false
		for name := range live {
			if linked[memoryName(name)] {
				delete(live, name)
				shrank = true
			}
		}
		if !shrank {
			return linked, nil
		}
	}
}

// plainMemoryFile rejects link targets that are not a flat *.md sibling — an
// absolute path, a `../` walk, a subdirectory, or a URL. Those are never moved.
//
// Both index basenames are rejected by name too. MEMORY.md is the index itself.
// INDEX.md is the closed/ ledger, and it is rejected for a reason the collision
// guard cannot cover: on a first run there is no ledger yet, so nothing occupies
// closed/INDEX.md and the move is allowed — after which appendClosedIndex reads
// that just-moved memory as `prev` and appends the ledger heading and every
// consolidated line onto the memory's own body. Nothing is lost (the append
// preserves prev), but the file stops being either a clean memory or a clean
// ledger.
func plainMemoryFile(name string) bool {
	// EqualFold, not ==: APFS and NTFS are case-insensitive, so a memory named
	// index.md IS closed/INDEX.md to the filesystem and would be read back as the
	// ledger's `prev` on a first run — the same corruption the reserved names
	// exist to prevent, reached through a case variant.
	if name == "" || strings.EqualFold(name, indexName) || strings.EqualFold(name, closedIndexName) || !strings.HasSuffix(name, ".md") {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	return filepath.Base(name) == name
}

// regularFile reports whether path is a plain file. Lstat, NOT Stat: os.Stat
// follows symlinks, and a symlinked index target would then have its TARGET's
// content copied into closed/ while os.Remove deleted only the link — the real
// memory left behind, loaded forever, with a decoy filed as consolidated.
func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// pathExists reports whether anything at all occupies path — a file, a dir, a
// dangling symlink. Lstat for the same reason regularFile uses it: a destination
// that is a dangling symlink still makes os.Rename clobber something.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// memoryName is the `name:` slug a `[[link]]` addresses: the basename minus .md.
func memoryName(file string) string {
	return strings.ToLower(strings.TrimSuffix(filepath.Base(file), ".md"))
}

// linkedFromOpen collects every `[[name]]` target referenced from a memory file
// that is NOT itself a consolidation candidate — the dependency guard. Scanning
// non-indexed *.md files too is deliberate: a file with no index line is still
// loaded on demand, and its links are still live.
func linkedFromOpen(dir string, candidates map[string]bool) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	linked := map[string]bool{}
	for _, en := range entries {
		if en.IsDir() || !strings.HasSuffix(en.Name(), ".md") {
			continue
		}
		if en.Name() == indexName || candidates[en.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, en.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue // vanished mid-scan
			}
			return nil, err
		}
		for _, m := range wikiLinkRe.FindAllStringSubmatch(string(data), -1) {
			linked[memoryName(strings.TrimSpace(m[1]))] = true
		}
	}
	return linked, nil
}

// ── apply ───────────────────────────────────────────────────────────────────

// ApplyOptions injects the caller's clock and backup path. Backup is called once
// with EVERY file the apply will touch (the index plus each moved topic file)
// BEFORE the first write; a nil Backup means the caller accepts no backup.
type ApplyOptions struct {
	Now    time.Time
	Backup func(paths []string) (string, error)
}

// ApplyResult is what an apply actually did.
type ApplyResult struct {
	Moved     []string `json:"moved"` // basenames, in plan order
	ClosedDir string   `json:"closedDir"`
	BackupID  string   `json:"backupId,omitempty"`
	IndexPath string   `json:"indexPath"`
}

// Apply executes a plan: stamp + move each Move file into memory/closed/, append
// its index line to memory/closed/INDEX.md, then rewrite MEMORY.md without those
// lines. Files are moved before the index is rewritten, so an interruption
// leaves a visible dangling index line rather than an invisible orphan file.
func Apply(dir string, plan Plan, opts ApplyOptions) (ApplyResult, error) {
	res := ApplyResult{Moved: []string{}, ClosedDir: ClosedDir(dir), IndexPath: IndexPath(dir)}
	if len(plan.Move) == 0 {
		return res, nil
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	today := now.Format("2006-01-02")
	closedDir := ClosedDir(dir)

	// Refuse BEFORE the first write, and before the backup, so a colliding plan
	// costs nothing and changes nothing. BuildPlan already holds these back with
	// ReasonClosedCollision, so getting here means the plan is stale relative to
	// closed/ — which is exactly the window in which the damage would be silent.
	if collisions := applyCollisions(closedDir, plan.Move); len(collisions) > 0 {
		return res, fmt.Errorf(
			"memconsolidate: refusing to consolidate %d file(s) that would overwrite an already-consolidated memory in %s: %s"+
				" — nothing was written; re-run the dry run to rebuild the plan",
			len(collisions), closedDir, strings.Join(collisions, ", "))
	}

	if opts.Backup != nil {
		paths := []string{IndexPath(dir)}
		for _, a := range plan.Move {
			paths = append(paths, filepath.Join(dir, a.File))
			// Defence in depth against the window between the check above and the
			// write below: if a destination exists after all, its CURRENT bytes are
			// in the snapshot, so the old memory stays recoverable either way.
			if dst := filepath.Join(closedDir, a.File); pathExists(dst) {
				paths = append(paths, dst)
			}
		}
		id, err := opts.Backup(paths)
		if err != nil {
			return res, fmt.Errorf("memconsolidate: backup: %w", err)
		}
		res.BackupID = id
	}

	if err := os.MkdirAll(closedDir, 0o755); err != nil {
		return res, err
	}

	// lineNo -> the verbatim line the plan saw there. rewriteIndex re-reads
	// MEMORY.md and refuses to drop a line whose text no longer matches, so an
	// edit landing mid-apply can never delete an unrelated (open) entry.
	moved := map[int]string{}
	var ledger []string
	for _, a := range plan.Move {
		src := filepath.Join(dir, a.File)
		data, err := os.ReadFile(src)
		if err != nil {
			return res, fmt.Errorf("memconsolidate: read %s: %w", src, err)
		}
		dst := filepath.Join(closedDir, a.File)
		if pathExists(dst) {
			return res, fmt.Errorf(
				"memconsolidate: %s appeared in %s after the plan was checked — refusing to overwrite it",
				a.File, closedDir)
		}
		if err := atomicWrite(dst, stampClosed(data, today)); err != nil {
			return res, err
		}
		if err := os.Remove(src); err != nil {
			return res, fmt.Errorf("memconsolidate: remove %s: %w", src, err)
		}
		moved[a.LineNo] = a.raw
		ledger = append(ledger, a.raw)
		res.Moved = append(res.Moved, a.File)
	}

	if err := appendClosedIndex(dir, ledger, today); err != nil {
		return res, err
	}
	if err := rewriteIndex(dir, moved, res.Moved); err != nil {
		return res, err
	}
	return res, nil
}

// applyCollisions names every planned move whose destination is already taken —
// either by a file in closed/ (a previously consolidated memory of that name) or
// by an earlier move in the same plan (two index lines naming one file). Both
// would end as os.Rename over live content.
func applyCollisions(closedDir string, moves []Action) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range moves {
		switch {
		case seen[a.File]:
			out = append(out, a.File+" (named twice in this plan)")
		case pathExists(filepath.Join(closedDir, a.File)):
			out = append(out, a.File)
		}
		seen[a.File] = true
	}
	return out
}

// stampClosed adds `closed_at` and `status: closed` to a memory file's YAML
// frontmatter (creating the block when the file has none). Keys already present
// at the top level are left alone — re-consolidating is idempotent.
func stampClosed(content []byte, today string) []byte {
	text := string(content)
	stamp := func(body string) string {
		var b strings.Builder
		if !hasTopLevelKey(body, "closed_at") {
			b.WriteString("closed_at: " + today + "\n")
		}
		if !hasTopLevelKey(body, "status") {
			b.WriteString("status: closed\n")
		}
		return b.String()
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.TrimRight(lines[0], "\r") == "---" {
		for i := 1; i < len(lines); i++ {
			trimmed := strings.TrimRight(lines[i], "\r")
			if trimmed != "---" && trimmed != "..." {
				continue
			}
			extra := stamp(strings.Join(lines[1:i], "\n"))
			if extra == "" {
				return content
			}
			return []byte(strings.Join(lines[:i], "\n") + "\n" + extra + strings.Join(lines[i:], "\n"))
		}
		// Unterminated frontmatter — treat the file as having none.
	}
	return []byte("---\nclosed_at: " + today + "\nstatus: closed\n---\n\n" + text)
}

// hasTopLevelKey reports whether a frontmatter body already declares key at
// column 0 (an indented `status:` under `metadata:` is a different key).
func hasTopLevelKey(body, key string) bool {
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, key+":") {
			return true
		}
	}
	return false
}

// appendClosedIndex appends the consolidated lines to memory/closed/INDEX.md
// under a dated heading, creating the ledger on first use.
func appendClosedIndex(dir string, lines []string, today string) error {
	if len(lines) == 0 {
		return nil
	}
	path := ClosedIndexPath(dir)
	prev, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var b strings.Builder
	if len(prev) == 0 {
		b.WriteString("# Closed memories\n\n")
		b.WriteString("Consolidated out of `MEMORY.md` by `swarmery memory consolidate`. ")
		b.WriteString("The topic files live beside this ledger; nothing was deleted.\n")
	} else {
		b.Write(prev)
		if !strings.HasSuffix(string(prev), "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n## " + today + "\n\n")
	for _, ln := range lines {
		b.WriteString(ln + "\n")
	}
	return atomicWrite(path, []byte(b.String()))
}

// rewriteIndex writes MEMORY.md back without the consolidated lines, preserving
// every other byte (headings, prose, blank lines) exactly.
//
// movedLines maps a planned line NUMBER to the verbatim line the plan read
// there, and both halves are checked. The number alone is not a safe handle: the
// plan was built from one ReadFile, this is a second one, and the whole move
// loop (read, write, fsync, rename, remove per file) sits between them. The
// harness edits MEMORY.md routinely — including from the very session that
// triggered the apply — and a single inserted or deleted line shifts every
// ordinal, which under a number-only drop silently deletes the index line of an
// OPEN memory and leaves the consolidated one listed.
//
// So: any ordinal whose text no longer matches aborts the rewrite and MEMORY.md
// is left exactly as found. The topic files have already moved at this point, so
// the error spells out the FULL recovery (see recoveryNote), not just the backup.
func rewriteIndex(dir string, movedLines map[int]string, movedFiles []string) error {
	path := IndexPath(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	matched := 0
	for i, ln := range lines {
		want, planned := movedLines[i+1]
		if !planned {
			out = append(out, ln)
			continue
		}
		if got := strings.TrimRight(ln, "\r"); got != want {
			return fmt.Errorf(
				"memconsolidate: %s changed since the plan was built — line %d now reads %q, not the planned %q; "+
					"the index was NOT rewritten. %s",
				path, i+1, got, want, recoveryNote(dir, movedFiles))
		}
		matched++
	}
	if matched != len(movedLines) {
		return fmt.Errorf(
			"memconsolidate: %s changed since the plan was built — %d of %d consolidated lines are no longer at their planned line numbers; "+
				"the index was NOT rewritten. %s",
			path, len(movedLines)-matched, len(movedLines), recoveryNote(dir, movedFiles))
	}
	return atomicWrite(path, []byte(strings.Join(out, "\n")))
}

// recoveryNote spells out the whole manual recovery after a rewriteIndex abort.
//
// Restoring the backup alone is NOT enough, because the ledger is appended
// before the index is rewritten. At the moment of the abort the topic files are
// in closed/, closed/INDEX.md already lists them, and MEMORY.md still lists them
// too. A backup restore puts memory/<file>.md back but leaves closed/<file>.md
// where it is — so the next plan holds that entry with ReasonClosedCollision and
// keeps holding it until someone clears closed/ by hand. Naming the exact files
// is the difference between a two-minute fix and a stuck entry.
func recoveryNote(dir string, movedFiles []string) string {
	closedDir := ClosedDir(dir)
	if len(movedFiles) == 0 {
		return fmt.Sprintf("no topic file was moved; nothing to undo in %s — re-run the dry run", closedDir)
	}
	paths := make([]string, 0, len(movedFiles))
	for _, f := range movedFiles {
		paths = append(paths, filepath.Join(closedDir, f))
	}
	return fmt.Sprintf(
		"to recover, do BOTH: restore the backup, AND undo the %d already-moved topic file(s) — delete %s "+
			"and their lines from %s. Restoring the backup alone leaves those file(s) in %s, and every later plan "+
			"then holds the same entries with %q until %s is cleared by hand. Then re-run the dry run.",
		len(movedFiles), strings.Join(paths, ", "), ClosedIndexPath(dir), closedDir,
		ReasonClosedCollision, closedDir)
}

// atomicWrite writes via tmp-in-the-same-dir → fsync → rename, so a crash can
// never leave a half-written memory file. It mirrors internal/api/memory.go's
// atomicWriteFile; this package cannot import api (api imports this one).
func atomicWrite(path string, content []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".memconsolidate-*.tmp")
	if err != nil {
		return fmt.Errorf("memconsolidate: tmp in %s: %w", dir, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("memconsolidate: write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("memconsolidate: fsync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("memconsolidate: close %s: %w", name, err)
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return fmt.Errorf("memconsolidate: chmod %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("memconsolidate: rename %s -> %s: %w", name, path, err)
	}
	return nil
}

// FormatPlan renders a plan as the CLI's table (also used by the report).
func FormatPlan(p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "index %s, %d/%d lines closed (%.0f%%)\n",
		HumanBytes(p.IndexBytes), p.ClosedCount, p.TotalLines, p.ClosedShare*100)
	fmt.Fprintf(&b, "  %s\n\n", p.IndexPath)
	if len(p.Move) == 0 {
		b.WriteString("  nothing to consolidate\n")
	} else {
		fmt.Fprintf(&b, "  consolidate %d -> %s\n", len(p.Move), p.ClosedDir)
		for _, a := range p.Move {
			fmt.Fprintf(&b, "    %-46s %s\n", truncate(a.Title, 46), a.File)
		}
	}
	if len(p.Keep) > 0 {
		fmt.Fprintf(&b, "\n  held back %d (closed marker, but):\n", len(p.Keep))
		for _, a := range p.Keep {
			fmt.Fprintf(&b, "    %-46s %s\n", truncate(a.Title, 46), a.Reason)
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// SortActions orders actions by index line number (plan order is already that,
// but callers assembling actions from a map need it).
func SortActions(as []Action) {
	sort.Slice(as, func(i, j int) bool { return as[i].LineNo < as[j].LineNo })
}
