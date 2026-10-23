// `swarmery open [--print] [--session <uuid>] [--port <n>]` — open the
// dashboard unlocked. The dashboard authenticates its writes with a session
// token, and the browser never holds the link token, so this command signs a
// short-lived unlock ticket with it (internal/linksig) and hands the browser
//
//	http://localhost:<port>/[sessions/<uuid>]#swarmery-unlock=<ticket>
//
// The ticket rides in the URL fragment, which a browser never sends to any
// server; the SPA redeems it once at POST /api/session/unlock and drops it
// from the address bar. It is valid for linksig.UnlockMaxSkew (2 minutes) and
// once.
//
// Exit codes: 0 opened (or printed), 2 a usage error, no link token, or the
// browser could not be started.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

const openUsage = "usage: swarmery open [--print] [--session <uuid>] [--port <n>] [--key-file <path>]"

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
	kp := *keyFile
	if kp == "" {
		kp = linksig.DefaultKeyPath()
	}
	key := linksig.ReadKeyFile(kp)
	if key == "" {
		fmt.Fprintf(stderr, "swarmery open: no link token at %s (the daemon mints it on start; `swarmery link token` mints one now)\n", kp)
		return 2
	}
	u, err := unlockURL(key, *port, *session, openNow())
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

// unlockURL is the dashboard URL (a session page when sessionID is set) with
// a fresh unlock ticket in its fragment.
func unlockURL(key string, port int, sessionID string, now time.Time) (string, error) {
	ticket, err := linksig.UnlockTicket(key, now)
	if err != nil {
		return "", errors.New("cannot mint an unlock ticket: " + err.Error())
	}
	path := "/"
	if sessionID != "" {
		path = "/sessions/" + sessionID
	}
	return fmt.Sprintf("http://localhost:%d%s#%s=%s", port, path, unlockFragment, ticket), nil
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
