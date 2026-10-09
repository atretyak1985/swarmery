package deviceflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// Token-shaped fixtures are built at runtime so no literal in this file looks
// like a real credential to a secret scanner.
var (
	ghToken     = "gho_" + strings.Repeat("A", 36)
	shapedLeak  = "ghp_" + strings.Repeat("Z", 36)
	glpatLeak   = "glpat-" + strings.Repeat("q", 20)
	opaqueToken = "opaque-access-" + strings.Repeat("7", 24) // GitLab OAuth tokens match no shape
	deviceCode  = "dev-code-" + strings.Repeat("9", 20)
)

var fixedNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// provider is one row of the GitHub/GitLab test matrix.
type provider struct {
	name         string
	kind         repoprovider.Kind
	host         string
	startPath    string
	pollPath     string
	defaultScope string
	// errStatus is the HTTP status the provider uses for poll error states:
	// GitHub answers 200 with an "error" field, GitLab answers 400.
	errStatus int
}

var providers = []provider{
	{"github", repoprovider.KindGitHub, "github.com", "/login/device/code", "/login/oauth/access_token", "repo", http.StatusOK},
	{"gitlab", repoprovider.KindGitLab, "gitlab.example.com", "/oauth/authorize_device", "/oauth/token", "api", http.StatusBadRequest},
}

type reply struct {
	status int
	body   string
}

type recorded struct {
	method, path, contentType, accept string
	form                              url.Values
}

// scripted is an httptest server that answers each path from a queue of
// replies (the last reply repeats) and records every request.
type scripted struct {
	mu     sync.Mutex
	routes map[string][]reply
	reqs   []recorded
}

func newScripted(t *testing.T, routes map[string][]reply) (*scripted, *httptest.Server) {
	t.Helper()
	s := &scripted{routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *scripted) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(raw))
	s.mu.Lock()
	s.reqs = append(s.reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Accept"), form})
	q := s.routes[r.URL.Path]
	if len(q) == 0 {
		s.mu.Unlock()
		http.Error(w, "unscripted path", http.StatusNotFound)
		return
	}
	rep := q[0]
	if len(q) > 1 {
		s.routes[r.URL.Path] = q[1:]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	io.WriteString(w, rep.body)
}

func (s *scripted) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.reqs...)
}

func clientFor(srv *httptest.Server) *Client {
	return &Client{
		HTTP:    srv.Client(),
		BaseURL: func(repoprovider.Kind, string) string { return srv.URL + "/" },
		Now:     func() time.Time { return fixedNow },
	}
}

func pendingFor(p provider) Pending {
	return Pending{
		Kind:       p.kind,
		Host:       p.host,
		ClientID:   "client-123",
		DeviceCode: deviceCode,
		UserCode:   "WDJB-MJHT",
		Interval:   5,
		ExpiresIn:  900,
		ExpiresAt:  fixedNow.Add(15 * time.Minute),
	}
}

func errBody(code string) string { return fmt.Sprintf(`{"error":%q}`, code) }

// assertClean fails when err's text carries any secret the tests use.
func assertClean(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := err.Error()
	for _, sec := range []string{ghToken, shapedLeak, glpatLeak, opaqueToken, deviceCode} {
		if strings.Contains(msg, sec) {
			t.Fatalf("error leaks a secret %q: %s", sec[:6], msg)
		}
	}
}

