package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Endpoint constants, copied from Fusion (usage.ts:349-369) with the exact
// values. These are the same calls `claude /usage` makes.
const (
	oauthUsagePath = "/api/oauth/usage"
	tokenPath      = "/v1/oauth/token"
	// oauthClientID is the public first-party OAuth client id, required as
	// client_id on the refresh_token grant.
	oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// anthropicBeta authorizes OAuth-scoped access to /api/oauth/usage.
	// Without this header the endpoint answers 401 "OAuth authentication is
	// currently not supported".
	anthropicBeta    = "oauth-2025-04-20"
	userAgent        = "swarmery-dashboard"
	maxRetries       = 3
	initialRetryWait = time.Second
)

const (
	defaultAPIBase   = "https://api.anthropic.com"
	defaultAuthBase  = "https://platform.claude.com"
	defaultTimeout   = 15 * time.Second
	requiredScope    = "user:profile"
	tokenExpiryGrace = 60 * time.Second
	maxBodyBytes     = 1 << 20 // cap an unbounded upstream body
	snippetRunes     = 120     // error-body snippet budget
	providerName     = "Claude"

	fiveHours = 5 * time.Hour
	sevenDays = 7 * 24 * time.Hour
)

// Client fetches the operator's Claude subscription usage. The zero value is
// usable; every field is a default-or-seam so the whole flow is testable
// against an httptest server with no network and no real credential.
//
// A Client carries a mutex and must not be copied after first use.
type Client struct {
	HTTP     *http.Client     // default: 15s timeout
	Now      func() time.Time // test seam
	APIBase  string           // default "https://api.anthropic.com"
	AuthBase string           // default "https://platform.claude.com"
	// LoginBase hosts the OAuth AUTHORIZE page (login.go) — a third host,
	// default "https://claude.com". Only the URL is built here; the daemon never
	// fetches it, the operator's browser does.
	LoginBase string
	LoadCreds func(context.Context) (*Creds, error) // test seam

	// Src is the account this client speaks for. The zero value is the default
	// account over the legacy credential chain — i.e. single-account behaviour,
	// unchanged. One Client per account is the multi-account unit, because the
	// refreshed-token cache below is per credential and must not be shared.
	Src Source

	// NoRefresh makes the client strictly read-only towards the OAuth token
	// endpoint: an expired stored token is reported (Provider.TokenExpired)
	// instead of refreshed, and a 401/403 is not retried with a refresh. A
	// background poller sets it — a refresh there rotates the refresh token
	// behind the CLI's back for no operator-visible reason. The zero value (the
	// dashboard's clients) keeps the refresh behaviour.
	NoRefresh bool

	// sleep is the retry-backoff seam; tests replace it so a 429 path costs no
	// wall-clock time. nil means time.Sleep.
	sleep func(time.Duration)

	mu             sync.Mutex
	refreshedToken string // in-memory only, daemon lifetime
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: defaultTimeout}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return defaultAPIBase
}

func (c *Client) authBase() string {
	if c.AuthBase != "" {
		return strings.TrimRight(c.AuthBase, "/")
	}
	return defaultAuthBase
}

func (c *Client) loadCreds(ctx context.Context) (*Creds, error) {
	if c.LoadCreds != nil {
		return c.LoadCreds(ctx)
	}
	return LoadCredsFor(ctx, c.Src)
}

func (c *Client) wait(d time.Duration) {
	if c.sleep != nil {
		c.sleep(d)
		return
	}
	time.Sleep(d)
}

func (c *Client) cachedToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshedToken
}

func (c *Client) cacheToken(tok string) {
	c.mu.Lock()
	c.refreshedToken = tok
	c.mu.Unlock()
}

