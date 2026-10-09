// Package github is the GitHub repoprovider.Provider, driven through the `gh`
// CLI (and `git` for the push).
//
// Imports: the parent repoprovider (types, Exec, Classify) and its credstore
// leaf (Redact) — never the other way round (see the repoprovider package doc).
//
// Every call that needs credentials runs with the env delta returned by the
// injected env func (production: credstore.Env — the daemon's own token and an
// isolated GH_CONFIG_DIR) when that delta carries a GitHub token, and with a
// nil delta otherwise: an operator logged in only through `gh auth login`
// keeps working exactly as board land always has, until a token is imported.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

const (
	ghBinary = "gh"
	// publicHost is the host whose repos gh addresses as bare OWNER/REPO;
	// any other host (GitHub Enterprise) needs HOST/OWNER/REPO.
	publicHost = "github.com"
	// statusFields is what Status asks `gh pr view --json` for.
	statusFields = "state,isDraft,mergedAt,reviewDecision,statusCheckRollup,url"
)

// ErrNoURL is returned when `gh pr create` exits 0 but prints no PR URL.
var ErrNoURL = errors.New("gh pr create printed no pull-request URL")

// ErrInvalidRef is repoprovider.ErrInvalidRef, kept here so callers that
// matched the GitHub provider's sentinel keep matching after the push moved
// into the shared repoprovider.GitPush.
var ErrInvalidRef = repoprovider.ErrInvalidRef

// Provider is the GitHub implementation of repoprovider.Provider.
type Provider struct {
	exec repoprovider.Exec
	env  func(host string) []string
	now  func() time.Time
}

var _ repoprovider.Provider = (*Provider)(nil)

// New builds a Provider over ex. env yields the per-host env delta for every
// credentialed call (production: credstore.Env); nil means "no delta".
func New(ex repoprovider.Exec, env func(host string) []string) *Provider {
	if env == nil {
		env = func(string) []string { return nil }
	}
	return &Provider{exec: ex, env: env, now: time.Now}
}

// Kind implements repoprovider.Provider.
func (p *Provider) Kind() repoprovider.Kind { return repoprovider.KindGitHub }

// Terms implements repoprovider.Provider.
func (p *Provider) Terms() repoprovider.Terms { return repoprovider.TermsFor(repoprovider.KindGitHub) }

// netCtx gives a network-bound call NetTimeout unless the caller set a deadline.
func netCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return repoprovider.NetCtx(ctx)
}

func hasToken(env []string) bool {
	for _, kv := range env {
		for _, k := range []string{credstore.GitHubTokenKey, credstore.GitHubEnterpriseTokenKey} {
			if v, ok := strings.CutPrefix(kv, k+"="); ok && v != "" {
				return true
			}
		}
	}
	return false
}

// callEnv is the env delta for a credentialed call to host: the injected env
// when it carries a GitHub token, else nil — the operator's own gh login, which
// is how board land has always run. An isolated GH_CONFIG_DIR without a token
// would only turn a working CLI login into a not-authenticated failure.
func (p *Provider) callEnv(host string) []string {
	if env := p.env(host); hasToken(env) {
		return env
	}
	return nil
}

// AuthStatus implements repoprovider.Provider.
//
//   - gh not on PATH                          ⇒ unknown / none
//   - store token present, `gh api user` ok   ⇒ ok / store (with login)
//   - store token present, 401                ⇒ expired / store
//   - no store, operator's `gh auth token` ok ⇒ ok / cli (an import candidate),
//     or expired / cli when that token is rejected
//   - no store, no CLI login                  ⇒ missing / none
//
// The error is non-nil only for an unclassified failure (status unknown); it
// is redacted.
func (p *Provider) AuthStatus(ctx context.Context, host string) (repoprovider.AuthStatus, error) {
	if err := p.exec.Look(ghBinary); err != nil {
		return repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: repoprovider.SourceNone}, nil
	}
	ctx, cancel := netCtx(ctx)
	defer cancel()

	env := p.callEnv(host)
	source := repoprovider.SourceStore
	if env == nil {
		// No daemon-owned token: does the operator's own gh login have one?
		if _, _, err := p.exec.Run(ctx, "", nil, ghBinary, "auth", "token", "--hostname", host); err != nil {
			return repoprovider.AuthStatus{Status: repoprovider.AuthMissing, Source: repoprovider.SourceNone}, nil
		}
		source = repoprovider.SourceCLI
	}
	stdout, stderr, err := p.exec.Run(ctx, "", env, ghBinary, "api", "user", "--hostname", host)
	if err != nil {
		cerr := repoprovider.Classify(stderr, err)
		if errors.Is(cerr, repoprovider.ErrNotAuthenticated) {
			return repoprovider.AuthStatus{Status: repoprovider.AuthExpired, Source: source}, nil
		}
		return repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: source}, cerr
	}
	var user struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal([]byte(stdout), &user) // a missing login is not an auth failure
	return repoprovider.AuthStatus{Status: repoprovider.AuthOK, Login: user.Login, Source: source}, nil
}

