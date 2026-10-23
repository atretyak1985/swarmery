// `swarmery open [--print] [--session <uuid>] [--port <n>] [--url <base>]` —
// open the dashboard unlocked. The dashboard authenticates its writes with a
// session token, and the browser never holds the link token, so this command
// signs a short-lived unlock ticket with it (internal/linksig) and hands the
// browser
//
//	http://127.0.0.1:<port>/[sessions/<uuid>]#swarmery-unlock=<ticket>
//
// The dashboard is opened by the literal address the daemon is bound to —
// 127.0.0.1 by default, or the loopback literal --url names (an IPv6 one in
// brackets) — never by the name localhost: that name may resolve to ::1
// first, where the daemon does not listen and another process may.
//
// The ticket rides in the URL fragment, which a browser never sends to any
// server; the SPA redeems it once at POST /api/session/unlock and drops it
// from the address bar. It is valid for linksig.UnlockMaxSkew (2 minutes),
// once, and only at the origin it was minted for.
//
// Before minting it, the command makes the listener prove it is the daemon
// (checkListener): a ticket is handed only to a listener that signs its
// answer with the link token.
//
// Exit codes: 0 opened (or printed), 2 a usage error, no link token, a
// listener that is down or does not prove it holds the token, or the browser
// could not be started.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

const openUsage = "usage: swarmery open [--print] [--session <uuid>] [--port <n>] [--url <base>] [--key-file <path>]"

// unlockFragment is the URL fragment key the SPA reads the ticket from.
const unlockFragment = "swarmery-unlock"

// openBrowser hands a URL to the platform's opener; a test replaces it.
var openBrowser = func(u string) error {
	var name string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "linux":
		name = "xdg-open"
	default:
		return fmt.Errorf("no browser opener on %s: use --print", runtime.GOOS)
	}
	return exec.Command(name, u).Run()
}

// openNow is the clock the ticket is signed at; a test replaces it.
var openNow = time.Now

// proveListener checks the listener at base holds the link token; a test
// replaces it.
var proveListener = checkListener

// listenerProbeTimeout bounds the identity probe: the daemon answers it from
// memory.
const listenerProbeTimeout = 3 * time.Second

// maxProbeAnswer bounds the probe answer read to verify its signature.
const maxProbeAnswer = 64 << 10

// checkListener makes the listener at base prove it is the daemon before a
// ticket is minted for it: a request signed with the link token to
// GET /api/link/health, whose answer the daemon signs (X-Swarmery-Sig over
// the request's nonce, the status and the body). A process that holds the
// port without holding the token can neither produce that signature nor read
// the token off the wire, so an unsigned, wrongly signed or non-200 answer —
// or no answer — is refused. Redirects are not followed.
func checkListener(base, key string) error {
	req, err := http.NewRequest(http.MethodGet, base+"/api/link/health", nil)
	if err != nil {
		return err
	}
	nonce, err := linksig.SignRequest(req, key, openNow())
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout:       listenerProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the daemon does not answer at %s (is it running? `swarmery serve`)", base)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeAnswer))
	if err != nil {
		return fmt.Errorf("reading the answer from %s: %v", base, err)
	}
	if !linksig.VerifyResponse(key, nonce, resp.StatusCode, body, resp.Header.Get(linksig.ResponseHeader)) {
		return fmt.Errorf("the listener at %s did not prove it holds the link token (HTTP %d, answer unsigned or wrongly signed): it may not be the swarmery daemon", base, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the daemon at %s answered HTTP %d to its identity check", base, resp.StatusCode)
	}
	return nil
}

func cmdOpen(args []string, stdout, stderr io.Writer) int {
	usageErr := func(format string, a ...any) int {
		if format != "" {
			fmt.Fprintf(stderr, "swarmery open: "+format+"\n", a...)
		}
		fmt.Fprintln(stderr, openUsage)
		return 2
	}
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	printOnly := fs.Bool("print", false, "print the unlock URL instead of opening it")
	session := fs.String("session", "", "open this session's page (its uuid)")
	port := fs.Int("port", envPort(), "daemon port (env: SWARMERY_PORT)")
	baseURL := fs.String("url", "", "daemon base URL, a loopback address literal (default http://127.0.0.1:<port>)")
	keyFile := fs.String("key-file", "", "link token file (default ~/.swarmery/link-token)")
	if err := fs.Parse(args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() > 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
	}
	if *port <= 0 || *port > 65535 {
		return usageErr("--port must be 1..65535")
	}
	if *session != "" && !validSessionID(*session) {
		return usageErr("--session must be a session uuid (got %q)", *session)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", *port)
	if *baseURL != "" {
		b, err := loopbackBase(*baseURL)
		if err != nil {
			return usageErr("--url: %v", err)
		}
		base = b
	}
	kp := *keyFile
	if kp == "" {
		kp = linksig.DefaultKeyPath()
	}
	key := linksig.ReadKeyFile(kp)
	if key == "" {
		fmt.Fprintf(stderr, "swarmery open: no link token at %s (the daemon mints it on start; `swarmery link token` mints one now)\n", kp)
		return 2
	}
	if err := proveListener(base, key); err != nil {
		fmt.Fprintf(stderr, "swarmery open: %v; no unlock ticket was minted\n", err)
		return 2
	}
	u, err := unlockURL(key, base, *session, openNow())
	if err != nil {
		fmt.Fprintf(stderr, "swarmery open: %v\n", err)
		return 2
	}
	if *printOnly {
		fmt.Fprintln(stdout, u)
		return 0
	}
	if err := openBrowser(u); err != nil {
		fmt.Fprintf(stderr, "swarmery open: %v\n", err)
		return 2
	}
	page, _, _ := strings.Cut(u, "#")
	fmt.Fprintf(stderr, "swarmery open: opened %s (the unlock link works once, within 2 minutes)\n", page)
	return 0
}

// loopbackBase reduces --url to scheme://<loopback literal>:<port>: http(s),
// a loopback IP address literal (no name, not even localhost; no zone), no
// path beyond "/", the scheme's default port when none is given. The result
// is linksig.UnlockOrigin's canonical form: the origin the ticket is bound to.
func loopbackBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("want a base URL with no path, query or fragment (got %q)", raw)
	}
	origin, err := linksig.UnlockOrigin(raw)
	if err != nil {
		return "", fmt.Errorf("want http://<loopback address literal>:<port> such as http://127.0.0.1:7777 or http://[::1]:7777, never a name (got %q)", raw)
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() {
		return "", fmt.Errorf("the host must be a loopback address, the daemon's own (got %q)", u.Hostname())
	}
	return origin, nil
}

// unlockURL is the dashboard URL under base — a canonical origin
// (linksig.UnlockOrigin), which the ticket is bound to — (a session page when
// sessionID is set) with a fresh unlock ticket in its fragment.
func unlockURL(key, base, sessionID string, now time.Time) (string, error) {
	ticket, err := linksig.UnlockTicket(key, base, now)
	if err != nil {
		return "", errors.New("cannot mint an unlock ticket: " + err.Error())
	}
	path := "/"
	if sessionID != "" {
		path = "/sessions/" + sessionID
	}
	return fmt.Sprintf("%s%s#%s=%s", base, path, unlockFragment, ticket), nil
}

// validSessionID: 1–64 characters of [0-9A-Za-z-] — a uuid, never a path.
func validSessionID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
		default:
			return false
		}
	}
	return true
}