// ── setup hints ────────────────────────────────────────────────────────────
//
// A local-setup failure — no `claude` login, an expired or rejected token, a
// missing scope, an explicit opt-out — is not a broken provider: the operator
// fixes it in one step. Those outcomes carry a Hint (and stay StatusNoAuth) so
// the card explains what is missing, where the credential is read from, why it
// is needed and how it is handled, instead of one red error line the operator
// has to decode. Genuine upstream trouble — 429, a non-200, an unparseable body,
// a transport failure — stays StatusError with no hint.

const (
	hintWhy = "Reads your live Claude quota — the 5-hour session window and the weekly windows, the same numbers `claude /usage` shows. Only this card depends on it."
	// hintHandling states the package's standing policy in operator-facing
	// words. Keep it in sync with the policy note in types.go.
	hintHandling = "Read from your own machine, sent only to Anthropic's API, refreshed in memory — never written back, logged, or returned by the dashboard."
	// reconnectDetail replaces the CLI remedy for a credential that came from
	// swarmery's own store. Without it the card would name a command that writes
	// somewhere this credential never came from, and the operator would run
	// `claude`, see nothing change, and have no idea why.
	reconnectDetail = "This account is connected through swarmery, not through the `claude` CLI, so a CLI login cannot repair it — reconnect the account from this card to authorize it again."
)

// loginHint is the "(re-)connect this account" hint every credential-shaped
// failure shares; kind and wording differ, the remedy does not. It is a method
// because both the remedy and the "looked in …" list are per ACCOUNT.
//
// The REMEDY is decided by provenance, the same rule that decides write-back. A
// credential the `claude` CLI owns is fixed by re-running that login, so the
// hint carries the exact command. A credential from SWARMERY'S OWN store is not:
// the CLI does not write there, so the command would be a dead end. Those hints
// carry NO command and point at the card's own Reconnect action, which is the
// one thing that actually replaces the file (login.go's CompleteLogin).
//
// creds is the credential the failure was about, and is nil when none resolved
// at all — the plain CLI case.
func (c *Client) loginHint(creds *Creds, kind, title, detail string) *Hint {
	h := &Hint{
		Kind:     kind,
		Title:    title,
		Detail:   detail,
		Command:  c.loginCommand(),
		Sources:  CredentialSourcesFor(c.Src),
		Why:      hintWhy,
		Handling: hintHandling,
	}
	if creds != nil && creds.FromStore {
		h.Command = ""
		h.Detail = detail + " " + reconnectDetail
	}
	return h
}

// loginCommand is the login THIS account needs. A scoped account is logged in
// with its own config dir in the environment; a bare `claude` would re-login the
// default account instead and leave the card exactly as it was.
func (c *Client) loginCommand() string {
	if c.Src.ConfigDir != "" {
		return configDirEnv + "=" + c.Src.ConfigDir + " claude"
	}
	return "claude"
}

// fail is an outcome that must reach the operator: the one-line message, plus
// the hint when the cause is local setup rather than a broken provider.
type fail struct {
	msg  string
	hint *Hint
	// tokenExpired marks the NoRefresh outcome: the stored token is expired
	// and this client will not refresh it.
	tokenExpired bool
}

// apply encodes a failure on the provider. A hinted failure is "not connected
// yet" (StatusNoAuth), everything else is a real error.
func (p *Provider) apply(f *fail) {
	p.Error = f.msg
	p.TokenExpired = f.tokenExpired
	if f.hint != nil {
		p.Status, p.Hint = StatusNoAuth, f.hint
		return
	}
	p.Status = StatusError
}

