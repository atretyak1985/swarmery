package pprofopt

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestPprofOptInLoopbackOnly pins the knob: unset is off, a loopback IP
// literal with a real port is on, and everything that could expose the
// profiler beyond this machine (all interfaces, a LAN address, a host name)
// or is malformed is refused.
func TestPprofOptInLoopbackOnly(t *testing.T) {
	addr, on, err := FromEnv(func(string) string { return "" })
	if err != nil || on || addr != "" {
		t.Fatalf("unset: got (%q, %v, %v), want off with no error", addr, on, err)
	}

	accept := map[string]string{
		"127.0.0.1:6061": "127.0.0.1:6061",
		"127.0.0.2:6061": "127.0.0.2:6061",
		"[::1]:6061":     "[::1]:6061",
	}
	for in, want := range accept {
		got, on, err := Parse(in)
		if err != nil || !on || got != want {
			t.Errorf("Parse(%q) = (%q, %v, %v), want (%q, true, nil)", in, got, on, err, want)
		}
	}

	refuseNotLoopback := []string{
		":6061",            // all interfaces
		"0.0.0.0:6061",     // all interfaces
		"[::]:6061",        // all interfaces
		"192.168.1.5:6061", // LAN
		"localhost:6061",   // a name, not a literal
		"example.com:6061",
	}
	for _, in := range refuseNotLoopback {
		if _, on, err := Parse(in); err == nil || on || !errors.Is(err, ErrNotLoopback) {
			t.Errorf("Parse(%q) = (on=%v, err=%v), want ErrNotLoopback", in, on, err)
		}
	}

	refuseMalformed := []string{"127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:abc", " "}
	for _, in := range refuseMalformed {
		if _, on, err := Parse(in); err == nil || on {
			t.Errorf("Parse(%q) = (on=%v, err=%v), want an error", in, on, err)
		}
	}

	// Start is a second fence: it refuses a non-loopback address outright.
	if _, _, err := Start("0.0.0.0:0"); !errors.Is(err, ErrNotLoopback) {
		t.Errorf("Start(0.0.0.0:0) err = %v, want ErrNotLoopback", err)
	}

	// And on loopback it serves the goroutine profile the stall diagnosis needs.
	srv, bound, err := Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start(127.0.0.1:0): %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	resp, err := http.Get("http://" + bound.String() + "/debug/pprof/goroutine?debug=2")
	if err != nil {
		t.Fatalf("GET goroutine dump: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "goroutine ") {
		t.Fatalf("goroutine dump: status %d, body %.200q", resp.StatusCode, body)
	}
}
