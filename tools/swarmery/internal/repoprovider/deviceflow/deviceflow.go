// Package deviceflow implements the OAuth 2.0 device authorization grant
// (RFC 8628) against GitHub and GitLab, so the operator can sign the daemon in
// from the dashboard when no `gh`/`glab` login exists (Phase 8 of the landing
// plan, SC-14). The human enters a short user code in a browser — on any
// machine — so the flow also works for a daemon running on a remote host.
//
// The package does the HTTP exchanges only. Start opens a flow; Poll performs
// ONE poll step and returns. The caller (the API layer) owns the loop, the
// wait between steps, the pending-login map and storing the token.
//
// # Endpoints
//
//	GitHub (github.com and GitHub Enterprise Server alike — the device flow
//	lives on the web host, not the API host):
//	  start  POST https://<host>/login/device/code          default scope "repo"
//	  poll   POST https://<host>/login/oauth/access_token
//	GitLab (gitlab.com and self-managed GitLab 17.2+):
//	  start  POST https://<host>/oauth/authorize_device     default scope "api"
//	  poll   POST https://<host>/oauth/token
//
// Both polls use grant_type urn:ietf:params:oauth:grant-type:device_code. Every
// request is form-encoded with Accept: application/json. There is no client
// secret: client ids are public identifiers.
//
// # Secrets discipline
//
// No error this package returns carries a token: every response body that can
// reach an error passes credstore.Redact, and on top of that every secret
// literal the exchange has seen (the device code, any access/refresh token in
// the body) is masked by value. Token's String/GoString methods mask the
// token, so a stray %v in a log line does not leak it either.
package deviceflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

// GrantType is the RFC 8628 device-code grant both providers poll with.
const GrantType = "urn:ietf:params:oauth:grant-type:device_code"

const (
	// DefaultGitHubScope / DefaultGitLabScope are requested when Start gets
	// no scopes: enough to push a branch and open a PR / MR.
	DefaultGitHubScope = "repo"
	DefaultGitLabScope = "api"

	// defaultInterval is RFC 8628's default poll interval (seconds) when the
	// server names none; slowDownStep is what slow_down adds to it.
	defaultInterval = 5
	slowDownStep    = 5
	// defaultExpiresIn applies when the server omits expires_in (seconds).
	defaultExpiresIn = 900

	// maxBody bounds every response read.
	maxBody = 64 << 10
	// errDetailBytes bounds the (redacted) body text an error carries.
	errDetailBytes = 512
	// requestTimeout applies when the caller's ctx has no deadline.
	requestTimeout = 20 * time.Second
)

// Sentinel errors Poll maps the RFC 8628 error codes to. A slow_down answer
// is a *SlowDownError, which matches ErrSlowDown under errors.Is.
var (
	// ErrPending: authorization_pending — the human has not entered the code
	// yet. Wait Pending.Interval seconds and poll again.
	ErrPending = errors.New("deviceflow: authorization pending")
	// ErrSlowDown: slow_down — polling too fast. Wait the new interval (see
	// SlowDownError / NextInterval) and poll again.
	ErrSlowDown = errors.New("deviceflow: slow down")
	// ErrExpired: expired_token, or the flow's ExpiresAt passed. Terminal;
	// start a new flow.
	ErrExpired = errors.New("deviceflow: device code expired")
	// ErrDenied: access_denied — the human declined. Terminal.
	ErrDenied = errors.New("deviceflow: access denied")
	// ErrUnsupportedKind: the provider kind has no device flow here.
	ErrUnsupportedKind = errors.New("deviceflow: unsupported provider")
	// ErrNoClientID: Start was called without an OAuth client id.
	ErrNoClientID = errors.New("deviceflow: no client id")
	// ErrBadHost: the host is empty or not a bare host[:port].
	ErrBadHost = errors.New("deviceflow: invalid host")
)

// SlowDownError is Poll's answer to slow_down. Interval is the poll interval
// (seconds) to use from now on: the server's own value when it sent one that
// is larger, otherwise the previous interval plus 5 s (RFC 8628 §3.5).
type SlowDownError struct{ Interval int }

func (e *SlowDownError) Error() string {
	return fmt.Sprintf("deviceflow: slow down (poll every %ds)", e.Interval)
}

// Is makes errors.Is(err, ErrSlowDown) true.
func (e *SlowDownError) Is(target error) bool { return target == ErrSlowDown }