// Fetch reads the operator's live Claude quota windows.
//
// It never returns an error: every outcome — opted out, not logged in, auth
// rejected, rate limited, malformed payload — is encoded in the returned
// Provider's Status, Error and Hint, so a broken provider degrades to one card
// and can never break the endpoint.
func (c *Client) Fetch(ctx context.Context) Provider {
	p := Provider{
		Account: c.Src.Account,
		Name:    providerName,
		Status:  StatusNoAuth,
		Source:  SourceOAuth,
		Windows: []Window{},
	}

	creds, err := c.loadCreds(ctx)
	switch {
	case errors.Is(err, ErrDisabled):
		p.Error = "usage OAuth disabled (SWARMERY_USAGE_OAUTH=0)"
		p.Hint = &Hint{
			Kind:     HintOptedOut,
			Title:    "Live usage is switched off",
			Detail:   "SWARMERY_USAGE_OAUTH=0 disables the credential read entirely — nothing on disk or in the keychain is touched.",
			Command:  "unset SWARMERY_USAGE_OAUTH",
			Why:      hintWhy,
			Handling: hintHandling,
		}
		return p
	case err != nil, creds == nil, creds.AccessToken == "":
		p.Error = "No Claude credentials — run `claude` to log in"
		p.Hint = c.loginHint(creds, HintLogin, "Claude login required",
			"No Claude credential was found on this machine, so the live quota cannot be read.")
		return p
	}
	// Provenance is stamped BEFORE any failure path can return: a card whose
	// swarmery-owned credential has gone bad still has to be recognisable as
	// ours, or the UI cannot offer the only remedy that works for it.
	if creds.FromStore {
		p.ConnectedVia = ConnectedViaSwarmery
	}

	// An absent or empty Scopes list is treated as permissive: the CLI does not
	// always persist a scope list to ~/.claude/.credentials.json (observed with
	// only accessToken/refreshToken/expiresAt present), so requiring one here
	// would reject every genuinely logged-in operator. Only a POPULATED list
	// that lacks requiredScope is rejected up front; the 401 → refresh → error
	// path below remains the backstop for a token that is actually unauthorized.
	if len(creds.Scopes) > 0 && !hasScope(creds.Scopes, requiredScope) {
		p.Error = "Claude token missing user:profile scope — re-run `claude` login"
		p.Hint = c.loginHint(creds, HintScope, "Claude login is missing a permission",
			"The stored credential has no `user:profile` scope, which the quota endpoint requires. A fresh login grants it.")
		return p
	}
	p.Plan = inferPlan(creds)

	token, refreshed, f := c.resolveToken(ctx, creds)
	if f != nil {
		p.apply(f)
		return p
	}

	body, f := c.fetchUsage(ctx, creds, token, refreshed)
	if f != nil {
		p.apply(f)
		return p
	}

	windows, err := parsePayload(body, c.now())
	if err != nil {
		p.Status = StatusError
		p.Error = "Claude usage response was not valid JSON"
		return p
	}
	p.Status = StatusOK
	p.Windows = windows
	return p
}

// resolveToken picks the bearer to use: the in-memory refreshed token when we
// have one, otherwise the stored token — refreshing up front when the stored
// token is expired (or expires within the grace window). The bool reports
// whether the returned token came from a refresh, so the 401 path knows not to
// refresh the same token twice.
func (c *Client) resolveToken(ctx context.Context, creds *Creds) (token string, refreshed bool, f *fail) {
	if tok := c.cachedToken(); tok != "" {
		return tok, true, nil
	}
	if !c.tokenExpired(creds.ExpiresAt) {
		return creds.AccessToken, false, nil
	}
	if c.NoRefresh {
		return "", false, &fail{
			msg:          "Claude token expired — not refreshing it here; run `claude` to renew the login",
			tokenExpired: true,
			hint: c.loginHint(creds, HintLogin, "Claude login expired",
				"The stored token has expired, and this reader never refreshes a token itself."),
		}
	}
	if creds.RefreshToken == "" {
		return "", false, &fail{
			msg: "Claude token expired and no refresh token — run `claude` to re-login",
			hint: c.loginHint(creds, HintLogin, "Claude login expired",
				"The stored token has expired and carries no refresh token, so it cannot be renewed automatically."),
		}
	}
	tok, ok := c.refresh(ctx, creds)
	if !ok {
		return "", false, &fail{
			msg: "Claude token refresh failed — run `claude` to re-login",
			hint: c.loginHint(creds, HintLogin, "Claude login expired",
				"The stored token has expired and Anthropic declined to refresh it, so a fresh login is needed."),
		}
	}
	return tok, true, nil
}

