package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSystemctl simulates the user manager behind the Runner interface. The
// unit is "loaded" whenever its file exists (what daemon-reload achieves on a
// real manager); enable --now and restart make it active; disable --now stops
// it. Every call is recorded, like fakeLaunchd.
type fakeSystemctl struct {
	unitPath string
	active   bool
	enabled  bool
	restarts int
	calls    []string
}

func (f *fakeSystemctl) loaded() bool {
	_, err := os.Stat(f.unitPath)
	return err == nil
}

func (f *fakeSystemctl) Run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "systemctl" {
		return "", nil // ps in status
	}
	if len(args) < 2 || args[0] != "--user" {
		return "", fmt.Errorf("every call must be `systemctl --user …`, got %v", args)
	}
	switch args[1] {
	case "daemon-reload":
		return "", nil
	case "is-active":
		if f.active {
			return "active\n", nil
		}
		return "inactive\n", fmt.Errorf("exit status 3")
	case "show":
		if !f.loaded() {
			return "LoadState=not-found\nActiveState=inactive\nMainPID=0\nActiveEnterTimestamp=\n", nil
		}
		state, pid := "inactive", "0"
		if f.active {
			state, pid = "active", "4242"
		}
		return fmt.Sprintf("LoadState=loaded\nActiveState=%s\nMainPID=%s\nActiveEnterTimestamp=Sat 2026-09-19 21:00:00 UTC\n", state, pid), nil
	case "enable":
		if !f.loaded() {
			return "Failed to enable unit: Unit file swarmery.service does not exist.", fmt.Errorf("exit status 1")
		}
		f.enabled = true
		if len(args) > 2 && args[2] == "--now" {
			f.active = true
		}
		return "", nil
	case "restart":
		if !f.loaded() {
			return "Failed to restart swarmery.service: Unit swarmery.service not found.", fmt.Errorf("exit status 5")
		}
		f.active = true
		f.restarts++
		return "", nil
	case "disable":
		if !f.loaded() {
			return "Failed to disable unit: Unit file swarmery.service does not exist.", fmt.Errorf("exit status 1")
		}
		f.enabled = false
		if len(args) > 2 && args[2] == "--now" {
			f.active = false
		}
		return "", nil
	default:
		return "", fmt.Errorf("unexpected systemctl verb %q", args[1])
	}
}

func (f *fakeSystemctl) count(verb string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "systemctl --user "+verb) {
			n++
		}
	}
	return n
}