// Error is any failure that is not one of the RFC 8628 poll states: an HTTP
// error status, an OAuth error code the flow does not model (for example
// GitHub's device_flow_disabled or incorrect_client_credentials), or a
// malformed response. Detail is the REDACTED, bounded response body.
type Error struct {
	Op     string // "start" or "poll"
	Status int    // HTTP status; 0 when no response was read
	Code   string // OAuth "error" field, when the body carried one
	Detail string // redacted body text (never a token)
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("deviceflow: ")
	b.WriteString(e.Op)
	if e.Status != 0 {
		fmt.Fprintf(&b, ": HTTP %d", e.Status)
	}
	if e.Code != "" {
		b.WriteString(": ")
		b.WriteString(e.Code)
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	return b.String()
}

// Pending is an open device flow: what the human needs (UserCode,
// VerificationURI) and what Poll needs (the rest). DeviceCode is a bearer
// secret for the flow's lifetime — keep Pending server-side, never send it to
// a browser.
type Pending struct {
	Kind     repoprovider.Kind
	Host     string
	ClientID string

	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string // GitLab sends it; GitHub does not

	ExpiresIn int       // seconds, as the server sent it (or the default)
	Interval  int       // seconds between polls; raised by slow_down
	ExpiresAt time.Time // Start's clock + ExpiresIn
}

// Token is a granted access token. Its String/GoString mask AccessToken and
// RefreshToken, so formatting a Token with %v / %+v / %#v never prints them.
type Token struct {
	AccessToken string
	TokenType   string
	Scope       string
	// RefreshToken / ExpiresIn are set when the provider issues an expiring
	// token (GitLab OAuth tokens expire; GitHub OAuth-app tokens do not).
	RefreshToken string
	ExpiresIn    int
}

func (t Token) String() string {
	return fmt.Sprintf("deviceflow.Token{AccessToken:***, TokenType:%q, Scope:%q, ExpiresIn:%d}",
		t.TokenType, t.Scope, t.ExpiresIn)
}

// GoString keeps %#v masked too.
func (t Token) GoString() string { return t.String() }

// Doer is the HTTP client seam; *http.Client satisfies it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client runs device flows. The zero value is usable: a nil HTTP uses a
// client with a 30 s timeout, a nil BaseURL uses "https://<host>", a nil Now
// uses time.Now.
type Client struct {
	HTTP Doer
	// BaseURL maps a provider and host to the scheme+authority the endpoint
	// paths are appended to (no trailing slash). Tests point it at httptest.
	BaseURL func(kind repoprovider.Kind, host string) string
	Now     func() time.Time
}

// Default is the client the package-level Start and Poll use.
var Default = &Client{}

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// Start opens a device flow with the Default client. See Client.Start.
func Start(ctx context.Context, kind repoprovider.Kind, host, clientID string, scopes []string) (Pending, error) {
	return Default.Start(ctx, kind, host, clientID, scopes)
}

// Poll runs one poll step with the Default client. See Client.Poll.
func Poll(ctx context.Context, p Pending) (Token, error) {
	return Default.Poll(ctx, p)
}

// NextInterval is the interval (seconds) to wait before polling p again after
// Poll returned err: the raised interval on slow_down, p.Interval otherwise
// (never less than 1). The caller stores it back: p.Interval = NextInterval(p, err).
func NextInterval(p Pending, err error) int {
	var sd *SlowDownError
	if errors.As(err, &sd) && sd.Interval > 0 {
		return sd.Interval
	}
	if p.Interval < 1 {
		return defaultInterval
	}
	return p.Interval
}

type endpoints struct{ start, poll, scope string }

func endpointsFor(kind repoprovider.Kind) (endpoints, error) {
	switch kind {
	case repoprovider.KindGitHub:
		return endpoints{"/login/device/code", "/login/oauth/access_token", DefaultGitHubScope}, nil
	case repoprovider.KindGitLab:
		return endpoints{"/oauth/authorize_device", "/oauth/token", DefaultGitLabScope}, nil
	}
	return endpoints{}, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
}

// Start asks host for a device code and user code. scopes nil/empty requests
// the provider default ("repo" on GitHub, "api" on GitLab).
func (c *Client) Start(ctx context.Context, kind repoprovider.Kind, host, clientID string, scopes []string) (Pending, error) {
	ep, err := endpointsFor(kind)
	if err != nil {
		return Pending{}, err
	}
	if err := checkHost(host); err != nil {
		return Pending{}, err
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return Pending{}, ErrNoClientID
	}
	scope := strings.Join(scopes, " ")
	if strings.TrimSpace(scope) == "" {
		scope = ep.scope
	}
	form := url.Values{"client_id": {clientID}, "scope": {scope}}
	status, body, err := c.post(ctx, kind, host, ep.start, form)
	if err != nil {
		return Pending{}, &Error{Op: "start", Detail: scrub(err.Error())}
	}
	var r struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
		Error                   string `json:"error"`
	}
	jerr := json.Unmarshal(body, &r)
	secrets := []string{r.DeviceCode}
	if jerr == nil && r.Error != "" {
		return Pending{}, &Error{Op: "start", Status: status, Code: r.Error, Detail: scrub(string(body), secrets...)}
	}
	if status < 200 || status > 299 {
		return Pending{}, &Error{Op: "start", Status: status, Detail: scrub(string(body), secrets...)}
	}
	if jerr != nil || r.DeviceCode == "" || r.UserCode == "" || r.VerificationURI == "" {
		return Pending{}, &Error{Op: "start", Status: status, Code: "malformed_response", Detail: scrub(string(body), secrets...)}
	}
	p := Pending{
		Kind:                    kind,
		Host:                    host,
		ClientID:                clientID,
		DeviceCode:              r.DeviceCode,
		UserCode:                r.UserCode,
		VerificationURI:         r.VerificationURI,
		VerificationURIComplete: r.VerificationURIComplete,
		ExpiresIn:               r.ExpiresIn,
		Interval:                r.Interval,
	}
	if p.ExpiresIn <= 0 {
		p.ExpiresIn = defaultExpiresIn
	}
	if p.Interval <= 0 {
		p.Interval = defaultInterval
	}
	p.ExpiresAt = c.now().Add(time.Duration(p.ExpiresIn) * time.Second)
	return p, nil
}