func TestStartParsesFieldsBothProviders(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"device_code":%q,"user_code":"WDJB-MJHT","verification_uri":"https://%s/login/device",`+
				`"verification_uri_complete":"https://%s/login/device?user_code=WDJB-MJHT","expires_in":600,"interval":7}`,
				deviceCode, p.host, p.host)
			s, srv := newScripted(t, map[string][]reply{p.startPath: {{200, body}}})
			got, err := clientFor(srv).Start(context.Background(), p.kind, p.host, " client-123 ", nil)
			if err != nil {
				t.Fatalf("Start = %v", err)
			}
			want := Pending{
				Kind: p.kind, Host: p.host, ClientID: "client-123",
				DeviceCode: deviceCode, UserCode: "WDJB-MJHT",
				VerificationURI:         "https://" + p.host + "/login/device",
				VerificationURIComplete: "https://" + p.host + "/login/device?user_code=WDJB-MJHT",
				ExpiresIn:               600, Interval: 7, ExpiresAt: fixedNow.Add(600 * time.Second),
			}
			if got != want {
				t.Fatalf("Pending =\n %+v\nwant\n %+v", got, want)
			}
			reqs := s.requests()
			if len(reqs) != 1 {
				t.Fatalf("requests = %d, want 1", len(reqs))
			}
			r := reqs[0]
			if r.method != http.MethodPost || r.path != p.startPath {
				t.Fatalf("request = %s %s, want POST %s", r.method, r.path, p.startPath)
			}
			if r.contentType != "application/x-www-form-urlencoded" || r.accept != "application/json" {
				t.Fatalf("headers: Content-Type=%q Accept=%q", r.contentType, r.accept)
			}
			if r.form.Get("client_id") != "client-123" || r.form.Get("scope") != p.defaultScope {
				t.Fatalf("form = %v, want client_id=client-123 scope=%s", r.form, p.defaultScope)
			}
		})
	}
}

func TestStartExplicitScopesAndDefaults(t *testing.T) {
	body := fmt.Sprintf(`{"device_code":%q,"user_code":"U","verification_uri":"https://x/device"}`, deviceCode)
	s, srv := newScripted(t, map[string][]reply{"/oauth/authorize_device": {{200, body}}})
	got, err := clientFor(srv).Start(context.Background(), repoprovider.KindGitLab, "gitlab.com", "cid",
		[]string{"read_api", "write_repository"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Interval != defaultInterval || got.ExpiresIn != defaultExpiresIn {
		t.Fatalf("defaults: Interval=%d ExpiresIn=%d", got.Interval, got.ExpiresIn)
	}
	if !got.ExpiresAt.Equal(fixedNow.Add(defaultExpiresIn * time.Second)) {
		t.Fatalf("ExpiresAt = %v", got.ExpiresAt)
	}
	if got.VerificationURIComplete != "" {
		t.Fatalf("VerificationURIComplete = %q, want empty", got.VerificationURIComplete)
	}
	if scope := s.requests()[0].form.Get("scope"); scope != "read_api write_repository" {
		t.Fatalf("scope = %q", scope)
	}
}

func TestStartHTTPErrorIsRedacted(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			body := `{"message":"Bad credentials for ` + shapedLeak + ` and ` + glpatLeak + `"}`
			_, srv := newScripted(t, map[string][]reply{p.startPath: {{http.StatusUnauthorized, body}}})
			_, err := clientFor(srv).Start(context.Background(), p.kind, p.host, "cid", nil)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Start = %v, want *Error", err)
			}
			if e.Op != "start" || e.Status != http.StatusUnauthorized {
				t.Fatalf("Error = %+v", e)
			}
			if !strings.Contains(err.Error(), "Bad credentials") || !strings.Contains(err.Error(), "***") {
				t.Fatalf("error lost its context or its mask: %s", err)
			}
			assertClean(t, err)
		})
	}
}

func TestStartOAuthErrorCode(t *testing.T) {
	// GitHub reports a disabled device flow as HTTP 200 with an error field.
	body := `{"error":"device_flow_disabled","error_description":"Device Flow must be explicitly enabled for this App"}`
	_, srv := newScripted(t, map[string][]reply{"/login/device/code": {{200, body}}})
	_, err := clientFor(srv).Start(context.Background(), repoprovider.KindGitHub, "github.com", "cid", nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != "device_flow_disabled" || e.Status != 200 {
		t.Fatalf("Start = %#v, want *Error{Code: device_flow_disabled}", err)
	}
}

func TestStartMalformedResponse(t *testing.T) {
	for name, body := range map[string]string{
		"not json":       `<html>oops ` + shapedLeak + `</html>`,
		"missing fields": `{"user_code":"U"}`,
		"device only":    fmt.Sprintf(`{"device_code":%q}`, deviceCode),
	} {
		t.Run(name, func(t *testing.T) {
			_, srv := newScripted(t, map[string][]reply{"/login/device/code": {{200, body}}})
			_, err := clientFor(srv).Start(context.Background(), repoprovider.KindGitHub, "github.com", "cid", nil)
			var e *Error
			if !errors.As(err, &e) || e.Code != "malformed_response" {
				t.Fatalf("Start = %v, want malformed_response", err)
			}
			assertClean(t, err)
		})
	}
}

func TestStartValidation(t *testing.T) {
	s, srv := newScripted(t, map[string][]reply{})
	c := clientFor(srv)
	ctx := context.Background()
	if _, err := c.Start(ctx, repoprovider.KindUnknown, "github.com", "cid", nil); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("unknown kind = %v", err)
	}
	for _, h := range []string{"", " github.com", "github.com/evil", "user@github.com", "github.com?x=1", "github.com#f", "a b"} {
		if _, err := c.Start(ctx, repoprovider.KindGitHub, h, "cid", nil); !errors.Is(err, ErrBadHost) {
			t.Fatalf("host %q = %v, want ErrBadHost", h, err)
		}
	}
	if _, err := c.Start(ctx, repoprovider.KindGitHub, "github.com", "  ", nil); !errors.Is(err, ErrNoClientID) {
		t.Fatalf("empty client id = %v", err)
	}
	if n := len(s.requests()); n != 0 {
		t.Fatalf("validation failures sent %d requests", n)
	}
}

func TestPollPendingThenOKBothProviders(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			tok := ghToken
			okBody := fmt.Sprintf(`{"access_token":%q,"token_type":"bearer","scope":%q}`, tok, p.defaultScope)
			if p.kind == repoprovider.KindGitLab {
				tok = opaqueToken
				okBody = fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer","scope":"api","refresh_token":"rt-%s","expires_in":7200}`,
					tok, strings.Repeat("r", 20))
			}
			s, srv := newScripted(t, map[string][]reply{p.pollPath: {
				{p.errStatus, errBody("authorization_pending")},
				{p.errStatus, errBody("authorization_pending")},
				{200, okBody},
			}})
			c := clientFor(srv)
			pend := pendingFor(p)
			for i := 0; i < 2; i++ {
				if _, err := c.Poll(context.Background(), pend); !errors.Is(err, ErrPending) {
					t.Fatalf("poll %d = %v, want ErrPending", i, err)
				}
				if NextInterval(pend, ErrPending) != pend.Interval {
					t.Fatal("pending changed the interval")
				}
			}
			got, err := c.Poll(context.Background(), pend)
			if err != nil {
				t.Fatalf("poll 3 = %v", err)
			}
			if got.AccessToken != tok || got.Scope == "" || got.TokenType == "" {
				t.Fatalf("Token fields not parsed: type=%q scope=%q", got.TokenType, got.Scope)
			}
			if p.kind == repoprovider.KindGitLab && (got.RefreshToken == "" || got.ExpiresIn != 7200) {
				t.Fatalf("GitLab refresh/expiry not parsed: expires=%d", got.ExpiresIn)
			}
			for _, f := range []string{fmt.Sprint(got), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got)} {
				if strings.Contains(f, tok) || (got.RefreshToken != "" && strings.Contains(f, got.RefreshToken)) {
					t.Fatalf("formatted Token leaks a secret: %s", f)
				}
			}
			reqs := s.requests()
			if len(reqs) != 3 {
				t.Fatalf("requests = %d, want 3", len(reqs))
			}
			for _, r := range reqs {
				if r.method != http.MethodPost || r.path != p.pollPath || r.accept != "application/json" {
					t.Fatalf("poll request = %s %s Accept=%q", r.method, r.path, r.accept)
				}
				if r.form.Get("grant_type") != GrantType || r.form.Get("device_code") != deviceCode || r.form.Get("client_id") != "client-123" {
					t.Fatalf("poll form = %v", r.form)
				}
			}
		})
	}
}