// tokenExpired treats a token expiring within the grace window as expired. An
// unknown expiry (0) is assumed valid — the 401 path is the real backstop.
func (c *Client) tokenExpired(expiresAtMs int64) bool {
	if expiresAtMs <= 0 {
		return false
	}
	return c.now().UnixMilli() >= expiresAtMs-tokenExpiryGrace.Milliseconds()
}

// fetchUsage performs GET /api/oauth/usage with the retry/refresh policy:
// one refresh attempt on 401/403, up to maxRetries attempts on 429 honouring
// Retry-After (seconds) or exponential backoff from 1s, and immediate failure
// on any other non-200.
func (c *Client) fetchUsage(ctx context.Context, creds *Creds, token string, refreshed bool) (body []byte, f *fail) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		status, header, b, err := c.get(ctx, c.apiBase()+oauthUsagePath, token)
		if err != nil {
			return nil, &fail{msg: "Claude usage request failed: " + scrubSecrets(err.Error())}
		}

		switch {
		case status == http.StatusUnauthorized, status == http.StatusForbidden:
			if creds.RefreshToken != "" && !refreshed && !c.NoRefresh {
				if tok, ok := c.refresh(ctx, creds); ok {
					token, refreshed = tok, true
					continue
				}
			}
			return nil, c.authRejected(creds)

		case status == http.StatusTooManyRequests:
			if attempt < maxRetries-1 {
				c.wait(retryDelay(header.Get("Retry-After"), attempt))
				continue
			}
			return nil, &fail{msg: "Claude usage rate-limited (HTTP 429) — retry later"}

		case status != http.StatusOK:
			if s := snippet(b); s != "" {
				return nil, &fail{msg: fmt.Sprintf("HTTP %d: %s", status, s)}
			}
			return nil, &fail{msg: fmt.Sprintf("HTTP %d", status)}
		}
		return b, nil
	}
	// Only reachable when every attempt was consumed by a 401→refresh retry.
	return nil, c.authRejected(creds)
}

// authRejected reports a rejected bearer AND drops the in-memory refreshed
// token. Without the drop, a token that has gone stale inside the daemon's
// lifetime would be replayed on every subsequent poll — resolveToken prefers
// the cache and marks it `refreshed`, which suppresses the refresh retry — so
// following the hint (`claude` re-login) would appear to change nothing until
// the daemon restarted. Clearing it makes the next poll start from the
// on-disk credential again.
func (c *Client) authRejected(creds *Creds) *fail {
	c.cacheToken("")
	return &fail{
		msg: "Claude auth rejected — run `claude` to re-login",
		hint: c.loginHint(creds, HintLogin, "Claude login was rejected",
			"Anthropic rejected the stored credential, which usually means the login was revoked or superseded elsewhere."),
	}
}

