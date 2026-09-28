// Package usage resolves the operator's local Claude OAuth credential and reads
// their live Anthropic subscription quotas — the 5-hour session window, the
// weekly window, and the per-model weekly windows — so the dashboard can show
// the real reading instead of a self-estimate.
//
// # Policy note — supersedes the 2026-06 OAuth spike
//
// internal/api/usage.go opens with a spike note declaring this OAuth path
// "credential exfiltration + a fragile private-API dependency, out of policy".
// That decision is deliberately reversed here, and the reasoning is recorded so
// the record stays honest:
//
//   - The daemon reads the operator's OWN credentials on the operator's OWN
//     machine, from the same store the `claude` CLI itself wrote
//     (~/.claude/.credentials.json, or the macOS Keychain item
//     "Claude Code-credentials"). Nothing is harvested from anyone else.
//     On a multi-subscription machine that extends to one credential per
//     ACCOUNT — <configDir>/.credentials.json for each config dir the operator
//     already logs in under (usage.Source, LoadCredsFor) — which is the same
//     property one account at a time: still the operator's own logins, still
//     read-only, still never written back. A scoped lookup is exclusive
//     precisely so one account's quota can never be published under another's
//     name. Where the CLI leaves nothing readable — a non-default account on
//     macOS, whose credential sits in the login Keychain under an undocumented
//     per-config-dir name this package refuses to depend on — the operator can
//     instead authorize the DAEMON itself, once, in their browser (login.go);
//     that is still the operator's own login, granted deliberately, to a
//     credential store swarmery owns rather than one it borrows.
//   - The bearer token is sent ONLY to Anthropic's own API — the exact call
//     `claude /usage` makes. It is never persisted to SQLite, never logged, and
//     never included in any HTTP response body served by the daemon.
//     Creds.String redacts so an accidental %v is safe, and upstream error
//     bodies are scrubbed of bearer material and truncated before they can
//     reach Provider.Error.
//   - Writing a credential back is decided by PROVENANCE, and there are exactly
//     two cases. A credential the `claude` CLI owns (a config-dir file, the
//     macOS keychain item) is NEVER written back — the CLI is the other writer,
//     and a refresh token we rotated behind its back can strand the operator's
//     login; a refreshed token for those lives in memory for the daemon's
//     lifetime and nowhere else. A credential the operator authorized THROUGH
//     THE DASHBOARD (login.go's PKCE flow) belongs to swarmery alone and lives
//     in swarmery's own store, ~/.swarmery/credentials/<account>.json at 0600
//     under a 0700 directory (store.go); there a rotated refresh token MUST be
//     persisted, atomically, or the connection dies at the next restart. The
//     CLI's own stores are never written under either rule.
//   - The endpoint is undocumented and may break. That is accepted and
//     contained: Client.Fetch never returns an error — every failure mode
//     degrades to a visible per-provider error card, never to a crash and never
//     to a fabricated number. Every parse is tolerant (multiple key fallbacks
//     plus a generic limits[] walk) so a field rename degrades one row, not the
//     endpoint.
//   - SWARMERY_USAGE_OAUTH=0 is an explicit opt-out covering the WHOLE OAuth
//     surface, read and write: LoadCreds returns ErrDisabled without touching
//     the filesystem or the keychain at all, and StartLogin/CompleteLogin
//     refuse before any URL is built or any code is exchanged. A per-account
//     `"disabled": true` in a store file parks one connection the same way,
//     without deleting its credential.
//
// Fusion's node-pty fallback (driving the `claude` TUI and scraping its /usage
// screen when the API path fails) is deliberately NOT ported: Go has no
// equivalent in this tree, and a 60-second PTY spawn per refresh is the wrong
// trade for a dashboard poll. Loss of the OAuth path degrades to an error card,
// not to a scrape.
package usage

// Provider status values. Fetch encodes every outcome in Provider.Status rather
// than returning an error, so one broken provider can never break the endpoint.
const (
	StatusOK     = "ok"
	StatusError  = "error"
	StatusNoAuth = "no-auth"
)

// Window/Provider source values. "oauth" is a real reading from Anthropic;
// "estimate" is the telemetry-derived secondary card owned by internal/api.
const (
	SourceOAuth    = "oauth"
	SourceEstimate = "estimate"
)

// ConnectedViaSwarmery is the only value Provider.ConnectedVia takes today: the
// card's credential came from SWARMERY'S OWN store (store.go), i.e. the operator
// authorized this daemon through the dashboard rather than through the `claude`
// CLI. A string rather than a bool because provenance is a question with more
// than two possible answers — naming the store leaves room for a second one
// without renaming the field the UI already reads.
const ConnectedViaSwarmery = "swarmery"

