package archmap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// gitIn runs git in dir with a pinned identity and no global config, so no test
// here inherits the machine's commit.gpgsign or init.defaultBranch.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeIn writes a file under dir, creating parent directories.
func writeIn(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tempRepo builds a throwaway git repo with two commits and returns its path
// plus the two shas. Committer identity and the initial branch are pinned so
// the test does not depend on the machine's git config.
func tempRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()

	run := func(args ...string) string { t.Helper(); return gitIn(t, dir, args...) }
	write := func(rel, body string) { t.Helper(); writeIn(t, dir, rel, body) }

	run("init", "--initial-branch=main", ".")
	write("README.md", "one\n")
	write("internal/api/routes.go", "package api\n")
	run("add", "-A")
	run("commit", "-m", "first")
	first = run("rev-parse", "HEAD")

	write("web/src/pages/Home.tsx", "export const Home = () => null;\n")
	write("internal/api/routes.go", "package api // touched\n")
	run("add", "-A")
	run("commit", "-m", "second")
	second = run("rev-parse", "HEAD")

	return dir, first, second
}

func TestDiff(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	files, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	want := []string{"internal/api/routes.go", "web/src/pages/Home.tsx"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("Diff = %v, want %v", files, want)
	}

	// An empty range is an empty, non-nil slice — "nothing changed", not "unknown".
	same, err := Diff(context.Background(), dir, second, second)
	if err != nil {
		t.Fatalf("Diff(second..second): %v", err)
	}
	if same == nil || len(same) != 0 {
		t.Errorf("Diff(second..second) = %v, want an empty non-nil slice", same)
	}
}

func TestBehind(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	n, err := Behind(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Behind: %v", err)
	}
	if n != 1 {
		t.Errorf("Behind(first..second) = %d, want 1", n)
	}

	if n, err := Behind(context.Background(), dir, second, second); err != nil || n != 0 {
		t.Errorf("Behind(second..second) = %d, %v; want 0, nil", n, err)
	}
	// The range is directional: second..first walks backwards and finds nothing.
	if n, err := Behind(context.Background(), dir, second, first); err != nil || n != 0 {
		t.Errorf("Behind(second..first) = %d, %v; want 0, nil", n, err)
	}
}

// TestMemo proves the second call for the same (repo, from, to) is served from
// the cache: git is removed from the equation by deleting the repo between the
// two calls — a re-fork would fail, the memo cannot.
func TestMemo(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	files, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	behind, err := Behind(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Behind: %v", err)
	}
	if got := memoStats(); got != 2 {
		t.Errorf("memo size = %d after one Diff + one Behind, want 2", got)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	files2, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("memoised Diff after the repo vanished: %v", err)
	}
	if !reflect.DeepEqual(files2, files) {
		t.Errorf("memoised Diff = %v, want the first result %v", files2, files)
	}
	behind2, err := Behind(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("memoised Behind after the repo vanished: %v", err)
	}
	if behind2 != behind {
		t.Errorf("memoised Behind = %d, want %d", behind2, behind)
	}

	// The returned slice is a copy — mutating it must not poison the cache.
	files2[0] = "MUTATED"
	files3, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if files3[0] == "MUTATED" {
		t.Error("mutating a returned slice changed the memoised value")
	}

	ResetMemo()
	if got := memoStats(); got != 0 {
		t.Errorf("memo size = %d after ResetMemo, want 0", got)
	}
}

func TestDiffRejectsOptionLikeRevisions(t *testing.T) {
	ResetMemo()
	dir, _, second := tempRepo(t)

	for _, bad := range []string{"", "--upload-pack=touch /tmp/pwned", "-x", "a b"} {
		if _, err := Diff(context.Background(), dir, bad, second); err == nil {
			t.Errorf("Diff accepted from=%q, want a refusal", bad)
		}
		if _, err := Behind(context.Background(), dir, second, bad); err == nil {
			t.Errorf("Behind accepted to=%q, want a refusal", bad)
		}
	}
}

func TestDiffOnNonRepo(t *testing.T) {
	ResetMemo()
	if _, err := Diff(context.Background(), t.TempDir(), "a1b2c3d", "e4f5a6b"); err == nil {
		t.Error("Diff on a non-repo returned nil error")
	}
}

