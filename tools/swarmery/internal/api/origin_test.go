package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIsLocalOriginBuiltins: only the loopback names are trusted out of the box.
// "swarmery" is deliberately NOT among them — the daemon does not control a bare
// hostname, and a DNS search domain can expand it to a host someone else serves.
func TestIsLocalOriginBuiltins(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	for _, ok := range []string{
		"http://localhost:7777", "http://127.0.0.1:7777", "http://[::1]:7777",
		"https://localhost",
	} {
		if !isLocalOrigin(ok) {
			t.Errorf("isLocalOrigin(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"http://swarmery:7777", "http://swarmery", "https://swarmery.corp.example",
		"http://evil.example.com", "file:///etc/passwd", "null", "",
	} {
		if isLocalOrigin(bad) {
			t.Errorf("isLocalOrigin(%q) = true, want false (nothing is opted in)", bad)
		}
	}
}

// TestTrustedOriginsOptIn: a named origin passes, and ONLY that origin — the
// allow-list matches scheme+host+port, not the hostname, so opting a friendly
// alias in does not hand every port or scheme on that host the same trust.
func TestTrustedOriginsOptIn(t *testing.T) {
	AttachTrustedOrigins([]string{"http://swarmery:7777"})
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	if !isLocalOrigin("http://swarmery:7777") {
		t.Error("the opted-in origin was rejected")
	}
	// Case-insensitive on scheme and host, as origins are.
	if !isLocalOrigin("HTTP://Swarmery:7777") {
		t.Error("origin comparison must be case-insensitive on scheme and host")
	}
	for _, bad := range []string{
		"https://swarmery:7777", // different scheme
		"http://swarmery:9999",  // different port
		"http://swarmery",       // default port ≠ the named one
		"http://swarmery.corp.example:7777",
		"http://evil.example.com:7777",
	} {
		if isLocalOrigin(bad) {
			t.Errorf("isLocalOrigin(%q) = true, want false — the allow-list holds only http://swarmery:7777", bad)
		}
	}
}

// TestTrustedOriginsDefaultPort: a named origin without a port matches the
// browser's Origin header for that scheme's default port, and vice versa.
func TestTrustedOriginsDefaultPort(t *testing.T) {
	AttachTrustedOrigins([]string{"http://swarmery", "https://dash.example"})
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	for _, ok := range []string{
		"http://swarmery", "http://swarmery:80",
		"https://dash.example", "https://dash.example:443",
	} {
		if !isLocalOrigin(ok) {
			t.Errorf("isLocalOrigin(%q) = false, want true", ok)
		}
	}
	if isLocalOrigin("https://swarmery") {
		t.Error("http://swarmery must not trust https://swarmery")
	}
}

// TestAttachTrustedOriginsDropsGarbage: entries that are not http(s) origins are
// dropped whole rather than half-matched.
func TestAttachTrustedOriginsDropsGarbage(t *testing.T) {
	AttachTrustedOrigins([]string{"swarmery", "file:///x", "", "   ", "ftp://swarmery"})
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	for _, bad := range []string{"swarmery", "http://swarmery", "file:///x", "ftp://swarmery"} {
		if isLocalOrigin(bad) {
			t.Errorf("isLocalOrigin(%q) = true, want false — malformed entries must not grant trust", bad)
		}
	}
}

// TestRequireLocalOriginFence: end-to-end through the middleware — a friendly
// alias is 403 until it is opted in, and the handler never runs meanwhile.
func TestRequireLocalOriginFence(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	var called int
	h := requireLocalOrigin(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})

	do := func(origin string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/anything", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}

	if got := do("http://swarmery:7777"); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403 with an empty allow-list", got)
	}
	if called != 0 {
		t.Errorf("handler ran %d time(s) for a rejected origin", called)
	}

	AttachTrustedOrigins([]string{"http://swarmery:7777"})
	if got := do("http://swarmery:7777"); got != http.StatusOK {
		t.Errorf("status = %d, want 200 once the origin is opted in", got)
	}
	if got := do("http://swarmery:9999"); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — a sibling port is not opted in", got)
	}
	// No Origin at all (the hook shim, curl) still passes: localhost trust is v1.
	if got := do(""); got != http.StatusOK {
		t.Errorf("status = %d, want 200 for a request with no Origin header", got)
	}
	if called != 2 {
		t.Errorf("handler ran %d time(s), want 2 (opted-in origin + no-origin)", called)
	}
}

// TestStrictOriginGateSharesTheAllowList: the terminal's stricter gate honours
// the same opt-in list — a trusted alias must not get working writes and a dead
// terminal — but still rejects an ABSENT Origin, which is its whole point.
func TestStrictOriginGateSharesTheAllowList(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	if isStrictLocalOrigin("http://swarmery:7777") {
		t.Error("an alias passed the strict gate with an empty allow-list")
	}
	AttachTrustedOrigins([]string{"http://swarmery:7777"})
	if !isStrictLocalOrigin("http://swarmery:7777") {
		t.Error("the opted-in origin was rejected by the strict gate")
	}
	if isStrictLocalOrigin("") {
		t.Error("the strict gate must still reject a missing Origin")
	}
	if isStrictLocalOrigin("http://swarmery:9999") {
		t.Error("the strict gate must match the full origin, not the hostname")
	}
}