// retryDelay honours a numeric Retry-After (seconds) when the server sends one,
// otherwise backs off exponentially from initialRetryWait.
func retryDelay(retryAfter string, attempt int) time.Duration {
	if s := strings.TrimSpace(retryAfter); s != "" {
		if secs, err := strconv.ParseFloat(s, 64); err == nil && secs >= 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	return initialRetryWait * time.Duration(1<<attempt)
}

// refresh exchanges the refresh token for a new access token and caches it in
// memory.
//
// # Write-back is decided by PROVENANCE, not by preference
//
// A credential read from the CLI's own store (a config-dir file, the keychain)
// is NEVER written back: the CLI is the other writer, and a rotated refresh
// token written by us behind its back can strand the operator's login (see the
// package policy note). Only the in-memory cache changes for those.
//
// A credential from SWARMERY'S OWN store (Creds.FromStore — the dashboard's
// Connect flow) is ours alone, and there the opposite is true: if the endpoint
// rotates the refresh token and we do not persist it, the old one is already
// dead upstream and the connection breaks at the next daemon restart. Those are
// written back atomically (store.go).
//
// The persist is best-effort: a store that cannot be written still leaves a
// working in-memory session for this daemon's lifetime, which beats failing the
// poll the operator is watching.
//
// The request shape mirrors what the CLI sends: JSON body (not form-encoded),
// including `scope`; omitting either makes Anthropic answer 4xx even for a
// valid refresh token.
//
// The scopes gate above is permissive for absent/empty Scopes, so a
// scope-less refresh IS reachable for credentials the `claude` CLI wrote
// without a `scopes` key; this payload legitimately omits `scope` here, and
// our stub accepts it (TestFetchRefreshesWithoutScopes) — but whether the
// LIVE Anthropic endpoint does is NOT yet verified. Do not hard-code a default.
func (c *Client) refresh(ctx context.Context, creds *Creds) (string, bool) {
	if creds.RefreshToken == "" {
		return "", false
	}
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": creds.RefreshToken,
		"client_id":     oauthClientID,
	}
	if len(creds.Scopes) > 0 {
		payload["scope"] = strings.Join(creds.Scopes, " ")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authBase()+tokenPath, bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("user-agent", userAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", false
	}

	var out struct {
		AccessToken      string `json:"access_token"`
		AccessTokenCamel string `json:"accessToken"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", false
	}
	tok := out.AccessToken
	if tok == "" {
		tok = out.AccessTokenCamel
	}
	if tok == "" {
		return "", false
	}
	c.cacheToken(tok)
	c.persistRefresh(creds, tok, out.RefreshToken, out.ExpiresIn)
	return tok, true
}

// persistRefresh writes a refreshed credential back to swarmery's own store, and
// ONLY there — see the provenance rule on refresh. rotated is the endpoint's new
// refresh token when it issued one; an empty value means the old one stands.
func (c *Client) persistRefresh(creds *Creds, access, rotated string, expiresIn int64) {
	if creds == nil || !creds.FromStore || c.Src.Account == "" {
		return
	}
	next := *creds
	next.AccessToken = access
	if rotated != "" {
		next.RefreshToken = rotated
	}
	if expiresIn > 0 {
		next.ExpiresAt = c.now().Add(time.Duration(expiresIn) * time.Second).UnixMilli()
	}
	// Ignored on purpose: an unwritable store must not fail the poll, and the
	// error text could carry a path we have no reason to surface.
	_ = writeStoredCreds(c.Src.Account, &next)
}

// get issues the authenticated usage request. The bearer is set here and
// nowhere else; it is never logged and never echoed into a returned value.
func (c *Client) get(ctx context.Context, url, token string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", anthropicBeta)
	req.Header.Set("user-agent", userAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// inferPlan derives the plan label from credential metadata: the title-cased
// subscriptionType when present, else a rateLimitTier keyword match
// (usage.ts:988). Empty when neither is set — never guessed.
func inferPlan(c *Creds) string {
	if s := strings.TrimSpace(c.SubscriptionType); s != "" {
		r := []rune(s)
		r[0] = unicode.ToUpper(r[0])
		return string(r)
	}
	tier := strings.TrimSpace(c.RateLimitTier)
	if tier == "" {
		return ""
	}
	switch lower := strings.ToLower(tier); {
	case strings.Contains(lower, "max"):
		return "Max"
	case strings.Contains(lower, "pro"):
		return "Pro"
	case strings.Contains(lower, "team"):
		return "Team"
	}
	return tier
}

// ── payload parsing ────────────────────────────────────────────────────────
//
// Anthropic ships several shapes for this undocumented endpoint, so every read
// is tolerant: multiple key spellings per field, three accepted timestamp
// encodings, and a generic limits[] walk for per-model weekly buckets. A shape
// we do not recognise costs one row, never the response.

var percentKeys = []string{"utilization", "percent_used", "percentUsed", "used_percent", "usage_percent"}

var resetKeys = []string{"resets_at", "reset_at", "resetAt", "resetsAt", "reset_time"}

// namedWindows are the fixed windows, in render order.
var namedWindows = []struct {
	keys   []string
	label  string
	window time.Duration
}{
	{[]string{"five_hour", "session"}, "Session (5h)", fiveHours},
	{[]string{"seven_day"}, "Weekly", sevenDays},
	{[]string{"seven_day_sonnet"}, "Weekly (Sonnet)", sevenDays},
	{[]string{"seven_day_opus"}, "Weekly (Opus)", sevenDays},
}

func parsePayload(body []byte, now time.Time) ([]Window, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}

	windows := make([]Window, 0, len(namedWindows)+2)
	for _, spec := range namedWindows {
		if w := parseNamedWindow(root, spec.keys, spec.label, spec.window, now); w != nil {
			windows = append(windows, *w)
		}
	}
	windows = append(windows, parseLimits(root["limits"], windows, now)...)

	for i := range windows {
		windows[i].Pace = CalculatePace(windows[i].PercentUsed, windows[i].ResetMs, windows[i].WindowMs)
	}
	return windows, nil
}

func parseNamedWindow(root map[string]json.RawMessage, keys []string, label string, window time.Duration, now time.Time) *Window {
	obj := firstObject(root, keys)
	if obj == nil {
		return nil
	}
	w := newWindow(label, firstNumber(obj, percentKeys), window)
	if ms, at, ok := parseResetTimestamp(firstValue(obj, resetKeys), now); ok {
		w.ResetMs, w.ResetAt = ms, at
		w.ResetText = "resets in " + FormatDuration(time.Duration(ms)*time.Millisecond)
	} else if window == fiveHours {
		// Session fallback (usage.ts:1126): when the API omits or invalidates
		// the session reset instant, assume a full window so the row still
		// renders a countdown and a pace marker. Deliberately NOT applied to
		// weekly windows — a fabricated weekly reset would be a lie the
		// operator cannot spot.
		w.ResetMs = window.Milliseconds()
		w.ResetAt = now.Add(window).UTC().Format(time.RFC3339)
		w.ResetText = "resets in 5h"
	}
	return &w
}

// limitEntry is one element of the top-level limits[] array. A live OAuth probe
// (Fusion FNXC:UsageIndicator 2026-07-11) showed Anthropic ships per-model
// weekly usage here as {kind:"weekly_scoped", group:"weekly", percent,
// resets_at, scope.model.display_name} while seven_day_opus/seven_day_sonnet
// went null — so this walk is generic and future models appear automatically.
type limitEntry struct {
	Kind      string   `json:"kind"`
	Group     string   `json:"group"`
	Percent   *float64 `json:"percent"`
	ResetsAt  any      `json:"resets_at"`
	ResetAt   any      `json:"reset_at"`
	ResetsAtC any      `json:"resetsAt"`
	Scope     struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

func (e limitEntry) resetValue() any {
	for _, v := range []any{e.ResetsAt, e.ResetAt, e.ResetsAtC} {
		if v != nil {
			return v
		}
	}
	return nil
}

func parseLimits(raw json.RawMessage, existing []Window, now time.Time) []Window {
	if len(raw) == 0 {
		return nil
	}
	var entries []limitEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}

	seen := make(map[string]bool, len(existing))
	for _, w := range existing {
		seen[w.Label] = true
	}

	var out []Window
	for _, e := range entries {
		name := strings.TrimSpace(e.Scope.Model.DisplayName)
		if name == "" {
			continue
		}
		if e.Percent == nil || math.IsNaN(*e.Percent) || math.IsInf(*e.Percent, 0) {
			continue
		}
		if e.Group != "weekly" && !strings.HasPrefix(e.Kind, "weekly") {
			continue
		}
		label := "Weekly (" + name + ")"
		if seen[label] {
			continue // a named key already emitted this model — no double row
		}
		seen[label] = true

		w := newWindow(label, *e.Percent, sevenDays)
		if ms, at, ok := parseResetTimestamp(e.resetValue(), now); ok {
			w.ResetMs, w.ResetAt = ms, at
			w.ResetText = "resets in " + FormatDuration(time.Duration(ms)*time.Millisecond)
		}
		out = append(out, w)
	}
	return out
}

// newWindow builds a window with a clamped percentage and a stable key.
func newWindow(label string, percentUsed float64, window time.Duration) Window {
	used := math.Min(100, math.Max(0, percentUsed))
	return Window{
		Key:         slug(label),
		Label:       label,
		PercentUsed: used,
		PercentLeft: 100 - used,
		WindowMs:    window.Milliseconds(),
		Source:      SourceOAuth,
	}
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slug derives a window key from its label: "Session (5h)" → "session-5h",
// "Weekly (Fable)" → "weekly-fable". Stable across refreshes so the UI's
// per-window hide preferences survive.
func slug(label string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(label), "-"), "-")
}

// parseResetTimestamp accepts an RFC3339 string, unix seconds, or unix millis
// (values at or above 1e12 are millis), as a JSON number or a numeric string.
// It reports the ms remaining and the absolute instant, or ok=false when the
// value is absent, unparseable, or already in the past.
func parseResetTimestamp(v any, now time.Time) (msLeft int64, resetAt string, ok bool) {
	var ts int64
	switch t := v.(type) {
	case nil:
		return 0, "", false
	case float64:
		ts = toMillis(t)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, "", false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			ts = toMillis(f)
		} else if parsed, err := time.Parse(time.RFC3339, s); err == nil {
			ts = parsed.UnixMilli()
		} else {
			return 0, "", false
		}
	default:
		return 0, "", false
	}

	left := ts - now.UnixMilli()
	if left <= 0 {
		return 0, "", false
	}
	return left, time.UnixMilli(ts).UTC().Format(time.RFC3339), true
}

func toMillis(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v >= 1e12 {
		return int64(v)
	}
	return int64(v * 1000)
}

// firstObject returns the first key that decodes to a JSON object. A key that
// is absent, null, or not an object is skipped — Anthropic nulls the legacy
// per-model keys rather than omitting them.
func firstObject(root map[string]json.RawMessage, keys []string) map[string]any {
	for _, k := range keys {
		raw, present := root[k]
		if !present {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			continue
		}
		return obj
	}
	return nil
}

func firstNumber(obj map[string]any, keys []string) float64 {
	for _, k := range keys {
		if f, ok := obj[k].(float64); ok {
			return f
		}
	}
	return 0
}

func firstValue(obj map[string]any, keys []string) any {
	for _, k := range keys {
		if v, ok := obj[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// ── error-string hygiene ───────────────────────────────────────────────────

var (
	bearerRe = regexp.MustCompile(`(?i)(bearer\s+)\S+`)
	apiKeyRe = regexp.MustCompile(`sk-[A-Za-z0-9_-]{4,}`)
)

// scrubSecrets removes bearer and API-key material from any string that is
// about to become operator-visible. Applied BEFORE truncation so a token can
// never survive as a partial suffix.
func scrubSecrets(s string) string {
	s = bearerRe.ReplaceAllString(s, "${1}[redacted]")
	return apiKeyRe.ReplaceAllString(s, "[redacted]")
}

// snippet renders an upstream error body as a single scrubbed line of at most
// snippetRunes characters.
func snippet(b []byte) string {
	s := strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(string(b)))
	s = scrubSecrets(s)
	if r := []rune(s); len(r) > snippetRunes {
		return string(r[:snippetRunes])
	}
	return s
}