func TestPollSlowDownRaisesInterval(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			_, srv := newScripted(t, map[string][]reply{p.pollPath: {
				{p.errStatus, errBody("slow_down")},
				{p.errStatus, `{"error":"slow_down","interval":30}`},
				{p.errStatus, `{"error":"slow_down","interval":2}`},
			}})
			c := clientFor(srv)
			pend := pendingFor(p)

			// No server interval: previous + 5.
			_, err := c.Poll(context.Background(), pend)
			if !errors.Is(err, ErrSlowDown) {
				t.Fatalf("poll = %v, want ErrSlowDown", err)
			}
			var sd *SlowDownError
			if !errors.As(err, &sd) || sd.Interval != 10 {
				t.Fatalf("slow_down interval = %+v, want 10", sd)
			}
			pend.Interval = NextInterval(pend, err)
			if pend.Interval != 10 {
				t.Fatalf("NextInterval = %d, want 10", pend.Interval)
			}

			// A larger server interval wins.
			_, err = c.Poll(context.Background(), pend)
			pend.Interval = NextInterval(pend, err)
			if pend.Interval != 30 {
				t.Fatalf("NextInterval with server interval = %d, want 30", pend.Interval)
			}

			// A smaller server interval never lowers it: previous + 5.
			_, err = c.Poll(context.Background(), pend)
			pend.Interval = NextInterval(pend, err)
			if pend.Interval != 35 {
				t.Fatalf("NextInterval with small server interval = %d, want 35", pend.Interval)
			}
			if !strings.Contains(err.Error(), "35s") {
				t.Fatalf("SlowDownError text = %q", err)
			}
		})
	}
}

