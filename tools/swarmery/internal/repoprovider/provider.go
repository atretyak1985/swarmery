// Package repoprovider is the vendor-neutral boundary between the daemon and a
// code host: pushing a branch, opening a change request (a GitHub pull request,
// a GitLab merge request), reading its status, and asking whether the daemon is
// authenticated to do any of it.
//
// # Import rule (leaf-level)
//
// This package and its subpackages import ONLY the standard library,
// internal/claudeacct (for SecretsDir) and internal/pluginreq (for the project
// config). Never internal/api, never the store, never a DB handle: callers
// (board land, phase landing, the status poller) depend on repoprovider, not the
// other way round.
//
// Within the tree the direction is fixed too:
//
//	repoprovider/credstore  ← leaf: stdlib + claudeacct only
//	repoprovider            → credstore (Redact)
//	repoprovider/github     → repoprovider, credstore
//	repoprovider/gitlab     → repoprovider, credstore
//	repoprovider/providers  → repoprovider, github, gitlab (Factory)
//	repoprovider/landpoll   → repoprovider, credstore, database/sql
//
// landpoll (the PR/MR status poller) is the one subpackage that holds a DB
// handle: it reads and stamps epic_phases' landing columns. It still never
// imports internal/api or the store package — the daemon hands it the *sql.DB,
// the provider factory and its api-side hooks (publish, auth-expired) as funcs.
//
// credstore must never import repoprovider (it would close a cycle through
// Redact); it declares its own Runner interface, which Exec satisfies.
//
// # Process boundary
//
// Every `git`, `gh` and `glab` invocation goes through Exec, so tests script a
// FakeExec instead of reaching a network or a real account. Nothing in this
// package ever passes `--force` to a push.
//
// # Redaction
//
// Every string that leaves this package inside an error passes
// credstore.Redact (see Classify), so a token echoed by a CLI never reaches a
// log line or an HTTP body unmasked.
package repoprovider

import (
	"context"
	"time"
)

// Kind names a code-host family.
type Kind string

const (
	KindGitHub  Kind = "github"
	KindGitLab  Kind = "gitlab"
	KindUnknown Kind = "unknown"
)

// Terms is the vocabulary a UI uses for a provider, so no surface ever
// branches on Kind to pick a label (SC-11).
type Terms struct {
	Provider    string `json:"provider"`    // "GitHub"
	Change      string `json:"change"`      // "Pull Request"
	ChangeShort string `json:"changeShort"` // "PR"
}

// TermsFor is the vocabulary for k. An unrecognised kind gets the neutral
// terms, never an empty label.
func TermsFor(k Kind) Terms {
	switch k {
	case KindGitHub:
		return Terms{Provider: "GitHub", Change: "Pull Request", ChangeShort: "PR"}
	case KindGitLab:
		return Terms{Provider: "GitLab", Change: "Merge Request", ChangeShort: "MR"}
	default:
		return Terms{Provider: "Repository", Change: "Change request", ChangeShort: "CR"}
	}
}

// Remote is a parsed remote URL.
type Remote struct {
	URL      string `json:"url"`
	Host     string `json:"host"`
	Owner    string `json:"owner"` // may contain "/" for GitLab subgroups
	Repo     string `json:"repo"`
	Protocol string `json:"protocol"` // "https" | "ssh"
}

// Slug is "owner/repo".
func (r Remote) Slug() string { return r.Owner + "/" + r.Repo }

// Auth statuses and sources.
const (
	AuthOK      = "ok"
	AuthMissing = "missing"
	AuthExpired = "expired"
	AuthUnknown = "unknown"

	SourceCLI   = "cli"
	SourceStore = "store"
	SourceNone  = "none"
)

// AuthStatus is whether the daemon can act on a host, and with whose
// credentials: the daemon's own store, the operator's CLI login (to be
// imported), or nothing.
type AuthStatus struct {
	Status string `json:"status"` // ok | missing | expired | unknown
	Login  string `json:"login,omitempty"`
	Source string `json:"source"` // cli | store | none
}

// Target is where a provider operation runs: the repo checkout, the git remote
// name, and that remote's parsed URL.
type Target struct {
	RepoDir    string
	RemoteName string
	Remote     Remote
}

// ChangeRequest is what OpenChangeRequest opens.
type ChangeRequest struct {
	Head  string
	Base  string // "" = the host's default branch
	Title string
	Body  string
	Draft bool
}

// ChangeRef identifies an opened change request.
type ChangeRef struct {
	URL      string `json:"url"`
	Number   int    `json:"number"`
	Provider Kind   `json:"provider"`
}

// Change-request states, CI and review summaries (provider-neutral). These are
// the ONLY values a Provider's Status may emit; they are stored verbatim in
// epic_phases.pr_status and rendered by the Plans UI, so they are wire values.
const (
	// State: the change request's lifecycle. A draft is State open + Draft true.
	StateOpen   = "open"
	StateClosed = "closed" // closed without merging
	StateMerged = "merged"

	// CI: the fold of every check on the change request's head commit.
	CISuccess = "success" // every check finished green (skipped/neutral count as green)
	CIFailure = "failure" // at least one check failed, errored, was cancelled or timed out
	CIPending = "pending" // nothing failed, but at least one check has not finished
	CINone    = "none"    // the change request has no checks at all

	// Review: the host's review verdict.
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewRequired         = "review_required" // a review is required and not yet given
	ReviewNone             = "none"            // no review policy / no verdict
)

// ChangeStatus is a change request's state as last read from the host. Every
// string field carries exactly one of the constants above.
type ChangeStatus struct {
	State     string    `json:"state"`     // open | merged | closed
	Draft     bool      `json:"draft"`     // the host marks it a draft / WIP
	CI        string    `json:"ci"`        // success | failure | pending | none
	Review    string    `json:"review"`    // approved | changes_requested | review_required | none
	CheckedAt time.Time `json:"checkedAt"` // when it was read (UTC)
}

// Provider is one code host's implementation.
type Provider interface {
	Kind() Kind
	Terms() Terms
	// AuthStatus reports whether the daemon is authenticated to host.
	AuthStatus(ctx context.Context, host string) (AuthStatus, error)
	// Push publishes branch to t.RemoteName with upstream tracking. Never forced.
	Push(ctx context.Context, t Target, branch string) error
	// OpenChangeRequest opens a PR/MR and returns its reference.
	OpenChangeRequest(ctx context.Context, t Target, req ChangeRequest) (ChangeRef, error)
	// Status reads a change request's current state.
	Status(ctx context.Context, t Target, ref ChangeRef) (ChangeStatus, error)
}
