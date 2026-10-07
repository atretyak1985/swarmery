package memlint

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint/memlinttest"
)

const fixtureDir = "testdata/memory"

// TestMemlintFixturesMatchSharedLines guards the committed testdata against
// drifting from memlinttest.Lines, which the advisor and api tests write.
func TestMemlintFixturesMatchSharedLines(t *testing.T) {
	for name, want := range memlinttest.Lines {
		got, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s differs from memlinttest.Lines[%q]:\n got: %q\nwant: %q", name, name, got, want)
		}
	}
}

func claimPRs(cs []Claim) []int {
	out := make([]int, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.PR)
	}
	sort.Ints(out)
	return out
}

func TestMemlintParseClaims(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []int
	}{
		{"merge shape", "daemon installed, but PR #366 UNMERGED (needs a review approval); aliases-only", []int{366}},
		{"squash shape", "PR #224 open 2026-08-11: tech-lead xhigh", []int{224}},
		{"open colon", "PR #212 open: settings.local.json as implicit overlay", []int{212}},
		{"mixed line keeps only the open half", "PR #340 … MERGED; #341 open", []int{341}},
		{"closed line is not a claim", "#340 + 7 PRs MERGED 2026-09-18; #341/#342 MERGED", nil},
		{"url fragment is not a ref", "see https://github.com/x/y/issues/12#issuecomment-1, still open", nil},
		{"open marker without a ref", "impl open; plan approved", nil},
		{"ref without an open marker", "shipped in #499 yesterday", nil},
		{"needs a merge", "#120 needs a merge before Friday", []int{120}},
		{"awaits", "(#7) awaits the operator", []int{7}},
		{"Open as a title word is not a status", "Open Source Summit talk, #88", nil},
		{"two open refs", "#10 open and #11 pending", []int{10, 11}},
		// Real lines from this repo's index that the per-LINE rule flagged.
		{"closed clause, open elsewhere", "ALL 7 phases DONE + archived by 2026-09-23 (#348 → 665284e, core 3.5.0; 0074 skill scope); no open tail.", nil},
		{"OPEN clause carries no ref", "MERGED + deployed 2026-08-24 (#260, #261), core 2.17.0; OPEN: flip the Bash guard to enforce 2026-08-31", nil},
		{"range merged, last one open", "SHIPPED 2026-08-11 (#215–#218 merged, #219 open); tail = live amnesty", []int{219}},
		{"deferrals open is not about the PR", "134 usage guides SHIPPED into dev 2026-08-06 (PR #187); `dev` created that day; 4 deferrals open.", nil},
		{"sentence split", "Everything left after #227 shipped in one commit. The light-mode check is still open.", nil},
		{"em dash split", "PR #65 merged — eval runs pending", nil},
		{"table bar split", "| #481 | merged | #482 open |", []int{482}},
		{"same PR twice on a line is one claim", "PR #481 OPEN (worktree p3); #481 awaits review", []int{481}},
		{"clause with both markers claims nothing", "merged #227 but #228 open", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claimPRs(ParseClaims("f.md", tc.line))
			if len(got) != len(tc.want) {
				t.Fatalf("claims = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("claims = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestMemlintParseClaimsLineNumbersAndTrim(t *testing.T) {
	cs := ParseClaims("f.md", "# title\n\n   PR #5 open   \nnothing\n")
	if len(cs) != 1 || cs[0].LineNo != 3 || cs[0].Line != "PR #5 open" || cs[0].File != "f.md" {
		t.Fatalf("claims = %+v", cs)
	}
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestMemlintMergedIn(t *testing.T) {
	repo := memlinttest.Repo(t)

	sha, at, ok := MergedIn(repo, 366)
	if !ok || !shaRe.MatchString(sha) {
		t.Fatalf("MergedIn(366) = %q %q %v, want a merge-commit hit", sha, at, ok)
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Fatalf("MergedAt %q is not RFC3339: %v", at, err)
	}
	if _, _, ok := MergedIn(repo, 224); !ok {
		t.Fatal("MergedIn(224): squash shape `(#224)` must match")
	}
	if _, _, ok := MergedIn(repo, 212); ok {
		t.Fatal("MergedIn(212): nothing in the history mentions it")
	}
	// The trailing delimiter in both patterns keeps #36 and #22 out of #366 and #224.
	if _, _, ok := MergedIn(repo, 36); ok {
		t.Fatal("MergedIn(36) matched inside #366")
	}
	if _, _, ok := MergedIn(repo, 22); ok {
		t.Fatal("MergedIn(22) matched inside (#224)")
	}
	if _, _, ok := MergedIn(t.TempDir(), 366); ok {
		t.Fatal("a directory without .git must never verify a merge")
	}
	if _, _, ok := MergedIn(repo, 0); ok {
		t.Fatal("PR 0 is not a ref")
	}
}

func TestMemlintLintDirFixture(t *testing.T) {
	repo := memlinttest.Repo(t)
	rep, err := LintDir(fixtureDir, repo)
	if err != nil {
		t.Fatalf("LintDir: %v", err)
	}
	if rep.Files != 5 {
		t.Fatalf("files = %d, want 5 (closed/ must not be descended)", rep.Files)
	}
	if rep.Claims != 4 {
		t.Fatalf("claims = %d, want 4 (#366, #224, #212, #341)", rep.Claims)
	}
	if len(rep.Findings) != 2 {
		t.Fatalf("findings = %+v, want exactly 2", rep.Findings)
	}
	byPR := map[int]Finding{}
	for _, f := range rep.Findings {
		byPR[f.PR] = f
	}
	f366, ok := byPR[366]
	if !ok || f366.File != "model-lineup.md" || f366.LineNo != 3 {
		t.Fatalf("#366 finding = %+v", f366)
	}
	f224, ok := byPR[224]
	if !ok || f224.File != "opus5.md" || f224.LineNo != 3 {
		t.Fatalf("#224 finding = %+v", f224)
	}
	for _, pr := range []int{212, 340, 341, 12} {
		if _, hit := byPR[pr]; hit {
			t.Fatalf("#%d must not be a finding: %+v", pr, byPR[pr])
		}
	}
	if !shaRe.MatchString(f366.MergeSHA) || f366.MergedDate() == "" || len(f366.ShortSHA()) != 7 {
		t.Fatalf("finding shape = %+v", f366)
	}
	if want := "model-lineup.md:3 says PR #366 is open; it merged " + f366.MergedDate() + " (" + f366.ShortSHA() + ")"; f366.Describe() != want {
		t.Fatalf("Describe = %q, want %q", f366.Describe(), want)
	}
	if rep.Dir != fixtureDir || rep.Project != repo {
		t.Fatalf("report dir/project = %q/%q", rep.Dir, rep.Project)
	}
}

// Which PRs are claimed (not just which are stale) is part of the contract:
// #212 and #341 are claims the history cannot confirm, #340 and #12 are not
// claims at all.
func TestMemlintFixtureClaims(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	var all []Claim
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixtureDir, en.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, ParseClaims(en.Name(), string(data))...)
	}
	got := claimPRs(all)
	want := []int{212, 224, 341, 366}
	if len(got) != len(want) {
		t.Fatalf("claims = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("claims = %v, want %v", got, want)
		}
	}
}

func TestMemlintLintDirWithoutGitIsNotAnError(t *testing.T) {
	rep, err := LintDir(fixtureDir, t.TempDir())
	if err != nil {
		t.Fatalf("LintDir: %v", err)
	}
	if rep.Claims != 4 || len(rep.Findings) != 0 {
		t.Fatalf("claims=%d findings=%d, want 4 claims and no findings without .git", rep.Claims, len(rep.Findings))
	}
	if rep.Findings == nil {
		t.Fatal("Findings must be an empty slice, not nil (JSON [] not null)")
	}
}

func TestMemlintLintDirMissingIsNotExist(t *testing.T) {
	_, err := LintDir(filepath.Join(t.TempDir(), "nope"), t.TempDir())
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want IsNotExist", err)
	}
}

func TestMemlintLintResolvesThroughClaudeDir(t *testing.T) {
	repo := memlinttest.Repo(t)
	claude := t.TempDir()
	memlinttest.WriteLines(t, memconsolidate.AutoMemoryDirIn(claude, repo))

	rep, err := Lint(repo, claude)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if rep.Files != 5 || rep.Claims != 4 || len(rep.Findings) != 2 {
		t.Fatalf("report = files %d claims %d findings %d", rep.Files, rep.Claims, len(rep.Findings))
	}
	if rep.Project != repo || rep.Dir != memconsolidate.AutoMemoryDirIn(claude, repo) {
		t.Fatalf("report dir/project = %q/%q", rep.Dir, rep.Project)
	}
}

func TestMemlintClaimTruncation(t *testing.T) {
	long := make([]rune, 0, 400)
	for len(long) < 300 {
		long = append(long, 'ж')
	}
	line := "PR #5 open " + string(long)
	got := truncateRunes(line, ClaimMaxRunes)
	if n := len([]rune(got)); n != ClaimMaxRunes {
		t.Fatalf("truncated to %d runes, want %d", n, ClaimMaxRunes)
	}
	if got[len(got)-len("…"):] != "…" {
		t.Fatalf("truncation marker missing: %q", got[len(got)-10:])
	}
}