func TestPollExpiredAndDeniedBothProviders(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			_, srv := newScripted(t, map[string][]reply{p.pollPath: {
				{p.errStatus, errBody("expired_token")},
				{p.errStatus, errBody("access_denied")},
			}})
			c := clientFor(srv)
			if _, err := c.Poll(context.Background(), pendingFor(p)); !errors.Is(err, ErrExpired) {
				t.Fatalf("expired_token = %v, want ErrExpired", err)
			}
			if _, err := c.Poll(context.Background(), pendingFor(p)); !errors.Is(err, ErrDenied) {
				t.Fatalf("access_denied = %v, want ErrDenied", err)
			}
		})
	}
}

func TestPollLocalExpirySendsNoRequest(t *testing.T) {
	s, srv := newScripted(t, map[string][]reply{})
	pend := pendingFor(providers[0])
	pend.ExpiresAt = fixedNow // now == ExpiresAt counts as expired
	if _, err := clientFor(srv).Poll(context.Background(), pend); !errors.Is(err, ErrExpired) {
		t.Fatalf("Poll(past ExpiresAt) = %v, want ErrExpired", err)
	}
	if n := len(s.requests()); n != 0 {
		t.Fatalf("expired flow sent %d requests", n)
	}
}

func TestPollValidation(t *testing.T) {
	c := &Client{}
	pend := pendingFor(providers[0])
	pend.Kind = repoprovider.KindUnknown
	if _, err := c.Poll(context.Background(), pend); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("unknown kind = %v", err)
	}
	pend = pendingFor(providers[0])
	pend.Host = "github.com/x"
	if _, err := c.Poll(context.Background(), pend); !errors.Is(err, ErrBadHost) {
		t.Fatalf("bad host = %v", err)
	}
}

func TestPollMalformedAndHTTPErrors(t *testing.T) {
	cases := []struct {
		name   string
		rep    reply
		status int
		code   string
	}{
		{"html", reply{200, `<html>` + deviceCode + `</html>`}, 200, "malformed_response"},
		{"empty object", reply{200, `{}`}, 200, "malformed_response"},
		{"server error", reply{502, `upstream down near ` + shapedLeak}, 502, ""},
		{"unknown oauth code", reply{400, `{"error":"incorrect_device_code","error_description":"for ` + deviceCode + `"}`}, 400, "incorrect_device_code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newScripted(t, map[string][]reply{"/oauth/token": {tc.rep}})
			_, err := clientFor(srv).Poll(context.Background(), pendingFor(providers[1]))
			var e *Error
			if !errors.As(err, &e) || e.Op != "poll" || e.Status != tc.status || e.Code != tc.code {
				t.Fatalf("Poll = %#v, want *Error{status %d code %q}", err, tc.status, tc.code)
			}
			assertClean(t, err)
		})
	}
}

// The access token — shaped or opaque — never appears in an error, even when a
// broken server returns it next to an error code or an error status.
func TestAccessTokenNeverInErrorMessages(t *testing.T) {
	bodies := []reply{
		{400, fmt.Sprintf(`{"error":"invalid_grant","access_token":%q,"error_description":"token %s rejected"}`, opaqueToken, opaqueToken)},
		{500, fmt.Sprintf(`{"access_token":%q,"refresh_token":%q}`, opaqueToken, ghToken)},
		{200, fmt.Sprintf(`{"access_token":%q,"error":"server_error","error_description":"%s %s"}`, ghToken, ghToken, glpatLeak)},
		{403, fmt.Sprintf(`{"message":"token %s and %s is not allowed"}`, shapedLeak, ghToken)},
	}
	for i, rep := range bodies {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, srv := newScripted(t, map[string][]reply{"/login/oauth/access_token": {rep}})
			_, err := clientFor(srv).Poll(context.Background(), pendingFor(providers[0]))
			if err == nil {
				t.Fatal("Poll = nil error")
			}
			assertClean(t, err)
		})
	}
}

