package onboard

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Settings backups are private (0600) under the default umask, including a
// .bak an older build left world-readable.

func umask022(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

func wantPrivate(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("%s: mode %o, want 600", p, got)
	}
}

func TestToggleBackupIsPrivate(t *testing.T) {
	umask022(t)
	dir := t.TempDir()
	path := writeTestSettings(t, dir, toggleSettings)
	if err := os.WriteFile(path+".bak", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TogglePlugin(dir, "lsp-pack", true); err != nil {
		t.Fatalf("TogglePlugin: %v", err)
	}
	wantPrivate(t, path+".bak")
}

// A .bak that is a symlink is replaced, never written through.
func TestBackupSymlinkIsNotFollowed(t *testing.T) {
	umask022(t)
	dir := t.TempDir()
	path := writeTestSettings(t, dir, toggleSettings)
	target := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := TogglePlugin(dir, "lsp-pack", true); err != nil {
		t.Fatalf("TogglePlugin: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("the symlink target was overwritten: %q", b)
	}
	fi, err := os.Lstat(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error(".bak is still a symlink")
	}
	wantPrivate(t, path+".bak")
}

func TestAttachBackupIsPrivate(t *testing.T) {
	umask022(t)
	dir := buildManagedProject(t)
	if _, err := Detach(DetachConfig{ProjectDir: dir, Slug: "demo", WorkspaceRoot: "/ws", Full: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Attach(AttachConfig{ProjectDir: dir, WorkspaceRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	wantPrivate(t, filepath.Join(dir, ".claude", "settings.json.bak"))
}

func TestGlobalEnableRestoreBackupIsPrivate(t *testing.T) {
	umask022(t)
	dir := t.TempDir()
	p := writeGlobalSettings(t, dir, `{"enabledPlugins":{"core@swarmery":true}}`)
	snap, err := CaptureGlobalEnable(dir, "infra-pack@swarmery")
	if err != nil {
		t.Fatalf("CaptureGlobalEnable: %v", err)
	}
	writeGlobalSettings(t, dir, `{"enabledPlugins":{"core@swarmery":true,"infra-pack@swarmery":true}}`)
	if err := snap.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	wantPrivate(t, p+".bak")
}
