// Package memlint checks a project's auto-memory against reality — the one
// reality it can reach offline, which is the project's own git history.
//
// An auto-memory note is written once and then trusted forever: "PR #366
// UNMERGED (needs a review approval)" stays in MEMORY.md long after the merge
// commit landed, and every later session plans around a fact that stopped being
// true. Nothing re-reads a remembered fact against the world, so the drift is
// invisible until an agent acts on it.
//
// This package closes exactly that gap for the one fact class that is cheap to
// verify deterministically: a clause that names a pull request AND says it is
// still open (a *claim*) is checked against `git log --all` for the two shapes
// a merge leaves behind in this repo — a merge commit (`Merge pull request
// #366 from …`) and a squash commit (`fix: thing (#224)`). A claim whose merge
// is in the history is a *finding*.
//
// Three deliberate limits:
//
//   - No LLM, no network, no `gh`. The daemon lints while offline and without
//     an account; the only external call is one `git log` per distinct PR per
//     directory, bounded by --max-count=1 and cached within a LintDir pass.
//   - Per-clause and textual. A memory line is a run of clauses — split at
//     `;`, `, `, `. `, a spaced dash or a table bar — and a clause is a claim
//     only when it carries an open marker and NO closed marker, so "MERGED
//     2026-08-24 (#260, #261); OPEN: flip the guard" claims nothing while
//     "PR #340 … MERGED; #341 open" claims #341 alone. The first cut of this
//     rule was per LINE and flagged 45 of 46 claims in this repo's own
//     memory, nearly all of them lines whose open marker was about something
//     else. A PR ref is `#<n>` at a word start, so `#12` inside a URL fragment
//     (`…/issues/12#issuecomment-1`) is not a ref: letters follow the `#`.
//   - It never edits a memory file. The output is a report; the correction is
//     the operator's (or a later, human-gated step's).
//
// The directory it reads is memconsolidate.AutoMemoryDirIn — the same resolver
// R10, the Memory page and `swarmery memory consolidate` share, so the four
// surfaces can never disagree about which directory they are talking about.
package memlint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
)

// Claim is one line of one memory file that names a PR and says it is open.
type Claim struct {
	File   string // basename of the memory file
	Line   string // the line, trimmed
	LineNo int    // 1-based
	PR     int
}

// Finding is a Claim whose merge is already in the project's git history.
type Finding struct {
	File   string `json:"file"`
	LineNo int    `json:"lineNo"`
	PR     int    `json:"pr"`
	// Claim is the line, trimmed and cut to ClaimMaxRunes.
	Claim string `json:"claim"`
	// MergeSHA is the full hash of the commit that carries the merge; MergedAt
	// is its committer date, RFC3339 (git's %cI).
	MergeSHA string `json:"mergeSha"`
	MergedAt string `json:"mergedAt"`
}

// Report is the result of linting one auto-memory directory.
type Report struct {
	Dir      string    `json:"dir"`
	Project  string    `json:"project"`
	Files    int       `json:"files"`
	Claims   int       `json:"claims"`
	Findings []Finding `json:"findings"`
}

// ClaimMaxRunes bounds Finding.Claim so a wide index line does not dominate a
// recommendation's evidence blob.
const ClaimMaxRunes = 200

// gitTimeout bounds one `git log`; a wedged repository must not stall the
// advisor pass or an API request indefinitely.
const gitTimeout = 30 * time.Second

var (
	// prRef: `#<n>` at clause start or after whitespace / `(` / `[`, followed
	// by a non-digit. The leading delimiter keeps `pull/12#r5` and
	// `issues/12#issuecomment-1` out; the trailing \b stops `#36` matching
	// inside `#366`.
	prRef = regexp.MustCompile(`(?:^|[\s(\[])#(\d{1,6})\b`)
	// openMark is the vocabulary of "still open". Case-sensitive on purpose:
	// `Open` as a title word is not a status.
	openMark = regexp.MustCompile(`\b(OPEN|open|UNMERGED|unmerged|needs? (?:a )?(?:review|merge|approval)|awaits|pending|not merged)\b`)
	// closedMark is the vocabulary of "finished"; \b keeps UNMERGED out of MERGED.
	closedMark = regexp.MustCompile(`\b(MERGED|merged|CLOSED|closed|DONE|SHIPPED|RESOLVED)\b`)
	// clauseSplit cuts a line into the clauses markers are scoped to: a
	// semicolon, a comma or full stop followed by a space, a spaced em/en
	// dash, or a table bar. Parentheses are NOT cuts, so "(#215–#218 merged,
	// #219 open)" still splits at the comma into a closed and an open clause.
	clauseSplit = regexp.MustCompile(`;|, |\. | — | – | \| `)
)