type fakeDoer struct {
	err error
	req *http.Request
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	f.req = r
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"error":"authorization_pending"}`))}, nil
}

func TestTransportErrorIsScrubbed(t *testing.T) {
	d := &fakeDoer{err: fmt.Errorf("dial failed while sending %s / %s", deviceCode, shapedLeak)}
	c := &Client{HTTP: d, Now: func() time.Time { return fixedNow }}
	_, err := c.Poll(context.Background(), pendingFor(providers[0]))
	var e *Error
	if !errors.As(err, &e) || e.Status != 0 || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("Poll = %v, want transport *Error", err)
	}
	assertClean(t, err)
	// Start has no device code yet; a token shape in the transport error is
	// still masked.
	d.err = fmt.Errorf("proxy said %s", shapedLeak)
	_, err = c.Start(context.Background(), repoprovider.KindGitHub, "github.com", "cid", nil)
	if !errors.As(err, &e) || e.Op != "start" {
		t.Fatalf("Start = %v, want transport *Error", err)
	}
	assertClean(t, err)
}

func TestDefaultBaseURLAndDeadline(t *testing.T) {
	cases := []struct {
		kind repoprovider.Kind
		host string
		want string
	}{
		{repoprovider.KindGitHub, "github.com", "https://github.com/login/device/code"},
		{repoprovider.KindGitHub, "ghe.example.com:8443", "https://ghe.example.com:8443/login/device/code"},
		{repoprovider.KindGitLab, "gitlab.com", "https://gitlab.com/oauth/authorize_device"},
	}
	for _, tc := range cases {
		d := &fakeDoer{err: errors.New("offline")}
		c := &Client{HTTP: d}
		_, _ = c.Start(context.Background(), tc.kind, tc.host, "cid", nil)
		if d.req == nil || d.req.URL.String() != tc.want {
			t.Fatalf("%s %s: URL = %v, want %s", tc.kind, tc.host, d.req.URL, tc.want)
		}
		if _, ok := d.req.Context().Deadline(); !ok {
			t.Fatal("request without a deadline")
		}
	}
	// A caller deadline is kept, not replaced.
	d := &fakeDoer{err: errors.New("offline")}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	want, _ := ctx.Deadline()
	_, _ = (&Client{HTTP: d}).Poll(ctx, Pending{Kind: repoprovider.KindGitLab, Host: "gitlab.com", DeviceCode: deviceCode})
	if got, _ := d.req.Context().Deadline(); !got.Equal(want) {
		t.Fatalf("deadline = %v, want caller's %v", got, want)
	}
	if d.req.URL.String() != "https://gitlab.com/oauth/token" {
		t.Fatalf("poll URL = %s", d.req.URL)
	}
}

func TestResponseBodyIsBounded(t *testing.T) {
	big := `{"error":"authorization_pending","pad":"` + strings.Repeat("x", maxBody) + `"}`
	_, srv := newScripted(t, map[string][]reply{"/login/oauth/access_token": {{200, big}}})
	_, err := clientFor(srv).Poll(context.Background(), pendingFor(providers[0]))
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Poll(oversized) = %v, want size error", err)
	}
	if len(err.Error()) > errDetailBytes+64 {
		t.Fatalf("error text not bounded: %d bytes", len(err.Error()))
	}
}

func TestErrorDetailIsBounded(t *testing.T) {
	_, srv := newScripted(t, map[string][]reply{"/oauth/token": {{500, strings.Repeat("e ", 4000)}}})
	_, err := clientFor(srv).Poll(context.Background(), pendingFor(providers[1]))
	var e *Error
	if !errors.As(err, &e) || len(e.Detail) > errDetailBytes+len("…") {
		t.Fatalf("Detail = %d bytes, want ≤ %d", len(e.Detail), errDetailBytes)
	}
}

func TestPackageLevelUsesDefault(t *testing.T) {
	body := fmt.Sprintf(`{"device_code":%q,"user_code":"U","verification_uri":"https://x/device","interval":0}`, deviceCode)
	_, srv := newScripted(t, map[string][]reply{
		"/login/device/code":        {{200, body}},
		"/login/oauth/access_token": {{200, fmt.Sprintf(`{"access_token":%q,"token_type":"bearer","scope":"repo"}`, ghToken)}},
	})
	saved := Default
	Default = clientFor(srv)
	t.Cleanup(func() { Default = saved })

	pend, err := Start(context.Background(), repoprovider.KindGitHub, "github.com", "cid", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := Poll(context.Background(), pend)
	if err != nil || tok.AccessToken != ghToken {
		t.Fatalf("Poll = %v", err)
	}
}

func TestNextIntervalFloor(t *testing.T) {
	if got := NextInterval(Pending{}, nil); got != defaultInterval {
		t.Fatalf("NextInterval(zero) = %d, want %d", got, defaultInterval)
	}
	if got := NextInterval(Pending{Interval: 3}, &SlowDownError{}); got != 3 {
		t.Fatalf("NextInterval(empty SlowDownError) = %d, want 3", got)
	}
}

func TestErrorText(t *testing.T) {
	e := &Error{Op: "poll", Status: 400, Code: "invalid_grant", Detail: "bad"}
	if got := e.Error(); got != "deviceflow: poll: HTTP 400: invalid_grant: bad" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&Error{Op: "start"}).Error(); got != "deviceflow: start" {
		t.Fatalf("Error() = %q", got)
	}
}
