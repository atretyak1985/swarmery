package link

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestTokenPath(t *testing.T) {
	if got, want := TokenPath("/h"), filepath.Join("/h", ".swarmery", "link-token"); got != want {
		t.Errorf("TokenPath = %q, want %q", got, want)
	}
}

func TestEnsureTokenMints0600(t *testing.T) {
	home := t.TempDir()
	tok, err := EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := hex.DecodeString(tok); err != nil || len(b) != tokenBytes {
		t.Errorf("token %d chars, want %d hex bytes (%v)", len(tok), tokenBytes, err)
	}
	if m := modeOf(t, TokenPath(home)); m != 0o600 {
		t.Errorf("token file mode = %o, want 600", m)
	}
	if m := modeOf(t, filepath.Dir(TokenPath(home))); m != 0o700 {
		t.Errorf("fresh .swarmery dir mode = %o, want 700", m)
	}
	// Stable on a second call: the same value, no re-mint.
	again, err := EnsureToken(home)
	if err != nil || again != tok {
		t.Errorf("second EnsureToken = %q, %v; want the same token", again, err)
	}
	// No temp files left behind by the atomic write.
	entries, _ := os.ReadDir(filepath.Dir(TokenPath(home)))
	if len(entries) != 1 {
		t.Errorf(".swarmery holds %d entries, want only link-token", len(entries))
	}
}

func TestEnsureTokenNarrowsOpenFile(t *testing.T) {
	home := t.TempDir()
	path := TokenPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("  existing-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // defeat umask
		t.Fatal(err)
	}
	tok, err := EnsureToken(home)
	if err != nil || tok != "existing-token" {
		t.Fatalf("EnsureToken = %q, %v; want the existing trimmed value", tok, err)
	}
	if m := modeOf(t, path); m != 0o600 {
		t.Errorf("mode after EnsureToken = %o, want 600", m)
	}
	// An existing directory keeps its own mode.
	if m := modeOf(t, filepath.Dir(path)); m != 0o755 {
		t.Errorf("existing dir mode changed to %o", m)
	}
}

func TestEnsureTokenReplacesEmptyFile(t *testing.T) {
	home := t.TempDir()
	path := TokenPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := EnsureToken(home)
	if err != nil || len(tok) != 2*tokenBytes {
		t.Fatalf("EnsureToken on an empty file = %q, %v; want a fresh token", tok, err)
	}
}

func TestEnsureTokenUnreadable(t *testing.T) {
	home := t.TempDir()
	// The token path is a directory: neither readable as a token nor
	// replaceable by rename — the error must surface, not a minted token.
	if err := os.MkdirAll(filepath.Join(TokenPath(home), "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if tok, err := EnsureToken(home); err == nil {
		t.Fatalf("EnsureToken = %q, want an error", tok)
	}
	if _, err := Rotate(home); err == nil {
		t.Fatal("Rotate over a directory: want an error")
	}
}

func TestEnsureTokenUncreatableDir(t *testing.T) {
	home := t.TempDir()
	// <home>/.swarmery is a regular file, so the directory cannot be made.
	if err := os.WriteFile(filepath.Join(home, ".swarmery"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureToken(home); err == nil {
		t.Fatal("want an error when .swarmery is not a directory")
	}
}

func TestRotateChangesToken(t *testing.T) {
	home := t.TempDir()
	old, err := EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Rotate(home)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old {
		t.Fatal("Rotate returned the old token")
	}
	onDisk, err := EnsureToken(home)
	if err != nil || onDisk != fresh {
		t.Errorf("file holds %q (%v), want the rotated token", onDisk, err)
	}
	if m := modeOf(t, TokenPath(home)); m != 0o600 {
		t.Errorf("rotated file mode = %o, want 600", m)
	}
}