func TestDefaultBranch(t *testing.T) {
	ResetMemo()
	dir, _, _ := tempRepo(t)

	// No origin: falls through to the local `main` the fixture repo was
	// initialised with.
	if got := DefaultBranch(dir); got != "main" {
		t.Errorf("DefaultBranch = %q, want \"main\"", got)
	}

	// Not a repo at all: the empty string, meaning "no baseline" — never a
	// confident "main" the caller would then diff against and 500 on.
	if got := DefaultBranch(t.TempDir()); got != "" {
		t.Errorf("DefaultBranch(non-repo) = %q, want \"\"", got)
	}
}

// tempRepoAdvancedBase builds the shape the blast endpoint exists to measure:
// `main` gains a commit AFTER `feature` was cut from it. base.txt is touched
// only by that later main commit and feature.txt only by the branch, so a diff
// starting at main's TIP wrongly reports base.txt as this branch's work while
// one starting at the merge base does not. Returns the repo, the sha the branch
// was cut at, and the feature tip.
func tempRepoAdvancedBase(t *testing.T) (dir, cut, feature string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()

	gitIn(t, dir, "init", "--initial-branch=main", ".")
	writeIn(t, dir, "README.md", "one\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "base")
	cut = gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-b", "feature")
	writeIn(t, dir, "feature.txt", "only this branch touched me\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "feature work")
	feature = gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "main")
	writeIn(t, dir, "base.txt", "landed on main after the cut\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "main moves on")
	gitIn(t, dir, "checkout", "feature")

	return dir, cut, feature
}

// TestMergeBaseExcludesCommitsThatLandedOnBaseAfterTheCut is the regression for
// the two-dot bug. `main..feature` folds base.txt — a file only main's newer
// commit touched — into the branch's answer; the merge-base range does not.
func TestMergeBaseExcludesCommitsThatLandedOnBaseAfterTheCut(t *testing.T) {
	ResetMemo()
	dir, cut, feature := tempRepoAdvancedBase(t)

	mb, err := MergeBase(context.Background(), dir, "main", feature)
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if mb != cut {
		t.Fatalf("MergeBase(main, feature) = %q, want the commit the branch was cut at %q", mb, cut)
	}

	files, err := Diff(context.Background(), dir, mb, feature)
	if err != nil {
		t.Fatalf("Diff from the merge base: %v", err)
	}
	if !reflect.DeepEqual(files, []string{"feature.txt"}) {
		t.Errorf("Diff(mergeBase..feature) = %v, want [feature.txt] — base.txt is main's work, not this branch's", files)
	}

	// Pin the behaviour this replaced, so the regression cannot quietly return:
	// the range against main's TIP really does report base.txt.
	tip, err := Diff(context.Background(), dir, "main", feature)
	if err != nil {
		t.Fatalf("Diff from the base tip: %v", err)
	}
	if !slices.Contains(tip, "base.txt") {
		t.Fatalf("fixture no longer reproduces the bug: main..feature = %v, expected base.txt in it", tip)
	}
}

func TestMergeBaseRejectsOptionLikeRevisions(t *testing.T) {
	ResetMemo()
	dir, _, second := tempRepo(t)

	for _, bad := range []string{"", "--upload-pack=touch /tmp/pwned", "-x", "a b"} {
		if _, err := MergeBase(context.Background(), dir, bad, second); err == nil {
			t.Errorf("MergeBase accepted base=%q, want a refusal", bad)
		}
		if _, err := MergeBase(context.Background(), dir, second, bad); err == nil {
			t.Errorf("MergeBase accepted head=%q, want a refusal", bad)
		}
	}
}

// TestMemoRetriesTransientErrors: a repo that is momentarily unreachable is a
// TRANSIENT failure, so the answer must not be cached — otherwise one unlucky
// moment (a 5 s timeout on a cold paint, an index.lock held by a concurrent
// agent session) leaves the panel empty for the daemon's lifetime.
func TestMemoRetriesTransientErrors(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	moved := dir + ".moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := Diff(context.Background(), dir, first, second); err == nil {
		t.Fatal("Diff on a vanished repo returned a nil error")
	}
	if got := memoStats(); got != 0 {
		t.Errorf("memo size = %d after a transient failure, want 0 — a retry must be able to succeed", got)
	}

	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
	files, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Diff after the repo came back: %v — the transient failure was cached", err)
	}
	if len(files) != 2 {
		t.Errorf("Diff after the retry = %v, want the two changed files", files)
	}
}

