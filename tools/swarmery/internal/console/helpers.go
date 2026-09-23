package console

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

// ctxBackground is a short-lived context for action Cmds fired from the TUI (an
// approve/deny/pause POST should never hang the Elm loop). Kept as a helper so
// tests can see the same bound.
func ctxBackground() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// The cancel runs when the deadline elapses; the Cmd goroutine is short and
	// the timeout bounds it, so leaking the cancel to the GC is acceptable here.
	_ = cancel
	return ctx
}

// openBrowser opens url in the OS default browser ([o] hotkey). Best-effort:
// callers ignore the error (a headless box just gets no window). A variable so
// tests can swap it out — the real one pops a browser tab on every `go test`.
var openBrowser = osOpenBrowser

func osOpenBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default: // linux, bsd, …
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
