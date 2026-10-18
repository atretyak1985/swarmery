package claudeacct

import (
	"strings"
	"testing"
	"time"
)

// A probe that runs out of time is a probe with no verdict, and no verdict is
// never a licence to trust the binding. This drives the REAL runner (no stub)
// against a real repository: with room to answer, the untracked binding is
// honoured; with a 1 ns bound the very same file is ignored, and the reason
// names the timeout. This is the mechanism by which a loaded machine falls back
// to a parent's pin (dispatch's TestRunPlaybook_ResolvesTheProjectsAccountAndEstate),
// pinned deterministically instead of by load.
func TestProbeTimeoutIsFailClosed(t *testing.T) {
	fakeHome(t)
	resetWarnOnce(t)
	const account = "work"
	seedProbeStore(t, account)
	repo := newRepo(t)
	path := writeBinding(t, repo, account) // untracked in a real repo: honoured when git answers

	restore := SetGitProbeTimeoutForTest(30 * time.Second)
	if why := bindingDistrusted(path); why != "" {
		restore()
		t.Fatalf("control: with a 30s bound the untracked binding was distrusted: %s", why)
	}
	wantHonoured(t, repo, account)
	restore()

	t.Cleanup(SetGitProbeTimeoutForTest(time.Nanosecond))
	why := bindingDistrusted(path)
	if why == "" {
		t.Fatal("with a 1ns probe bound the binding was honoured — a timed-out probe must fail closed")
	}
	if !strings.Contains(why, "timed out after 1ns") {
		t.Errorf("distrust reason = %q, want it to name the timeout", why)
	}
	wantIgnored(t, repo)
}

// The seam's restore puts back exactly what it replaced, so a test cannot leak
// its bound into the next one — and the production default is 2 s.
func TestSetGitProbeTimeoutForTestRestores(t *testing.T) {
	if got := probeTimeout(); got != 2*time.Second {
		t.Fatalf("default probe timeout = %s, want 2s", got)
	}
	outer := SetGitProbeTimeoutForTest(time.Minute)
	inner := SetGitProbeTimeoutForTest(time.Millisecond)
	if got := probeTimeout(); got != time.Millisecond {
		t.Errorf("after the inner override = %s, want 1ms", got)
	}
	inner()
	if got := probeTimeout(); got != time.Minute {
		t.Errorf("after the inner restore = %s, want 1m", got)
	}
	outer()
	if got := probeTimeout(); got != 2*time.Second {
		t.Errorf("after the outer restore = %s, want 2s", got)
	}
}
