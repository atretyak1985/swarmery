// Package memlinttest holds the fixtures the memlint, advisor and api tests
// share: a throwaway git repository with chosen commit subjects, and the memory
// lines that reproduce the three stale notes found in this repo on 2026-10-07.
// It lives outside the _test files so three packages do not each carry a copy.
package memlinttest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Subjects are the commit subjects Repo writes by default: the merge-commit
// shape for #366 and the squash shape for #224. Nothing mentions #212 or #341.
var Subjects = []string{
	"chore: first",
	"Merge pull request #366 from x/y",
	"fix: thing (#224)",
}

// Repo creates a git repository under t.TempDir() with one empty commit per
// subject (Subjects when none are given) and returns its path. It skips the
// test when git is not on PATH.
func Repo(t testing.TB, subjects ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if len(subjects) == 0 {
		subjects = Subjects
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for _, s := range subjects {
		run("commit", "-q", "--allow-empty", "-m", s)
	}
	return dir
}

// Lines reproduces the real stale notes (and their healthy neighbours) as
// file → content. Claims: #366 (merge shape), #224 (squash shape), #212 (never
// merged) and #341 (the open half of a mixed line). Never claims: #340 (closed
// on the same line) and the `#issuecomment-1` URL fragment in clean.md.
var Lines = map[string]string{
	"MEMORY.md": "# memory index\n" +
		"- [Model lineup](model-lineup.md) — current model and the headless pins.\n" +
		"- [Opus 5 prompting](opus5.md) — the prompting guide facts.\n" +
		"- [Plugins effective settings](plugins.md) — settings.local.json as an overlay.\n" +
		"- [Secrets & spawn env](clean.md) — PR #340 … MERGED; #341 open.\n",
	"model-lineup.md": "# Model lineup\n\n" +
		"Opus 5.5 current since 2026-09-22; daemon installed + recost done, but PR #366 UNMERGED (needs a review approval); aliases-only in agent frontmatter.\n",
	"opus5.md": "# Opus 5 prompting alignment\n\n" +
		"PR #224 open 2026-08-11: tech-lead xhigh + delegation economy + runner --effort pins; guide facts inside.\n",
	"plugins.md": "# Plugins effective local settings\n\n" +
		"PR #212 open: settings.local.json as implicit overlay + PUT override warning + dist prune.\n",
	"clean.md": "# Secrets & spawn env\n\n" +
		"#340 + 7 PRs MERGED 2026-09-18; the follow-up at https://github.com/x/y/issues/12#issuecomment-1 is still open for the record.\n",
}

// WriteLines writes Lines into dir (creating it), plus a closed/ subdirectory
// holding a stale-looking line that a correct lint never counts.
func WriteLines(t testing.TB, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "closed"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for name, content := range Lines {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	closed := "# archived\n\nPR #366 UNMERGED (needs a review approval) — archived copy.\n"
	if err := os.WriteFile(filepath.Join(dir, "closed", "old.md"), []byte(closed), 0o644); err != nil {
		t.Fatalf("write closed/old.md: %v", err)
	}
}
