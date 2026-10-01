package accountdoctor

import (
	"testing"
	"time"
)

// Options.Timeout bounds the whole call: once it is spent the remaining arms
// are skipped, one timeout warn says so, and the lists stay non-nil.
func TestFastTimeout(t *testing.T) {
	newFixture(t)
	orig := wallClock
	t.Cleanup(func() { wallClock = orig })
	calls := 0
	base := orig()
	wallClock = func() time.Time {
		calls++
		if calls == 1 {
			return base // start: the deadline is base+1s
		}
		return base.Add(time.Hour)
	}
	rep, err := Fast(Options{Path: t.TempDir(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if countFindings(rep, "timeout", SevWarn) != 1 || countFindings(rep, "sysscan-single-account", "") != 0 {
		t.Errorf("findings = %+v, want one timeout and no later arm", rep.Findings)
	}
	if rep.StaleDuplicates == nil || rep.Parity == nil || rep.SettingsDelta == nil {
		t.Error("a skipped arm left a nil list")
	}
	calls = 0
	full, _ := Full(Options{Path: t.TempDir(), Timeout: time.Second})
	if countFindings(full, "timeout", SevWarn) != 1 {
		t.Errorf("Full after a timeout: %+v", full.Findings)
	}
}
