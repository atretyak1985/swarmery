// Package pprofopt is the daemon's opt-in profiling listener. It is off unless
// SWARMERY_PPROF names a loopback address ("127.0.0.1:6061", "[::1]:6061"),
// and it serves net/http/pprof on its own mux and its own listener — never on
// the API's handler, so the dashboard port exposes nothing new.
//
// It exists for one job: taking a goroutine dump of a daemon whose API has
// stalled (curl 'http://127.0.0.1:<port>/debug/pprof/goroutine?debug=2'),
// which is the only way to name what holds the store's single connection.
package pprofopt

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"
)

// Env is the variable that turns the listener on.
const Env = "SWARMERY_PPROF"

// ErrNotLoopback is returned for an address whose host is not a loopback IP
// literal. A host name is refused too: "localhost" can resolve elsewhere, and
// a profiling endpoint must never be reachable from another machine.
var ErrNotLoopback = errors.New("pprof address must be a loopback IP literal (127.0.0.1 or ::1)")

// Parse validates the SWARMERY_PPROF value. An empty value is "off": enabled
// is false and err is nil. Anything else must be host:port with
// a loopback IP literal host and a port in 1..65535, or it is refused.
func Parse(value string) (addr string, enabled bool, err error) {
	if value == "" {
		return "", false, nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", false, fmt.Errorf("%s=%q: %w", Env, value, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", false, fmt.Errorf("%s=%q: %w", Env, value, ErrNotLoopback)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false, fmt.Errorf("%s=%q: port must be 1..65535", Env, value)
	}
	return net.JoinHostPort(ip.String(), port), true, nil
}

// FromEnv reads and validates Env through getenv.
func FromEnv(getenv func(string) string) (addr string, enabled bool, err error) {
	return Parse(getenv(Env))
}

// Handler is the pprof mux: the index, cmdline, profile, symbol and trace
// endpoints plus every named profile (goroutine, heap, block, mutex, …)
// through the index handler.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Start listens on addr and serves Handler in a goroutine. The listen error is
// returned synchronously so the caller can log a port clash; the returned
// server's Close stops it. Callers pass an address Parse accepted — Start
// itself refuses a non-loopback one as a second fence.
func Start(addr string) (*http.Server, net.Addr, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, nil, fmt.Errorf("pprof listen %q: %w", addr, ErrNotLoopback)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("pprof listen %q: %w", addr, err)
	}
	srv := &http.Server{Handler: Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln) //nolint:errcheck // ErrServerClosed on Close; nothing to report
	return srv, ln.Addr(), nil
}