// Pace status values. Note the vocabulary, kept verbatim from Fusion so the
// operator-visible strings match the reference: "ahead" means burning FASTER
// than a linear burn of the window (rendered as a warning), "behind" means
// under pace (rendered positively). The words read backwards at first pass.
const (
	PaceAhead   = "ahead"
	PaceOnTrack = "on-track"
	PaceBehind  = "behind"
)

// Pace compares consumption against elapsed time inside a window, in
// percentage points (Fusion semantics: percentUsed - percentElapsed).
type Pace struct {
	Status         string  `json:"status"` // "ahead" | "on-track" | "behind"
	PercentElapsed float64 `json:"percentElapsed"`
	Message        string  `json:"message"`
}

// Window is one quota window (session, weekly, per-model weekly, or an
// operator-configured telemetry estimate).
type Window struct {
	Key         string  `json:"key"`   // stable id for React keys + hide prefs
	Label       string  `json:"label"` // "Session (5h)", "Weekly", "Weekly (Fable)"
	PercentUsed float64 `json:"percentUsed"`
	PercentLeft float64 `json:"percentLeft"`
	ResetText   string  `json:"resetText,omitempty"` // "resets in 3h 30m"
	ResetMs     int64   `json:"resetMs,omitempty"`   // ms until reset
	ResetAt     string  `json:"resetAt,omitempty"`   // RFC3339
	WindowMs    int64   `json:"windowDurationMs,omitempty"`
	Pace        *Pace   `json:"pace,omitempty"`
	Source      string  `json:"source"`          // "oauth" | "estimate"
	Used        int64   `json:"used,omitempty"`  // estimate provider only
	Limit       int64   `json:"limit,omitempty"` // estimate provider only
}

// Hint kinds. A hint marks a failure the operator can fix themselves on this
// machine, as opposed to a broken or unreachable provider.
const (
	HintLogin    = "login"     // no credential, expired, refresh failed, rejected
	HintScope    = "scope"     // credential lacks the user:profile scope
	HintOptedOut = "opted-out" // SWARMERY_USAGE_OAUTH=0
)

// Hint is operator-facing setup guidance attached to a provider whose quota
// could not be read because of LOCAL configuration — no `claude` login, an
// expired token, a missing scope, an explicit opt-out.
//
// It exists so the card can answer the four questions a bare error line leaves
// open: what is missing (Title/Detail), how to supply it (Command), where the
// daemon looks for it (Sources), and why it is needed and how it is handled
// (Why/Handling). A provider carrying a Hint is StatusNoAuth, never
// StatusError: nothing is broken, something is simply not connected yet.
type Hint struct {
	Kind     string   `json:"kind"`              // "login" | "scope" | "opted-out"
	Title    string   `json:"title"`             // "Claude login required"
	Detail   string   `json:"detail"`            // what is missing, one sentence
	Command  string   `json:"command,omitempty"` // exact command to run
	Sources  []string `json:"sources,omitempty"` // where the credential is read from
	Why      string   `json:"why"`               // what supplying it unlocks
	Handling string   `json:"handling"`          // how the credential is used
}

// Provider is one card in the Usage modal.
type Provider struct {
	// Account is the account key the card belongs to ("default", "nabu-org", …
	// — ingest.AccountFor's vocabulary). Name alone is NOT unique once the
	// daemon reads more than one account: every account contributes a card
	// called "Claude", so the UI's card identity is `account:name` (React keys,
	// hidden-window preferences).
	Account string   `json:"account"`
	Name    string   `json:"name"`   // "Claude"
	Status  string   `json:"status"` // "ok" | "error" | "no-auth"
	Error   string   `json:"error,omitempty"`
	Plan    string   `json:"plan,omitempty"` // "Max" | "Pro" | "Team"
	Source  string   `json:"source"`         // "oauth" | "estimate"
	Windows []Window `json:"windows"`
	// ConnectedVia names the store this card's credential came from, but ONLY
	// when that store is swarmery's own — the single value ConnectedViaSwarmery.
	// Absent means the credential came from one of the `claude` CLI's sources,
	// or that there is no credential at all.
	//
	// It is provenance, never material: not a token, not a path, nothing derived
	// from the credential's content. The UI needs it for two decisions it cannot
	// otherwise make honestly — which cards may offer a Disconnect (only the
	// connections swarmery owns are swarmery's to delete), and whether a FAILING
	// card should offer to re-authorize here instead of printing a `claude`
	// command that writes to a store this credential never came from.
	//
	// Set as soon as the credential resolves, so a card that then fails to read
	// its quota still carries it.
	ConnectedVia string `json:"connectedVia,omitempty"`
	// Hint is set only for a local-setup failure (StatusNoAuth); the UI renders
	// it instead of the raw Error line.
	Hint *Hint `json:"hint,omitempty"`
	// TokenExpired is set only by a NoRefresh client that found the stored
	// token expired and did not refresh it. In-process only — never served.
	TokenExpired bool `json:"-"`
}