// TestMemoCachesPermanentErrors: "unknown revision" is git's answer forever, so
// it is memoised and never re-forked. Proof: the repo is deleted afterwards and
// the SAME error text comes back — a re-fork would fail differently.
func TestMemoCachesPermanentErrors(t *testing.T) {
	ResetMemo()
	dir, _, second := tempRepo(t)

	_, err := Diff(context.Background(), dir, "nosuchref", second)
	if err == nil {
		t.Fatal("Diff against an unknown revision returned a nil error")
	}
	if !strings.Contains(err.Error(), "unknown revision") {
		t.Fatalf("unexpected error %v — the fixture no longer produces a permanent failure", err)
	}
	if got := memoStats(); got != 1 {
		t.Fatalf("memo size = %d after a permanent failure, want 1", got)
	}

	if rmErr := os.RemoveAll(dir); rmErr != nil {
		t.Fatal(rmErr)
	}
	_, err2 := Diff(context.Background(), dir, "nosuchref", second)
	if err2 == nil || err2.Error() != err.Error() {
		t.Errorf("second call = %v, want the memoised %v", err2, err)
	}
}

// TestMemoExpiresMutableRevKeys: a key naming a BRANCH cannot be trusted for
// the daemon's lifetime — a fetch moves `main` under a stationary feature HEAD.
// Shrinking the TTL proves that entry is recomputed; the all-sha key beside it
// proves an immutable one is not.
func TestMemoExpiresMutableRevKeys(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	orig := mutableRevTTL
	mutableRevTTL = time.Nanosecond
	t.Cleanup(func() { mutableRevTTL = orig })

	if _, err := Diff(context.Background(), dir, "main", second); err != nil {
		t.Fatalf("Diff(main..second): %v", err)
	}
	if _, err := Diff(context.Background(), dir, first, second); err != nil {
		t.Fatalf("Diff(first..second): %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Diff(context.Background(), dir, "main", second); err == nil {
		t.Error("the branch-keyed entry outlived its TTL; a base branch that moved would stay invisible")
	}
	if _, err := Diff(context.Background(), dir, first, second); err != nil {
		t.Errorf("the all-sha entry expired (%v); an immutable key must be cached for good", err)
	}
}

func TestIsObjectName(t *testing.T) {
	for _, tc := range []struct {
		rev  string
		want bool
	}{
		{strings.Repeat("a", 40), true},
		{strings.Repeat("F", 64), true},
		{strings.Repeat("a", 7), false},  // abbreviated — can turn ambiguous
		{strings.Repeat("g", 40), false}, // not hex
		{"main", false},
		{"HEAD", false},
		{"", false},
	} {
		if got := isObjectName(tc.rev); got != tc.want {
			t.Errorf("isObjectName(%q) = %v, want %v", tc.rev, got, tc.want)
		}
	}
}

func TestGitErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		stderr    string
		permanent bool
	}{
		{"fatal: not a git repository (or any of the parent directories): .git", true},
		{"fatal: ambiguous argument 'x..HEAD': unknown revision or path not in the working tree.", true},
		{"fatal: bad object deadbeef", true},
		{"fatal: Unable to create '/repo/.git/index.lock': File exists.", false},
		{"error: another git process seems to be running in this repository", false},
		{"", false}, // git never got far enough to speak — worth retrying
		{"fatal: could not read Username: terminal prompts disabled", false},
	} {
		if got := isPermanentGitMessage(tc.stderr); got != tc.permanent {
			t.Errorf("isPermanentGitMessage(%q) = %v, want %v", tc.stderr, got, tc.permanent)
		}
	}

	// A plain error carries no classification, and an unclassified failure is a
	// deterministic decoding failure — cached, not retried.
	if isTransient(errors.New("archmap: rev-list --count returned \"x\"")) {
		t.Error("isTransient(plain error) = true, want false")
	}
	if !isTransient(&gitError{msg: "timed out", transient: true}) {
		t.Error("isTransient(transient gitError) = false, want true")
	}
}

func TestDefaultBranchPrefersOriginHEAD(t *testing.T) {
	ResetMemo()
	dir, _, _ := tempRepo(t)

	// Fabricate origin/HEAD → origin/trunk without a network remote: a packed
	// symbolic ref file is exactly what `git remote set-head` writes.
	gitDir := filepath.Join(dir, ".git", "refs", "remotes", "origin")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/remotes/origin/trunk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "trunk"), []byte(strings.Repeat("0", 40)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := DefaultBranch(dir); got != "trunk" {
		t.Errorf("DefaultBranch = %q, want \"trunk\" from refs/remotes/origin/HEAD", got)
	}
}