// Poll performs ONE token request for p and maps the answer:
//
//	granted                → Token, nil
//	authorization_pending  → ErrPending
//	slow_down              → *SlowDownError (errors.Is ErrSlowDown; see NextInterval)
//	expired_token          → ErrExpired (also returned without a request once p.ExpiresAt passed)
//	access_denied          → ErrDenied
//	anything else          → *Error with the redacted body
//
// GitHub answers every state with HTTP 200 and an "error" field; GitLab uses
// HTTP 400 for the error states. Both shapes are handled.
func (c *Client) Poll(ctx context.Context, p Pending) (Token, error) {
	ep, err := endpointsFor(p.Kind)
	if err != nil {
		return Token{}, err
	}
	if err := checkHost(p.Host); err != nil {
		return Token{}, err
	}
	if !p.ExpiresAt.IsZero() && !c.now().Before(p.ExpiresAt) {
		return Token{}, ErrExpired
	}
	form := url.Values{
		"client_id":   {p.ClientID},
		"device_code": {p.DeviceCode},
		"grant_type":  {GrantType},
	}
	status, body, err := c.post(ctx, p.Kind, p.Host, ep.poll, form)
	if err != nil {
		return Token{}, &Error{Op: "poll", Detail: scrub(err.Error(), p.DeviceCode)}
	}
	var r struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Interval     int    `json:"interval"`
		Error        string `json:"error"`
	}
	jerr := json.Unmarshal(body, &r)
	secrets := []string{p.DeviceCode, r.AccessToken, r.RefreshToken}
	if jerr == nil && r.Error != "" {
		switch r.Error {
		case "authorization_pending":
			return Token{}, ErrPending
		case "slow_down":
			next := NextInterval(p, nil) + slowDownStep
			if r.Interval > next {
				next = r.Interval
			}
			return Token{}, &SlowDownError{Interval: next}
		case "expired_token":
			return Token{}, ErrExpired
		case "access_denied":
			return Token{}, ErrDenied
		}
		return Token{}, &Error{Op: "poll", Status: status, Code: r.Error, Detail: scrub(string(body), secrets...)}
	}
	if status < 200 || status > 299 {
		return Token{}, &Error{Op: "poll", Status: status, Detail: scrub(string(body), secrets...)}
	}
	if jerr != nil || r.AccessToken == "" {
		return Token{}, &Error{Op: "poll", Status: status, Code: "malformed_response", Detail: scrub(string(body), secrets...)}
	}
	return Token{
		AccessToken:  r.AccessToken,
		TokenType:    r.TokenType,
		Scope:        r.Scope,
		RefreshToken: r.RefreshToken,
		ExpiresIn:    r.ExpiresIn,
	}, nil
}

// post sends a form POST and returns the status and the (bounded) body. The
// returned transport error never contains the form: url.Error carries only
// the URL, and the form travels in the body.
func (c *Client) post(ctx context.Context, kind repoprovider.Kind, host, path string, form url.Values) (int, []byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requestTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(kind, host)+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.doer().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxBody {
		return resp.StatusCode, nil, fmt.Errorf("response body exceeds %d bytes", maxBody)
	}
	return resp.StatusCode, body, nil
}

func (c *Client) doer() Doer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) baseURL(kind repoprovider.Kind, host string) string {
	if c.BaseURL != nil {
		return strings.TrimRight(c.BaseURL(kind, host), "/")
	}
	return "https://" + host
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// checkHost accepts a bare host or host:port — nothing a URL could smuggle a
// path, query, userinfo or second authority through.
func checkHost(host string) error {
	if host == "" || strings.TrimSpace(host) != host {
		return fmt.Errorf("%w: %q", ErrBadHost, host)
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: %q", ErrBadHost, host)
	}
	return nil
}

// scrub redacts s (known token shapes + every literal credstore has seen),
// masks each extra secret literal by value, collapses whitespace and bounds
// the result.
func scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	s = credstore.Redact(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > errDetailBytes {
		s = s[:errDetailBytes] + "…"
	}
	return s
}
