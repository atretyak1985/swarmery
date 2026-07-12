package installer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeLaunchd simulates the launchd domain state behind the Runner
// interface: print fails unless a service was bootstrapped, bootstrap
// registers, bootout deregisters. Every call is recorded.
//
// bootout is asynchronous on real launchd: the dying service can linger in
// the domain for a moment. Two knobs simulate that race:
//   - printGhosts: after a bootout, `print` keeps reporting the service for
//     N more calls before it disappears;
//   - bootstrapEIO: the next N `bootstrap` calls fail with exit 5
//     (Input/output error) before succeeding.
type fakeLaunchd struct {
	registered   bool
	calls        []string
	printGhosts  int // armed into ghost by bootout
	bootstrapEIO int
	ghost        int
}

func (f *fakeLaunchd) Run(name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if name != "launchctl" {
		return "", nil // e.g. ps in status
	}
	switch args[0] {
	case "print":
		if f.registered {
			return "state = running\npid = 4242\n", nil
		}
		if f.ghost > 0 { // stale registration still visible after async bootout
			f.ghost--
			return "state = running\npid = 4242\n", nil
		}
		return "Could not find service", fmt.Errorf("exit status 113")
	case "bootstrap":
		if f.registered {
			return "Bootstrap failed: 5: Input/output error", fmt.Errorf("exit status 5")
		}
		if f.bootstrapEIO > 0 { // domain still tearing the old service down
			f.bootstrapEIO--
			return "Bootstrap failed: 5: Input/output error", fmt.Errorf("exit status 5")
		}
		f.registered = true
		return "", nil
	case "bootout":
		if !f.registered {
			return "No such process", fmt.Errorf("exit status 3")
		}
		f.registered = false
		f.ghost = f.printGhosts
		return "", nil
	default:
		return "", fmt.Errorf("unexpected launchctl subcommand %q", args[0])
	}
}

func (f *fakeLaunchd) count(sub string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "launchctl "+sub) {
			n++
		}
	}
	return n
}

func testSystem(t *testing.T) (*System, *fakeLaunchd, string) {
	t.Helper()
	home := t.TempDir()
	fake := &fakeLaunchd{}
	sys := &System{Home: home, UID: "501", Run: fake, Out: io.Discard,
		Sleep: func(time.Duration) {}} // no real sleeping in tests

	src := filepath.Join(home, "source-binary")
	if err := os.WriteFile(src, []byte("binary v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return sys, fake, src
}

func TestInstallIdempotent(t *testing.T) {
	sys, fake, src := testSystem(t)

	// First install: fresh registration, no bootout.
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !fake.registered {
		t.Fatal("service not registered after first install")
	}
	if got := fake.count("bootstrap"); got != 1 {
		t.Errorf("bootstrap calls after first install = %d, want 1", got)
	}
	if got := fake.count("bootout"); got != 0 {
		t.Errorf("bootout calls after first install = %d, want 0", got)
	}

	// Second install with a newer binary: restart (bootout → bootstrap),
	// still exactly one registered service and one plist.
	if err := os.WriteFile(src, []byte("binary v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !fake.registered {
		t.Fatal("service not registered after second install")
	}
	if got := fake.count("bootstrap"); got != 2 {
		t.Errorf("total bootstrap calls = %d, want 2", got)
	}
	if got := fake.count("bootout"); got != 1 {
		t.Errorf("total bootout calls = %d, want 1 (restart on re-install)", got)
	}

	// The fake errors on bootstrap-while-registered, so reaching here means
	// no duplicate registration was attempted. Verify the file side too.
	agents, err := os.ReadDir(filepath.Join(sys.Home, "Library", "LaunchAgents"))
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name() != Label+".plist" {
		t.Errorf("LaunchAgents contents = %v, want exactly [%s.plist]", agents, Label)
	}
	bin, err := os.ReadFile(sys.BinPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(bin) != "binary v2" {
		t.Errorf("installed binary = %q, want updated %q", bin, "binary v2")
	}
	if fi, _ := os.Stat(sys.BinPath()); fi.Mode().Perm() != 0o755 {
		t.Errorf("binary mode = %v, want 0755", fi.Mode().Perm())
	}
}

// TestReinstallWaitsForAsyncBootout: after bootout the service lingers in the
// domain for a few `print` polls — install must wait it out and bootstrap
// exactly once, without failing.
func TestReinstallWaitsForAsyncBootout(t *testing.T) {
	sys, fake, src := testSystem(t)
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("first install: %v", err)
	}

	fake.printGhosts = 3 // stale registration visible for 3 polls post-bootout
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("reinstall with async bootout: %v", err)
	}
	if !fake.registered {
		t.Fatal("service not registered after reinstall")
	}
	if got := fake.count("bootstrap"); got != 2 {
		t.Errorf("total bootstrap calls = %d, want 2 (poll absorbed the lag, no retries needed)", got)
	}
}

// TestReinstallRetriesBootstrapOnExit5: the reproduced launchd race — bootout
// returns but the immediate bootstrap fails with exit 5 (Input/output error).
// Install must retry with backoff until it succeeds.
func TestReinstallRetriesBootstrapOnExit5(t *testing.T) {
	sys, fake, src := testSystem(t)
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("first install: %v", err)
	}

	fake.bootstrapEIO = 2 // first two bootstrap attempts hit the race
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("reinstall across exit-5 race: %v", err)
	}
	if !fake.registered {
		t.Fatal("service not registered after retried bootstrap")
	}
	// 1 (first install) + 3 (two exit-5 failures, then success).
	if got := fake.count("bootstrap"); got != 4 {
		t.Errorf("total bootstrap calls = %d, want 4 (2 failed attempts + success)", got)
	}
}

