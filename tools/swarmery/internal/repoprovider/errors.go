package repoprovider

import (
	"errors"
	"os/exec"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

// Sentinels. Callers branch on them with errors.Is to pick an HTTP status and
// an operator hint; the wrapped detail is already redacted.
var (
	ErrNoRemote         = errors.New("no remote configured")
	ErrNotAuthenticated = errors.New("not authenticated to the code host")
	ErrNoPushAccess     = errors.New("no push access")
	ErrRemoteDiverged   = errors.New("remote branch has diverged")
	ErrBinaryMissing    = errors.New("required CLI not found on PATH")
	// ErrBaseRefused and ErrForkUnsupported are policy refusals made by
	// callers (push to the base branch without allowPushToBase; the reserved
	// fork workflow), never classified from tool output.
	ErrBaseRefused     = errors.New("pushing to the base branch is refused")
	ErrForkUnsupported = errors.New("fork workflow is not supported yet")
)

// Error is a classified tool failure. Its message is redacted; Unwrap exposes
// both the sentinel (nil when unclassified) and the underlying process error,
// so errors.Is works against either.
type Error struct {
	Sentinel error
	Detail   string // redacted, ≤ OutputTail bytes
	Cause    error
}

func (e *Error) Error() string {
	head := "command failed"
	if e.Sentinel != nil {
		head = e.Sentinel.Error()
	}
	if e.Detail == "" {
		return head
	}
	return head + ": " + e.Detail
}

// Unwrap implements multi-error unwrapping.
func (e *Error) Unwrap() []error {
	var out []error
	if e.Sentinel != nil {
		out = append(out, e.Sentinel)
	}
	if e.Cause != nil {
		out = append(out, e.Cause)
	}
	return out
}

// classifier maps stderr substrings (matched case-insensitively) to a
// sentinel. Order matters: a protected-branch rejection also says "rejected",
// and must read as a permission problem, not a divergence.
var classifier = []struct {
	sentinel error
	needles  []string
}{
	{ErrNoPushAccess, []string{
		"protected branch", "http 403", "returned error: 403", "403 forbidden",
	}},
	{ErrNotAuthenticated, []string{
		"not logged into", "gh auth login", "glab auth login", "http 401", "returned error: 401",
		"401 unauthorized", "bad credentials", "authentication failed", "could not read username",
		"permission denied (publickey)",
	}},
	{ErrRemoteDiverged, []string{"non-fast-forward", "fetch first", "[rejected]"}},
	{ErrNoRemote, []string{"no such remote", "does not appear to be a git repository"}},
}

// Classify turns a failed tool call into an *Error carrying a sentinel when
// the output matches a known failure, and a redacted, bounded detail either
// way. nil when err is nil.
func Classify(stderr string, err error) error {
	if err == nil {
		return nil
	}
	return classify(stderr, err)
}

// classify is Classify for a non-nil err, typed so a caller can re-label the
// sentinel (Detect turns any remote-lookup failure into ErrNoRemote).
func classify(stderr string, err error) *Error {
	detail := credstore.Redact(Tail(stderr, err))
	if errors.Is(err, exec.ErrNotFound) {
		return &Error{Sentinel: ErrBinaryMissing, Detail: detail, Cause: err}
	}
	low := strings.ToLower(stderr)
	// "Permission to x/y.git denied to user" — two separated substrings.
	if strings.Contains(low, "permission to") && strings.Contains(low, "denied") {
		return &Error{Sentinel: ErrNoPushAccess, Detail: detail, Cause: err}
	}
	for _, c := range classifier {
		for _, n := range c.needles {
			if strings.Contains(low, n) {
				return &Error{Sentinel: c.sentinel, Detail: detail, Cause: err}
			}
		}
	}
	return &Error{Detail: detail, Cause: err}
}

// Tail picks the most informative text of a failed call — the tool's own
// stderr when it said something, the process error otherwise — bounded to
// OutputTail bytes (the end, where tools put the reason). Not redacted; use
// Classify for anything that leaves the package.
func Tail(stderr string, err error) string {
	s := strings.TrimSpace(stderr)
	if s == "" && err != nil {
		s = err.Error()
	}
	if len(s) > OutputTail {
		s = s[len(s)-OutputTail:]
	}
	return s
}
