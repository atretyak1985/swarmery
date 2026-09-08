package archmap

// Git helpers for the blast endpoint. Unlike internal/githead — which resolves
// HEAD by reading .git files precisely so the /api/tools feed never forks a
// process — these questions ("what changed between two commits", "how far
// behind is this") need real revision walking, so they shell out to git.
//
// Every call is bounded twice: a 5 s deadline via exec.CommandContext, and a
// process-lifetime memo keyed by (repo, from, to, op). The memo is what makes
// this affordable on a page that re-fetches on a 3 s settle-poll. Two rules
// keep it from lying:
//
//   - Staleness. A key made only of OBJECT NAMES (full shas) is immutable, so
//     its entry never expires: when HEAD moves the key moves with it and a new
//     HEAD simply misses the cache. A key still holding a MUTABLE name — a
//     branch, a tag, HEAD — gets a short expiry instead, because `main` moves
//     under a stationary feature HEAD on every fetch, and a permanent entry
//     would serve the pre-fetch answer for the daemon's lifetime.
//   - Failure. Only PERMANENT failures are cached ("not a git repository",
//     "unknown revision"): re-forking those on every poll just to fail the same
//     way is waste. Transient ones — a deadline on a cold first paint, an
//     index.lock held by a concurrent agent session — are never cached, so the
//     next poll retries instead of the panel staying silently absent until the
//     daemon restarts.
//
// Entries are never mutated in place.