// Push implements repoprovider.Provider through the shared
// repoprovider.GitPush: `git push -u <remote> <branch>`, never forced, a
// smuggled refspec refused before git runs.
func (p *Provider) Push(ctx context.Context, t repoprovider.Target, branch string) error {
	return repoprovider.GitPush(ctx, p.exec, p.callEnv(t.Remote.Host), t, branch)
}

// repoArg is the --repo value: OWNER/REPO on github.com, HOST/OWNER/REPO on an
// Enterprise host; "" when the remote was not parsed.
func repoArg(r repoprovider.Remote) string {
	if r.Owner == "" || r.Repo == "" {
		return ""
	}
	if r.Host == "" || r.Host == publicHost {
		return r.Slug()
	}
	return r.Host + "/" + r.Slug()
}

// OpenChangeRequest implements repoprovider.Provider: `gh pr create --head …
// --title … --body … [--base …] [--repo …] [--draft]`. The argument order keeps
// the "pr create --head <branch>" prefix the board land tests match on.
func (p *Provider) OpenChangeRequest(ctx context.Context, t repoprovider.Target, req repoprovider.ChangeRequest) (repoprovider.ChangeRef, error) {
	if err := p.exec.Look(ghBinary); err != nil {
		return repoprovider.ChangeRef{}, repoprovider.Classify("", err)
	}
	if !repoprovider.ValidRef(req.Head) || (req.Base != "" && !repoprovider.ValidRef(req.Base)) {
		return repoprovider.ChangeRef{}, fmt.Errorf("%w: head %q base %q", ErrInvalidRef, req.Head, req.Base)
	}
	args := []string{"pr", "create", "--head", req.Head, "--title", req.Title, "--body", req.Body}
	if req.Base != "" {
		args = append(args, "--base", req.Base)
	}
	if r := repoArg(t.Remote); r != "" {
		args = append(args, "--repo", r)
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	ctx, cancel := netCtx(ctx)
	defer cancel()
	stdout, stderr, err := p.exec.Run(ctx, t.RepoDir, p.callEnv(t.Remote.Host), ghBinary, args...)
	if err != nil {
		return repoprovider.ChangeRef{}, repoprovider.Classify(stderr, err)
	}
	// gh narrates on stderr and prints the URL on stdout; which stream carries
	// what has changed across versions, so both are scanned.
	url := repoprovider.FirstURL(stdout)
	if url == "" {
		url = repoprovider.FirstURL(stderr)
	}
	if url == "" {
		return repoprovider.ChangeRef{}, fmt.Errorf("%w: %s", ErrNoURL,
			repoprovider.RedactedTail(stdout+"\n"+stderr, nil))
	}
	return repoprovider.ChangeRef{URL: url, Number: prNumber(url), Provider: repoprovider.KindGitHub}, nil
}

// prNumber is the N of …/pull/N, or 0.
func prNumber(url string) int {
	_, rest, ok := strings.Cut(url, "/pull/")
	if !ok {
		return 0
	}
	rest, _, _ = strings.Cut(rest, "/")
	rest, _, _ = strings.Cut(rest, "#")
	rest, _, _ = strings.Cut(rest, "?")
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// prView is the `gh pr view --json statusFields` shape.
type prView struct {
	State             string    `json:"state"`
	IsDraft           bool      `json:"isDraft"`
	MergedAt          string    `json:"mergedAt"`
	ReviewDecision    string    `json:"reviewDecision"`
	StatusCheckRollup []ghCheck `json:"statusCheckRollup"`
	URL               string    `json:"url"`
}

// ghCheck is one rollup entry: a CheckRun (status + conclusion) or a commit
// StatusContext (state).
type ghCheck struct {
	Typename   string `json:"__typename"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// Status implements repoprovider.Provider: `gh pr view <n> --repo … --json …`.
func (p *Provider) Status(ctx context.Context, t repoprovider.Target, ref repoprovider.ChangeRef) (repoprovider.ChangeStatus, error) {
	if err := p.exec.Look(ghBinary); err != nil {
		return repoprovider.ChangeStatus{}, repoprovider.Classify("", err)
	}
	sel := ref.URL
	if ref.Number > 0 {
		sel = strconv.Itoa(ref.Number)
	}
	if sel == "" {
		return repoprovider.ChangeStatus{}, errors.New("github: change reference has neither a number nor a URL")
	}
	args := []string{"pr", "view", sel}
	if r := repoArg(t.Remote); r != "" {
		args = append(args, "--repo", r)
	}
	args = append(args, "--json", statusFields)
	ctx, cancel := netCtx(ctx)
	defer cancel()
	stdout, stderr, err := p.exec.Run(ctx, t.RepoDir, p.callEnv(t.Remote.Host), ghBinary, args...)
	if err != nil {
		return repoprovider.ChangeStatus{}, repoprovider.Classify(stderr, err)
	}
	var v prView
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		return repoprovider.ChangeStatus{}, fmt.Errorf("github: unreadable gh pr view output: %s",
			repoprovider.RedactedTail(stdout, err))
	}
	return repoprovider.ChangeStatus{
		State:     prState(v),
		Draft:     v.IsDraft,
		CI:        ciSummary(v.StatusCheckRollup),
		Review:    reviewSummary(v.ReviewDecision),
		CheckedAt: p.now().UTC(),
	}, nil
}

func prState(v prView) string {
	if v.MergedAt != "" {
		return repoprovider.StateMerged
	}
	switch strings.ToUpper(v.State) {
	case "MERGED":
		return repoprovider.StateMerged
	case "CLOSED":
		return repoprovider.StateClosed
	default:
		return repoprovider.StateOpen
	}
}

// reviewSummary maps gh's reviewDecision (APPROVED | CHANGES_REQUESTED |
// REVIEW_REQUIRED | "" when the repo has no review policy) onto the neutral
// review values; "" and anything unrecognised ⇒ none.
func reviewSummary(decision string) string {
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case "APPROVED":
		return repoprovider.ReviewApproved
	case "CHANGES_REQUESTED":
		return repoprovider.ReviewChangesRequested
	case "REVIEW_REQUIRED":
		return repoprovider.ReviewRequired
	default:
		return repoprovider.ReviewNone
	}
}

// ciSummary folds the rollup into the neutral CI value: any failure ⇒
// failure; else any unfinished check ⇒ pending; else (every check
// SUCCESS|SKIPPED|NEUTRAL) ⇒ success; an empty rollup ⇒ none.
func ciSummary(checks []ghCheck) string {
	if len(checks) == 0 {
		return repoprovider.CINone
	}
	failure, pending := false, false
	for _, c := range checks {
		switch checkOutcome(c) {
		case repoprovider.CIFailure:
			failure = true
		case repoprovider.CIPending:
			pending = true
		}
	}
	switch {
	case failure:
		return repoprovider.CIFailure
	case pending:
		return repoprovider.CIPending
	default:
		return repoprovider.CISuccess
	}
}

// checkOutcome is one rollup entry's CI value. The rollup mixes two shapes:
//
//   - a commit StatusContext carries only `state`
//     (SUCCESS | FAILURE | ERROR | PENDING | EXPECTED);
//   - a CheckRun carries `status` (QUEUED | IN_PROGRESS | WAITING | PENDING |
//     REQUESTED | COMPLETED) and, once COMPLETED, `conclusion` (SUCCESS |
//     NEUTRAL | SKIPPED | FAILURE | CANCELLED | TIMED_OUT | ACTION_REQUIRED |
//     STARTUP_FAILURE | STALE).
//
// An unrecognised StatusContext state reads as pending (it may still turn
// green); an unrecognised conclusion on a COMPLETED run reads as failure (it
// finished, and not green).
func checkOutcome(c ghCheck) string {
	if c.Typename == "StatusContext" || (c.Status == "" && c.Conclusion == "" && c.State != "") {
		switch strings.ToUpper(c.State) {
		case "SUCCESS":
			return repoprovider.CISuccess
		case "FAILURE", "ERROR":
			return repoprovider.CIFailure
		default: // PENDING, EXPECTED
			return repoprovider.CIPending
		}
	}
	// A run that has not COMPLETED is pending whatever conclusion it carries (a
	// re-run may still show the previous one).
	if s := strings.ToUpper(c.Status); s != "" && s != "COMPLETED" {
		return repoprovider.CIPending
	}
	switch strings.ToUpper(c.Conclusion) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return repoprovider.CISuccess
	case "":
		return repoprovider.CIPending // COMPLETED (or status-less) without a conclusion yet
	default: // FAILURE, CANCELLED, TIMED_OUT, ACTION_REQUIRED, STARTUP_FAILURE, STALE
		return repoprovider.CIFailure
	}
}