// TestInstallBootstrapGivesUpAfterMaxAttempts: a persistent failure must
// surface after the bounded retries, not loop forever.
func TestInstallBootstrapGivesUpAfterMaxAttempts(t *testing.T) {
	sys, fake, src := testSystem(t)
	fake.bootstrapEIO = 100 // never recovers

	err := sys.Install(src, 0)
	if err == nil {
		t.Fatal("install succeeded, want bootstrap failure")
	}
	if !strings.Contains(err.Error(), "after 5 attempts") {
		t.Errorf("error = %v, want mention of retry attempts", err)
	}
	if got := fake.count("bootstrap"); got != 5 {
		t.Errorf("bootstrap calls = %d, want exactly 5 (bounded retries)", got)
	}
	if fake.registered {
		t.Error("service must not be registered after a failed install")
	}
}

func TestInstallWritesPortIntoPlist(t *testing.T) {
	sys, _, src := testSystem(t)
	if err := sys.Install(src, 8899); err != nil {
		t.Fatalf("install: %v", err)
	}
	plist, err := os.ReadFile(sys.PlistPath())
	if err != nil {
		t.Fatal(err)
	}
	want := Plist(sys.BinPath(), sys.LogsDir(), 8899)
	if string(plist) != want {
		t.Errorf("plist on disk does not match generated plist with port\n%s", plist)
	}
	if !strings.Contains(string(plist), "<key>SWARMERY_PORT</key>") {
		t.Error("plist missing SWARMERY_PORT EnvironmentVariables entry")
	}
}

func TestUninstallKeepsLogsAndDB(t *testing.T) {
	sys, fake, src := testSystem(t)
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Simulate daemon artifacts that must survive uninstall.
	if err := os.WriteFile(sys.DBPath(), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(sys.LogsDir(), "swarmery.out.log")
	if err := os.WriteFile(logFile, []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sys.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if fake.registered {
		t.Error("service still registered after uninstall")
	}
	if _, err := os.Stat(sys.PlistPath()); !os.IsNotExist(err) {
		t.Error("plist still present after uninstall")
	}
	for _, keep := range []string{sys.DBPath(), logFile} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s should survive uninstall: %v", keep, err)
		}
	}

	// Uninstalling again is safe (nothing to boot out, plist gone).
	if err := sys.Uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestStatusOutputs(t *testing.T) {
	sys, _, src := testSystem(t)
	var buf strings.Builder
	sys.Out = &buf

	// Not installed.
	if err := sys.Status(); err != nil {
		t.Fatalf("status (not installed): %v", err)
	}
	if !strings.Contains(buf.String(), "not installed") {
		t.Errorf("status before install should say 'not installed', got:\n%s", buf.String())
	}

	// Installed + running (fake reports pid 4242).
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := os.WriteFile(sys.DBPath(), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := sys.Status(); err != nil {
		t.Fatalf("status (installed): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"running", "4242", Version, "2.0 KiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}