import (
	"context"
	"errors"
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

// memoCap bounds the in-memory result cache. Keys are (repo, from, to, op); the
// whole map is dropped once it grows past the cap rather than evicting an LRU
// tail — the working set is "the few projects on screen", so a periodic full
// reset costs one recompute and keeps this to a dozen lines instead of a list.
const memoCap = 256

// mutableRevTTL is how long an entry whose key still holds a mutable name is
// trusted: long enough that a settle-poll burst costs one fork, short enough
// that a fetch which moved the base branch shows up within seconds. A var, not
// a const, so tests can shrink it.
var mutableRevTTL = 30 * time.Second

type memoKey struct{ repo, from, to, op string }

var (
	memoMu  sync.Mutex
	memoMap = map[memoKey]memoVal{}
)

type memoVal struct {
	files []string
	n     int
	sha   string
	err   error
	// expires is when this entry stops being served. The zero time means
	// "never": every revision in its key is an object name, so the answer
	// cannot go stale.
	expires time.Time
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

// memoized runs fn once per key and serves later calls from the cache until the
// entry expires; ttl == 0 means it never does. A permanent failure is cached
// like any value — a repo that is not a repo must not be re-forked on every
// poll just to fail the same way — but a TRANSIENT one is returned WITHOUT
// being stored, so one unlucky moment does not outlive itself.
func memoized(k memoKey, ttl time.Duration, fn func() memoVal) memoVal {
	now := time.Now()

	memoMu.Lock()
	if v, ok := memoMap[k]; ok {
		if v.expires.IsZero() || now.Before(v.expires) {
			memoMu.Unlock()
			return v
		}
		delete(memoMap, k)
	}
	memoMu.Unlock()

	v := fn()

	if v.err != nil && isTransient(v.err) {
		return v
	}
	if ttl > 0 {
		v.expires = now.Add(ttl)
	}

	memoMu.Lock()
	if len(memoMap) >= memoCap {
		memoMap = map[memoKey]memoVal{}
	}
	memoMap[k] = v
	memoMu.Unlock()
	return v
}

// isObjectName reports whether rev is a full object name — 40 hex chars (SHA-1)
// or 64 (SHA-256). Branch and tag names are not, and neither are abbreviated
// shas: an abbreviation can turn ambiguous as the repo gains objects.
func isObjectName(rev string) bool {
	if len(rev) != 40 && len(rev) != 64 {
		return false
	}
	for _, r := range rev {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// memoTTL returns 0 ("cache forever") only when every revision in the key is an
// immutable object name; otherwise a short expiry.
func memoTTL(revs ...string) time.Duration {
	for _, rev := range revs {
		if !isObjectName(rev) {
			return mutableRevTTL
		}
	}
	return 0
}

// gitError carries git's own words plus whether a retry could plausibly
// succeed. Permanent failures are an allowlist of git's own phrasings; anything
// else is treated as transient, because the cost of retrying a truly permanent
// error is one process per poll, while the cost of caching a truly transient
// one is a silently absent number until the daemon restarts.
type gitError struct {
	msg       string
	stderr    string
	err       error
	transient bool
}

func (e *gitError) Error() string { return e.msg }
func (e *gitError) Unwrap() error { return e.err }

// permanentGitMessages mark a failure git will keep reporting for the same
// arguments no matter how often it is asked.
var permanentGitMessages = []string{
	"not a git repository",
	"unknown revision or path not in the working tree",
	"bad revision",
	"bad object",
	"ambiguous argument",
}

// transientGitMessages win over the permanent list when both match: lock
// contention is the failure this daemon provokes on itself, because the repos
// it measures are the repos its own parallel agent sessions commit in.
var transientGitMessages = []string{
	"index.lock",
	"another git process seems to be running",
}

func isPermanentGitMessage(stderr string) bool {
	if stderr == "" {
		// git never got far enough to say anything — a missing binary, an
		// unreadable working dir, a killed process. All worth retrying.
		return false
	}
	low := strings.ToLower(stderr)
	for _, p := range transientGitMessages {
		if strings.Contains(low, p) {
			return false
		}
	}
	for _, p := range permanentGitMessages {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// isTransient reports whether a retry of the same call could plausibly succeed.
func isTransient(err error) bool {
	var ge *gitError
	if errors.As(err, &ge) {
		return ge.transient
	}
	// Anything not classified here (an unparseable rev-list count, an absent
	// merge base) is a deterministic decoding failure, not a flake.
	return false
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
			return "", &gitError{
				msg:       fmt.Sprintf("archmap: git %s timed out after %s", args[0], gitTimeout),
				err:       ctx.Err(),
				transient: true,
			}
		}
		msg := strings.TrimSpace(stderr.String())
		ge := &gitError{stderr: msg, err: err, transient: !isPermanentGitMessage(msg)}
		if msg == "" {
			ge.msg = fmt.Sprintf("archmap: git %s: %v", args[0], err)
		} else {
			ge.msg = fmt.Sprintf("archmap: git %s: %v: %s", args[0], err, msg)
		}
		return "", ge
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Diff returns the repo-relative paths changed between `from` and `to`
// (`git diff --name-only from..to`), ascending as git emits them. An empty
// range yields an empty, non-nil slice.
//
// TWO dots on purpose: this answers "how far has `to` moved past `from`", which
// is exactly what the map-freshness numbers ask. A caller asking instead "what
// does this BRANCH touch" must resolve `from` through MergeBase first —
// otherwise a base branch that advanced after the branch was cut lands in the
// answer as if this branch had touched it.
func Diff(ctx context.Context, repo, from, to string) ([]string, error) {
	if err := safeRev(from); err != nil {
		return nil, err
	}
	if err := safeRev(to); err != nil {
		return nil, err
	}
	v := memoized(memoKey{repo: repo, from: from, to: to, op: "diff"}, memoTTL(from, to), func() memoVal {
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

// MergeBase resolves the best common ancestor of `base` and `head`
// (`git merge-base base head`) — the commit the branch was cut from.
//
// Diffing a branch from THIS sha rather than from the base branch tip is the
// difference between `base..head` and `base...head`: once the base branch
// advances after the branch was cut, a two-dot diff attributes every file those
// newer base commits touched to this branch, inflating the blast radius with
// modules the branch never opened — precisely the question the endpoint exists
// to answer. Resolving to a sha also makes the downstream Diff key immutable,
// so that entry can be memoised for good.
func MergeBase(ctx context.Context, repo, base, head string) (string, error) {
	if err := safeRev(base); err != nil {
		return "", err
	}
	if err := safeRev(head); err != nil {
		return "", err
	}
	v := memoized(memoKey{repo: repo, from: base, to: head, op: "merge-base"}, memoTTL(base, head), func() memoVal {
		out, err := runGit(ctx, repo, "merge-base", base, head)
		if err != nil {
			// Unrelated histories: git exits 1 saying nothing at all. That is a
			// property of the two commits, not a flake, so it IS cached.
			var ge *gitError
			var ee *exec.ExitError
			if errors.As(err, &ge) && ge.stderr == "" && errors.As(err, &ee) && ee.ExitCode() == 1 {
				return memoVal{err: &gitError{msg: fmt.Sprintf("archmap: no merge base between %q and %q", base, head)}}
			}
			return memoVal{err: err}
		}
		sha := strings.TrimSpace(out)
		if sha == "" {
			return memoVal{err: fmt.Errorf("archmap: no merge base between %q and %q", base, head)}
		}
		return memoVal{sha: sha}
	})
	if v.err != nil {
		return "", v.err
	}
	return v.sha, nil
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
	v := memoized(memoKey{repo: repo, from: from, to: to, op: "behind"}, memoTTL(from, to), func() memoVal {
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