// testSystemd wires a Systemd against a temp home and a fake manager, and
// returns a source binary to install.
func testSystemd(t *testing.T) (*Systemd, *fakeSystemctl, string) {
	t.Helper()
	home := t.TempDir()
	sys := &Systemd{Home: home, Out: &strings.Builder{}}
	fake := &fakeSystemctl{unitPath: sys.UnitPath()}
	sys.Run = fake
	src := filepath.Join(home, "source-binary")
	if err := os.WriteFile(src, []byte("binary v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return sys, fake, src
}

func TestUnitGolden(t *testing.T) {
	const bin = "/home/u/.swarmery/bin/swarmery"
	const logs = "/home/u/.swarmery/logs"
	cases := []struct {
		name   string
		port   int
		extra  []EnvVar
		golden string
	}{
		{"default port omits Environment", 0, nil, "unit_default.golden"},
		{"explicit port writes SWARMERY_PORT", 8899, nil, "unit_with_port.golden"},
		{"extra vars are quoted and %-escaped", 7777, []EnvVar{
			{Key: "SWARMERY_ONBOARD_ROOTS", Value: "/home/u/projects,/srv/repos"},
			{Key: "SWARMERY_STATUSLINE_SRC", Value: `/home/u/50% "quoted" dir`},
		}, "unit_with_env.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			got := Unit(bin, logs, tc.port, tc.extra...)
			if got != string(want) {
				t.Errorf("unit mismatch with %s\n--- got ---\n%s\n--- want ---\n%s", tc.golden, got, want)
			}
		})
	}
}

// What Unit writes, ExistingEnv reads back — including the quoting and the
// doubled `%` — so a bare reinstall preserves exactly what was baked.
func TestUnitEnvRoundTrip(t *testing.T) {
	sys, _, _ := testSystemd(t)
	if err := os.MkdirAll(filepath.Dir(sys.UnitPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []EnvVar{
		{Key: "SWARMERY_ONBOARD_ROOTS", Value: "/home/u/projects,/srv/repos"},
		{Key: "SWARMERY_STATUSLINE_SRC", Value: `/home/u/50% "quoted" dir\x`},
	}
	if err := os.WriteFile(sys.UnitPath(), []byte(Unit(sys.BinPath(), sys.LogsDir(), 8899, env...)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := sys.ExistingEnv()
	want := map[string]string{
		"SWARMERY_PORT":           "8899",
		"SWARMERY_ONBOARD_ROOTS":  "/home/u/projects,/srv/repos",
		"SWARMERY_STATUSLINE_SRC": `/home/u/50% "quoted" dir\x`,
	}
	if len(got) != len(want) {
		t.Fatalf("ExistingEnv = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("ExistingEnv[%s] = %q, want %q", k, got[k], v)
		}
	}
	if sys2 := (&Systemd{Home: t.TempDir()}); sys2.ExistingEnv() != nil {
		t.Error("ExistingEnv without a unit must be nil")
	}
}

func TestSystemdInstallIdempotent(t *testing.T) {
	sys, fake, src := testSystemd(t)

	// First install: reload, enable --now, no restart.
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !fake.active || !fake.enabled {
		t.Fatalf("after first install active=%v enabled=%v, want both", fake.active, fake.enabled)
	}
	if got := fake.count("enable --now"); got != 1 {
		t.Errorf("enable --now calls = %d, want 1", got)
	}
	if fake.restarts != 0 {
		t.Errorf("restarts after first install = %d, want 0", fake.restarts)
	}
	if got := fake.count("daemon-reload"); got != 1 {
		t.Errorf("daemon-reload calls = %d, want 1", got)
	}

	// Second install with a newer binary: the running unit is restarted so it
	// picks up the new inode; still one unit file.
	if err := os.WriteFile(src, []byte("binary v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if fake.restarts != 1 {
		t.Errorf("restarts after re-install = %d, want 1", fake.restarts)
	}
	units, err := os.ReadDir(filepath.Dir(sys.UnitPath()))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Name() != UnitName {
		t.Errorf("unit dir contents = %v, want exactly [%s]", units, UnitName)
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
	// Every manager call was addressed to the USER manager.
	for _, c := range fake.calls {
		if strings.HasPrefix(c, "systemctl") && !strings.HasPrefix(c, "systemctl --user ") {
			t.Errorf("call not scoped to --user: %q", c)
		}
	}
}

func TestSystemdInstallWritesPortIntoUnit(t *testing.T) {
	sys, _, src := testSystemd(t)
	if err := sys.Install(src, 8899, EnvVar{Key: "SWARMERY_ONBOARD_ROOTS", Value: "/srv/repos"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(sys.UnitPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Environment="SWARMERY_PORT=8899"`, `Environment="SWARMERY_ONBOARD_ROOTS=/srv/repos"`, "ExecStart=" + sys.BinPath() + " serve"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("unit lacks %q:\n%s", want, data)
		}
	}
}

func TestSystemdUninstallKeepsLogsAndDB(t *testing.T) {
	sys, fake, src := testSystemd(t)
	if err := sys.Install(src, 0); err != nil {
		t.Fatalf("install: %v", err)
	}
	logFile := filepath.Join(sys.LogsDir(), "swarmery.out.log")
	if err := os.WriteFile(logFile, []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sys.DBPath(), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sys.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if fake.active || fake.enabled {
		t.Errorf("after uninstall active=%v enabled=%v, want neither", fake.active, fake.enabled)
	}
	if _, err := os.Stat(sys.UnitPath()); !os.IsNotExist(err) {
		t.Errorf("unit file should be removed, stat err = %v", err)
	}
	for _, keep := range []string{logFile, sys.DBPath(), sys.BinPath()} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s should survive uninstall: %v", keep, err)
		}
	}
	// A second uninstall is a no-op, not an error.
	var buf strings.Builder
	sys.Out = &buf
	if err := sys.Uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if !strings.Contains(buf.String(), "already absent") {
		t.Errorf("second uninstall should report the unit absent, got:\n%s", buf.String())
	}
}

func TestSystemdStatusOutputs(t *testing.T) {
	sys, _, src := testSystemd(t)
	var buf strings.Builder
	sys.Out = &buf

	if err := sys.Status(); err != nil {
		t.Fatalf("status (not installed): %v", err)
	}
	if !strings.Contains(buf.String(), "not installed") {
		t.Errorf("status before install should say 'not installed', got:\n%s", buf.String())
	}

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
	for _, want := range []string{"service: active", "pid:     4242", Version, "2.0 KiB", "unit:    " + sys.UnitPath()} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

// The GOOS switch: launchd on macOS, systemd --user on Linux, a plain error
// elsewhere — with every input injected so the choice is testable on any host.
func TestNewServiceByGOOS(t *testing.T) {
	out := &strings.Builder{}
	if svc, err := newService("darwin", "/home/u", "501", &fakeLaunchd{}, out); err != nil {
		t.Errorf("darwin: %v", err)
	} else if _, ok := svc.(*System); !ok {
		t.Errorf("darwin: got %T, want *System", svc)
	}
	if svc, err := newService("linux", "/home/u", "1000", &fakeSystemctl{}, out); err != nil {
		t.Errorf("linux: %v", err)
	} else if s, ok := svc.(*Systemd); !ok {
		t.Errorf("linux: got %T, want *Systemd", svc)
	} else if s.UnitPath() != "/home/u/.config/systemd/user/swarmery.service" {
		t.Errorf("linux unit path = %s", s.UnitPath())
	}
	if _, err := newService("windows", "/home/u", "1", &fakeSystemctl{}, out); err == nil {
		t.Error("windows: want an error, got a backend")
	}
	// Both backends agree on the home layout.
	a, _ := newService("darwin", "/home/u", "501", &fakeLaunchd{}, out)
	b, _ := newService("linux", "/home/u", "1000", &fakeSystemctl{}, out)
	if a.(*System).BinPath() != b.(*Systemd).BinPath() || a.(*System).DBPath() != b.(*Systemd).DBPath() {
		t.Error("launchd and systemd backends disagree on the ~/.swarmery layout")
	}
}
