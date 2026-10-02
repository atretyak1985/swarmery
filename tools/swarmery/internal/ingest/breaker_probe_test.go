package ingest

import (
	"context"
	"os"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// TestMain stubs breakerProbe for the whole package. Its default — a
// confirmed no-login — preserves every EXISTING auth-kind test's expectations
// (TestFreshTranscriptTripsBreaker, its "exactly ten minutes old" sub-test,
// TestTranscriptUnderTheUnboundDirAlsoPausesDefault) with zero edits to them:
// they keep asserting "opens", now via the confirmed path instead of the
// unconfirmed one. It also guarantees this suite never calls the real
// claudeprobe.Probe → claudebin.Resolve unless a test explicitly overrides it.
func TestMain(m *testing.M) {
	prev := breakerProbe
	breakerProbe = func(context.Context, string) claudeprobe.Result {
		return claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}
	}
	code := m.Run()
	breakerProbe = prev
	os.Exit(code)
}

// breakerProbeFor overrides breakerProbe with a fixed result for one test.
func breakerProbeFor(t *testing.T, r claudeprobe.Result) {
	t.Helper()
	prev := breakerProbe
	breakerProbe = func(context.Context, string) claudeprobe.Result { return r }
	t.Cleanup(func() { breakerProbe = prev })
}

// TestAuthFailureWithReadyProbeDoesNotTrip: the false-trip case this hardening
// exists for. An auth-failure transcript (possibly written by a process the
// daemon never spawned) whose confirming probe answers "ready" must leave the
// breaker closed and write no alert.
func TestAuthFailureWithReadyProbeDoesNotTrip(t *testing.T) {
	db := testDB(t)
	breakerAt(t, breakerNoon)
	breakerProbeFor(t, claudeprobe.Result{Status: claudeprobe.StatusReady})
	f, root := failureTranscript(t, "sess-false-trip", orgDisabledText, "2026-09-30T11:55:00.000Z", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if b, ok := workBreaker(t, db); ok && b.IsOpen() {
		t.Errorf("breaker = %+v, want closed — the confirming probe said ready", b)
	}
	if n := workAlerts(t, db); n != 0 {
		t.Errorf("open alerts = %d, want 0", n)
	}
}

// TestAuthFailureWithNoLoginProbeTrips pins the "confirmed" contract
// explicitly, independent of TestMain's default.
func TestAuthFailureWithNoLoginProbeTrips(t *testing.T) {
	db := testDB(t)
	breakerAt(t, breakerNoon)
	breakerProbeFor(t, claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin})
	f, root := failureTranscript(t, "sess-confirmed", orgDisabledText, "2026-09-30T11:55:00.000Z", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if b, ok := workBreaker(t, db); !ok || !b.IsOpen() {
		t.Errorf("breaker = %+v ok=%v, want open — the confirming probe said no-login", b, ok)
	}
}

// TestAuthFailureWithUnknownProbeDoesNotTrip: an inconclusive probe (no
// binary, timeout) fails open, matching runcore's own rule for an
// inconclusive pre-flight probe.
func TestAuthFailureWithUnknownProbeDoesNotTrip(t *testing.T) {
	db := testDB(t)
	breakerAt(t, breakerNoon)
	breakerProbeFor(t, claudeprobe.Result{Status: claudeprobe.StatusUnknown, Reason: claudeprobe.ReasonNoBinary})
	f, root := failureTranscript(t, "sess-unknown-probe", orgDisabledText, "2026-09-30T11:55:00.000Z", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if b, ok := workBreaker(t, db); ok && b.IsOpen() {
		t.Errorf("breaker = %+v, want closed — an unconfirmed probe must fail open", b)
	}
}

// TestQuotaFailureNeverCallsTheConfirmingProbe: LimitScope already reads the
// line's own wording, so a quota trip opens immediately and never consults
// the confirming probe at all.
func TestQuotaFailureNeverCallsTheConfirmingProbe(t *testing.T) {
	db := testDB(t)
	breakerAt(t, breakerNoon)
	prev := breakerProbe
	breakerProbe = func(context.Context, string) claudeprobe.Result {
		t.Fatal("quota trip must not consult the confirming probe")
		return claudeprobe.Result{}
	}
	t.Cleanup(func() { breakerProbe = prev })
	f, root := failureTranscript(t, "sess-quota-noprobe", limitText, "2026-09-30T11:59:30Z", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if b, ok := workBreaker(t, db); !ok || !b.IsOpen() || b.Kind != store.BreakerKindQuota {
		t.Errorf("breaker = %+v ok=%v, want an open quota breaker", b, ok)
	}
}