// ParseClaims extracts the open-PR claims of one file's content. A line is
// cut into clauses; a clause yields its PR refs as claims when it carries an
// open marker and no closed marker. The same PR is one claim per line however
// often the line repeats it.
func ParseClaims(file string, content string) []Claim {
	var out []Claim
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !openMark.MatchString(line) || !prRef.MatchString(line) {
			continue
		}
		seen := map[int]bool{}
		for _, clause := range clauseSplit.Split(line, -1) {
			if !openMark.MatchString(clause) || closedMark.MatchString(clause) {
				continue
			}
			for _, m := range prRef.FindAllStringSubmatchIndex(clause, -1) {
				n, err := strconv.Atoi(clause[m[2]:m[3]])
				if err != nil || n <= 0 || seen[n] {
					continue
				}
				seen[n] = true
				out = append(out, Claim{File: file, Line: line, LineNo: i + 1, PR: n})
			}
		}
	}
	return out
}

// MergedIn reports whether repo's history (all refs) carries the merge of PR
// n, in either shape GitHub leaves behind: a merge commit whose subject is
// `Merge pull request #n from …`, or a squash commit whose subject ends in
// `(#n)`. The two --grep patterns are OR'd and matched as fixed strings; the
// trailing space in the first and the parentheses in the second stop `#36`
// matching inside `#366`.
//
// ok is false on ANY failure — no .git, git missing, a timeout, a non-zero
// exit — and never an error to the caller: an unverifiable claim is simply not
// a finding. The repo path is only ever passed to -C, never into a flag.
func MergedIn(repo string, pr int) (sha, at string, ok bool) {
	if pr <= 0 || repo == "" {
		return "", "", false
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "log", "--all", "--max-count=1",
		"--format=%H%x09%cI", "--fixed-strings",
		fmt.Sprintf("--grep=Merge pull request #%d ", pr),
		fmt.Sprintf("--grep=(#%d)", pr))
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	line := bytes.TrimSpace(out)
	if len(line) == 0 {
		return "", "", false
	}
	parts := strings.SplitN(string(line), "\t", 2)
	if len(parts) != 2 || len(parts[0]) < 7 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// LintDir lints every *.md directly under dir (subdirectories, closed/ among
// them, are not descended) against the git history at repo. A file that cannot
// be read is skipped; a missing dir is returned as the os error (IsNotExist),
// so callers can tell "no memory" from "a broken one". A repo without .git
// yields the claims and zero findings.
func LintDir(dir, repo string) (Report, error) {
	rep := Report{Dir: dir, Project: repo, Findings: []Finding{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return rep, err
	}
	type key struct {
		repo string
		pr   int
	}
	type merged struct {
		sha, at string
		ok      bool
	}
	cache := map[key]merged{}
	lookup := func(pr int) merged {
		k := key{repo, pr}
		if m, hit := cache[k]; hit {
			return m
		}
		sha, at, ok := MergedIn(repo, pr)
		m := merged{sha, at, ok}
		cache[k] = m
		return m
	}
	for _, en := range entries {
		if en.IsDir() || !strings.HasSuffix(en.Name(), ".md") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, en.Name()))
		if rerr != nil {
			continue
		}
		rep.Files++
		for _, c := range ParseClaims(en.Name(), string(data)) {
			rep.Claims++
			m := lookup(c.PR)
			if !m.ok {
				continue
			}
			rep.Findings = append(rep.Findings, Finding{
				File:     c.File,
				LineNo:   c.LineNo,
				PR:       c.PR,
				Claim:    truncateRunes(c.Line, ClaimMaxRunes),
				MergeSHA: m.sha,
				MergedAt: m.at,
			})
		}
	}
	return rep, nil
}

// Lint lints projectPath's auto-memory directory under claudeDir against the
// git history at projectPath itself.
func Lint(projectPath, claudeDir string) (Report, error) {
	return LintDir(memconsolidate.AutoMemoryDirIn(claudeDir, projectPath), projectPath)
}

// MergedDate is the calendar day of MergedAt ("2026-09-22"), or the raw value
// when it is not RFC3339-shaped.
func (f Finding) MergedDate() string {
	if len(f.MergedAt) >= 10 && f.MergedAt[4] == '-' && f.MergedAt[7] == '-' {
		return f.MergedAt[:10]
	}
	return f.MergedAt
}

// ShortSHA is the first seven characters of MergeSHA.
func (f Finding) ShortSHA() string {
	if len(f.MergeSHA) > 7 {
		return f.MergeSHA[:7]
	}
	return f.MergeSHA
}

// Describe is the one-line human sentence every surface prints:
// "<file>:<line> says PR #<n> is open; it merged <date> (<sha7>)".
func (f Finding) Describe() string {
	return fmt.Sprintf("%s:%d says PR #%d is open; it merged %s (%s)",
		f.File, f.LineNo, f.PR, f.MergedDate(), f.ShortSHA())
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
