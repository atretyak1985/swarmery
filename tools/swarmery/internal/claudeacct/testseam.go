package claudeacct

import "time"

// SetGitProbeTimeoutForTest replaces the provenance probe's per-invocation git
// timeout and returns a func that restores the previous value. It is for TESTS
// ONLY — production never calls it, and the daemon's bound stays 2 s.
//
// It exists for tests in OTHER packages that reach the probe through a real git
// (dispatch's account resolution, the api's binding discovery): on a loaded
// machine git can take longer than 2 s to start, the probe then reads as
// "unknown", and the binding under test is ignored. A test widens the bound with
//
//	t.Cleanup(claudeacct.SetGitProbeTimeoutForTest(30 * time.Second))
//
// The store is atomic, so goroutines still probing when the restore runs read
// either value, never a torn one.
func SetGitProbeTimeoutForTest(d time.Duration) (restore func()) {
	prev := gitProbeTimeout.Swap(int64(d))
	return func() { gitProbeTimeout.Store(prev) }
}
