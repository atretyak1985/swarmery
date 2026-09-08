package archmap

// Git helpers for the blast endpoint. Unlike internal/githead — which resolves
// HEAD by reading .git files precisely so the /api/tools feed never forks a
// process — these questions ("what changed between two commits", "how far
// behind is this") need real revision walking, so they shell out to git.
//
// Every call is bounded twice: a 5 s deadline via exec.CommandContext, and a
// process-lifetime memo keyed by (repo, from, to). The memo is what makes this
// affordable on a page that re-fetches on a 3 s settle-poll: `from..to` is a
// pair of immutable commit-ish names for the duration of a request burst, and
// when HEAD moves the key moves with it, so a new HEAD simply misses the cache
// rather than reading a stale answer. Entries are never mutated in place.

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gitTimeout bounds every git invocation. A repo big enough to need longer
// than this is a repo whose numbers the page is better off omitting.
const gitTimeout = 5 * time.Second

// memoCap bounds the in-memory result cache. Keys are (repo, from, to); the
// whole map is dropped once it grows past the cap rather than evicting an LRU
// tail — the working set is "the few projects on screen", so a periodic full
// reset costs one recompute and keeps this to a dozen lines instead of a list.
const memoCap = 256

type memoKey struct{ repo, from, to, op string }

var (
	memoMu  sync.Mutex
	memoMap = map[memoKey]memoVal{}
)

type memoVal struct {
	files []string
	n     int
	err   error
}

// memoStats reports the live cache size; tests use it to prove a second call
// was served from the memo rather than from git.
func memoStats() int {
	memoMu.Lock()
	defer memoMu.Unlock()
	return len(memoMap)
}

// ResetMemo drops every memoised git result. Exported for tests and for any
// caller that has just mutated a repo out of band.
func ResetMemo() {
	memoMu.Lock()
	memoMap = map[memoKey]memoVal{}
	memoMu.Unlock()
}

// memoized runs fn once per key and serves every later call from the cache,
// errors included: a repo that is not a repo must not be re-forked on every
// poll just to fail the same way.
func memoized(k memoKey, fn func() memoVal) memoVal {
	memoMu.Lock()
	if v, ok := memoMap[k]; ok {
		memoMu.Unlock()
		return v
	}
	memoMu.Unlock()

	v := fn()

	memoMu.Lock()
	if len(memoMap) >= memoCap {
		memoMap = map[memoKey]memoVal{}
	}
	memoMap[k] = v
	memoMu.Unlock()
	return v
}

// safeRev rejects revision arguments that could be read as git options. Both
// `from` and `to` reach here from a query string, and `--upload-pack=…` in
// argv[n] is the classic way a "just two revisions" call turns into an exec.
func safeRev(rev string) error {
	if rev == "" {
		return fmt.Errorf("archmap: empty revision")
	}
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("archmap: revision %q may not start with '-'", rev)
	}
	if strings.ContainsAny(rev, " \t\n\r") {
		return fmt.Errorf("archmap: revision %q may not contain whitespace", rev)
	}
	return nil
}

// runGit executes git in `repo` with a 5 s deadline, returning trimmed stdout.
// Stderr rides along in the error so a caller logging it sees git's own words
// ("unknown revision", "not a git repository") rather than just "exit status 128".
func runGit(ctx context.Context, repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("archmap: git %s timed out after %s", args[0], gitTimeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("archmap: git %s: %w", args[0], err)
		}
		return "", fmt.Errorf("archmap: git %s: %w: %s", args[0], err, msg)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Diff returns the repo-relative paths changed between `from` and `to`
// (`git diff --name-only from..to`), ascending as git emits them. An empty
// range yields an empty, non-nil slice.
func Diff(ctx context.Context, repo, from, to string) ([]string, error) {
	if err := safeRev(from); err != nil {
		return nil, err
	}
	if err := safeRev(to); err != nil {
		return nil, err
	}
	v := memoized(memoKey{repo: repo, from: from, to: to, op: "diff"}, func() memoVal {
		out, err := runGit(ctx, repo, "diff", "--name-only", from+".."+to)
		if err != nil {
			return memoVal{err: err}
		}
		files := []string{}
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				files = append(files, line)
			}
		}
		return memoVal{files: files}
	})
	if v.err != nil {
		return nil, v.err
	}
	// Copy: the memo hands the same backing array to every caller, and a
	// caller that sorts or appends in place would corrupt the cache. make+copy
	// rather than append-to-nil, which would hand back a NIL slice for an empty
	// range and turn "nothing changed" into JSON `null`.
	out := make([]string, len(v.files))
	copy(out, v.files)
	return out, nil
}

// Behind returns how many commits `to` is ahead of `from`
// (`git rev-list --count from..to`) — i.e. how far behind `from` the map is
// when `from` is the analysed commit.
func Behind(ctx context.Context, repo, from, to string) (int, error) {
	if err := safeRev(from); err != nil {
		return 0, err
	}
	if err := safeRev(to); err != nil {
		return 0, err
	}
	v := memoized(memoKey{repo: repo, from: from, to: to, op: "behind"}, func() memoVal {
		out, err := runGit(ctx, repo, "rev-list", "--count", from+".."+to)
		if err != nil {
			return memoVal{err: err}
		}
		n, cerr := strconv.Atoi(strings.TrimSpace(out))
		if cerr != nil {
			return memoVal{err: fmt.Errorf("archmap: rev-list --count returned %q: %w", out, cerr)}
		}
		return memoVal{n: n}
	})
	if v.err != nil {
		return 0, v.err
	}
	return v.n, nil
}

// DefaultBranch names the repo's default branch: the target of
// refs/remotes/origin/HEAD when the remote published one, else whichever of
// `main` / `master` actually exists. It is NOT memoised — the answer is a
// single ref read, and a repo that just gained an origin should not have to
// wait out a cache to be measured against it.
//
// The empty string means "no default branch could be determined"; callers
// treat that as "no baseline", not as "main".
func DefaultBranch(repo string) string {
	ctx := context.Background()
	if out, err := runGit(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		// "origin/main" → "main"; a bare name is passed through unchanged.
		if _, name, ok := strings.Cut(out, "/"); ok && name != "" {
			return name
		}
		if out != "" {
			return out
		}
	}
	// refs/heads/<name>, not the bare name: `git rev-parse --verify main` also
	// succeeds for a TAG or a file called main, and naming a tag as the diff
	// base would silently measure against the wrong thing.
	for _, cand := range []string{"main", "master"} {
		if _, err := runGit(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+cand); err == nil {
			return cand
		}
	}
	return ""
}
